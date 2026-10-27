// Package dexz3 is the Z3-32 strict, bounded DEX reader and indexed-reference normalizer.
//
// Parse takes an io.ReaderAt plus size, validates the header, map list and every ID table with
// checked arithmetic and hard limits, and tiles the file into an exact partition of typed spans
// (anything it does not model stays an opaque span). Walk then enumerates the *slots* of the file:
// the byte ranges that hold a numeric string/type/proto/field/method index (fixed-width fields,
// instruction operands, ULEB class-data deltas, handler types) or a file offset. Encode replaces
// each index slot by a short digest of the target's DEX-native identity (never the numeric index)
// and Decode rebuilds the exact bytes from that representation alone. Nothing here depends on the
// tool that produced the DEX, on the base revision, or on the header checksum (the header is kept
// as exact bytes; its adler32 is re-verified on decode when the original was valid). DEX versions
// 035-041 are accepted (037 and 039 are what the corpus holds); CompactDex, link data and the
// call-site / method-handle tables are not modelled (their bytes stay opaque payload). Stdlib only.
package dexz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

var (
	ErrFormat      = errors.New("dexz3: malformed DEX")
	ErrUnsupported = errors.New("dexz3: unsupported DEX feature")
	ErrLimit       = errors.New("dexz3: limit exceeded")
)

// Limits bound every parse; zero fields take the defaults.
type Limits struct {
	MaxFile     int64 // default 512 MiB
	MaxStrings  int   // 4 Mi
	MaxIDs      int   // types/protos/fields/methods/classes: 1 Mi
	MaxMap      int   // 64
	MaxStrLen   int   // MUTF-8 bytes per string: 1 Mi
	MaxLEB      int   // ULEB/SLEB bytes: 5
	MaxCodeUnit int   // insns units per method: 1 Mi
	MaxTries    int   // 64 Ki
	MaxHandlers int   // 64 Ki
	MaxMembers  int   // class_data members per class: 256 Ki
	MaxSlots    int   // 64 Mi
	MaxDepth    int   // encoded value / annotation nesting: 32
	MaxDebugOps int   // 1 Mi
	MaxItems    int   // per section: 8 Mi
}

func (l Limits) norm() Limits {
	d := func(v *int, x int) {
		if *v <= 0 {
			*v = x
		}
	}
	if l.MaxFile <= 0 {
		l.MaxFile = 512 << 20
	}
	d(&l.MaxStrings, 4<<20)
	d(&l.MaxIDs, 1<<20)
	d(&l.MaxMap, 64)
	d(&l.MaxStrLen, 1<<20)
	d(&l.MaxLEB, 5)
	d(&l.MaxCodeUnit, 1<<20)
	d(&l.MaxTries, 1<<16)
	d(&l.MaxHandlers, 1<<16)
	d(&l.MaxMembers, 1<<18)
	d(&l.MaxSlots, 64<<20)
	d(&l.MaxDepth, 32)
	d(&l.MaxDebugOps, 1<<20)
	d(&l.MaxItems, 8<<20)
	return l
}

// Map item type codes.
const (
	TypeHeader       = 0x0000
	TypeStringID     = 0x0001
	TypeTypeID       = 0x0002
	TypeProtoID      = 0x0003
	TypeFieldID      = 0x0004
	TypeMethodID     = 0x0005
	TypeClassDef     = 0x0006
	TypeCallSiteID   = 0x0007
	TypeMethodHandle = 0x0008
	TypeMapList      = 0x1000
	TypeTypeList     = 0x1001
	TypeAnnoSetRef   = 0x1002
	TypeAnnoSet      = 0x1003
	TypeClassData    = 0x2000
	TypeCode         = 0x2001
	TypeStringData   = 0x2002
	TypeDebugInfo    = 0x2003
	TypeAnnotation   = 0x2004
	TypeEncodedArray = 0x2005
	TypeAnnoDir      = 0x2006
	TypeHiddenAPI    = 0xF000
	TypeGap          = 0xFFFF // synthetic: bytes between mapped sections (opaque)
)

