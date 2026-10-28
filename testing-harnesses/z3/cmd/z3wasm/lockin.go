package main

import (
	"fmt"
	"strings"

	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/wasmz3"
)

func init() { cmds["lockin"] = lockin }

// lockin DLL... : first-sight lock-in. Each revision is stored either as plain CDC or as the
// indexed-reference representation; compared: all CDC, all representation, greedy per revision
// (cheapest increment given the store so far) and hindsight (best of every assignment).
func lockin(args []string) error {
	type pair struct{ cdc, rep *lab.Plan }
	var revs []econx.Rev
	var plans []pair
	cfg := wasmz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut}
	for _, p := range args {
		r, err := readRev(p)
		if err != nil {
			return err
		}
		revs = append(revs, r)
		f := r.Files[0]
		c, _, err := (func() (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "wasm"); return p, false, err })()
		if err != nil {
			return err
		}
		x, fb, err := exact(cfg)(f)
		if err != nil || fb {
			return fmt.Errorf("%s: %v fallback=%v", p, err, fb)
		}
		plans = append(plans, pair{c, x})
	}
	n := len(revs)
	run := func(st *lab.EconStore, i int, useRep bool) int64 {
		pl := plans[i].cdc
		if useRep {
			pl = plans[i].rep
		}
		a := st.Run(lab.EconRep{Name: "x", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { return econx.Treeify(pl, 64), false, nil }}, revs[i].Files)
		return a.Pack
	}
	total := func(mask int) (int64, string) {
		st := lab.NewEconStore()
		var t int64
		var s []string
		for i := 0; i < n; i++ {
			rep := mask>>uint(i)&1 == 1
			t += run(st, i, rep)
			if rep {
				s = append(s, "N")
			} else {
				s = append(s, "C")
			}
		}
		return t, strings.Join(s, "")
	}
	allC, _ := total(0)
	allN, _ := total(1<<uint(n) - 1)
	// greedy
	st := lab.NewEconStore()
	var g int64
	var gs []string
	for i := 0; i < n; i++ {
		a, b := st.Clone(), st.Clone()
		ca, cb := run(a, i, false), run(b, i, true)
		if cb < ca {
			st, g = b, g+cb
			gs = append(gs, "N")
		} else {
			st, g = a, g+ca
			gs = append(gs, "C")
		}
	}
	best, bs := int64(1<<62), ""
	for m := 0; m < 1<<uint(n); m++ {
		if t, s := total(m); t < best {
			best, bs = t, s
		}
	}
	fmt.Printf("%d revisions: all CDC %s | all representation %s | greedy %s (%s) | hindsight %s (%s)\n", n, econx.Kib(allC), econx.Kib(allN), econx.Kib(g), strings.Join(gs, ""), econx.Kib(best), bs)
	return nil
}
