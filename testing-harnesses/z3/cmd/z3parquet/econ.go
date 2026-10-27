package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// scenario is one measured addition: the cost of Target added to a store that already holds
// Store (cost = incremental packed bytes + recipes + references).
type scenario struct {
	Name, Class string
	Store       []state
	Target      state
	Note        string
}

type row struct {
	Name  string                 `json:"name"`
	Class string                 `json:"class"`
	Reps  map[string]lab.EconAgg `json:"reps"`
	Raw   int64                  `json:"raw"`
}

func file(s state) (lab.EconFile, error) {
	b, err := readFile(s.Path)
	return lab.EconFile{Name: s.Fam + "/" + s.Rev, Data: b}, err
}

// scenarios builds the measured additions of the variant families.
func scenarios() (first []scenario, rows []scenario, err error) {
	pa, _, err := loadFamily("pyarrow-variants")
	if err != nil {
		return
	}
	by := map[string]state{}
	for _, s := range pa {
		by[s.Rev] = s
	}
	r0 := by["r0"]
	first = append(first, scenario{Name: "r0 (pyarrow)", Class: ClsWorkload, Target: r0})
	for _, s := range pa {
		if s.Rev == "r0" {
			continue
		}
		rows = append(rows, scenario{Name: s.Rev, Class: s.Class, Store: []state{r0}, Target: s, Note: s.Mutation})
	}
	rows = append(rows, scenario{Name: "a01→r0 append into last row group", Class: ClsWorkload, Store: []state{by["a01-last-rg-partial-395k"]}, Target: r0})
	dd, _, derr := loadFamily("duckdb-variants")
	if derr == nil {
		dby := map[string]state{}
		for _, s := range dd {
			dby[s.Rev] = s
		}
		d01 := dby["d01-snappy"]
		first = append(first, scenario{Name: "d01 (duckdb)", Class: ClsReexport, Target: d01})
		rows = append(rows, scenario{Name: "d01 cross-producer re-export (store r0)", Class: ClsReexport, Store: []state{r0}, Target: d01})
		for _, s := range dd {
			if s.Rev != "d01-snappy" {
				rows = append(rows, scenario{Name: s.Rev, Class: s.Class, Store: []state{d01}, Target: s, Note: s.Mutation})
			}
		}
	}
	ct, _, cerr := loadFamily("controls")
	if cerr == nil {
		for _, s := range ct {
			rows = append(rows, scenario{Name: s.Rev, Class: s.Class + " (control)", Target: s, Note: s.Mutation})
		}
	}
	return
}

func runScenario(reps []lab.EconRep, sc scenario, base map[string]*lab.EconStore) (row, error) {
	r := row{Name: sc.Name, Class: sc.Class, Reps: map[string]lab.EconAgg{}}
	tf, err := file(sc.Target)
	if err != nil {
		return r, err
	}
	r.Raw = int64(len(tf.Data))
	var sfiles []lab.EconFile
	for _, s := range sc.Store {
		f, err := file(s)
		if err != nil {
			return r, err
		}
		sfiles = append(sfiles, f)
	}
	for _, rep := range reps {
		st := lab.NewEconStore()
		for _, f := range sfiles {
			st.Run(rep, []lab.EconFile{f})
		}
		r.Reps[rep.Name] = st.Run(rep, []lab.EconFile{tf})
	}
	return r, nil
}

func allReps(opt repOptions) []lab.EconRep {
	return append([]lab.EconRep{cdcRep(), physRep("rgs", "rg", "rg"), physRep("chunks", "chunk", "chunk"), physRep("pages", "page", "page"), dpageRep()}, logicalReps(opt)...)
}

func pick(all []lab.EconRep, names string) []lab.EconRep {
	if names == "" {
		return all
	}
	set := map[string]bool{}
	for _, n := range strings.Split(names, ",") {
		set[n] = true
	}
	var out []lab.EconRep
	for _, r := range all {
		if set[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func cmdEcon(args []string) error {
	fs := flag.NewFlagSet("econ", flag.ExitOnError)
	repNames := fs.String("reps", "cdc,rgs,chunks,pages,dpages", "representations")
	only := fs.String("only", "", "comma-separated scenario name prefixes")
	out := fs.String("json", "", "write JSON here")
	target := fs.Int("target", 8<<10, "group target bytes (logical representations)")
	par := fs.Int("par", 3, "scenarios in parallel")
	fs.Parse(args)
	reps := pick(allReps(repOptions{Target: *target}), *repNames)
	first, rows, err := scenarios()
	if err != nil {
		return err
	}
	match := func(n string) bool {
		if *only == "" {
			return true
		}
		for _, p := range strings.Split(*only, ",") {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
		return false
	}
	var res []row
	for _, sc := range first {
		r, err := runScenario(reps, sc, nil)
		if err != nil {
			return err
		}
		res = append(res, r)
	}
	out2 := make([]row, len(rows))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *par)
	var mu sync.Mutex
	var firstErr error
	for i, sc := range rows {
		if !match(sc.Name) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, sc scenario) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := runScenario(reps, sc, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			out2[i] = r
			fmt.Fprintln(os.Stderr, "done", sc.Name)
		}(i, sc)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	for _, r := range out2 {
		if r.Name != "" {
			res = append(res, r)
		}
	}
	printRows(res, reps)
	if *out != "" {
		b, _ := json.MarshalIndent(res, "", " ")
		return os.WriteFile(*out, b, 0o644)
	}
	return nil
}

func printRows(rows []row, reps []lab.EconRep) {
	h := fstate.Human
	head, sep := "| scenario | class | file |", "|---|---|---:|"
	for _, r := range reps {
		head += " " + r.Name + " |"
		sep += "---:|"
	}
	fmt.Println(head)
	fmt.Println(sep)
	for _, r := range rows {
		line := fmt.Sprintf("| %s | %s | %s |", r.Name, r.Class, h(r.Raw))
		for _, rp := range reps {
			a := r.Reps[rp.Name]
			s := h(a.Pack)
			if !a.Exact && !rp.Oracle {
				s += "!"
			}
			if a.Fallback > 0 {
				s += "(fb)"
			}
			line += " " + s + " |"
		}
		fmt.Println(line)
	}
}
