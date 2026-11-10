package main

import (
	"strings"
	"time"
	"zeros3-z3/dcore"
)

// cmdSelect measures staged candidate selection against the exhaustive oracle
// on real families. For each target version the candidates are every earlier
// version plus (as negative controls) the other families' first files.
func cmdSelect(root, only string) {
	fams := families(root)
	var negatives [][]byte
	for _, f := range fams {
		if len(f.Files) > 0 {
			negatives = append(negatives, read(f.Files[0], 4<<20))
		}
	}
	for _, f := range fams {
		if only != "" && !strings.Contains(","+only+",", ","+f.Name+",") {
			continue
		}
		var vers [][]byte
		for _, p := range f.Files {
			vers = append(vers, read(p, 64<<20))
		}
		for ti := 1; ti < len(vers); ti++ {
			var cands []dcore.Candidate
			for j := 0; j < ti; j++ {
				cands = append(cands, dcore.Candidate{ID: j, Bases: [][]byte{vers[j]}})
			}
			for j, n := range negatives {
				cands = append(cands, dcore.Candidate{ID: 1000 + j, Bases: [][]byte{n}})
			}
			// pairs of the two newest versions as a two-base candidate
			if ti >= 2 {
				cands = append(cands, dcore.Candidate{ID: 2000, Bases: [][]byte{vers[ti-1], vers[ti-2]}})
			}
			type cfg struct {
				name string
				o    dcore.SelectOpts
			}
			base := dcore.SelectOpts{Params: dcore.DefaultParams(), FrameSpan: 128 << 10, MinSaving: 0}
			ex := base
			ex.Exhaustive = true
			st := base
			st.Slack = 0.05
			st2 := base
			st2.Slack = 0.0
			st2.Finalists = 1
			en := st
			en.Est.Entropy = true
			var oracle int64
			var oracleBest int
			for _, c := range []cfg{{"exhaustive", ex}, {"staged-s5", st}, {"staged-top1", st2}, {"staged-entropy-s5", en}} {
				t0 := time.Now()
				sel, err := dcore.Select(vers[ti], cands, c.o)
				must(err)
				secs := time.Since(t0).Seconds()
				if c.name == "exhaustive" {
					oracle, oracleBest = sel.Size, sel.Best
				}
				regret := 0.0
				if oracle > 0 && sel.Size > 0 {
					regret = float64(sel.Size)/float64(oracle) - 1
				}
				emit(map[string]any{"mode": "select", "family": f.Name, "target": ti, "policy": c.name, "sec": secs, "candidates": len(cands),
					"matched": sel.Matched, "exact_encoded": sel.ExactEncoded, "rejected_early": sel.RejectedEarly, "best": sel.Best,
					"oracle_best": oracleBest, "size": sel.Size, "oracle_size": oracle, "regret": regret, "direct": sel.Direct,
					"target_bytes": len(vers[ti]), "wrong_choice": sel.Best != oracleBest})
			}
		}
	}
}
