package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Z4-P1C crash qualification of the segment layer.
//
//   - TestP1C_CrashMatrix_Segments: process death (panic at the Nth firing of a
//     durability boundary; afterwards every goroutine dies at its next hook).
//     The crashed process's files are copied and recovered.
//   - TestP1C_CrashStateModel: power-loss states. At each boundary the store
//     directory is snapshotted while the workload keeps running; the snapshot
//     is then degraded the ways a power cut can degrade it (volatile suffix of
//     a file dropped, torn, left with a zero-filled hole in front of later
//     bytes, a new file or directory entry missing) and recovered. This models
//     which bytes may be missing at each barrier; it is not a block-device fault
//     test (none is available in this environment).
//   - Real-process SIGKILL rounds: TestP1B_RealKill_* (segments-small-objects).

func waitSegIdle(t *testing.T, s *Store) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.segs.mu.Lock()
		idle := len(s.segs.queue) == 0 && !s.segs.leading && s.segs.sealing == 0
		s.segs.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("segment writer stuck after a crash: queue=%d leading=%v sealing=%d", len(s.segs.queue), s.segs.leading, s.segs.sealing)
		}
		time.Sleep(time.Millisecond)
	}
}

var p1cSegPoints = []string{
	hookSegCreated, hookSegDirSynced, hookSegBeforeWrite, hookSegAfterWrite, hookSegBeforeSync, hookSegAfterSync,
	hookSegPublished, hookSegSealWritten, hookSegSealSynced, hookSegBeforeRename, hookSegAfterRename, hookSegSealed,
	hookAfterManifestPublished, hookAfterJournalWriteBeforeSync, hookAfterJournalSync, hookAfterApplyBeforeResponse,
}

func TestP1C_CrashMatrix_Segments(t *testing.T) {
	useProductionLayout(t)
	reached, total := 0, 0
	unreached := map[string]int{}
	for _, group := range []int{1, 6, 16} {
		for _, point := range p1cSegPoints {
			for _, nth := range []int{1, 2, 5} {
				total++
				t.Run(fmt.Sprintf("g%d/%s/n%d", group, point, nth), func(t *testing.T) {
					if segCrashCase(t, group, point, nth) {
						reached++
					} else {
						unreached[point]++
					}
				})
			}
		}
	}
	t.Logf("crash injected in %d of %d cases; boundary fired fewer than n times (clean run, still verified): %v", reached, total, unreached)
	if reached < total/2 {
		t.Fatalf("only %d of %d cases injected a crash", reached, total)
	}
}

