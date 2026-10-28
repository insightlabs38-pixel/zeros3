package main

import (
	"fmt"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
)

func init() { cmds["rolling"] = rolling }

// rolling DLL... : the physical size of the live set when only the two newest revisions are kept
// (plain CDC against the indexed-reference representation), per revision pair and summed.
func rolling(args []string) error {
	cfg := clrz3.Config{Auto: 12, Cut: econx.UnitCut(4).Cut}
	var cdc, rep []*lab.Plan
	for _, p := range args {
		r, err := readRev(p)
		if err != nil {
			return err
		}
		f := r.Files[0]
		c, err := lab.CDCPlan(f, "clr")
		if err != nil {
			return err
		}
		x, fb, err := exact(cfg)(f)
		if err != nil || fb {
			return fmt.Errorf("%s: %v", p, err)
		}
		cdc, rep = append(cdc, c), append(rep, econx.Treeify(x, 64))
	}
	live := func(ps []*lab.Plan, i int) int64 {
		ac := lab.NewAcct(lab.EconRef, true)
		a, _ := ac.Add(ps[i-1])
		b, _ := ac.Add(ps[i])
		return a.IncrementalPack + b.IncrementalPack
	}
	var tc, tr int64
	for i := 1; i < len(args); i++ {
		c, r := live(cdc, i), live(rep, i)
		tc += c
		tr += r
		fmt.Printf("  live set of revisions %d+%d: CDC %s, representation %s\n", i-1, i, econx.Kib(c), econx.Kib(r))
	}
	fmt.Printf("rolling two-revision live set, mean over %d windows: CDC %s, representation %s\n", len(args)-1, econx.Kib(tc/int64(len(args)-1)), econx.Kib(tr/int64(len(args)-1)))
	return nil
}
