package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Z4-P4: deferred (background) representation construction.

func p4Deferred(t *testing.T, mode repMode, workers, maxJobs int) *Store {
	t.Helper()
	s := repOpenStore(t, mode)
	s.enableRepDeferred(workers, maxJobs)
	return s
}

func p4WaitRoot(t *testing.T, s *Store, bucket, key string) *objectEntry {
	t.Helper()
	s.repQuiesce()
	e, _ := repManifestOf(t, s, bucket, key)
	return e
}

// p4ReadCDC reads the object through the authoritative path only.
func p4ReadCDC(t testing.TB, s *Store, man manifestV1) []byte {
	t.Helper()
	return repCollectReader(t, s.newManifestReader(man, byteRange{0, man.TotalLength - 1}))
}

func p4CopyDir(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // a file renamed away while walking
		}
		rel, _ := filepath.Rel(src, p)
		if strings.HasPrefix(rel, "LOCK") {
			return nil
		}
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP4DeferredBuildsAfterAcknowledgement(t *testing.T) {
	for _, mode := range []repMode{repModeShadow, repModePrefer} {
		t.Run(mode.String(), func(t *testing.T) {
			s := p4Deferred(t, mode, 1, 16)
			release := make(chan struct{})
			var started atomic.Int32
			withTestHook(t, func(p string) {
				if p == hookRepDeferredStarted {
					started.Add(1)
					<-release
				}
			})
			fx := repFixtures(t)
			for _, f := range fx {
				if _, err := s.PutObject("b", f.name, f.data, "application/octet-stream", nil); err != nil {
					t.Fatal(err)
				}
				e, man := repManifestOf(t, s, "b", f.name)
				if repHasRoot(s, e.manifestUUID) {
					t.Fatalf("%s: the PUT returned only after the representation existed", f.name)
				}
				if got := repReadAll(t, s, man, byteRange{0, man.TotalLength - 1}); !bytes.Equal(got, f.data) {
					t.Fatalf("%s: read before the root differs", f.name)
				}
			}
			close(release)
			s.repQuiesce()
			st := s.repDeferredStats()
			if st.Pending != 0 || st.Running != 0 || st.Dropped != 0 || st.QueuedBytes != 0 {
				t.Fatalf("queue not drained: %+v", st)
			}
			// the deferred roots are byte-identical to what an inline build publishes
			ref := repOpenStore(t, mode)
			for _, f := range fx {
				if _, err := ref.PutObject("b", f.name, f.data, "application/octet-stream", nil); err != nil {
					t.Fatal(err)
				}
				e, man := repManifestOf(t, s, "b", f.name)
				re, _ := repManifestOf(t, ref, "b", f.name)
				if !repHasRoot(s, e.manifestUUID) {
					t.Fatalf("%s: no root after the queue drained", f.name)
				}
				a, b := repMustRoot(t, s, e.manifestUUID), repMustRoot(t, ref, re.manifestUUID)
				if a.Rep != b.Rep || a.ObjSHA != b.ObjSHA || len(a.Descs) != len(b.Descs) {
					t.Fatalf("%s: deferred root differs from the inline one", f.name)
				}
				if got := repReadAll(t, s, man, byteRange{0, man.TotalLength - 1}); !bytes.Equal(got, f.data) {
					t.Fatalf("%s: read after the root differs", f.name)
				}
			}
			res := repVerifyOK(t, s, true)
			if res.RepresentationsVerified != len(fx) {
				t.Fatalf("verified %d of %d", res.RepresentationsVerified, len(fx))
			}
			if mode == repModePrefer {
				e, man := repManifestOf(t, s, "b", "sqlite-4k")
				_ = e
				if got := repReadAll(t, s, man, byteRange{5000, 9999}); !bytes.Equal(got, fx[0].data[5000:10000]) || s.rep.rangeReads.Load() == 0 {
					t.Fatalf("prefer mode did not range-read through a deferred representation (%d)", s.rep.rangeReads.Load())
				}
			}
		})
	}
}

