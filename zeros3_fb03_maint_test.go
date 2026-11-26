package main

// FB-03 M-A7: maintenance robustness and the dirty gate (section 13d3).
// G-FAULT fixtures: a corrupt live record in a pack chosen for rewrite, a
// missing loose leaf, a pack of live leaves gone, a missing manifest, and a
// crash at every point of a pass that isolates damage. G-MODEL: randomized
// histories over two buckets that share chunks, with damage injected between
// passes; no pass may delete a copy of a damaged digest or the last readable
// copy of any live digest. The dirty gate: an idle store runs no mark, and
// every kind of garbage-making event brings the next scheduled pass back.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func fb03PackPath(t *testing.T, s *Store, id string) string {
	t.Helper()
	st := s.packSnap()
	for i, p := range st.packs {
		if p.id == id && !st.isGone(uint32(i)) {
			return p.path
		}
	}
	t.Fatalf("pack %s is not in the locator", id)
	return ""
}

// fb03Files is every file under chunks/, manifests/ and the pack directories,
// by path relative to dir, with its SHA-256.
func fb03Files(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, sub := range []string{"chunks", "manifests", "packs", "segments", "tiers"} {
		filepath.WalkDir(filepath.Join(dir, sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(p) == ".lock" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			out[rel] = sha256.Sum256(b)
			return nil
		})
	}
	return out
}

// fb03Holders is every file that holds a copy of sum (packs and the loose
// file), with its SHA-256.
func fb03Holders(t *testing.T, s *Store, sum [32]byte) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	st := s.packSnap()
	var locs [4]packLoc
	for _, loc := range st.appendLocs(locs[:0], sum) {
		p := st.packs[loc.pack].path
		if b, err := os.ReadFile(p); err == nil {
			out[p] = sha256.Sum256(b)
		}
	}
	if b, err := os.ReadFile(s.chunkPath(sum)); err == nil {
		out[s.chunkPath(sum)] = sha256.Sum256(b)
	}
	return out
}

func fb03KeepsHolders(t *testing.T, before map[string][32]byte) {
	t.Helper()
	for p, h := range before {
		b, err := os.ReadFile(p)
		if err != nil || sha256.Sum256(b) != h {
			t.Fatalf("a copy of a damaged digest was removed or changed: %s (%v)", p, err)
		}
	}
}

// fb03MaintFixture: packs that end up partly dead, loose garbage, dead
// manifests and an aborted upload (b4Fixture + b4Garbage).
func fb03MaintFixture(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	dir, want := b4Fixture(t, 16, 90<<10)
	s := b4OpenMaint(t, dir)
	b4Garbage(t, s, want)
	mustOK(t, s.Close())
	return dir, want
}

func fb03Chunks(t *testing.T, s *Store, bucket, key string) [][32]byte {
	t.Helper()
	_, man, err := s.HeadObject(bucket, key)
	mustOK(t, err)
	var out [][32]byte
	for _, c := range man.Chunks {
		sum, err := decodeHexSHA256(c.SHA256)
		mustOK(t, err)
		out = append(out, sum)
	}
	return out
}

// fb03Victim is a live packed chunk in a pack the pass will rewrite, and the
// keys whose objects hold it.
func fb03Victim(t *testing.T, s *Store, want map[string][]byte) (packUsage, [32]byte, []string) {
	t.Helper()
	rr, err := s.computeReachability(false)
	mustOK(t, err)
	u := partialPackOf(t, s, rr)
	live := livePackedDigests(t, s, rr.ReferencedChunks, u)
	victim := live[len(live)/2]
	var keys []string
	for k := range want {
		if slices.Contains(fb03Chunks(t, s, "b", k), victim) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		t.Fatal("the victim is referenced by no current object")
	}
	return u, victim, keys
}

