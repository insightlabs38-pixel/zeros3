package main

// FB-03 M-A2: CAS presence is a function of published state. A chunk counts
// as present only when a durable copy is reachable: a locator entry (inserted
// after its pack or segment is durable, removed before the file is
// unlinked) or a loose file with no publication in flight. Every sink uses
// the same check, so a chunk already durable as a loose file is not written
// again by the small-object segment path (F2-CAS-01), and the locator is
// consulted before the loose path (F2-CAS-02, F3-FS-02).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fb03Physical sums the bytes of chunk files, packs and segments.
func fb03Physical(t *testing.T, dir string) (total int64, loose int) {
	t.Helper()
	for _, sub := range []string{"chunks", "packs", "segments"} {
		filepath.WalkDir(filepath.Join(dir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				total += info.Size()
				if sub == "chunks" {
					loose++
				}
			}
			return nil
		})
	}
	return
}

func fb03Put(t *testing.T, s *Store, prefix string, bodies [][]byte) {
	t.Helper()
	for i, b := range bodies {
		if _, err := s.PutObject("b", fmt.Sprintf("%s%04d", prefix, i), b, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func fb03ChunkSums(t *testing.T, s *Store, key string) [][32]byte {
	t.Helper()
	_, man, err := s.HeadObject("b", key)
	mustOK(t, err)
	out := make([][32]byte, len(man.Chunks))
	for i, c := range man.Chunks {
		b, err := hex.DecodeString(c.SHA256)
		mustOK(t, err)
		copy(out[i][:], b)
	}
	return out
}

// The F2 reproduction (TestF2_SegmentDedupAgainstLooseChunks), now with
// assertions: re-putting content whose chunks are loose grows the store by
// about what the segments-first control grows (inline manifests only).
func TestFB03_SegmentDedupAgainstLooseChunks(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		n    int
	}{
		{"small-200KB-segment-sink", 200_000, 40},
		{"medium-1.5MB-direct-pack-sink", 1_500_000, 8},
	} {
		growth := map[bool]float64{}
		for _, firstLoose := range []bool{true, false} {
			name := tc.name + map[bool]string{true: "/first-copy-loose", false: "/first-copy-production"}[firstLoose]
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				bodies := make([][]byte, tc.n)
				for i := range bodies {
					bodies[i] = genRandomBytes(4242+int64(i), tc.size)
				}
				t.Run("first", func(t *testing.T) {
					if firstLoose {
						useLooseLayout(t, "FB-03: first copy stored loose (pre-segment store, import or policy off)")
					} else {
						useProductionLayout(t)
					}
					s, err := OpenStore(dir)
					mustOK(t, err)
					mustOK(t, s.CreateBucket("b"))
					fb03Put(t, s, "a", bodies)
					mustOK(t, s.Close())
				})
				useProductionLayout(t)
				s, err := OpenStore(dir)
				mustOK(t, err)
				before, looseBefore := fb03Physical(t, dir)
				fb03Put(t, s, "dup", bodies)
				for i, b := range bodies {
					_, got, err := s.GetObject("b", fmt.Sprintf("dup%04d", i))
					if err != nil || !bytes.Equal(got, b) {
						t.Fatalf("dup%04d: %v", i, err)
					}
				}
				mustOK(t, s.Close())
				after, looseAfter := fb03Physical(t, dir)
				logical := int64(tc.size * tc.n)
				g := float64(after-before) / float64(logical)
				growth[firstLoose] = g
				t.Logf("%s: growth %d B (%.5fx logical), loose files %d -> %d", name, after-before, g, looseBefore, looseAfter)
				if looseAfter != looseBefore {
					t.Fatalf("loose files changed %d -> %d", looseBefore, looseAfter)
				}
				if g > 0.01 {
					t.Fatalf("re-put of stored content grew the store by %.4fx its logical size", g)
				}
			})
		}
		if growth[true] > 2*growth[false]+0.001 {
			t.Fatalf("%s: first-copy-loose growth %.5f against control %.5f", tc.name, growth[true], growth[false])
		}
	}
}

