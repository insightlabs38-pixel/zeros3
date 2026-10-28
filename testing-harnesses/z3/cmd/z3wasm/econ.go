package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/econx"
	"zeros3-z3/lab"
	"zeros3-z3/wasmz3"
)

func init() { cmds["econ"] = econCmd }

func isFallback(err error) bool {
	return errors.Is(err, wasmz3.ErrFormat) || errors.Is(err, wasmz3.ErrUnsupported) || errors.Is(err, wasmz3.ErrLimit)
}

func withModule(f lab.EconFile, fn func(*wasmz3.Module) (*lab.Plan, error)) (*lab.Plan, bool, error) {
	dir, err := wasmz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), wasmz3.Limits{})
	if err == nil {
		var m *wasmz3.Module
		if m, err = wasmz3.Analyze(dir, f.Data, wasmz3.Limits{}); err == nil {
			var p *lab.Plan
			if p, err = fn(m); err == nil {
				return p, false, nil
			}
		}
	}
	if !isFallback(err) {
		return nil, false, err
	}
	p, e := lab.CDCPlan(f, "wasm")
	return p, true, e
}

func rawCut(c econx.Cutter) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withModule(f, func(m *wasmz3.Module) (*lab.Plan, error) {
			return econx.ConcatPlan(f.Data, c.Cut(f.Data, wasmz3.RawSecs(m, f.Data)), "raw"), nil
		})
	}
}

// perType cuts at every item of the listed section ids and at section ends.
func perType(ids ...uint16) econx.Cutter { return econx.PerItem(ids...) }

func allReps() []econx.Rep {
	reps := []econx.Rep{
		{Name: "cdc", Plan: func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "wasm"); return p, false, err }},
		{Name: "sections", Plan: rawCut(econx.Sections())},
		{Name: "funcs", Plan: rawCut(perType(wasmz3.SecCode))},
		{Name: "datas", Plan: rawCut(perType(wasmz3.SecData))},
		{Name: "funcs+datas", Plan: rawCut(perType(wasmz3.SecCode, wasmz3.SecData, wasmz3.SecElement))},
		{Name: "raw-unit-4k", Plan: rawCut(econx.UnitCut(4))},
	}
	return append(reps, exactReps()...)
}

func readRev(p string) (econx.Rev, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return econx.Rev{}, err
	}
	n := strings.TrimSuffix(filepath.Base(p), ".wasm")
	if n == "index_bg" || n == "esbuild" || strings.HasPrefix(n, "sql-wasm") {
		n = filepath.Base(filepath.Dir(filepath.Dir(p)))
		if strings.Contains(p, "/dist/") {
			n = filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
		}
		if i := strings.LastIndex(n, "-"); i > 0 {
			n = n[i+1:]
		}
	}
	return econx.Rev{Name: n, Files: []lab.EconFile{{Name: filepath.Base(p), Data: b}}}, nil
}

// econ [-star] [-tree N] [-ref N] REPS|all WASM... : each file is one revision, in order.
func econCmd(args []string) error {
	var o econx.Opts
	o.TreeFan = 64
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-star":
			o.Star = true
			args = args[1:]
		case "-tree":
			fmt.Sscan(args[1], &o.TreeFan)
			args = args[2:]
		case "-ref":
			fmt.Sscan(args[1], &o.RefBytes)
			args = args[2:]
		default:
			return fmt.Errorf("unknown flag %s", args[0])
		}
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: econ [-star] [-tree N] [-ref N] REPS|all WASM...")
	}
	want := map[string]bool{}
	if args[0] != "all" {
		for _, n := range strings.Split(args[0], ",") {
			want[n] = true
		}
	}
	var revs []econx.Rev
	for _, p := range args[1:] {
		r, err := readRev(p)
		if err != nil {
			return err
		}
		revs = append(revs, r)
	}
	fmt.Printf("%-16s", "rep")
	for _, r := range revs {
		fmt.Printf(" %9s", r.Name)
	}
	fmt.Printf(" %9s %7s %5s\n", "total", "newComp", "fb")
	for _, rp := range allReps() {
		if len(want) > 0 && !want[rp.Name] {
			continue
		}
		res := econx.Run(revs, rp, o)
		fmt.Printf("%-16s", rp.Name)
		for _, a := range res.PerRev {
			fmt.Printf(" %9s", econx.Kib(a.Pack))
		}
		fmt.Printf(" %9s %7d %5d", econx.Kib(res.Total.Pack), res.Total.NewComps, res.Total.Fallback)
		if !res.Exact {
			fmt.Printf("  NOT EXACT: %s", strings.TrimSpace(res.Err))
		}
		fmt.Println()
	}
	return nil
}
