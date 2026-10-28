// Package clrz3 is a bounded parser and indexed-reference representation for .NET (CLR) managed
// assemblies (ECMA-335): PE32/PE32+ container, CLI header, metadata root, the compressed (#~)
// table stream with its heaps, signature blobs and CIL method bodies. Parse takes an io.ReaderAt
// and reads only headers; Walk and the representation take the file bytes. It never interprets
// strong-name or Authenticode material (they stay opaque bytes). Reference spaces — table index,
// coded index, #Strings / #Blob / #US / #GUID offsets, metadata token, RVA, list start — are kept
// apart end to end.
package clrz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"

	"zeros3-z3/pez3"
)

var (
	// ErrFormat reports a file that is not a well-formed assembly (or a corrupt representation).
	ErrFormat = errors.New("clrz3: malformed")
	// ErrUnsupported reports legal structure this package does not represent (the caller keeps CDC).
	ErrUnsupported = errors.New("clrz3: unsupported")
	// ErrLimit reports a bound exceeded.
	ErrLimit = errors.New("clrz3: limit exceeded")
)

func bad(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrFormat, fmt.Sprintf(f, a...)) }
func unsup(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(f, a...))
}
func limit(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrLimit, fmt.Sprintf(f, a...)) }

// Limits bounds what Parse accepts.
type Limits struct {
	MaxStreams int   // metadata streams (default 32)
	MaxRows    int64 // rows in one table (default 1<<24, the token row space)
	MaxHdr     int64 // bytes of PE headers (default 4096)
}

func (l *Limits) fill() {
	if l.MaxStreams == 0 {
		l.MaxStreams = 32
	}
	if l.MaxRows == 0 {
		l.MaxRows = 1 << 24
	}
	if l.MaxHdr == 0 {
		l.MaxHdr = 4096
	}
}

// Region is a byte range of the file.
type Region struct{ Off, Len int64 }

func (r Region) End() int64 { return r.Off + r.Len }

// Stream is one metadata stream (heap or table stream).
type Stream struct {
	Name   string
	HdrOff int64
	Off    int64 // absolute file offset
	Size   int64
}

// ColLayout is the byte position and width of a column inside a row.
type ColLayout struct{ Off, W int }

// Table is the geometry of one metadata table.
type Table struct {
	Rows    uint32
	Off     int64
	RowSize int
	Cols    []ColLayout
}

// CLIHeader is the IMAGE_COR20_HEADER.
type CLIHeader struct {
	Cb                          uint32
	Major, Minor                uint16
	MetaRVA, MetaSize           uint32
	Flags                       uint32
	Entry                       uint32
	ResRVA, ResSize             uint32
	SNRVA, SNSize               uint32
	VTableRVA, VTableSize       uint32
	ManagedNativeRVA, ManagedNS uint32
}

