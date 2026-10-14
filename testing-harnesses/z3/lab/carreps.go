package lab

import (
	"bytes"
	"errors"
	"io"

	"zeros3-z3/carz3"
)

// Z3-14: CAR representations. A CAR section is varint(len) || CID || block.
// The block payload is the reusable content; everything else is serialisation
// (order, length prefixes, CIDs, header/roots, CARv2 wrapper and index).
//
//	car-section        structural cut: header/wrapper inline, one component per
//	                   whole section (prefix+CID+block), CARv2 index one component
//	car-block          block payloads as components keyed by SHA-256; CIDs, header,
//	                   wrapper and index kept verbatim in the recipe (naive recipe)
//	car-block+recipe   same, with sha2-256 CIDs derived from the component digest
//	                   and a CARv2 index rebuilt from the section list when exact
//	car-block+recipe-i512  ... and blocks under 512 B inlined in the recipe
//
// Native CIDs never decide ZeroS3 identity: the component ID is always the
// SHA-256 of the exact block bytes.

// CARReps returns the CAR representations in report order, preceded by the
// generic baselines they are compared with.
func CARReps() []Rep {
	var out []Rep
	for _, r := range Reps() {
		switch r.Name {
		case "raw", "cdc", "fixed-64k", "fixed-4k":
			out = append(out, r)
		}
	}
	return append(out,
		Rep{"car-section", analyzeCARSection},
		Rep{"car-block", carBlockRep(carz3.Options{NoDerive: true})},
		Rep{"car-block+recipe", carBlockRep(carz3.Options{})},
		Rep{"car-block+recipe-i512", carBlockRep(carz3.Options{InlineMax: 512})},
		Rep{"car-set", analyzeCARSet},
		Rep{"car-auto", analyzeCARAuto},
	)
}

func carOptions(o carz3.Options) carz3.Options {
	l := carz3.DefaultLimits()
	o.Limits = l
	return o
}

// carBlockRep builds the block-component plan for the given recipe options.
func carBlockRep(o carz3.Options) func(*Input) (*Plan, error) {
	return func(in *Input) (*Plan, error) {
		rep, st, err := carz3.Build(in.Src.R, in.Src.Size, carOptions(o))
		if err != nil {
			if carz3.IsFallback(err) {
				return nil, ErrNotApplicable
			}
			return nil, err
		}
		return carPlan(in, rep, st)
	}
}

// carPlan converts a Rep into a lab plan. The recipe is serialised and parsed
// back before the plan's Rebuild uses it, so reconstruction exercises the
// bytes that are counted.
func carPlan(in *Input, rep *carz3.Rep, st *carz3.Stats) (*Plan, error) {
	blob, sz := rep.Marshal()
	p := &Plan{}
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if r.Inline != nil {
			p.Pieces = append(p.Pieces, Piece{Inline: r.Inline, Len: r.Len})
			continue
		}
		c := spanComp(in.Src, r.SrcOff, r.Len, ID(r.Sum))
		c.Class = "carblock"
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: r.Len})
	}
	// Meta beyond the piece table: header, wrapper, CID prefix table, per-section
	// CID mode (+ exact CIDs), small-block markers are counted by the layouts.
	var modes int
	for i := range rep.Refs {
		r := &rep.Refs[i]
		modes++
		if !r.Derived {
			modes += uvarintLen(uint64(len(r.CID)))
		}
	}
	p.MetaBytes = int64(sz.Header + sz.Wrapper + sz.Prefix + sz.CIDExact + modes)
	p.IndexBytes = int64(sz.Index)
	p.Detail = map[string]any{"recipe_bytes": len(blob), "sizes": sz, "stats": *st, "index_mode": int(rep.IndexMode)}
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		r2, err := carz3.Unmarshal(blob)
		if err != nil {
			return err
		}
		return r2.Reconstruct(w, func(s [32]byte) ([]byte, error) { return get(ID(s)) })
	}
	return p, nil
}

