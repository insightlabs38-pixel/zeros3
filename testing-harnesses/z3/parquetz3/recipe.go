package parquetz3

import (
	"encoding/binary"
	"math/bits"
)

// Segment kinds of the recipe: the file is the concatenation of its segments in order.
const (
	SegFrame     uint8 = 1 // opaque framing bytes, inline (magic, small gaps, the footer)
	SegFrameComp uint8 = 2 // opaque framing bytes as a component (page index, bloom filter, a big footer)
	SegOpaque    uint8 = 3 // a page of an opaque chunk: raw header inline, payload a component
	SegData      uint8 = 4 // a logical data page: raw header inline, body from the column streams
	SegDict      uint8 = 5 // a logical dictionary page
)

// Seg is one segment of the recipe.
type Seg struct {
	Kind         uint8
	Frame        []byte // SegFrame
	Member       int    // SegFrameComp / SegOpaque payload; SegDict explicit dictionary body
	Len          int64  // SegFrameComp length
	Col          int    // SegData / SegDict
	Hdr          []byte // raw PageHeader bytes (SegOpaque, SegData, SegDict)
	Comp         uint8  // compression id
	CompMember   int    // member of the explicit compression recipe (CompSnapCmds, CompZstd)
	Lay          layout
	Present      int  // non-null values of a SegData page
	DictExplicit bool // SegDict: the dictionary body is a component

	// derived by ParseRecipe (never serialized)
	Page       *Page
	Off, Total int64
	VPos, LPos uint64
	DictSeg    int
}

// ColRec is the column table entry: the physical type and the roots of the column's value
// and level streams.
type ColRec struct {
	Phys, TypeLen, MaxDef int
	HasV, HasL            bool
	Vm, Lm                []nodeMember // member index of every root node
	V, L                  streamRef    // resolved by ParseRecipe
}

type nodeMember struct {
	member int
	items  uint64
}

// Recipe is the decoded representation head plus its member table.
type Recipe struct {
	Size    int64
	Cols    []ColRec
	Segs    []Seg
	Digests []ID
}

// Sizes breaks the serialized recipe head down by purpose (bytes).
type Sizes struct {
	Frames   int64 // inline opaque framing (magic, footer, small gaps)
	Headers  int64 // raw page headers, with their length prefixes
	Layout   int64 // per-page flags, non-null counts and explicit layouts
	CompRefs int64 // compression ids and member references
	Columns  int64 // column table and stream roots
	Other    int64 // version, counts, segment kinds, framing references
	Digests  int64 // the member table (32 B per member)
}

// Total is the whole recipe including the member table.
func (s Sizes) Total() int64 {
	return s.Frames + s.Headers + s.Layout + s.CompRefs + s.Columns + s.Other + s.Digests
}

const recipeVersion = 1

func appendExplicit(b []byte, hdrs []uint64, pad []uint32, bw int) []byte {
	b = binary.AppendUvarint(b, uint64(len(hdrs)))
	for _, h := range hdrs {
		b = binary.AppendUvarint(b, h)
	}
	b = binary.AppendUvarint(b, uint64(len(pad)))
	var w bitWriter
	for _, v := range pad {
		w.put(v, bw)
	}
	w.flush()
	return append(b, w.out...)
}

func flagsOf(s *Seg) byte {
	f := s.Comp << 2
	if s.Lay.LvlExpl {
		f |= 1
	}
	if s.Lay.ValExpl {
		f |= 2
	}
	if s.DictExplicit {
		f |= 64
	}
	if s.Lay.ValWidth {
		f |= 128
	}
	return f
}

func compNeedsMember(id uint8) bool { return id == CompSnapCmds || id == CompZstd }

