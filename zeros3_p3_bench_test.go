package main

// Z4-P3 maintenance benchmarks. They run against a store built elsewhere
// (ZEROS3_P3_STORE=/path) and only read it, so one fixture serves every variant:
//
//	ZEROS3_P3_STORE=/tmp/fix/s100k go test -run xxx -bench P3 -benchtime 3x -cpuprofile cpu.out
import (
	"os"
	"testing"
)

func p3BenchStore(b *testing.B) *Store {
	dir := os.Getenv("ZEROS3_P3_STORE")
	if dir == "" {
		b.Skip("ZEROS3_P3_STORE not set")
	}
	s, err := OpenStoreShared(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

func BenchmarkP3Open(b *testing.B) {
	dir := os.Getenv("ZEROS3_P3_STORE")
	if dir == "" {
		b.Skip("ZEROS3_P3_STORE not set")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s, err := OpenStoreShared(dir)
		if err != nil {
			b.Fatal(err)
		}
		s.Close()
	}
}

func BenchmarkP3Reachability(b *testing.B) {
	s := p3BenchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr, err := s.computeReachability(false)
		if err != nil || !rr.OK() {
			b.Fatal(err, rr.Issues)
		}
	}
}

func BenchmarkP3ReachabilityDeep(b *testing.B) {
	s := p3BenchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr, err := s.computeReachability(true)
		if err != nil || !rr.OK() {
			b.Fatal(err, rr.Issues)
		}
	}
}

func BenchmarkP3Stats(b *testing.B) {
	s := p3BenchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.computeStats(statsScope{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkP3Verify(b *testing.B) {
	s := p3BenchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if r, err := s.Verify(false); err != nil || !r.OK() {
			b.Fatal(err, r.Issues)
		}
	}
}

func BenchmarkP3VerifyDeep(b *testing.B) {
	s := p3BenchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if r, err := s.Verify(true); err != nil || !r.OK() {
			b.Fatal(err, r.Issues)
		}
	}
}