// fb03DamageManifest makes the manifest of ent unreadable -- its file gone, or
// its inline record flipped -- and returns the repair.
func fb03DamageManifest(t *testing.T, s *Store, ent *objectEntry) func() {
	t.Helper()
	p := filepath.Join(s.root, "manifests", ent.manifestUUID+".json")
	if data, err := os.ReadFile(p); err == nil {
		mustOK(t, os.Remove(p))
		s.purgeReadCaches() // a served manifest stays cached; reads must see the loss
		return func() { mustOK(t, os.WriteFile(p, data, 0o644)); s.purgeReadCaches() }
	}
	flipPackedByte(t, s, ent.manifestSHA256)
	s.purgeReadCaches()
	return func() { flipPackedByte(t, s, ent.manifestSHA256); s.purgeReadCaches() }
}

func fb03Without(want map[string][]byte, keys []string) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range want {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}

// F4-MNT-02: one corrupt live record in a pack chosen for rewrite. The pass
// completes and reports it, the pack stays byte for byte, the rest is
// reclaimed; the next pass leaves the pack alone without reading it again;
// once the record is repaired the quarantine is released and the pack goes.
func TestFB03_MaintIsolatesCorruptRecord(t *testing.T) {
	// The undamaged control: what a pass reclaims from this fixture.
	cdir, _ := fb03MaintFixture(t)
	cs := b4OpenMaint(t, cdir)
	control, err := cs.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	mustOK(t, cs.Close())

	dir, want := fb03MaintFixture(t)
	s := b4OpenMaint(t, dir)
	defer func() { s.Close() }()
	u, victim, keys := fb03Victim(t, s, want)
	flipPackedByte(t, s, victim)
	holders := fb03Holders(t, s, victim)

	rep, err := s.maintainOnline(context.Background(), b4Opts())
	if err != nil {
		t.Fatalf("a pass over one corrupt live record failed: %v", err)
	}
	if len(rep.Quarantined) != 1 || rep.Quarantined[0].Digest != hex.EncodeToString(victim[:]) || rep.HeldPacks != 1 {
		t.Fatalf("the corrupt record is not reported: %+v", rep)
	}
	if rep.BytesReclaimed == 0 || rep.ChunksDeleted != control.ChunksDeleted || rep.ManifestsDeleted != control.ManifestsDeleted {
		t.Fatalf("the rest of the pass did not run: %+v (control %+v)", rep, control)
	}
	t.Logf("reclaimed %d of the control's %d bytes; %d packs retired (control %d)", rep.BytesReclaimed, control.BytesReclaimed, rep.PacksRetired, control.PacksRetired)
	fb03KeepsHolders(t, holders)
	others := fb03Without(want, keys)
	b4Check(t, s, others)
	for _, k := range keys {
		if _, _, err := s.GetObject("b", k); err == nil {
			t.Fatalf("%s holds the corrupt record and was served", k)
		}
	}

	// The next pass skips the held pack at selection: the carry never reads
	// the damaged record again (the quarantine recheck reads it untraced).
	var reads atomic.Int64
	s.packTrace = &packTrace{OnRecord: func(sum *[32]byte, _ int32, _ int64, _, _ uint32, _ byte, _, _ int64) {
		if *sum == victim {
			reads.Add(1)
		}
	}}
	rep2, err := s.maintainOnline(context.Background(), b4Opts())
	s.packTrace = nil
	mustOK(t, err)
	if rep2.HeldPacks != 1 || len(rep2.Quarantined) != 1 || !rep2.Quarantined[0].FirstSeen.Equal(rep.Quarantined[0].FirstSeen) || rep2.RecordsCopied != 0 {
		t.Fatalf("second pass: %+v", rep2)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("the second pass read the damaged record %d times", n)
	}
	fb03KeepsHolders(t, holders)
	_ = u

	// Repaired: the recheck releases the quarantine and the pack is rewritten.
	flipPackedByte(t, s, victim)
	s.purgeReadCaches()
	rep3, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if len(rep3.Quarantined) != 0 || rep3.HeldPacks != 0 || rep3.PacksRetired == 0 {
		t.Fatalf("after the repair: %+v", rep3)
	}
	b4Check(t, s, want)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}

