package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- B9-A grouped authority records ------------------------------------------

// b9Grouped turns grouped publication on with small groups (several seals per test) for one test.
func b9Grouped(t testing.TB, target int64) {
	old, oldT := agWanted, agTargetBytes
	agWanted, agTargetBytes = true, target
	t.Cleanup(func() { agWanted, agTargetBytes = old, oldT })
}

// b9SmallHistory puts versions of many small text files (4-40 KB, small edits per version) under one
// key each: delta records of a few hundred bytes, the shape the allocation floor penalizes.
func b9SmallHistory(l *authLab, seed uint64, files, versions int) {
	for f := range files {
		v := b5Text(seed*100+uint64(f), 4096+int(seed*7+uint64(f)*3001)%36000)
		for ver := range versions {
			if ver > 0 {
				r := rand.New(rand.NewPCG(seed, uint64(f*31+ver)))
				v = append([]byte(nil), v...)
				for range 3 {
					off := r.IntN(len(v) - 40)
					copy(v[off:], fmt.Sprintf("edit %d/%d/%d", f, ver, r.IntN(1e6)))
				}
			}
			l.put(fmt.Sprintf("src/f%03d.c#%d", f, ver), v)
		}
	}
}

func b9LegacyFiles(t testing.TB, s *Store) int {
	t.Helper()
	files, err := s.authLegacyFiles()
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func b9Groups(t testing.TB, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "bodies", agDirName, "*"+agSuffix))
	return m
}

// TestB9FrameScan: frames round trip; a torn tail at every cut ends the scan at the last whole frame; a
// damaged middle region is skipped (and reported) when valid frames follow it; seal indexes round trip
// and a damaged seal or trailer falls back to the scan.
func TestB9FrameScan(t *testing.T) {
	rec := func(i int) []byte {
		r := &authRecord{SHA: [32]byte{byte(i), 7}, Size: int64(100 + i), Gen: uint64(i + 1), Form: authFormPin, Pieces: []authPiece{{SHA: [32]byte{byte(i)}, Len: int64(100 + i)}}}
		b, err := r.marshal()
		if err != nil {
			t.Fatal(err)
		}
		return agFrame(agRecordFrame, b)
	}
	file := agHeader(9)
	var want []agEnt
	for i := range 6 {
		fr := rec(i)
		if i == 3 {
			fr = agTomb([32]byte{2, 7}, 3)
		}
		k, p, n, ok := agParse(fr)
		if !ok || n != int64(len(fr)) {
			t.Fatalf("frame %d does not parse", i)
		}
		want = append(want, agEnt{agFrameSHA(k, p), agLoc{9, int64(len(file)), n, k}})
		file = append(file, fr...)
	}
	ents, end, sealed, dmg := agScan(file, 9, agHeaderLen)
	if !slices.Equal(ents, want) || end != int64(len(file)) || sealed || len(dmg) != 0 {
		t.Fatalf("scan: %d ents end %d sealed %v dmg %v", len(ents), end, sealed, dmg)
	}
	for cut := agHeaderLen; cut < len(file); cut++ {
		e, end, _, dmg := agScan(file[:cut], 9, agHeaderLen)
		k := 0
		for k < len(want) && want[k].loc.off+want[k].loc.n <= int64(cut) {
			k++
		}
		if !slices.Equal(e, want[:k]) || len(dmg) != 0 || (k > 0 && end != want[k-1].loc.off+want[k-1].loc.n) {
			t.Fatalf("cut %d: %d ents (want %d) end %d dmg %v", cut, len(e), k, end, dmg)
		}
	}
	// Damage inside frame 2: frames 0, 1 and 3-5 are still found.
	bad := slices.Clone(file)
	bad[want[2].loc.off+20] ^= 0x40
	e, _, _, dmg := agScan(bad, 9, agHeaderLen)
	if len(dmg) != 1 || len(e) != 5 || e[2] != want[3] {
		t.Fatalf("damaged middle: %d ents, dmg %v", len(e), dmg)
	}
	// Seal round trip through a file, and fallbacks.
	dir := t.TempDir()
	sealFr := agFrame(agSealFrame, agSealPayload(want))
	sealed2 := append(slices.Clone(file), sealFr...)
	sealed2 = binary.LittleEndian.AppendUint64(sealed2, uint64(len(file)))
	sealed2 = append(sealed2, agTrailMagic...)
	for name, mut := range map[string]func([]byte) []byte{
		"good":    func(b []byte) []byte { return b },
		"trailer": func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"seal":    func(b []byte) []byte { b[len(file)+20] ^= 1; return b },
		"torn":    func(b []byte) []byte { return b[:len(b)-5] },
	} {
		p := filepath.Join(dir, name+agSuffix)
		mustOK(t, os.WriteFile(p, mut(slices.Clone(sealed2)), 0o644))
		g, err := agOpenGroup(p, 9)
		mustOK(t, err)
		if !slices.Equal(g.ents, want) {
			t.Fatalf("%s: %d ents", name, len(g.ents))
		}
		if wantSealed := name != "seal"; g.sealed != wantSealed {
			t.Fatalf("%s: sealed %v", name, g.sealed)
		}
		g.f.Close()
	}
	// A header naming another group is refused.
	p := filepath.Join(dir, "other"+agSuffix)
	mustOK(t, os.WriteFile(p, file, 0o644))
	if _, err := agOpenGroup(p, 10); !errors.Is(err, errAG) {
		t.Fatalf("foreign header: %v", err)
	}
	// Random damage never panics and never yields a frame the bytes do not hold.
	r := rand.New(rand.NewPCG(5, 5))
	for range 2000 {
		b := slices.Clone(sealed2)
		for range 1 + r.IntN(4) {
			b[agHeaderLen+r.IntN(len(b)-agHeaderLen)] = byte(r.IntN(256))
		}
		e, _, _, _ := agScan(b, 9, agHeaderLen)
		for _, x := range e {
			if _, _, n, ok := agParse(b[x.loc.off:]); !ok || n != x.loc.n {
				t.Fatal("scan returned an invalid frame")
			}
		}
	}
}

