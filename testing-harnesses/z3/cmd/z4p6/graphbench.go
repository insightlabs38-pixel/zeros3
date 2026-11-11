package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/graphnorm"
)

// synthGraph streams an LLVM-shaped synthetic graph into add(): nodes reference mostly
// recent earlier nodes (locality), carry small literals, a share are repeated structures, and
// about 1% of nodes sit in small cycles.
func synthGraph(n int, seed int64, add func(class uint32, flags uint8, lit []byte, out, set []graphnorm.Edge)) {
	r := rand.New(rand.NewSource(seed))
	var lit [8]byte
	for i := 0; i < n; i++ {
		class := uint32(r.Intn(40))
		binary.LittleEndian.PutUint64(lit[:], uint64(r.Intn(1<<(1+r.Intn(20)))))
		var out, set []graphnorm.Edge
		pick := func() int32 {
			if i == 0 {
				return 0
			}
			w := 1 + r.Intn(64)
			if r.Intn(8) == 0 {
				w = 1 + r.Intn(i)
			}
			if w > i {
				w = i
			}
			return int32(i - w)
		}
		for k := 1 + r.Intn(3); k > 0 && i > 0; k-- {
			out = append(out, graphnorm.Edge{Kind: uint16(r.Intn(4)), To: pick()})
		}
		if r.Intn(4) == 0 && i > 0 {
			set = append(set, graphnorm.Edge{Kind: 7, To: pick()}, graphnorm.Edge{Kind: 7, To: pick()})
		}
		if r.Intn(100) == 0 && i+3 < n { // small forward reference closing a cycle later
			out = append(out, graphnorm.Edge{Kind: 9, To: int32(i + 1 + r.Intn(3))})
		}
		add(class, 0, lit[:1+r.Intn(8)], out, set)
	}
}

// cmdGraphBench measures identity computation on synthetic graphs: compact vs reference,
// with live-heap bytes per node (after GC) and peak heap.
func cmdGraphBench(args []string) {
	fl := flag.NewFlagSet("graphbench", flag.ExitOnError)
	n := fl.Int("n", 1000000, "nodes")
	mode := fl.String("mode", "compact", "compact|ref|graph")
	seed := fl.Int64("seed", 1, "seed")
	fl.Parse(args)
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	hs := startHeap()
	t0 := time.Now()
	var ids *graphnorm.Idents
	var live uint64
	var tBuild, tID time.Duration
	switch *mode {
	case "compact":
		c := graphnorm.NewCGraph(*n, *n*3)
		synthGraph(*n, *seed, func(cl uint32, fg uint8, lit []byte, out, set []graphnorm.Edge) {
			if _, err := c.Add(cl, fg, lit, out, set); err != nil {
				must(err)
			}
		})
		c.Trim()
		tBuild = time.Since(t0)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		graphLive := m1.HeapAlloc - m0.HeapAlloc
		t1 := time.Now()
		ids = c.SCCIdentities()
		tID = time.Since(t1)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		live = m1.HeapAlloc - m0.HeapAlloc
		out(*mode, *n, tBuild, tID, graphLive, live, hs.Stop(), ids, c.Edges())
		runtime.KeepAlive(c)
	default:
		g := &graphnorm.Graph{}
		synthGraph(*n, *seed, func(cl uint32, fg uint8, lit []byte, o, st []graphnorm.Edge) {
			g.Add(graphnorm.Node{Class: cl, Flags: fg, Lit: append([]byte(nil), lit...), Out: o, Set: st})
		})
		tBuild = time.Since(t0)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		graphLive := m1.HeapAlloc - m0.HeapAlloc
		t1 := time.Now()
		if *mode == "ref" {
			ids = g.ReferenceSCCIdentities()
		} else {
			ids = g.SCCIdentities()
		}
		tID = time.Since(t1)
		runtime.GC()
		runtime.ReadMemStats(&m1)
		live = m1.HeapAlloc - m0.HeapAlloc
		ne := 0
		for i := range g.Nodes {
			ne += len(g.Nodes[i].Out) + len(g.Nodes[i].Set)
		}
		out(*mode, *n, tBuild, tID, graphLive, live, hs.Stop(), ids, ne)
		runtime.KeepAlive(g)
	}
	_ = os.Stdout
}

func out(mode string, n int, build, id time.Duration, graphLive, live, peak uint64, ids *graphnorm.Idents, edges int) {
	var h [8]byte
	copy(h[:], ids.ID[n/2][:8])
	json.NewEncoder(os.Stdout).Encode(map[string]any{
		"mode": mode, "nodes": n, "edges": edges, "build_ms": build.Milliseconds(), "identity_ms": id.Milliseconds(),
		"graph_bytes_per_node": float64(graphLive) / float64(n), "graph_plus_idents_bytes_per_node": float64(live) / float64(n),
		"peak_heap_bytes_per_node": float64(peak) / float64(n), "classes": ids.Classes, "sccs": ids.SCCs, "cyclic_sccs": ids.Cyclic,
		"search_nodes": ids.Search, "mid_identity_prefix": h[:], "vmhwm_mib": vmHWM(), "bytes_per_node_vmhwm": vmHWM() * (1 << 20) / float64(n),
	})
}

// vmHWM is the process peak resident set (MiB) from /proc/self/status.
func vmHWM() float64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			var kb float64
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:")), "%f", &kb)
			return kb / 1024
		}
	}
	return 0
}
