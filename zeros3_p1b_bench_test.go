package main

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestP1B_PopulatedStoreThroughput is a measurement, not a regression test:
// it only runs with ZEROS3_P1B_BENCH=1. It times concurrent 4 KiB PUTs on a
// brand new store (every shard directory missing) and again after the store
// holds tens of thousands of chunks (most shard directories present).
func TestP1B_PopulatedStoreThroughput(t *testing.T) {
	if os.Getenv("ZEROS3_P1B_BENCH") != "1" {
		t.Skip("set ZEROS3_P1B_BENCH=1")
	}
	store, _ := newBucketStore(t)
	const conc = 8
	put := func(first, n int) float64 {
		var wg sync.WaitGroup
		start := time.Now()
		for w := 0; w < conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := first + w; i < first+n; i += conc {
					if _, err := store.PutObject("b", fmt.Sprintf("k%d", i), genRandomBytes(int64(i)+1, 4096), "", nil); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		return float64(n) / time.Since(start).Seconds()
	}
	cold := put(0, 2000)
	put(2000, 40000) // populate
	warm := put(42000, 2000)
	t.Logf("P1B_POPULATED cold(0 chunks)=%.0f ops/s  populated(42k chunks)=%.0f ops/s", cold, warm)
}
