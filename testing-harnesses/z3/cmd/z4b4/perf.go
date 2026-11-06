package main

import (
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/graphnorm"
)

func hwm() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "?"
}

func cmdPerf(files []string) {
	for _, f := range files {
		d, err := os.ReadFile(f)
		must(err)
		mb := float64(len(d)) / (1 << 20)
		t0 := time.Now()
		bc, err := graphnorm.ParseBitstream(d)
		must(err)
		tParse := time.Since(t0)
		t0 = time.Now()
		L, err := graphnorm.AnalyzeLLVM(bc)
		must(err)
		tAn := time.Since(t0)
		nodes, edges := len(L.Graph.Nodes), 0
		for _, n := range L.Graph.Nodes {
			edges += len(n.Out) + len(n.Set)
		}
		t0 = time.Now()
		e, err := graphnorm.EncodeLLVM(d, graphnorm.PolicyAll)
		must(err)
		tEnc := time.Since(t0)
		t0 = time.Now()
		out, err := e.Decode()
		must(err)
		tDec := time.Since(t0)
		_ = out
		fmt.Printf("%s: %.2f MiB | parse+exactness proof %.0f ms (%.1f MiB/s) | module analysis+SCC identities %.0f ms (%d nodes, %d edges, %.0f ns/node) | full encode incl. per-function proofs+self-replay %.0f ms (%.1f MiB/s) | VM decode %.0f ms (%.1f MiB/s) | VmHWM %s\n",
			f, mb, ms(tParse), mb/tParse.Seconds(), ms(tAn), nodes, edges, float64(tAn.Nanoseconds())/float64(max(nodes, 1)), ms(tEnc), mb/tEnc.Seconds(), ms(tDec), mb/tDec.Seconds(), hwm())
	}
	// graph-engine scaling on synthetic graphs
	for _, n := range []int{100_000, 1_000_000} {
		r := rand.New(rand.NewSource(1))
		g := &graphnorm.Graph{}
		for i := 0; i < n; i++ {
			nd := graphnorm.Node{Class: uint32(r.Intn(8)), Lit: []byte{byte(r.Intn(250)), byte(r.Intn(250))}}
			for k := r.Intn(4); k > 0 && i > 0; k-- {
				nd.Out = append(nd.Out, graphnorm.Edge{Kind: 1, To: int32(r.Intn(i))})
			}
			g.Add(nd)
		}
		var ms0, ms1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms0)
		t0 := time.Now()
		id := g.SCCIdentities()
		tS := time.Since(t0)
		runtime.ReadMemStats(&ms1)
		// renumbered copy for matching
		perm := r.Perm(n)
		h := &graphnorm.Graph{Nodes: make([]graphnorm.Node, n)}
		for i, nd := range g.Nodes {
			c := nd
			c.Out = nil
			for _, e := range nd.Out {
				c.Out = append(c.Out, graphnorm.Edge{Kind: e.Kind, To: int32(perm[e.To])})
			}
			h.Nodes[perm[i]] = c
		}
		ih := h.SCCIdentities()
		t0 = time.Now()
		m := graphnorm.Match(g, h, id, ih, graphnorm.DefaultMatch())
		tM := time.Now().Sub(t0)
		got := 0
		for _, x := range m.Map {
			if x >= 0 {
				got++
			}
		}
		fmt.Printf("synthetic DAG n=%d: SCC identity %.0f ms (%.0f ns/node, +%.0f MiB heap) | match after random renumbering %.0f ms, matched %d (exact %d, dup-order %d, ctx %d)\n",
			n, ms(tS), float64(tS.Nanoseconds())/float64(n), float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<20), ms(tM), got, m.Count[1], m.Count[2], m.Count[3])
	}
	// hostile: one huge symmetric cycle and a dense multi-cycle component
	n := 20000
	g := &graphnorm.Graph{}
	for i := 0; i < n; i++ {
		g.Add(graphnorm.Node{Class: 1, Lit: []byte{1}, Out: []graphnorm.Edge{{Kind: 1, To: int32((i + 1) % n)}}})
	}
	t0 := time.Now()
	id := g.SCCIdentities()
	fmt.Printf("hostile symmetric %d-cycle: %.0f ms, budget exhausted=%v, all symmetric=%v\n", n, ms(time.Since(t0)), id.Budget, countAmbig(id) == n)
}

func countAmbig(i *graphnorm.Idents) int {
	n := 0
	for _, a := range i.Ambig {
		if a == graphnorm.AmbigSymmetric {
			n++
		}
	}
	return n
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
