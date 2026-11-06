package main

import (
	"encoding/json"
	"os"
	"runtime"
	"time"
	"zeros3-z3/simdelta"
)

func heapSampler() func() {
	stop, done := make(chan struct{}), make(chan struct{})
	var peak uint64
	go func() {
		defer close(done)
		timer := time.NewTicker(5 * time.Millisecond)
		defer timer.Stop()
		sample := func() {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
		}
		for {
			select {
			case <-timer.C:
				sample()
			case <-stop:
				sample()
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"sampled_peak_go_heap": peak, "sample_period_ms": 5})
	}
}
func featureKey(i int) uint64 {
	x := uint64(i) + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
func hashScale(n int) {
	if n < 1 || n > 1000000 {
		panic("hash scale bound")
	}
	x := simdelta.NewIndex()
	t := time.Now()
	for i := 0; i < n; i++ {
		f := []uint64{featureKey(i * 4), featureKey(i*4 + 1), featureKey(i*4 + 2), featureKey(i*4 + 3)}
		x.Add(uint32(i), f)
	}
	build := time.Since(t)
	t = time.Now()
	for i := 0; i < 1000; i++ {
		id := (i * 7919) % n
		q := x.Query([]uint64{featureKey(id * 4)}, 1)
		if len(q) != 1 || q[0].ID != uint32(id) {
			panic("hash query")
		}
	}
	query := time.Since(t)
	t = time.Now()
	for i := 0; i < n; i += 10000 {
		x.Delete(uint32(i))
	}
	x.Rebuild()
	rebuild := time.Since(t)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	emit(map[string]any{"kind": "hash", "chunks": n, "features_per_chunk": 4, "postings": len(x.Buckets), "heap": m.HeapAlloc, "build_ms": float64(build.Microseconds()) / 1000, "query_us": float64(query.Microseconds()) / 1000, "deletion_rebuild_ms": float64(rebuild.Microseconds()) / 1000})
}
