package main

// FB-04 M-B1 independent check (X): a second implementation's probes, run
// against the shipped commit queue.
//
// TestFB04X_NoJournalFsyncUnderStoreMu holds the journal fsync of each kind
// of namespace mutation open and requires Store.mu to be free and HEAD, GET
// and LIST to complete meanwhile. At cd9c0d5 (before M-B1) it fails for 9 of
// the 12 kinds; only the object-root paths (PUT, copy, restore) pass.

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fb04xBlockSync makes the next journal fsync wait until release is closed;
// entered is closed when it starts waiting.
func fb04xBlockSync(t *testing.T) (entered, release chan struct{}) {
	entered, release = make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	old := testJournalFault
	testJournalFault = func(point string, size int) (int, error) {
		if point == faultJournalSync && once.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-time.After(30 * time.Second):
			}
		}
		return size, nil
	}
	t.Cleanup(func() { testJournalFault = old })
	return entered, release
}

// fb04xMuFree reports whether Store.mu can be taken within d (a reader may
// hold it for a moment; a mutator holding it across the fsync never lets go).
func fb04xMuFree(s *Store, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if s.mu.TryLock() {
			s.mu.Unlock()
			return true
		}
	}
	return false
}

type fb04xFixture struct {
	dir    string
	s      *Store
	srv    *Server
	upload string // open upload on b/mpu with part 1
	man    manifestV1
}

func newFB04XFixture(t *testing.T) *fb04xFixture {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fb04xFixture{dir: dir, s: s}
	f.srv = NewServer(s, Credentials{AccessKeyID: "AKIAFB04TESTACCESSKY", SecretAccessKey: "FB04TestSecretKeyForZeroS3Tests000000000"}, "us-east-1")
	t.Cleanup(func() { f.s.Close() })
	for _, b := range []string{"b", "empty"} {
		if err := s.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		if _, err := s.PutObject("b", fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("value %d", i)), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	// two archived versions of h, for prune
	for i := 0; i < 3; i++ {
		if _, err := s.PutObject("b", "h", []byte(fmt.Sprintf("h%d", i)), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if f.upload, err = s.CreateMultipartUpload("b", "mpu", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadPart("b", "mpu", f.upload, 1, []byte("part one")); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fb04xFixture) reopen(t *testing.T) {
	t.Helper()
	_ = f.s.Close()
	s, err := OpenStore(f.dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f.s = s
	f.srv = NewServer(s, Credentials{AccessKeyID: "AKIAFB04TESTACCESSKY", SecretAccessKey: "FB04TestSecretKeyForZeroS3Tests000000000"}, "us-east-1")
}

// fb04xMutations is every kind of namespace mutation, run against a fixture.
func fb04xMutations(f *fb04xFixture) []struct {
	name string
	run  func() error
} {
	return []struct {
		name string
		run  func() error
	}{
		{"CreateBucket", func() error { return f.s.CreateBucket("new") }},
		{"DeleteBucket", func() error { return f.s.DeleteBucket("empty") }},
		{"PutObject", func() error { _, err := f.s.PutObject("b", "k1", []byte("v2"), "", nil); return err }},
		{"CopyObject", func() error {
			_, _, err := f.s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "k1", DstBucket: "b", DstKey: "copy"})
			return err
		}},
		{"DeleteObject", func() error { return f.s.DeleteObject("b", "k2") }},
		{"DeleteObjects", func() error {
			w := httptest.NewRecorder()
			f.srv.handleDeleteObjects(w, "b", fb04DeleteXML([]string{"k3", "k4", "k5"}))
			if w.Code != 200 || strings.Contains(w.Body.String(), "<Error>") {
				return fmt.Errorf("DeleteObjects: %d %s", w.Code, w.Body.String())
			}
			return nil
		}},
		{"CreateMultipartUpload", func() error { _, err := f.s.CreateMultipartUpload("b", "m2", "", nil); return err }},
		{"UploadPart", func() error { _, err := f.s.UploadPart("b", "mpu", f.upload, 2, []byte("part two")); return err }},
		{"AbortMultipartUpload", func() error { return f.s.AbortMultipartUpload("b", "mpu", f.upload) }},
		{"CompleteMultipartUpload", func() error {
			_, _, err := f.s.CompleteMultipartUpload("b", "mpu", f.upload, []completedPart{{PartNumber: 1, ETag: fmt.Sprintf("%x", md5Sum([]byte("part one")))}})
			return err
		}},
		{"RestoreObjectVersion", func() error {
			hist, err := f.s.histFind("b", "h")
			if err != nil || len(hist) == 0 {
				return fmt.Errorf("history: %v", err)
			}
			_, _, err = f.s.RestoreObjectVersion("b", "h", hist[0].versionID)
			return err
		}},
		{"PruneHistory", func() error {
			keep := 0
			res, err := f.s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Key: "h", KeepLast: &keep})
			if err != nil {
				return err
			}
			if err := f.s.ApplyHistoryPrune(&res); err != nil {
				return err
			}
			if res.VersionsPruned == 0 {
				return errors.New("nothing pruned")
			}
			return nil
		}},
	}
}

func TestFB04X_NoJournalFsyncUnderStoreMu(t *testing.T) {
	names := fb04xMutations(&fb04xFixture{})
	for i := range names {
		name := names[i].name
		t.Run(name, func(t *testing.T) {
			f := newFB04XFixture(t)
			m := fb04xMutations(f)[i]
			entered, release := fb04xBlockSync(t)
			done := make(chan error, 1)
			go func() { done <- m.run() }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("%s finished without a journal fsync: %v", name, err)
			case <-time.After(20 * time.Second):
				t.Fatalf("%s did not reach the journal fsync", name)
			}
			free := fb04xMuFree(f.s, 500*time.Millisecond)
			reads := make(chan error, 1)
			if free {
				go func() {
					if _, _, err := f.s.headServed("b", "k7"); err != nil {
						reads <- err
						return
					}
					if _, _, err := f.s.GetObject("b", "k7"); err != nil {
						reads <- err
						return
					}
					_, err := f.s.ListObjectsV2("b", "", "", "", 1000)
					reads <- err
				}()
			}
			var readErr error
			readsDone := false
			if free {
				select {
				case readErr = <-reads:
					readsDone = true
				case <-time.After(5 * time.Second):
				}
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !free {
				t.Fatalf("%s holds Store.mu across its journal fsync", name)
			}
			if !readsDone || readErr != nil {
				t.Fatalf("%s: HEAD/GET/LIST did not complete during its journal fsync (done %v, err %v)", name, readsDone, readErr)
			}
		})
	}
}
