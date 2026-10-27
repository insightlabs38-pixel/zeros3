package parquetz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	tc "zeros3-z3/thriftcompactz3"
)

// Physical types, encodings, codecs and page types (parquet.thrift enum values).
const (
	TBoolean  = 0
	TInt32    = 1
	TInt64    = 2
	TInt96    = 3
	TFloat    = 4
	TDouble   = 5
	TByteArr  = 6
	TFixedLen = 7

	EncPlain       = 0
	EncPlainDict   = 2
	EncRLE         = 3
	EncBitPacked   = 4
	EncDeltaBin    = 5
	EncDeltaLenBA  = 6
	EncDeltaBA     = 7
	EncRLEDict     = 8
	EncByteStrSplt = 9

	CodecNone   = 0
	CodecSnappy = 1
	CodecGzip   = 2
	CodecLZO    = 3
	CodecBrotli = 4
	CodecLZ4    = 5
	CodecZstd   = 6
	CodecLZ4Raw = 7

	PageData   = 0
	PageIndex  = 1
	PageDict   = 2
	PageDataV2 = 3
)

var (
	// ErrCorrupt reports a file that violates the format (or a recipe that does).
	ErrCorrupt = errors.New("parquetz3: corrupt")
	// ErrUnsupported reports a valid-looking construct outside the implemented subset.
	// Encode returns it for "use CDC": it is never decoded on a best-effort basis.
	ErrUnsupported = errors.New("parquetz3: unsupported feature")
	// ErrLimit reports that a configured bound would be exceeded.
	ErrLimit = errors.New("parquetz3: limit exceeded")
	// ErrRecipe reports a malformed or inconsistent recipe.
	ErrRecipe = errors.New("parquetz3: malformed recipe")
)

func corrupt(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...)
}
func unsupported(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrUnsupported}, a...)...)
}
func limitErr(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrLimit}, a...)...)
}

// wrapTC puts a Compact Thrift error in this package's classes while keeping the original
// in the chain (Pages tells "need more header bytes" from corruption with errors.Is).
func wrapTC(err error) error {
	switch {
	case err == nil, errors.Is(err, ErrCorrupt), errors.Is(err, ErrUnsupported), errors.Is(err, ErrLimit):
		return err
	case errors.Is(err, tc.ErrLimit):
		return fmt.Errorf("%w: %w", ErrLimit, err)
	}
	return fmt.Errorf("%w: %w", ErrCorrupt, err)
}

func recipeErr(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrRecipe}, a...)...)
}

// Limits bound every parse and every allocation. The zero value selects the defaults.
type Limits struct {
	MaxFooter    int64 // FileMetaData bytes (default 64 MiB)
	MaxRowGroups int   // default 1<<20
	MaxColumns   int   // schema elements / leaf columns (default 1<<14)
	MaxChunks    int   // row groups x columns (default 1<<22)
	MaxPages     int   // pages of one file (default 1<<24)
	MaxHeader    int   // bytes of one PageHeader (default 1 MiB)
	MaxPage      int64 // compressed or uncompressed bytes of one page (default 128 MiB)
	MaxValue     int   // bytes of one BYTE_ARRAY value kept logical (default 4 MiB; larger: the chunk is opaque)
	MaxDict      int64 // bytes of one chunk's dictionary page (default 128 MiB)
	Thrift       tc.Limits
}

func (l Limits) norm() Limits {
	if l.MaxFooter <= 0 {
		l.MaxFooter = 64 << 20
	}
	if l.MaxRowGroups <= 0 {
		l.MaxRowGroups = 1 << 20
	}
	if l.MaxColumns <= 0 {
		l.MaxColumns = 1 << 14
	}
	if l.MaxChunks <= 0 {
		l.MaxChunks = 1 << 22
	}
	if l.MaxPages <= 0 {
		l.MaxPages = 1 << 24
	}
	if l.MaxHeader <= 0 {
		l.MaxHeader = 1 << 20
	}
	if l.MaxPage <= 0 {
		l.MaxPage = 128 << 20
	}
	if l.MaxValue <= 0 {
		l.MaxValue = 4 << 20
	}
	if l.MaxDict <= 0 {
		l.MaxDict = 128 << 20
	}
	return l
}

