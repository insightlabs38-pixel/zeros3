package gitz3

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sort"
)

// Idx is a parsed pack index v2. Only the fanout, the section geometry and the
// two trailer checksums are resident; ids, CRCs and offsets are read on demand
// from the source in bounded pieces and never beyond Size.
type Idx struct {
	Algo   Algo
	N      int
	Fanout [256]uint32
	Size   int64
	src    io.ReaderAt
	err    error // first I/O error met by a convenience accessor
	// section offsets
	idsOff, crcOff, offOff, big int64
	bigN                        int64
	PackSum, IdxSum             []byte
}

const idxHdr = 8 + 256*4

// idxBuf is the fixed streaming window of ParseIdxAt.
const idxBuf = 64 << 10

// ParseIdx parses a byte-backed v2 index; it is a thin wrapper over ParseIdxAt.
func ParseIdx(data []byte, a Algo, lim Limits) (*Idx, error) {
	return ParseIdxAt(bytes.NewReader(data), int64(len(data)), a, lim)
}

// ParseIdxAt parses and validates the v2 pack index of the size bytes readable
// from r: magic/version, fanout monotonicity, exact length (including the 64-bit
// offset table), strictly sorted ids consistent with the fanout, bounded 64-bit
// offset references, and the index checksum. It streams through fixed windows,
// never reads past size, and allocates nothing from a declared count. A source
// shorter than size is ErrCorrupt.
func ParseIdxAt(r io.ReaderAt, size int64, a Algo, lim Limits) (*Idx, error) {
	lim = lim.norm()
	hs := int64(a.Size())
	if size < idxHdr+2*hs {
		return nil, corrupt("idx shorter than fanout+trailer")
	}
	src := io.NewSectionReader(r, 0, size)
	var hdr [idxHdr]byte
	if err := readFullAt(src, hdr[:], 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(hdr[:4], []byte{0xff, 't', 'O', 'c'}) {
		return nil, ErrUnsupported // v1 idx has no magic; handled by ParseIdxV1
	}
	if binary.BigEndian.Uint32(hdr[4:]) != 2 {
		return nil, ErrUnsupported
	}
	x := &Idx{Algo: a, Size: size, src: src}
	prev := uint32(0)
	for i := 0; i < 256; i++ {
		v := binary.BigEndian.Uint32(hdr[8+4*i:])
		if v < prev {
			return nil, corrupt("fanout not monotonic at %d", i)
		}
		x.Fanout[i], prev = v, v
	}
	if uint64(prev) > uint64(lim.MaxObjects) {
		return nil, limit("idx object count %d", prev)
	}
	n := int64(prev)
	x.N = int(n)
	// n <= MaxObjects, so these products cannot overflow int64.
	x.idsOff = idxHdr
	x.crcOff = x.idsOff + n*hs
	x.offOff = x.crcOff + 4*n
	x.big = x.offOff + 4*n
	rest := size - 2*hs - x.big
	if rest < 0 || rest%8 != 0 {
		return nil, corrupt("idx length")
	}
	x.bigN = rest / 8
	buf := make([]byte, idxBuf)
	// offsets: every 64-bit reference must land inside the 64-bit table
	maxRef := int64(-1)
	for done := int64(0); done < n; {
		m := min(n-done, idxBuf/4)
		if err := readFullAt(src, buf[:4*m], x.offOff+4*done); err != nil {
			return nil, err
		}
		for i := int64(0); i < m; i++ {
			if o := binary.BigEndian.Uint32(buf[4*i:]); o&0x80000000 != 0 {
				maxRef = max(maxRef, int64(o&0x7fffffff))
			}
		}
		done += m
	}
	if maxRef >= x.bigN {
		return nil, corrupt("64-bit offset reference %d of %d", maxRef, x.bigN)
	}
	// ids: strictly sorted, consistent with the fanout
	last := make([]byte, hs)
	pos, b := int64(0), 0
	for done := int64(0); done < n; {
		m := min(n-done, idxBuf/hs)
		if err := readFullAt(src, buf[:m*hs], x.idsOff+done*hs); err != nil {
			return nil, err
		}
		for i := int64(0); i < m; i++ {
			id := buf[i*hs : (i+1)*hs]
			if done+i > 0 && bytes.Compare(last, id) >= 0 {
				return nil, corrupt("ids not strictly sorted at %d", done+i)
			}
			copy(last, id)
			// fanout[c] = number of ids whose first byte <= c
			for b < int(id[0]) {
				if int64(x.Fanout[b]) != pos {
					return nil, corrupt("fanout[%d]=%d, ids say %d", b, x.Fanout[b], pos)
				}
				b++
			}
			pos++
		}
		done += m
	}
	for ; b < 256; b++ {
		if int64(x.Fanout[b]) != pos {
			return nil, corrupt("fanout[%d]=%d, ids say %d", b, x.Fanout[b], pos)
		}
	}
	x.PackSum = make([]byte, hs)
	x.IdxSum = make([]byte, hs)
	if err := readFullAt(src, x.PackSum, size-2*hs); err != nil {
		return nil, err
	}
	if err := readFullAt(src, x.IdxSum, size-hs); err != nil {
		return nil, err
	}
	h := a.New()
	if got, err := io.CopyBuffer(h, io.NewSectionReader(src, 0, size-hs), buf); err != nil {
		return nil, err
	} else if got != size-hs {
		return nil, corrupt("source ended at %d of %d bytes", got, size-hs)
	}
	if !bytes.Equal(h.Sum(nil), x.IdxSum) {
		return nil, ErrChecksum
	}
	return x, nil
}

// Err returns the first I/O error met by ID, Offset, CRC or Lookup (which return
// zero values on error so that byte-backed callers keep their shape).
func (x *Idx) Err() error { return x.err }

func (x *Idx) fail(err error) { x.err = cmp.Or(x.err, err) }

func (x *Idx) check(i int) error {
	if i < 0 || i >= x.N {
		return corrupt("idx position %d of %d", i, x.N)
	}
	return nil
}

// ReadID reads the i'th (sorted) id.
func (x *Idx) ReadID(i int) ([]byte, error) {
	if err := x.check(i); err != nil {
		return nil, err
	}
	hs := int64(x.Algo.Size())
	id := make([]byte, hs)
	return id, readFullAt(x.src, id, x.idsOff+int64(i)*hs)
}

// ReadOffset reads the pack offset of the i'th (sorted) id, following the
// 64-bit table when the 32-bit slot refers to it.
func (x *Idx) ReadOffset(i int) (int64, error) {
	if err := x.check(i); err != nil {
		return 0, err
	}
	var w [8]byte
	if err := readFullAt(x.src, w[:4], x.offOff+4*int64(i)); err != nil {
		return 0, err
	}
	o := binary.BigEndian.Uint32(w[:4])
	if o&0x80000000 == 0 {
		return int64(o), nil
	}
	r := int64(o & 0x7fffffff)
	if r >= x.bigN {
		return 0, corrupt("64-bit offset reference %d of %d", r, x.bigN)
	}
	if err := readFullAt(x.src, w[:], x.big+8*r); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(w[:])), nil
}

