package main

// FB-04 M-B1 measurements. Both tests use only APIs that exist before and
// after M-B1, so the same file runs on the base build for comparison.
//
// TestFB04Bench_ReadTailUnderMutations (ZEROS3_FB04_BENCH=1): HEAD and GET
// latency of hot keys while one goroutine loops a kind of mutation, as in the
// F1 ZS-LOCK-01 measurement. Optional injected journal fsync delay
// (ZEROS3_FB04_FSYNC_US, e.g. 2000) stands in for slower media; it delays the
// journal fsync only. One JSON line per mode and round goes to
// ZEROS3_FB04_OUT when set.
//
// TestFB04_DeleteObjectsSyncCount counts journal fsyncs of one 1,000-key
// DeleteObjects request through the S3 handler.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fb04Pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	return d[int(p*float64(len(d)-1))]
}

// fb04CountSyncs counts journal fsyncs while fn runs, delaying each by delay.
func fb04CountSyncs(t testing.TB, delay time.Duration, fn func()) int64 {
	var n atomic.Int64
	old := testJournalFault
	testJournalFault = func(point string, size int) (int, error) {
		if point == faultJournalSync {
			n.Add(1)
			if delay > 0 {
				time.Sleep(delay)
			}
		}
		return size, nil
	}
	defer func() { testJournalFault = old }()
	fn()
	return n.Load()
}

func fb04DeleteXML(keys []string) []byte {
	var b strings.Builder
	b.WriteString("<Delete><Quiet>true</Quiet>")
	for _, k := range keys {
		fmt.Fprintf(&b, "<Object><Key>%s</Key></Object>", k)
	}
	b.WriteString("</Delete>")
	return []byte(b.String())
}

