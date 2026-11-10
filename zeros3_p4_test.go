package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	data := repTestSQLite(800, 4096, 21)
	for _, mode := range []repMode{repModeShadow, repModePrefer} {
		for _, phase := range phases {
			t.Run(mode.String()+"/"+phase, func(t *testing.T) {
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
