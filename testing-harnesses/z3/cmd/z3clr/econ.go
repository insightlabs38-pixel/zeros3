package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/lab"
)

func init() { cmds["econ"] = econCmd }

func isFallback(err error) bool {
	return errors.Is(err, clrz3.ErrFormat) || errors.Is(err, clrz3.ErrUnsupported) || errors.Is(err, clrz3.ErrLimit)
}

func cdcPlan(f lab.EconFile) (*lab.Plan, bool, error) {
	p, err := lab.CDCPlan(f, "clr")
	return p, true, err
}

// withFile parses f and runs fn; assemblies the parser refuses stay plain CDC.
func withFile(f lab.EconFile, fn func(*clrz3.File) (*lab.Plan, error)) (*lab.Plan, bool, error) {
	cf, err := clrz3.Parse(bytes.NewReader(f.Data), int64(len(f.Data)), clrz3.Limits{})
	if err == nil {
		var p *lab.Plan
		if p, err = fn(cf); err == nil {
			return p, false, nil
		}
	}
	if !isFallback(err) {
		return nil, false, err
	}
	return cdcPlan(f)
}

// rawCut is a structure-only baseline: the raw file cut by the cutter over its record sections.
func rawCut(c econx.Cutter) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withFile(f, func(cf *clrz3.File) (*lab.Plan, error) {
			secs, err := clrz3.RawSecs(cf, f.Data)
			if err != nil {
				return nil, err
			}
			return econx.ConcatPlan(f.Data, c.Cut(f.Data, secs), "raw"), nil
		})
	}
}

// peSections cuts at the PE header end and every PE section end only.
func peSections() func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withFile(f, func(cf *clrz3.File) (*lab.Plan, error) {
			cuts := []int{int(cf.SizeOfHeaders)}
			for _, s := range cf.Secs {
				cuts = append(cuts, int(s.RawOff)+int(s.RawSize))
			}
			return econx.ConcatPlan(f.Data, cuts, "raw"), nil
		})
	}
}

// layerOracle prices the masked payload alone (no references, no recipe): what a layer could save.
func layerOracle(layers int, c econx.Cutter) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withFile(f, func(cf *clrz3.File) (*lab.Plan, error) {
			pl, secs, err := clrz3.LayerPayload(cf, f.Data, layers)
			if err != nil {
				return nil, err
			}
			p := &lab.Plan{Oracle: true}
			prev := 0
			for _, e := range c.Cut(pl, secs) {
				if e > prev {
					p.Pieces = append(p.Pieces, lab.Piece{Comp: econx.MkComp(pl[prev:e], "payload")})
					prev = e
				}
			}
			if prev < len(pl) {
				p.Pieces = append(p.Pieces, lab.Piece{Comp: econx.MkComp(pl[prev:], "payload")})
			}
			return p, nil
		})
	}
}

// exact is the real representation (serialized, verified by a decode from its components).
func exact(cfg clrz3.Config) func(f lab.EconFile) (*lab.Plan, bool, error) {
	return func(f lab.EconFile) (*lab.Plan, bool, error) {
		return withFile(f, func(cf *clrz3.File) (*lab.Plan, error) {
			rep, err := clrz3.Encode(cf, f.Data, cfg)
			if err != nil {
				return nil, err
			}
			return econx.RepPlan(rep.Meta, rep.Comps, "clrn", clrz3.Decode), nil
		})
	}
}

func allReps() []econx.Rep {
	cdc := econx.Rep{Name: "cdc", Plan: cdcPlan}
	cdc.Plan = func(f lab.EconFile) (*lab.Plan, bool, error) { p, err := lab.CDCPlan(f, "clr"); return p, false, err }
	reps := []econx.Rep{
		cdc,
		{Name: "pe-sections", Plan: peSections()},
		{Name: "meta-sections", Plan: rawCut(econx.Sections())},
		{Name: "methods", Plan: rawCut(econx.PerItem(0x204))},
		{Name: "raw-unit-4k", Plan: rawCut(econx.UnitCut(4))},
		{Name: "raw-unit-2k", Plan: rawCut(econx.UnitCut(2))},
		{Name: "o-idx-4k", Plan: layerOracle(clrz3.LayerIdx, econx.UnitCut(4))},
		{Name: "o-idx+tok-4k", Plan: layerOracle(clrz3.LayerIdx|clrz3.LayerTok, econx.UnitCut(4))},
		{Name: "o-all-4k", Plan: layerOracle(clrz3.LayerAll, econx.UnitCut(4))},
	}
	for _, c := range []econx.Cutter{econx.CDCProd(), econx.UnitCut(64), econx.UnitCut(16), econx.UnitCut(8), econx.UnitCut(4), econx.UnitCut(2)} {
		reps = append(reps, econx.Rep{Name: "n-" + c.Name, Plan: exact(clrz3.Config{Cut: c.Cut})})
	}
	for _, a := range []int{5, 12, 25} {
		for _, c := range []econx.Cutter{econx.UnitCut(4), econx.UnitCut(2)} {
			reps = append(reps, econx.Rep{Name: fmt.Sprintf("n-%s-a%d", c.Name, a), Plan: exact(clrz3.Config{Auto: a, Cut: c.Cut})})
		}
	}
	return reps
}

func readRev(p string) (econx.Rev, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return econx.Rev{}, err
	}
	n := filepath.Base(filepath.Dir(p))
	if base := filepath.Base(p); strings.HasSuffix(base, ".dll") && !strings.Contains(p, "/clr/") {
		n = strings.TrimSuffix(base, ".dll")
	}
	return econx.Rev{Name: n, Files: []lab.EconFile{{Name: filepath.Base(p), Data: b}}}, nil
}

// econ [-star] [-tree N] [-ref N] REPS DLL... : each DLL is one revision, in order.
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
		return fmt.Errorf("usage: econ [-star] [-tree N] [-ref N] REPS|all DLL...")
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