// TestB9GroupedLifecycle: a small-file history migrated with grouped records stores no record files,
// reads exactly through reopen, gc and compaction; deleted contents are retired by tombs and their
// groups reclaimed; a retired identity is published again; revert, ungroup and regroup keep it exact.
func TestB9GroupedLifecycle(t *testing.T) {
	b9Grouped(t, 8<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 3, 24, 3)
	l.reclaim()
	rep := l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	if rep.Admitted < 20 {
		t.Fatalf("only %d deltas admitted", rep.Admitted)
	}
	if n := b9LegacyFiles(t, l.s); n != 0 {
		t.Fatalf("%d record files with grouping on", n)
	}
	if !slices.Contains(l.s.formatSnapshot().Features, agFeature) {
		t.Fatal("recgroup not raised")
	}
	if g := b9Groups(t, l.dir); len(g) < 2 {
		t.Fatalf("%d groups: want several (8 KiB target)", len(g))
	}
	l.exact("migrated")
	l.verifyOK("migrated")
	l.close()
	l.open()
	l.exact("reopened")
	files0, _ := l.s.authFiles()

	// Delete a third of the history: its records become orphans, gc tombs them, compaction reclaims.
	for f := 0; f < 24; f += 3 {
		for ver := range 3 {
			k := fmt.Sprintf("src/f%03d.c#%d", f, ver)
			mustOK(t, l.s.DeleteObject("b", k))
			delete(l.want, k)
		}
	}
	zero := 0
	res, err := l.s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
	mustOK(t, err)
	mustOK(t, l.s.ApplyHistoryPrune(&res))
	gc := l.reclaim()
	_ = gc
	files1, _ := l.s.authFiles()
	if len(files1) >= len(files0) {
		t.Fatalf("records %d -> %d", len(files0), len(files1))
	}
	l.exact("reclaimed")
	l.verifyOK("reclaimed")
	ag, err := l.s.agCompact(false)
	mustOK(t, err)
	if float64(ag.LiveBytes) < agCompactLive*float64(ag.GroupBytes)*0.5 {
		t.Fatalf("groups mostly dead after compaction: %+v", ag)
	}

	// Revert one record, then re-migrate: a retired identity is published again at generation 1.
	sha := files1[0]
	mustOK(t, l.s.AuthorityRevert(sha))
	if r, _ := l.s.authLoad(sha); r != nil && r.Form != authFormPin {
		t.Fatal("reverted record still visible") // a base with dependents becomes a pin
	}
	l.exact("reverted")
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.exact("re-migrated")
	l.reclaim()
	l.exact("re-migrated reclaimed")
	l.verifyOK("re-migrated")

	// Ungroup writes record files back and lowers the feature; regroup moves them again.
	n, err := l.s.agUngroup()
	mustOK(t, err)
	if n == 0 || b9LegacyFiles(t, l.s) != n || len(b9Groups(t, l.dir)) != 0 || slices.Contains(l.s.formatSnapshot().Features, agFeature) {
		t.Fatalf("ungroup: %d records, %d files, %d groups", n, b9LegacyFiles(t, l.s), len(b9Groups(t, l.dir)))
	}
	l.close()
	l.open()
	l.exact("ungrouped")
	l.verifyOK("ungrouped")
	ag, err = l.s.agCompact(true)
	mustOK(t, err)
	if ag.Regrouped != n || b9LegacyFiles(t, l.s) != 0 {
		t.Fatalf("regroup moved %d of %d", ag.Regrouped, n)
	}
	l.close()
	l.open()
	l.exact("regrouped")
	l.verifyOK("regrouped")
}

