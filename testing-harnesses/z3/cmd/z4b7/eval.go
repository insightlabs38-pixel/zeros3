package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	pl "zeros3-z3/planner2"
)

func scenarioList() []pl.Scenario {
	all := pl.Scenarios()
	if *scenFlag == "" {
		return all
	}
	var out []pl.Scenario
	for _, s := range all {
		for _, p := range strings.Split(*scenFlag, ",") {
			if strings.HasPrefix(s.Name, p) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func evalAll(lins []pl.Lineage) {
	proto, err := pl.LoadProtocol(*protoP)
	die(err)
	folds, err := pl.SplitFolds(proto, lins)
	die(err)
	base := proto.BaseConfig()
	adaptive := []string{"sim-conf", "lineage-prior", "class-prior", "lifetime", "read-ev", "bandit", "deferred", "group-reopt", "combined", "combined-no-defer", "cons", "cons-group"}
	autoSet := []string{"sim-conf", "lineage-prior", "class-prior", "lifetime", "read-ev", "deferred", "group-reopt", "combined", "combined-no-defer", "cons-group"}
	full := append(append([]string{}, pl.PolicyNames...), pl.AblationNames...)
	full = append(full, "premium-cap-0", "cons", "cons-group")
	core := []string{"always-cdc", "greedy", "premium-cap", "expected-value", "class-prior", "lifetime", "sim-conf", "cons", "cons-group", "combined", "combined-no-defer", "read-ev"}
	seeds := proto.Seeds
	if *seedsN < len(seeds) {
		seeds = seeds[:*seedsN]
	}
	var all []pl.Row
	t0 := time.Now()
	tunedAll := map[string]map[string]pl.AParams{}
	for _, held := range proto.FoldNames() {
		tuned, scores, err := pl.Tune(proto, folds, held, adaptive, base, *workers)
		die(err)
		for _, ab := range pl.AblationNames {
			if ab != "combined-no-defer" {
				tuned[ab] = tuned["combined"]
			}
		}
		// validated choice: the preset with the best TRAINING-fold score
		chosen, bs := "", 1e18
		for _, n := range autoSet {
			if scores[n] < bs {
				chosen, bs = n, scores[n]
			}
		}
		tuned["__auto"] = pl.AParams{Name: chosen}
		tunedAll[held] = tuned
		prior := pl.Calibrate(pl.Others(folds, held))
		fmt.Fprintf(os.Stderr, "fold %-8s lineages=%d auto=%s (train score %.3f) prior=%+v (%.0fs)\n", held, len(folds[held]), chosen, bs, prior, time.Since(t0).Seconds())
		var jobs []pl.Job
		for _, sc := range scenarioList() {
			for i, sd := range seeds {
				oracle := i == 0 && sc.Name == "S1-retain-all"
				max := 120000
				if i == 0 && strings.HasPrefix(sc.Name, "S2") {
					oracle, max = true, 300
				}
				pols := full
				if sc.Name[:2] > "S4" {
					pols = core
				}
				pols = append(append([]string{}, pols...), chosen)
				jobs = append(jobs, pl.Job{Fold: held, Scenario: sc, Seed: sd, Lins: folds[held], Prior: prior, Tuned: tuned, Cfg: base,
					Books: proto.Books, Policies: dedupe(pols), Oracle: oracle, OracleMax: max, Auto: chosen})
			}
		}
		res, err := pl.Parallel(jobs, *workers)
		die(err)
		for _, r := range res {
			all = append(all, r...)
		}
	}
	fh, err := os.Create(*out)
	die(err)
	zw := gzip.NewWriter(fh)
	enc := json.NewEncoder(zw)
	die(enc.Encode(struct {
		Rows  []pl.Row
		Tuned map[string]map[string]pl.AParams
	}{all, tunedAll}))
	die(zw.Close())
	die(fh.Close())
	fmt.Fprintf(os.Stderr, "wrote %d rows to %s in %.0fs\n", len(all), *out, time.Since(t0).Seconds())
}

type rowFile struct {
	Rows  []pl.Row
	Tuned map[string]map[string]pl.AParams
}

func loadRows() rowFile {
	fh, err := os.Open(*out)
	die(err)
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	die(err)
	var rf rowFile
	die(json.NewDecoder(zr).Decode(&rf))
	return rf
}

type agg struct {
	n                     int
	rel                   map[string]float64 // sum of cost/always-cdc per book
	bytes                 float64
	bad, missed, ffs, lin int
}

func summarize() {
	rf := loadRows()
	books := []string{"A_storage", "B_io_heavy"}
	// index always-cdc rows
	type key struct {
		fold, sc string
		seed     int64
	}
	cdc := map[key]pl.Row{}
	for _, r := range rf.Rows {
		if r.Policy == "always-cdc" {
			cdc[key{r.Fold, r.Scenario, r.Seed}] = r
		}
	}
	policies := map[string]bool{}
	folds := map[string]bool{}
	scs := map[string]bool{}
	for _, r := range rf.Rows {
		policies[r.Policy], folds[r.Fold], scs[r.Scenario] = true, true, true
	}
	pn, fn, sn := keys(policies), keys(folds), keys(scs)
	// mean relative cost per (policy, fold, book) over all scenarios+seeds
	rel := func(p, f, sc, b string) (float64, float64, int) {
		var sum, sb float64
		var n int
		for _, r := range rf.Rows {
			if r.Policy != p || (f != "" && r.Fold != f) || (sc != "" && r.Scenario != sc) {
				continue
			}
			c := cdc[key{r.Fold, r.Scenario, r.Seed}]
			sum += r.Cost[b] / c.Cost[b]
			sb += float64(r.FinalPhys) / float64(c.FinalPhys)
			n++
		}
		if n == 0 {
			return 0, 0, 0
		}
		return sum / float64(n), sb / float64(n), n
	}
	fmt.Println("== mean operational cost / always-cdc, held-out fold x policy (all scenarios+seeds), book A | book B | final bytes")
	for _, b := range books {
		fmt.Printf("\n[%s]\n%-22s", b, "policy")
		for _, f := range fn {
			fmt.Printf(" %9s", f)
		}
		fmt.Printf(" %9s\n", "mean")
		for _, p := range pn {
			fmt.Printf("%-22s", p)
			var tot float64
			for _, f := range fn {
				v, _, _ := rel(p, f, "", b)
				tot += v
				fmt.Printf(" %9.3f", v)
			}
			fmt.Printf(" %9.3f\n", tot/float64(len(fn)))
		}
	}
	fmt.Println("\n== by scenario (mean over folds+seeds), book A")
	fmt.Printf("%-22s", "policy")
	for _, s := range sn {
		fmt.Printf(" %9.9s", s[:2])
	}
	fmt.Println()
	for _, p := range pn {
		fmt.Printf("%-22s", p)
		for _, s := range sn {
			v, _, _ := rel(p, "", s, "A_storage")
			fmt.Printf(" %9.3f", v)
		}
		fmt.Println()
	}
}

func keys(m map[string]bool) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	sort.Strings(o)
	return o
}

func dedupe(x []string) []string {
	seen := map[string]bool{}
	var o []string
	for _, v := range x {
		if !seen[v] {
			seen[v] = true
			o = append(o, v)
		}
	}
	return o
}