func TestP4DeferredCoalescesIdenticalContent(t *testing.T) {
	s := p4Deferred(t, repModeShadow, 1, 16)
	gate := make(chan struct{})
	withTestHook(t, func(p string) {
		if p == hookRepDeferredStarted {
			<-gate
		}
	})
	data := repTestSQLite(300, 4096, 5)
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.PutObject("b", k, data, "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	close(gate)
	s.repQuiesce()
	// a copy after the build clones the remembered root
	if _, err := s.PutObject("b", "d", data, "x", nil); err != nil {
		t.Fatal(err)
	}
	s.repQuiesce()
	st := s.repDeferredStats()
	if st.Enqueued != 2 || st.Built != 1 || st.Cloned != 3 || st.Coalesced != 2 {
		t.Fatalf("stats %+v", st)
	}
	if bt := s.rep.bt.built.Load(); bt != 1 {
		t.Fatalf("%d analyses for identical content", bt)
	}
	for _, k := range []string{"a", "b", "c", "d"} {
		e, _ := repManifestOf(t, s, "b", k)
		if !repHasRoot(s, e.manifestUUID) {
			t.Fatalf("%s has no root", k)
		}
	}
	repVerifyOK(t, s, true)
}

func TestP4DeferredNeverPublishesForAnotherVersion(t *testing.T) {
	s := p4Deferred(t, repModeShadow, 1, 16)
	hold := make(chan struct{})
	var once sync.Once
	reached := make(chan struct{})
	withTestHook(t, func(p string) {
		if p == hookRepDeferredStarted {
			once.Do(func() { close(reached) })
			<-hold
		}
	})
	v1 := repTestSQLite(200, 4096, 1)
	v2 := repTestSQLiteUpdate(v1, 4096, []int{3, 4, 5}, 2)
	if _, err := s.PutObject("b", "k", v1, "x", nil); err != nil {
		t.Fatal(err)
	}
	e1, _ := repManifestOf(t, s, "b", "k")
	<-reached
	if _, err := s.PutObject("b", "k", v2, "x", nil); err != nil { // overwrite while v1's build waits
		t.Fatal(err)
	}
	e2, _ := repManifestOf(t, s, "b", "k")
	close(hold)
	s.repQuiesce()
	if !repHasRoot(s, e2.manifestUUID) {
		t.Fatal("the current version has no representation")
	}
	r2 := repMustRoot(t, s, e2.manifestUUID)
	if r2.ObjSHA != sha256.Sum256(v2) {
		t.Fatal("root of the current version describes other content")
	}
	if repHasRoot(s, e1.manifestUUID) {
		r1 := repMustRoot(t, s, e1.manifestUUID)
		if r1.ObjSHA != sha256.Sum256(v1) || r1.ManifestUUID != e1.manifestUUID {
			t.Fatal("root filed under the old version describes other content")
		}
	}
	repVerifyOK(t, s, true)
}

func TestP4DeferredSkipsManifestsThatLeftTheNamespace(t *testing.T) {
	s := p4Deferred(t, repModeShadow, 1, 16)
	hold := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	withTestHook(t, func(p string) {
		if p == hookRepDeferredStarted {
			once.Do(func() { close(reached) })
			<-hold
		}
	})
	data := repTestSQLite(200, 4096, 9)
	if _, err := s.PutObject("b", "gone", data, "x", nil); err != nil {
		t.Fatal(err)
	}
	e, _ := repManifestOf(t, s, "b", "gone")
	<-reached
	// the key is deleted and its retained history pruned (what an offline prune does) before the build proceeds
	s.mu.Lock()
	delete(s.buckets["b"].objects, "gone")
	delete(s.history, "b")
	s.mu.Unlock()
	close(hold)
	s.repQuiesce()
	if repHasRoot(s, e.manifestUUID) {
		t.Fatal("a root was published for a manifest no namespace or history names")
	}
	if st := s.repDeferredStats(); st.Abandoned == 0 {
		t.Fatalf("not counted as abandoned: %+v", st)
	}
}

func TestP4DeferredQueueIsBoundedAndNeverBlocksPuts(t *testing.T) {
	s := p4Deferred(t, repModeShadow, 1, 3)
	hold := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	withTestHook(t, func(p string) {
		if p == hookRepDeferredStarted {
			once.Do(func() { close(reached) })
			<-hold
		}
	})
	var puts []time.Duration
	for i := 0; i < 8; i++ {
		d := repTestSQLite(120+i, 4096, int64(100+i))
		t0 := time.Now()
		if _, err := s.PutObject("b", fmt.Sprint("k", i), d, "x", nil); err != nil {
			t.Fatal(err)
		}
		puts = append(puts, time.Since(t0))
		if i == 0 {
			<-reached
		}
	}
	st := s.repDeferredStats()
	if st.Pending+st.Running > 3 || st.Dropped != 5 {
		t.Fatalf("queue exceeded its bound: %+v", st)
	}
	close(hold)
	s.repQuiesce()
	if st := s.repDeferredStats(); st.Built != 3 {
		t.Fatalf("%+v", st)
	}
	// every object is still readable and verifies; dropped ones are CDC-only
	res := repVerifyOK(t, s, true)
	if res.RepresentationsVerified != 3 {
		t.Fatalf("verified %d", res.RepresentationsVerified)
	}
	for i := 0; i < 8; i++ {
		_, got, err := s.GetObject("b", fmt.Sprint("k", i))
		if err != nil || !bytes.Equal(got, repTestSQLite(120+i, 4096, int64(100+i))) {
			t.Fatalf("object %d: %v", i, err)
		}
	}
}

func TestP4DeferredShutdownAbandonsWorkWithoutLeaks(t *testing.T) {
	before := runtime.NumGoroutine()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.setRepMode(repModeShadow)
	s.CreateBucket("b")
	s.enableRepDeferred(2, 8)
	gate := make(chan struct{})
	var inBuild atomic.Int32
	withTestHook(t, func(p string) {
		if p == hookRepAnalysisStarted {
			inBuild.Add(1)
			<-gate // a build in flight
		}
	})
	var datas [][]byte
	for i := 0; i < 6; i++ {
		d := repTestSQLite(150+i, 4096, int64(7+i))
		datas = append(datas, d)
		if _, err := s.PutObject("b", fmt.Sprint("k", i), d, "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for inBuild.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	time.Sleep(20 * time.Millisecond)
	close(gate) // the running builds hit the stop flag at their next read
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not return")
	}
	st := s.repDeferredStats()
	if st.Pending != 0 || st.Running != 0 || st.Abandoned < 4 {
		t.Fatalf("%+v", st)
	}
	testHook = nil
	for i := 0; i < 20 && runtime.NumGoroutine() > before+2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Fatalf("goroutines %d -> %d", before, n)
	}
	// the abandoned store reopens clean; every acknowledged object is intact
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.setRepMode(repModeShadow)
	for i, d := range datas {
		_, got, err := s2.GetObject("b", fmt.Sprint("k", i))
		if err != nil || !bytes.Equal(got, d) {
			t.Fatalf("object %d after abandoned shutdown: %v", i, err)
		}
	}
	repVerifyOK(t, s2, true)
}

func TestP4DeferredYieldsToForegroundThenRuns(t *testing.T) {
	old := repDeferStarvation
	repDeferStarvation = 400 * time.Millisecond
	t.Cleanup(func() { repDeferStarvation = old })
	s := p4Deferred(t, repModeShadow, 1, 8)
	var started atomic.Int32
	withTestHook(t, func(p string) {
		if p == hookRepDeferredStarted {
			started.Add(1)
		}
	})
	s.fg.enter() // a request in flight
	if _, err := s.PutObject("b", "k", repTestSQLite(150, 4096, 3), "x", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if started.Load() != 0 {
		t.Fatal("background work started while a foreground request was in flight")
	}
	s.fg.exit()
	s.repQuiesce()
	if started.Load() != 1 {
		t.Fatal("background work did not run once the store was idle")
	}
	// starvation bound: a continuously busy store still makes progress
	s.fg.enter()
	if _, err := s.PutObject("b", "k2", repTestSQLite(151, 4096, 4), "x", nil); err != nil {
		t.Fatal(err)
	}
	s.repQuiesce()
	s.fg.exit()
	if started.Load() != 2 || s.repDeferredStats().Yield == 0 {
		t.Fatalf("a busy store starved background work: %+v", s.repDeferredStats())
	}
}

// A crash image is taken at every build phase (the directory as a killed
// process would leave it); each must hold every acknowledged object, carry
// either no root or an exact one, and verify.
func TestP4DeferredCrashImagesAtEveryPhase(t *testing.T) {
	phases := []string{hookRepDeferredStarted, hookRepAnalysisStarted, hookRepComponentsStaged, hookRepComponentsFlushed,
		hookRepObjectPublished, hookRepRootAfterFormat, hookRepRootBeforeWrite, hookRepRootWritten, hookRepRootBeforeRename,
		hookRepRootAfterRename, hookRepRootPublished}
	for _, sz := range []struct {
		name  string
		pages int
	}{{"packs", 800}, {"segment", 40}} { // components land in packs, or in the active segment for a small object
		data := repTestSQLite(sz.pages, 4096, 21)
		for _, mode := range []repMode{repModeShadow, repModePrefer} {
			for _, phase := range phases {
				t.Run(sz.name+"/"+mode.String()+"/"+phase, func(t *testing.T) {
					s := p4Deferred(t, mode, 1, 4)
					img := t.TempDir()
					var taken atomic.Bool
					withTestHook(t, func(p string) {
						if p == phase && taken.CompareAndSwap(false, true) {
							p4CopyDir(t, s.root, img)
						}
					})
					if _, err := s.PutObject("b", "k", data, "x", nil); err != nil {
						t.Fatal(err)
					}
					s.repQuiesce()
					if !taken.Load() {
						t.Fatalf("phase %s never reached", phase)
					}
					c, err := OpenStore(img)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					c.setRepMode(mode)
					e, man, err := c.HeadObject("b", "k")
					if err != nil {
						t.Fatal(err)
					}
					if got := repReadAll(t, c, man, byteRange{0, man.TotalLength - 1}); !bytes.Equal(got, data) {
						t.Fatal("acknowledged object differs in the crash image")
					}
					res := repVerifyOK(t, c, true)
					if repHasRoot(c, e.manifestUUID) {
						if r := repMustRoot(t, c, e.manifestUUID); r.ObjSHA != sha256.Sum256(data) {
							t.Fatal("published root describes other content")
						}
						if res.RepresentationsVerified != 1 {
							t.Fatalf("a visible root was not exact: %+v", res)
						}
					}
					// the abandoned build can be redone on the image
					c.repQuiesce()
					if err := c.repBuild(man); err != nil {
						t.Fatalf("rebuild on the crash image: %v", err)
					}
					repVerifyOK(t, c, true)
				})
			}
		}
	}
}

func TestP4DeferredConcurrentWritersRace(t *testing.T) {
	s := p4Deferred(t, repModeShadow, 3, 64)
	var wg sync.WaitGroup
	want := map[string][]byte{}
	var mu sync.Mutex
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				d := repTestSQLite(130+(i%3), 4096, int64(w*10+i%4))
				key := fmt.Sprint("k", i%3) // contended keys
				if _, err := s.PutObject("b", key, d, "x", nil); err != nil {
					t.Error(err)
					return
				}
				if i%4 == 3 {
					s.DeleteObject("b", key)
				}
				mu.Lock()
				want[key] = d
				mu.Unlock()
				_, _, _ = s.HeadObject("b", key)
			}
		}(w)
	}
	wg.Wait()
	s.repQuiesce()
	repVerifyOK(t, s, true)
	if st := s.repDeferredStats(); st.Pending != 0 || st.Running != 0 {
		t.Fatalf("%+v", st)
	}
	_ = io.Discard
}