// Marshal serializes the recipe: the head, then the member table.
func (r *Recipe) Marshal() ([]byte, Sizes) {
	var sz Sizes
	b := []byte{recipeVersion}
	b = binary.AppendUvarint(b, uint64(r.Size))
	b = binary.AppendUvarint(b, uint64(len(r.Digests)))
	b = binary.AppendUvarint(b, uint64(len(r.Cols)))
	mark := len(b)
	for i := range r.Cols {
		c := &r.Cols[i]
		b = append(b, byte(c.Phys))
		b = binary.AppendUvarint(b, uint64(c.TypeLen))
		fl := byte(c.MaxDef)
		if c.HasV {
			fl |= 0x40
		}
		if c.HasL {
			fl |= 0x80
		}
		b = append(b, fl)
		for _, st := range [2]struct {
			has bool
			n   []nodeMember
		}{{c.HasV, c.Vm}, {c.HasL, c.Lm}} {
			if !st.has {
				continue
			}
			b = binary.AppendUvarint(b, uint64(len(st.n)))
			for _, n := range st.n {
				b = binary.AppendUvarint(b, uint64(n.member))
				b = binary.AppendUvarint(b, n.items)
			}
		}
	}
	sz.Columns = int64(len(b) - mark)
	b = binary.AppendUvarint(b, uint64(len(r.Segs)))
	for i := range r.Segs {
		s := &r.Segs[i]
		start := len(b)
		b = append(b, s.Kind)
		switch s.Kind {
		case SegFrame:
			b = binary.AppendUvarint(b, uint64(len(s.Frame)))
			b = append(b, s.Frame...)
			sz.Frames += int64(len(b) - start - 1)
		case SegFrameComp:
			b = binary.AppendUvarint(binary.AppendUvarint(b, uint64(s.Member)), uint64(s.Len))
		case SegOpaque:
			b = binary.AppendUvarint(b, uint64(len(s.Hdr)))
			b = append(b, s.Hdr...)
			sz.Headers += int64(len(b) - start - 1)
			b = binary.AppendUvarint(b, uint64(s.Member))
		case SegData, SegDict:
			b = binary.AppendUvarint(b, uint64(s.Col))
			m := len(b)
			b = binary.AppendUvarint(b, uint64(len(s.Hdr)))
			b = append(b, s.Hdr...)
			sz.Headers += int64(len(b) - m)
			m = len(b)
			b = append(b, flagsOf(s))
			if compNeedsMember(s.Comp) {
				b = binary.AppendUvarint(b, uint64(s.CompMember))
			}
			sz.CompRefs += int64(len(b) - m)
			m = len(b)
			if s.Kind == SegDict {
				if s.DictExplicit {
					b = binary.AppendUvarint(b, uint64(s.Member))
				}
			} else {
				col := &r.Cols[s.Col]
				if col.MaxDef > 0 {
					b = binary.AppendUvarint(b, uint64(s.Present))
					if s.Lay.LvlExpl {
						b = appendExplicit(b, s.Lay.LvlHdrs, s.Lay.LvlPad, bits.Len(uint(col.MaxDef)))
					}
				}
				if len(s.Lay.Delta) > 0 {
					b = appendUv(b, uint64(len(s.Lay.Delta)))
					b = append(b, s.Lay.Delta...)
				}
				if s.Lay.ValWidth {
					b = append(b, byte(s.Lay.ValBW))
				}
				if s.Lay.ValExpl {
					b = append(b, byte(s.Lay.ValBW))
					b = appendExplicit(b, s.Lay.ValHdrs, s.Lay.ValPad, s.Lay.ValBW)
				}
			}
			sz.Layout += int64(len(b) - m)
		}
	}
	sz.Other = int64(len(b)) - sz.Frames - sz.Headers - sz.Layout - sz.CompRefs - sz.Columns
	sz.Digests = 32 * int64(len(r.Digests))
	for _, d := range r.Digests {
		b = append(b, d[:]...)
	}
	return b, sz
}

type rrd struct {
	b   []byte
	pos int
	err error
}

func (r *rrd) fail(f string, a ...any) {
	if r.err == nil {
		r.err = recipeErr(f, a...)
	}
}

func (r *rrd) uv(max uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, k := readUv(r.b[r.pos:])
	if k <= 0 {
		r.fail("varint at %d", r.pos)
		return 0
	}
	r.pos += k
	if v > max {
		r.fail("value %d above %d at %d", v, max, r.pos)
		return 0
	}
	return v
}

func (r *rrd) byte() byte {
	if r.err != nil || r.pos >= len(r.b) {
		r.fail("recipe ends early")
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}

func (r *rrd) take(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)-r.pos) {
		r.fail("%d bytes at %d beyond the recipe", n, r.pos)
		return nil
	}
	p := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return p
}

func (r *rrd) explicit(bw int, maxHdrs uint64) (hdrs []uint64, pad []uint32) {
	n := r.uv(maxHdrs)
	if n > uint64(len(r.b)-r.pos) {
		r.fail("%d run headers in %d bytes", n, len(r.b)-r.pos)
		return
	}
	hdrs = make([]uint64, 0, n)
	for i := uint64(0); i < n; i++ {
		hdrs = append(hdrs, r.uv(1<<32-1))
	}
	np := r.uv(1 << 24)
	if bw == 0 && np != 0 || np*uint64(bw) > uint64(len(r.b)-r.pos)*8 {
		r.fail("%d pad values of width %d", np, bw)
		return
	}
	p := r.take((np*uint64(bw) + 7) / 8)
	for i := uint64(0); i < np && r.err == nil; i++ {
		pad = append(pad, unpack(p, int(i), bw))
	}
	return
}

