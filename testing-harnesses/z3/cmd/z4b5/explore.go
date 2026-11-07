package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zeros3-z3/mdelta2"
)

// cmdExplore evaluates the whole predictor ladder per tensor: explore [-json f] [-low 1,2,4,8,16,32] TARGET BASE [BASE2 [BASE3]]
// Costs are NRC-proxy bytes for ranking and real coded bytes (best of flate/NRC) for the per-tensor winner.
func cmdExplore(args []string) {
	fl := flag.NewFlagSet("explore", flag.ExitOnError)
	jsonOut := fl.String("json", "", "write per-tensor results")
	lowS := fl.String("low", "1,2,4,8,16,32", "low-rank ladder")
	maxLow := fl.Int("maxlow", 24<<20, "max elements for low-rank fitting")
	fl.Parse(args)
	paths := fl.Args()
	t, err := mdelta2.Open(paths[0])
	must(err)
	var bs []*mdelta2.Model
	for _, p := range paths[1:] {
		b, err := mdelta2.Open(p)
		must(err)
		bs = append(bs, b)
	}
	ranks := parseInts(*lowS)
	type tres struct {
		Name    string             `json:"name"`
		DType   string             `json:"dtype"`
		Elems   int                `json:"elems"`
		Raw     int                `json:"raw_bytes"`
		Direct  int                `json:"direct"`
		Proxy   map[string]float64 `json:"proxy"`
		Best    string             `json:"best"`
		BestEnc int                `json:"best_coded"`
	}
	results := make([]tres, len(t.Tensors))
	jobs := make(chan int)
	var wg sync.WaitGroup
	t0 := time.Now()
	for wk := 0; wk < 4; wk++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ti := range jobs {
				tt := t.Tensors[ti]
				if !tt.DType.IsFloat() {
					continue
				}
				tdata, _ := t.Read(ti)
				cd := tt.DType
				if cd == mdelta2.DF32 {
					if nd, ok := mdelta2.Narrow(tdata, mdelta2.DF16); ok {
						tdata, cd = nd, mdelta2.DF16
					}
				}
				w := cd.Width()
				r := tres{Name: tt.Name, DType: cd.String(), Elems: int(tt.Elems()), Raw: len(tdata), Proxy: map[string]float64{}}
				zero := make([]byte, len(tdata))
				dsz := 1 << 60
				for _, c := range []int{mdelta2.CFlate, mdelta2.CNRC, mdelta2.CFPE} {
					if s, _ := mdelta2.EncodeResidual(c, cd, mdelta2.RXor, tdata, zero); s != nil && len(s) < dsz {
						dsz = len(s)
					}
				}
				r.Direct = dsz
				var bases []mdelta2.Base
				ok := true
				for _, b := range bs {
					bi, has := b.Index(tt.Name)
					if !has || b.Tensors[bi].Elems() != tt.Elems() || !b.Tensors[bi].DType.IsFloat() {
						ok = false
						break
					}
					d, _ := b.Read(bi)
					bases = append(bases, mdelta2.Base{DT: b.Tensors[bi].DType, Data: d})
				}
				type cand struct {
					name string
					p    *mdelta2.Params
				}
				var cs []cand
				if ok && len(bases) > 0 {
					cs = append(cs, cand{"id-key", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RKey, NB: 1, Prec: 4}},
						cand{"id-xor", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RXor, NB: 1, Prec: 4}},
						cand{"id-sub", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RSub, NB: 1, Prec: 4}},
						cand{"aff-tensor-ls", mdelta2.FitAffine(cd, tt.Shape, mdelta2.GTensor, 0, 0, 4, bases[0], tdata)},
						cand{"aff-tensor", mdelta2.FitAffineRobust(cd, tt.Shape, mdelta2.GTensor, 0, 4, bases[0], tdata)},
						cand{"aff-row", mdelta2.FitAffineRobust(cd, tt.Shape, mdelta2.GRow, 0, 4, bases[0], tdata)},
						cand{"aff-blk4096", mdelta2.FitAffineRobust(cd, tt.Shape, mdelta2.GBlock, 4096, 4, bases[0], tdata)},
						cand{"aff-blk256", mdelta2.FitAffineRobust(cd, tt.Shape, mdelta2.GBlock, 256, 4, bases[0], tdata)})
					if len(tt.Shape) == 2 && tt.Shape[0] >= 16 && tt.Shape[1] >= 16 && int(tt.Elems()) <= *maxLow {
						for _, rk := range ranks {
							cs = append(cs, cand{"low" + strconv.Itoa(rk), mdelta2.LowRankFit(cd, tt.Shape, rk, 4, bases[0], tdata, 3, 1)})
						}
					}
					if len(bases) >= 2 {
						cs = append(cs, cand{"lin2", mdelta2.FitLin(cd, tt.Shape, mdelta2.GTensor, 4, bases[:2], tdata)},
							cand{"lin2-row", mdelta2.FitLin(cd, tt.Shape, mdelta2.GRow, 4, bases[:2], tdata)})
					}
					if len(bases) >= 3 {
						cs = append(cs, cand{"lin3", mdelta2.FitLin(cd, tt.Shape, mdelta2.GTensor, 4, bases[:3], tdata)})
					}
				}
				best, bp := "direct", float64(r.Direct)
				var bestC *cand
				for i := range cs {
					c := &cs[i]
					pred, err := mdelta2.Predict(c.p, cd, tt.Shape, bases)
					if err != nil {
						continue
					}
					res := mdelta2.MakeResidual(c.p.Res, w, tdata, pred)
					px := mdelta2.NRCProxy(cd, res, pred) + float64(len(c.p.Marshal()))
					r.Proxy[c.name] = px
					if px < bp {
						best, bp, bestC = c.name, px, c
					}
				}
				r.Best, r.BestEnc = best, r.Direct
				if bestC != nil {
					pred, _ := mdelta2.Predict(bestC.p, cd, tt.Shape, bases)
					res := mdelta2.MakeResidual(bestC.p.Res, w, tdata, pred)
					e := 1 << 60
					for _, c := range []int{mdelta2.CFlate, mdelta2.CNRC} {
						if s, ok := mdelta2.EncodeResidual(c, cd, bestC.p.Res, res, pred); ok && len(s) < e {
							e = len(s)
						}
					}
					if e+len(bestC.p.Marshal()) < r.Direct {
						r.BestEnc = e + len(bestC.p.Marshal())
					} else {
						r.Best = "direct"
					}
				}
				results[ti] = r
			}
		}()
	}
	for i := range t.Tensors {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	// aggregate
	tot := map[string]float64{}
	wins := map[string]int{}
	var raw, direct, best float64
	var rows []tres
	allKeys := map[string]bool{}
	for _, r := range results {
		for k := range r.Proxy {
			allKeys[k] = true
		}
	}
	for _, r := range results {
		if r.Elems == 0 {
			continue
		}
		rows = append(rows, r)
		raw += float64(r.Raw)
		direct += float64(r.Direct)
		best += float64(r.BestEnc)
		wins[r.Best]++
		for k := range allKeys {
			if v, ok := r.Proxy[k]; ok {
				tot[k] += math.Min(v, float64(r.Direct))
			} else {
				tot[k] += float64(r.Direct)
			}
		}
	}
	names := make([]string, 0, len(tot))
	for k := range tot {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Printf("target %s bases %d float bytes %.0f  (%.1fs)\n  direct %.0f (%.3f)\n", paths[0], len(bs), raw, time.Since(t0).Seconds(), direct, direct/raw)
	for _, k := range names {
		fmt.Printf("  %-14s proxy-if-accepted %12.0f (%.3f)\n", k, tot[k], tot[k]/raw)
	}
	var ws []string
	for k, v := range wins {
		ws = append(ws, fmt.Sprintf("%s:%d", k, v))
	}
	sort.Strings(ws)
	fmt.Printf("  BEST-PER-TENSOR coded %.0f (%.3f of raw, %.3f of direct); winners %s\n", best, best/raw, best/direct, strings.Join(ws, " "))
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"args": paths, "raw": raw, "direct": direct, "best": best, "tensors": rows}, "", " ")
		os.WriteFile(*jsonOut, b, 0o644)
	}
}