// File is the parsed geometry of one assembly.
type File struct {
	Size          int64
	PE32Plus      bool
	Machine       uint16
	Secs          []pez3.Section
	SizeOfHeaders int64
	Dirs          [16][2]uint32
	CertOff       int64 // security directory (file offset) and size, 0 if none
	CertSize      int64
	CLIOff        int64
	CLI           CLIHeader
	MetaOff       int64
	MetaVer       string
	Streams       []Stream
	Tab           Stream
	Str, US, GUID Stream
	Blob          Stream
	HasStr        bool
	HasUS         bool
	HasGUID       bool
	HasBlob       bool
	TabKind       byte // '~' compressed, '-' uncompressed
	HeapSizes     byte
	Valid         uint64
	Tables        [NumTables]Table
	TabData       int64 // file offset of the first table row
	TabEnd        int64 // file offset after the last table row
	Excised       []Region
	HasPtr        bool

	strW, guidW, blobW int
	tabW               [NumTables]int
	codedW             [NumCoded]int
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func readAt(ra io.ReaderAt, size, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > size || int64(n) > size-off {
		return nil, bad("read of %d bytes at %d outside the %d-byte file", n, off, size)
	}
	b := make([]byte, n)
	if _, err := ra.ReadAt(b, off); err != nil && !(err == io.EOF && n == len(b)) {
		return nil, err
	}
	return b, nil
}

// Parse validates the PE/CLI/metadata geometry of an assembly.
func Parse(ra io.ReaderAt, size int64, lim Limits) (*File, error) {
	lim.fill()
	f := &File{Size: size}
	dos, err := readAt(ra, size, 0, 0x40)
	if err != nil || dos[0] != 'M' || dos[1] != 'Z' {
		return nil, bad("no DOS header")
	}
	pe := int64(le32(dos[0x3c:]))
	hdr, err := readAt(ra, size, pe, 24)
	if err != nil || string(hdr[:4]) != "PE\x00\x00" {
		return nil, bad("no PE signature")
	}
	f.Machine = le16(hdr[4:])
	nsec := int(le16(hdr[6:]))
	optSize := int64(le16(hdr[20:]))
	if nsec == 0 || nsec > 96 {
		return nil, bad("%d sections", nsec)
	}
	if optSize < 96 || optSize > 1024 {
		return nil, bad("optional header of %d bytes", optSize)
	}
	opt, err := readAt(ra, size, pe+24, int(optSize))
	if err != nil {
		return nil, err
	}
	var dirOff int
	switch le16(opt) {
	case 0x10b:
		dirOff = 96
	case 0x20b:
		f.PE32Plus = true
		dirOff = 112
	default:
		return nil, bad("optional header magic %#x", le16(opt))
	}
	f.SizeOfHeaders = int64(le32(opt[60:]))
	nrva := int(le32(opt[dirOff-4:]))
	if nrva > 16 || int64(dirOff+8*nrva) > optSize {
		return nil, bad("%d data directories", nrva)
	}
	for i := 0; i < nrva; i++ {
		f.Dirs[i] = [2]uint32{le32(opt[dirOff+8*i:]), le32(opt[dirOff+8*i+4:])}
	}
	secOff := pe + 24 + optSize
	sh, err := readAt(ra, size, secOff, 40*nsec)
	if err != nil {
		return nil, err
	}
	for i := 0; i < nsec; i++ {
		b := sh[40*i:]
		name := string(b[:8])
		for j := 0; j < 8; j++ {
			if b[j] == 0 {
				name = string(b[:j])
				break
			}
		}
		s := pez3.Section{Name: name, VSize: le32(b[8:]), RVA: le32(b[12:]), RawSize: le32(b[16:]), RawOff: le32(b[20:]), Chr: le32(b[36:])}
		if s.RawSize > 0 && (int64(s.RawOff) > size || int64(s.RawSize) > size-int64(s.RawOff)) {
			return nil, bad("section %q outside the file", name)
		}
		f.Secs = append(f.Secs, s)
	}
	hdrEnd := secOff + int64(40*nsec)
	if f.SizeOfHeaders < hdrEnd || f.SizeOfHeaders > lim.MaxHdr || f.SizeOfHeaders > size {
		return nil, bad("SizeOfHeaders %d", f.SizeOfHeaders)
	}
	for _, s := range f.Secs {
		if s.RawSize > 0 && int64(s.RawOff) < f.SizeOfHeaders {
			return nil, unsup("section data inside the header area")
		}
	}
	if d := f.Dirs[4]; d[0] != 0 && d[1] != 0 {
		f.CertOff, f.CertSize = int64(d[0]), int64(d[1])
	}
	cliRVA, cliSize := f.Dirs[14][0], f.Dirs[14][1]
	if cliRVA == 0 || cliSize < 72 {
		return nil, bad("no CLI header")
	}
	off, avail, ok := pez3.RVAOff(f.Secs, int(size), uint64(cliRVA))
	if !ok || avail < 72 {
		return nil, bad("CLI header RVA %#x unmapped", cliRVA)
	}
	f.CLIOff = int64(off)
	cb, err := readAt(ra, size, f.CLIOff, 72)
	if err != nil {
		return nil, err
	}
	f.CLI = CLIHeader{Cb: le32(cb), Major: le16(cb[4:]), Minor: le16(cb[6:]), MetaRVA: le32(cb[8:]), MetaSize: le32(cb[12:]), Flags: le32(cb[16:]),
		Entry: le32(cb[20:]), ResRVA: le32(cb[24:]), ResSize: le32(cb[28:]), SNRVA: le32(cb[32:]), SNSize: le32(cb[36:]),
		VTableRVA: le32(cb[48:]), VTableSize: le32(cb[52:]), ManagedNativeRVA: le32(cb[64:]), ManagedNS: le32(cb[68:])}
	if f.CLI.Cb < 72 {
		return nil, bad("CLI header cb %d", f.CLI.Cb)
	}
	moff, mavail, ok := pez3.RVAOff(f.Secs, int(size), uint64(f.CLI.MetaRVA))
	if !ok || int64(mavail) < int64(f.CLI.MetaSize) || f.CLI.MetaSize < 32 {
		return nil, bad("metadata RVA %#x size %d unmapped", f.CLI.MetaRVA, f.CLI.MetaSize)
	}
	f.MetaOff = int64(moff)
	if err := f.parseRoot(ra, lim); err != nil {
		return nil, err
	}
	if err := f.parseTables(ra, lim); err != nil {
		return nil, err
	}
	f.Excised = []Region{{0, f.SizeOfHeaders}, {f.CLIOff, 72}}
	rootEnd := f.MetaOff + f.rootLen()
	f.Excised = append(f.Excised, Region{f.MetaOff, rootEnd - f.MetaOff}, Region{f.Tab.Off, f.TabData - f.Tab.Off})
	// the excised areas must be disjoint and ascending once sorted
	sortRegions(f.Excised)
	for i := 1; i < len(f.Excised); i++ {
		if f.Excised[i].Off < f.Excised[i-1].End() {
			return nil, unsup("header regions overlap")
		}
	}
	return f, nil
}

func sortRegions(r []Region) {
	for i := 1; i < len(r); i++ {
		for j := i; j > 0 && r[j].Off < r[j-1].Off; j-- {
			r[j], r[j-1] = r[j-1], r[j]
		}
	}
}

// rootLen is the byte length of the metadata root plus the stream headers.
func (f *File) rootLen() int64 {
	end := int64(0)
	for _, s := range f.Streams {
		n := int64(len(s.Name)) + 1
		n = (n + 3) &^ 3
		if e := s.HdrOff + 8 + n - f.MetaOff; e > end {
			end = e
		}
	}
	return end
}

func (f *File) parseRoot(ra io.ReaderAt, lim Limits) error {
	b, err := readAt(ra, f.Size, f.MetaOff, 16)
	if err != nil {
		return err
	}
	if le32(b) != 0x424A5342 {
		return bad("metadata signature %#x", le32(b))
	}
	vlen := int64(le32(b[12:]))
	if vlen > 256 || vlen%4 != 0 {
		return bad("metadata version length %d", vlen)
	}
	vb, err := readAt(ra, f.Size, f.MetaOff+16, int(vlen)+4)
	if err != nil {
		return err
	}
	f.MetaVer = string(vb[:vlen])
	for i := 0; i < int(vlen); i++ {
		if vb[i] == 0 {
			f.MetaVer = string(vb[:i])
			break
		}
	}
	ns := int(le16(vb[vlen+2:]))
	if ns > lim.MaxStreams {
		return limit("%d metadata streams", ns)
	}
	pos := f.MetaOff + 16 + vlen + 4
	for i := 0; i < ns; i++ {
		h, err := readAt(ra, f.Size, pos, 8)
		if err != nil {
			return err
		}
		off, sz := int64(le32(h)), int64(le32(h[4:]))
		nb, err := readAt(ra, f.Size, pos+8, int(min(36, f.Size-pos-8)))
		if err != nil {
			return err
		}
		n := 0
		for n < len(nb) && nb[n] != 0 {
			n++
		}
		if n == len(nb) || n > 32 {
			return bad("stream name")
		}
		s := Stream{Name: string(nb[:n]), HdrOff: pos, Off: f.MetaOff + off, Size: sz}
		if off < 0 || sz < 0 || s.Off > f.Size || sz > f.Size-s.Off || off > int64(f.CLI.MetaSize) {
			return bad("stream %q outside the file", s.Name)
		}
		f.Streams = append(f.Streams, s)
		pos += 8 + int64((n+1+3)&^3)
	}
	for i := range f.Streams {
		s := f.Streams[i]
		switch s.Name {
		case "#~", "#-":
			if f.TabKind != 0 {
				return unsup("two table streams")
			}
			f.Tab, f.TabKind = s, s.Name[1]
		case "#Strings":
			f.Str, f.HasStr = s, true
		case "#US":
			f.US, f.HasUS = s, true
		case "#GUID":
			f.GUID, f.HasGUID = s, true
		case "#Blob":
			f.Blob, f.HasBlob = s, true
		}
	}
	if f.TabKind == 0 {
		return bad("no table stream")
	}
	// streams must not overlap one another
	for i := range f.Streams {
		for j := i + 1; j < len(f.Streams); j++ {
			a, c := f.Streams[i], f.Streams[j]
			if a.Size > 0 && c.Size > 0 && a.Off < c.Off+c.Size && c.Off < a.Off+a.Size {
				return unsup("overlapping streams %s %s", a.Name, c.Name)
			}
		}
	}
	return nil
}

func (f *File) parseTables(ra io.ReaderAt, lim Limits) error {
	t := f.Tab
	if t.Size < 24 {
		return bad("table stream of %d bytes", t.Size)
	}
	h, err := readAt(ra, f.Size, t.Off, 24)
	if err != nil {
		return err
	}
	f.HeapSizes = h[6]
	f.Valid = binary.LittleEndian.Uint64(h[8:])
	if f.HeapSizes&0x40 != 0 {
		return unsup("extra data in the table stream header")
	}
	if f.Valid>>NumTables != 0 {
		return unsup("tables beyond %d (valid mask %#x)", NumTables-1, f.Valid)
	}
	nv := bits.OnesCount64(f.Valid)
	hdrLen := int64(24 + 4*nv)
	if hdrLen > t.Size {
		return bad("table row counts outside the stream")
	}
	rb, err := readAt(ra, f.Size, t.Off+24, 4*nv)
	if err != nil {
		return err
	}
	k := 0
	for i := 0; i < NumTables; i++ {
		if f.Valid>>uint(i)&1 == 0 {
			continue
		}
		n := int64(le32(rb[4*k:]))
		k++
		if n > lim.MaxRows {
			return limit("table %s has %d rows", TableName(i), n)
		}
		f.Tables[i].Rows = uint32(n)
		if isPtr(i) && n > 0 {
			f.HasPtr = true
		}
	}
	f.strW, f.guidW, f.blobW = 2, 2, 2
	if f.HeapSizes&1 != 0 {
		f.strW = 4
	}
	if f.HeapSizes&2 != 0 {
		f.guidW = 4
	}
	if f.HeapSizes&4 != 0 {
		f.blobW = 4
	}
	for i := 0; i < NumTables; i++ {
		f.tabW[i] = 2
		if f.Tables[i].Rows >= 1<<16 {
			f.tabW[i] = 4
		}
	}
	for c := 0; c < NumCoded; c++ {
		var mx uint32
		for _, tb := range coded[c].tabs {
			if tb >= 0 && f.Tables[tb].Rows > mx {
				mx = f.Tables[tb].Rows
			}
		}
		f.codedW[c] = 2
		if uint64(mx) >= 1<<uint(16-coded[c].bits) {
			f.codedW[c] = 4
		}
	}
	pos := t.Off + hdrLen
	f.TabData = pos
	for i := 0; i < NumTables; i++ {
		tb := &f.Tables[i]
		o := 0
		for _, c := range schema[i] {
			w := f.colWidth(c)
			tb.Cols = append(tb.Cols, ColLayout{o, w})
			o += w
		}
		tb.RowSize, tb.Off = o, pos
		if tb.Rows == 0 {
			continue
		}
		sz := uint64(tb.Rows) * uint64(o)
		if sz > uint64(t.Off+t.Size-pos) {
			return bad("table %s of %d rows runs past the stream", TableName(i), tb.Rows)
		}
		pos += int64(sz)
	}
	f.TabEnd = pos
	return nil
}

func (f *File) colWidth(c ColDef) int {
	switch c.K {
	case KConst, KRVA:
		return c.W
	case KStr:
		return f.strW
	case KGuid:
		return f.guidW
	case KBlob:
		return f.blobW
	case KTable, KList:
		return f.tabW[c.T]
	case KCoded:
		return f.codedW[c.T]
	}
	return 0
}

// Rows is the row count of table t.
func (f *File) Rows(t int) uint32 { return f.Tables[t].Rows }

// CodedWidth is the byte width of a coded-index family in this file.
func (f *File) CodedWidth(c int) int { return f.codedW[c] }

// HeapWidths returns the byte widths of #Strings, #GUID and #Blob indexes.
func (f *File) HeapWidths() (str, guid, blob int) { return f.strW, f.guidW, f.blobW }

// IndexWidth is the byte width of a simple index into table t.
func (f *File) IndexWidth(t int) int { return f.tabW[t] }

// RowOff is the file offset of row r (1-based) of table t.
func (f *File) RowOff(t int, r uint32) int64 {
	return f.Tables[t].Off + int64(r-1)*int64(f.Tables[t].RowSize)
}

// Col reads column c of row r (1-based) of table t from data.
func (f *File) Col(data []byte, t int, r uint32, c int) uint32 {
	cl := f.Tables[t].Cols[c]
	o := f.RowOff(t, r) + int64(cl.Off)
	if cl.W == 2 {
		return uint32(le16(data[o:]))
	}
	return le32(data[o:])
}

// PutCol writes column c of row r of table t into data (width as in the file; a value that does
// not fit is an error).
func (f *File) PutCol(data []byte, t int, r uint32, c int, v uint32) error {
	cl := f.Tables[t].Cols[c]
	o := f.RowOff(t, r) + int64(cl.Off)
	if cl.W == 2 {
		if v > 0xffff {
			return bad("value %d does not fit a 2-byte %s.%s", v, TableName(t), schema[t][c].Name)
		}
		binary.LittleEndian.PutUint16(data[o:], uint16(v))
		return nil
	}
	binary.LittleEndian.PutUint32(data[o:], v)
	return nil
}

// DecodeCoded splits a coded-index value into its target table and row. ok is false for a tag
// that names no table. A zero value is the null reference (table -1, row 0).
func DecodeCoded(c int, v uint32) (table int, row uint32, ok bool) {
	d := &coded[c]
	tag := int(v & (1<<uint(d.bits) - 1))
	row = v >> uint(d.bits)
	if tag >= len(d.tabs) || d.tabs[tag] < 0 {
		return -1, row, false
	}
	return d.tabs[tag], row, true
}

// EncodeCoded is the inverse of DecodeCoded.
func EncodeCoded(c, table int, row uint32) (uint32, bool) {
	d := &coded[c]
	for tag, t := range d.tabs {
		if t == table {
			if uint64(row)<<uint(d.bits) > 0xffffffff {
				return 0, false
			}
			return row<<uint(d.bits) | uint32(tag), true
		}
	}
	return 0, false
}

// RVAToOff maps an RVA to a file offset and the bytes available from it.
func (f *File) RVAToOff(rva uint32) (off, avail int, ok bool) {
	return pez3.RVAOff(f.Secs, int(f.Size), uint64(rva))
}