// ParseRecipe decodes and validates a recipe (head followed by the member table): every
// count is bounded, every member index and page header is checked, and the segment
// lengths must add up to the declared size.
func ParseRecipe(b []byte, lim Limits) (*Recipe, error) {
	lim = lim.norm()
	r := &rrd{b: b}
	if v := r.byte(); r.err == nil && v != recipeVersion {
		return nil, recipeErr("version %d", v)
	}
	out := &Recipe{Size: int64(r.uv(1 << 62))}
	ndig := r.uv(1 << 24)
	ncols := r.uv(uint64(lim.MaxColumns))
	if r.err != nil {
		return nil, r.err
	}
	if ndig*32 > uint64(len(b)) {
		return nil, recipeErr("%d members do not fit", ndig)
	}
	node := func(has bool, total *uint64) []nodeMember {
		if !has {
			return nil
		}
		n := r.uv(1 << 22)
		if n > uint64(len(b)-r.pos) {
			r.fail("%d stream nodes in %d bytes", n, len(b)-r.pos)
			return nil
		}
		out := make([]nodeMember, 0, n)
		for i := uint64(0); i < n && r.err == nil; i++ {
			m := r.uv(ndig)
			if m >= ndig {
				r.fail("stream node member %d of %d", m, ndig)
			}
			it := r.uv(1 << 60)
			*total += it
			out = append(out, nodeMember{int(m), it})
		}
		return out
	}
	var vtot, ltot []uint64
	for i := uint64(0); i < ncols && r.err == nil; i++ {
		var c ColRec
		c.Phys = int(r.byte())
		c.TypeLen = int(r.uv(1 << 20))
		fl := r.byte()
		c.MaxDef, c.HasV, c.HasL = int(fl&0x3f), fl&0x40 != 0, fl&0x80 != 0
		if r.err == nil && (c.Phys > TFixedLen || c.MaxDef > 8 || c.HasL && c.MaxDef == 0 || c.Phys == TFixedLen && c.TypeLen == 0) {
			r.fail("column %d: type %d, max definition level %d", i, c.Phys, c.MaxDef)
		}
		var vt, lt uint64
		c.Vm = node(c.HasV, &vt)
		c.Lm = node(c.HasL, &lt)
		vtot, ltot = append(vtot, vt), append(ltot, lt)
		out.Cols = append(out.Cols, c)
	}
	nsegs := r.uv(1 << 24)
	if nsegs > uint64(len(b)) {
		r.fail("%d segments in %d bytes", nsegs, len(b))
	}
	out.Segs = make([]Seg, 0, min(nsegs, 1<<16))
	vpos := make([]uint64, len(out.Cols))
	lpos := make([]uint64, len(out.Cols))
	lastDict := make([]int, len(out.Cols))
	for i := range lastDict {
		lastDict[i] = -1
	}
	var off int64
	pages := 0
	for i := uint64(0); i < nsegs && r.err == nil; i++ {
		s := Seg{Kind: r.byte(), DictSeg: -1}
		switch s.Kind {
		case SegFrame:
			s.Frame = r.take(r.uv(uint64(lim.MaxFooter)))
			s.Total = int64(len(s.Frame))
		case SegFrameComp:
			s.Member = int(r.uv(ndig))
			s.Len = int64(r.uv(1 << 40))
			s.Total = s.Len
		case SegOpaque, SegData, SegDict:
			if s.Kind != SegOpaque {
				c := r.uv(uint64(len(out.Cols)))
				if r.err == nil && int(c) >= len(out.Cols) {
					r.fail("segment %d names column %d", i, c)
				}
				s.Col = int(c)
			}
			s.Hdr = r.take(r.uv(uint64(lim.MaxHeader)))
			if r.err != nil {
				break
			}
			pg, err := ParsePageHeader(s.Hdr, lim)
			if err != nil || pg.HdrLen != len(s.Hdr) {
				r.fail("segment %d: page header (%v)", i, err)
				break
			}
			s.Page = pg
			s.Total = int64(pg.HdrLen) + int64(pg.Comp)
			if pages++; pages > lim.MaxPages {
				r.fail("more than %d pages", lim.MaxPages)
			}
			if s.Kind == SegOpaque {
				s.Member = int(r.uv(ndig))
				break
			}
			fl := r.byte()
			s.Comp = fl >> 2 & 15
			s.DictExplicit = fl&64 != 0
			s.Lay.LvlExpl, s.Lay.ValExpl, s.Lay.ValWidth = fl&1 != 0, fl&2 != 0, fl&128 != 0
			if r.err == nil && (s.Comp > compMax || s.Lay.ValExpl && s.Lay.ValWidth) {
				r.fail("segment %d: flags %02x", i, fl)
			}
			if compNeedsMember(s.Comp) {
				s.CompMember = int(r.uv(ndig))
			}
			col := &out.Cols[s.Col]
			if s.Kind == SegDict {
				if pg.Type != PageDict {
					r.fail("segment %d: a dictionary segment with page type %d", i, pg.Type)
				}
				if s.DictExplicit {
					s.Member = int(r.uv(ndig))
				}
				if !col.HasV {
					r.fail("segment %d: dictionary of a column without values", i)
				}
				s.VPos = vpos[s.Col]
				lastDict[s.Col] = int(i)
				break
			}
			if pg.Type != PageData && pg.Type != PageDataV2 || !col.HasV {
				r.fail("segment %d: a data segment with page type %d", i, pg.Type)
				break
			}
			n := uint64(pg.NumValues)
			s.Present = int(n)
			if col.MaxDef > 0 {
				if !col.HasL {
					r.fail("segment %d: levels of a column without a level stream", i)
					break
				}
				s.Present = int(r.uv(n))
				if s.Lay.LvlExpl {
					s.Lay.LvlHdrs, s.Lay.LvlPad = r.explicit(bits.Len(uint(col.MaxDef)), 1<<22)
				}
			} else if s.Lay.LvlExpl {
				r.fail("segment %d: explicit levels without levels", i)
			}
			if s.Page.Enc == EncDeltaLenBA {
				s.Lay.Delta = r.take(r.uv(uint64(lim.MaxPage)))
			}
			if s.Lay.ValWidth {
				s.Lay.ValBW = int(r.byte())
				if s.Lay.ValBW > 32 {
					r.fail("segment %d: index width %d", i, s.Lay.ValBW)
				}
			}
			if s.Lay.ValExpl {
				s.Lay.ValBW = int(r.byte())
				if s.Lay.ValBW > 32 {
					r.fail("segment %d: index width %d", i, s.Lay.ValBW)
				}
				s.Lay.ValHdrs, s.Lay.ValPad = r.explicit(s.Lay.ValBW, 1<<22)
			}
			s.VPos, s.LPos, s.DictSeg = vpos[s.Col], lpos[s.Col], lastDict[s.Col]
			vpos[s.Col] += uint64(s.Present)
			if col.MaxDef > 0 {
				lpos[s.Col] += n
			}
		default:
			r.fail("segment %d: kind %d", i, s.Kind)
		}
		s.Off = off
		off += s.Total
		if r.err != nil {
			break
		}
		if off > out.Size {
			r.fail("segments run past the declared size %d", out.Size)
		}
		out.Segs = append(out.Segs, s)
	}
	if r.err != nil {
		return nil, r.err
	}
	if off != out.Size {
		return nil, recipeErr("segments add up to %d bytes, the recipe declares %d", off, out.Size)
	}
	for i := range out.Cols {
		if out.Cols[i].HasV && vpos[i] != vtot[i] {
			return nil, recipeErr("column %d: pages consume %d values, the stream holds %d", i, vpos[i], vtot[i])
		}
		if out.Cols[i].HasL && lpos[i] != ltot[i] {
			return nil, recipeErr("column %d: pages consume %d levels, the stream holds %d", i, lpos[i], ltot[i])
		}
		out.Cols[i].V.total, out.Cols[i].L.total = vtot[i], ltot[i]
	}
	rest := b[r.pos:]
	if uint64(len(rest)) != 32*ndig {
		return nil, recipeErr("%d bytes after the head, %d members declared", len(rest), ndig)
	}
	out.Digests = make([]ID, ndig)
	for i := range out.Digests {
		copy(out.Digests[i][:], rest[32*i:])
	}
	check := func(m int, what string) error {
		if m < 0 || m >= len(out.Digests) {
			return recipeErr("%s member %d", what, m)
		}
		return nil
	}
	for i := range out.Segs {
		s := &out.Segs[i]
		switch s.Kind {
		case SegFrameComp, SegOpaque:
			if err := check(s.Member, "payload"); err != nil {
				return nil, err
			}
		case SegDict:
			if s.DictExplicit {
				if err := check(s.Member, "dictionary"); err != nil {
					return nil, err
				}
			}
		}
		if (s.Kind == SegData || s.Kind == SegDict) && compNeedsMember(s.Comp) {
			if err := check(s.CompMember, "compression"); err != nil {
				return nil, err
			}
		}
	}
	for i := range out.Cols {
		c := &out.Cols[i]
		for _, st := range []struct {
			m  []nodeMember
			to *streamRef
		}{{c.Vm, &c.V}, {c.Lm, &c.L}} {
			for _, n := range st.m {
				st.to.nodes = append(st.to.nodes, nodeRef{out.Digests[n.member], n.items})
			}
		}
	}
	return out, nil
}