// casStat consults the locator first: a packed dedup hit no longer pays a
// failed stat of the loose path (F2-CAS-02 measured 1,004 ns against 34 ns
// for the lookup and 977 ns for the failed stat).
func TestFB03_CasStatCost(t *testing.T) {
	useProductionLayout(t)
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	var present [][32]byte
	for i := range 6 {
		key := fmt.Sprintf("p%d", i)
		_, err := s.PutObject("b", key, genRandomBytes(777+int64(i), 8<<20), "", nil)
		mustOK(t, err)
		present = append(present, fb03ChunkSums(t, s, key)...)
	}
	absent := make([][32]byte, len(present))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range absent {
		for j := range absent[i] {
			absent[i][j] = byte(rng.Uint32())
		}
	}
	timeIt := func(sums [][32]byte, fn func([32]byte)) float64 {
		best := 0.0
		for round := range 5 {
			t0 := time.Now()
			for range 20 {
				for _, x := range sums {
					fn(x)
				}
			}
			ns := float64(time.Since(t0).Nanoseconds()) / float64(20*len(sums))
			if round == 0 || ns < best {
				best = ns
			}
		}
		return best
	}
	var sink int64
	statPresent := timeIt(present, func(x [32]byte) { n, _ := s.casStat(x); sink += n })
	statAbsent := timeIt(absent, func(x [32]byte) { n, _ := s.casStat(x); sink += n })
	lookPresent := timeIt(present, func(x [32]byte) { l, _ := s.packLookup(x); sink += int64(l.logical) })
	osStat := timeIt(present, func(x [32]byte) { os.Stat(s.chunkPath(x)) })
	_, loose := fb03Physical(t, dir)
	t.Logf("FB03_CASSTAT chunks=%d loose=%d casStat_present_packed_ns=%.0f casStat_absent_ns=%.0f packLookup_present_ns=%.0f os_stat_missing_loose_ns=%.0f sink=%d",
		len(present), loose, statPresent, statAbsent, lookPresent, osStat, sink%7)
	for _, x := range present {
		if n, err := s.casStat(x); err != nil || n <= 0 {
			t.Fatalf("packed chunk %x: %d %v", x, n, err)
		}
	}
	if statPresent > osStat/2 {
		t.Fatalf("casStat of a packed chunk costs %.0f ns, more than half a failed stat (%.0f ns): the loose path is still stat'ed first", statPresent, osStat)
	}
}

func fb03Absent(t *testing.T, s *Store, sum [32]byte, when string) {
	t.Helper()
	if n, err := s.casStat(sum); err == nil || !os.IsNotExist(err) {
		t.Errorf("%s: chunk %x reported present (%d, %v)", when, sum[:6], n, err)
	}
}

// No dedup hit on a chunk whose pack or segment is not published yet.
func TestFB03_PresenceWaitsForPublication(t *testing.T) {
	t.Run("direct-pack", func(t *testing.T) {
		useProductionLayout(t)
		s, err := OpenStore(t.TempDir())
		mustOK(t, err)
		defer s.Close()
		mustOK(t, s.CreateBucket("b"))
		body := genRandomBytes(9001, 2<<20)
		sums := map[[32]byte]bool{}
		for pos := 0; pos < len(body); {
			n := findCDCBoundary(body[pos:], true)
			sums[sha256.Sum256(body[pos:pos+n])] = true
			pos += n
		}
		var checked atomic.Int64
		withTestHook(t, func(p string) {
			switch p {
			case hookPackValidated, hookPackBeforePublish, hookPackAfterRename, hookPackAfterDirSync:
				for sum := range sums {
					fb03Absent(t, s, sum, p)
				}
				checked.Add(1)
			}
		})
		_, err = s.PutObject("b", "k", body, "", nil)
		mustOK(t, err)
		testHook = nil
		if checked.Load() < 4 {
			t.Fatalf("only %d publication points checked", checked.Load())
		}
		for sum := range sums {
			if _, err := s.casStat(sum); err != nil {
				t.Fatalf("after publication: %v", err)
			}
		}
	})
	t.Run("segment", func(t *testing.T) {
		useProductionLayout(t)
		s, err := OpenStore(t.TempDir())
		mustOK(t, err)
		defer s.Close()
		mustOK(t, s.CreateBucket("b"))
		body := genRandomBytes(9002, 40_000)
		sum := sha256.Sum256(body) // one chunk: below the CDC minimum cut
		var checked atomic.Int64
		withTestHook(t, func(p string) {
			switch p {
			case hookSegBeforeWrite, hookSegAfterWrite, hookSegBeforeSync, hookSegAfterSync:
				fb03Absent(t, s, sum, p)
				checked.Add(1)
			}
		})
		_, err = s.PutObject("b", "k", body, "", nil)
		mustOK(t, err)
		testHook = nil
		if checked.Load() < 4 {
			t.Fatalf("only %d segment points checked", checked.Load())
		}
		if _, err := s.casStat(sum); err != nil {
			t.Fatalf("after the group commit: %v", err)
		}
	})
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("loose/publisher-fails=%v", fail), func(t *testing.T) {
			s, err := OpenStore(t.TempDir())
			mustOK(t, err)
			defer s.Close()
			data := genRandomBytes(9003, 5000)
			sum := sha256.Sum256(data)
			type res struct {
				err error
				at  time.Time
			}
			got := make(chan res, 1)
			var released time.Time
			withTestHook(t, func(p string) {
				switch p {
				case hookCASAfterFirstRename:
					// the file is at its final path; its directory is not durable
					if _, err := os.Stat(s.chunkPath(sum)); err != nil {
						t.Errorf("renamed file: %v", err)
					}
					go func() {
						_, err := s.casStat(sum)
						got <- res{err, time.Now()}
					}()
					select {
					case r := <-got:
						t.Errorf("casStat returned %v while the publication was in flight", r.err)
						got <- r
					case <-time.After(100 * time.Millisecond):
					}
				case hookCASBeforeDirSync:
					released = time.Now()
					if fail {
						panic(simulatedCrash{point: p})
					}
				}
			})
			if fail {
				runExpectingSimulatedCrash(t, func() { s.casWrite(data) })
			} else {
				_, err := s.casWrite(data)
				mustOK(t, err)
			}
			testHook = nil
			var r res
			select {
			case r = <-got:
			case <-time.After(10 * time.Second):
				t.Fatal("casStat never returned")
			}
			if r.at.Before(released) {
				t.Fatalf("casStat returned before the publisher finished")
			}
			if fail {
				if !os.IsNotExist(r.err) {
					t.Fatalf("a failed publication was reported present: %v", r.err)
				}
				if _, err := os.Stat(s.chunkPath(sum)); !os.IsNotExist(err) {
					t.Fatalf("failed publication left its file: %v", err)
				}
			} else if r.err != nil {
				t.Fatalf("a completed publication was reported absent: %v", r.err)
			}
		})
	}
}