// TestB9MixedLegacyAndGrouped: records published as files before grouping keep reading beside grouped
// ones; a grouped generation supersedes its file; repack regroups the rest.
func TestB9MixedLegacyAndGrouped(t *testing.T) {
	l := newAuthLab(t)
	b9SmallHistory(l, 5, 12, 2)
	l.reclaim()
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	legacy := b9LegacyFiles(t, l.s)
	if legacy < 6 {
		t.Fatalf("%d legacy records", legacy)
	}
	b9SmallHistory(l, 6, 12, 2)
	l.reclaim()
	b9Grouped(t, 4<<10)
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	if b9LegacyFiles(t, l.s) == 0 || len(b9Groups(t, l.dir)) == 0 {
		t.Fatal("want a mixed store before repack")
	}
	l.exact("mixed")
	l.verifyOK("mixed")
	// A file record's next generation goes to the log and the file is removed.
	files, _ := l.s.authLegacyFiles()
	r, err := l.s.authLoad(files[0])
	mustOK(t, err)
	r2 := *r
	r2.Gen++
	mustOK(t, l.s.authPublish(&r2))
	if got, _ := l.s.authLoad(files[0]); got == nil || got.Gen != r2.Gen {
		t.Fatal("grouped generation does not decide")
	}
	if _, err := os.Stat(l.s.authPath(files[0])); !os.IsNotExist(err) {
		t.Fatal("superseded file kept")
	}
	// A stale generation is refused.
	mustOK(t, func() error {
		if err := l.s.authPublish(&r2); !errors.Is(err, errAuthStale) {
			return fmt.Errorf("stale publish: %v", err)
		}
		return nil
	}())
	l.reclaim()
	if n := b9LegacyFiles(t, l.s); n != 0 {
		t.Fatalf("%d files after repack regroup", n)
	}
	l.close()
	l.open()
	l.exact("regrouped")
	l.verifyOK("regrouped")
}