// F4-MNT-01: a missing live leaf (a loose chunk, or a whole pack of them)
// leaves the live set exact; the pass reclaims around it and keeps every
// file the damaged objects reach. A missing manifest is a root that cannot be
// resolved: the pass deletes nothing anywhere.
func TestFB03_MaintProceedsAroundMissingLeaf(t *testing.T) {
	t.Run("loose-chunk", func(t *testing.T) {
		dir, want := fb03MaintFixture(t)
		s := b4OpenMaint(t, dir)
		defer func() { s.Close() }()
		chunks := fb03Chunks(t, s, "b", "k000") // rewritten loose by b4Garbage
		if _, ok := s.packLookup(chunks[0]); ok {
			t.Fatal("fixture: k000 is expected loose")
		}
		mustOK(t, os.Remove(s.chunkPath(chunks[0])))
		var rest map[string][32]byte = map[string][32]byte{}
		for _, c := range chunks[1:] {
			for p, h := range fb03Holders(t, s, c) {
				rest[p] = h
			}
		}
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		if err != nil || rep.Damage != "leaf" || rep.DamagedChunks != 1 || rep.LiveSetOK || rep.BytesReclaimed == 0 {
			t.Fatalf("pass over a missing loose leaf: %v %+v", err, rep)
		}
		fb03KeepsHolders(t, rest)
		b4Check(t, s, fb03Without(want, []string{"k000"}))
		if _, _, err := s.GetObject("b", "k000"); err == nil {
			t.Fatal("the damaged object was served")
		}
		if v, err := s.Verify(true); err != nil || v.Missing != 1 || len(v.Issues) != 1 {
			t.Fatalf("the damage must stay exactly visible: %v %+v", err, v.Issues)
		}
	})
	t.Run("pack-gone", func(t *testing.T) {
		dir, want := fb03MaintFixture(t)
		s := b4OpenMaint(t, dir)
		// A direct-pack PUT: its chunks form a pack of their own, its
		// manifest is a file.
		old := smallObjectSegments
		smallObjectSegments = false
		want["big"] = b4Body(7070, 600<<10)
		_, err := s.PutObject("b", "big", want["big"], "application/octet-stream", nil)
		smallObjectSegments = old
		mustOK(t, err)
		lost := fb03Chunks(t, s, "b", "big")
		loc, ok := s.packLookup(lost[0])
		if !ok {
			t.Fatal("fixture: the big object is not packed")
		}
		path := s.packSnap().packs[loc.pack].path
		_, entries, err := loadPackFile(path)
		mustOK(t, err)
		mustOK(t, s.Close())
		mustOK(t, os.Rename(path, filepath.Join(t.TempDir(), "moved.pack")))
		s = b4OpenMaint(t, dir)
		defer func() { s.Close() }()
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		if err != nil || rep.Damage != "leaf" || rep.DamagedChunks != len(entries) || rep.BytesReclaimed == 0 {
			t.Fatalf("pass with a pack of %d live chunks gone: %v %+v", len(entries), err, rep)
		}
		t.Logf("%d live chunks gone; reclaimed %d bytes", len(entries), rep.BytesReclaimed)
		b4Check(t, s, fb03Without(want, []string{"big"}))
		if v, err := s.Verify(true); err != nil || v.Missing != len(entries) || len(v.Issues) != len(entries) {
			t.Fatalf("the damage must stay exactly visible: %v %d issues", err, len(v.Issues))
		}
	})
	t.Run("manifest-gone", func(t *testing.T) {
		dir, _ := fb03MaintFixture(t)
		s := b4OpenMaint(t, dir)
		defer func() { s.Close() }()
		ent, err := s.lookupObject("b", "k001")
		mustOK(t, err)
		fb03DamageManifest(t, s, ent)
		before := fb03Files(t, dir)
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		if !errors.Is(err, errGCUnsafe) || rep.Damage != "reference" {
			t.Fatalf("a pass over an unresolvable root: %v %+v", err, rep)
		}
		if after := fb03Files(t, dir); len(after) != len(before) {
			t.Fatalf("files changed under a refused pass: %d -> %d", len(before), len(after))
		}
	})
}

