package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	pl "zeros3-z3/planner2"
)

// frontierReport tabulates the unified B1-B6 candidate frontier and validates the
// first-sight scaling proxy against stages where the standalone cost was measured.
func frontierReport(lins []pl.Lineage) {
	f, sl, err := pl.LoadSeriesFile(*series)
	die(err)
	fmt.Println("=== C. Candidate frontier (captured Z4 series). cum = sum of marginal bytes all-in / cdc; fs = first-sight / cdc first-sight")
	fmt.Printf("%-26s %-12s %-4s %6s %8s %8s %9s %9s %5s %7s %s\n", "history", "scheme", "dep", "stages", "cum/cdc", "fs/cdc", "analyzeMiB/s", "decodeMiB/s", "depth", "amp4k", "std/inc source")
	var rows []map[string]interface{}
	var errs []float64
	for _, l := range sl {
		var cdcCum, cdc0 float64
		for _, s := range l.H.Schemes {
			if s.Name == pl.CDC {
				for _, v := range s.Inc {
					cdcCum += float64(v)
				}
				cdc0 = float64(s.Inc[0])
			}
		}
		for _, s := range l.H.Schemes {
			var cum float64
			for _, v := range s.Inc {
				cum += float64(v)
			}
			fmt.Printf("%-26.26s %-12.12s %-4.4s %6d %8.3f %8.3f %9.1f %9.1f %5d %7.1f %s / %s\n", l.H.Name, s.Name, s.Dep, len(s.Inc), cum/cdcCum, float64(s.Inc[0])/cdc0, s.AnalysisMiBs, s.DecodeMiBs, s.DepthCap, s.Amp4K, s.StdSrc, s.IncSrc)
			rows = append(rows, map[string]interface{}{"history": l.H.Name, "scheme": s.Name, "dep": s.Dep, "stages": len(s.Inc), "cum_over_cdc": cum / cdcCum, "first_sight_over_cdc": float64(s.Inc[0]) / cdc0,
				"std_src": s.StdSrc, "inc_src": s.IncSrc, "analysis_mibs": s.AnalysisMiBs, "decode_mibs": s.DecodeMiBs, "depth_cap": s.DepthCap, "amp4k": s.Amp4K, "amp64k": s.Amp64K, "amp1m": s.Amp1M, "amp_src": s.AmpSrc})
			if strings.HasPrefix(s.StdSrc, "measured") && len(s.Inc) > 1 {
				for t := 1; t < len(s.Std); t++ {
					proxy := float64(s.Inc[0]) * float64(l.H.Stages[t].Logical) / float64(l.H.Stages[0].Logical)
					if s.Std[t] > l.H.Stages[t].Logical/100 {
						errs = append(errs, math.Abs(proxy-float64(s.Std[t]))/float64(s.Std[t]))
					}
				}
			}
		}
	}
	var sum, mx float64
	for _, e := range errs {
		sum += e
		mx = math.Max(mx, e)
	}
	fmt.Printf("\nfirst-sight scaling proxy vs %d measured standalone stages: mean |err| %.1f%%, max %.1f%%\n", len(errs), 100*sum/float64(len(errs)), 100*mx)
	fmt.Println("\nFuture opportunities (never admissible):")
	for _, o := range f.Opportunities {
		fmt.Printf("  %-24s ready=%v  %s\n", o.Name, o.Ready, o.Reason)
	}
	_, zl, err := pl.LoadFrontier(*frontier)
	die(err)
	fmt.Printf("\nLayer A: %d Z3-36 cases with real object-level closures\n", len(zl))
	out := map[string]interface{}{"series": rows, "proxy_mean_abs_err": sum / math.Max(1, float64(len(errs))), "proxy_max_err": mx, "proxy_n": len(errs), "opportunities": f.Opportunities}
	fh, err := os.Create("../z4/b7/frontier_table.json")
	die(err)
	enc := json.NewEncoder(fh)
	enc.SetIndent("", " ")
	die(enc.Encode(out))
	die(fh.Close())
}
