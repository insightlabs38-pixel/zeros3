package main

import (
	"bytes"
	"fmt"
	"runtime"
	"time"

	"zeros3-z3/dagz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func scaleSection() {
	fmt.Println("-- wide: one root, N derived tiny children (16 B payload each)")
	fmt.Printf("%7s %8s %10s %10s %10s %10s %11s %12s %7s\n", "N", "objects", "root B", "marshal", "unmarshal", "reach", "reconstruct", "reach alloc", "reads")
	for _, n := range []int{100, 1000, 10000} {
		s, err := dagz3.MeasureWide(n)
		must(err)
		fmt.Printf("%7d %8d %10d %10v %10v %10v %11v %10d B %7d\n", n, s.Objects, s.RootBytes, s.Marshal, s.Unmarshal, s.Reach, s.Reconstruct, s.ReachAlloc, s.ReachReads)
	}
	fmt.Println("-- fan-in: one child, N parents; union walk vs N separate walks")
	fmt.Printf("%7s %8s %12s %7s %14s %7s %12s\n", "N", "objects", "union walk", "reads", "separate walks", "reads", "reach alloc")
	for _, n := range []int{10, 100, 1000} {
		s, err := dagz3.MeasureFanIn(n)
		must(err)
		fmt.Printf("%7d %8d %12v %7d %14v %7d %10d B\n", n, s.Objects, s.Reach, s.ReachReads, s.SumReach, s.SumReads, s.ReachAlloc)
	}
	fmt.Println("-- planner memory (candidates x objects), allocation volume of the marginal planner")
	fmt.Printf("%7s %8s %12s %12s\n", "objects", "cands", "marginal", "alloc")
	for _, n := range []int{100, 1000, 5000} {
		p := dagz3.Synthetic(n, 28)
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		t := time.Now()
		_, err := p.Marginal()
		must(err)
		el := time.Since(t)
		runtime.ReadMemStats(&m1)
		fmt.Printf("%7d %8d %12v %10d B\n", n, len(p.Cands), el, m1.TotalAlloc-m0.TotalAlloc)
	}
	fmt.Println("-- parallel reconstruction of independent roots over one source (optional experiment)")
	st := repz3.NewMemStore()
	_, roots, err := dagz3.FanIn(st, 2000)
	must(err)
	env := kinds.NewEnvLimits(st, dagz3.Lim)
	t := time.Now()
	for _, r := range roots {
		_, _, err := env.Reconstruct(r, discardW{})
		must(err)
	}
	ser := time.Since(t)
	t = time.Now()
	done := make(chan error, 8)
	for w := 0; w < 8; w++ {
		go func(w int) {
			for i := w; i < len(roots); i += 8 {
				if _, _, err := env.Reconstruct(roots[i], discardW{}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(w)
	}
	for w := 0; w < 8; w++ {
		must(<-done)
	}
	fmt.Printf("   2000 roots: serial %v, 8 goroutines %v (GOMAXPROCS %d); no scheduler is proposed\n", ser, time.Since(t), runtime.GOMAXPROCS(0))
	_ = bytes.Equal
}
