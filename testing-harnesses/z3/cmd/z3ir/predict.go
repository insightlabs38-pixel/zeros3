package main

import (
	"bytes"
	"flag"
	"fmt"
	"runtime"
	"time"

	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

type predAgg struct {
	streams             int
	comp, plain         int64
	tokens, matches     int64
	pfRecon, pfN        int64 // preflate blob bytes over streams it accepted; and their tokens
	pfNS                int64
	pfTokens, pfComp    int64
	prof                [4]int64 // mono size per profile
	profWins            [4]int
	auto                int64
	tok                 map[string]int64 // policy -> bytes (auto profile)
	cost                [ir.NumCats]float64
	escapes, escMatches int64
	skelAuto            int64
	encDur, decDur      time.Duration
	allocEnc            uint64
	e2Mono              int64
	work                int64
}

func predictCmd(args []string) {
	fs := flag.NewFlagSet("predict", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	revs := fs.Int("revs", 2, "revisions per family to include (from r0)")
	skipPF := fs.Bool("no-preflate", false, "do not call the Preflate oracle")
	ablate := fs.Bool("ablate", false, "run the Z3-07A feature ablation instead of the profile tables")
	fs.Parse(args)
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	ins, order, cl, err := lab.FamilyInputs(*dir, split(*fams), lab.DefaultOptions())
	if err != nil {
		fatal(err)
	}
	defer cl.Close()
	if *ablate {
		ablation(ins, order, *revs)
		return
	}
	pols := []ir.Policy{{Kind: ir.PTokRun, Target: 16 << 10}, {Kind: ir.PTokRun, Target: 4 << 10}, {Kind: ir.PTokRun, Target: 2 << 10}, {Kind: ir.PBlock}}
	by := map[string]*predAgg{}
	for _, fam := range order {
		a := &predAgg{tok: map[string]int64{}}
		by[fam] = a
		for ri, in := range ins[fam] {
			if ri >= *revs {
				break
			}
			regs, err := in.StreamRegions()
			if err != nil {
				break
			}
			for _, rg := range regs {
				if !rg.Stream {
					continue
				}
				s, err := in.ParseStream(rg.Off, rg.Len)
				if err != nil {
					continue
				}
				st := s.Stats()
				if st.Tokens == 0 {
					continue
				}
				a.streams++
				a.comp += int64(st.CompressedBytes)
				a.plain += int64(st.PlainBytes)
				a.tokens += int64(st.Tokens)
				a.matches += int64(st.Matches)
				if !*skipPF {
					raw, _ := in.Src.Span(rg.Off, rg.Len)
					if rc, ns, ok := lab.PreflateRecon(raw); ok {
						a.pfNS += ns
						a.pfRecon += int64(rc)
						a.pfTokens += int64(st.Tokens)
						a.pfComp += int64(st.CompressedBytes)
					}
				}
				p2, _ := ir.Encode(s, ir.Config{Codec: ir.E2, Policy: ir.Policy{Kind: ir.PStream}})
				a.e2Mono += int64(p2.TotalUnitBytes() + len(p2.Skeleton))
				bestProf, bestSize := 0, 1<<62
				for pi := range ir.Profiles {
					pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PStream}, Profile: uint8(pi)})
					if err != nil {
						continue
					}
					n := int64(pk.TotalUnitBytes() + len(pk.Skeleton))
					a.prof[pi] += n
					if int(n) < bestSize {
						bestProf, bestSize = pi, int(n)
					}
				}
				a.profWins[bestProf]++
				// auto-profile, mono, with category costs and timing
				var m0, m1 runtime.MemStats
				runtime.ReadMemStats(&m0)
				t0 := time.Now()
				pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PStream}, Profile: ir.AutoProfile, Cost: &a.cost})
				a.encDur += time.Since(t0)
				runtime.ReadMemStats(&m1)
				a.allocEnc += m1.TotalAlloc - m0.TotalAlloc
				if err != nil {
					fatal(err)
				}
				a.auto += int64(pk.TotalUnitBytes() + len(pk.Skeleton))
				a.skelAuto += int64(len(pk.Skeleton))
				a.escapes += int64(pk.Pred.Escapes)
				a.escMatches += int64(pk.Pred.Matches)
				a.work += pk.Pred.Work
				t0 = time.Now()
				var out bytes.Buffer
				if err := ir.Reconstruct(&out, pk.Skeleton, func(i int) ([]byte, error) { return pk.Units[i], nil }, s.Plain, ir.DecodeOptions{}); err != nil {
					fatal(err)
				}
				a.decDur += time.Since(t0)
				raw, _ := in.Src.Span(rg.Off, rg.Len)
				if !bytes.Equal(out.Bytes(), raw) {
					fatal(fmt.Errorf("%s: predictive reconstruction differs", fam))
				}
				for _, pol := range pols {
					pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: pol, Profile: ir.AutoProfile})
					if err != nil {
						continue
					}
					a.tok[pol.String()] += int64(pk.TotalUnitBytes() + len(pk.Skeleton))
				}
			}
			in.DropParsed()
		}
	}
	bpt := func(b, tokens int64) string {
		if tokens == 0 {
			return "-"
		}
		return fmt.Sprintf("%.2f", float64(b)*8/float64(tokens))
	}
	fmt.Println("### Predictor correction cost: whole-stream predictor, % of compressed bytes (bits/token)")
	fmt.Println("| family | tokens/comp byte | Huffman bits/token | Preflate blob | E2 explicit | P0 generic | P1 zlib-like | P2 go-like | P3 zlib-fast-like | best-profile wins (0/1/2/3) | auto |")
	fmt.Println("|---|---:|---:|---|---:|---|---|---|---|---|---|")
	cell := func(b, comp, tokens int64) string { return fmt.Sprintf("%s (%s)", pct(b, comp), bpt(b, tokens)) }
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		pf := "n/a"
		if a.pfTokens > 0 {
			pf = cell(a.pfRecon, a.pfComp, a.pfTokens)
		}
		fmt.Printf("| %s | %.2f | %.2f | %s | %s | %s | %s | %s | %s | %d/%d/%d/%d | %s |\n", fam, float64(a.tokens)/float64(a.comp),
			float64(a.comp)*8/float64(a.tokens), pf, pct(a.e2Mono, a.comp),
			cell(a.prof[0], a.comp, a.tokens), cell(a.prof[1], a.comp, a.tokens), cell(a.prof[2], a.comp, a.tokens), cell(a.prof[3], a.comp, a.tokens),
			a.profWins[0], a.profWins[1], a.profWins[2], a.profWins[3], cell(a.auto, a.comp, a.tokens))
	}
	fmt.Println("\n### Predictor reset at unit boundaries (auto profile): % of compressed bytes, no cross-unit sharing")
	fmt.Println("| family | whole-stream | tok16K | tok4K | tok2K | block | reset penalty 16K / 4K / 2K (pp) |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		pp := func(k string) float64 { return 100 * float64(a.tok[k]-a.auto) / float64(a.comp) }
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %.1f / %.1f / %.1f |\n", fam, pct(a.auto, a.comp), pct(a.tok["tok16K"], a.comp), pct(a.tok["tok4K"], a.comp), pct(a.tok["tok2K"], a.comp), pct(a.tok["block"], a.comp), pp("tok16K"), pp("tok4K"), pp("tok2K"))
	}
	fmt.Println("\n### Where the corrections go (whole-stream, auto profile): bits per match; escape rate")
	fmt.Println("| family | is-match flags (/token) | candidate index | length | escapes (explicit) | escape rate % | bits/token |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.matches == 0 {
			continue
		}
		m, t := float64(a.matches), float64(a.tokens)
		fmt.Printf("| %s | %.3f | %.3f | %.3f | %.3f | %.2f | %.3f |\n", fam, a.cost[ir.CatPredHit]/t, a.cost[ir.CatPredDist]/m, a.cost[ir.CatPredLen]/m, a.cost[ir.CatPredEscape]/m,
			100*float64(a.escapes)/float64(max(a.escMatches, 1)), float64(a.auto)*8/t)
	}
	fmt.Println("\n### Speed (single thread; MiB/s of plaintext) and allocation")
	fmt.Println("| family | predict+encode | decode+replay+CRC | allocated per MiB plaintext | candidate work (compare bytes) per plaintext byte | Preflate analyze ms/MiB comp |")
	fmt.Println("|---|---:|---:|---:|---:|---|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		mbp := float64(a.plain) / (1 << 20)
		pf := "n/a"
		if a.pfComp > 0 {
			pf = fmt.Sprintf("%.0f (%.1f MiB/s)", float64(a.pfNS)/1e6/(float64(a.pfComp)/(1<<20)), float64(a.pfComp)/(1<<20)/(float64(a.pfNS)/1e9))
		}
		fmt.Printf("| %s | %.1f | %.1f | %.1f MiB | %.1f | %s |\n", fam, mbp/a.encDur.Seconds(), mbp/a.decDur.Seconds(), float64(a.allocEnc)/(1<<20)/mbp, float64(a.work)/float64(a.plain), pf)
	}
}

