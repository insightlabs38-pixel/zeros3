package main

// FB-02 G-FAULT: a simulated crash at every hook point, and an ENOSPC short
// write and an EIO fsync on the journal, along the two write paths FB-02
// changed: the idempotent sync commit (M-A5) and the part record at the
// journal frame cap (M-A4). Each store is reopened from disk afterwards and
// must hold the old state or the new one, never a third, and must converge
// on a retry. The refusals themselves (an equivalent idempotent commit, an
// over-cap part) must reach no write point at all.

import (
	"bytes"
	"errors"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fb02Points runs fn and returns the hook points it fired, in first-seen
// order.
func fb02Points(fn func()) []string {
	var mu sync.Mutex
	seen := map[string]bool{}
	var order []string
	old := testHook
	testHook = func(p string) {
		mu.Lock()
		defer mu.Unlock()
		if !seen[p] {
			seen[p] = true
			order = append(order, p)
		}
	}
	defer func() { testHook = old }()
	fn()
	return order
}

func fb02CrashAt(t *testing.T, point string, fn func()) {
	t.Helper()
	old := testHook
	testHook = func(p string) {
		if p == point {
			panic(simulatedCrash{point: p})
		}
	}
	defer func() { testHook = old }()
	runExpectingSimulatedCrash(t, fn)
}

type fb02CommitFixture struct {
	dir    string
	srv    *Server
	creds  Credentials
	region string
	a, b   []byte
	ca, cb []syncChunkDescriptor
	old    string
}

func newFB02CommitFixture(t *testing.T) *fb02CommitFixture {
	t.Helper()
	dir, srv, creds, region := newSyncTestServer(t)
	mustCreateReplicateBucket(t, srv, "b")
	f := &fb02CommitFixture{dir: dir, srv: srv, creds: creds, region: region,
		a: genRandomBytes(5211, 300_000), b: genRandomBytes(5212, 300_000)}
	f.ca, f.cb = fb02Chunks(t, srv.store, f.a), fb02Chunks(t, srv.store, f.b)
	if code, _, body := fb02Commit(t, srv, creds, region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.ca}); code != 200 {
		t.Fatalf("seed: %d %s", code, body)
	}
	f.old, _ = fb02Versions(t, srv.store, "b", "k")
	return f
}

// reopen closes the crashed store and opens it again from disk.
func (f *fb02CommitFixture) reopen(t *testing.T) {
	t.Helper()
	_ = f.srv.store.Close()
	s, err := OpenStore(f.dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f.srv = NewServer(s, f.creds, f.region)
	t.Cleanup(func() { s.Close() })
}

// check: the object is a (history 0) or b (history 1); then an idempotent
// retry lands b once, and a second retry writes nothing.
func (f *fb02CommitFixture) check(t *testing.T) (landed bool) {
	t.Helper()
	_, got, err := f.srv.store.GetObject("b", "k")
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	cur, hist := fb02Versions(t, f.srv.store, "b", "k")
	switch {
	case bytes.Equal(got, f.a) && cur == f.old && hist == 0:
	case bytes.Equal(got, f.b) && cur != f.old && hist == 1:
		landed = true
	default:
		t.Fatalf("after reopen: neither the old nor the new state (current %s, old %s, history %d)", cur, f.old, hist)
	}
	code, resp, body := fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.cb, IfUnchanged: true})
	if code != 200 || resp.Unchanged != landed {
		t.Fatalf("retry: %d %s (landed %v)", code, body, landed)
	}
	fp := storeContentFingerprint(t, f.dir)
	if code, resp, _ := fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.cb, IfUnchanged: true}); code != 200 || !resp.Unchanged || storeContentFingerprint(t, f.dir) != fp {
		t.Fatalf("second retry wrote: %d %+v", code, resp)
	}
	if _, h := fb02Versions(t, f.srv.store, "b", "k"); h != 1 {
		t.Fatalf("history %d after the retries, want 1", h)
	}
	return landed
}

