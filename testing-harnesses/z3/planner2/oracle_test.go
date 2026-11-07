package planner2

import "testing"

func loadSeriesT(t *testing.T) []*SeriesLineage {
	_, sl, err := LoadSeriesFile("../../z4/b7/series.json")
	if err != nil {
		t.Skipf("series unavailable: %v", err)
	}
	return sl
}

// The plan oracle (exhaustive) must equal brute-force over scheme sequences computed
// directly from the series arithmetic on a tiny all-in-or-nothing case.
func TestOracleMatchesBruteForce(t *testing.T) {
	for _, l := range loadSeriesT(t) {
		if l.Name() != "sst-lz4-rocksdb" {
			continue
		}
		w := ArrivalsOnly(l)
		cfg := DefaultConfig()
		obj := func(led *Ledger) float64 { return float64(led.FinalPhys) }
		res, err := Oracle(SubWorld(w, 0), cfg, obj, 100000)
		if err != nil || !res.Exact {
			t.Fatalf("oracle %v exact=%v", err, res.Exact)
		}
		// brute force: 3 schemes over 8 stages with nearest-earlier-in-scheme telescoping
		sch := l.H.Schemes
		n := len(l.H.Stages)
		best := int64(1 << 62)
		seq := make([]int, n)
		var rec func(i int)
		rec = func(i int) {
			if i == n {
				var total int64
				last := make([]int, len(sch))
				for k := range last {
					last[k] = -1
				}
				for stg := 0; stg < n; stg++ {
					s := sch[seq[stg]]
					var cost int64
					if last[seq[stg]] < 0 {
						cost = s.Std[stg]
					} else {
						var gap int64
						for j := last[seq[stg]] + 1; j <= stg; j++ {
							gap += s.Inc[j]
						}
						cost = gap
						if s.Std[stg] < gap {
							cost = s.Std[stg]
						}
					}
					total += cost
					last[seq[stg]] = stg
				}
				if total < best {
					best = total
				}
				return
			}
			for k := range sch {
				seq[i] = k
				rec(i + 1)
			}
		}
		rec(0)
		// nearest-3 base limit and CAS pinning do not change bytes here (retain-all)
		if res.FinalBytes != best {
			t.Fatalf("oracle %d != brute force %d", res.FinalBytes, best)
		}
		return
	}
	t.Skip("history missing")
}

// The oracle must agree with the independent Z3-36 branch-and-bound on object frontiers.
func TestOracleMatchesZ3BranchAndBound(t *testing.T) {
	corp, lins, err := LoadFrontier(frontierPath)
	if err != nil {
		t.Skip(err)
	}
	cfg := DefaultConfig()
	cfg.Overhead = corp.ObjectOverhead
	checked := 0
	for i, l := range lins {
		f := corp.Cases[i]
		if len(f.Stages) > 6 || len(f.Name) > 6 && (f.Name[:3] == "no-" || f.Name[:4] == "one-") {
			continue
		}
		ref, err := corp.Run(f, "greedy")
		if err != nil || !ref.OracleExact {
			continue
		}
		res, err := Oracle(SubWorld(ArrivalsOnly(l), 0), cfg, func(led *Ledger) float64 { return float64(led.FinalPhys) }, 200000)
		if err != nil || !res.Exact {
			continue
		}
		if res.FinalBytes != ref.Oracle {
			t.Fatalf("%s: planner2 oracle %d != Z3-36 B&B %d", f.Name, res.FinalBytes, ref.Oracle)
		}
		checked++
	}
	if checked < 3 {
		t.Fatalf("only %d cases cross-checked", checked)
	}
	t.Logf("cross-checked %d Z3-36 cases against its exact B&B oracle", checked)
}