// analyzeCARSet stores the archive as a block *set* plus an order. The sorted
// digest table of the distinct blocks is cut with CDC into components (so two
// archives holding the same blocks share it, and an added block perturbs one
// chunk); the blocks hang off that table (Via: no recipe reference), and the
// recipe keeps only the header, wrapper, CID prefixes and the per-section
// order as small indexes into the sorted table.
func analyzeCARSet(in *Input) (*Plan, error) {
	rep, st, err := carz3.Build(in.Src.R, in.Src.Size, carOptions(carz3.Options{}))
	if err != nil {
		return nil, notApplicableCAR(err)
	}
	head, table, tail, sz := rep.MarshalParts(true)
	p := &Plan{}
	var tchunks []ID
	if err := forEachChunk(bytes.NewReader(table), func(_ int64, c []byte) error {
		cp := append([]byte(nil), c...)
		cc := bytesComp(sum(cp), cp)
		cc.Class = "carset"
		p.Pieces = append(p.Pieces, Piece{Comp: cc, Len: int64(len(cp))})
		tchunks = append(tchunks, cc.ID)
		return nil
	}); err != nil {
		return nil, err
	}
	seen := map[[32]byte]bool{}
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if seen[r.Sum] {
			continue
		}
		seen[r.Sum] = true
		c := spanComp(in.Src, r.SrcOff, r.Len, ID(r.Sum))
		c.Class = "carblock"
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: r.Len, Via: true})
	}
	p.MetaBytes = int64(len(head) + len(tail))
	p.IndexBytes = 0
	p.Detail = map[string]any{"table_bytes": len(table), "order_bytes": len(tail), "table_chunks": len(tchunks), "sizes": sz, "stats": *st, "index_mode": int(rep.IndexMode)}
	p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
		var t []byte
		for _, id := range tchunks {
			b, err := get(id)
			if err != nil {
				return err
			}
			t = append(t, b...)
		}
		r2, err := carz3.Unmarshal(append(append(append([]byte(nil), head...), t...), tail...))
		if err != nil {
			return err
		}
		return r2.Reconstruct(w, func(s [32]byte) ([]byte, error) { return get(ID(s)) })
	}
	return p, nil
}

// analyzeCARSection cuts the archive at section boundaries without looking at
// CIDs or payload identity separately: a section is the unit of reuse.
func analyzeCARSection(in *Input) (*Plan, error) {
	s := in.Src
	head := make([]byte, min(s.Size, carz3.V2PragmaLen))
	if _, err := s.R.ReadAt(head, 0); err != nil && err != io.EOF {
		return nil, err
	}
	base, dataSize := int64(0), s.Size
	var v2 *carz3.V2
	if carz3.IsV2(head) {
		var err error
		if v2, err = carz3.ParseV2(s.R, s.Size); err != nil {
			return nil, notApplicableCAR(err)
		}
		base, dataSize = int64(v2.DataOffset), int64(v2.DataSize)
	}
	p := &Plan{Concat: true}
	inline := func(off, n int64) error {
		if n == 0 {
			return nil
		}
		if n < in.Opts.InlineMax {
			return appendRegion(p, s, off, n, in.Opts)
		}
		b, err := s.Span(off, n)
		if err != nil {
			return err
		}
		c := spanComp(s, off, n, sum(b))
		c.Class = "carwrap"
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: n})
		return nil
	}
	if err := inline(0, base); err != nil {
		return nil, err
	}
	rd, err := carz3.NewReader(io.NewSectionReader(s.R, base, dataSize), carz3.DefaultLimits())
	if err != nil {
		return nil, notApplicableCAR(err)
	}
	if err := inline(base, rd.Pos()); err != nil {
		return nil, err
	}
	for {
		e, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, notApplicableCAR(err)
		}
		n := e.End() - e.Off
		if n < in.Opts.InlineMax {
			if err := appendRegion(p, s, base+e.Off, n, in.Opts); err != nil {
				return nil, err
			}
			continue
		}
		b, err := s.Span(base+e.Off, n)
		if err != nil {
			return nil, err
		}
		c := spanComp(s, base+e.Off, n, sum(b))
		c.Class = "carsec"
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: n})
	}
	if v2 != nil {
		if err := inline(base+dataSize, s.Size-base-dataSize); err != nil {
			return nil, err
		}
	}
	p.Rebuild = ConcatRebuild(p)
	return p, nil
}

func notApplicableCAR(err error) error {
	if carz3.IsFallback(err) {
		return ErrNotApplicable
	}
	return err
}

// analyzeCARAuto is the admission rule under test: use block components when
// the archive parses and its blocks are large enough for per-block references
// to pay (mean block >= CARAutoMinBlock bytes); otherwise fall back to the
// production CDC plan. Unparseable input always falls back.
func analyzeCARAuto(in *Input) (*Plan, error) {
	rep, st, err := carz3.Build(in.Src.R, in.Src.Size, carOptions(carz3.Options{InlineMax: 512}))
	if err == nil && st.Sections > 0 && st.BlockBytes/int64(st.Sections) >= CARAutoMinBlock {
		p, err := carPlan(in, rep, st)
		if err == nil {
			p.Detail["auto"] = "car-block"
			return p, nil
		}
	} else if err != nil && !carz3.IsFallback(err) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	p, err := analyzeChunked(in, cdcSplit)
	if err != nil {
		return nil, err
	}
	p.Detail = map[string]any{"auto": "cdc"}
	return p, nil
}

// CARAutoMinBlock is the mean-block-size threshold of car-auto (bytes).
var CARAutoMinBlock int64 = 1024
