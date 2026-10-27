package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// variantBase names the variant a store already holds besides r0 (the same sequence of
// writes with a different table size: tables split or merged, nothing else changes).
var variantBase = map[string]string{"x01-reexport-half": "v09-reexport", "x02-reexport-double": "v09-reexport"}

const refBytes = 96 // Z3-18/29 convention: one component reference in a manifest

// agg is the accounting of one state under one representation.
type agg struct {
	Raw       int64            `json:"raw"`
	Pack      int64            `json:"pack"`      // packed new components + references + recipe (the cost)
	New       int64            `json:"new"`       // unpacked new component bytes
	PackedNew int64            `json:"packedNew"` // packed new component bytes
	Meta      int64            `json:"meta"`      // recipe bytes charged (head, no digests)
	Refs      int              `json:"refs"`      // component references
	NewComps  int              `json:"newComps"`
	Fallback  int              `json:"fallback"` // files that fell back to CDC
	Files     int              `json:"files"`
	Classes   map[string]int64 `json:"classes,omitempty"` // packed new bytes by component class
	Exact     bool             `json:"exact"`
	Err       string           `json:"err,omitempty"`
}

func (a *agg) add(o agg) {
	a.Raw += o.Raw
	a.Pack += o.Pack
	a.New += o.New
	a.PackedNew += o.PackedNew
	a.Meta += o.Meta
	a.Refs += o.Refs
	a.NewComps += o.NewComps
	a.Fallback += o.Fallback
	a.Files += o.Files
	for k, v := range o.Classes {
		if a.Classes == nil {
			a.Classes = map[string]int64{}
		}
		a.Classes[k] += v
	}
}

// store is a component store plus the files (name and content) it already holds: an
// unchanged file costs nothing under every representation (the Z3-16/17 runner rule).
type store struct {
	ac   *lab.Acct
	seen map[string][32]byte
}

func newStore() *store { return &store{lab.NewAcct(refBytes, true), map[string][32]byte{}} }

func (s *store) Clone() *store {
	m := make(map[string][32]byte, len(s.seen))
	for k, v := range s.seen {
		m[k] = v
	}
	return &store{s.ac.Clone(), m}
}