func segCrashCase(t *testing.T, group int, point string, nth int) (reached bool) {
	setSegTarget(t, 40<<10, 12<<10) // frequent rollovers so the seal boundaries fire
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateBucket("b")
	pre := p1cPutN(t, s, "pre", 4, 9000) // acknowledged before the crash window
	bodies := map[string][]byte{}
	shared := p1cBody(9999, 25_000)
	for i := 0; i < group; i++ {
		bodies[fmt.Sprintf("uniq%d", i)] = p1cBody(2000+i, 6000+i*37)
		bodies[fmt.Sprintf("dup%d", i)] = shared
	}
	var fired atomic.Int32
	var crashed atomic.Bool
	withTestHook(t, func(p string) {
		if crashed.Load() {
			panic(simulatedCrash{point: p})
		}
		if p == point && fired.Add(1) == int32(nth) {
			crashed.Store(true)
			panic(simulatedCrash{point: p})
		}
	})
	var mu sync.Mutex
	ackedKeys := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ { // a sequential writer: more boundary firings and rollovers
		bodies[fmt.Sprintf("seq%d", i)] = p1cBody(3000+i, 6000)
	}
	for k, body := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			if _, err := s.PutObject("b", k, body, "", nil); err == nil {
				mu.Lock()
				ackedKeys[k] = true
				mu.Unlock()
			}
		}()
	}
	withTimeout(t, 60*time.Second, "crashed group", wg.Wait)
	reached = crashed.Load()
	waitSegIdle(t, s) // no stuck leader, queue entry or seal goroutine
	testHook = nil
	snap := t.TempDir()
	copyTree(t, dir, snap) // the crashed process's files, as they are
	s.Close()

	r, err := OpenStore(snap)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	check := func(stage string) {
		t.Helper()
		p1cCheck(t, r, pre)
		for k, body := range bodies {
			_, got, err := r.GetObject("b", k)
			switch {
			case err == nil && !bytes.Equal(got, body):
				t.Fatalf("%s: %s is visible with wrong bytes", stage, k)
			case err != nil && !errors.Is(err, errNoSuchKey):
				t.Fatalf("%s: %s: %v", stage, k, err)
			case err != nil && ackedKeys[k]:
				t.Fatalf("%s: ACKNOWLEDGED %s is missing after the crash", stage, k)
			}
		}
	}
	check("after crash")
	p1cVerify(t, r)
	r.Close()
	if res, err := gcCollect(snap, true); err != nil || !res.LiveSetOK {
		t.Fatalf("gc: %v %+v", err, res)
	}
	r2, err := OpenStore(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	r = r2
	check("after gc")
	for k, body := range bodies { // every unacknowledged PUT is retryable
		if _, _, err := r.GetObject("b", k); err == nil {
			continue
		}
		if _, err := r.PutObject("b", k, body, "", nil); err != nil {
			t.Fatalf("retry of %s: %v", k, err)
		}
		if _, got, err := r.GetObject("b", k); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("retried %s unreadable: %v", k, err)
		}
	}
	p1cVerify(t, r)
	return reached
}

// copyStoreLive copies a store that is being written to: everything else first,
// then segments/, then packs/, skipping files that vanish. A seal links the pack name
// before it removes the segment name, so this order never captures a state in
// which neither exists (no crash can produce that either).
func copyStoreLive(t *testing.T, src, dst string) {
	t.Helper()
	copyOne := func(root string) {
		filepath.WalkDir(filepath.Join(src, root), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(src, p)
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil // vanished meanwhile
			}
			os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		})
	}
	// The journal (and everything else) is copied first: a journal older than the
	// segment data is a legitimate crash state (frames not yet written), a journal
	// newer than the data it names is not.
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		if e.Name() != segDirName && e.Name() != "packs" {
			copyOne(e.Name())
		}
	}
	copyOne(segDirName)
	copyOne("packs")
}

// --- power-loss state model -----------------------------------------------------------

type crashSnapshot struct {
	dir     string
	acked   map[string][]byte // acknowledged before the snapshot started
	active  string            // active segment path and its durable size at that instant
	durable int64
}

// footerDataEnd returns the end of the record area of a segment that carries a
// (possibly unsynced) pack footer, or -1.
func footerDataEnd(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < packHeaderSize+packFooterSize {
		return -1
	}
	ft := b[len(b)-packFooterSize:]
	if string(ft[:4]) != packFooterMagic {
		return -1
	}
	off := binary.LittleEndian.Uint64(ft[16:24])
	if off < packHeaderSize || off > uint64(len(b)) {
		return -1
	}
	return int64(off)
}

