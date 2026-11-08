// P0 profiling overlay. Compiled INTO the root package only through
// `go build -overlay` (see ../build.sh); the file is never written into the
// production tree and production zeros3.go is unchanged.
//
// Enabled only when ZEROS3_P0_PROF_DIR is set. Works for `serve` and for every
// CLI command (gc, repack, ...), including commands that end in os.Exit:
// CPU profiles are written as 500 ms segments (merge with `go tool pprof`),
// and cumulative profiles/metrics are rewritten once per second and on
// SIGUSR1/SIGTERM/SIGINT. At most the last <1 s of a CLI run is not captured.
package main

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var p0mu sync.Mutex
var p0dir string
var p0seg int
var p0cpuOn bool
var p0cpuBuf *os.File

func p0atomic(name string, fill func(f *os.File) error) {
	tmp := filepath.Join(p0dir, "."+name+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	if fill(f) != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	f.Close()
	os.Rename(tmp, filepath.Join(p0dir, name))
}

func p0cpuCycle(restart bool) {
	if p0cpuOn {
		pprof.StopCPUProfile()
		p0cpuBuf.Close()
		p0cpuOn = false
		os.Rename(p0cpuBuf.Name(), p0cpuBuf.Name()[:len(p0cpuBuf.Name())-4]+".pprof")
	}
	if restart {
		p0seg++
		f, err := os.Create(filepath.Join(p0dir, "cpu-"+strconv.Itoa(1000000+p0seg)+".tmp"))
		if err != nil {
			return
		}
		if pprof.StartCPUProfile(f) == nil {
			p0cpuBuf = f
			p0cpuOn = true
		} else {
			f.Close()
		}
	}
}

func p0snapshot(final bool) {
	for _, n := range []string{"allocs", "heap", "mutex", "block"} {
		name := n
		p0atomic(name+".pprof", func(f *os.File) error { return pprof.Lookup(name).WriteTo(f, 0) })
	}
	samples := []metrics.Sample{
		{Name: "/gc/heap/allocs:bytes"}, {Name: "/gc/heap/allocs:objects"},
		{Name: "/gc/cycles/total:gc-cycles"}, {Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/user:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"}, {Name: "/memory/classes/total:bytes"},
		{Name: "/gc/heap/live:bytes"}, {Name: "/sched/goroutines:goroutines"},
	}
	metrics.Read(samples)
	out := map[string]any{"final": final, "t": time.Now().UnixNano(), "gomaxprocs": runtime.GOMAXPROCS(0)}
	for _, s := range samples {
		switch s.Value.Kind() {
		case metrics.KindUint64:
			out[s.Name] = s.Value.Uint64()
		case metrics.KindFloat64:
			out[s.Name] = s.Value.Float64()
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out["mem.pause_total_ns"] = ms.PauseTotalNs
	out["mem.num_gc"] = ms.NumGC
	out["mem.mallocs"] = ms.Mallocs
	out["mem.total_alloc"] = ms.TotalAlloc
	out["mem.heap_inuse"] = ms.HeapInuse
	out["mem.sys"] = ms.Sys
	p0atomic("metrics.json", func(f *os.File) error { return json.NewEncoder(f).Encode(out) })
}

func init() {
	p0dir = os.Getenv("ZEROS3_P0_PROF_DIR")
	if p0dir == "" {
		return
	}
	os.MkdirAll(p0dir, 0o755)
	if v, _ := strconv.Atoi(os.Getenv("ZEROS3_P0_MEMRATE")); v > 0 {
		runtime.MemProfileRate = v
	}
	if v, _ := strconv.Atoi(os.Getenv("ZEROS3_P0_BLOCK_NS")); v > 0 {
		runtime.SetBlockProfileRate(v)
	}
	if v, _ := strconv.Atoi(os.Getenv("ZEROS3_P0_MUTEX_FRAC")); v > 0 {
		runtime.SetMutexProfileFraction(v)
	}
	wantCPU := os.Getenv("ZEROS3_P0_CPU") == "1"
	if secs, _ := strconv.Atoi(os.Getenv("ZEROS3_P0_TRACE_SECS")); secs > 0 {
		if f, err := os.Create(filepath.Join(p0dir, "trace.out")); err == nil {
			if trace.Start(f) == nil {
				time.AfterFunc(time.Duration(secs)*time.Second, func() { trace.Stop(); f.Close() })
			}
		}
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGUSR1, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		p0mu.Lock()
		if wantCPU {
			p0cpuCycle(true)
		}
		p0mu.Unlock()
		tick := time.NewTicker(500 * time.Millisecond)
		n := 0
		for {
			select {
			case <-tick.C:
				p0mu.Lock()
				if wantCPU {
					p0cpuCycle(true)
				}
				n++
				if n%2 == 0 {
					p0snapshot(false)
				}
				p0mu.Unlock()
			case s := <-sig:
				p0mu.Lock()
				if wantCPU {
					p0cpuCycle(s == syscall.SIGUSR1)
				}
				p0snapshot(s != syscall.SIGUSR1)
				p0mu.Unlock()
			}
		}
	}()
}