// G-FAULT: a crash at every point a pass that isolates a corrupt record
// reaches. After reopening, every undamaged object is intact, every copy of
// the damaged digest is still there, and a new pass completes.
func TestFB03_MaintCrashDuringIsolatedPass(t *testing.T) {
	fixture := func(t *testing.T) (string, *Store, map[string][]byte, []string, map[string][32]byte) {
		dir, want := fb03MaintFixture(t)
		s := b4OpenMaint(t, dir)
		_, victim, keys := fb03Victim(t, s, want)
		flipPackedByte(t, s, victim)
		return dir, s, want, keys, fb03Holders(t, s, victim)
	}
	_, s, _, _, _ := fixture(t)
	pts := fb02Points(func() {
		if _, err := s.maintainOnline(context.Background(), b4Opts()); err != nil {
			t.Fatal(err)
		}
	})
	mustOK(t, s.Close())
	t.Logf("an isolating pass reaches %v", pts)
	for _, point := range pts {
		t.Run(point, func(t *testing.T) {
			dir, s, want, keys, holders := fixture(t)
			fb02CrashAt(t, point, func() { s.maintainOnline(context.Background(), b4Opts()) })
			s.Close()
			s = b4OpenMaint(t, dir)
			defer s.Close()
			fb03KeepsHolders(t, holders)
			b4Check(t, s, fb03Without(want, keys))
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			if err != nil || len(rep.Quarantined) > 1 {
				t.Fatalf("pass after the crash: %v %+v", err, rep)
			}
			fb03KeepsHolders(t, holders)
			b4Check(t, s, fb03Without(want, keys))
		})
	}
}

// G-MODEL: randomized histories on two buckets that share chunks (copies and
// concatenations of other bodies), with leaf or root damage injected before
// a pass and repaired after it.
func TestFB03_MaintModel(t *testing.T) {
	seeds := 6
	if testing.Short() {
		seeds = 2
	}
	for seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { fb03ModelRun(t, uint64(seed)) })
	}
}