// No dedup hit on a chunk whose pack is retired: once a pass has taken a pack
// out of the locator its dead records are absent although the file is still
// on disk, and a re-put of that content writes a new copy.
func TestFB03_RetiredPackIsAbsent(t *testing.T) {
	defer func(v int64) { b4FixturePackBytes = v }(b4FixturePackBytes)
	b4FixturePackBytes = 256 << 10
	dir, want := b4Fixture(t, 12, 60<<10)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	dead := map[[32]byte]bool{}
	for i := range 6 {
		k := fmt.Sprintf("k%03d", i)
		for _, sum := range fb03ChunkSums(t, s, k) {
			dead[sum] = true
		}
		mustOK(t, s.DeleteObject("b", k))
		delete(want, k)
	}
	for k := range want {
		for _, sum := range fb03ChunkSums(t, s, k) {
			delete(dead, sum)
		}
	}
	b4Prune(t, s)
	var checked, stillOnDisk int
	withTestHook(t, func(p string) {
		if p != hookMaintBeforePackUnlink || checked > 0 {
			return
		}
		checked++
		for sum := range dead {
			fb03Absent(t, s, sum, p)
		}
		st := s.packSnap()
		for _, e := range s.retire.m {
			if e.swapped && fileExists(e.path) {
				stillOnDisk++
			}
		}
		_ = st
	})
	rep, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	testHook = nil
	if checked == 0 || rep.PacksRetired == 0 || stillOnDisk == 0 {
		t.Fatalf("no pack reached the unlink point (checked %d, retired %d, on disk %d)", checked, rep.PacksRetired, stillOnDisk)
	}
	for sum := range dead {
		fb03Absent(t, s, sum, "after the pass")
	}
	b4Check(t, s, want)
	// the dead content stored again is a new durable copy
	_, err = s.PutObject("b", "again", b4Body(0, 60<<10), "", nil)
	mustOK(t, err)
	_, got, err := s.GetObject("b", "again")
	if err != nil || !bytes.Equal(got, b4Body(0, 60<<10)) {
		t.Fatalf("re-put of retired content: %v", err)
	}
	want["again"] = got
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}

