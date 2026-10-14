package main

import (
	"flag"
	"fmt"

	"zeros3-z3/binref"
)

type regStat struct {
	name        string
	size        int
	raw, normed int // new bytes under section+CDC, and under full/symbol payload+refs
	refMeta     int
}

// attribute splits the new-chunk bytes of cur against base by region.
func attribute(base, cur *bin, c cfg) []regStat {
	have := map[[32]byte]bool{}
	for _, r := range base.Im.Regions {
		for _, o := range cdcObjs(base.Data[r.Off : r.Off+r.Len]) {
			have[o.key] = true
		}
	}
	nb, _, err := normalize(base, c)
	if err != nil {
		fatal(err)
	}
	nc, _, err := normalize(cur, c)
	if err != nil {
		fatal(err)
	}
	haveN := map[[32]byte]bool{}
	for _, s := range nb.Streams {
		for _, o := range cdcObjs(s.Data) {
			haveN[o.key] = true
		}
	}
	out := make([]regStat, len(cur.Im.Regions))
	for i, r := range cur.Im.Regions {
		out[i] = regStat{name: r.Name, size: r.Len}
		for _, o := range cdcObjs(cur.Data[r.Off : r.Off+r.Len]) {
			if !have[o.key] {
				have[o.key] = true
				out[i].raw += o.size
			}
		}
	}
	for _, s := range nc.Streams {
		ri := s.Region
		if s.Kind == "regions" || s.Kind == "units" {
			ri = -1
		}
		for _, o := range cdcObjs(s.Data) {
			if haveN[o.key] {
				continue
			}
			haveN[o.key] = true
			if ri >= 0 {
				out[ri].normed += o.size
				if s.Kind == "refs" {
					out[ri].refMeta += o.size
				}
			}
		}
	}
	return out
}

func runAttr(args []string) {
	fs := flag.NewFlagSet("attr", flag.ExitOnError)
	a := fs.String("base", "", "base")
	b := fs.String("cur", "", "cur")
	um := fs.String("units", "auto", "unit source")
	fs.Parse(args)
	base, cur := mustLoad(*a), mustLoad(*b)
	st := attribute(base, cur, cfg{setFull, binref.IdentSymbol, *um})
	fmt.Printf("%-22s %9s %9s %9s %9s\n", "region", "size", "secCDC", "normed", "refsNew")
	var tr, tn int
	for _, s := range st {
		if s.raw == 0 && s.normed == 0 {
			continue
		}
		fmt.Printf("%-22s %9d %9d %9d %9d\n", s.name, s.size, s.raw, s.normed, s.refMeta)
		tr += s.raw
		tn += s.normed
	}
	fmt.Printf("%-22s %9d %9d %9d\n", "TOTAL", len(cur.Data), tr, tn)
}

// runDelta reports per-region literal bytes of cur against the whole of base
// (so movement between regions is not an artefact).
func runDelta(args []string) {
	fs := flag.NewFlagSet("delta", flag.ExitOnError)
	a := fs.String("base", "", "base")
	b := fs.String("cur", "", "cur")
	fs.Parse(args)
	bb, cc := mustLoad(*a), mustLoad(*b)
	base, cur, ci := bb.Data, cc.Data, cc.Im
	t0 := nowNS()
	whole := deltaCost(base, cur)
	fmt.Printf("whole-file delta: literal %d (%.2f%%) copies %d cost %d (%.2f%%)  [%.1fs]\n", whole.Literal, pct(whole.Literal, len(cur)), whole.Copies, whole.Cost, pct(whole.Cost, len(cur)), float64(nowNS()-t0)/1e9)
	for _, r := range ci.Regions {
		if r.Len < 1024 {
			continue
		}
		d := deltaCost(base, cur[r.Off:r.Off+r.Len])
		fmt.Printf("%-18s %9d literal %9d (%5.1f%%) cost %9d\n", r.Name, r.Len, d.Literal, pct(d.Literal, r.Len), d.Cost)
	}
}
