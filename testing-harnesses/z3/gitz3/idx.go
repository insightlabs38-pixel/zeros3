package gitz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"sort"
)

// Idx is a parsed pack index v2.
type Idx struct {
	Algo   Algo
	Data   []byte
	N      int
	Fanout [256]uint32
	// section offsets
	idsOff, crcOff, offOff, big, bigN int
	PackSum, IdxSum                   []byte
}

const idxHdr = 8 + 256*4

// ParseIdx parses and validates a v2 pack index: magic/version, fanout
// monotonicity, exact length (including the 64-bit offset table), strictly
// sorted ids consistent with the fanout, bounded 64-bit offset references, and
// the index checksum.
func ParseIdx(data []byte, a Algo, lim Limits) (*Idx, error) {
	lim = lim.norm()
	hs := a.Size()
	if len(data) < idxHdr+2*hs {
		return nil, corrupt("idx shorter than fanout+trailer")
	}
	if !bytes.Equal(data[:4], []byte{0xff, 't', 'O', 'c'}) {
		return nil, ErrUnsupported // v1 idx has no magic; handled by ParseIdxV1
	}
	if binary.BigEndian.Uint32(data[4:]) != 2 {
		return nil, ErrUnsupported
	}
	x := &Idx{Algo: a, Data: data}
	prev := uint32(0)
	for i := 0; i < 256; i++ {
		v := binary.BigEndian.Uint32(data[8+4*i:])
		if v < prev {
			return nil, corrupt("fanout not monotonic at %d", i)
		}
		x.Fanout[i], prev = v, v
	}
	if uint64(prev) > uint64(lim.MaxObjects) {
		return nil, limit("idx object count %d", prev)
	}
	n := int(prev)
	x.N = n
	x.idsOff = idxHdr
	x.crcOff = x.idsOff + n*hs
	x.offOff = x.crcOff + 4*n
	x.big = x.offOff + 4*n
	rest := len(data) - 2*hs - x.big
	if rest < 0 || rest%8 != 0 {
		return nil, corrupt("idx length")
	}
	x.bigN = rest / 8
	maxRef := -1
	for i := 0; i < n; i++ {
		o := binary.BigEndian.Uint32(data[x.offOff+4*i:])
		if o&0x80000000 != 0 {
			if r := int(o & 0x7fffffff); r > maxRef {
				maxRef = r
			}
		}
	}
	if maxRef >= x.bigN {
		return nil, corrupt("64-bit offset reference %d of %d", maxRef, x.bigN)
	}
	var last []byte
	for i := 0; i < n; i++ {
		id := data[x.idsOff+i*hs : x.idsOff+(i+1)*hs]
		if last != nil && bytes.Compare(last, id) >= 0 {
			return nil, corrupt("ids not strictly sorted at %d", i)
		}
		last = id
	}
	// fanout consistency: fanout[b] = number of ids whose first byte <= b
	pos := 0
	for b := 0; b < 256; b++ {
		for pos < n && int(data[x.idsOff+pos*hs]) <= b {
			pos++
		}
		if uint32(pos) != x.Fanout[b] {
			return nil, corrupt("fanout[%d]=%d, ids say %d", b, x.Fanout[b], pos)
		}
	}
	x.PackSum = data[len(data)-2*hs : len(data)-hs]
	x.IdxSum = data[len(data)-hs:]
	if !bytes.Equal(a.Sum(data[:len(data)-hs]), x.IdxSum) {
		return nil, ErrChecksum
	}
	return x, nil
}

// ID returns the i'th (sorted) id.
func (x *Idx) ID(i int) []byte {
	hs := x.Algo.Size()
	return x.Data[x.idsOff+i*hs : x.idsOff+(i+1)*hs]
}

// Offset returns the pack offset of the i'th (sorted) id.
func (x *Idx) Offset(i int) int64 {
	o := binary.BigEndian.Uint32(x.Data[x.offOff+4*i:])
	if o&0x80000000 == 0 {
		return int64(o)
	}
	r := int(o & 0x7fffffff)
	return int64(binary.BigEndian.Uint64(x.Data[x.big+8*r:]))
}

// CRC returns the stored CRC-32 of the i'th (sorted) id's pack entry.
func (x *Idx) CRC(i int) uint32 { return binary.BigEndian.Uint32(x.Data[x.crcOff+4*i:]) }

// LookupStats counts what a lookup touched.
type LookupStats struct {
	Probes  int
	Touched int // bytes read from the index
}

// Lookup finds id with the fanout table and a binary search over the sorted
// ids. It returns the sorted position or -1.
func (x *Idx) Lookup(id []byte) (int, LookupStats) {
	var st LookupStats
	hs := x.Algo.Size()
	if len(id) != hs {
		return -1, st
	}
	b := int(id[0])
	lo := 0
	if b > 0 {
		lo = int(x.Fanout[b-1])
		st.Touched += 4
	}
	hi := int(x.Fanout[b])
	st.Touched += 4
	for lo < hi {
		m := (lo + hi) / 2
		c := bytes.Compare(x.ID(m), id)
		st.Probes++
		st.Touched += hs
		switch {
		case c == 0:
			st.Touched += 4 + 4 // CRC and 32-bit offset (the CRC is read only to verify; counted)
			return m, st
		case c < 0:
			lo = m + 1
		default:
			hi = m
		}
	}
	return -1, st
}

