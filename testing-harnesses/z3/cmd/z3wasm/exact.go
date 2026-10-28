package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/wasmz3"
)

func init() { cmds["rt"] = rt }

func exact(cfg wasmz3.Config) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withModule(f, func(m *wasmz3.Module) (*lab.Plan, error) {
			rep, err := wasmz3.Encode(m, f.Data, cfg)
			if err != nil {
				return nil, err
			}
			return econx.RepPlan(rep.Meta, rep.Comps, "wasmn", wasmz3.Decode), nil
		})
	}
}

// exactReps are the real representations: grouping sweep, adaptive prefix, name hints.
func exactReps() []econx.Rep {
	var reps []econx.Rep
	for _, c := range []econx.Cutter{econx.UnitCut(64), econx.UnitCut(16), econx.UnitCut(4), econx.UnitCut(2)} {
		reps = append(reps, econx.Rep{Name: "n-" + c.Name, Plan: exact(wasmz3.Config{Cut: c.Cut})})
		reps = append(reps, econx.Rep{Name: "n-" + c.Name + "-a12", Plan: exact(wasmz3.Config{Auto: 12, Cut: c.Cut})})
	}
	reps = append(reps, econx.Rep{Name: "n-unit-4k-a12-names", Plan: exact(wasmz3.Config{Auto: 12, UseNames: true, Cut: econx.UnitCut(4).Cut})})
	reps = append(reps, econx.Rep{Name: "n-cdc-prod-a12", Plan: exact(wasmz3.Config{Auto: 12, Cut: econx.CDCProd().Cut})})
	return reps
}

func printStats(st wasmz3.Stats) {
	fmt.Printf("  payload %d comps %d opaque bodies %d\n", st.Payload, st.Comps, st.Opaque)
	for sp := wasmz3.SpType; sp < wasmz3.SpAddr; sp++ {
		fmt.Printf("    %-7s candidates %6d (dup %5d) slots %7d refbytes %8d\n", wasmz3.SpaceName[sp], st.Candidate[sp], st.Dups[sp], st.Slots[sp], st.RefBytes[sp])
	}
}

// rt FILE... : encode with a verifying decode, report statistics.
func rt(args []string) error {
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		if err != nil {
			fmt.Printf("%s: parse: %v\n", p, err)
			continue
		}
		m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
		if err != nil {
			fmt.Printf("%s: analyze: %v\n", p, err)
			continue
		}
		t0 := time.Now()
		rep, err := wasmz3.Encode(m, d, wasmz3.Config{Cut: econx.UnitCut(4).Cut})
		if err != nil {
			fmt.Printf("%s: encode: %v\n", p, err)
			continue
		}
		fmt.Printf("%s: %d B -> meta %d, %d comps, %v\n", p, len(d), len(rep.Meta), len(rep.Comps), time.Since(t0))
		if os.Getenv("V") != "" {
			printStats(rep.Stats)
		}
	}
	return nil
}
