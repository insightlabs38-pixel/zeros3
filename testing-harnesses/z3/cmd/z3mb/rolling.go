package main

import (
	"fmt"
	"os"
	"strings"

	"zeros3-z3/lab"
)

func init() { cmds["rolling"] = rollingCmd }

// rolling WINDOW REPS FILE...: cost of holding only the latest WINDOW revisions (a fresh store per
// window, revisions added in order): the live-history view of the same plans.
func rollingCmd(args []string) error {
	var win int
	fmt.Sscan(args[0], &win)
	names := strings.Split(args[1], ",")
	st := &innerStats{}
	reps := containerReps(names, st)
	files := args[2:]
	for _, rp := range reps {
		var plans []*lab.Plan
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			pl, _, err := rp.Plan(lab.EconFile{Name: "pkg", Data: b})
			if err != nil {
				return err
			}
			plans = append(plans, treeify(pl, treeFan))
		}
		fmt.Printf("%-22s window %d:", rp.Name, win)
		var sum int64
		n := 0
		for i := 0; i+win <= len(plans); i++ {
			ac := lab.NewAcct(refBytes, true)
			var tot int64
			for _, pl := range plans[i : i+win] {
				a, err := ac.Add(pl)
				if err != nil {
					return err
				}
				tot += a.IncrementalPack
			}
			fmt.Printf(" %s", kib(tot))
			sum += tot
			n++
		}
		fmt.Printf("   mean %s\n", kib(sum/int64(max(n, 1))))
	}
	return nil
}