func TestFB02_FaultIfUnchangedCommit(t *testing.T) {
	fb01NoTailWait(t)
	// The equivalent commit reaches no write point: only the acknowledgement.
	f := newFB02CommitFixture(t)
	fp := storeContentFingerprint(t, f.dir)
	pts := fb02Points(func() {
		if code, resp, body := fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.ca, IfUnchanged: true}); code != 200 || !resp.Unchanged {
			t.Fatalf("equivalent commit: %d %s", code, body)
		}
	})
	// The preparation barrier can run without a media mutation. Only the
	// acknowledgement may follow it for an already-equivalent publication.
	if len(pts) != 2 || pts[0] != hookPublicationPrepared || pts[1] != hookAfterAck || storeContentFingerprint(t, f.dir) != fp {
		t.Fatalf("an equivalent idempotent commit reached %v", pts)
	}

	// Every point the writing commit reaches.
	f = newFB02CommitFixture(t)
	pts = fb02Points(func() {
		if code, _, body := fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.cb, IfUnchanged: true}); code != 200 {
			t.Fatalf("writing commit: %d %s", code, body)
		}
	})
	t.Logf("writing idempotent commit reaches %v", pts)
	if len(pts) < 3 {
		t.Fatalf("too few points to cut: %v", pts)
	}
	landed := map[bool]int{}
	for _, point := range pts {
		t.Run("crash="+point, func(t *testing.T) {
			f := newFB02CommitFixture(t)
			fb02CrashAt(t, point, func() {
				fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.cb, IfUnchanged: true})
			})
			f.reopen(t)
			landed[f.check(t)]++
			f.srv.store.Close()
			if res, err := gcCollect(f.dir, true); err != nil || !res.LiveSetOK {
				t.Fatalf("gc after the crash: %v %+v", err, res)
			}
		})
	}
	t.Logf("crash cuts: %d left the old state, %d the new", landed[false], landed[true])

	for _, c := range []struct {
		name  string
		fault func(string, int) (int, error)
		err   error
	}{
		{"ENOSPC short journal write", func(p string, n int) (int, error) {
			if p == faultJournalWrite {
				return n / 2, syscall.ENOSPC
			}
			return 0, nil
		}, syscall.ENOSPC},
		{"EIO journal fsync", func(p string, n int) (int, error) {
			if p == faultJournalSync {
				return 0, syscall.EIO
			}
			return 0, nil
		}, syscall.EIO},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFB02CommitFixture(t)
			testJournalFault = c.fault
			code, _, body := fb02Commit(t, f.srv, f.creds, f.region, syncCommitRequest{Bucket: "b", Key: "k", Chunks: f.cb, IfUnchanged: true})
			testJournalFault = nil
			if code == 200 {
				t.Fatalf("a faulted commit was acknowledged: %s", body)
			}
			f.reopen(t)
			if f.check(t) {
				t.Fatalf("a refused commit landed after reopen")
			}
		})
	}
}

func TestFB02_FaultPartRecordAtCap(t *testing.T) {
	fb01NoTailWait(t)
	at := time.Date(2026, 11, 26, 9, 0, 0, 123456789, time.UTC)
	prev := multipartNow
	multipartNow = func() time.Time { return at }
	defer func() { multipartNow = prev }()
	setup := func(t *testing.T) (string, *Store, string) {
		dir := t.TempDir()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
		id, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		return dir, s, id
	}
	_, s, id := setup(t)
	atCap, atCapSize, over, overSize, n := fb02CapChunks(t, id, at)

	// The over-cap refusal reaches no write point.
	var tooLarge *errPartMetadataTooLarge
	pts := fb02Points(func() {
		if _, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: over, size: overSize}); !errors.As(err, &tooLarge) {
			t.Fatalf("over-cap part: %v", err)
		}
	})
	if len(pts) != 0 {
		t.Fatalf("an over-cap part reached %v", pts)
	}
	pts = fb02Points(func() {
		if _, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: atCap, size: atCapSize}); err != nil {
			t.Fatalf("at-cap part: %v", err)
		}
	})
	s.Close()
	t.Logf("at-cap part record (%d chunks) reaches %v", n, pts)

	// reopened: the upload holds no part 1, or part 1 whole; a retry lands it.
	reopened := func(t *testing.T, dir, id string) bool {
		t.Helper()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer s.Close()
		parts, err := s.ListParts("b", "k", id)
		if err != nil {
			t.Fatal(err)
		}
		landed := len(parts) == 1
		if len(parts) > 1 || landed && (parts[0].size != atCapSize || len(parts[0].chunks) != n) {
			t.Fatalf("after reopen: %d parts", len(parts))
		}
		if _, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: atCap, size: atCapSize}); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if parts, _ := s.ListParts("b", "k", id); len(parts) != 1 || len(parts[0].chunks) != n {
			t.Fatalf("after the retry: %d parts", len(parts))
		}
		return landed
	}
	sort.Strings(pts)
	for _, point := range pts {
		t.Run("crash="+point, func(t *testing.T) {
			dir, s, id := setup(t)
			fb02CrashAt(t, point, func() {
				s.commitPart("b", "k", id, 1, ingestResult{chunks: atCap, size: atCapSize})
			})
			s.Close()
			reopened(t, dir, id)
		})
	}
	for _, c := range []struct {
		name  string
		fault func(string, int) (int, error)
	}{
		{"ENOSPC short journal write", func(p string, n int) (int, error) {
			if p == faultJournalWrite {
				return n / 2, syscall.ENOSPC
			}
			return 0, nil
		}},
		{"EIO journal fsync", func(p string, n int) (int, error) {
			if p == faultJournalSync {
				return 0, syscall.EIO
			}
			return 0, nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, s, id := setup(t)
			testJournalFault = c.fault
			_, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: atCap, size: atCapSize})
			testJournalFault = nil
			if err == nil {
				t.Fatal("a faulted part record was acknowledged")
			}
			s.Close()
			if reopened(t, dir, id) {
				t.Fatal("a refused part record is present after reopen")
			}
		})
	}
}
