// Command z4p6 is the Z4-P6 driver for the streaming B3 engine: ReaderAt-backed ingestion
// with measured resident memory, fresh-process replay and offline optimization.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync/atomic"
	"time"

	"zeros3-z3/semcas"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z4p6:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z4p6 ingest|replay|optimize|cost ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "ingest":
		cmdIngest(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	case "cost":
		cmdCost(os.Args[2:])
	case "optimize":
		cmdOptimize(os.Args[2:])
	case "gen":
		cmdGen(os.Args[2:])
	case "roots":
		cmdRoots(os.Args[2:])
	case "graphbench":
		cmdGraphBench(os.Args[2:])
	case "ranges":
		cmdRanges(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

// countingFile counts the bytes a streaming ingest reads from the source file.
type countingFile struct {
	f *os.File
	n *int64
}

func (c countingFile) ReadAt(p []byte, off int64) (int, error) {
	k, err := c.f.ReadAt(p, off)
	atomic.AddInt64(c.n, int64(k))
	return k, err
}

// heapSampler records the peak Go heap in use while a phase runs.
type heapSampler struct {
	stop chan struct{}
	done chan struct{}
	peak uint64
}

func startHeap() *heapSampler {
	h := &heapSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > atomic.LoadUint64(&h.peak) {
				atomic.StoreUint64(&h.peak, m.HeapInuse)
			}
			select {
			case <-h.stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return h
}

func (h *heapSampler) Stop() uint64 { close(h.stop); <-h.done; return h.peak }

func cmdIngest(args []string) {
	fl := flag.NewFlagSet("ingest", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	mode := fl.String("mode", "full", "identity|semantic|full")
	prof := fl.String("cpuprofile", "", "write a CPU profile")
	cache := fl.Int64("cache-mib", 256, "decoded parent cache budget (MiB)")
	maxT := fl.Int64("max-tensor-mib", 192, "tensors above this are stored as bounded blocks (MiB)")
	work := fl.Int64("work-mib", 512, "transient candidate-buffer budget (MiB)")
	par := fl.Int("parallel", 0, "concurrent candidate evaluations (0 = NumCPU)")
	fl.Parse(args)
	if *prof != "" {
		pf, err := os.Create(*prof)
		must(err)
		must(pprof.StartCPUProfile(pf))
		defer pprof.StopCPUProfile()
	}
	s, err := semcas.Open(*dir)
	must(err)
	opt := semcas.DefaultOptions()
	switch *mode {
	case "identity":
		opt.Semantic, opt.Convert = false, false
	case "semantic":
		opt.Convert = false
	}
	opt.CacheBytes = *cache << 20
	opt.MaxTensor = int(*maxT << 20)
	opt.WorkBytes = *work << 20
	opt.Parallel = *par
	p := semcas.NewPlanner(s, opt)
	enc := json.NewEncoder(os.Stdout)
	hs := startHeap()
	for _, f := range fl.Args() {
		fh, err := os.Open(f)
		must(err)
		st, err := fh.Stat()
		must(err)
		var rd int64
		src := countingFile{fh, &rd}
		t0 := time.Now()
		doc, err := semcas.DetectAt(filepath.Base(f), src, st.Size())
		must(err)
		rec, fs, err := p.Ingest(doc)
		must(err)
		must(s.PutRecipe(rec))
		fh.Close()
		enc.Encode(struct {
			*semcas.FileStats
			ReadBytes int64
			WallMs    int64
			PeakCache int64
			Probe     semcas.ProbeStats
		}{fs, rd, time.Since(t0).Milliseconds(), p.PeakCache, p.Probe})
	}
	peak := hs.Stop()
	enc.Encode(map[string]any{"peak_heap_inuse_mib": float64(peak) / (1 << 20)})
}

func cmdCost(args []string) {
	fl := flag.NewFlagSet("cost", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	c, err := semcas.CostOf(s, *dir)
	must(err)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"cost": c, "total": c.Total(), "with_index": c.WithIndex()})
}

func cmdReplay(args []string) {
	fl := flag.NewFlagSet("replay", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	out := fl.String("out", "", "output directory (one file per recipe, named by original SHA-256)")
	cache := fl.Int64("cache-mib", 256, "replay cache budget (MiB)")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	ids, err := s.B.ListRecipes()
	must(err)
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	hs := startHeap()
	for _, id := range ids {
		r, err := s.GetRecipe(id)
		must(err)
		path := os.DevNull
		if *out != "" {
			must(os.MkdirAll(*out, 0o755))
			path = filepath.Join(*out, id.String())
		}
		f, err := os.Create(path)
		must(err)
		w := bufio.NewWriterSize(f, 1<<20)
		t := time.Now()
		must(s.Replay(r, w, *cache<<20))
		must(w.Flush())
		f.Close()
		fmt.Printf("{\"sha\":\"%s\",\"bytes\":%d,\"ms\":%d,\"ok\":true}\n", id, r.Size, time.Since(t).Milliseconds())
	}
	fmt.Printf("{\"peak_heap_inuse_mib\":%.1f}\n", float64(hs.Stop())/(1<<20))
}

func cmdOptimize(args []string) {
	fl := flag.NewFlagSet("optimize", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	p := semcas.NewPlanner(s, semcas.DefaultOptions())
	t := time.Now()
	st, err := p.Optimize(3)
	must(err)
	json.NewEncoder(os.Stdout).Encode(map[string]any{"optimize": st, "ms": time.Since(t).Milliseconds()})
}

// cmdRoots plans (and optionally applies) group-aware canonical root selection.
func cmdRoots(args []string) {
	fl := flag.NewFlagSet("roots", flag.ExitOnError)
	dir := fl.String("store", "", "store directory")
	apply := fl.Bool("apply", false, "rewrite the store to the planned layout")
	perGroup := fl.Bool("groups", false, "print one line per multi-member group")
	elems := fl.Uint64("elems", 0, "diagnostic: plan only tensors with this element count and print the groups")
	fl.Parse(args)
	s, err := semcas.Open(*dir)
	must(err)
	opt := semcas.DefaultOptions()
	opt.CacheBytes = 256 << 20
	p := semcas.NewPlanner(s, opt)
	if *elems > 0 {
		p.PlanFilter = func(n uint64) bool { return n == *elems }
	}
	c0, err := semcas.CostOf(s, *dir)
	must(err)
	t := time.Now()
	plan, err := p.PlanRoots()
	must(err)
	planMs := time.Since(t).Milliseconds()
	enc := json.NewEncoder(os.Stdout)
	if *elems > 0 {
		for _, g := range plan.Groups {
			if len(g.Nodes) > 1 {
				fmt.Println(g.Describe(s))
			}
		}
	}
	if *perGroup {
		for _, g := range plan.Groups {
			if len(g.Nodes) > 1 {
				enc.Encode(map[string]any{"n": len(g.Nodes), "edges": len(g.Edges), "roots": g.Roots, "cost": g.Cost, "current": g.Current, "exact": g.Exact, "greedy": g.Greedy})
			}
		}
	}
	res := map[string]any{"plan_ms": planMs, "groups": len(plan.Groups), "singletons": plan.Singletons, "evaluated": plan.Evaluated,
		"exact_groups": plan.ExactGroups, "heuristic_groups": plan.HeuristicGroups, "greedy_regret_bytes": plan.Regret,
		"plan_before": plan.Before, "plan_after": plan.After, "store_before": c0.Total()}
	if *apply {
		t = time.Now()
		freed, err := p.ApplyRoots(plan)
		must(err)
		res["apply_ms"] = time.Since(t).Milliseconds()
		res["blob_bytes_freed"] = freed
		c1, err := semcas.CostOf(s, *dir)
		must(err)
		res["store_after"] = c1.Total()
		res["derived_after"] = c1.DerivedComps
	}
	enc.Encode(res)
}