// ---- read cache, parallel deep verify, prefix index -------------------------------------

func p4RefPrefixLookup(ids []repIXIdentity, b []byte, k0 int) (int, bool) {
	// the reference rule: the shortest prefix >= k0 of b that matches exactly one identity
	for n := k0; n <= 32 && n <= len(b); n++ {
		var hit []int
		for i := range ids {
			if bytes.Equal(ids[i][:n], b[:n]) {
				hit = append(hit, i)
			}
		}
		if len(hit) == 1 {
			return hit[0], true
		}
		if len(hit) == 0 {
			return 0, false
		}
	}
	return 0, false
}

func TestP4PrefixIndexMatchesReference(t *testing.T) {
	for seed := int64(0); seed < 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 1 + r.Intn(300)
		ids := make([]repIXIdentity, n)
		for i := range ids {
			r.Read(ids[i][:])
			if seed%3 == 0 && i > 0 && r.Intn(4) == 0 { // long shared prefixes and exact duplicates
				ids[i] = ids[r.Intn(i)]
				if r.Intn(2) == 0 {
					ids[i][31-r.Intn(8)] ^= 1
				}
			}
		}
		k0 := 1 + int(seed%4)
		pi := repIXNewPrefixIndex(append([]repIXIdentity(nil), ids...), k0)
		for i := range ids {
			ref, err := pi.Put(nil, uint32(i))
			if err != nil {
				continue // a duplicate cannot be referenced uniquely
			}
			got, rest, gerr := pi.Get(append(append([]byte(nil), ref...), 0xAA))
			if gerr != nil || int(got) != i || len(rest) != 1 {
				t.Fatalf("seed %d id %d: Get = %d, %v", seed, i, got, gerr)
			}
			if want, ok := p4RefPrefixLookup(ids, ids[i][:], k0); ok && want != i {
				t.Fatalf("reference lookup disagrees")
			}
		}
		// foreign references resolve to nothing, as they must
		for j := 0; j < 20; j++ {
			var f [33]byte
			r.Read(f[:])
			if _, _, err := pi.Get(f[:]); err == nil {
				if want, ok := p4RefPrefixLookup(ids, f[:], k0); !ok {
					_ = want
					t.Fatalf("seed %d: a foreign reference resolved", seed)
				}
			}
		}
	}
}

