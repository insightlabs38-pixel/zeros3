package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"sync"

	"zeros3-z3/mdelta2"
)

// cmdMatrix estimates pairwise single-base costs c[i][j] (state j predicted from state i, best per tensor
// of {direct, identity-key, copy}) and derives ingestion-order policies and the retrospective
// optimum (minimum-cost arborescence). usage: matrix [-json f] STATE...
func cmdMatrix(args []string) {
	fl := flag.NewFlagSet("matrix", flag.ExitOnError)
	jsonOut := fl.String("json", "", "output")
	perms := fl.Int("perms", 300, "random insertion orders")
	fl.Parse(args)
	paths := fl.Args()
	n := len(paths)
	ms := make([]*mdelta2.Model, n)
	for i, p := range paths {
		m, err := mdelta2.Open(p)
		must(err)
		ms[i] = m
	}
	c := make([][]float64, n)
	for i := range c {
		c[i] = make([]float64, n)
	}
	direct := make([]float64, n)
	var mu sync.Mutex
	for j := 0; j < n; j++ {
		tj := ms[j]
		var wg sync.WaitGroup
		jobs := make(chan int)
		for wk := 0; wk < 4; wk++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ti := range jobs {
					t := tj.Tensors[ti]
					td, _ := tj.Read(ti)
					if !t.DType.IsFloat() {
						mu.Lock()
						direct[j] += float64(mdelta2.DeflateLen(td))
						for i := range ms {
							c[i][j] += float64(mdelta2.DeflateLen(td))
						}
						mu.Unlock()
						continue
					}
					cd := t.DType
					if cd == mdelta2.DF32 {
						if nd, ok := mdelta2.Narrow(td, mdelta2.DF16); ok {
							td, cd = nd, mdelta2.DF16
						}
					}
					dc := float64(mdelta2.PlaneDeflate(td, cd.Width()))
					if f, ok := mdelta2.EncodeResidual(mdelta2.CFPE, cd, mdelta2.RXor, td, make([]byte, len(td))); ok && float64(len(f)) < dc {
						dc = float64(len(f))
					}
					costs := make([]float64, n)
					for i := 0; i < n; i++ {
						if i == j {
							continue
						}
						bi, ok := ms[i].Index(t.Name)
						if !ok || ms[i].Tensors[bi].Elems() != t.Elems() || !ms[i].Tensors[bi].DType.IsFloat() {
							costs[i] = dc
							continue
						}
						bd, _ := ms[i].Read(bi)
						if ms[i].Tensors[bi].DType == t.DType && bytes.Equal(bd, func() []byte { o, _ := tj.Read(ti); return o }()) {
							costs[i] = 14
							continue
						}
						base := mdelta2.Base{DT: ms[i].Tensors[bi].DType, Data: bd}
						pr := &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RKey, NB: 1, Prec: 4}
						pred, _ := mdelta2.Predict(pr, cd, t.Shape, []mdelta2.Base{base})
						r := mdelta2.MakeResidual(mdelta2.RKey, cd.Width(), td, pred)
						costs[i] = math.Min(dc, mdelta2.NRCProxy(cd, r, pred)+8)
					}
					mu.Lock()
					direct[j] += dc
					for i := 0; i < n; i++ {
						if i != j {
							c[i][j] += costs[i]
						}
					}
					mu.Unlock()
				}
			}()
		}
		for ti := range tj.Tensors {
			jobs <- ti
		}
		close(jobs)
		wg.Wait()
		fmt.Fprintf(os.Stderr, "target %d/%d done\n", j+1, n)
	}
	// ingestion policy: each state uses its best EARLIER state (causal), or direct
	order := func(perm []int) float64 {
		var tot float64
		for k, j := range perm {
			best := direct[j]
			for _, i := range perm[:k] {
				if c[i][j] < best {
					best = c[i][j]
				}
			}
			tot += best
		}
		return tot
	}
	fwd := make([]int, n)
	for i := range fwd {
		fwd[i] = i
	}
	rev := make([]int, n)
	for i := range rev {
		rev[i] = n - 1 - i
	}
	prevOnly := direct[0]
	for j := 1; j < n; j++ {
		prevOnly += math.Min(direct[j], c[j-1][j])
	}
	rng := rand.New(rand.NewSource(1))
	var rs []float64
	for k := 0; k < *perms; k++ {
		rs = append(rs, order(rng.Perm(n)))
	}
	sort.Float64s(rs)
	arb := minArborescence(direct, c)
	var sumDirect float64
	for _, d := range direct {
		sumDirect += d
	}
	fmt.Printf("states %d  independent %.0f  previous-only chain %.0f  causal-best-earlier forward %.0f  reverse %.0f  random orders min/median/max %.0f/%.0f/%.0f  retrospective arborescence %.0f\n",
		n, sumDirect, prevOnly, order(fwd), order(rev), rs[0], rs[len(rs)/2], rs[len(rs)-1], arb)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(map[string]any{"states": paths, "cost": c, "direct": direct, "independent": sumDirect, "prev_chain": prevOnly, "forward": order(fwd), "reverse": order(rev), "random_min": rs[0], "random_median": rs[len(rs)/2], "random_max": rs[len(rs)-1], "arborescence": arb}, "", " ")
		os.WriteFile(*jsonOut, b, 0o644)
	}
}

