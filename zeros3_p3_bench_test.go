package main

// Z4-P3 maintenance benchmarks. They run against a store built elsewhere
// (ZEROS3_P3_STORE=/path) and only read it, so one fixture serves every variant:
//
//	ZEROS3_P3_STORE=/tmp/fix/s100k go test -run xxx -bench P3 -benchtime 3x -cpuprofile cpu.out
import (
	"fmt"
	mrand "math/rand/v2"
	"os"
	"sort"
	"strconv"
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

// TestP3_ChurnFixture deletes a fraction of the current objects of the store at
// ZEROS3_P3_CHURN_DIR (mode random or range) and drops all retained history, so
// the data is truly dead. It is a fixture tool for the reclamation matrix, not a
// test: it does nothing unless the variables are set.
//
//	ZEROS3_P3_CHURN_DIR=/x ZEROS3_P3_CHURN_FRAC=0.5 ZEROS3_P3_CHURN_MODE=range go test -run P3_ChurnFixture .
func TestP3_ChurnFixture(t *testing.T) {
	dir := os.Getenv("ZEROS3_P3_CHURN_DIR")
	if dir == "" {
		t.Skip("fixture tool")
	}
	frac, _ := strconv.ParseFloat(os.Getenv("ZEROS3_P3_CHURN_FRAC"), 64)
	mode := os.Getenv("ZEROS3_P3_CHURN_MODE")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	objs := s.snapshotNamespace()
	sort.Slice(objs, func(i, j int) bool { return objs[i].entry.seq < objs[j].entry.seq }) // insertion order
	n := int(float64(len(objs)) * frac)
	var victims []namespaceObject
	if mode == "range" {
		victims = objs[:n]
	} else {
		rng := mrand.New(mrand.NewPCG(5, 6))
		for _, i := range rng.Perm(len(objs))[:n] {
			victims = append(victims, objs[i])
		}
	}
	for _, o := range victims {
		if err := s.DeleteObject(o.bucket, o.key); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	keep := 0
	if _, err := pruneHistoryStore(dir, historyPruneOptions{Bucket: "bench", KeepLast: &keep}, true); err != nil {
		t.Fatal(err)
	}
}

// TestP3_HistoryFixture writes ZEROS3_P3_HIST_VERSIONS versions of each of
// ZEROS3_P3_HIST_KEYS keys of ZEROS3_P3_HIST_SIZE bytes into a fresh store at
// ZEROS3_P3_HIST_DIR; each version edits 2% of the bytes of the previous one in
// place (the size stays constant, most chunks are shared). A fixture tool: it
// does nothing unless the directory is set.
func TestP3_HistoryFixture(t *testing.T) {
	dir := os.Getenv("ZEROS3_P3_HIST_DIR")
	if dir == "" {
		t.Skip("fixture tool")
	}
	keys, _ := strconv.Atoi(os.Getenv("ZEROS3_P3_HIST_KEYS"))
	versions, _ := strconv.Atoi(os.Getenv("ZEROS3_P3_HIST_VERSIONS"))
	size, _ := strconv.Atoi(os.Getenv("ZEROS3_P3_HIST_SIZE"))
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("bench"); err != nil {
		t.Fatal(err)
	}
	rng := mrand.New(mrand.NewPCG(8, 9))
	for k := 0; k < keys; k++ {
		body := genRandomBytes(int64(500+k), size)
		for v := 0; v < versions; v++ {
			for e := 0; e < size/50/64; e++ { // ~2% of the bytes, in 64-byte runs
				off := rng.IntN(size - 64)
				for i := 0; i < 64; i++ {
					body[off+i] = byte(rng.IntN(256))
				}
			}
			if _, err := s.PutObject("bench", fmt.Sprintf("k/%06d", k), body, "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}