// runFiles adds the plans of the files to the store and verifies every non-oracle plan by
// rebuilding the file from the serialized recipe and the stored components.
func runFiles(st *store, rep repDef, files []file) agg {
	ac := st.ac
	a := agg{Exact: true}
	for _, f := range files {
		a.Files++
		if h := sha256.Sum256(f.data); st.seen[f.name] == h {
			a.Raw += int64(len(f.data))
			continue
		} else {
			st.seen[f.name] = h
		}
		a.Raw += int64(len(f.data))
		pl, fb, err := rep.plan(f)
		if err != nil || pl == nil {
			a.Exact, a.Err = false, fmt.Sprint(err)
			continue
		}
		if fb {
			a.Fallback++
		}
		acc, err := ac.Add(pl)
		if err != nil {
			a.Exact, a.Err = false, err.Error()
			continue
		}
		a.Pack += acc.IncrementalPack
		a.New += acc.NewBytes
		a.PackedNew += acc.PackedNew
		a.Meta += acc.MetaBytes - refBytes*int64(acc.Components)
		a.Refs += acc.Components
		a.NewComps += acc.NewComponents
		for k, v := range acc.ClassPackedNew {
			if a.Classes == nil {
				a.Classes = map[string]int64{}
			}
			a.Classes[k] += v
		}
		if rep.oracle || pl.Oracle {
			continue
		}
		if pl.Rebuild == nil {
			a.Exact, a.Err = false, "no reconstruction path"
			continue
		}
		var out bytes.Buffer
		if err := pl.Rebuild(&out, ac.Get); err != nil {
			a.Exact, a.Err = false, err.Error()
		} else if out.Len() != len(f.data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(f.data) || !bytes.Equal(out.Bytes(), f.data) {
			a.Exact, a.Err = false, "rebuild differs: "+f.name
		}
	}
	return a
}

type corpusState struct {
	rev    string
	tables []file
	other  []file
	meta   revMeta
}

type revMeta struct {
	Rev, Mutation, Class, Base string
	Entries                    int
	Logical                    int64
}

type famMeta struct {
	Revisions []revMeta `json:"revisions"`
}

func loadFamily(dir string, revs []string) ([]corpusState, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	var fm famMeta
	if err := json.Unmarshal(b, &fm); err != nil {
		return nil, err
	}
	meta := map[string]revMeta{}
	for _, r := range fm.Revisions {
		meta[r.Rev] = r
	}
	var out []corpusState
	for _, r := range revs {
		t, o, err := loadRev(filepath.Join(dir, r))
		if err != nil {
			return nil, err
		}
		out = append(out, corpusState{r, t, o, meta[r]})
	}
	return out, nil
}

func revNames(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

type variantRow struct {
	Rev    string         `json:"rev"`
	Class  string         `json:"class"`
	Reps   map[string]agg `json:"reps"`
	Logica int64          `json:"logical"`
}

type econResult struct {
	Producer string         `json:"producer"`
	Target   int            `json:"target"`
	First    map[string]agg `json:"first"` // r0 under each representation
	Variants []variantRow   `json:"variants"`
	Chain    []chainRow     `json:"chain"`
}

type chainRow struct {
	Rev        string           `json:"rev"`
	Class      string           `json:"class"`
	Raw        int64            `json:"raw"`
	Incr       map[string]agg   `json:"incr"`    // against everything retained so far
	Rolling    map[string]agg   `json:"rolling"` // against the previous state only
	Cumulative map[string]int64 `json:"cumulative"`
	Other      agg              `json:"other"` // non-table files of the directory (CDC)
}

func pick(all []repDef, names string) []repDef {
	if names == "" {
		return all
	}
	set := map[string]bool{}
	for _, n := range strings.Split(names, ",") {
		set[n] = true
	}
	var out []repDef
	for _, r := range all {
		if set[r.name] {
			out = append(out, r)
		}
	}
	return out
}

// cmdEcon runs the economics: for every producer, the cost of each variant added to a store
// holding r0 under each representation, and the revision chain (incremental, rolling and
// cumulative) with the directory view.
func cmdEcon(args []string) error {
	fs := flag.NewFlagSet("econ", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	prods := fs.String("prod", "leveldb,rocksdb,pebble", "producers")
	target := fs.Int("target", 8<<10, "group target bytes")
	repNames := fs.String("reps", "", "comma-separated representation names for the variants (default all)")
	chainReps := fs.String("creps", "cdc,blocks,ublocks,full,ukv,kv", "representations for the chain")
	out := fs.String("json", "", "write results as JSON to this prefix")
	chain := fs.Bool("chain", true, "run the revision chain")
	vars := fs.Bool("variants", true, "run the variants")
	fs.Parse(args)
	reps := pick(allReps(*target), *repNames)
	creps := pick(allReps(*target), *chainReps)
	for _, prod := range strings.Split(*prods, ",") {
		res := &econResult{Producer: prod, Target: *target, First: map[string]agg{}}
		if *vars {
			vdir := filepath.Join(*root, prod, "variants")
			sts, err := loadFamily(vdir, revNames(vdir))
			if err != nil {
				return err
			}
			var r0 corpusState
			var rest []corpusState
			for _, s := range sts {
				if s.rev == "r0" {
					r0 = s
				} else {
					rest = append(rest, s)
				}
			}
			rows := make([]variantRow, len(rest))
			for i, s := range rest {
				rows[i] = variantRow{Rev: s.rev, Class: s.meta.Class, Reps: map[string]agg{}, Logica: s.meta.Logical}
			}
			for _, rep := range reps {
				ac0 := newStore()
				res.First[rep.name] = runFiles(ac0, rep, r0.tables)
				var wg sync.WaitGroup
				sem := make(chan struct{}, 4)
				var mu sync.Mutex
				byRev := map[string]corpusState{}
				for _, s := range rest {
					byRev[s.rev] = s
				}
				for i := range rest {
					wg.Add(1)
					sem <- struct{}{}
					go func(i int) {
						defer wg.Done()
						defer func() { <-sem }()
						ac := ac0.Clone()
						if b, ok := variantBase[rest[i].rev]; ok { // split/merge are measured against the table set they re-split
							runFiles(ac, rep, byRev[b].tables)
						}
						a := runFiles(ac, rep, rest[i].tables)
						mu.Lock()
						rows[i].Reps[rep.name] = a
						mu.Unlock()
					}(i)
				}
				wg.Wait()
				fmt.Fprintf(os.Stderr, "%s variants %s done\n", prod, rep.name)
			}
			res.Variants = rows
		}
		if *chain {
			cdir := filepath.Join(*root, prod, "chain")
			sts, err := loadFamily(cdir, revNames(cdir))
			if err != nil {
				return err
			}
			rows := make([]chainRow, len(sts))
			for i, s := range sts {
				rows[i] = chainRow{Rev: s.rev, Class: s.meta.Class, Incr: map[string]agg{}, Rolling: map[string]agg{}, Cumulative: map[string]int64{}}
				for _, f := range s.tables {
					rows[i].Raw += int64(len(f.data))
				}
			}
			for _, rep := range creps {
				ac := newStore()
				var cum int64
				for i, s := range sts {
					a := runFiles(ac, rep, s.tables)
					rows[i].Incr[rep.name] = a
					cum += a.Pack
					rows[i].Cumulative[rep.name] = cum
					if i == 0 {
						rows[i].Rolling[rep.name] = a
						continue
					}
					rc := newStore()
					runFiles(rc, rep, sts[i-1].tables)
					rows[i].Rolling[rep.name] = runFiles(rc, rep, s.tables)
				}
				fmt.Fprintf(os.Stderr, "%s chain %s done\n", prod, rep.name)
			}
			cdc := creps[0]
			for _, r := range creps {
				if r.name == "cdc" {
					cdc = r
				}
			}
			ac := newStore()
			for i, s := range sts {
				rows[i].Other = runFiles(ac, cdc, s.other)
			}
			res.Chain = rows
		}
		printEcon(res, reps, creps)
		if *out != "" {
			b, _ := json.MarshalIndent(res, "", " ")
			if err := os.WriteFile(*out+"-"+prod+".json", b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func printEcon(r *econResult, reps, creps []repDef) {
	h := fstate.Human
	fmt.Printf("\n### %s variants: cost of the revision added to a store holding r0 (packed bytes, 96 B/ref, group target %d KiB)\n", r.Producer, r.Target>>10)
	head := "| variant |"
	sep := "|---|"
	for _, rp := range reps {
		head += " " + rp.name + " |"
		sep += "---:|"
	}
	fmt.Println(head)
	fmt.Println(sep)
	row := "| r0 (first sight) |"
	for _, rp := range reps {
		a := r.First[rp.name]
		row += " " + h(a.Pack) + " |"
	}
	fmt.Println(row)
	for _, v := range r.Variants {
		row := "| " + v.Rev + " |"
		for _, rp := range reps {
			a := v.Reps[rp.name]
			s := h(a.Pack)
			if !a.Exact && !rp.oracle {
				s += "!"
			}
			if a.Fallback > 0 {
				s += fmt.Sprintf("(fb%d)", a.Fallback)
			}
			row += " " + s + " |"
		}
		fmt.Println(row)
	}
	if len(r.Chain) == 0 {
		return
	}
	head, sep = "| state |", "|---|"
	for _, rp := range creps {
		head += " " + rp.name + " |"
		sep += "---:|"
	}
	for _, kind := range []string{"incremental", "rolling"} {
		fmt.Printf("\n### %s chain, %s cost per state (packed bytes)\n", r.Producer, kind)
		fmt.Println(head)
		fmt.Println(sep)
		for _, c := range r.Chain {
			row := "| " + c.Rev + " |"
			for _, rp := range creps {
				a := c.Incr[rp.name]
				if kind == "rolling" {
					a = c.Rolling[rp.name]
				}
				row += " " + h(a.Pack) + " |"
			}
			fmt.Println(row)
		}
	}
	fmt.Printf("\n### %s chain totals (packed)\n", r.Producer)
	fmt.Println(head)
	fmt.Println(sep)
	var cum, roll = map[string]int64{}, map[string]int64{}
	for _, c := range r.Chain {
		for _, rp := range creps {
			cum[rp.name] = c.Cumulative[rp.name]
			roll[rp.name] += c.Rolling[rp.name].Pack
		}
	}
	for _, kv := range []struct {
		n string
		m map[string]int64
	}{{"cumulative retained", cum}, {"rolling (live state only)", roll}} {
		row := "| " + kv.n + " |"
		for _, rp := range creps {
			row += " " + h(kv.m[rp.name]) + " |"
		}
		fmt.Println(row)
	}
}
