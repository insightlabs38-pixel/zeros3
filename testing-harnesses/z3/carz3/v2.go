package carz3

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"
)

// CARv2 wire layout: an 11-byte pragma, a 40-byte header, optional padding,
// the CARv1 payload, optional padding, then an optional index to end of file.
const (
	V2PragmaLen = 11
	V2HeaderLen = 40
	v2Fixed     = V2PragmaLen + V2HeaderLen
	// MaxPad bounds the padding the lab keeps as exact bytes.
	MaxPad = 1 << 20
)

// V2Pragma is the fixed CARv2 pragma: a CARv1 header of {"version": 2}.
var V2Pragma = []byte{0x0a, 0xa1, 0x67, 'v', 'e', 'r', 's', 'i', 'o', 'n', 0x02}

// Index multicodecs.
const (
	IndexSortedCodec          = 0x0400
	MultihashIndexSortedCodec = 0x0401
)

// V2 is the exact wrapper layout of a CARv2 file.
type V2 struct {
	HeaderRaw   [V2HeaderLen]byte
	DataOffset  uint64
	DataSize    uint64
	IndexOffset uint64
	PadBefore   []byte // [51, DataOffset)
	PadAfter    []byte // [DataOffset+DataSize, IndexOffset or file end)
	IndexRaw    []byte // [IndexOffset, file end); nil when there is no index
}

// IsV2 reports whether b begins with the CARv2 pragma.
func IsV2(b []byte) bool { return bytes.HasPrefix(b, V2Pragma) }

