package zstdz3

import (
	"bytes"
	"encoding/binary"
	"io"
)

// The sequence model is the one predictor of Z3-25. It does not imitate any compressor:
// it re-derives, from the plaintext alone, the two things a sequence's match fields
// usually contain no information about, and codes only the deviation.
//
//	match length   the longest extension of the source over the plaintext, bounded by the
//	               block end; coded only when the producer chose a shorter match
//	offset         the rank of the source among the earlier positions that start the same
//	               4 bytes, nearest first, found by walking a hash chain over the
//	               plaintext window; coded as the raw offset value when it is not found
//
// Both are bijections given the plaintext, so nothing is ever guessed: a block whose
// model coding does not decode back to its decisions simply keeps the explicit coding.
// Literal lengths and repeat-offset codes stay explicit.
//
// Sequence record in a model-coded unit: uvarint literal length, then one tag byte:
//
//	0..63      source by rank, match length = longest extension
//	128..191   source by rank (tag-128), then uvarint match length - 3
//	64..66     repeat-offset code 1..3 (tag-63), then uvarint match length - 3
//	255        uvarint offset value, then uvarint match length - 3
const (
	tagRank    = 0
	tagRankML  = 128
	tagRep     = 64
	tagRaw     = 255
	maxRank    = 64
	chainSteps = 64
	minMatch   = 4

	// modelMaxWindow bounds the index memory (4 bytes per window position, rounded up).
	modelMaxWindow = 16 << 20
)

type matchModel struct {
	window int64
	hbits  uint
	head   []uint32 // hash -> position+1 (frame-relative, wraps at 2^32)
	prev   []uint32 // ring: position & mask -> previous position+1 with the same hash
	mask   uint32
	ins    int64 // positions < ins have been indexed
}

// newMatchModel returns nil when the window is too large for the model; such frames
// keep the explicit coding.
func newMatchModel(window int64) *matchModel {
	if window <= 0 || window > modelMaxWindow {
		return nil
	}
	hb := uint(10)
	for int64(1)<<hb < window && hb < 22 {
		hb++
	}
	size := uint32(1)
	for int64(size) < window+2*maxBlock {
		size <<= 1
	}
	return &matchModel{window: window, hbits: hb, head: make([]uint32, 1<<hb), prev: make([]uint32, size), mask: size - 1}
}

func (m *matchModel) hash(b []byte) uint32 {
	return (binary.LittleEndian.Uint32(b) * 2654435761) >> (32 - m.hbits)
}

// blockView is the plaintext a block's sequences are coded against: buf holds the
// frame's recent output ending with the block, base is the frame offset of buf[0].
type blockView struct {
	buf        []byte
	base       int64
	start, end int64 // frame offsets of the block
}

// advance indexes every position below limit whose four bytes are available.
func (m *matchModel) advance(v *blockView, limit int64) {
	if lo := limit - m.window - 1; m.ins < lo {
		m.ins = lo
	}
	if m.ins < v.base {
		m.ins = v.base
	}
	last := v.end - 3 // positions p with p+4 <= end
	if limit > last {
		limit = last
	}
	for ; m.ins < limit; m.ins++ {
		i := m.ins - v.base
		h := m.hash(v.buf[i:])
		m.prev[uint32(m.ins)&m.mask] = m.head[h]
		m.head[h] = uint32(m.ins) + 1
	}
}

// walk visits the chain of earlier positions with the same hash as q, nearest first,
// calling fn for those that share at least minMatch bytes with q; fn returns true to
// stop. At most chainSteps links are followed.
func (m *matchModel) walk(v *blockView, q int64, fn func(src int64) bool) {
	if q+minMatch > v.end {
		return
	}
	cur := v.buf[q-v.base : v.end-v.base]
	p := m.head[m.hash(cur)]
	last := q
	for steps := 0; p != 0 && steps < chainSteps; steps++ {
		src := q - int64(uint32(q)-(p-1))
		if src >= last || q-src > m.window || src < v.base {
			return
		}
		if lce(v.buf[src-v.base:], cur) >= minMatch && fn(src) {
			return
		}
		last = src
		p = m.prev[uint32(src)&m.mask]
	}
}

