package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	pl "zeros3-z3/planner2"
)

// verdict applies the preregistered promotion rule and prints supporting tables.
func verdict() {
	rf := loadRows()
	books := []string{"A_storage", "B_io_heavy"}
	type key struct {
		book, fold, sc string
		seed           int64
	}
	cdc := map[key]pl.Row{}
	for _, r := range rf.Rows {
		if r.Policy == "always-cdc" {
			cdc[key{r.Book, r.Fold, r.Scenario, r.Seed}] = r
		}
	}
	folds := map[string]bool{}
	pols := map[string]bool{}
	for _, r := range rf.Rows {
		folds[r.Fold], pols[r.Policy] = true, true
	}
	fn := keys(folds)
	// per-world ratios
	ratios := func(p, fold, book string, scen func(string) bool) []float64 {
		var x []float64
		for _, r := range rf.Rows {
			if r.Policy == p && r.Fold == fold && r.Book == book && scen(r.Scenario) {
				x = append(x, r.Cost[book]/cdc[key{r.Book, r.Fold, r.Scenario, r.Seed}].Cost[book])
			}
		}
		return x
	}
	all := func(string) bool { return true }
	mean := func(x []float64) float64 { return pl.Mean(x) }
	simple := []string{"greedy", "premium-cap", "expected-value", "late-bootstrap", "always-cdc"}
	res := map[string]interface{}{}
	for _, cand := range []string{"auto", "combined", "class-prior", "lifetime", "sim-conf", "cons-group", "combined-no-defer"} {
		wins, worst := map[string]int{}, map[string]float64{}
		var impr []float64
		fmt.Printf("\n-- candidate %q vs best simple baseline per held-out fold (mean cost ratio vs always-cdc)\n", cand)
		for _, b := range books {
			for _, f := range fn {
				c := mean(ratios(cand, f, b, all))
				best, bn := math.Inf(1), ""
				for _, s := range simple {
					if v := mean(ratios(s, f, b, all)); v < best {
						best, bn = v, s
					}
				}
				win := c < best
				if win {
					wins[b]++
				}
				if c > worst[b] {
					worst[b] = c
				}
				impr = append(impr, (best-c)/best)
				fmt.Printf("  %-10s %-8s cand %.3f best-simple %.3f (%s) %s  rel.improvement %+.1f%%\n", b, f, c, best, bn, map[bool]string{true: "WIN ", false: "lose"}[win], 100*(best-c)/best)
			}
		}
		avg := mean(impr)
		ok := wins["A_storage"] >= 4 && wins["B_io_heavy"] >= 4 && avg >= 0.05 && worst["A_storage"] <= 1.03 && worst["B_io_heavy"] <= 1.03
		spec := wins["A_storage"] >= 2 || wins["B_io_heavy"] >= 2
		fmt.Printf("  wins A=%d B=%d of %d; mean rel.improvement %+.1f%%; worst fold ratio A=%.3f B=%.3f => promote-rule=%v, >=2-folds=%v\n", wins["A_storage"], wins["B_io_heavy"], len(fn), 100*avg, worst["A_storage"], worst["B_io_heavy"], ok, spec)
		res[cand] = map[string]interface{}{"wins_A": wins["A_storage"], "wins_B": wins["B_io_heavy"], "mean_rel_improvement": avg, "worst_A": worst["A_storage"], "worst_B": worst["B_io_heavy"], "promote_rule": ok, "two_fold_rule": spec}
	}
	// bootstrap CI of the mean ratio for headline policies (resampling worlds, deterministic)
	fmt.Println("\n-- mean cost ratio vs always-cdc over all held-out worlds, 95% bootstrap CI over worlds")
	ci := map[string]interface{}{}
	for _, p := range []string{"greedy", "premium-cap", "expected-value", "late-bootstrap", "cons", "cons-group", "class-prior", "lifetime", "sim-conf", "read-ev", "deferred", "combined", "combined-no-defer", "auto"} {
		for _, b := range books {
			var x []float64
			for _, f := range fn {
				x = append(x, ratios(p, f, b, all)...)
			}
			if len(x) == 0 {
				continue
			}
			lo, hi := bootstrap(x)
			fmt.Printf("  %-18s %-10s n=%3d mean %.3f CI [%.3f, %.3f]\n", p, b, len(x), mean(x), lo, hi)
			ci[p+"|"+b] = []float64{mean(x), lo, hi, float64(len(x))}
		}
	}
	res["bootstrap"] = ci
	// worst cases: per policy, largest ratio vs always-cdc and vs greedy
	fmt.Println("\n-- worst single worlds (cost ratio vs always-cdc, book A)")
	for _, p := range []string{"greedy", "expected-value", "class-prior", "auto", "combined"} {
		var worst pl.Row
		wr := 0.0
		for _, r := range rf.Rows {
			if r.Policy != p || r.Book != "A_storage" {
				continue
			}
			v := r.Cost["A_storage"] / cdc[key{r.Book, r.Fold, r.Scenario, r.Seed}].Cost["A_storage"]
			if v > wr {
				wr, worst = v, r
			}
		}
		fmt.Printf("  %-16s %.3f in %s/%s/seed %d\n", p, wr, worst.Fold, worst.Scenario, worst.Seed)
		res["worst_"+p] = map[string]interface{}{"ratio": wr, "fold": worst.Fold, "scenario": worst.Scenario, "seed": worst.Seed}
	}
	// adverse-outcome counts, regret vs oracle
	fmt.Println("\n-- lineage-level adverse outcomes, S1 (sum over folds+seeds) and storage regret vs static clairvoyant optimum (seed0 worlds)")
	fmt.Printf("  %-18s %8s %8s %8s %10s %10s\n", "policy", "bad", "missed", "ffs", "bytes/cdc", "regret%")
	adv := map[string]interface{}{}
	for _, p := range []string{"greedy", "premium-cap", "late-bootstrap", "expected-value", "cons", "cons-group", "class-prior", "lifetime", "sim-conf", "combined", "combined-no-defer", "auto"} {
		var bad, miss, ffs int
		var tot, cd, or, tot0 float64
		for _, r := range rf.Rows {
			if r.Policy != p || r.Scenario != "S1-retain-all" || r.Book != "A_storage" {
				continue
			}
			bad += r.BadLineages
			miss += r.MissedLineages
			ffs += r.FalseFirstSight
			tot += float64(r.FinalPhys)
			cd += float64(cdc[key{r.Book, r.Fold, r.Scenario, r.Seed}].FinalPhys)
			if r.OracleBytes > 0 {
				or += float64(r.OracleBytes)
				tot0 += float64(r.FinalPhys)
			}
		}
		reg := math.NaN()
		if or > 0 {
			reg = 100 * (tot0/or - 1)
		}
		fmt.Printf("  %-18s %8d %8d %8d %10.3f %9.2f%%\n", p, bad, miss, ffs, tot/cd, reg)
		adv[p] = map[string]interface{}{"bad": bad, "missed": miss, "false_first_sight": ffs, "bytes_over_cdc": tot / cd, "regret_pct_vs_oracle": reg}
	}
	res["adverse"] = adv
	fh, err := os.Create("../z4/b7/summary.json")
	die(err)
	enc := json.NewEncoder(fh)
	enc.SetIndent("", " ")
	die(enc.Encode(res))
	die(fh.Close())
}

func bootstrap(x []float64) (float64, float64) {
	n := len(x)
	var ms []float64
	state := uint64(12345)
	for b := 0; b < 2000; b++ {
		var s float64
		for i := 0; i < n; i++ {
			state = state*6364136223846793005 + 1442695040888963407
			s += x[int(state>>33)%n]
		}
		ms = append(ms, s/float64(n))
	}
	sort.Float64s(ms)
	return ms[int(0.025*float64(len(ms)))], ms[int(0.975*float64(len(ms)))]
}