const (
	HeaderSize = 112
	NoIndex    = 0xFFFFFFFF
	endianTag  = 0x12345678
)

// Header is the decoded fixed header (the raw bytes are kept in File.Raw).
type Header struct {
	Magic                   [8]byte
	Checksum                uint32
	Signature               [20]byte
	FileSize, HeaderSize    uint32
	EndianTag               uint32
	LinkSize, LinkOff       uint32
	MapOff                  uint32
	StringIDs, StringIDsOff uint32
	TypeIDs, TypeIDsOff     uint32
	ProtoIDs, ProtoIDsOff   uint32
	FieldIDs, FieldIDsOff   uint32
	MethodIDs, MethodIDsOff uint32
	ClassDefs, ClassDefsOff uint32
	DataSize, DataOff       uint32
}

// MapItem is one map_list entry.
type MapItem struct {
	Type   uint16
	Count  uint32
	Offset uint32
}

// Span is one piece of the exact partition of the file.
type Span struct {
	Type  uint16 // map type, TypeGap for unmapped bytes
	Off   int64
	Len   int64
	Count uint32 // items the map declares (0 for gaps)
}

// File is a parsed DEX. All tables are immutable after Parse.
type File struct {
	Size    int64
	Version int // 35..41
	Hdr     Header
	Raw     [HeaderSize]byte
	Map     []MapItem
	Spans   []Span // exact partition, file order
	Lim     Limits
}

// Section returns the span of the given map type (ok=false when absent).
func (f *File) Section(t uint16) (Span, bool) {
	for _, s := range f.Spans {
		if s.Type == t {
			return s, true
		}
	}
	return Span{}, false
}

// rd is a bounded windowed reader over an io.ReaderAt.
type rd struct {
	r    io.ReaderAt
	size int64
	buf  []byte
	base int64
}

const winSize = 1 << 20

func (x *rd) get(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > x.size || int64(n) > x.size-off {
		return nil, fmt.Errorf("%w: range %d+%d outside %d", ErrFormat, off, n, x.size)
	}
	if n == 0 {
		return nil, nil
	}
	if off >= x.base && off+int64(n) <= x.base+int64(len(x.buf)) {
		return x.buf[off-x.base : off-x.base+int64(n)], nil
	}
	if n > winSize {
		b := make([]byte, n)
		if _, err := x.r.ReadAt(b, off); err != nil && !(err == io.EOF) {
			return nil, err
		}
		return b, nil
	}
	w := int64(winSize)
	if w > x.size-off {
		w = x.size - off
	}
	if cap(x.buf) < int(w) {
		x.buf = make([]byte, winSize)
	}
	x.buf = x.buf[:w]
	if _, err := x.r.ReadAt(x.buf, off); err != nil && err != io.EOF {
		x.buf = x.buf[:0]
		return nil, err
	}
	x.base = off
	return x.buf[:n], nil
}

func le16(b []byte) uint32 { return uint32(binary.LittleEndian.Uint16(b)) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func bad(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrFormat}, a...)...) }

// fixedSize is the byte length of one entry of the fixed-size ID sections.
func fixedSize(t uint16) int64 {
	switch t {
	case TypeStringID, TypeTypeID, TypeCallSiteID:
		return 4
	case TypeProtoID:
		return 12
	case TypeFieldID, TypeMethodID, TypeMethodHandle:
		return 8
	case TypeClassDef:
		return 32
	}
	return 0
}