func TestP4ReadCacheServesExactBytesAndStillVerifiesData(t *testing.T) {
	useLooseLayout(t, "the fixture removes and damages individual component files")
	s := repOpenStore(t, repModePrefer)
	db := repTestSQLite(600, 4096, 41)
	blocks := repTestBlocks(300, 3000, 42)
	car := repTestCAR(blocks)
	tarb := repTestTar(t, map[string][]byte{"a": db, "b": car, "c": make([]byte, 90<<10)}, []string{"a", "b", "c"})
	objs := map[string][]byte{"db": db, "car": car, "tar": tarb}
	for k, v := range objs {
		if _, err := s.PutObject("b", k, v, "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	c := s.repCache()
	for round := 0; round < 3; round++ {
		for k, v := range objs {
			_, man := repManifestOf(t, s, "b", k)
			for _, r := range []byteRange{{100, 5000}, {int64(len(v)) / 2, int64(len(v))/2 + 70000}, {int64(len(v)) - 900, int64(len(v)) - 1}} {
				if r.end >= int64(len(v)) {
					r.end = int64(len(v)) - 1
				}
				if got := repReadAll(t, s, man, r); !bytes.Equal(got, v[r.start:r.end+1]) {
					t.Fatalf("round %d %s %v: bytes differ", round, k, r)
				}
			}
		}
	}
	if c.hits.Load() == 0 {
		t.Fatal("the cache was never hit")
	}
	if s.rep.rangeReads.Load() == 0 || s.rep.fallbacks.Load() != 0 {
		t.Fatalf("range reads %d fallbacks %d", s.rep.rangeReads.Load(), s.rep.fallbacks.Load())
	}
	// a data component damaged after the cache was warm is still caught: the read
	// falls back to CDC and the bytes stay right
	e, man := repManifestOf(t, s, "b", "db")
	root := repMustRoot(t, s, e.manifestUUID)
	src := s.newRepSource(root.Descs, nil)
	reach, err := (&repEnv{Src: src, Limits: repProdLimits}).reachable(root.Rep)
	src.close()
	if err != nil {
		t.Fatal(err)
	}
	damaged := false
	for _, id := range reach.Order {
		if reach.Role[id] == repDepVia && root.Descs[id] == nil {
			corruptChunkAnyLayout(t, s, id)
			damaged = true
			break
		}
	}
	if !damaged {
		t.Fatal("no data component to damage")
	}
	for off := int64(0); off+4096 < int64(len(db)); off += 4096 * 7 {
		got := repReadAll(t, s, man, byteRange{off, off + 4095})
		if !bytes.Equal(got, db[off:off+4096]) {
			t.Fatalf("damaged component served at %d", off)
		}
	}
	// retiring the root drops it from the cache: no representation read follows
	before := s.rep.rangeReads.Load()
	if err := s.retireRepRoot(e.manifestUUID); err != nil {
		t.Fatal(err)
	}
	if got := repReadAll(t, s, man, byteRange{10, 5000}); !bytes.Equal(got, db[10:5001]) || s.rep.rangeReads.Load() != before {
		t.Fatal("a retired root was still served from the cache")
	}
}

func TestP4DeepVerifyParallelEqualsSerial(t *testing.T) {
	useLooseLayout(t, "the fixture damages individual component files")
	s := repOpenStore(t, repModeShadow)
	var victims []string
	for i := 0; i < 9; i++ {
		db := repTestSQLite(150+i, 4096, int64(i))
		if _, err := s.PutObject("b", fmt.Sprint("k", i), db, "x", nil); err != nil {
			t.Fatal(err)
		}
		if i%3 == 1 {
			victims = append(victims, fmt.Sprint("k", i))
		}
	}
	good, _ := s.Verify(true)
	if !good.OK() || good.RepresentationsVerified != 9 {
		t.Fatalf("%+v", good)
	}
	for _, k := range victims {
		e, _ := repManifestOf(t, s, "b", k)
		root := repMustRoot(t, s, e.manifestUUID)
		src := s.newRepSource(root.Descs, nil)
		reach, _ := (&repEnv{Src: src, Limits: repProdLimits}).reachable(root.Rep)
		src.close()
		for _, id := range reach.Order {
			if reach.Role[id] == repDepVia {
				corruptChunkAnyLayout(t, s, id)
				break
			}
		}
	}
	t.Setenv("ZEROS3_MAINT_WORKERS", "1")
	serial, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZEROS3_MAINT_WORKERS", "4")
	par, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	// the physical scan reports chunk issues in scan order; the set must be equal and
	// the representation issues (folded in manifest order) identical
	norm := func(r VerifyResult) (all, reps []string) {
		for _, is := range r.Issues {
			l := fmt.Sprint(is)
			all = append(all, l)
			if strings.HasPrefix(is.Subject, "representation ") {
				reps = append(reps, l)
			}
		}
		slices.Sort(all)
		return
	}
	sa, sr := norm(serial)
	pa, pr := norm(par)
	if serial.OK() || par.OK() || serial.RepresentationsVerified != par.RepresentationsVerified || !slices.Equal(sa, pa) || !slices.Equal(sr, pr) {
		t.Fatalf("serial and parallel verification differ:\n%+v\n%+v", serial, par)
	}
}

// ---- real-process kill ---------------------------------------------------------------------

func p4Body(seed int64, kind int) []byte {
	switch kind {
	case 0:
		return repTestSQLite(70+int(seed%90), 4096, seed)
	case 1:
		return repTestCAR(repTestBlocks(120+int(seed%100), 2500, seed))
	default:
		r := rand.New(rand.NewSource(seed))
		a := make([]byte, 20000+r.Intn(60000))
		r.Read(a)
		return repTestTar(nil, map[string][]byte{"x": a, "y": repTestSQLite(40, 4096, seed)}, []string{"x", "y"})
	}
}

// TestP4_RealKill_DeferredBuildsNeverHurtAcknowledgedData kills a real server
// (SIGKILL) while background builders are running and checks after every kill
// that each acknowledged object is intact, that no visible representation is
// anything but exact (verify -deep), that gc keeps all acknowledged data, and
// that the deferred path really built representations across the rounds.
func TestP4_RealKill_DeferredBuildsNeverHurtAcknowledgedData(t *testing.T) {
	if testing.Short() {
		t.Skip("real-process kill rounds")
	}
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	type acked struct {
		seed int64
		kind int
	}
	var mu sync.Mutex
	ack := map[string]acked{}
	rng := rand.New(rand.NewSource(3))
	rootsSeen, objectsWithoutRoot := 0, 0
	for round := 0; round < 6; round++ {
		addr := freeTCPAddr(t)
		srv := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr, "-representations", "shadow", "-representation-build", "deferred", "-representation-workers", "2")
		srv.Env = os.Environ()
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		waitForZeros3Serve(t, addr)
		client := &http.Client{Timeout: 20 * time.Second}
		if round == 0 {
			resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/b", nil, nil)
			resp.Body.Close()
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for c := 0; c < 6; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					seed := int64(round)<<32 | int64(c)<<20 | int64(i)
					kind := (c + i) % 3
					key := fmt.Sprintf("o/%d/%d/%d", round, c, i)
					body := p4Body(seed, kind)
					req, _ := http.NewRequest(http.MethodPut, "http://"+addr+"/b/"+key, bytes.NewReader(body))
					signTestRequest(t, req, signer, req.URL.Path, req.URL.RawQuery, body, time.Now(), nil)
					resp, err := client.Do(req)
					if err != nil {
						return
					}
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						mu.Lock()
						ack[key] = acked{seed, kind}
						mu.Unlock()
					}
				}
			}(c)
		}
		time.Sleep(time.Duration(600+rng.Intn(1500)) * time.Millisecond)
		srv.Process.Kill()
		srv.Wait()
		close(stop)
		wg.Wait()

		s, err := OpenStore(storeDir)
		if err != nil {
			t.Fatalf("round %d: open after kill: %v", round, err)
		}
		mu.Lock()
		for key, a := range ack {
			_, got, err := s.GetObject("b", key)
			if err != nil || !bytes.Equal(got, p4Body(a.seed, a.kind)) {
				mu.Unlock()
				t.Fatalf("round %d: ACKNOWLEDGED object %s lost or corrupt: %v", round, key, err)
			}
		}
		n := len(ack)
		mu.Unlock()
		v, err := s.Verify(true)
		if err != nil || !v.OK() {
			t.Fatalf("round %d: verify -deep: %v %+v", round, err, v.Issues)
		}
		rootsSeen, objectsWithoutRoot = v.RepresentationRoots, n-v.RepresentationRoots
		s.Close()
		if res, err := gcCollect(storeDir, true); err != nil || !res.LiveSetOK {
			t.Fatalf("round %d: gc: %v %+v", round, err, res)
		}
		s, err = OpenStore(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		for key, a := range ack {
			if _, got, err := s.GetObject("b", key); err != nil || !bytes.Equal(got, p4Body(a.seed, a.kind)) {
				mu.Unlock()
				t.Fatalf("round %d: acknowledged object %s damaged by gc: %v", round, key, err)
			}
		}
		mu.Unlock()
		if v, err := s.Verify(true); err != nil || !v.OK() {
			t.Fatalf("round %d: verify after gc: %v %+v", round, err, v.Issues)
		}
		s.Close()
		t.Logf("round %d: %d acknowledged objects intact; %d roots, %d objects without one (build abandoned by the kill or not reached)", round, n, rootsSeen, objectsWithoutRoot)
	}
	if rootsSeen == 0 {
		t.Fatal("no deferred representation was ever published: the test does not exercise the builders")
	}
}

