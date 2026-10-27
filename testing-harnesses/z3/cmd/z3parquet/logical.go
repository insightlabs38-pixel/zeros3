package main

import (
	"bytes"
	"errors"
	"hash/fnv"
	"io"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

// repOptions configure the logical representations.
type repOptions struct{ Target int }

type pqComp struct {
	class string
	id    parquetz3.ID
	data  []byte
}

// logicalPlan encodes a file with parquetz3 and turns the result into a lab plan: the
// recipe's member table are the references (96 B each), the leaf groups hang off the
// nodes (Via: no reference of their own), the recipe head is the plan's metadata, and
// Rebuild decodes from the serialized recipe and the stored components only.
func logicalPlan(f lab.EconFile, opt parquetz3.Options) (*lab.Plan, bool, error) {
	var comps []pqComp
	seen := map[parquetz3.ID]bool{}
	recipe, sz, _, err := parquetz3.Encode(bytes.NewReader(f.Data), int64(len(f.Data)), opt, func(class string, id parquetz3.ID, b []byte) error {
		if !seen[id] {
			seen[id] = true
			comps = append(comps, pqComp{class, id, append([]byte(nil), b...)})
		}
		return nil
	})
	if err != nil {
		if !parquetz3.Supported(err) {
			p, err := cdcPlan(f)
			return p, true, err
		}
		return nil, false, err
	}
	pr, err := parquetz3.ParseRecipe(recipe, opt.Limits)
	if err != nil {
		return nil, false, err
	}
	member := map[parquetz3.ID]bool{}
	for _, d := range pr.Digests {
		member[d] = true
	}
	p := &lab.Plan{MetaBytes: int64(len(recipe)) - sz.Digests, Detail: map[string]any{"sizes": sz}}
	for _, c := range comps {
		b := c.data
		p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: lab.ID(c.id), Size: int64(len(b)), Class: c.class, Data: func() ([]byte, error) { return b, nil }}, Via: !member[c.id]})
	}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		return parquetz3.Decode(w, recipe, func(id parquetz3.ID) ([]byte, error) { return get(lab.ID(id)) }, parquetz3.Limits{})
	}
	return p, false, nil
}

func logicalRep(name string, opt parquetz3.Options) repDef {
	return fixed(lab.EconRep{Name: name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { return logicalPlan(f, opt) }})
}

// oracleValues is the perfect value-identity bound: per column, the bytes of every value
// (and null run) not already stored, each distinct one once; no references, no recipe, no
// page layout, no framing. It is not a representation.
func oracleValues() repDef {
	return repDef{"o-value", true, func() lab.EconRep {
		seen := map[uint64]struct{}{}
		return lab.EconRep{Name: "o-value", Oracle: true, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
			pf, err := parseFile(f)
			if err != nil {
				p, err := cdcPlan(f)
				return p, true, err
			}
			p := &lab.Plan{Oracle: true}
			newBytes := make([][]byte, len(pf.f.Columns)*2)
			hit := func(ci, kind int, rec []byte) {
				h := fnv.New64a()
				h.Write([]byte{byte(ci), byte(kind)})
				h.Write(rec)
				k := h.Sum64()
				if _, ok := seen[k]; !ok {
					seen[k] = struct{}{}
					newBytes[2*ci+kind] = append(newBytes[2*ci+kind], rec...)
				}
			}
			for gi := range pf.f.RowGroups {
				for ci := range pf.f.RowGroups[gi].Chunks {
					ch := &pf.f.RowGroups[gi].Chunks[ci]
					opaque, err := pf.an.Content(ch, func(rec []byte) error { hit(ci, 0, rec); return nil }, func(rec []byte, _ int) error { hit(ci, 1, rec); return nil })
					if err != nil {
						return nil, false, err
					}
					if opaque { // an opaque chunk's payload is its own identity
						b, _ := readFile2(pf, ch)
						hit(ci, 0, b)
					}
				}
			}
			for ci := range pf.f.Columns {
				pf.an.FinishColumn(ci, func(rec []byte, _ int) error { hit(ci, 1, rec); return nil })
			}
			for i, b := range newBytes {
				if len(b) == 0 {
					continue
				}
				bb := b
				p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: sid(bb), Size: int64(len(bb)), Class: "value", Data: func() ([]byte, error) { return bb, nil }}, Via: true})
				_ = i
			}
			return p, false, nil
		}}
	}}
}

func readFile2(pf *pqFile, ch *parquetz3.Chunk) ([]byte, error) {
	b := make([]byte, ch.End-ch.Start)
	_, err := pf.r.ReadAt(b, ch.Start)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return b, err
}

func logicalReps(o repOptions) []repDef {
	t := o.Target
	if t <= 0 {
		t = 8 << 10
	}
	return []repDef{
		logicalRep("logical", parquetz3.Options{Target: t}),
		logicalRep("lg-fixed", parquetz3.Options{Cut: parquetz3.CutFixed, FixedCount: 1024}),
		logicalRep("lg-page", parquetz3.Options{Cut: parquetz3.CutPage}),
		logicalRep("lg-nopred", parquetz3.Options{Target: t, NoPredict: true}),
		logicalRep("lg-2k", parquetz3.Options{Target: 2 << 10}),
		logicalRep("lg-32k", parquetz3.Options{Target: 32 << 10}),
		logicalRep("lg-128k", parquetz3.Options{Target: 128 << 10}),
		logicalRep("lg-512k", parquetz3.Options{Target: 512 << 10}),
		oracleValues(),
	}
}