func lce(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i+8 <= n && binary.LittleEndian.Uint64(a[i:]) == binary.LittleEndian.Uint64(b[i:]) {
		i += 8
	}
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// encode codes the block's sequences. Every record is decoded again right away, with
// the index in exactly the state a decoder would have, and ok is false if any record
// does not come back identical (the block then keeps the explicit coding).
func (m *matchModel) encode(v *blockView, trip []seqRec) (out []byte, ok bool) {
	pos := v.start
	var rec []byte
	for _, t := range trip {
		rec = m.encodeOne(rec[:0], v, pos, t)
		back, _, err := m.decodeOne(bytes.NewReader(rec), v, pos)
		if err != nil || back != t {
			return nil, false
		}
		out = append(out, rec...)
		pos += int64(t.ll) + int64(t.ml)
	}
	return out, true
}

func (m *matchModel) encodeOne(out []byte, v *blockView, pos int64, t seqRec) []byte {
	q := pos + int64(t.ll)
	out = binary.AppendUvarint(out, uint64(t.ll))
	m.advance(v, q)
	if t.of <= 3 {
		out = append(out, byte(tagRep+t.of-1))
		return binary.AppendUvarint(out, uint64(t.ml-3))
	}
	src := q - int64(t.of-3)
	rank, found := 0, false
	m.walk(v, q, func(s int64) bool {
		if s == src {
			found = true
			return true
		}
		rank++
		return rank >= maxRank
	})
	if !found {
		out = append(out, tagRaw)
		out = binary.AppendUvarint(out, uint64(t.of))
		return binary.AppendUvarint(out, uint64(t.ml-3))
	}
	if int(t.ml) == lce(v.buf[src-v.base:], v.buf[q-v.base:v.end-v.base]) {
		return append(out, byte(tagRank+rank))
	}
	out = append(out, byte(tagRankML+rank))
	return binary.AppendUvarint(out, uint64(t.ml-3))
}

// decode reads n sequences of a model-coded unit.
func (m *matchModel) decode(r io.ByteReader, v *blockView, n int) ([]seqRec, error) {
	trip := make([]seqRec, n)
	pos := v.start
	for i := range trip {
		t, _, err := m.decodeOne(r, v, pos)
		if err != nil {
			return nil, err
		}
		trip[i] = t
		pos += int64(t.ll) + int64(t.ml)
	}
	return trip, nil
}

func (m *matchModel) decodeOne(r io.ByteReader, v *blockView, pos int64) (seqRec, int64, error) {
	ll, err := readMinimalUvarint(r)
	if err != nil {
		return seqRec{}, 0, err
	}
	tag, err := r.ReadByte()
	if err != nil {
		return seqRec{}, 0, badIR("truncated unit stream")
	}
	if ll > maxBlock {
		return seqRec{}, 0, badIR("literal length %d", ll)
	}
	q := pos + int64(ll)
	var of uint32
	var ml uint64
	explicitML := true
	switch {
	case tag >= tagRep && tag <= tagRep+2:
		of = uint32(tag-tagRep) + 1
	case tag == tagRaw:
		ov, err := readMinimalUvarint(r)
		if err != nil {
			return seqRec{}, 0, err
		}
		if ov == 0 || ov > 1<<32-1 {
			return seqRec{}, 0, badIR("offset value %d", ov)
		}
		of = uint32(ov)
	case tag < maxRank || tag >= tagRankML && tag < tagRankML+maxRank:
		rank := int(tag & 63)
		explicitML = tag >= tagRankML
		m.advance(v, q)
		src, k := int64(-1), 0
		m.walk(v, q, func(s int64) bool {
			if k == rank {
				src = s
				return true
			}
			k++
			return false
		})
		if src < 0 {
			return seqRec{}, 0, badIR("rank %d beyond the candidates", rank)
		}
		of = uint32(q-src) + 3
		if !explicitML {
			ml = uint64(lce(v.buf[src-v.base:], v.buf[q-v.base:v.end-v.base]))
			if ml < 3 {
				return seqRec{}, 0, badIR("match length %d", ml)
			}
		}
	default:
		return seqRec{}, 0, badIR("sequence tag %d", tag)
	}
	if explicitML {
		x, err := readMinimalUvarint(r)
		if err != nil {
			return seqRec{}, 0, err
		}
		ml = x + 3
	}
	if ml > maxBlock {
		return seqRec{}, 0, badIR("match length %d", ml)
	}
	return seqRec{ll: uint32(ll), ml: uint32(ml), of: of}, q + int64(ml), nil
}
