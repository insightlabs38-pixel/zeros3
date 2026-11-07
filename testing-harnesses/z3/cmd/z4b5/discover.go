package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"time"

	"zeros3-z3/mdelta2"
)

type discAgg struct {
	Method      string  `json:"method"`
	Top1Bytes   float64 `json:"top1_bytes"`
	Top4Bytes   float64 `json:"top4_bytes"`
	Recall1     float64 `json:"recall_at_1"`
	Recall4     float64 `json:"recall_at_4"`
	HarmfulTop1 int     `json:"harmful_top1"`
	NoCand      int     `json:"no_candidate"`
	RankMS      int64   `json:"rank_ms"`
}

var layerRe = regexp.MustCompile(`(layers?\.|h\.)(\d+)\.`)

// renameLayers rotates layer indexes by one (names now lie about which layer a tensor belongs to).
func renameLayers(name string, nLayers int) string {
	return layerRe.ReplaceAllStringFunc(name, func(m string) string {
		sm := layerRe.FindStringSubmatch(m)
		i, _ := strconv.Atoi(sm[2])
		return sm[1] + strconv.Itoa((i+1)%nLayers) + "."
	})
}

// cmdDiscover: discover [-rename N] TARGET POOL...   (evaluates content-based base discovery)
func cmdDiscover(args []string) {
	fl := flag.NewFlagSet("discover", flag.ExitOnError)
	rename := fl.Int("rename", 0, "rotate layer numbers of the TARGET names modulo N (misleading-name control)")
	rowperm := fl.Bool("rowperm", false, "reverse the row order of every TARGET tensor (layout-change control); also reports the cost with perfect row alignment")
	minElems := fl.Int("min", 4096, "ignore tensors smaller than this")
	jsonOut := fl.String("json", "", "write aggregates here")
	fl.Parse(args)
	paths := fl.Args()
	load := func(mi int, path string, rn int) []*mdelta2.PT {
		m, err := mdelta2.Open(path)
		must(err)
		defer m.Close()
		var out []*mdelta2.PT
		for i := range m.Tensors {
			t := m.Tensors[i]
			if !t.DType.IsFloat() || int(t.Elems()) < *minElems {
				continue
			}
			d, err := m.Read(i)
			must(err)
			if rn > 0 {
				t.Name = renameLayers(t.Name, rn)
			}
			if mi < 0 && *rowperm && len(t.Shape) == 2 {
				d = reverseRows(d, int(t.Shape[0]), int(t.Shape[1])*t.DType.Width())
			}
			out = append(out, mdelta2.NewPT(mi, i, &t, d))
		}
		return out
	}
	tg := load(-1, paths[0], *rename)
	var pool []*mdelta2.PT
	for mi, p := range paths[1:] {
		pool = append(pool, load(mi, p, 0)...)
	}
	// ground truth per target tensor
	type gt struct {
		direct float64
		cost   []float64 // per pool index (Inf = unusable)
		best   float64
	}
	gts := make([]gt, len(tg))
	var oracle, direct float64
	for ti, t := range tg {
		g := gt{direct: mdelta2.DirectCost(t), cost: make([]float64, len(pool)), best: math.Inf(1)}
		for j, p := range pool {
			g.cost[j] = mdelta2.PairCost(t, p)
			if g.cost[j] < g.best {
				g.best = g.cost[j]
			}
		}
		g.best = math.Min(g.best, g.direct)
		gts[ti] = g
		oracle += g.best
		direct += g.direct
	}
	if *rowperm {
		var al float64
		for ti, t := range tg {
			if len(t.Shape) != 2 {
				al += gts[ti].best
				continue
			}
			u := *t
			u.Data = reverseRows(t.Data, int(t.Shape[0]), int(t.Shape[1])*t.DT.Width())
			b := gts[ti].direct
			for _, p := range pool {
				if c := mdelta2.PairCost(&u, p); c < b {
					b = c
				}
			}
			al += b
		}
		fmt.Printf("row-permuted target: best exact cost with identity prediction %.0f; bound if the row permutation were known (B3-style gather) %.0f; direct %.0f\n", oracle, al, direct)
	}
	var aggs []discAgg
	for _, method := range mdelta2.DiscMethods {
		a := discAgg{Method: method}
		t0 := time.Now()
		var ranks [][]int
		for _, t := range tg {
			ranks = append(ranks, mdelta2.Rank(method, t, pool))
		}
		a.RankMS = time.Since(t0).Milliseconds()
		var n1, n4 int
		for ti := range tg {
			g := gts[ti]
			r := ranks[ti]
			c1, c4 := g.direct, g.direct
			if len(r) == 0 {
				a.NoCand++
			}
			for k, j := range r {
				if k >= 4 {
					break
				}
				c := g.cost[j]
				if k == 0 && c >= g.direct && !math.IsInf(c, 1) {
					a.HarmfulTop1++
				}
				if k == 0 && c < c1 {
					c1 = c
				}
				if c < c4 {
					c4 = c
				}
			}
			a.Top1Bytes += c1
			a.Top4Bytes += c4
			if c1 <= g.best*1.02+1 {
				n1++
			}
			if c4 <= g.best*1.02+1 {
				n4++
			}
		}
		a.Recall1, a.Recall4 = float64(n1)/float64(len(tg)), float64(n4)/float64(len(tg))
		aggs = append(aggs, a)
	}
	fmt.Printf("target %s (%d tensors) pool %d tensors from %d models; direct %.0f oracle %.0f (%.3f of direct)\n", paths[0], len(tg), len(pool), len(paths)-1, direct, oracle, oracle/direct)
	for _, a := range aggs {
		fmt.Printf("  %-14s top1 %12.0f (%.3f)  best-of-4 %12.0f (%.3f)  recall@1 %.2f @4 %.2f  harmful-top1 %3d  no-cand %3d  rank %dms\n", a.Method, a.Top1Bytes, a.Top1Bytes/direct, a.Top4Bytes, a.Top4Bytes/direct, a.Recall1, a.Recall4, a.HarmfulTop1, a.NoCand, a.RankMS)
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"target": paths[0], "pool": paths[1:], "rename": *rename, "direct": direct, "oracle": oracle, "methods": aggs}, "", " ")
		os.WriteFile(*jsonOut, b, 0o644)
	}
}

func reverseRows(d []byte, rows, rowBytes int) []byte {
	out := make([]byte, len(d))
	for r := 0; r < rows; r++ {
		copy(out[(rows-1-r)*rowBytes:], d[r*rowBytes:(r+1)*rowBytes])
	}
	return out
}