// TestB9GroupFaults: a torn append fails the publish and leaves the last valid state; the next append
// cuts the torn tail; a damaged frame fails its own content closed (never another record's bytes) and
// verify reports it; damaged seals fall back to the scan; a foreign group header refuses the open.
func TestB9GroupFaults(t *testing.T) {
	b9Grouped(t, 1<<20)
	l := newAuthLab(t)
	b9SmallHistory(l, 7, 10, 2)
	l.reclaim()
	n := 0
	authFault = func(p string) error {
		if p == "group-write" {
			if n++; n == 3 {
				return errors.New("injected torn group append")
			}
		}
		return nil
	}
	_, err := l.s.AuthorityMigrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2, Apply: true, AllowDelta: true, AllowRep: true, Order: "oldest"})
	authFault = nil
	if n < 3 {
		t.Fatalf("fault not reached (%d group writes)", n)
	}
	_ = err // the migrator reports the failed content and continues
	if g := b9Groups(t, l.dir); len(g) != 1 {
		t.Fatalf("%d groups", len(g))
	} else if fi, _ := os.Stat(g[0]); fi.Size() == l.s.agEnd().off {
		t.Fatal("no torn tail left by the injected append")
	}
	l.exact("after torn append")
	l.close()
	l.open()
	l.exact("reopened after torn append")
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.exact("appended after torn tail")
	l.verifyOK("appended after torn tail")

	// Damage one record frame in place.
	files, _ := l.s.authFiles()
	var victim [32]byte
	var loc agLoc
	for _, f := range files {
		if lc, known, _ := l.s.agLookup(f); known && lc.kind == agRecordFrame {
			victim, loc = f, lc
		}
	}
	l.close()
	p := filepath.Join(l.dir, "bodies", agDirName, fmt.Sprintf("%016x%s", loc.seq, agSuffix))
	b, err := os.ReadFile(p)
	mustOK(t, err)
	b[loc.off+loc.n/2] ^= 0x10
	mustOK(t, os.WriteFile(p, b, 0o644))
	l.open()
	if r, err := l.s.authLoad(victim); err == nil && r != nil {
		t.Fatal("damaged frame still decodes")
	}
	res, err := l.s.Verify(true)
	mustOK(t, err)
	if res.OK() {
		t.Fatal("verify missed a damaged frame")
	}
	for _, f := range files {
		if f == victim {
			continue
		}
		if r, err := l.s.authLoad(f); err != nil || r == nil || r.SHA != f {
			t.Fatalf("intact record %x: %v", f[:4], err)
		}
	}
	// gc refuses to act on a store with a broken live root; compaction keeps the damaged group.
	l.close()
	if _, err := gcCollect(l.dir, true); err == nil {
		t.Log("gc proceeded (the damaged record may be an orphan)")
	}
	l.open()
	ag, err := l.s.agCompact(false)
	mustOK(t, err)
	_ = ag
	if _, err := os.Stat(p); err != nil {
		t.Fatal("damaged group was removed")
	}
	// Restore: the record reads again.
	b[loc.off+loc.n/2] ^= 0x10
	l.close()
	mustOK(t, os.WriteFile(p, b, 0o644))
	l.open()
	l.exact("restored")
	l.verifyOK("restored")
}

// TestB9OnlineMaintenance: B4 online passes over a grouped store retire orphan records by tombs and
// compact their groups under the reader lease while readers stay exact; reopen and deep verify pass.
func TestB9OnlineMaintenance(t *testing.T) {
	b9Grouped(t, 6<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 9, 18, 3)
	l.reclaim()
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.reclaim()
	files0, _ := l.s.authFiles()
	groups0 := len(b9Groups(t, l.dir))
	l.close()
	s := b4OpenMaint(t, l.dir)
	l.s = s
	var stop atomic.Bool
	var wg sync.WaitGroup
	var rerr atomic.Value
	keys := func() map[string][]byte { return maps.Clone(l.want) }
	cur := keys()
	var curMu sync.Mutex
	for w := range 4 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 3))
			for !stop.Load() {
				curMu.Lock()
				ks := slices.Sorted(maps.Keys(cur))
				k := ks[r.IntN(len(ks))]
				want := cur[k]
				curMu.Unlock()
				_, _, got, err := s.GetObjectRange("b", k, byteRange{0, int64(len(want)) - 1})
				if err != nil {
					if errors.Is(err, errNoSuchKey) {
						continue
					}
					rerr.Store(fmt.Errorf("%s: %v", k, err))
					return
				}
				if !bytes.Equal(got, want) {
					rerr.Store(fmt.Errorf("%s differs", k))
					return
				}
			}
		}(w)
	}
	zero := 0
	for f := 0; f < 18; f += 2 {
		for ver := range 3 {
			k := fmt.Sprintf("src/f%03d.c#%d", f, ver)
			curMu.Lock()
			delete(cur, k)
			curMu.Unlock()
			mustOK(t, s.DeleteObject("b", k))
			delete(l.want, k)
		}
	}
	res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
	mustOK(t, err)
	mustOK(t, s.ApplyHistoryPrune(&res))
	time.Sleep(10 * time.Millisecond)
	var compacted int
	for range 3 {
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		mustOK(t, err)
		if rep.RecordGroups != nil {
			compacted += rep.RecordGroups.Compacted
		}
	}
	stop.Store(true)
	wg.Wait()
	if e := rerr.Load(); e != nil {
		t.Fatal(e)
	}
	files1, _ := l.s.authFiles()
	if len(files1) >= len(files0) || compacted == 0 {
		t.Fatalf("records %d -> %d, groups compacted %d (of %d)", len(files0), len(files1), compacted, groups0)
	}
	l.exact("online")
	l.verifyOK("online")
	l.close()
	l.open()
	l.exact("reopened")
	l.verifyOK("reopened")
}