func TestP1C_CrashStateModel(t *testing.T) {
	useProductionLayout(t)
	type variant struct {
		name  string
		apply func(t *testing.T, dir string, snap crashSnapshot)
	}
	mutateSegs := func(name string, f func(path string, size, durable int64) []byte) variant {
		return variant{name, func(t *testing.T, dir string, snap crashSnapshot) {
			files, _ := filepath.Glob(filepath.Join(dir, segDirName, "*"+segFileSuffix))
			for _, p := range files {
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				d := int64(-1)
				if de := footerDataEnd(p); de >= 0 {
					d = de
				} else if filepath.Base(p) == filepath.Base(snap.active) {
					d = snap.durable
				}
				if d < 0 || d >= int64(len(b)) {
					continue // nothing volatile in this file
				}
				if nb := f(p, int64(len(b)), d); nb != nil {
					if err := os.WriteFile(p, append(b[:d:d], nb...), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				_ = b
			}
		}}
	}
	variants := []variant{
		{"as-written", func(*testing.T, string, crashSnapshot) {}},
		mutateSegs("volatile-dropped", func(p string, size, d int64) []byte { return []byte{} }),
		{"volatile-torn-midway", func(t *testing.T, dir string, snap crashSnapshot) {
			files, _ := filepath.Glob(filepath.Join(dir, segDirName, "*"+segFileSuffix))
			for _, p := range files {
				b, _ := os.ReadFile(p)
				d := int64(-1)
				if de := footerDataEnd(p); de >= 0 {
					d = de
				} else if filepath.Base(p) == filepath.Base(snap.active) {
					d = snap.durable
				}
				if d >= 0 && d < int64(len(b)) {
					os.WriteFile(p, b[:d+(int64(len(b))-d)/2+1], 0o644)
				}
			}
		}},
		{"zero-hole-before-later-bytes", func(t *testing.T, dir string, snap crashSnapshot) {
			files, _ := filepath.Glob(filepath.Join(dir, segDirName, "*"+segFileSuffix))
			for _, p := range files {
				b, _ := os.ReadFile(p)
				d := int64(-1)
				if de := footerDataEnd(p); de >= 0 {
					d = de
				} else if filepath.Base(p) == filepath.Base(snap.active) {
					d = snap.durable
				}
				if d >= 0 && d < int64(len(b)) {
					n := (int64(len(b)) - d) / 2
					if n < 1 {
						n = 1
					}
					copy(b[d:d+n], make([]byte, n))
					os.WriteFile(p, b, 0o644)
				}
			}
		}},
		{"new-segment-dirent-lost", func(t *testing.T, dir string, snap crashSnapshot) {
			// a segment created but whose directory entry was not yet durable
			files, _ := filepath.Glob(filepath.Join(dir, segDirName, "*"+segFileSuffix))
			sort.Strings(files)
			if len(files) > 0 {
				if fi, _ := os.Stat(files[len(files)-1]); fi != nil && fi.Size() <= packHeaderSize {
					os.Remove(files[len(files)-1])
				}
			}
		}},
		{"seal-link-not-durable", func(t *testing.T, dir string, snap crashSnapshot) {
			// the new pack name was not persisted although the segment file already carried its footer
			segs, _ := filepath.Glob(filepath.Join(dir, segDirName, "*"+segFileSuffix))
			for _, sp := range segs {
				if footerDataEnd(sp) < 0 {
					continue
				}
				if info, _, err := statPackFile(sp); err == nil {
					os.Remove(filepath.Join(dir, "packs", info.id+packFileSuffix))
				}
			}
		}},
		{"segment-name-resurrected-after-seal", func(t *testing.T, dir string, snap crashSnapshot) {
			// the unlink of the segment name was not persisted: the sealed bytes are back under both names
			packs, _ := filepath.Glob(filepath.Join(dir, "packs", "*"+packFileSuffix))
			sort.Strings(packs)
			if len(packs) > 0 {
				b, _ := os.ReadFile(packs[len(packs)-1])
				os.WriteFile(filepath.Join(dir, segDirName, "00000000000000e9"+segFileSuffix), b, 0o644)
			}
		}},
	}
	snapshotted, recovered := 0, 0
	for _, group := range []int{1, 6} {
		for _, point := range p1cSegPoints[:12] {
			for _, nth := range []int{1, 3} {
				name := fmt.Sprintf("g%d/%s/n%d", group, point, nth)
				t.Run(name, func(t *testing.T) {
					setSegTarget(t, 40<<10, 12<<10)
					dir := t.TempDir()
					s, err := OpenStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					s.CreateBucket("b")
					bodies := map[string][]byte{}
					var mu sync.Mutex
					acked := map[string][]byte{}
					put := func(k string, body []byte) {
						bodies[k] = body
					}
					for i := 0; i < 3; i++ {
						k := fmt.Sprintf("pre%d", i)
						b := p1cBody(100+i, 9000)
						put(k, b)
						if _, err := s.PutObject("b", k, b, "", nil); err != nil {
							t.Fatal(err)
						}
						acked[k] = b
					}
					for i := 0; i < group*3; i++ {
						put(fmt.Sprintf("w%02d", i), p1cBody(500+i, 5000+i*91))
					}
					var snap *crashSnapshot
					var fired atomic.Int32
					var snapMu sync.Mutex
					withTestHook(t, func(p string) {
						if p != point || fired.Add(1) != int32(nth) {
							return
						}
						snapMu.Lock()
						defer snapMu.Unlock()
						mu.Lock()
						a := map[string][]byte{}
						for k, v := range acked {
							a[k] = v
						}
						mu.Unlock()
						cs := &crashSnapshot{dir: t.TempDir(), acked: a}
						s.segs.mu.Lock()
						act := s.segs.active
						s.segs.mu.Unlock()
						if act != nil {
							cs.active, cs.durable = act.path, s.segs.activeBytes.Load()
						}
						copyStoreLive(t, dir, cs.dir)
						snap = cs
					})
					var wg sync.WaitGroup
					keys := make([]string, 0)
					for k := range bodies {
						if !strings.HasPrefix(k, "pre") {
							keys = append(keys, k)
						}
					}
					for _, k := range keys {
						wg.Add(1)
						go func() {
							defer wg.Done()
							if _, err := s.PutObject("b", k, bodies[k], "", nil); err == nil {
								mu.Lock()
								acked[k] = bodies[k]
								mu.Unlock()
							}
						}()
					}
					withTimeout(t, 60*time.Second, "workload", wg.Wait)
					waitSegIdle(t, s)
					testHook = nil
					s.sealSegments() // exercise the maintenance seal as well when the point was not reached by rollovers
					s.Close()
					if snap == nil {
						return // the boundary did not fire that many times
					}
					snapshotted++
					for _, v := range variants {
						vdir := t.TempDir()
						copyTree(t, snap.dir, vdir)
						v.apply(t, vdir, *snap)
						r, err := OpenStore(vdir)
						if err != nil {
							t.Fatalf("%s: recovery failed: %v", v.name, err)
						}
						for k, body := range snap.acked {
							if _, got, err := r.GetObject("b", k); err != nil || !bytes.Equal(got, body) {
								t.Fatalf("%s: ACKNOWLEDGED %s lost or damaged: %v", v.name, k, err)
							}
						}
						// nothing visible may be unreadable or wrong
						for k, body := range bodies {
							_, got, err := r.GetObject("b", k)
							if err == nil && !bytes.Equal(got, body) {
								t.Fatalf("%s: %s visible with wrong bytes", v.name, k)
							}
							if err != nil && !errors.Is(err, errNoSuchKey) {
								t.Fatalf("%s: %s is visible but unreadable: %v", v.name, k, err)
							}
						}
						if vr, err := r.Verify(true); err != nil || !vr.OK() {
							t.Fatalf("%s: verify -deep: %v %+v", v.name, err, vr)
						}
						if _, err := r.PutObject("b", "post-recovery", p1cBody(7, 3000), "", nil); err != nil {
							t.Fatalf("%s: store not writable after recovery: %v", v.name, err)
						}
						r.Close()
						r2, err := OpenStore(vdir) // recovery is idempotent
						if err != nil {
							t.Fatalf("%s: second recovery: %v", v.name, err)
						}
						p1cCheck(t, r2, snap.acked)
						r2.Close()
						recovered++
					}
				})
			}
		}
	}
	t.Logf("%d boundary snapshots, %d degraded states recovered with every invariant", snapshotted, recovered)
	if snapshotted < 20 {
		t.Fatalf("only %d snapshots taken: the model is not exercising the boundaries", snapshotted)
	}
}
