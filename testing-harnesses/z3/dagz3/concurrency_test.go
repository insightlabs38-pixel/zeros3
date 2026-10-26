package dagz3

import (
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"zeros3-z3/repz3"
)

func settle(base int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 100 && n > base; i++ {
		time.Sleep(10 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

// TestCodecBranchesDoNotLeakOrDeadlock: a parent with several codec children (gzip and zstd,
// each streaming its semantic plaintext through a pipe, one of them below another stream)
// reconstructs, fails cleanly when a component is missing mid-stream, and leaves no goroutine
// behind either way. Independent roots over one shared source reconstruct concurrently.
func TestCodecBranchesDoNotLeakOrDeadlock(t *testing.T) {
	inner := Tar([]string{"x.db"}, map[string][]byte{"x.db": Join(DB(81, 20))})
	outer := Tar([]string{"a.db.gz", "d.db.gz", "b.car.zst", "c.tar.zst", "note"}, map[string][]byte{
		"a.db.gz":   Gzip(Join(DB(82, 20)), 6),
		"d.db.gz":   Gzip(Join(DB(85, 20)), 6),
		"b.car.zst": Zstd(CAR(1)),
		"c.tar.zst": Zstd(inner),
		"note":      []byte("n"),
	})
	bud := DefaultBudget(4)
	b, err := Discover(outer, bud)
	if err != nil {
		t.Fatal(err)
	}
	if bud.Accepted["gzip"] < 2 || bud.Accepted["zstd"] < 2 {
		t.Fatalf("expected two gzip and two zstd branches: accepted %v rejected %v stops %v", bud.Accepted, bud.Rejected, bud.Stops)
	}
	st, env, id := newStore(t, b)
	base := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		if err := Exact(env, id, outer); err != nil {
			t.Fatal(err)
		}
	}
	if n := settle(base); n > base {
		t.Fatalf("goroutines %d -> %d after successful reconstructions", base, n)
	}
	// failure mid-stream: every reachable codec table or unit in turn
	reach, _ := env.Reachable(id)
	for i, oid := range reach.Order {
		if i%7 != 0 { // a spread of victims keeps the test fast; the exhaustive form is TestDeleteEach
			continue
		}
		cl := st.Clone()
		cl.Delete(oid)
		e := &repz3.Env{Src: cl, Table: env.Table, Limits: env.Limits}
		if _, _, err := e.Reconstruct(id, io.Discard); err == nil || !Bounded(err) {
			t.Fatalf("object %x: %v", oid[:6], err)
		}
	}
	if n := settle(base); n > base {
		t.Fatalf("goroutines %d -> %d after failed reconstructions", base, n)
	}
	// concurrent readers of one source
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Exact(env, id, outer)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := settle(base); n > base {
		t.Fatalf("goroutines %d -> %d after concurrent readers", base, n)
	}
}
