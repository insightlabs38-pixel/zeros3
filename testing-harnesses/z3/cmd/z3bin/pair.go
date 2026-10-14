package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/binref"
)

func pct(a, b int) float64 { return 100 * float64(a) / float64(b) }

func mustLoad(path string) *bin {
	d, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	b, err := loadBin(path, d)
	if err != nil {
		fatal(fmt.Errorf("%s: %v", path, err))
	}
	return b
}

// normDelta is the pairwise-delta cost of cur's streams against base's,
// stream kinds compared with like kinds (payload, refs, everything else).
func normDelta(base, cur *binref.Norm) (payload, meta int) {
	cat := func(n *binref.Norm, kind string) []byte {
		var out []byte
		for _, s := range n.Streams {
			if (kind == "payload") == (s.Kind == "payload") {
				out = append(out, s.Data...)
			}
		}
		return out
	}
	p := deltaCost(cat(base, "payload"), cat(cur, "payload"))
	m := deltaCost(cat(base, "meta"), cat(cur, "meta"))
	return p.Cost, m.Cost
}

type row struct {
	name                     string
	cdc, entries, meta, refs int
	delta, deltaP, deltaM    int
	hasDelta                 bool
	exact                    bool
}

func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	a := fs.String("base", "", "base binary")
	b := fs.String("cur", "", "current binary")
	unitsF := fs.String("units", "auto", "sym|pcln|call|auto (comma list)")
	fast := fs.Bool("fast", false, "skip the weaker identities")
	fs.Parse(args)
	base, cur := mustLoad(*a), mustLoad(*b)
	n := len(cur.Data)
	fmt.Printf("cur %d B (%s); syms %d; pclntab %v\n", n, cur.Format, len(cur.Im.Syms), cur.Pcln != nil)
	fmt.Printf("%-40s %8s %8s %6s %9s %7s | %8s %8s %8s\n", "representation", "newCDC", "%", "objs", "meta", "refs", "Δ-total%", "Δpayld", "Δmeta")
	pr := func(name string, br, cr *Rep, bn, cn *binref.Norm) {
		nb, objs := Cost(br.Objs, cr.Objs)
		line := fmt.Sprintf("%-40s %8d %7.2f%% %6d %9d %7d |", name, nb, pct(nb, n), objs, cr.Meta, cr.Refs)
		if bn != nil {
			dp, dm := normDelta(bn, cn)
			line += fmt.Sprintf(" %7.2f%% %8d %8d", pct(dp+dm, n), dp, dm)
		}
		fmt.Println(line)
	}
	bb, cb := baselineReps(base), baselineReps(cur)
	for i := range cb {
		pr(cb[i].Name, bb[i], cb[i], nil, nil)
	}
	d := deltaCost(base.Data, cur.Data)
	fmt.Printf("%-40s %8s %8s %6s %9s %7s | %7.2f%% (literal %d, copies %d)\n", "raw file delta", "", "", "", "", "", pct(d.Cost, n), d.Literal, d.Copies)
	ids := []binref.Ident{binref.IdentOrdinal, binref.IdentRegionRel, binref.IdentSymbol, binref.IdentContent}
	if *fast {
		ids = []binref.Ident{binref.IdentSymbol, binref.IdentContent}
	}
	for _, um := range strings.Split(*unitsF, ",") {
		for _, set := range []slotSet{setReloc, setStruct, setX86, setFull} {
			for _, id := range ids {
				c := cfg{set, id, um}
				bnn, _, e1 := normalize(base, c)
				cnn, _, e2 := normalize(cur, c)
				if e1 != nil || e2 != nil {
					fatal(fmt.Errorf("%v %v", e1, e2))
				}
				br, err := normRep(base, c, false)
				if err != nil {
					fatal(err)
				}
				cr, err := normRep(cur, c, false)
				if err != nil {
					fatal(err)
				}
				pr(c.String(), br, cr, bnn, cnn)
				if id == binref.IdentSymbol && set.Name == "full" {
					bo, _ := normRep(base, c, true)
					co, _ := normRep(cur, c, true)
					pr("ORACLE(no meta) "+c.String(), bo, co, nil, nil)
				}
			}
		}
	}
}
