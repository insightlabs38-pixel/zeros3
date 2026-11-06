package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"zeros3-z3/mdelta2"
)

// cmdExplore: explore TARGET BASE [BASE2]  -> per-predictor totals (DEFLATE-of-planes cost and ideal ctx bound).
func cmdExplore(args []string) {
	t, err := mdelta2.Open(args[0])
	must(err)
	var bs []*mdelta2.Model
	for _, p := range args[1:] {
		if _, err := strconv.Atoi(p); err == nil {
			continue
		}
		b, err := mdelta2.Open(p)
		must(err)
		bs = append(bs, b)
	}
	rankMax := 16
	type row struct {
		name string
		z, h int64
	}
	var mu sync.Mutex
	tot := map[string]*[3]float64{}
	nrcTot := map[string]float64{}
	best := 0.0
	rawTot := 0.0
	add := func(name string, z int, h float64, par int) {
		mu.Lock()
		defer mu.Unlock()
		r := tot[name]
		if r == nil {
			r = new([3]float64)
			tot[name] = r
		}
		r[0] += float64(z + par)
		r[1] += h + float64(par)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	t0 := time.Now()
	for wk := 0; wk < 4; wk++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ti := range jobs {
				tt := &t.Tensors[ti]
				if !tt.DType.IsFloat() {
					continue
				}
				w := tt.DType.Width()
				tdata, _ := t.Read(ti)
				if tt.DType == mdelta2.DF32 {
					if nd, ok := mdelta2.Narrow(tdata, mdelta2.DF16); ok {
						tdata = nd
						ttc := *tt
						ttc.DType = mdelta2.DF16
						tt = &ttc
						w = 2
					}
				}
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
				n := int(tt.Elems())
				rawz := mdelta2.PlaneDeflate(tdata, w)
				mu.Lock()
				rawTot += float64(len(tdata))
				mu.Unlock()
				cands := map[string][4]float64{} // name -> deflate, h, params, nrc
				cands["direct+planes"] = [4]float64{float64(rawz), mdelta2.HCtx(mdelta2.Planes(tdata, w)), 0, float64(rawz)}
				if ok && len(bases) > 0 {
					res := func(name string, p *mdelta2.Params) {
						pred, err := mdelta2.Predict(p, tt.DType, tt.Shape, bases)
						if err != nil {
							return
						}
						r := mdelta2.MakeResidual(p.Res, w, tdata, pred)
						cands[name] = [4]float64{float64(mdelta2.PlaneDeflate(r, w)), mdelta2.HCtx(mdelta2.Planes(r, w)), float64(len(p.Marshal())), float64(len(mdelta2.NRCEncode(tt.DType, r, pred)))}
					}
					res("id-key", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RKey, NB: 1, Prec: 4})
					res("id-xor", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RXor, NB: 1, Prec: 4})
					res("id-sub", &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RSub, NB: 1, Prec: 4})
					res("aff-tensor", mdelta2.FitAffine(tt.DType, tt.Shape, mdelta2.GTensor, 0, 0, 4, bases[0], tdata))
					res("aff-row", mdelta2.FitAffine(tt.DType, tt.Shape, mdelta2.GRow, 0, 0, 4, bases[0], tdata))
					res("aff-blk4096", mdelta2.FitAffine(tt.DType, tt.Shape, mdelta2.GBlock, 4096, 0, 4, bases[0], tdata))
					res("aff-blk256", mdelta2.FitAffine(tt.DType, tt.Shape, mdelta2.GBlock, 256, 0, 4, bases[0], tdata))
					if len(bases) > 1 {
						res("lin2-tensor", mdelta2.FitLin(tt.DType, tt.Shape, mdelta2.GTensor, 4, bases[:2], tdata))
						res("lin2-row", mdelta2.FitLin(tt.DType, tt.Shape, mdelta2.GRow, 4, bases[:2], tdata))
					}
					if len(tt.Shape) == 2 && tt.Shape[0] >= 32 && tt.Shape[1] >= 32 && n <= 24<<20 {
						for _, r := range []int{1, 4, rankMax} {
							res("low"+strconv.Itoa(r), mdelta2.LowRankFit(tt.DType, tt.Shape, r, 4, bases[0], tdata, 2, 1))
						}
					}
				}
				bz := 1e18
				mu.Lock()
				for nme, c := range cands {
					add2 := func() {}
					_ = add2
					r := tot[nme]
					if r == nil {
						r = new([3]float64)
						tot[nme] = r
					}
					r[0] += c[0] + c[2]
					r[1] += c[1] + c[2]
					nrcTot[nme] += math.Min(c[3], c[0]) + c[2]
					if c[0]+c[2] < bz {
						bz = c[0] + c[2]
					}
				}
				// tensors with no applicable candidate use direct for every predictor row
				for nme := range tot {
					if _, has := cands[nme]; !has {
						tot[nme][0] += cands["direct+planes"][0]
						tot[nme][1] += cands["direct+planes"][1]
						nrcTot[nme] += cands["direct+planes"][0]
					}
				}
				best += bz
				mu.Unlock()
			}
		}()
	}
	for i := range t.Tensors {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	_ = add
	names := make([]string, 0, len(tot))
	for k := range tot {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stdout, "target %s (%d bytes of float tensors), %.1fs\n", args[0], int64(rawTot), time.Since(t0).Seconds())
	for _, k := range names {
		fmt.Printf("  %-14s deflate %11.0f (%.3f)  idealCtx %11.0f (%.3f)  nrc %11.0f (%.3f)\n", k, tot[k][0], tot[k][0]/rawTot, tot[k][1], tot[k][1]/rawTot, nrcTot[k], nrcTot[k]/rawTot)
	}
	fmt.Printf("  %-14s deflate %12.0f (%.3f)\n", "BEST-PER-TENSOR", best, best/rawTot)
}
