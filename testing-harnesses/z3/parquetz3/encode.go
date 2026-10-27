package parquetz3

import (
	"crypto/sha256"
	"errors"
	"io"
)

// Options configures Encode.
type Options struct {
	Limits Limits
	// Target is the leaf group size in bytes for CutContent (default 8 KiB).
	Target int
	// Cut selects how a column stream is cut into groups (default CutContent).
	Cut        CutMode
	FixedCount int // CutFixed: records per group
	// NoPredict forces explicit compression recipes, layouts and dictionaries (measurement only).
	NoPredict bool
	// FrameInline is the largest framing region kept inline in the recipe (default 16 KiB).
	FrameInline int
	// ColumnClasses labels value and level components "val:<column>" / "lvl:<column>" instead
	// of "val" / "lvl" (accounting only; identity and bytes are unchanged).
	ColumnClasses bool
}

func (o *Options) norm() {
	if o.Target <= 0 {
		o.Target = 8 << 10
	}
	if o.FrameInline <= 0 {
		o.FrameInline = 16 << 10
	}
	if o.FixedCount <= 0 {
		o.FixedCount = 256
	}
}

// Stats describes one encoding.
type Stats struct {
	FileBytes                                    int64
	LogicalChunks, OpaqueChunks                  int
	LogicalBytes, OpaqueBytes                    int64 // chunk bytes (headers included)
	OpaqueWhy                                    map[string]int64
	Pages, LogicalPages                          int
	PagePayload                                  int64 // payload bytes of logical pages
	CompPages                                    map[string]int
	CompBytes                                    map[string]int64 // payload bytes by compression id
	CompRecBytes                                 int64            // explicit compression recipe bytes
	LayoutStd, LayoutLvl, LayoutVal, LayoutWidth int
	DictPredicted, DictExplicit                  int
	ValueGroups, LevelGroups                     int64
	ValueRecords, LevelRecords                   int64
	ValueBytes, LevelBytes                       int64 // logical stream bytes
	Nodes                                        int
	Members                                      int
	ClassBytes                                   map[string]int64 // unique component bytes by class
	ClassCount                                   map[string]int
}

// Encode analyses one Parquet file and returns the serialized recipe (head and member
// table) and the components it needs (through emit). An error wrapping ErrUnsupported,
// ErrCorrupt or ErrLimit means the file is not expressible and should be stored by CDC.
func Encode(r io.ReaderAt, size int64, opt Options, emit Emit) ([]byte, Sizes, Stats, error) {
	opt.norm()
	st := Stats{FileBytes: size, OpaqueWhy: map[string]int64{}, CompPages: map[string]int{}, CompBytes: map[string]int64{},
		ClassBytes: map[string]int64{}, ClassCount: map[string]int{}}
	f, err := Open(r, size, opt.Limits)
	if err != nil {
		return nil, Sizes{}, st, err
	}
	spans, err := f.Spans()
	if err != nil {
		return nil, Sizes{}, st, err
	}
	e := &encoder{f: f, r: r, opt: &opt, st: &st, user: emit, members: map[ID]int{}, seen: map[ID]bool{}}
	e.an = NewAnalyzer(r, f, opt.Limits)
	e.an.NoPredict = opt.NoPredict
	e.rec = &Recipe{Size: size, Cols: make([]ColRec, len(f.Columns))}
	e.vs = make([]*streamWriter, len(f.Columns))
	e.ls = make([]*streamWriter, len(f.Columns))
	for i, c := range f.Columns {
		e.rec.Cols[i] = ColRec{Phys: c.Phys, TypeLen: c.TypeLen, MaxDef: c.MaxDef}
	}
	for _, s := range spans {
		if err := e.span(s); err != nil {
			return nil, Sizes{}, st, err
		}
	}
	if st.LogicalChunks == 0 {
		return nil, Sizes{}, st, unsupported("no column chunk is logically decodable")
	}
	for ci := range e.rec.Cols {
		c := &e.rec.Cols[ci]
		if e.vs[ci] == nil {
			continue
		}
		if c.HasL {
			if err := e.an.Runs(ci).finish(e.ls[ci].addRun); err != nil {
				return nil, Sizes{}, st, err
			}
		}
		for _, sw := range []struct {
			w  *streamWriter
			to *[]nodeMember
		}{{e.vs[ci], &c.Vm}, {e.ls[ci], &c.Lm}} {
			if sw.w == nil {
				continue
			}
			if err := sw.w.finish(); err != nil {
				return nil, Sizes{}, st, err
			}
			for _, n := range sw.w.nodes {
				*sw.to = append(*sw.to, nodeMember{e.member(n.id), n.items})
			}
			st.Nodes += len(sw.w.nodes)
		}
		st.ValueGroups += e.vs[ci].Groups
		st.ValueRecords += e.vs[ci].Records
		st.ValueBytes += e.vs[ci].GroupBytes
		if e.ls[ci] != nil {
			st.LevelGroups += e.ls[ci].Groups
			st.LevelRecords += e.ls[ci].Records
			st.LevelBytes += e.ls[ci].GroupBytes
		}
	}
	e.rec.Digests = e.digests
	st.Members = len(e.digests)
	b, sz := e.rec.Marshal()
	return b, sz, st, nil
}

type encoder struct {
	f       *File
	r       io.ReaderAt
	opt     *Options
	st      *Stats
	an      *Analyzer
	user    Emit
	rec     *Recipe
	vs, ls  []*streamWriter
	members map[ID]int
	digests []ID
	seen    map[ID]bool
}

func (s *streamWriter) addRun(rec []byte, items int) error { return s.add(rec, items) }