// G-LINEAR and the race detector: PUT, overwrite, DELETE and GET of a few
// keys through every sink (segments, direct packs, loose remainders) while
// online passes retire packs and loose files, with content shared between
// keys and with loose chunks of a dead seed generation. Each key's history
// must be linearizable, every GET returns bytes some PUT wrote, and the
// store reopens and deep-verifies.
func TestFB03_ConcurrentSinksLinearizable(t *testing.T) {
	defer func(v int64) { segTargetBytes = v }(segTargetBytes)
	segTargetBytes = 256 << 10
	dir := t.TempDir()
	sizes := []int{9_000, 40_000, 120_000, 300 << 10, 700 << 10}
	pool := make([][]byte, 16)
	for i := range pool {
		pool[i] = b4Body(7000+i, sizes[i%len(sizes)])
	}
	t.Run("seed-loose", func(t *testing.T) {
		useLooseLayout(t, "FB-03: a dead loose generation that the workload dedups against")
		s, err := OpenStore(dir)
		mustOK(t, err)
		mustOK(t, s.CreateBucket("b"))
		for i, b := range pool {
			_, err := s.PutObject("b", fmt.Sprintf("seed%02d", i), b, "", nil)
			mustOK(t, err)
		}
		mustOK(t, s.Close())
	})
	useProductionLayout(t)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	for i := range pool {
		mustOK(t, s.DeleteObject("b", fmt.Sprintf("seed%02d", i)))
	}
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	hexOf := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	keys := []string{"a", "b", "c", "d", "e"}
	rec := newLinRecorder()
	var failures, ops atomic.Int64
	fail := func(err error) {
		if failures.Add(1) < 5 {
			t.Error(err)
		}
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 0xfb03))
			for i := 0; i < 30; i++ {
				key := keys[rng.IntN(len(keys))]
				var body []byte
				del := rng.IntN(6) == 0
				switch {
				case del:
				case rng.IntN(3) == 0: // fresh content
					body = b4Body(20000+w*1000+i, sizes[rng.IntN(len(sizes))])
				default: // content of the dead seed generation, or of another key
					body = pool[rng.IntN(len(pool))]
				}
				method, val := http.MethodPut, hexOf(body)
				if del {
					method, val, body = http.MethodDelete, "", nil
				}
				call := rec.now()
				resp := doSignedRequest(t, client, ts.URL, signer, method, "/b/"+key, body, nil)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				ret := rec.now()
				ops.Add(1)
				if resp.StatusCode/100 != 2 {
					fail(fmt.Errorf("%s %s: %d", method, key, resp.StatusCode))
					continue
				}
				rec.add(key, linOp{call: call, ret: ret, write: true, val: val, label: fmt.Sprintf("w%d.%d %s", w, i, method)})
			}
		}()
	}
	for r := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(r), 0x3bf0))
			for i := 0; i < 40; i++ {
				key := keys[rng.IntN(len(keys))]
				call := rec.now()
				resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/"+key, nil, nil)
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				ret := rec.now()
				ops.Add(1)
				val := ""
				switch {
				case err != nil:
					fail(fmt.Errorf("GET %s: %v", key, err))
					continue
				case resp.StatusCode == http.StatusOK:
					val = hexOf(body)
				case resp.StatusCode == http.StatusNotFound:
				default:
					fail(fmt.Errorf("GET %s: %d %s", key, resp.StatusCode, body))
					continue
				}
				rec.add(key, linOp{call: call, ret: ret, val: val, label: fmt.Sprintf("r%d.%d", r, i)})
			}
		}()
	}
	var passes, retired, reclaimed, carried int64
	passErr := make(chan error, 1)
	go func() {
		defer close(passErr)
		opt := b4Opts()
		opt.TargetBytes = 512 << 10
		for {
			select {
			case <-stop:
				return
			default:
			}
			one := 1
			res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &one})
			if err == nil {
				err = s.ApplyHistoryPrune(&res)
			}
			if err == nil {
				var rep MaintReport
				rep, err = s.maintainOnline(context.Background(), opt)
				passes++
				retired += int64(rep.PacksRetired)
				reclaimed += rep.BytesReclaimed
				carried += int64(rep.LateRecords + rep.RevivedRecords + rep.RetainedPinned)
			}
			if err != nil && !errors.Is(err, errMaintBusy) {
				passErr <- err
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	mustOK(t, <-passErr)
	if failures.Load() > 0 {
		t.Fatalf("%d failed requests", failures.Load())
	}
	t.Logf("FB03_LINEAR ops=%d passes=%d retired=%d reclaimed=%d late_revived_pinned=%d", ops.Load(), passes, retired, reclaimed, carried)
	if passes == 0 || retired+reclaimed == 0 {
		t.Fatalf("maintenance did no work (%d passes)", passes)
	}
	for _, err := range rec.check(nil) {
		t.Error(err)
	}
	final := map[string][]byte{}
	for _, k := range keys {
		_, body, err := s.GetObject("b", k)
		if errors.Is(err, errNoSuchKey) {
			continue
		}
		mustOK(t, err)
		final[k] = body
	}
	mustOK(t, s.Close())
	b4Verify(t, dir, final)
}
