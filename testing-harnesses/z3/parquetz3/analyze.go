package parquetz3

import (
	"errors"
	"fmt"
	"io"
)

// Page and chunk modes of the analysis.
const (
	ModeOpaque uint8 = iota // payload kept as bytes (nothing decoded)
	ModeData                // v1 or v2 data page decoded to logical values and levels
	ModeDict                // dictionary page
)

// Dictionary modes of a chunk.
const (
	DictNone      uint8 = iota
	DictPredicted       // the dictionary is the first N distinct values of the chunk's value stream
	DictExplicit        // the dictionary page body is a component
)

// PageInfo is the verified analysis of one page.
type PageInfo struct {
	Page         Page
	Mode         uint8
	Comp         CompRec // Cmds/Zstd cleared after sizing in pass 1
	CompRecBytes int     // bytes of the explicit compression recipe (0 for predictors)
	Lay          layout
	Present      int
	Body         int64 // uncompressed body bytes (levels + values)
}

// ChunkPlan is the verified plan of one column chunk.
type ChunkPlan struct {
	Chunk       *Chunk
	Opaque      bool
	Reason      string
	Pages       []PageInfo
	DictMode    uint8
	DictEntries int
	dict        *dict
}

// sink receives the logical content of a chunk in pass 2.
type sink struct {
	value func(rec []byte) error
	level func(rec []byte, items int) error
	page  func(i int, pi *PageInfo, body []byte, comp CompRec, hdr []byte) error
}

// Analyzer verifies chunks of one open file.
type Analyzer struct {
	F     *File
	R     io.ReaderAt
	Lim   Limits
	comps map[int]*Compressor
	rs    map[int]*runState // per column, so a level run may span chunks
	// NoPredict forces explicit compression recipes and explicit layouts (measurement only).
	NoPredict bool
}

// NewAnalyzer prepares an analyzer for f.
func NewAnalyzer(r io.ReaderAt, f *File, lim Limits) *Analyzer {
	return &Analyzer{F: f, R: r, Lim: lim.norm(), comps: map[int]*Compressor{}, rs: map[int]*runState{}}
}

func (a *Analyzer) comp(codec int) *Compressor {
	c := a.comps[codec]
	if c == nil {
		c = NewCompressor(codec, a.Lim)
		c.NoPredict = a.NoPredict
		a.comps[codec] = c
	}
	return c
}

// Runs is the level-run state of a column (pass 2).
func (a *Analyzer) Runs(col int) *runState {
	r := a.rs[col]
	if r == nil {
		r = &runState{}
		a.rs[col] = r
	}
	return r
}

func (a *Analyzer) spec(col int) colSpec {
	c := a.F.Columns[col]
	return colSpec{Phys: c.Phys, TypeLen: c.TypeLen, MaxDef: c.MaxDef}
}

// ChunkSupport reports why a chunk cannot be decoded logically ("" when it can).
func (a *Analyzer) ChunkSupport(c *Chunk) string {
	col := a.F.Columns[c.Col]
	spec := a.spec(c.Col)
	switch {
	case col.MaxRep > 0:
		return "repeated (nested) column"
	case col.MaxDef > 8:
		return "definition levels above 8"
	case !Codecs(c.Codec):
		return "codec " + CodecName(c.Codec)
	case c.NumValues > 1<<40:
		return "huge chunk"
	}
	for _, e := range c.Encodings {
		if e != EncRLE && e != EncBitPacked && !encodingSupported(spec, e) {
			return fmt.Sprintf("encoding %s", EncodingName(e))
		}
	}
	return ""
}

// EncodingName names an encoding for audits.
func EncodingName(e int32) string {
	n := map[int32]string{0: "PLAIN", 2: "PLAIN_DICTIONARY", 3: "RLE", 4: "BIT_PACKED", 5: "DELTA_BINARY_PACKED", 6: "DELTA_LENGTH_BYTE_ARRAY", 7: "DELTA_BYTE_ARRAY", 8: "RLE_DICTIONARY", 9: "BYTE_STREAM_SPLIT"}
	if s, ok := n[e]; ok {
		return s
	}
	return fmt.Sprint("ENC", e)
}

// splitBody separates the level bytes from the value section of an uncompressed body
// (v1: the level stream is length-prefixed at the front; v2: the header gives its length,
// and the caller passes the value section separately).
func splitV1(c colSpec, body []byte) (levels, values []byte, err error) {
	if c.MaxDef == 0 {
		return nil, body, nil
	}
	if len(body) < 4 {
		return nil, nil, corrupt("v1 level length prefix")
	}
	l := int64(uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24)
	if l > int64(len(body)-4) {
		return nil, nil, corrupt("v1 level stream of %d bytes in a body of %d", l, len(body))
	}
	return body[:4+l], body[4+l:], nil
}