// Parse validates header, map list and the ID tables and returns the exact partition.
func Parse(r io.ReaderAt, size int64, lim Limits) (*File, error) {
	lim = lim.norm()
	if size < HeaderSize {
		return nil, bad("short file %d", size)
	}
	if size > lim.MaxFile || size > 1<<32-1 {
		return nil, fmt.Errorf("%w: file %d bytes", ErrLimit, size)
	}
	x := &rd{r: r, size: size}
	hb, err := x.get(0, HeaderSize)
	if err != nil {
		return nil, err
	}
	f := &File{Size: size, Lim: lim}
	copy(f.Raw[:], hb)
	h := &f.Hdr
	copy(h.Magic[:], hb[0:8])
	if string(h.Magic[:4]) != "dex\n" || h.Magic[7] != 0 {
		return nil, bad("magic % x", h.Magic[:])
	}
	v := 0
	for _, c := range h.Magic[4:7] {
		if c < '0' || c > '9' {
			return nil, bad("version % x", h.Magic[4:7])
		}
		v = v*10 + int(c-'0')
	}
	if v < 35 || v > 41 {
		return nil, fmt.Errorf("%w: version %d", ErrUnsupported, v)
	}
	f.Version = v
	h.Checksum = le32(hb[8:])
	copy(h.Signature[:], hb[12:32])
	u := func(i int) uint32 { return le32(hb[32+4*i:]) }
	h.FileSize, h.HeaderSize, h.EndianTag = u(0), u(1), u(2)
	h.LinkSize, h.LinkOff, h.MapOff = u(3), u(4), u(5)
	h.StringIDs, h.StringIDsOff = u(6), u(7)
	h.TypeIDs, h.TypeIDsOff = u(8), u(9)
	h.ProtoIDs, h.ProtoIDsOff = u(10), u(11)
	h.FieldIDs, h.FieldIDsOff = u(12), u(13)
	h.MethodIDs, h.MethodIDsOff = u(14), u(15)
	h.ClassDefs, h.ClassDefsOff = u(16), u(17)
	h.DataSize, h.DataOff = u(18), u(19)
	if int64(h.FileSize) != size {
		return nil, bad("file_size %d != %d", h.FileSize, size)
	}
	if h.HeaderSize != HeaderSize {
		return nil, bad("header_size %d", h.HeaderSize)
	}
	if h.EndianTag != endianTag {
		return nil, fmt.Errorf("%w: endian_tag %#x", ErrUnsupported, h.EndianTag)
	}
	if h.LinkSize != 0 || h.LinkOff != 0 {
		return nil, fmt.Errorf("%w: link section", ErrUnsupported)
	}
	if int64(h.DataOff) > size || int64(h.DataSize) > size-int64(h.DataOff) {
		return nil, bad("data section %d+%d", h.DataOff, h.DataSize)
	}
	counts := []struct {
		n   uint32
		max int
		w   string
	}{{h.StringIDs, lim.MaxStrings, "string_ids"}, {h.TypeIDs, lim.MaxIDs, "type_ids"}, {h.ProtoIDs, lim.MaxIDs, "proto_ids"},
		{h.FieldIDs, lim.MaxIDs, "field_ids"}, {h.MethodIDs, lim.MaxIDs, "method_ids"}, {h.ClassDefs, lim.MaxIDs, "class_defs"}}
	for _, c := range counts {
		if int64(c.n) > int64(c.max) {
			return nil, fmt.Errorf("%w: %s %d", ErrLimit, c.w, c.n)
		}
	}
	// map list
	if h.MapOff < HeaderSize || h.MapOff%4 != 0 || int64(h.MapOff)+4 > size {
		return nil, bad("map_off %d", h.MapOff)
	}
	nb, err := x.get(int64(h.MapOff), 4)
	if err != nil {
		return nil, err
	}
	mn := int(le32(nb))
	if mn <= 0 || mn > lim.MaxMap {
		return nil, bad("map size %d", mn)
	}
	mb, err := x.get(int64(h.MapOff)+4, mn*12)
	if err != nil {
		return nil, err
	}
	seen := map[uint16]bool{}
	var prev int64 = -1
	for i := 0; i < mn; i++ {
		e := mb[12*i:]
		it := MapItem{Type: uint16(le16(e)), Count: le32(e[4:]), Offset: le32(e[8:])}
		if le16(e[2:]) != 0 {
			return nil, bad("map unused field")
		}
		if seen[it.Type] {
			return nil, bad("duplicate map type %#x", it.Type)
		}
		seen[it.Type] = true
		if int64(it.Offset) <= prev || int64(it.Offset) > size {
			return nil, bad("map offsets not increasing: %#x@%d", it.Type, it.Offset)
		}
		prev = int64(it.Offset)
		f.Map = append(f.Map, it)
	}
	if f.Map[0].Type != TypeHeader || f.Map[0].Offset != 0 || f.Map[0].Count != 1 {
		return nil, bad("map does not start with the header")
	}
	last := f.Map[mn-1]
	if last.Type != TypeMapList || last.Count != 1 || last.Offset != h.MapOff {
		return nil, bad("map does not end with the map list")
	}
	if int64(h.MapOff)+4+int64(mn)*12 > size {
		return nil, bad("map list outside file")
	}
	// header/ID table agreement with the map
	agree := func(t uint16, cnt, off uint32) error {
		for _, m := range f.Map {
			if m.Type == t {
				if m.Count != cnt || m.Offset != off {
					return bad("map %#x (%d@%d) vs header (%d@%d)", t, m.Count, m.Offset, cnt, off)
				}
				return nil
			}
		}
		if cnt != 0 {
			return bad("header declares %#x (%d@%d) but the map has none", t, cnt, off)
		}
		return nil
	}
	for _, a := range []struct {
		t        uint16
		cnt, off uint32
	}{{TypeStringID, h.StringIDs, h.StringIDsOff}, {TypeTypeID, h.TypeIDs, h.TypeIDsOff}, {TypeProtoID, h.ProtoIDs, h.ProtoIDsOff},
		{TypeFieldID, h.FieldIDs, h.FieldIDsOff}, {TypeMethodID, h.MethodIDs, h.MethodIDsOff}, {TypeClassDef, h.ClassDefs, h.ClassDefsOff}} {
		if err := agree(a.t, a.cnt, a.off); err != nil {
			return nil, err
		}
	}
	// exact partition: each mapped section runs to the next one; the last (map) has a known extent
	for i, m := range f.Map {
		end := size
		if i+1 < len(f.Map) {
			end = int64(f.Map[i+1].Offset)
		}
		off := int64(m.Offset)
		switch {
		case m.Type == TypeHeader:
			end = HeaderSize
			if i+1 < len(f.Map) && int64(f.Map[i+1].Offset) < HeaderSize {
				return nil, bad("section inside header")
			}
		case m.Type == TypeMapList:
			end = off + 4 + int64(mn)*12
		case fixedSize(m.Type) > 0:
			w := fixedSize(m.Type)
			if int64(m.Count) > (size-off)/w {
				return nil, bad("section %#x count %d overflows", m.Type, m.Count)
			}
			end = off + int64(m.Count)*w
			if i+1 < len(f.Map) && end > int64(f.Map[i+1].Offset) {
				return nil, bad("section %#x overlaps the next", m.Type)
			}
		}
		if end < off || end > size {
			return nil, bad("section %#x extent", m.Type)
		}
		if m.Count > uint32(lim.MaxItems) {
			return nil, fmt.Errorf("%w: section %#x count %d", ErrLimit, m.Type, m.Count)
		}
		f.Spans = append(f.Spans, Span{Type: m.Type, Off: off, Len: end - off, Count: m.Count})
	}
	// gaps (alignment padding after fixed sections, trailing bytes) become explicit opaque spans
	var out []Span
	var pos int64
	for _, s := range f.Spans {
		if s.Off < pos {
			return nil, bad("overlapping sections at %d", s.Off)
		}
		if s.Off > pos {
			out = append(out, Span{Type: TypeGap, Off: pos, Len: s.Off - pos})
		}
		out = append(out, s)
		pos = s.Off + s.Len
	}
	if pos < size {
		out = append(out, Span{Type: TypeGap, Off: pos, Len: size - pos})
	}
	f.Spans = out
	// the data section must lie inside the file and contain every data item section
	sort.SliceStable(f.Spans, func(i, j int) bool { return f.Spans[i].Off < f.Spans[j].Off })
	return f, nil
}