// ParseV2 validates the wrapper of the CARv2 file src (size bytes) and keeps
// every non-payload byte exactly. The CARv1 payload is [DataOffset,
// DataOffset+DataSize) of the file.
func ParseV2(src io.ReaderAt, size int64) (*V2, error) {
	if size < v2Fixed {
		return nil, malformed("CARv2 shorter than pragma+header")
	}
	var fixed [v2Fixed]byte
	if _, err := src.ReadAt(fixed[:], 0); err != nil {
		return nil, malformed("reading CARv2 header: %v", err)
	}
	if !bytes.Equal(fixed[:V2PragmaLen], V2Pragma) {
		return nil, malformed("not a CARv2 pragma")
	}
	v := &V2{}
	copy(v.HeaderRaw[:], fixed[V2PragmaLen:])
	h := v.HeaderRaw[:]
	v.DataOffset = binary.LittleEndian.Uint64(h[16:])
	v.DataSize = binary.LittleEndian.Uint64(h[24:])
	v.IndexOffset = binary.LittleEndian.Uint64(h[32:])
	sz := uint64(size)
	if v.DataOffset < v2Fixed || v.DataOffset > sz || v.DataSize > sz-v.DataOffset {
		return nil, malformed("CARv2 data range [%d,+%d) outside file of %d", v.DataOffset, v.DataSize, size)
	}
	dataEnd := v.DataOffset + v.DataSize
	tailEnd := sz
	if v.IndexOffset != 0 {
		if v.IndexOffset < dataEnd || v.IndexOffset > sz {
			return nil, malformed("CARv2 index offset %d outside [%d,%d]", v.IndexOffset, dataEnd, sz)
		}
		tailEnd = v.IndexOffset
	}
	if v.DataOffset-v2Fixed > MaxPad || tailEnd-dataEnd > MaxPad {
		return nil, unsupported("CARv2 padding over %d bytes", MaxPad)
	}
	var err error
	if v.PadBefore, err = readAt(src, v2Fixed, int64(v.DataOffset-v2Fixed)); err != nil {
		return nil, err
	}
	if v.PadAfter, err = readAt(src, int64(dataEnd), int64(tailEnd-dataEnd)); err != nil {
		return nil, err
	}
	if v.IndexOffset != 0 {
		if sz-v.IndexOffset > 1<<30 {
			return nil, unsupported("CARv2 index over 1 GiB")
		}
		if v.IndexRaw, err = readAt(src, int64(v.IndexOffset), int64(sz-v.IndexOffset)); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func readAt(src io.ReaderAt, off, n int64) ([]byte, error) {
	b := make([]byte, n)
	if n == 0 {
		return b, nil
	}
	if _, err := src.ReadAt(b, off); err != nil && !(err == io.EOF && int64(len(b)) == n) {
		return nil, malformed("short read: %v", err)
	}
	return b, nil
}

// DeriveHeader recomputes the 40-byte header from the wrapper pieces and the
// payload size; characteristics are taken from the original.
func DeriveHeader(char []byte, padBefore int, dataSize int64, padAfter int, hasIndex bool) [V2HeaderLen]byte {
	var h [V2HeaderLen]byte
	copy(h[:16], char)
	do := uint64(v2Fixed + padBefore)
	binary.LittleEndian.PutUint64(h[16:], do)
	binary.LittleEndian.PutUint64(h[24:], uint64(dataSize))
	if hasIndex {
		binary.LittleEndian.PutUint64(h[32:], do+uint64(dataSize)+uint64(padAfter))
	}
	return h
}

// IndexRec is one (multihash digest, section offset) index record. Mh is the
// multihash code for MultihashIndexSorted and 0 for IndexSorted.
type IndexRec struct {
	Mh     uint64
	Digest []byte
	Offset uint64
}

// Index is a parsed sorted CARv2 index. Offsets are relative to the start of
// the CARv1 payload and point at the section's length varint.
type Index struct {
	Codec uint64
	Recs  []IndexRec
}

// ParseIndex parses an IndexSorted (0x0400) or MultihashIndexSorted (0x0401)
// index. ok is false for any other codec (kept as opaque bytes by callers).
// The whole input must be consumed exactly.
func ParseIndex(raw []byte) (idx *Index, ok bool, err error) {
	codec, n, err := Uvarint(raw)
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, malformed("empty index")
	}
	if codec != IndexSortedCodec && codec != MultihashIndexSortedCodec {
		return nil, false, nil
	}
	idx = &Index{Codec: codec}
	p := n
	u32 := func() (uint32, bool) {
		if len(raw)-p < 4 {
			return 0, false
		}
		v := binary.LittleEndian.Uint32(raw[p:])
		p += 4
		return v, true
	}
	u64 := func() (uint64, bool) {
		if len(raw)-p < 8 {
			return 0, false
		}
		v := binary.LittleEndian.Uint64(raw[p:])
		p += 8
		return v, true
	}
	multi := func(mh uint64) error {
		nw, ok := u32()
		if !ok || int(nw) < 0 || int64(nw) > int64(len(raw)) {
			return malformed("index width count")
		}
		for i := uint32(0); i < nw; i++ {
			w, ok1 := u32()
			dl, ok2 := u64()
			if !ok1 || !ok2 || w < 8 || w-8 > MaxDigest || dl%uint64(w) != 0 || dl > uint64(len(raw)-p) {
				return malformed("index bucket header")
			}
			for q := uint64(0); q < dl; q += uint64(w) {
				idx.Recs = append(idx.Recs, IndexRec{Mh: mh, Digest: raw[p : p+int(w)-8], Offset: binary.LittleEndian.Uint64(raw[p+int(w)-8:])})
				p += int(w)
			}
		}
		return nil
	}
	if codec == IndexSortedCodec {
		if err := multi(0); err != nil {
			return nil, false, err
		}
	} else {
		nm, ok := u32()
		if !ok || int64(nm) > int64(len(raw)) {
			return nil, false, malformed("index multihash count")
		}
		for i := uint32(0); i < nm; i++ {
			mh, ok := u64()
			if !ok {
				return nil, false, malformed("index multihash code")
			}
			if err := multi(mh); err != nil {
				return nil, false, err
			}
		}
	}
	if p != len(raw) {
		return nil, false, malformed("trailing bytes after index")
	}
	return idx, true, nil
}

// BuildIndex encodes recs in the canonical order the lab expects from
// go-car-style writers: multihash codes ascending, then widths ascending, then
// digests bytewise (ties by offset). Whether that reproduces a given file's
// index is an empirical question the callers check byte for byte.
func BuildIndex(codec uint64, recs []IndexRec) []byte {
	rs := append([]IndexRec(nil), recs...)
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Mh != b.Mh {
			return a.Mh < b.Mh
		}
		if len(a.Digest) != len(b.Digest) {
			return len(a.Digest) < len(b.Digest)
		}
		if c := bytes.Compare(a.Digest, b.Digest); c != 0 {
			return c < 0
		}
		return a.Offset < b.Offset
	})
	out := AppendUvarint(nil, codec)
	le32 := func(v uint32) { out = binary.LittleEndian.AppendUint32(out, v) }
	le64 := func(v uint64) { out = binary.LittleEndian.AppendUint64(out, v) }
	// group: runs of the same mh, within them runs of the same width.
	type run struct{ lo, hi int }
	groups := func(rs []IndexRec, key func(IndexRec) uint64) []run {
		var out []run
		for i := 0; i < len(rs); {
			j := i
			for j < len(rs) && key(rs[j]) == key(rs[i]) {
				j++
			}
			out = append(out, run{i, j})
			i = j
		}
		return out
	}
	widthRuns := func(rs []IndexRec) {
		wr := groups(rs, func(r IndexRec) uint64 { return uint64(len(r.Digest)) })
		le32(uint32(len(wr)))
		for _, g := range wr {
			w := len(rs[g.lo].Digest) + 8
			le32(uint32(w))
			le64(uint64(w * (g.hi - g.lo)))
			for _, r := range rs[g.lo:g.hi] {
				out = append(out, r.Digest...)
				le64(r.Offset)
			}
		}
	}
	if codec == IndexSortedCodec {
		widthRuns(rs)
		return out
	}
	mr := groups(rs, func(r IndexRec) uint64 { return r.Mh })
	le32(uint32(len(mr)))
	for _, g := range mr {
		le64(rs[g.lo].Mh)
		widthRuns(rs[g.lo:g.hi])
	}
	return out
}
