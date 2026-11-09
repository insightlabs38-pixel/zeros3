package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

// TestP1C_StoreLayerPutThroughput is a measurement, not a regression test (it only
// runs with ZEROS3_P1C_BENCH=1). It times PutObject directly on the store -- no HTTP,
// no SigV4 -- for tiny sizes the HTTP driver cannot express (0 B) and isolates the
// storage layer from the request path. Policies alternate within one binary:
// "loose" (ZEROS3_SMALL_OBJECT_STORAGE=off behaviour) and "segments" (default).
//
//	ZEROS3_P1C_BENCH=1 go test -run StoreLayerPutThroughput -v .
func TestP1C_StoreLayerPutThroughput(t *testing.T) {
	if os.Getenv("ZEROS3_P1C_BENCH") != "1" {
		t.Skip("set ZEROS3_P1C_BENCH=1")
	}
	type cell struct{ size, conc, n int }
	cells := []cell{{0, 8, 3000}, {1, 8, 3000}, {1024, 8, 3000}, {4096, 1, 1000}, {4096, 8, 4000}, {4096, 32, 4000}, {65536, 8, 1500}, {262143, 8, 800}}
	res := map[string][]float64{}
	lat := map[string][]float64{}
	for round := 0; round < 3; round++ {
		for _, c := range cells {
			policies := []string{"loose", "segments"}
			if round%2 == 1 {
				policies = []string{"segments", "loose"}
			}
			for _, pol := range policies {
				dir := t.TempDir()
				old := smallObjectSegments
				smallObjectSegments = pol == "segments"
				s, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				s.CreateBucket("b")
				var mu sync.Mutex
				var lats []float64
				var wg sync.WaitGroup
				start := time.Now()
				for w := 0; w < c.conc; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := w; i < c.n; i += c.conc {
							body := genRandomBytes(int64(i)+1, c.size)
							ts := time.Now()
							if _, err := s.PutObject("b", fmt.Sprintf("k%d", i), body, "", nil); err != nil {
								t.Error(err)
								return
							}
							d := float64(time.Since(ts).Microseconds()) / 1000
							mu.Lock()
							lats = append(lats, d)
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
				el := time.Since(start).Seconds()
				s.Close()
				smallObjectSegments = old
				key := fmt.Sprintf("%dB_c%d/%s", c.size, c.conc, pol)
				res[key] = append(res[key], float64(c.n)/el)
				sort.Float64s(lats)
				lat[key] = append(lat[key], lats[len(lats)*99/100])
				runtime.GC()
			}
		}
	}
	for _, c := range cells {
		l, sgm := res[fmt.Sprintf("%dB_c%d/loose", c.size, c.conc)], res[fmt.Sprintf("%dB_c%d/segments", c.size, c.conc)]
		sort.Float64s(l)
		sort.Float64s(sgm)
		pl, ps := lat[fmt.Sprintf("%dB_c%d/loose", c.size, c.conc)], lat[fmt.Sprintf("%dB_c%d/segments", c.size, c.conc)]
		sort.Float64s(pl)
		sort.Float64s(ps)
		t.Logf("P1C_STORE size=%d conc=%d loose=%.0f ops/s segments=%.0f ops/s x%.2f  p99 %.2f -> %.2f ms (medians of 3 interleaved rounds)", c.size, c.conc, l[1], sgm[1], sgm[1]/l[1], pl[1], ps[1])
	}
}