func TestP4SmallObjectComponentsShareTheSegmentAndSurviveMaintenance(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	dbA := repTestSQLite(40, 4096, 71)
	dbB := repTestSQLiteUpdate(dbA, 4096, []int{3, 17, 30}, 72)
	car := repTestCAR(repTestBlocks(30, 2500, 73))
	tarT := repTestTar(t, map[string][]byte{"a.db": dbA, "note": []byte("n")}, []string{"a.db", "note"})
	keys := repTestKeys(900, 74)
	mk := repTestMerkleSnapshot(3, keys, nil)
	l.put("db", dbA)
	l.put("db", dbB)
	l.put("car", car)
	l.put("tar", tarT)
	l.put("mk", mk)
	loose := len(repLooseSet(t, l.dir))
	if loose != 0 {
		t.Fatalf("%d loose files: small-object components bypassed the segment", loose)
	}
	if r := l.reach(); r.RepRoots < 5 {
		t.Fatalf("roots = %d", r.RepRoots)
	}
	l.exact("initial")
	// restart without a clean seal, then every maintenance operation
	l.offline(func() {})
	l.exact("restart")
	l.offline(func() {
		if res, err := gcCollect(l.dir, true); err != nil || !res.LiveSetOK {
			t.Fatalf("gc: %v %+v", err, res)
		}
	})
	l.exact("gc")
	l.offline(func() {
		if _, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true}); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("repack")
	l.offline(func() {
		if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
			t.Fatal(err)
		}
	})
	l.exact("compact")
	for _, p := range l.packIDs() {
		pid := p.id
		l.offline(func() {
			if _, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: tierCold, IDs: []string{pid}, Apply: true}); err != nil {
				t.Logf("tier move %s: %v", pid, err)
			}
		})
	}
	l.exact("tier")
	l.offline(func() {
		zero := 0
		if _, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "db", KeepLast: &zero}, true); err != nil {
			t.Fatal(err)
		}
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	res := l.exact("prune + gc")
	if res.RepresentationsVerified != 4 {
		t.Fatalf("verified %d", res.RepresentationsVerified)
	}
}