// out forwards a component to the caller and accounts its class once per unique digest.
func (e *encoder) out(class string, id ID, data []byte) error {
	if !e.seen[id] {
		e.seen[id] = true
		e.st.ClassBytes[class] += int64(len(data))
		e.st.ClassCount[class]++
	}
	return e.user(class, id, data)
}

func (e *encoder) member(id ID) int {
	if i, ok := e.members[id]; ok {
		return i
	}
	e.members[id] = len(e.digests)
	e.digests = append(e.digests, id)
	return len(e.digests) - 1
}

func (e *encoder) component(class string, b []byte) (int, error) {
	id := ID(sha256.Sum256(b))
	if err := e.out(class, id, b); err != nil {
		return 0, err
	}
	return e.member(id), nil
}

func (e *encoder) read(off, n int64) ([]byte, error) {
	b := make([]byte, n)
	if err := readFull(e.r, b, off); err != nil {
		return nil, err
	}
	return b, nil
}

func (e *encoder) span(s Span) error {
	switch s.Kind {
	case SpanChunk:
		return e.chunk(s.Chunk)
	}
	b, err := e.read(s.Off, s.Len)
	if err != nil {
		return err
	}
	if int64(len(b)) <= int64(e.opt.FrameInline) {
		e.rec.Segs = append(e.rec.Segs, Seg{Kind: SegFrame, Frame: b})
		return nil
	}
	m, err := e.component("frame", b)
	if err != nil {
		return err
	}
	e.rec.Segs = append(e.rec.Segs, Seg{Kind: SegFrameComp, Member: m, Len: s.Len})
	return nil
}

func (e *encoder) streams(ci int) error {
	if e.vs[ci] != nil {
		return nil
	}
	col := &e.rec.Cols[ci]
	vc, lc := "val", "lvl"
	if e.opt.ColumnClasses {
		vc, lc = "val:"+e.f.Columns[ci].Name, "lvl:"+e.f.Columns[ci].Name
	}
	e.vs[ci] = newStream(vc, e.opt, e.out)
	col.HasV = true
	if col.MaxDef > 0 {
		e.ls[ci] = newStream(lc, e.opt, e.out)
		col.HasL = true
	}
	return nil
}

func (e *encoder) chunk(c *Chunk) error {
	plan, err := e.an.Chunk(c, nil, nil)
	if err != nil {
		return err
	}
	if plan.Opaque {
		return e.opaque(c, plan)
	}
	ci := c.Col
	if err := e.streams(ci); err != nil {
		return err
	}
	e.st.LogicalChunks++
	e.st.LogicalBytes += c.TotalComp
	switch plan.DictMode {
	case DictPredicted:
		e.st.DictPredicted++
	case DictExplicit:
		e.st.DictExplicit++
	}
	col := &e.rec.Cols[ci]
	sk := &sink{
		value: func(rec []byte) error { return e.vs[ci].add(rec, 1) },
		page: func(i int, pi *PageInfo, values []byte, comp CompRec, hdr []byte) error {
			s := Seg{Col: ci, Hdr: append([]byte(nil), hdr...), Comp: comp.ID, Lay: pi.Lay, Present: pi.Present}
			var err error
			if compNeedsMember(comp.ID) {
				cb := comp.Cmds
				if comp.ID == CompZstd {
					cb = comp.Zstd
				}
				if s.CompMember, err = e.component("crec", cb); err != nil {
					return err
				}
				e.st.CompRecBytes += int64(len(cb))
			}
			e.st.Pages++
			e.st.LogicalPages++
			e.st.PagePayload += int64(pi.Page.Comp)
			e.st.CompPages[CompName(int(comp.ID))]++
			e.st.CompBytes[CompName(int(comp.ID))] += int64(pi.Page.Comp)
			if pi.Mode == ModeDict {
				s.Kind = SegDict
				if plan.DictMode == DictExplicit {
					s.DictExplicit = true
					if s.Member, err = e.component("dict", values); err != nil {
						return err
					}
				}
			} else {
				s.Kind = SegData
				l, v := pi.Lay.Explicit()
				switch {
				case pi.Lay.WidthOnly() && !l:
					e.st.LayoutWidth++
				case l || v:
					if l {
						e.st.LayoutLvl++
					}
					if v {
						e.st.LayoutVal++
					}
				default:
					e.st.LayoutStd++
				}
			}
			e.rec.Segs = append(e.rec.Segs, s)
			if err := e.vs[ci].endPage(); err != nil {
				return err
			}
			if e.ls[ci] != nil {
				return e.ls[ci].endPage()
			}
			return nil
		},
	}
	if col.HasL {
		sk.level = e.ls[ci].addRun
	}
	_, err = e.an.Chunk(c, plan, sk)
	return err
}

// opaque stores the pages of a chunk that cannot be decoded: raw headers inline, every
// payload one component.
func (e *encoder) opaque(c *Chunk, plan *ChunkPlan) error {
	e.st.OpaqueChunks++
	e.st.OpaqueBytes += c.TotalComp
	why := plan.Reason
	if len(why) > 60 {
		why = why[:60]
	}
	e.st.OpaqueWhy[why] += c.TotalComp
	return e.f.Pages(e.r, c, func(p *Page, hdr []byte) error {
		b, err := ReadPayload(e.r, p)
		if err != nil {
			return err
		}
		m, err := e.component("opq", b)
		if err != nil {
			return err
		}
		e.st.Pages++
		e.rec.Segs = append(e.rec.Segs, Seg{Kind: SegOpaque, Hdr: append([]byte(nil), hdr...), Member: m})
		return nil
	})
}

// Supported reports whether err means "not expressible: store by CDC".
func Supported(err error) bool {
	return !(errors.Is(err, ErrUnsupported) || errors.Is(err, ErrCorrupt) || errors.Is(err, ErrLimit))
}
