package main

import (
	"bytes"
	"crypto/sha256"
	"io"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

func sid(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

func cdcPlan(f lab.EconFile) (*lab.Plan, error) { return lab.CDCPlan(f, "parquet") }

func cdcRep() lab.EconRep {
	return lab.EconRep{Name: "cdc", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := cdcPlan(f); return p, false, err }}
}

// pqFile is a parsed file together with its exact span decomposition.
type pqFile struct {
	f     *parquetz3.File
	r     *bytes.Reader
	spans []parquetz3.Span
	an    *parquetz3.Analyzer
}

func parseFile(f lab.EconFile) (*pqFile, error) {
	r := bytes.NewReader(f.Data)
	pf, err := parquetz3.Open(r, int64(len(f.Data)), parquetz3.Limits{})
	if err != nil {
		return nil, err
	}
	sp, err := pf.Spans()
	if err != nil {
		return nil, err
	}
	return &pqFile{pf, r, sp, parquetz3.NewAnalyzer(r, pf, parquetz3.Limits{})}, nil
}

// concatPlan makes a plan from contiguous spans of the file; spans under 512 B live in the
// recipe like in the lab's other structural representations.
func concatPlan(f lab.EconFile, cuts []int64, class string) *lab.Plan {
	p := &lab.Plan{Concat: true}
	var pos int64
	for _, c := range cuts {
		if c <= pos {
			continue
		}
		b := f.Data[pos:c]
		if len(b) < 512 {
			p.Pieces = append(p.Pieces, lab.Piece{Inline: b, Len: int64(len(b))})
		} else {
			bb := b
			p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: sid(b), Size: int64(len(b)), Class: class, Data: func() ([]byte, error) { return bb, nil }}, Len: int64(len(b))})
		}
		pos = c
	}
	p.Rebuild = lab.ConcatRebuild(p)
	return p
}

// physCuts returns the segmentation boundaries of a file at one structural level.
func physCuts(pf *pqFile, level string, size int64) []int64 {
	var cuts []int64
	switch level {
	case "rg": // every row group (all its column chunks) is one segment
		last := -1
		for _, s := range pf.spans {
			if s.Kind != parquetz3.SpanChunk {
				continue
			}
			if s.Chunk.RG != last {
				last = s.Chunk.RG
				if s.Off > 4 {
					cuts = append(cuts, s.Off)
				}
			}
		}
		cuts = append(cuts, pf.f.FooterOff)
	case "chunk": // every column chunk, gap and the footer
		for _, s := range pf.spans {
			cuts = append(cuts, s.Off+s.Len)
		}
	case "page": // every page (header and payload), gap and the footer
		for _, s := range pf.spans {
			if s.Kind != parquetz3.SpanChunk {
				cuts = append(cuts, s.Off+s.Len)
				continue
			}
			pf.f.Pages(pf.r, s.Chunk, func(p *parquetz3.Page, _ []byte) error { cuts = append(cuts, p.End()); return nil })
		}
	}
	return append(cuts, size)
}

func physRep(name, level, class string) lab.EconRep {
	return lab.EconRep{Name: name, Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
		pf, err := parseFile(f)
		if err != nil {
			p, err := cdcPlan(f)
			return p, true, err
		}
		return concatPlan(f, physCuts(pf, level, int64(len(f.Data))), class), false, nil
	}}
}

// dpageRep is the exact decompressed-page representation: page headers and framing inline,
// every page body (the page payload decompressed) a component, and the codec recipe
// (predictor id, or the explicit commands/zstd description) charged as recipe bytes. Pages
// whose codec is unsupported keep their compressed payload as the component.
func dpageRep() lab.EconRep {
	return lab.EconRep{Name: "dpages", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) {
		pf, err := parseFile(f)
		if err != nil {
			p, err := cdcPlan(f)
			return p, true, err
		}
		type op struct {
			inline []byte
			comp   *lab.Comp
			page   *parquetz3.Page
			rec    parquetz3.CompRec
		}
		var ops []op
		p := &lab.Plan{}
		comp := func(b []byte, class string) *lab.Comp {
			bb := b
			return &lab.Comp{ID: sid(b), Size: int64(len(b)), Class: class, Data: func() ([]byte, error) { return bb, nil }}
		}
		raw := func(off, n int64, class string) {
			b := f.Data[off : off+n]
			if len(b) < 512 {
				ops = append(ops, op{inline: b})
				p.Pieces = append(p.Pieces, lab.Piece{Inline: b})
				return
			}
			c := comp(b, class)
			ops = append(ops, op{comp: c})
			p.Pieces = append(p.Pieces, lab.Piece{Comp: c})
		}
		for _, s := range pf.spans {
			if s.Kind != parquetz3.SpanChunk {
				raw(s.Off, s.Len, "frame")
				continue
			}
			ch := s.Chunk
			err := pf.f.Pages(pf.r, ch, func(pg *parquetz3.Page, hdr []byte) error {
				h := append([]byte(nil), hdr...)
				ops = append(ops, op{inline: h})
				p.Pieces = append(p.Pieces, lab.Piece{Inline: h})
				if !parquetz3.Codecs(ch.Codec) {
					raw(pg.PayloadOff(), int64(pg.Comp), "payload")
					return nil
				}
				body, rec, err := pf.an.PageBody(ch, pg)
				if err != nil {
					raw(pg.PayloadOff(), int64(pg.Comp), "payload")
					return nil
				}
				c := comp(body, "body")
				pgc := *pg
				ops = append(ops, op{comp: c, page: &pgc, rec: rec})
				p.Pieces = append(p.Pieces, lab.Piece{Comp: c})
				p.MetaBytes += 1 + int64(len(rec.Cmds)) + int64(len(rec.Zstd))
				return nil
			})
			if err != nil {
				q, err := cdcPlan(f)
				return q, true, err
			}
		}
		p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
			for _, o := range ops {
				switch {
				case o.inline != nil:
					if _, err := w.Write(o.inline); err != nil {
						return err
					}
				default:
					b, err := get(o.comp.ID)
					if err != nil {
						return err
					}
					if o.page != nil {
						if b, err = pf.an.PagePayload(o.page, b, o.rec); err != nil {
							return err
						}
					}
					if _, err := w.Write(b); err != nil {
						return err
					}
				}
			}
			return nil
		}
		return p, false, nil
	}}
}