func fb03ModelRun(t *testing.T, seed uint64) {
	old := smallObjectSegments
	smallObjectSegments = false // loose ingest; compaction cuts the packs
	defer func() { smallObjectSegments = old }()
	r := rand.New(rand.NewPCG(seed, 0xfb03))
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	for _, b := range []string{"b", "c"} {
		mustOK(t, s.CreateBucket(b))
	}
	model := map[[2]string][]byte{}
	keysOf := func() [][2]string {
		ks := slices.Collect(func(yield func([2]string) bool) {
			for k := range model {
				if !yield(k) {
					return
				}
			}
		})
		slices.SortFunc(ks, func(a, b [2]string) int { return bytes.Compare([]byte(a[0]+"/"+a[1]), []byte(b[0]+"/"+b[1])) })
		return ks
	}
	put := func(k [2]string, body []byte) {
		_, err := s.PutObject(k[0], k[1], body, "application/octet-stream", nil)
		mustOK(t, err)
		model[k] = body
	}
	pick := func() ([2]string, []byte) {
		ks := keysOf()
		k := ks[r.IntN(len(ks))]
		return k, model[k]
	}
	newKey := func() [2]string {
		return [2]string{[]string{"b", "c"}[r.IntN(2)], fmt.Sprintf("o%03d", r.IntN(40))}
	}
	for i := range 6 {
		put(newKey(), b4Body(int(seed)*1000+i, 20<<10+r.IntN(120<<10)))
	}
	prune := func() {
		zero := 0
		for _, b := range []string{"b", "c"} {
			res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: b, KeepLast: &zero})
			mustOK(t, err)
			mustOK(t, s.ApplyHistoryPrune(&res))
		}
	}
	for round := range 4 {
		for range 6 + r.IntN(6) {
			switch op := r.IntN(6); {
			case op == 0 || len(model) < 3:
				put(newKey(), b4Body(int(seed)*1000+100*round+r.IntN(100), 20<<10+r.IntN(120<<10)))
			case op == 1: // same content elsewhere: every chunk shared
				_, body := pick()
				put(newKey(), body)
			case op == 2: // concatenation: most chunks shared
				_, a := pick()
				_, b := pick()
				put(newKey(), append(append([]byte(nil), a...), b...))
			case op == 3:
				k, _ := pick()
				put(k, b4Body(int(seed)*1000+500+round*10+r.IntN(10), 30<<10+r.IntN(60<<10)))
			default:
				k, _ := pick()
				mustOK(t, s.DeleteObject(k[0], k[1]))
				delete(model, k)
			}
		}
		prune()
		mustOK(t, s.Close())
		if r.IntN(2) == 0 {
			opt := defaultCompactOptions()
			opt.TargetBytes, opt.MinBytes = 256<<10, 1
			_, err := compactStore(dir, opt)
			mustOK(t, err)
		}
		s = b4OpenMaint(t, dir)

		// Every live digest, by object; which of them read back now.
		chunksOf := map[[2]string][][32]byte{}
		readable := map[[32]byte]bool{}
		for _, k := range keysOf() {
			chunksOf[k] = fb03Chunks(t, s, k[0], k[1])
			for _, c := range chunksOf[k] {
				if _, err := s.casRead(c); err == nil {
					readable[c] = true
				}
			}
		}
		// Damage: none, a loose live chunk gone, a packed live record
		// flipped, or a manifest file gone.
		var damaged [][32]byte
		var repair func()
		kind := []string{"none", "loose", "packed", "manifest"}[r.IntN(4)]
		ks := keysOf()
		victimKey := ks[r.IntN(len(ks))]
		cs := chunksOf[victimKey]
		victim := cs[r.IntN(len(cs))]
		switch kind {
		case "loose":
			if _, ok := s.packLookup(victim); ok {
				kind = "none"
				break
			}
			data, err := os.ReadFile(s.chunkPath(victim))
			mustOK(t, err)
			mustOK(t, os.Remove(s.chunkPath(victim)))
			damaged = [][32]byte{victim}
			repair = func() { mustOK(t, os.WriteFile(s.chunkPath(victim), data, 0o644)) }
		case "packed":
			if _, ok := s.packLookup(victim); !ok {
				kind = "none"
				break
			}
			flipPackedByte(t, s, victim)
			damaged = [][32]byte{victim}
			repair = func() { flipPackedByte(t, s, victim); s.purgeReadCaches() }
		case "manifest":
			ent, err := s.lookupObject(victimKey[0], victimKey[1])
			mustOK(t, err)
			repair = fb03DamageManifest(t, s, ent)
		}
		holders := map[string][32]byte{}
		for _, d := range damaged {
			for p, h := range fb03Holders(t, s, d) {
				holders[p] = h
			}
		}
		affected := map[[2]string]bool{}
		for k, cs := range chunksOf {
			if kind == "manifest" && k == victimKey {
				affected[k] = true
			}
			for _, c := range cs {
				if slices.Contains(damaged, c) {
					affected[k] = true
				}
			}
		}
		before := fb03Files(t, dir)

		rep, err := s.maintainOnline(context.Background(), b4Opts())
		t.Logf("round %d: %d objects, damage %s; %+v", round, len(model), kind, struct {
			Damage                         string
			Reclaimed, Retired, Held, Quar int64
		}{rep.Damage, rep.BytesReclaimed, int64(rep.PacksRetired), int64(rep.HeldPacks), int64(len(rep.Quarantined))})
		switch kind {
		case "manifest":
			if !errors.Is(err, errGCUnsafe) {
				t.Fatalf("round %d: a pass over a missing manifest: %v", round, err)
			}
			if after := fb03Files(t, dir); len(after) != len(before) {
				t.Fatalf("round %d: a refused pass changed %d files to %d", round, len(before), len(after))
			}
		default:
			mustOK(t, err)
		}
		fb03KeepsHolders(t, holders)
		for _, k := range keysOf() {
			_, got, err := s.GetObject(k[0], k[1])
			switch {
			case affected[k]:
				if err == nil && kind != "packed" { // a packed flip may sit in a second, intact copy
					t.Fatalf("round %d: damaged %v was served", round, k)
				}
			case err != nil || !bytes.Equal(got, model[k]):
				t.Fatalf("round %d: %v lost: %v", round, k, err)
			}
		}
		for c := range readable {
			if slices.Contains(damaged, c) {
				continue
			}
			if _, err := s.casRead(c); err != nil {
				t.Fatalf("round %d: live chunk %x lost its last readable copy: %v", round, c[:6], err)
			}
		}
		if repair != nil {
			repair()
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			if err != nil || len(rep.Quarantined) != 0 || rep.Damage != "" {
				t.Fatalf("round %d: the pass after the repair: %v %+v", round, err, rep)
			}
		}
		for _, k := range keysOf() {
			_, got, err := s.GetObject(k[0], k[1])
			if err != nil || !bytes.Equal(got, model[k]) {
				t.Fatalf("round %d: %v after the repair: %v", round, k, err)
			}
		}
	}
	mustOK(t, s.Close())
	s, err = OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	for k, v := range model {
		_, got, err := s.GetObject(k[0], k[1])
		if err != nil || !bytes.Equal(got, v) {
			t.Fatalf("%v after reopen: %v", k, err)
		}
	}
	if v, err := s.Verify(true); err != nil || !v.OK() {
		t.Fatalf("deep verify at the end: %v %+v", err, v.Issues)
	}
}

