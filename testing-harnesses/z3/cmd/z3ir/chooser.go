package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/lab"
)

// revCost is one (family, representation, revision) physical cost in packed bytes.
type revCost struct {
	cost    int64
	logical int64
	ok      bool
	analyze float64 // ms
	replay  float64 // ms
	plain   int64
}

func gather(reports []*lab.Report, refBytes int64) (map[string]map[string][]revCost, []string) {
	out := map[string]map[string][]revCost{}
	var fams []string
	seen := map[string]bool{}
	for _, rep := range reports {
		for _, r := range rep.Rows {
			if !seen[r.Family] {
				seen[r.Family] = true
				fams = append(fams, r.Family)
			}
			m := out[r.Family]
			if m == nil {
				m = map[string][]revCost{}
				out[r.Family] = m
			}
			for len(m[r.Rep]) <= r.Index {
				m[r.Rep] = append(m[r.Rep], revCost{})
			}
			if r.Status != "ok" || r.CostNA {
				continue
			}
			inc := r.Incremental
			if r.IncrementalPacked != nil {
				inc = *r.IncrementalPacked
			}
			inc += int64(r.Components) * (refBytes - rep.RefBytes)
			rc := revCost{cost: inc, logical: r.LogicalSize, ok: r.RoundtripExact == "yes" || r.RoundtripExact == "N/A"}
			if r.Detail != nil {
				rc.analyze = detFloat(r.Detail, "analyze_ms")
				rc.replay = detFloat(r.Detail, "replay_ms")
				rc.plain = detInt(r.Detail, "plain_bytes")
			}
			m[r.Rep][r.Index] = rc
		}
	}
	return out, fams
}

