package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/fstate"
)

var laneFamilies = map[string]string{"sqlite": "kv24", "leveldb": "kv200k", "rocksdb": "kv200k", "parquet": "t400k", "zarr": "a2048", "zarr3": "a2048v3"}
var laneKind = map[string]string{"sqlite": "sqlite", "leveldb": "ldb", "rocksdb": "sst", "parquet": "parquet", "zarr": "zarr", "zarr3": "zarr"}

// CharRow is one (variant, representation) measurement: the cost of the variant
// on a store that already holds r0.
type CharRow struct {
	Lane, Family, Rev, Mutation string
	Rep                         string
	Oracle                      bool
	Raw, Cost, Packed           int64
	Recipe, New                 int64
	Logical                     int64
	Exact                       bool
	Err                         string `json:",omitempty"`
	Fallback                    int64
	// First-sight rows (Rev == "r0") carry the cost of storing r0 on an empty store.
}

func cmdChars(args []string) error {
	fs := flag.NewFlagSet("chars", flag.ExitOnError)
	lane := fs.String("lane", "all", "sqlite|leveldb|rocksdb|parquet|zarr|all")
	jsonOut := fs.String("json", "", "write all rows as JSON")
	fs.Parse(args)
	lanes := []string{"sqlite", "leveldb", "rocksdb", "parquet", "zarr", "zarr3"}
	if *lane != "all" {
		lanes = strings.Split(*lane, ",")
	}
	var all []CharRow
	for _, l := range lanes {
		rows, err := charLane(l)
		if err != nil {
			return fmt.Errorf("%s: %w", l, err)
		}
		all = append(all, rows...)
		printLane(l, rows)
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(all, "", " ")
		return os.WriteFile(*jsonOut, b, 0o644)
	}
	return nil
}

func charLane(lane string) ([]CharRow, error) {
	dir := filepath.Join(cacheRoot(), strings.TrimSuffix(lane, "3"), laneFamilies[lane])
	meta, err := loadMeta(dir)
	if err != nil {
		return nil, err
	}
	load := func(rev string) (*fstate.State, error) {
		st, err := loadState(dir, rev, laneKind[lane])
		if err != nil {
			return nil, err
		}
		for _, f := range st.Files {
			if lane == "parquet" {
				loadSidecar(filepath.Join(dir, rev, f.Name), f.Data)
			}
		}
		return st, nil
	}
	base, err := load("r0")
	if err != nil {
		return nil, err
	}
	reps := laneReps(lane)
	r := &fstate.Runner{RefBytes: 96, Pack: true}
	var rows []CharRow
	add := func(m RevMeta, st *fstate.State, res []fstate.Row, want string) {
		for _, x := range res {
			if x.State != want {
				continue
			}
			rows = append(rows, CharRow{Lane: lane, Family: meta.Family, Rev: m.Rev, Mutation: m.Mutation, Rep: x.Rep, Oracle: x.Oracle,
				Raw: x.RawBytes, Cost: x.Cost, Packed: x.CostPacked, Recipe: x.Recipe, New: x.NewBytes, Logical: m.LogicalBytes, Exact: x.Exact, Err: x.Err, Fallback: x.Detail["fallback_files"]})
		}
	}
	for i, m := range meta.Revisions {
		if m.Rev == "r0" {
			res := r.RunStates([]*fstate.State{base}, reps)
			add(m, base, res, "r0")
			continue
		}
		st, err := load(m.Rev)
		if err != nil {
			return nil, err
		}
		b := base
		if m.Base != "" && m.Base != "r0" {
			if b, err = load(m.Base); err != nil {
				return nil, err
			}
		}
		_ = i
		res := r.RunStates([]*fstate.State{b, st}, reps)
		add(m, st, res, st.Label)
	}
	return rows, nil
}

func kib(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.0fK", float64(n)/1024)
	}
	return fmt.Sprintf("%d", n)
}

func printLane(lane string, rows []CharRow) {
	var order []string
	seen := map[string]bool{}
	for _, r := range rows {
		if !seen[r.Rep] {
			seen[r.Rep] = true
			order = append(order, r.Rep)
		}
	}
	fmt.Printf("\n== %s (cost of the variant on a store holding r0; production pack rule; bytes, K=KiB, M=MiB)\n", lane)
	fmt.Printf("%-34s %8s", "variant", "logical")
	for _, o := range order {
		fmt.Printf(" %18s", o)
	}
	fmt.Println()
	byRev := map[string]map[string]CharRow{}
	var revs []string
	for _, r := range rows {
		if byRev[r.Rev] == nil {
			byRev[r.Rev] = map[string]CharRow{}
			revs = append(revs, r.Rev)
		}
		byRev[r.Rev][r.Rep] = r
	}
	for _, rv := range revs {
		first := byRev[rv][order[0]]
		lbl := rv
		if rv == "r0" {
			lbl = "r0 (first sight, raw " + kib(first.Raw) + ")"
		}
		fmt.Printf("%-34.34s %8s", lbl, kib(first.Logical))
		for _, o := range order {
			x := byRev[rv][o]
			mark := ""
			if !x.Exact && !x.Oracle {
				mark = "!"
			}
			if x.Fallback > 0 {
				mark += "f"
			}
			fmt.Printf(" %17s%1s", kib(x.Packed), mark)
		}
		fmt.Println()
	}
}