func TestFB04_DeleteObjectsSyncCount(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := NewServer(store, Credentials{AccessKeyID: "AKIAFB04TESTACCESSKY", SecretAccessKey: "FB04TestSecretKeyForZeroS3Tests000000000"}, "us-east-1")
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	const n = 1000
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("k/%04d", i)
		if _, err := store.PutObject("b", keys[i], []byte("x"), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	body := fb04DeleteXML(keys)
	var w *httptest.ResponseRecorder
	t0 := time.Now()
	syncs := fb04CountSyncs(t, 0, func() {
		w = httptest.NewRecorder()
		srv.handleDeleteObjects(w, "b", body)
	})
	el := time.Since(t0)
	if w.Code != 200 || strings.Contains(w.Body.String(), "<Error>") {
		t.Fatalf("DeleteObjects: %d %s", w.Code, w.Body.String())
	}
	for _, k := range keys {
		if e, _ := store.objFind("b", k); e != nil {
			t.Fatalf("%s still visible", k)
		}
	}
	t.Logf("DeleteObjects of %d keys: %d journal fsyncs, %v", n, syncs, el)
	if out := os.Getenv("ZEROS3_FB04_OUT"); out != "" {
		fb04Emit(t, out, map[string]any{"test": "delete_objects", "keys": n, "journal_fsyncs": syncs, "elapsed_us": el.Microseconds()})
	}
	if os.Getenv("ZEROS3_FB04_BASE") == "" && syncs != 1 {
		t.Fatalf("DeleteObjects of %d keys took %d journal fsyncs, want 1", n, syncs)
	}
}

func fb04Emit(t testing.TB, path string, row map[string]any) {
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// fb04Mutator returns one iteration of a mutation loop.
func fb04Mutator(t testing.TB, s *Store, srv *Server, mode string) func(i int) error {
	body := []byte("hello world")
	switch mode {
	case "put-put":
		return func(i int) error {
			k := fmt.Sprintf("churn-%d", i)
			if _, err := s.PutObject("b", k, body, "text/plain", nil); err != nil {
				return err
			}
			_, err := s.PutObject("b", k, body, "text/plain", nil)
			return err
		}
	case "put-delete":
		return func(i int) error {
			k := fmt.Sprintf("churn-%d", i)
			if _, err := s.PutObject("b", k, body, "text/plain", nil); err != nil {
				return err
			}
			return s.DeleteObject("b", k)
		}
	case "bucket":
		return func(i int) error {
			name := fmt.Sprintf("tmp-%d", i)
			if err := s.CreateBucket(name); err != nil {
				return err
			}
			return s.DeleteBucket(name)
		}
	case "multipart":
		return func(i int) error {
			id, err := s.CreateMultipartUpload("b", fmt.Sprintf("mpu-%d", i), "", nil)
			if err != nil {
				return err
			}
			return s.AbortMultipartUpload("b", fmt.Sprintf("mpu-%d", i), id)
		}
	case "delete-batch":
		return func(i int) error {
			keys := make([]string, 50)
			for j := range keys {
				keys[j] = fmt.Sprintf("batch-%d-%d", i, j)
				if _, err := s.PutObject("b", keys[j], body, "", nil); err != nil {
					return err
				}
			}
			w := httptest.NewRecorder()
			srv.handleDeleteObjects(w, "b", fb04DeleteXML(keys))
			if w.Code != 200 {
				return fmt.Errorf("DeleteObjects: %d", w.Code)
			}
			return nil
		}
	}
	t.Fatalf("unknown mode %s", mode)
	return nil
}

func TestFB04Bench_ReadTailUnderMutations(t *testing.T) {
	if os.Getenv("ZEROS3_FB04_BENCH") == "" {
		t.Skip("set ZEROS3_FB04_BENCH=1")
	}
	delay := time.Duration(0)
	if v := os.Getenv("ZEROS3_FB04_FSYNC_US"); v != "" {
		us, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		delay = time.Duration(us) * time.Microsecond
	}
	dur := 2 * time.Second
	if v := os.Getenv("ZEROS3_FB04_SECONDS"); v != "" {
		sec, _ := strconv.Atoi(v)
		dur = time.Duration(sec) * time.Second
	}
	modes := []string{"idle", "put-put", "put-delete", "bucket", "multipart", "delete-batch"}
	if v := os.Getenv("ZEROS3_FB04_MODES"); v != "" {
		modes = strings.Split(v, ",")
	}
	build := os.Getenv("ZEROS3_FB04_BUILD")
	for round := 0; round < 2; round++ {
		for _, mode := range modes {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(s, Credentials{AccessKeyID: "AKIAFB04TESTACCESSKY", SecretAccessKey: "FB04TestSecretKeyForZeroS3Tests000000000"}, "us-east-1")
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 64; i++ {
				if _, err := s.PutObject("b", fmt.Sprintf("hot-%d", i), []byte("hello world"), "text/plain", nil); err != nil {
					t.Fatal(err)
				}
			}
			var stop atomic.Bool
			var iters atomic.Int64
			var wg sync.WaitGroup
			var syncs int64
			// phase runs two readers for dur: HEAD and GET of hot keys, either
			// in a closed loop (as F1 measured) or paced every 20 us with the
			// latency taken from the scheduled start, so a reader stalled
			// behind a lock counts every request it could not issue (no
			// coordinated omission). The phases run apart so the spinning
			// closed-loop readers do not starve the paced ones.
			phase := func(paced bool) {
				kinds := []string{"head", "get"}
				if paced {
					kinds = []string{"head_paced", "get_paced"}
				}
				res := make([][]time.Duration, len(kinds))
				var rwg sync.WaitGroup
				it0 := iters.Load()
				begin := time.Now()
				deadline := begin.Add(dur)
				const interval = 20 * time.Microsecond
				readers := 2
				if paced {
					// One paced goroutine alternates HEAD and GET: two
					// spinning goroutines see multi-millisecond scheduler
					// stalls on this VM even with no store at all.
					readers = 1
				}
				var resMu sync.Mutex
				for g := 0; g < readers; g++ {
					rwg.Add(1)
					go func(g int) {
						defer rwg.Done()
						local := [2][]time.Duration{make([]time.Duration, 0, 1<<20), make([]time.Duration, 0, 1<<20)}
						for i := 0; ; i++ {
							k := fmt.Sprintf("hot-%d", (i*7+g)%64)
							get := g == 1
							t0 := time.Now()
							if paced {
								get = i%2 == 1
								t0 = begin.Add(time.Duration(i) * interval)
								// spin: a timer sleep overshoots by up to a
								// millisecond here, which would read as latency
								for time.Until(t0) > 0 {
									runtime.Gosched()
								}
							}
							if t0.After(deadline) {
								break
							}
							var err error
							if get {
								_, _, err = s.GetObject("b", k)
							} else {
								_, _, err = s.headServed("b", k)
							}
							if err != nil {
								t.Error(err)
								return
							}
							if get {
								local[1] = append(local[1], time.Since(t0))
							} else {
								local[0] = append(local[0], time.Since(t0))
							}
						}
						resMu.Lock()
						res[0] = append(res[0], local[0]...)
						res[1] = append(res[1], local[1]...)
						resMu.Unlock()
					}(g)
				}
				rwg.Wait()
				n := iters.Load() - it0
				for ki, kind := range kinds {
					lat := res[ki]
					slices.Sort(lat)
					slow := 0
					for _, d := range lat {
						if d > 250*time.Microsecond {
							slow++
						}
					}
					row := map[string]any{"build": build, "round": round, "mode": mode, "reader": kind, "fsync_delay_us": delay.Microseconds(),
						"ops": len(lat), "ops_per_s": float64(len(lat)) / dur.Seconds(),
						"p50_ns": fb04Pct(lat, 0.5).Nanoseconds(), "p99_ns": fb04Pct(lat, 0.99).Nanoseconds(), "p999_ns": fb04Pct(lat, 0.999).Nanoseconds(),
						"max_ns": fb04Pct(lat, 1).Nanoseconds(), "over_250us": slow, "mutation_iters": n}
					t.Logf("%s r%d %-12s %-10s ops=%d p50=%v p99=%v p99.9=%v max=%v >250us=%d iters=%d", build, round, mode, kind, len(lat),
						fb04Pct(lat, 0.5), fb04Pct(lat, 0.99), fb04Pct(lat, 0.999), fb04Pct(lat, 1), slow, n)
					if out := os.Getenv("ZEROS3_FB04_OUT"); out != "" {
						fb04Emit(t, out, row)
					}
				}
			}
			run := func() {
				time.Sleep(100 * time.Millisecond)
				phase(false)
				phase(true)
				stop.Store(true)
				wg.Wait()
			}
			syncs = fb04CountSyncs(t, delay, func() {
				if mode != "idle" {
					step := fb04Mutator(t, s, srv, mode)
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := 0; !stop.Load(); i++ {
							if err := step(i); err != nil {
								t.Error(err)
								return
							}
							iters.Add(1)
						}
					}()
				}
				run()
			})
			if out := os.Getenv("ZEROS3_FB04_OUT"); out != "" {
				fb04Emit(t, out, map[string]any{"build": build, "round": round, "mode": mode, "reader": "syncs", "fsync_delay_us": delay.Microseconds(), "journal_fsyncs": syncs, "mutation_iters": iters.Load()})
			}
			s.Close()
		}
	}
}