// Column is one leaf column of the schema.
type Column struct {
	Path           []string
	Name           string
	Phys           int
	TypeLen        int
	MaxDef, MaxRep int
	Converted      int32  // ConvertedType, -1 when absent
	Logical        string // LogicalType union member, "" when absent
}

// Chunk is one column chunk of one row group.
type Chunk struct {
	RG, Col             int
	Codec               int
	Encodings           []int32
	NumValues           int64
	DictOff, DataOff    int64 // -1 when absent
	Start, End          int64 // the exact page span [Start, End)
	TotalComp, TotalUnc int64
	FilePath            string
	HasStats            bool
	HasEncStats         bool
	HasSizeStats        bool
	ColIdx, OffIdx      [2]int64 // offset, length; -1 when absent
	Bloom               [2]int64
}

// RowGroup is one row group.
type RowGroup struct {
	NumRows int64
	Chunks  []Chunk
}

// File is the parsed footer of a Parquet file. FooterOff..Size-8 is the FileMetaData.
type File struct {
	Size      int64
	FooterOff int64
	FooterLen int
	Version   int32
	CreatedBy string
	NumRows   int64
	KVCount   int
	Columns   []Column
	RowGroups []RowGroup
	Raw       []byte // the FileMetaData bytes (kept: the footer is opaque framing)
	lim       Limits
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// Open parses the footer of the Parquet file in r (size bytes). It reads the first four
// bytes, the last eight and the FileMetaData; no page is touched.
func Open(r io.ReaderAt, size int64, lim Limits) (*File, error) {
	lim = lim.norm()
	if size < 12 {
		return nil, corrupt("%d bytes is too short for a Parquet file", size)
	}
	var head [4]byte
	if err := readFull(r, head[:], 0); err != nil {
		return nil, err
	}
	var tail [8]byte
	if err := readFull(r, tail[:], size-8); err != nil {
		return nil, err
	}
	if string(tail[4:]) == "PARE" || string(head[:]) == "PARE" {
		return nil, unsupported("encrypted footer (PARE)")
	}
	if string(head[:]) != "PAR1" || string(tail[4:]) != "PAR1" {
		return nil, corrupt("bad PAR1 magic")
	}
	fl := int64(binary.LittleEndian.Uint32(tail[:4]))
	if fl == 0 || fl > size-12 {
		return nil, corrupt("footer length %d in a %d byte file", fl, size)
	}
	if fl > lim.MaxFooter {
		return nil, limitErr("footer of %d bytes", fl)
	}
	raw := make([]byte, fl)
	if err := readFull(r, raw, size-8-fl); err != nil {
		return nil, err
	}
	f := &File{Size: size, FooterOff: size - 8 - fl, FooterLen: int(fl), Raw: raw, lim: lim}
	if err := f.parseMeta(raw); err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

type schemaElem struct {
	typ, typeLen, rep, nch, conv int32
	name                         string
	logical                      string
	hasType, hasRep              bool
}

const maxSchemaDepth = 32

var logicalNames = map[int16]string{1: "STRING", 2: "MAP", 3: "LIST", 4: "ENUM", 5: "DECIMAL", 6: "DATE", 7: "TIME", 8: "TIMESTAMP", 10: "INTEGER", 11: "UNKNOWN", 12: "JSON", 13: "BSON", 14: "UUID", 15: "FLOAT16"}

func (f *File) parseMeta(raw []byte) error {
	r := tc.NewReader(raw, f.lim.Thrift)
	var elems []schemaElem
	err := r.Struct(func(id int16, t tc.Type) error {
		switch {
		case id == 1 && t == tc.I32:
			v, err := r.I32()
			f.Version = v
			return err
		case id == 2 && t == tc.List:
			et, n, err := r.ListHeader()
			if err != nil {
				return err
			}
			if et != tc.Struct {
				return corrupt("schema is not a list of structs")
			}
			if n > f.lim.MaxColumns*2+1 {
				return limitErr("%d schema elements", n)
			}
			elems = make([]schemaElem, 0, min(n, 1024))
			for i := 0; i < n; i++ {
				e, err := parseSchemaElem(r)
				if err != nil {
					return err
				}
				elems = append(elems, e)
			}
			return nil
		case id == 3 && t == tc.I64:
			v, err := r.I64()
			f.NumRows = v
			return err
		case id == 4 && t == tc.List:
			et, n, err := r.ListHeader()
			if err != nil {
				return err
			}
			if et != tc.Struct {
				return corrupt("row_groups is not a list of structs")
			}
			if n > f.lim.MaxRowGroups {
				return limitErr("%d row groups", n)
			}
			f.RowGroups = make([]RowGroup, 0, min(n, 1024))
			for i := 0; i < n; i++ {
				rg, err := f.parseRowGroup(r, i)
				if err != nil {
					return err
				}
				f.RowGroups = append(f.RowGroups, rg)
			}
			return nil
		case id == 5 && t == tc.List:
			et, n, err := r.ListHeader()
			if err != nil {
				return err
			}
			f.KVCount = n
			for i := 0; i < n; i++ {
				if err := r.Skip(et); err != nil {
					return err
				}
			}
			return nil
		case id == 6 && t == tc.Binary:
			b, err := r.Binary()
			f.CreatedBy = string(b)
			return err
		}
		return r.Skip(t)
	})
	if err != nil {
		return wrapTC(err)
	}
	if r.Remaining() != 0 {
		return corrupt("%d bytes after the FileMetaData struct", r.Remaining())
	}
	return f.buildColumns(elems)
}

func parseSchemaElem(r *tc.Reader) (schemaElem, error) {
	e := schemaElem{conv: -1}
	err := r.Struct(func(id int16, t tc.Type) error {
		var err error
		switch {
		case id == 1 && t == tc.I32:
			e.typ, err = r.I32()
			e.hasType = true
		case id == 2 && t == tc.I32:
			e.typeLen, err = r.I32()
		case id == 3 && t == tc.I32:
			e.rep, err = r.I32()
			e.hasRep = true
		case id == 4 && t == tc.Binary:
			var b []byte
			b, err = r.Binary()
			e.name = string(b)
		case id == 5 && t == tc.I32:
			e.nch, err = r.I32()
		case id == 6 && t == tc.I32:
			e.conv, err = r.I32()
		case id == 10 && t == tc.Struct:
			err = r.Struct(func(uid int16, ut tc.Type) error {
				if n, ok := logicalNames[uid]; ok && e.logical == "" {
					e.logical = n
				}
				return r.Skip(ut)
			})
		default:
			err = r.Skip(t)
		}
		return err
	})
	return e, err
}

// buildColumns flattens the depth-first schema into leaf columns with their levels.
func (f *File) buildColumns(el []schemaElem) error {
	if len(el) == 0 {
		return corrupt("empty schema")
	}
	root := el[0]
	if root.nch < 0 || int(root.nch) > len(el)-1 {
		return corrupt("root has %d children in a schema of %d elements", root.nch, len(el))
	}
	type frame struct {
		left     int32
		def, rep int
	}
	stack := []frame{{left: root.nch}}
	var path []string // names of the open groups: len(path) == len(stack)-1
	for i := 1; i < len(el); i++ {
		for len(stack) > 1 && stack[len(stack)-1].left == 0 {
			stack = stack[:len(stack)-1]
			path = path[:len(path)-1]
		}
		if stack[len(stack)-1].left == 0 {
			return corrupt("schema element %d is outside the tree", i)
		}
		top := &stack[len(stack)-1]
		top.left--
		e := el[i]
		def, rep := top.def, top.rep
		switch {
		case !e.hasRep || e.rep == 0:
		case e.rep == 1:
			def++
		case e.rep == 2:
			def++
			rep++
		default:
			return corrupt("repetition type %d", e.rep)
		}
		if e.nch < 0 || int(e.nch) > len(el)-i-1 {
			return corrupt("element %d declares %d children", i, e.nch)
		}
		if e.nch > 0 {
			if len(stack) >= maxSchemaDepth {
				return limitErr("schema nesting")
			}
			stack = append(stack, frame{left: e.nch, def: def, rep: rep})
			path = append(path, e.name)
			continue
		}
		if !e.hasType {
			return corrupt("leaf %q has no physical type", e.name)
		}
		if len(f.Columns) >= f.lim.MaxColumns {
			return limitErr("more than %d columns", f.lim.MaxColumns)
		}
		p := append(append([]string(nil), path...), e.name)
		f.Columns = append(f.Columns, Column{Path: p, Name: strings.Join(p, "."), Phys: int(e.typ), TypeLen: int(e.typeLen), MaxDef: def, MaxRep: rep, Converted: e.conv, Logical: e.logical})
	}
	for len(stack) > 0 && stack[len(stack)-1].left == 0 {
		stack = stack[:len(stack)-1]
	}
	if len(stack) != 0 {
		return corrupt("schema ends with unfilled groups")
	}
	if len(f.Columns) == 0 {
		return corrupt("schema has no leaf columns")
	}
	for _, c := range f.Columns {
		if c.Phys < TBoolean || c.Phys > TFixedLen {
			return corrupt("physical type %d", c.Phys)
		}
		if c.Phys == TFixedLen && (c.TypeLen <= 0 || c.TypeLen > 1<<20) {
			return corrupt("fixed length %d", c.TypeLen)
		}
	}
	return nil
}

func (f *File) parseRowGroup(r *tc.Reader, idx int) (RowGroup, error) {
	rg := RowGroup{}
	err := r.Struct(func(id int16, t tc.Type) error {
		switch {
		case id == 1 && t == tc.List:
			et, n, err := r.ListHeader()
			if err != nil {
				return err
			}
			if et != tc.Struct {
				return corrupt("columns is not a list of structs")
			}
			if n > f.lim.MaxColumns {
				return limitErr("%d chunks in a row group", n)
			}
			rg.Chunks = make([]Chunk, 0, min(n, 256))
			for i := 0; i < n; i++ {
				c, err := parseChunk(r)
				if err != nil {
					return err
				}
				c.RG, c.Col = idx, i
				rg.Chunks = append(rg.Chunks, c)
			}
			return nil
		case id == 3 && t == tc.I64:
			v, err := r.I64()
			rg.NumRows = v
			return err
		}
		return r.Skip(t)
	})
	return rg, err
}

func parseChunk(r *tc.Reader) (Chunk, error) {
	c := Chunk{DictOff: -1, DataOff: -1, ColIdx: [2]int64{-1, -1}, OffIdx: [2]int64{-1, -1}, Bloom: [2]int64{-1, -1}}
	err := r.Struct(func(id int16, t tc.Type) error {
		var err error
		switch {
		case id == 1 && t == tc.Binary:
			var b []byte
			b, err = r.Binary()
			c.FilePath = string(b)
		case id == 3 && t == tc.Struct:
			err = parseColumnMeta(r, &c)
		case id == 4 && t == tc.I64:
			c.OffIdx[0], err = r.I64()
		case id == 5 && t == tc.I32:
			var v int32
			v, err = r.I32()
			c.OffIdx[1] = int64(v)
		case id == 6 && t == tc.I64:
			c.ColIdx[0], err = r.I64()
		case id == 7 && t == tc.I32:
			var v int32
			v, err = r.I32()
			c.ColIdx[1] = int64(v)
		case id == 8 || id == 9:
			err = unsupported("encrypted column chunk")
		default:
			err = r.Skip(t)
		}
		return err
	})
	return c, err
}

func parseColumnMeta(r *tc.Reader, c *Chunk) error {
	return r.Struct(func(id int16, t tc.Type) error {
		var err error
		switch {
		case id == 2 && t == tc.List:
			et, n, err := r.ListHeader()
			if err != nil {
				return err
			}
			if et != tc.I32 || n > 64 {
				return corrupt("encodings list")
			}
			for i := 0; i < n; i++ {
				v, err := r.I32()
				if err != nil {
					return err
				}
				c.Encodings = append(c.Encodings, v)
			}
			return nil
		case id == 4 && t == tc.I32:
			var v int32
			v, err = r.I32()
			c.Codec = int(v)
		case id == 5 && t == tc.I64:
			c.NumValues, err = r.I64()
		case id == 6 && t == tc.I64:
			c.TotalUnc, err = r.I64()
		case id == 7 && t == tc.I64:
			c.TotalComp, err = r.I64()
		case id == 9 && t == tc.I64:
			c.DataOff, err = r.I64()
		case id == 11 && t == tc.I64:
			c.DictOff, err = r.I64()
		case id == 12 && t == tc.Struct:
			c.HasStats = true
			err = r.Skip(t)
		case id == 13 && t == tc.List:
			c.HasEncStats = true
			err = r.Skip(t)
		case id == 14 && t == tc.I64:
			c.Bloom[0], err = r.I64()
		case id == 15 && t == tc.I32:
			var v int32
			v, err = r.I32()
			c.Bloom[1] = int64(v)
		case id == 16 && t == tc.Struct:
			c.HasSizeStats = true
			err = r.Skip(t)
		default:
			err = r.Skip(t)
		}
		return err
	})
}

// validate checks every offset and size the footer declares against the file.
func (f *File) validate() error {
	lim := f.lim
	total := 0
	for gi := range f.RowGroups {
		rg := &f.RowGroups[gi]
		if len(rg.Chunks) != len(f.Columns) {
			return corrupt("row group %d has %d chunks for %d columns", gi, len(rg.Chunks), len(f.Columns))
		}
		total += len(rg.Chunks)
		if total > lim.MaxChunks {
			return limitErr("more than %d column chunks", lim.MaxChunks)
		}
		for ci := range rg.Chunks {
			c := &rg.Chunks[ci]
			if c.FilePath != "" {
				return unsupported("column chunk %d/%d is in another file", gi, ci)
			}
			if c.DataOff < 0 {
				return corrupt("chunk %d/%d has no data page offset", gi, ci)
			}
			c.Start = c.DataOff
			if c.DictOff > 0 && (c.DictOff < c.Start || c.Start <= 0) {
				c.Start = c.DictOff // an unset (0) data_page_offset must not hide a dictionary offset
			}
			if c.TotalComp < 0 || c.NumValues < 0 {
				return corrupt("chunk %d/%d has negative sizes", gi, ci)
			}
			if c.Start < 4 || c.Start > f.FooterOff || c.TotalComp > f.FooterOff-c.Start {
				return corrupt("chunk %d/%d spans [%d,+%d) outside the data area [4,%d)", gi, ci, c.Start, c.TotalComp, f.FooterOff)
			}
			c.End = c.Start + c.TotalComp
			for _, rg := range [][2]int64{c.ColIdx, c.OffIdx, c.Bloom} {
				// a bloom filter may declare an offset without a length (the length is then unknown)
				if rg[0] >= 0 && (rg[0] < 4 || rg[0] > f.FooterOff || rg[1] > f.FooterOff-rg[0]) {
					return corrupt("chunk %d/%d has an index or filter outside the file", gi, ci)
				}
			}
		}
	}
	return nil
}

// SpanKind classifies an exact span of the file.
type SpanKind uint8

const (
	SpanMagic  SpanKind = iota // the 4-byte head
	SpanChunk                  // a column chunk's pages (headers and payloads)
	SpanGap                    // bytes between chunks: page indexes, bloom filters, anything else
	SpanFooter                 // FileMetaData, footer length, tail magic
)

func (k SpanKind) String() string { return [...]string{"magic", "chunk", "gap", "footer"}[k] }

// Span is one non-overlapping piece of the file; the spans of a file tile [0, Size).
type Span struct {
	Off, Len int64
	Kind     SpanKind
	Chunk    *Chunk // for SpanChunk
}

// Spans decomposes the file into exact spans in file order: head, column chunks (sorted
// by offset), the gaps between them and the footer. Chunks that overlap are an error.
func (f *File) Spans() ([]Span, error) {
	var cs []*Chunk
	for gi := range f.RowGroups {
		for ci := range f.RowGroups[gi].Chunks {
			cs = append(cs, &f.RowGroups[gi].Chunks[ci])
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Start < cs[j].Start })
	out := []Span{{0, 4, SpanMagic, nil}}
	pos := int64(4)
	for _, c := range cs {
		if c.TotalComp == 0 {
			continue
		}
		if c.Start < pos {
			return nil, unsupported("column chunks overlap at offset %d", c.Start)
		}
		if c.Start > pos {
			out = append(out, Span{pos, c.Start - pos, SpanGap, nil})
		}
		out = append(out, Span{c.Start, c.TotalComp, SpanChunk, c})
		pos = c.End
	}
	if pos > f.FooterOff {
		return nil, corrupt("chunks run into the footer")
	}
	if pos < f.FooterOff {
		out = append(out, Span{pos, f.FooterOff - pos, SpanGap, nil})
	}
	out = append(out, Span{f.FooterOff, f.Size - f.FooterOff, SpanFooter, nil})
	return out, nil
}

// Page is one parsed PageHeader plus where it sits in the file.
type Page struct {
	Off       int64 // offset of the raw header
	HdrLen    int   // bytes of the raw header
	Type      int32
	Uncomp    int32
	Comp      int32
	HasCRC    bool
	CRC       int32
	NumValues int32
	Enc       int32
	DefEnc    int32
	RepEnc    int32
	NumNulls  int32 // v2
	NumRows   int32 // v2
	DefLen    int32 // v2
	RepLen    int32 // v2
	IsComp    bool  // v2 is_compressed (true when absent)
	HasStats  bool
}

// PayloadOff is the offset of the page's payload.
func (p *Page) PayloadOff() int64 { return p.Off + int64(p.HdrLen) }

// End is the offset after the payload.
func (p *Page) End() int64 { return p.PayloadOff() + int64(p.Comp) }

// ParsePageHeader parses one PageHeader from b (which may extend past it) and returns
// the page and the header length. ErrShort-wrapping errors mean b ended inside it.
func ParsePageHeader(b []byte, lim Limits) (*Page, error) {
	lim = lim.norm()
	r := tc.NewReader(b, lim.Thrift)
	p := &Page{IsComp: true}
	var have [4]bool // type, uncompressed, compressed, one of the page-type structs
	err := r.Struct(func(id int16, t tc.Type) error {
		var err error
		switch {
		case id == 1 && t == tc.I32:
			p.Type, err = r.I32()
			have[0] = true
		case id == 2 && t == tc.I32:
			p.Uncomp, err = r.I32()
			have[1] = true
		case id == 3 && t == tc.I32:
			p.Comp, err = r.I32()
			have[2] = true
		case id == 4 && t == tc.I32:
			p.CRC, err = r.I32()
			p.HasCRC = true
		case id == 5 && t == tc.Struct:
			have[3] = true
			err = r.Struct(func(id int16, t tc.Type) error {
				var err error
				switch {
				case id == 1 && t == tc.I32:
					p.NumValues, err = r.I32()
				case id == 2 && t == tc.I32:
					p.Enc, err = r.I32()
				case id == 3 && t == tc.I32:
					p.DefEnc, err = r.I32()
				case id == 4 && t == tc.I32:
					p.RepEnc, err = r.I32()
				case id == 5 && t == tc.Struct:
					p.HasStats = true
					err = r.Skip(t)
				default:
					err = r.Skip(t)
				}
				return err
			})
		case id == 6 && t == tc.Struct:
			have[3] = true
			err = r.Skip(t)
		case id == 7 && t == tc.Struct:
			have[3] = true
			err = r.Struct(func(id int16, t tc.Type) error {
				var err error
				switch {
				case id == 1 && t == tc.I32:
					p.NumValues, err = r.I32()
				case id == 2 && t == tc.I32:
					p.Enc, err = r.I32()
				default:
					err = r.Skip(t)
				}
				return err
			})
		case id == 8 && t == tc.Struct:
			have[3] = true
			err = r.Struct(func(id int16, t tc.Type) error {
				var err error
				switch {
				case id == 1 && t == tc.I32:
					p.NumValues, err = r.I32()
				case id == 2 && t == tc.I32:
					p.NumNulls, err = r.I32()
				case id == 3 && t == tc.I32:
					p.NumRows, err = r.I32()
				case id == 4 && t == tc.I32:
					p.Enc, err = r.I32()
				case id == 5 && t == tc.I32:
					p.DefLen, err = r.I32()
				case id == 6 && t == tc.I32:
					p.RepLen, err = r.I32()
				case id == 7 && (t == tc.BoolTrue || t == tc.BoolFalse):
					p.IsComp = t == tc.BoolTrue
				case id == 8 && t == tc.Struct:
					p.HasStats = true
					err = r.Skip(t)
				default:
					err = r.Skip(t)
				}
				return err
			})
		default:
			err = r.Skip(t)
		}
		return err
	})
	if err != nil {
		return nil, wrapTC(err)
	}
	if !have[0] || !have[1] || !have[2] || !have[3] {
		return nil, corrupt("PageHeader lacks type, sizes or its page-type struct")
	}
	p.HdrLen = r.Pos()
	if p.Type < PageData || p.Type > PageDataV2 {
		return nil, corrupt("page type %d", p.Type)
	}
	if p.Uncomp < 0 || p.Comp < 0 || int64(p.Uncomp) > lim.MaxPage || int64(p.Comp) > lim.MaxPage {
		return nil, limitErr("page of %d/%d bytes", p.Comp, p.Uncomp)
	}
	if p.NumValues < 0 {
		return nil, corrupt("page of %d values", p.NumValues)
	}
	if p.Type == PageDataV2 {
		if p.DefLen < 0 || p.RepLen < 0 || int64(p.DefLen)+int64(p.RepLen) > int64(p.Uncomp) || int64(p.DefLen)+int64(p.RepLen) > int64(p.Comp) {
			return nil, corrupt("v2 level lengths %d+%d in a page of %d/%d", p.DefLen, p.RepLen, p.Comp, p.Uncomp)
		}
	}
	return p, nil
}

// Pages walks the pages of a chunk in file order. The pages must tile [Start, End)
// exactly; a header or payload that crosses End is an error. fn gets the page with
// Off/HdrLen set; the raw header is hdr (valid only during the call).
func (f *File) Pages(r io.ReaderAt, c *Chunk, fn func(p *Page, hdr []byte) error) error {
	off := c.Start
	win := make([]byte, 0, 512)
	n := 0
	for off < c.End {
		if n++; n > f.lim.MaxPages {
			return limitErr("more than %d pages", f.lim.MaxPages)
		}
		want := 512
		var p *Page
		for {
			w := int64(want)
			if w > c.End-off {
				w = c.End - off
			}
			if int64(cap(win)) < w {
				win = make([]byte, 0, w)
			}
			win = win[:w]
			if err := readFull(r, win, off); err != nil {
				return err
			}
			var err error
			p, err = ParsePageHeader(win, f.lim)
			if err == nil {
				break
			}
			if errors.Is(err, tc.ErrShort) && w < c.End-off && want < f.lim.MaxHeader {
				want = min(want*4, f.lim.MaxHeader)
				continue
			}
			if errors.Is(err, tc.ErrShort) {
				return corrupt("page header at %d runs past its column chunk", off)
			}
			return err
		}
		p.Off = off
		if p.End() > c.End || p.End() < p.Off {
			return corrupt("page at %d (%d header + %d payload bytes) runs past its chunk end %d", off, p.HdrLen, p.Comp, c.End)
		}
		if err := fn(p, win[:p.HdrLen]); err != nil {
			return err
		}
		off = p.End()
	}
	if off != c.End {
		return corrupt("pages end at %d, chunk ends at %d", off, c.End)
	}
	return nil
}

// ReadPayload reads the compressed payload of p.
func ReadPayload(r io.ReaderAt, p *Page) ([]byte, error) {
	b := make([]byte, p.Comp)
	if err := readFull(r, b, p.PayloadOff()); err != nil {
		return nil, err
	}
	return b, nil
}
