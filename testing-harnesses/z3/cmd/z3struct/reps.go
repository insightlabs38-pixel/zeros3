package main

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

func sid(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

// segPlan is a concatenation plan of the segments (which must tile the file).
// Segments below inlineMax live inline in the recipe, as in the lab's other
// structural representations.
func segPlan(f fstate.File, segs []seg) (*lab.Plan, error) {
	p := &lab.Plan{Concat: true}
	var pos int64
	for _, s := range segs {
		if s.off != pos || s.n < 0 || s.off+s.n > int64(len(f.Data)) {
			return nil, errors.New("segments do not tile the file")
		}
		b := f.Data[s.off : s.off+s.n]
		pos += s.n
		if s.n == 0 {
			continue
		}
		if s.n < 512 {
			p.Pieces = append(p.Pieces, lab.Piece{Inline: b, Len: s.n})
			continue
		}
		bb := b
		p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: sid(b), Size: s.n, Class: s.class, Data: func() ([]byte, error) { return bb, nil }}, Len: s.n})
	}
	if pos != int64(len(f.Data)) {
		return nil, errors.New("segments do not cover the file")
	}
	p.Rebuild = lab.ConcatRebuild(p)
	return p, nil
}

// groupSegs merges consecutive segments into content-defined groups: a group ends
// after a segment whose digest falls on a 1-in-k boundary, subject to min/max
// segments per group. Cut points are decided by segment content only, so a
// changed segment moves no later boundary.
func groupSegs(d []byte, segs []seg, k, min, max int) []seg {
	var out []seg
	cur := seg{off: -1}
	cnt := 0
	for _, s := range segs {
		if cur.off < 0 {
			cur = seg{off: s.off, class: s.class}
			cnt = 0
		}
		cur.n += s.n
		cnt++
		h := sha256.Sum256(d[s.off : s.off+s.n])
		cut := (int(h[0])<<8|int(h[1]))%k == 0 && cnt >= min
		if s.class == "tail" || s.class == "footer" {
			cut = true
		}
		if cut || cnt >= max {
			out = append(out, cur)
			cur = seg{off: -1}
		}
	}
	if cur.off >= 0 {
		out = append(out, cur)
	}
	return out
}

// cdcWithin restarts production CDC inside every segment longer than min.
func cdcWithin(d []byte, segs []seg, min int64) ([]seg, error) {
	var out []seg
	for _, s := range segs {
		if s.n <= min {
			out = append(out, s)
			continue
		}
		sizes, err := lab.CDCSizes(bytes.NewReader(d[s.off : s.off+s.n]))
		if err != nil {
			return nil, err
		}
		o := s.off
		for _, z := range sizes {
			out = append(out, seg{o, int64(z), s.class})
			o += int64(z)
		}
	}
	return out, nil
}

type scanFn func(d []byte) ([]seg, error)

// laneReps are the representations measured for a lane: production CDC, the
// fixed-block controls, and native-boundary variants. A native representation that
// cannot scan its file falls back to production CDC for that file.
func laneReps(lane string) []fstate.RepDef {
	reps := []fstate.RepDef{
		{Name: "cdc", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) { return fstate.Baseline("cdc", f) }},
		{Name: "fixed-4k", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) { return fstate.Baseline("fixed-4k", f) }},
		{Name: "fixed-64k", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) { return fstate.Baseline("fixed-64k", f) }},
	}
	native := func(name string, scan scanFn, oracle bool, post func(f fstate.File, s []seg) ([]seg, error)) fstate.RepDef {
		return fstate.RepDef{Name: name, Oracle: oracle, Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
			s, err := scan(f.Data)
			if err == nil && post != nil {
				s, err = post(f, s)
			}
			if err != nil {
				c.Counts["fallback_files"]++
				return fstate.FallbackCDC(f)
			}
			return segPlan(f, s)
		}}
	}
	grp := func(f fstate.File, s []seg) ([]seg, error) { return groupSegs(f.Data, s, 16, 4, 64), nil }
	switch lane {
	case "sqlite":
		reps = append(reps, native("pages", sqlitePages, false, nil), native("pages-norm", sqlitePagesNorm, false, nil))
	case "leveldb":
		reps = append(reps, native("blocks", ldbBlocks, false, nil), native("blocks-grouped", ldbBlocks, false, grp))
	case "parquet":
		reps = append(reps, native("chunks[oracle]", pqChunks, true, nil),
			native("chunks+cdc[oracle]", pqChunks, true, func(f fstate.File, s []seg) ([]seg, error) { return cdcWithin(f.Data, s, 64<<10) }))
	case "zarr3":
		reps = append(reps, fstate.RepDef{Name: "object-whole", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
			return segPlan(f, []seg{{0, int64(len(f.Data)), "object"}})
		}}, native("inner-chunks", func(d []byte) ([]seg, error) {
			if len(d) < 1<<16 { // zarr.json and other small objects
				return []seg{{0, int64(len(d)), "meta"}}, nil
			}
			return zarrShardChunks(d)
		}, false, nil))
	case "zarr":
		reps = append(reps, fstate.RepDef{Name: "object-whole", Plan: func(c *fstate.Ctx, f fstate.File) (*lab.Plan, error) {
			return segPlan(f, []seg{{0, int64(len(f.Data)), "object"}})
		}}, fstate.SnapshotCDC())
	}
	return reps
}
