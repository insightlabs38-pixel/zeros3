package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Benchmark the complete maintenance scan, including representation liveness.
// The SQLite-shaped workload has thousands of distinct page components.
func BenchmarkRepReachabilitySQLite(b *testing.B) {
	l := newRepLab(b, repModeShadow)
	l.put("db", repTestSQLite(4096, 4096, 1401))
	l.offline(func() {
		o := defaultCompactOptions()
		o.TargetBytes, o.MinBytes = 4<<20, 1
		if _, err := compactStore(l.dir, o); err != nil {
			b.Fatal(err)
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rr, err := l.s.computeReachability(false)
		if err != nil || !rr.OK() || rr.RepRoots != 1 || rr.RepComponents < 4096 {
			b.Fatalf("reachability: roots=%d components=%d err=%v issues=%v", rr.RepRoots, rr.RepComponents, err, rr.Issues)
		}
	}
}

// A corrupt live root must be rejected before its oversized payload is read.
func TestRepOversizedRootReadBounded(t *testing.T) {
	l := newRepLab(t, repModeShadow)
	l.put("db", repTestSQLite(12, 4096, 1402))
	_, man := l.manifest("db")
	path := l.s.repRootPath(man.ManifestUUID)
	if err := os.Truncate(path, 4*repMaxRootSize); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := l.s.loadRepRoot(man.ManifestUUID)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errRepDescriptor) {
		t.Fatalf("oversized root: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("oversized root allocated %d bytes before rejection", allocated)
	}
	rr, err := l.s.computeReachability(false)
	if err != nil || rr.OK() || rr.RepFaults[man.ManifestUUID] == "" {
		t.Fatalf("oversized live root must fail closed: err=%v issues=%v", err, rr.Issues)
	}
	// Unrelated files are not touched by the read or failed maintenance scan.
	if fi, err := os.Stat(filepath.Join(l.dir, "manifests", man.ManifestUUID+".json")); err != nil || fi.Size() == 0 {
		t.Fatalf("manifest unavailable: %v", err)
	}
}
