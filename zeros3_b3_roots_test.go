package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestB3RootsProbe measures enumerating every manifest root of a large
// namespace, the first step of a similarity index build or refresh.
func TestB3RootsProbe(t *testing.T) {
	dir := os.Getenv("ZEROS3_B3_ROOTS")
	if dir == "" {
		t.Skip("set ZEROS3_B3_ROOTS")
	}
	s, err := openStore(dir, false, true)
	mustOK(t, err)
	defer s.Close()
	runtime.GC()
	var base, end runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	start := time.Now()
	roots, err := s.authSimilarityRoots(nil)
	mustOK(t, err)
	elapsed := time.Since(start)
	close(stop)
	<-done
	runtime.ReadMemStats(&end)
	n := rootsLen(roots)
	runtime.GC()
	var live runtime.MemStats
	runtime.ReadMemStats(&live)
	out := map[string]any{"roots": n, "ms": elapsed.Milliseconds(), "alloc_bytes": end.TotalAlloc - base.TotalAlloc,
		"peak_heap_inuse_above_base": int64(peak.Load()) - int64(base.HeapInuse), "retained_bytes": int64(live.HeapAlloc) - int64(base.HeapAlloc)}
	line, _ := json.Marshal(out)
	fmt.Printf("B3ROOTS %s\n", line)
	runtime.KeepAlive(roots)
}
