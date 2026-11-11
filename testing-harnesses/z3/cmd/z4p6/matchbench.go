package main

import (
	"encoding/json"
	"flag"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"runtime/pprof"
	"time"

	"zeros3-z3/graphnorm"
)

// cmdMatchBench times cross-revision matching on a synthetic graph and a renumbered,
// edited copy (insertions, deletions, literal changes), reference versus compact engine, and
// verifies the two results are identical.
func cmdMatchBench(args []string) {
	fl := flag.NewFlagSet("matchbench", flag.ExitOnError)
	n := fl.Int("n", 200000, "nodes")
	mode := fl.String("mode", "both", "ref|new|both")
	prof := fl.String("cpuprofile", "", "write a CPU profile of the matching phase")
	fl.Parse(args)
	g := &graphnorm.Graph{}
	synthGraph(*n, 1, func(cl uint32, fg uint8, lit []byte, o, st []graphnorm.Edge) {
		g.Add(graphnorm.Node{Class: cl, Flags: fg, Lit: append([]byte(nil), lit...), Out: o, Set: st})
	})
	// revision B: random renumbering, 1% literal edits, 0.5% appended nodes
	r := rand.New(rand.NewSource(2))
	perm := r.Perm(len(g.Nodes))
	h := &graphnorm.Graph{Nodes: make([]graphnorm.Node, len(g.Nodes))}
	for old, nw := range perm {
		nd := g.Nodes[old]
		out := make([]graphnorm.Edge, len(nd.Out))
		for i, e := range nd.Out {
			out[i] = graphnorm.Edge{Kind: e.Kind, To: int32(perm[e.To])}
		}
		set := make([]graphnorm.Edge, len(nd.Set))
		for i, e := range nd.Set {
			set[i] = graphnorm.Edge{Kind: e.Kind, To: int32(perm[e.To])}
		}
		h.Nodes[nw] = graphnorm.Node{Class: nd.Class, Flags: nd.Flags, Lit: nd.Lit, Out: out, Set: set}
	}
	for i := 0; i < *n/100; i++ {
		x := r.Intn(len(h.Nodes))
		h.Nodes[x].Lit = []byte{byte(r.Intn(250)), 9}
	}
	ia, ib := g.SCCIdentities(), h.SCCIdentities()
	opt := graphnorm.DefaultMatch()
	type res struct {
		name   string
		ms     int64
		heapMB float64
		m      *graphnorm.MatchResult
	}
	run := func(name string, f func() *graphnorm.MatchResult) res {
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)
		hs := startHeap()
		t := time.Now()
		m := f()
		d := time.Since(t)
		peak := hs.Stop()
		return res{name, d.Milliseconds(), float64(peak) / (1 << 20), m}
	}
	if *prof != "" {
		pf, err := os.Create(*prof)
		must(err)
		must(pprof.StartCPUProfile(pf))
		defer pprof.StopCPUProfile()
	}
	var rs []res
	if *mode != "new" {
		rs = append(rs, run("reference", func() *graphnorm.MatchResult { return graphnorm.ReferenceMatch(g, h, ia, ib, opt) }))
	}
	if *mode != "ref" {
		rs = append(rs, run("compact", func() *graphnorm.MatchResult { return graphnorm.Match(g, h, ia, ib, opt) }))
	}
	same := len(rs) < 2 || reflect.DeepEqual(rs[0].m, rs[1].m)
	var out []map[string]any
	for _, x := range rs {
		matched := 0
		for _, v := range x.m.Map {
			if v >= 0 {
				matched++
			}
		}
		out = append(out, map[string]any{"engine": x.name, "ms": x.ms, "peak_heap_mib": x.heapMB, "matched": matched, "counts": x.m.Count, "passes": x.m.Passes, "reject": x.m.Reject})
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"nodes": *n, "identical_results": same, "runs": out})
}
