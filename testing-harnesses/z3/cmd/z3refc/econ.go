package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
)

// dexEcon prices the DEX files (one per revision) under fixed and adaptive prefix lengths.
func dexEcon(files []string) error {
	var revs []econx.Rev
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		revs = append(revs, econx.Rev{Name: p[len(p)-len("rN/classesN.dex"):], Files: []lab.EconFile{{Name: "classes.dex", Data: b}}})
	}
	plan := func(cfg dexz3.Config) func(f lab.EconFile) (*lab.Plan, bool, error) {
		return func(f lab.EconFile) (*lab.Plan, bool, error) {
			df, err := dexz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), dexz3.Limits{})
			if err != nil {
				return nil, false, err
			}
			rep, err := dexz3.Encode(df, f.Data, cfg)
			if err != nil {
				return nil, false, err
			}
			return econx.RepPlan(rep.Meta, rep.Comps, "dexn", dexz3.Decode), false, nil
		}
	}
	reps := []econx.Rep{{Name: "cdc", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "dex"); return p, false, err }}}
	for _, a := range []int{0, 12, 25, 50} {
		for _, kb := range []int{4} {
			reps = append(reps, econx.Rep{Name: fmt.Sprintf("dex-unit-%dk-auto%d", kb, a), Plan: plan(dexz3.Config{Auto: a, Cut: econx.UnitCut(kb).Cut})})
		}
	}
	fmt.Printf("%-24s", "rep")
	for _, r := range revs {
		fmt.Printf(" %9s", r.Name)
	}
	fmt.Printf(" %9s %7s\n", "total", "newComp")
	for _, rp := range reps {
		res := econx.Run(revs, rp, econx.Opts{TreeFan: 64})
		fmt.Printf("%-24s", rp.Name)
		for _, a := range res.PerRev {
			fmt.Printf(" %9s", econx.Kib(a.Pack))
		}
		fmt.Printf(" %9s %7d", econx.Kib(res.Total.Pack), res.Total.NewComps)
		if !res.Exact {
			fmt.Printf("  NOT EXACT: %s", res.Err)
		}
		fmt.Println()
	}
	return nil
}