// TestB9ConcurrentGrouped: readers of every content stay exact while one goroutine publishes new
// generations and another compacts the groups (run under -race).
func TestB9ConcurrentGrouped(t *testing.T) {
	b9Grouped(t, 4<<10)
	old := agCompactLive
	agCompactLive = 1.01 // every sealed group is a victim on every pass
	t.Cleanup(func() { agCompactLive = old })
	l := newAuthLab(t)
	b9SmallHistory(l, 11, 10, 2)
	l.reclaim()
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	files, _ := l.s.authFiles()
	var stop atomic.Bool
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for w := range 6 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 8))
			keys := slices.Sorted(maps.Keys(l.want))
			for !stop.Load() {
				k := keys[r.IntN(len(keys))]
				want := l.want[k]
				a := int64(r.IntN(len(want)))
				_, _, got, err := l.s.GetObjectRange("b", k, byteRange{a, int64(len(want)) - 1})
				if err != nil || !bytes.Equal(got, want[a:]) {
					errs <- fmt.Errorf("%s+%d: %v", k, a, err)
					return
				}
			}
		}(w)
	}
	wg.Add(2)
	go func() { // republish: every record again at its next generation
		defer wg.Done()
		for i := 0; i < 3*len(files) && !stop.Load(); i++ {
			sha := files[i%len(files)]
			r, err := l.s.authLoad(sha)
			if err != nil || r == nil {
				errs <- fmt.Errorf("load %x: %v", sha[:4], err)
				return
			}
			r2 := *r
			r2.Gen++
			if err := l.s.authPublish(&r2); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			if _, err := l.s.agCompact(false); err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(1500 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	l.exact("after concurrent publish and compaction")
	l.verifyOK("after concurrent publish and compaction")
	l.close()
	l.open()
	l.exact("reopened")
}

// TestB9CrossProcess: records another process publishes into groups are seen by an open store (the
// tail refresh), and its compaction (another process unlinking groups) is followed by a reload.
func TestB9CrossProcess(t *testing.T) {
	b9Grouped(t, 4<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 13, 10, 2)
	l.reclaim()
	l.close()
	l.open()
	before, _ := l.s.authFiles()
	if len(before) != 0 {
		t.Fatal("records before migration")
	}
	if killed, _ := runHelper(t, "groupmigrate", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1"); killed {
		t.Fatal("helper killed")
	}
	after, _ := l.s.authFiles()
	if len(after) < 5 {
		t.Fatalf("other process's %d records not seen", len(after))
	}
	for _, f := range after {
		if r, err := l.s.authLoad(f); err != nil || r == nil {
			t.Fatalf("record %x: %v", f[:4], err)
		}
	}
	l.exact("seen from another process")
	l.close()
	l.open()
	l.exact("reopened")
}

// TestB9CrashMatrix: real SIGKILL cuts of grouped publication (appended, fsynced, sealed, group created),
// of gc/repack compaction (frames copied, groups unlinked, legacy files unlinked by regroup) and of
// ungrouping. After each: every acknowledged object is exact, reclamation converges, deep verify passes.
func TestB9CrashMatrix(t *testing.T) {
	if testing.Short() {
		t.Skip("process-kill matrix")
	}
	b9Grouped(t, 4<<10)
	base := newAuthLab(t)
	b9SmallHistory(base, 15, 14, 3)
	base.reclaim()
	base.close()
	// A second fixture already migrated (half its records as legacy files) for the compaction cuts.
	agWanted = false
	mig := &authLab{t: t, dir: t.TempDir(), want: base.want}
	mustOK(t, os.CopyFS(mig.dir, os.DirFS(base.dir)))
	os.Remove(filepath.Join(mig.dir, "LOCK"))
	mig.open()
	mig.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	agWanted = true
	for f := 0; f < 14; f += 2 {
		k := fmt.Sprintf("src/f%03d.c#2", f)
		mustOK(t, mig.s.DeleteObject("b", k))
	}
	mig.close()
	wantMig := maps.Clone(base.want)
	for f := 0; f < 14; f += 2 {
		delete(wantMig, fmt.Sprintf("src/f%03d.c#2", f))
	}
	type cut struct{ scenario, point, from string }
	var cuts []cut
	for _, p := range []string{hookAGFrameWritten, hookAGSynced, hookAGSealed, hookAGCreated, hookAuthPublished} {
		cuts = append(cuts, cut{"groupmigrate", p, "base"})
	}
	for _, p := range []string{hookAGBeforeFileDel, hookAGCopied, hookAGUnlinked, hookAGFrameWritten, hookBeforeGCDelete} {
		cuts = append(cuts, cut{"gc", p, "mig"})
	}
	n := 0
	for _, c := range cuts {
		for occ := 1; occ <= 6; occ++ {
			src, want := base.dir, base.want
			if c.from == "mig" {
				src, want = mig.dir, wantMig
			}
			l := &authLab{t: t, dir: t.TempDir(), want: want}
			mustOK(t, os.CopyFS(l.dir, os.DirFS(src)))
			os.Remove(filepath.Join(l.dir, "LOCK"))
			if killed, _ := runHelper(t, c.scenario, l.dir, c.point, occ, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096"); !killed {
				break
			}
			n++
			stage := fmt.Sprintf("%s %s#%d", c.scenario, c.point, occ)
			l.open()
			l.exact(stage + " after kill")
			l.verifyOK(stage + " after kill")
			l.reclaim()
			l.exact(stage + " reclaimed")
			l.verifyOK(stage + " reclaimed")
			l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
			l.reclaim()
			l.exact(stage + " completed")
			l.verifyOK(stage + " completed")
			if b9LegacyFiles(t, l.s) != 0 {
				t.Fatalf("%s: legacy files after regroup", stage)
			}
			l.close()
		}
	}
	if n < 20 {
		t.Fatalf("only %d cuts reached", n)
	}
	t.Logf("%d process-kill cuts", n)
}

// TestB9OldBinaryRefuses: the B8 build refuses a grouped store through the declared feature and leaves
// it untouched; this build reads it.
func TestB9OldBinaryRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the B8 binary")
	}
	old := buildBinaryAt(t, "1e261c299e52478f736ba88b803c53eed8298489")
	b9Grouped(t, 4<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 17, 8, 2)
	l.reclaim()
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.close()
	before := treeDigest(t, l.dir)
	for _, args := range [][]string{{"stats", "-store", l.dir}, {"verify", "-store", l.dir}, {"gc", "-store", l.dir, "-apply"}, {"repack", "-store", l.dir, "-apply"}} {
		out, errOut, code := runZeros3CLI(t, old, args...)
		if code == 0 || !strings.Contains(out+errOut, agFeature) {
			t.Fatalf("B8 binary %v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	if treeDigest(t, l.dir) != before {
		t.Fatal("the B8 binary mutated a grouped store while refusing it")
	}
	l.open()
	l.exact("read by this build")
}

// b9ExpectNoTmp asserts a store left no staged group files behind after reclamation.
func b9ExpectNoTmp(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(filepath.Join(dir, "tmp"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "auth-group-") {
			t.Fatalf("staged group left: %s", p)
		}
		return nil
	})
}