// readPage fetches a page's payload and decompresses it with exact-replay analysis. For
// v2 pages the level bytes are returned apart and only the value section is compressed.
func (a *Analyzer) readPage(c *Chunk, p *Page, spec colSpec) (levels, values []byte, rec CompRec, err error) {
	payload, err := ReadPayload(a.R, p)
	if err != nil {
		return nil, nil, rec, err
	}
	cp := a.comp(c.Codec)
	if p.Type == PageDataV2 {
		if p.RepLen != 0 {
			return nil, nil, rec, unsupported("repetition levels in a flat column")
		}
		lv := int64(p.DefLen)
		if (spec.MaxDef == 0) != (lv == 0) {
			return nil, nil, rec, corrupt("v2 page level bytes %d for max definition level %d", lv, spec.MaxDef)
		}
		sect := payload[lv:]
		if !p.IsComp {
			return payload[:lv], sect, CompRec{ID: CompNone}, nil
		}
		body, rec, err := cp.Analyze(sect, int64(p.Uncomp)-lv)
		return payload[:lv], body, rec, err
	}
	body, rec, err := cp.Analyze(payload, int64(p.Uncomp))
	if err != nil {
		return nil, nil, rec, err
	}
	if p.Type == PageDict {
		return nil, body, rec, nil
	}
	lv, vals, err := splitV1(spec, body)
	return lv, vals, rec, err
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Chunk analyses one column chunk: it decides logical or opaque, verifies every page's
// layout predictor and compression recipe by re-encoding and comparing bytes, and, when
// sk is set (pass 2), delivers the logical content to the sink in file order. The plan is
// the same with and without a sink.
func (a *Analyzer) Chunk(c *Chunk, plan *ChunkPlan, sk *sink) (*ChunkPlan, error) {
	if plan == nil {
		plan = &ChunkPlan{Chunk: c}
		if why := a.ChunkSupport(c); why != "" {
			plan.Opaque, plan.Reason = true, why
			return plan, nil
		}
		err := a.chunkPass(c, plan, nil)
		var ue *opaqueErr
		if errors.As(err, &ue) {
			*plan = ChunkPlan{Chunk: c, Opaque: true, Reason: ue.why}
			return plan, nil
		}
		return plan, err
	}
	if plan.Opaque {
		return plan, nil
	}
	return plan, a.chunkPass(c, plan, sk)
}

// opaqueErr marks "this chunk cannot be a logical chunk" (as opposed to a corrupt file).
type opaqueErr struct{ why string }

func (e *opaqueErr) Error() string { return "opaque chunk: " + e.why }

func opq(f string, a ...any) error { return &opaqueErr{fmt.Sprintf(f, a...)} }

func (a *Analyzer) chunkPass(c *Chunk, plan *ChunkPlan, sk *sink) error {
	spec := a.spec(c.Col)
	pass1 := plan.Pages == nil
	var d *dict
	rs := a.Runs(c.Col)
	nextIdx, firstPlain, predicted := 0, false, true
	pi := 0
	if !pass1 {
		d = plan.dict
	}
	err := a.F.Pages(a.R, c, func(p *Page, hdr []byte) error {
		defer func() { pi++ }()
		if pass1 && pi >= a.Lim.MaxPages {
			return limitErr("pages")
		}
		levels, values, rec, err := a.readPage(c, p, spec)
		if err != nil {
			if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrCorrupt) {
				return opq("page %d: %v", pi, err)
			}
			return err
		}
		info := PageInfo{Page: *p, Comp: rec}
		if rec.ID == CompSnapCmds {
			info.CompRecBytes = len(rec.Cmds)
		}
		if rec.ID == CompZstd {
			info.CompRecBytes = len(rec.Zstd)
		}
		switch p.Type {
		case PageDict:
			if pi != 0 || d != nil && pass1 {
				return opq("dictionary page is not the first page")
			}
			if p.NumValues > MaxPageValues || spec.Phys == TBoolean {
				return opq("dictionary of %d values for type %d", p.NumValues, spec.Phys)
			}
			if int64(len(values)) > a.Lim.MaxDict {
				return opq("dictionary of %d bytes", len(values))
			}
			r, used, err := decodePlain(spec, values, int(p.NumValues), a.Lim)
			if err != nil || used != len(values) {
				return opq("dictionary page body: %v", err)
			}
			if p.Enc != EncPlain && p.Enc != EncPlainDict {
				return opq("dictionary page encoding %s", EncodingName(p.Enc))
			}
			if pass1 {
				d = newDict(r)
				plan.dict = d
				plan.DictEntries = r.n
			}
			info.Mode = ModeDict
			info.Body = int64(len(values))
		case PageData, PageDataV2:
			if p.Type == PageData && spec.MaxDef > 0 && p.DefEnc != EncRLE {
				return opq("definition levels use encoding %s", EncodingName(p.DefEnc))
			}
			if !encodingSupported(spec, p.Enc) {
				return opq("page encoding %s", EncodingName(p.Enc))
			}
			pc, lay, err := decodePage(p, spec, d, levels, values, a.Lim)
			if err != nil {
				if errors.Is(err, ErrUnsupported) {
					return opq("page %d: %v", pi, err)
				}
				return opq("page %d does not decode: %v", pi, err)
			}
			if p.Type == PageDataV2 && int(p.NumNulls) != pc.n-pc.present {
				return opq("v2 num_nulls %d, levels say %d", p.NumNulls, pc.n-pc.present)
			}
			std := layout{}
			if a.NoPredict {
				std = lay
				std.LvlExpl, std.ValExpl = spec.MaxDef > 0, dictEnc(p.Enc) || p.Enc == EncRLE
			}
			lb, ok1 := encodeLevels(p, spec, pc.defs, std)
			vb, ok2 := encodeValues(p, spec, d, &pc, std)
			if !ok1 || !ok2 || !sameBytes(lb, levels) || !sameBytes(vb, values) {
				ex := lay
				ex.LvlExpl = spec.MaxDef > 0 && (!ok1 || !sameBytes(lb, levels))
				ex.ValExpl = dictEnc(p.Enc) || p.Enc == EncRLE
				ex.ValExpl = ex.ValExpl && (!ok2 || !sameBytes(vb, values))
				lb, ok1 = encodeLevels(p, spec, pc.defs, ex)
				vb, ok2 = encodeValues(p, spec, d, &pc, ex)
				if !ok1 || !ok2 || !sameBytes(lb, levels) || !sameBytes(vb, values) {
					return opq("page %d layout is not reproducible", pi)
				}
				std = ex
			}
			if !std.LvlExpl {
				std.LvlHdrs, std.LvlPad = nil, nil
			}
			if !std.ValExpl {
				std.ValHdrs, std.ValPad, std.ValBW = nil, nil, 0
			}
			info.Lay, info.Present, info.Mode = std, pc.present, ModeData
			info.Body = int64(len(lb) + len(vb))
			if p.Type == PageDataV2 && (int64(p.Uncomp) != info.Body || int(p.DefLen) != len(lb)) {
				return opq("v2 page sizes disagree with its header")
			}
			if p.Type == PageData && int64(p.Uncomp) != info.Body {
				return opq("v1 page body %d bytes, header says %d", info.Body, p.Uncomp)
			}
			if dictEnc(p.Enc) {
				if firstPlain {
					predicted = false
				}
				if pass1 {
					for i := 0; i < pc.vals.n; i++ {
						ix := int(d.idx[string(pc.vals.at(i))])
						if ix > nextIdx {
							predicted = false
						} else if ix == nextIdx {
							nextIdx++
						}
					}
				}
			} else {
				firstPlain = true
			}
			if sk != nil {
				if err := feed(sk, spec, &pc, rs); err != nil {
					return err
				}
			}
		default:
			return opq("page type %d", p.Type)
		}
		if sk != nil && sk.page != nil {
			if err := sk.page(pi, &info, values, rec, hdr); err != nil {
				return err
			}
		}
		if pass1 {
			info.Comp.Cmds, info.Comp.Zstd = nil, nil
			plan.Pages = append(plan.Pages, info)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if pass1 {
		if len(plan.Pages) == 0 && c.TotalComp > 0 {
			return opq("no pages")
		}
		var vals int64
		for i := range plan.Pages {
			if plan.Pages[i].Mode == ModeData {
				vals += int64(plan.Pages[i].Page.NumValues)
			}
		}
		if vals != c.NumValues {
			return opq("pages hold %d values, the chunk says %d", vals, c.NumValues)
		}
		if d != nil {
			if nextIdx == 0 && d.vals.n > 0 || nextIdx != d.vals.n || d.dup {
				predicted = false
			}
			plan.DictMode = DictExplicit
			if predicted && !a.NoPredict {
				plan.DictMode = DictPredicted
			}
		}
	}
	return nil
}

// feed delivers a page's content to the sink: level runs (across pages through rs) and values.
func feed(sk *sink, spec colSpec, pc *pageContent, rs *runState) error {
	if spec.MaxDef > 0 && sk.level != nil {
		for _, d := range pc.defs {
			if err := rs.add(spec, int(d), sk.level); err != nil {
				return err
			}
		}
	}
	if sk.value != nil {
		for i := 0; i < pc.vals.n; i++ {
			if err := sk.value(pc.vals.at(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
