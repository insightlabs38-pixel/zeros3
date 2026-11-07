package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/lz4z3"
)

func init() { cmds["lz4blocks"] = lz4blocks }

// lz4blocks DIR...: raw-block producer matrix. Class = file name up to the first '_'.
func lz4blocks(args []string) error {
	type agg struct{ n, pred, hyb, expl, comp, rec, explOnly int }
	res := map[string]*agg{}
	for _, d := range args {
		files, _ := filepath.Glob(filepath.Join(d, "*.lz4"))
		for _, f := range files {
			blk, _ := os.ReadFile(f)
			plain, seqs, err := lz4z3.Decode(blk, -1, lz4z3.Limits{})
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			rc := lz4z3.Analyze(plain, seqs, true)
			rep, err := rc.Replay(nil, plain, lz4z3.Limits{})
			if err != nil || string(rep) != string(blk) {
				return fmt.Errorf("%s: recipe does not reproduce the block", f)
			}
			cls := strings.SplitN(filepath.Base(f), "_", 2)[0]
			a := res[cls]
			if a == nil {
				a = &agg{}
				res[cls] = a
			}
			a.n++
			a.comp += len(blk)
			a.rec += len(rc.Marshal())
			a.explOnly += len(lz4z3.Recipe{Kind: lz4z3.KindExplicit, Seqs: seqs}.Marshal())
			switch rc.Kind {
			case lz4z3.KindPredicted:
				a.pred++
			case lz4z3.KindHybrid:
				a.hyb++
			default:
				a.expl++
			}
		}
	}
	var ks []string
	for k := range res {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Printf("%-16s %6s %6s %6s %6s %10s %10s %7s %10s %7s\n", "producer", "blocks", "pred", "hybrid", "expl", "comp B", "recipe B", "%comp", "expl-only", "%comp")
	for _, k := range ks {
		a := res[k]
		fmt.Printf("%-16s %6d %6d %6d %6d %10d %10d %6.2f%% %10d %6.1f%%\n", k, a.n, a.pred, a.hyb, a.expl, a.comp, a.rec, 100*float64(a.rec)/float64(a.comp), a.explOnly, 100*float64(a.explOnly)/float64(a.comp))
	}
	return nil
}