// The dirty gate: once a pass found nothing, scheduled passes run no mark
// until a garbage-making event; an explicit request always runs.
func TestFB03_IdleStoreRunsNoMark(t *testing.T) {
	old := smallObjectSegments
	smallObjectSegments = false
	defer func() { smallObjectSegments = old }()
	dir, want := fb03MaintFixture(t)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	var marks atomic.Int64
	withTestHook(t, func(p string) {
		if p == hookMaintMarked {
			marks.Add(1)
		}
	})
	opt := b4Opts()
	mustOK(t, s.startMaintenance(opt, 5*time.Millisecond, -1))
	defer s.stopMaintenance()
	settle := func(what string) int64 {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for s.maintIdleAt.Load() == nil || s.maintenanceStatus().Running {
			if time.Now().After(deadline) {
				t.Fatalf("%s: no pass found nothing (last %+v)", what, s.maintenanceStatus().Last)
			}
			time.Sleep(5 * time.Millisecond)
		}
		return marks.Load()
	}
	quiet := func(what string) {
		t.Helper()
		m, sk := settle(what), s.maintenanceStatus().Skipped
		time.Sleep(200 * time.Millisecond)
		if n := marks.Load() - m; n != 0 {
			t.Fatalf("%s: an idle store ran %d marks", what, n)
		}
		if d := s.maintenanceStatus().Skipped - sk; d < 10 {
			t.Fatalf("%s: only %d scheduled passes skipped", what, d)
		}
	}
	// wakes: the event brings a mark back, and the store settles again.
	wakes := func(what string, event func()) MaintReport {
		t.Helper()
		m := settle(what)
		event()
		deadline := time.Now().Add(20 * time.Second)
		for marks.Load() == m {
			if time.Now().After(deadline) {
				t.Fatalf("%s: no pass ran after the event", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
		settle(what)
		quiet(what)
		return *s.maintenanceStatus().Last
	}
	quiet("after the fixture's garbage is reclaimed")

	wakes("delete", func() { mustOK(t, s.DeleteObject("b", "k001")); delete(want, "k001") })
	wakes("prune", func() { b4Prune(t, s) })
	wakes("explicit request", func() { s.triggerMaintenance() })

	// A PUT whose commit fails leaves loose chunks nothing references: no
	// journal frame, only the event count says so.
	settle("before the failed put")
	files := countChunkFiles(t, dir)
	wakes("failed put", func() {
		testJournalFault = func(p string, n int) (int, error) {
			if p == faultJournalWrite {
				return 0, syscall.EIO
			}
			return 0, nil
		}
		_, err := s.PutObject("b", "failed", b4Body(4242, 80<<10), "application/octet-stream", nil)
		testJournalFault = nil
		if err == nil {
			t.Fatal("the faulted PUT was acknowledged")
		}
		if countChunkFiles(t, dir) <= files {
			t.Fatal("fixture: the failed PUT left no loose chunk")
		}
	})
	if n := countChunkFiles(t, dir); n != files {
		t.Fatalf("the failed PUT's chunks were not reclaimed: %d -> %d", files, n)
	}
	b4Check(t, s, want)
}

// The gate never skips scheduled passes for longer than maintIdleRecheck.
func TestFB03_IdleRecheckHorizon(t *testing.T) {
	dir, _ := fb03MaintFixture(t)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	var marks atomic.Int64
	withTestHook(t, func(p string) {
		if p == hookMaintMarked {
			marks.Add(1)
		}
	})
	prev := maintIdleRecheck
	maintIdleRecheck = 40 * time.Millisecond
	defer func() { maintIdleRecheck = prev }()
	mustOK(t, s.startMaintenance(b4Opts(), 5*time.Millisecond, -1))
	time.Sleep(500 * time.Millisecond)
	s.stopMaintenance()
	st := s.maintenanceStatus()
	// one pass reclaims, one finds nothing, then at most one per horizon
	if n := marks.Load(); n < 4 || n > 16 || st.Skipped == 0 {
		t.Fatalf("%d marks and %d skips in 500 ms with a 40 ms horizon", n, st.Skipped)
	}
}

// In a store with authority bodies a reclaimed CDC list stands in for a body
// record, so chunk damage there can be the only trace of a record that
// vanished: an online pass refuses as for reference damage and removes
// nothing. A damaged frame of an unsealed group is reported, not skipped.
func TestFB03_MaintAuthorityDamageIsReference(t *testing.T) {
	for _, mode := range []string{"grouped-last-frame", "grouped-inner-frame", "missing-component"} {
		t.Run(mode, func(t *testing.T) {
			if mode != "missing-component" {
				b9Grouped(t, 1<<20)
			}
			f := eqFixture(t)
			switch mode {
			case "grouped-last-frame": // reads as a torn tail; the CDC symptom remains
				authDamageRecord(t, f.l.s, f.rec.SHA)
			case "grouped-inner-frame": // the group's first record: a valid frame follows
				files, err := f.l.s.authFiles()
				mustOK(t, err)
				first, at := files[0], int64(-1)
				for _, sha := range files {
					if p, off, _ := authRecordBytesAt(t, f.l.s, sha); filepath.Ext(p) == agSuffix && (at < 0 || off < at) {
						first, at = sha, off
					}
				}
				authDamageRecord(t, f.l.s, first)
			default:
				removeChunkAnyLayout(t, f.l.s, f.victim.SHA)
			}
			f.l.close()
			s := b4OpenMaint(t, f.l.dir)
			defer s.Close()
			if mode == "grouped-inner-frame" {
				if rr, err := s.computeReachability(false); err != nil || !slices.ContainsFunc(rr.Issues, func(is VerifyIssue) bool { return is.Subject == "authority groups" }) {
					t.Fatalf("a damaged group region is not reported: %v %+v", err, rr.Issues)
				}
			}
			before := fb03Files(t, f.l.dir)
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			if !errors.Is(err, errGCUnsafe) || rep.Damage != "reference" {
				t.Fatalf("a pass over authority damage: %v %+v", err, rep)
			}
			if after := fb03Files(t, f.l.dir); len(after) != len(before) {
				t.Fatalf("files changed under a refused pass: %d -> %d", len(before), len(after))
			}
		})
	}
}