// ReadCRC reads the stored CRC-32 of the i'th (sorted) id's pack entry.
func (x *Idx) ReadCRC(i int) (uint32, error) {
	if err := x.check(i); err != nil {
		return 0, err
	}
	var w [4]byte
	err := readFullAt(x.src, w[:], x.crcOff+4*int64(i))
	return binary.BigEndian.Uint32(w[:]), err
}

// ID returns the i'th (sorted) id (nil on error; see Err).
func (x *Idx) ID(i int) []byte {
	id, err := x.ReadID(i)
	if err != nil {
		x.fail(err)
		return nil
	}
	return id
}

// Offset returns the pack offset of the i'th (sorted) id (0 on error; see Err).
func (x *Idx) Offset(i int) int64 {
	o, err := x.ReadOffset(i)
	x.fail(err)
	return o
}

// CRC returns the stored CRC-32 of the i'th (sorted) id's pack entry (0 on
// error; see Err).
func (x *Idx) CRC(i int) uint32 {
	c, err := x.ReadCRC(i)
	x.fail(err)
	return c
}

// LookupStats counts what a lookup touched.
type LookupStats struct {
	Probes  int
	Touched int // bytes read from the index
}

// Lookup finds id with the fanout table and a binary search over the sorted
// ids. It returns the sorted position or -1 (also on an I/O error; see Err).
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
		mid, err := x.ReadID(m)
		if err != nil {
			x.fail(err)
			return -1, st
		}
		c := bytes.Compare(mid, id)
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
		id, err := x.ReadID(i)
		if err != nil {
			return err
		}
		off, err := x.ReadOffset(i)
		if err != nil {
			return err
		}
		crc, err := x.ReadCRC(i)
		if err != nil {
			return err
		}
		j := p.IndexOfID(id)
		if j < 0 || p.Entries[j].Off != off || p.Entries[j].CRC != crc {
			return ErrMismatch
		}
	}
	return nil
}

var _ = crc32.IEEE

// ParseIdxV1 is byte-backed and characterization-only (v1 is the legacy form; it
// was not converted to ReaderAt). It validates a version-1 pack index (no magic): 256 fanout words, then
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
