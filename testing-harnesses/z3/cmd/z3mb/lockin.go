package main

import (
	"fmt"
	"os"
	"strings"

	"zeros3-z3/lab"
)

func init() { cmds["lockin"] = lockinCmd }

// lockin A,B FILE...: per-revision choice between two container representations (exact plans are
// built once, then replayed in stores): all-A, all-B, greedy, and the hindsight optimum.
func lockinCmd(args []string) error {
	names := strings.Split(args[0], ",")
	if len(names) != 2 {
		return fmt.Errorf("lockin needs two representations")
	}
	st := &innerStats{}
	reps := containerReps(names, st)
	if len(reps) != 2 {
		return fmt.Errorf("unknown representation")
	}
	var plans [2][]*lab.Plan
	files := args[1:]
	for ri, rp := range reps {
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			pl, _, err := rp.Plan(lab.EconFile{Name: "pkg", Data: b})
			if err != nil {
				return err
			}
			plans[ri] = append(plans[ri], treeify(pl, treeFan))
		}
	}
	n := len(files)
	run := func(seq []int) (int64, error) {
		ac := lab.NewAcct(refBytes, true)
		var tot int64
		for i, c := range seq {
			a, err := ac.Add(plans[c][i])
			if err != nil {
				return 0, err
			}
			tot += a.IncrementalPack
		}
		return tot, nil
	}
	for c := 0; c < 2; c++ {
		seq := make([]int, n)
		for i := range seq {
			seq[i] = c
		}
		t, err := run(seq)
		if err != nil {
			return err
		}
		fmt.Printf("all %-22s %s\n", names[c], kib(t))
	}
	// greedy: at each revision pick the cheaper plan against the store built so far
	ac := lab.NewAcct(refBytes, true)
	var gt int64
	var gseq []int
	for i := 0; i < n; i++ {
		var best int
		var bestCost int64 = -1
		for c := 0; c < 2; c++ {
			a, err := ac.Clone().Add(plans[c][i])
			if err != nil {
				return err
			}
			if bestCost < 0 || a.IncrementalPack < bestCost {
				best, bestCost = c, a.IncrementalPack
			}
		}
		a, _ := ac.Add(plans[best][i])
		gt += a.IncrementalPack
		gseq = append(gseq, best)
	}
	fmt.Printf("greedy %-21s %s  sequence %v\n", "(cheaper plan each revision)", kib(gt), gseq)
	var ht int64 = -1
	var hseq []int
	for m := 0; m < 1<<n; m++ {
		seq := make([]int, n)
		for i := range seq {
			seq[i] = m >> i & 1
		}
		t, err := run(seq)
		if err != nil {
			return err
		}
		if ht < 0 || t < ht {
			ht, hseq = t, seq
		}
	}
	fmt.Printf("hindsight %-18s %s  sequence %v\n", "(best of all 2^n)", kib(ht), hseq)
	return nil
}