// BuildIdx regenerates the v2 index of a resolved pack. For packs produced by
// Git the bytes are expected to be identical to the shipped .idx (they are a
// function of the pack); the laboratory verifies that per instance.
func BuildIdx(p *Pack) ([]byte, error) {
	if !p.resolved {
		return nil, ErrUnsupported
	}
	hs := p.Algo.Size()
	order := make([]int, len(p.Entries))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return bytes.Compare(p.Entries[order[a]].ID, p.Entries[order[b]].ID) < 0 })
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 't', 'O', 'c', 0, 0, 0, 2})
	var fan [256]uint32
	for _, i := range order {
		fan[p.Entries[i].ID[0]]++
	}
	var acc uint32
	for b := 0; b < 256; b++ {
		acc += fan[b]
		binary.Write(&buf, binary.BigEndian, acc)
	}
	for _, i := range order {
		buf.Write(p.Entries[i].ID)
	}
	for _, i := range order {
		binary.Write(&buf, binary.BigEndian, p.Entries[i].CRC)
	}
	var big []uint64
	for _, i := range order {
		o := uint64(p.Entries[i].Off)
		if o < 0x80000000 {
			binary.Write(&buf, binary.BigEndian, uint32(o))
		} else {
			binary.Write(&buf, binary.BigEndian, uint32(0x80000000|len(big)))
			big = append(big, o)
		}
	}
	for _, o := range big {
		binary.Write(&buf, binary.BigEndian, o)
	}
	buf.Write(p.Trailer)
	buf.Write(p.Algo.Sum(buf.Bytes()))
	_ = hs
	return buf.Bytes(), nil
}

// CheckAgainstPack verifies that the index describes exactly this pack:
// matching pack checksum, object set, offsets and CRCs (Pack must be resolved).
func (x *Idx) CheckAgainstPack(p *Pack) error {
	if !p.resolved {
		return ErrUnsupported
	}
	if x.N != len(p.Entries) || !bytes.Equal(x.PackSum, p.Trailer) {
		return ErrMismatch
	}
	for i := 0; i < x.N; i++ {
		j := p.IndexOfID(x.ID(i))
		if j < 0 || p.Entries[j].Off != x.Offset(i) || p.Entries[j].CRC != x.CRC(i) {
			return ErrMismatch
		}
	}
	return nil
}

var _ = crc32.IEEE

// ParseIdxV1 validates a version-1 pack index (no magic): 256 fanout words, then
// N records of (u32 offset, id), then the pack and index checksums. It exists to
// characterise the older form, which carries no CRCs and only 31-bit offsets.
func ParseIdxV1(data []byte, a Algo, lim Limits) (ids [][]byte, offsets []uint32, err error) {
	lim = lim.norm()
	hs := a.Size()
	if len(data) < 256*4+2*hs {
		return nil, nil, corrupt("v1 idx shorter than fanout+trailer")
	}
	var prev uint32
	for i := 0; i < 256; i++ {
		v := binary.BigEndian.Uint32(data[4*i:])
		if v < prev {
			return nil, nil, corrupt("fanout not monotonic")
		}
		prev = v
	}
	n := int(prev)
	if n > lim.MaxObjects || len(data) != 256*4+n*(4+hs)+2*hs {
		return nil, nil, corrupt("v1 idx length for %d objects", n)
	}
	var last []byte
	for i := 0; i < n; i++ {
		rec := data[1024+i*(4+hs):]
		id := rec[4 : 4+hs]
		if last != nil && bytes.Compare(last, id) >= 0 {
			return nil, nil, corrupt("ids not strictly sorted")
		}
		last = id
		ids = append(ids, id)
		offsets = append(offsets, binary.BigEndian.Uint32(rec))
	}
	if !bytes.Equal(a.Sum(data[:len(data)-hs]), data[len(data)-hs:]) {
		return nil, nil, ErrChecksum
	}
	return ids, offsets, nil
}

// BuildIdxV1 regenerates a v1 index of a resolved pack.
func BuildIdxV1(p *Pack) ([]byte, error) {
	if !p.resolved {
		return nil, ErrUnsupported
	}
	order := make([]int, len(p.Entries))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return bytes.Compare(p.Entries[order[a]].ID, p.Entries[order[b]].ID) < 0 })
	var buf bytes.Buffer
	var fan [256]uint32
	for _, i := range order {
		fan[p.Entries[i].ID[0]]++
	}
	var acc uint32
	for b := 0; b < 256; b++ {
		acc += fan[b]
		binary.Write(&buf, binary.BigEndian, acc)
	}
	for _, i := range order {
		if p.Entries[i].Off >= 1<<31 {
			return nil, ErrUnsupported
		}
		binary.Write(&buf, binary.BigEndian, uint32(p.Entries[i].Off))
		buf.Write(p.Entries[i].ID)
	}
	buf.Write(p.Trailer)
	buf.Write(p.Algo.Sum(buf.Bytes()))
	return buf.Bytes(), nil
}

// FallbackReason names why an optimisation input was left to ordinary CDC.
func FallbackReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrThin):
		return "thin-or-unresolved-delta"
	case errors.Is(err, ErrChecksum):
		return "checksum"
	case errors.Is(err, ErrLimit):
		return "limit"
	case errors.Is(err, ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrMismatch):
		return "mismatch"
	case errors.Is(err, ErrCorrupt):
		return "malformed"
	}
	return "other"
}