func chooserCmd(args []string) {
	fs := flag.NewFlagSet("chooser", flag.ExitOnError)
	from := fs.String("from", "", "comma-separated lab reports (z3ir family -json)")
	cdcRep := fs.String("cdc", "cdc", "raw CDC representation")
	expRep := fs.String("explicit", "ir-tok4k-e2", "block-local explicit IR representation")
	predRep := fs.String("predictive", "irp-tok4k", "block-local predictive IR representation")
	pfRep := fs.String("preflate", "preflate-rcdc", "Preflate reference representation")
	lowRep := fs.String("lower-bound", "oracle-logical", "plaintext-only lower bound representation")
	refB := fs.Int64("ref-bytes", 35, "recipe bytes per component reference")
	thr := fs.Float64("commit-threshold", 0.8, "commit a family to a transform if its first update costs less than this fraction of CDC")
	fs.Parse(args)
	var reports []*lab.Report
	for _, p := range split(*from) {
		r, err := lab.ReadJSON(p)
		if err != nil {
			fatal(err)
		}
		reports = append(reports, r)
	}
	if len(reports) == 0 {
		fatal(fmt.Errorf("-from is required"))
	}
	data, fams := gather(reports, *refB)
	fmt.Printf("ref bytes per component reference: %d. Representations: raw CDC `%s`, explicit `%s`, predictive `%s`; Preflate `%s`; plaintext lower bound `%s`.\n", *refB, *cdcRep, *expRep, *predRep, *pfRep, *lowRep)
	fmt.Println("Percentages are of the update revisions' compressed bytes (r1..rN), physical bytes with the production pack rule.")
	fmt.Println()
	fmt.Println("| family | CDC | Preflate (rcdc) | explicit IR | predictive IR | plaintext lower bound | per-revision best of {CDC, explicit, predictive} | wins CDC/expl/pred | commit-after-r1 (incl. r0 entry) | all-CDC (incl. r0) |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---|---:|---:|")
	type tot struct{ cdc, pf, exp, pred, low, best, logical int64 }
	var all tot
	wins := map[string]int{}
	var commitSum, cdcCum, logicalCum int64
	commits := 0
	sort.Strings(fams)
	for _, fam := range fams {
		m := data[fam]
		c, e, p := m[*cdcRep], m[*expRep], m[*predRep]
		if len(c) < 2 || len(e) < 2 || len(p) < 2 {
			continue
		}
		var t tot
		w := map[string]int{}
		okAll := true
		for k := 1; k < len(c); k++ {
			if !c[k].ok || !e[k].ok || !p[k].ok || c[k].logical == 0 {
				okAll = false
				continue
			}
			t.logical += c[k].logical
			t.cdc += c[k].cost
			t.exp += e[k].cost
			t.pred += p[k].cost
			if pf := m[*pfRep]; len(pf) > k {
				t.pf += pf[k].cost
			}
			if lb := m[*lowRep]; len(lb) > k {
				t.low += lb[k].cost
			}
			best, name := c[k].cost, "cdc"
			if e[k].cost < best {
				best, name = e[k].cost, "explicit"
			}
			if p[k].cost < best {
				best, name = p[k].cost, "predictive"
			}
			t.best += best
			w[name]++
			wins[name]++
		}
		if !okAll {
			fmt.Printf("| %s | (rows not exact/ok) |||||||||\n", fam)
			continue
		}
		// commit-after-r1 policy: decide from the first update revision only
		x, xe, name := c, c[0].cost, "cdc"
		if p[1].cost <= e[1].cost && float64(p[1].cost) < *thr*float64(c[1].cost) {
			x, xe, name = p, p[0].cost, "predictive"
		} else if e[1].cost < p[1].cost && float64(e[1].cost) < *thr*float64(c[1].cost) {
			x, xe, name = e, e[0].cost, "explicit"
		}
		commit := xe
		for k := 1; k < len(x); k++ {
			commit += x[k].cost
		}
		if name != "cdc" {
			commits++
		}
		cdcAll := c[0].cost
		logAll := c[0].logical
		for k := 1; k < len(c); k++ {
			cdcAll += c[k].cost
			logAll += c[k].logical
		}
		commitSum += commit
		cdcCum += cdcAll
		logicalCum += logAll
		all.cdc += t.cdc
		all.pf += t.pf
		all.exp += t.exp
		all.pred += t.pred
		all.low += t.low
		all.best += t.best
		all.logical += t.logical
		lowS := "n/a"
		if t.low > 0 {
			lowS = pct(t.low, t.logical)
		}
		pfS := "n/a"
		if t.pf > 0 {
			pfS = pct(t.pf, t.logical)
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %d/%d/%d | %s (%s) | %s |\n", fam, pct(t.cdc, t.logical), pfS, pct(t.exp, t.logical), pct(t.pred, t.logical), lowS,
			pct(t.best, t.logical), w["cdc"], w["explicit"], w["predictive"], pct(commit, logAll), name, pct(cdcAll, logAll))
	}
	n := wins["cdc"] + wins["explicit"] + wins["predictive"]
	fmt.Printf("| **all families (byte-weighted)** | %s | %s | %s | %s | %s | %s | %d/%d/%d | %s (%d families transformed) | %s |\n", pct(all.cdc, all.logical), pct(all.pf, all.logical), pct(all.exp, all.logical), pct(all.pred, all.logical),
		pct(all.low, all.logical), pct(all.best, all.logical), wins["cdc"], wins["explicit"], wins["predictive"], pct(commitSum, logicalCum), commits, pct(cdcCum, logicalCum))
	if n > 0 {
		fmt.Printf("\nWin share over %d family-revisions: CDC %.0f%%, explicit %.0f%%, predictive %.0f%%.\n", n, 100*float64(wins["cdc"])/float64(n), 100*float64(wins["explicit"])/float64(n), 100*float64(wins["predictive"])/float64(n))
	}
	// CPU
	fmt.Println("\n| family | CDC | analyze ms/MiB comp (explicit / predictive) | replay MiB/s plaintext (explicit / predictive) |")
	fmt.Println("|---|---|---|---|")
	for _, fam := range fams {
		m := data[fam]
		e, p := m[*expRep], m[*predRep]
		if len(e) < 2 || len(p) < 2 {
			continue
		}
		var ea, pa, er, pr float64
		var ep, pp, ec, pc int64
		for k := 1; k < len(e); k++ {
			ea += e[k].analyze
			er += e[k].replay
			ep += e[k].plain
			ec += e[k].logical
			if k < len(p) {
				pa += p[k].analyze
				pr += p[k].replay
				pp += p[k].plain
				pc += p[k].logical
			}
		}
		if ec == 0 || pc == 0 || er == 0 || pr == 0 {
			continue
		}
		fmt.Printf("| %s | free | %.0f / %.0f | %.0f / %.0f |\n", fam, ea/(float64(ec)/(1<<20)), pa/(float64(pc)/(1<<20)),
			float64(ep)/(1<<20)/(er/1000), float64(pp)/(1<<20)/(pr/1000))
	}
	_ = strings.Join
}