// minArborescence: virtual root pays direct[j] to attach j; Chu-Liu/Edmonds on n+1 nodes.
func minArborescence(direct []float64, c [][]float64) float64 {
	n := len(direct)
	w := make([][]float64, n+1)
	for i := range w {
		w[i] = make([]float64, n+1)
		for j := range w[i] {
			w[i][j] = math.Inf(1)
		}
	}
	for j := 0; j < n; j++ {
		w[n][j+0] = direct[j]
		for i := 0; i < n; i++ {
			if i != j {
				w[i][j] = c[i][j]
			}
		}
	}
	return edmonds(w, n)
}

func edmonds(w [][]float64, root int) float64 {
	n := len(w)
	res := 0.0
	alive := make([]bool, n)
	for i := range alive {
		alive[i] = true
	}
	for {
		in := make([]int, n)
		inw := make([]float64, n)
		for v := 0; v < n; v++ {
			in[v], inw[v] = -1, math.Inf(1)
			if !alive[v] || v == root {
				continue
			}
			for u := 0; u < n; u++ {
				if alive[u] && u != v && w[u][v] < inw[v] {
					in[v], inw[v] = u, w[u][v]
				}
			}
			res += inw[v]
		}
		id := make([]int, n)
		vis := make([]int, n)
		for i := range id {
			id[i], vis[i] = -1, -1
		}
		cnt := 0
		for v := 0; v < n; v++ {
			if !alive[v] || v == root {
				continue
			}
			x := v
			for vis[x] != v && id[x] == -1 && x != root && in[x] >= 0 {
				vis[x] = v
				x = in[x]
			}
			if x != root && id[x] == -1 && vis[x] == v {
				for y := in[x]; y != x; y = in[y] {
					id[y] = cnt
				}
				id[x] = cnt
				cnt++
			}
		}
		if cnt == 0 {
			return res
		}
		for v := 0; v < n; v++ {
			if alive[v] && id[v] == -1 {
				id[v] = cnt
				cnt++
			}
		}
		nw := make([][]float64, cnt)
		for i := range nw {
			nw[i] = make([]float64, cnt)
			for j := range nw[i] {
				nw[i][j] = math.Inf(1)
			}
		}
		for u := 0; u < n; u++ {
			for v := 0; v < n; v++ {
				if !alive[u] || !alive[v] || id[u] == id[v] {
					continue
				}
				c := w[u][v]
				if v != root && in[v] >= 0 {
					c -= inw[v]
				}
				if c < nw[id[u]][id[v]] {
					nw[id[u]][id[v]] = c
				}
			}
		}
		root = id[root]
		w = nw
		n = cnt
		alive = make([]bool, n)
		for i := range alive {
			alive[i] = true
		}
	}
}