// The dense inverse must answer exactly what the binary search answers, for every
// (symbol, state), on predefined tables, random valid distributions and RLE tables.
func TestP4FSEInverseEqualsBinarySearch(t *testing.T) {
	check := func(name string, mk func() *repZSfseTable) {
		slow, fast := mk(), mk()
		slow.calls = -1 << 30 // never builds the inverse
		fast.calls = 127      // builds it on the first lookup
		for sym := 0; sym <= len(slow.counts)+1; sym++ {
			for next := -2; next < len(slow.dec)+3; next++ {
				s1, b1, n1, ok1 := slow.prev(sym, next)
				s2, b2, n2, ok2 := fast.prev(sym, next)
				if s1 != s2 || b1 != b2 || n1 != n2 || ok1 != ok2 {
					t.Fatalf("%s sym %d next %d: search=(%d,%d,%d,%v) inverse=(%d,%d,%d,%v)", name, sym, next, s1, b1, n1, ok1, s2, b2, n2, ok2)
				}
			}
		}
		if fast.inv == nil && len(fast.dec) > 1 {
			t.Fatalf("%s: inverse never built", name)
		}
	}
	for i := 0; i < 3; i++ {
		i := i
		check(fmt.Sprint("predefined", i), func() *repZSfseTable {
			p := repZSpredTables[i]
			return &repZSfseTable{kind: p.kind, desc: p.desc, acc: p.acc, counts: p.counts, dec: p.dec}
		})
	}
	r := rand.New(rand.NewSource(5))
	for n := 0; n < 200; n++ {
		acc := 5 + r.Intn(5)
		size := 1 << acc
		nsym := 2 + r.Intn(30)
		counts := make([]int16, nsym)
		left := size
		for left > 0 {
			counts[r.Intn(nsym)]++
			left--
		}
		if r.Intn(5) == 0 { // "less than one" probabilities
			for k := 0; k < nsym; k++ {
				if counts[k] > 1 && r.Intn(3) == 0 {
					counts[k]--
					counts[(k+1)%nsym]++
				}
			}
		}
		tbl, err := repZSbuildFSE(append([]int16(nil), counts...), acc)
		if err != nil {
			continue
		}
		check(fmt.Sprint("random", n), func() *repZSfseTable {
			return &repZSfseTable{kind: tbl.kind, desc: tbl.desc, acc: tbl.acc, counts: tbl.counts, dec: tbl.dec}
		})
	}
	check("rle", func() *repZSfseTable { return repZSrleTable(7) })
}