// ablation measures the whole-stream predictor with one feature disabled at a time.
func ablation(ins map[string][]*lab.Input, order []string, revs int) {
	type variant struct {
		name string
		set  func()
	}
	reset := func() {
		ir.Ablate.NoLazyHint, ir.Ablate.NoRunCtx, ir.Ablate.NoRepCtx, ir.Ablate.NoLenCtx, ir.Ablate.NoIdxCtx = false, false, false, false, false
	}
	variants := []variant{
		{"full model", func() {}},
		{"no lazy hint", func() { ir.Ablate.NoLazyHint = true }},
		{"no literal-run context", func() { ir.Ablate.NoRunCtx = true }},
		{"no repeat-distance context", func() { ir.Ablate.NoRepCtx = true }},
		{"no length contexts", func() { ir.Ablate.NoLenCtx = true }},
		{"no candidate contexts (rank only)", func() { ir.Ablate.NoIdxCtx = true; ir.Ablate.NoLenCtx = true; ir.Ablate.NoRepCtx = true }},
		{"no contexts at all", func() {
			ir.Ablate.NoIdxCtx, ir.Ablate.NoLenCtx, ir.Ablate.NoRepCtx, ir.Ablate.NoRunCtx, ir.Ablate.NoLazyHint = true, true, true, true, true
		}},
	}
	fmt.Println("### Predictor feature ablation: whole-stream, best fixed profile per stream, % of compressed bytes (bits/token)")
	hdr := "| variant |"
	sep := "|---|"
	for _, f := range order {
		hdr += " " + f + " |"
		sep += "---|"
	}
	fmt.Println(hdr)
	fmt.Println(sep)
	type stream struct {
		fam string
		s   interface{}
	}
	_ = stream{}
	for _, v := range variants {
		row := "| " + v.name + " |"
		for _, fam := range order {
			var comp, size, tokens int64
			for ri, in := range ins[fam] {
				if ri >= revs {
					break
				}
				regs, err := in.StreamRegions()
				if err != nil {
					break
				}
				for _, rg := range regs {
					if !rg.Stream {
						continue
					}
					s, err := in.ParseStream(rg.Off, rg.Len)
					if err != nil || s.Stats().Tokens == 0 {
						continue
					}
					reset()
					v.set()
					best := int64(1 << 62)
					for pi := range ir.Profiles {
						pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PStream}, Profile: uint8(pi)})
						if err == nil {
							best = min(best, int64(pk.TotalUnitBytes()+len(pk.Skeleton)))
						}
					}
					reset()
					comp += int64(s.Stats().CompressedBytes)
					tokens += int64(s.Stats().Tokens)
					size += best
				}
				in.DropParsed()
			}
			row += fmt.Sprintf(" %s (%s) |", pct(size, comp), fmt.Sprintf("%.2f", float64(size)*8/float64(max(tokens, 1))))
		}
		fmt.Println(row)
	}
}
