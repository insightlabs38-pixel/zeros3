package main

import "testing"

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
