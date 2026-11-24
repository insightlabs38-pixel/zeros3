package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ---- B10-0 grouped-record integration ------------------------------------------

// authLegacyLayout pins the pre-B9 layout (one record file per content) for a test that edits record files.
func authLegacyLayout(t testing.TB) {
	old := agWanted
	agWanted = false
	t.Cleanup(func() { agWanted = old })
}

// authRecordBytesAt locates the stored bytes of sha's deciding record in either layout: the file and the
// [off, off+n) region holding the record (a whole legacy file, or one group frame).
func authRecordBytesAt(t testing.TB, s *Store, sha [32]byte) (path string, off, n int64) {
	t.Helper()
	if loc, known, err := s.agLookup(sha); err == nil && known {
		if loc.kind != agRecordFrame {
			t.Fatalf("record %x is retired", sha[:4])
		}
		return s.agPath(loc.seq), loc.off, loc.n
	}
	fi, err := os.Stat(s.authPath(sha))
	if err != nil {
		t.Fatalf("record %x: %v", sha[:4], err)
	}
	return s.authPath(sha), 0, fi.Size()
}

// authRawRecord is sha's marshalled record in either layout.
func authRawRecord(t *testing.T, s *Store, sha [32]byte) []byte {
	t.Helper()
	data, known, err := s.agLoadRecord(sha)
	if !known && err == nil {
		data, err = os.ReadFile(s.authPath(sha))
	}
	if err != nil || len(data) == 0 {
		t.Fatalf("record %x: %v", sha[:4], err)
	}
	return data
}

// authDamageRecord flips one byte in the middle of sha's stored record (file or frame) and returns an undo.
func authDamageRecord(t *testing.T, s *Store, sha [32]byte) func() {
	t.Helper()
	p, off, n := authRecordBytesAt(t, s, sha)
	b, err := os.ReadFile(p)
	mustOK(t, err)
	b[off+n/2] ^= 0x5a
	mustOK(t, os.WriteFile(p, b, 0o644))
	return func() {
		b[off+n/2] ^= 0x5a
		mustOK(t, os.WriteFile(p, b, 0o644))
	}
}

// TestB10GroupedDamagedRecordRefusesGC: in the grouped layout a damaged live record frame must stop
// gc and repack exactly as a damaged record file does; after the frame is restored every object is exact.
func TestB10GroupedDamagedRecordRefusesGC(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%v", grouped), func(t *testing.T) {
			if grouped {
				b9Grouped(t, 1<<20)
			} else {
				authLegacyLayout(t)
			}
			f := eqFixture(t)
			if p, _, _ := authRecordBytesAt(t, f.l.s, f.rec.SHA); grouped != (filepath.Ext(p) == agSuffix) {
				t.Fatalf("record layout %s", p)
			}
			undo := authDamageRecord(t, f.l.s, f.rec.SHA)
			f.l.close()
			if _, err := gcCollect(f.l.dir, true); err == nil {
				t.Fatal("gc ran against a damaged live record")
			}
			if _, err := repackStore(f.l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err == nil {
				t.Fatal("repack ran against a damaged live record")
			}
			undo()
			f.l.open()
			f.l.exact("restored")
			f.l.verifyOK("restored")
		})
	}
}

var _ = bytes.Equal
var _ = filepath.Join

// TestB10GroupedMissingBase: the grouped analogues of a lost base record file (a damaged base frame, a lost
// group file) fail reachability and gc closed; restored, the store is exact and verifies.
func TestB10GroupedMissingBase(t *testing.T) {
	for _, mode := range []string{"damaged-frame", "lost-group"} {
		t.Run(mode, func(t *testing.T) {
			b9Grouped(t, 2<<10) // small groups: the base and its dependents land in different files
			l := newAuthLab(t)
			for i, v := range authHistory(41, 3, 1_000_000) {
				l.put("k"+string(rune('0'+i)), v)
			}
			l.migrate(AuthPolicy{MinSaving: 0.05})
			l.reclaim() // the CDC copies of body-backed contents are gone: the records are their only source
			files, _ := l.s.authFiles()
			var victim [32]byte
			for _, f := range files {
				if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormPin {
					victim = f
				}
			}
			if victim == ([32]byte{}) {
				t.Fatal("no pin record")
			}
			p, _, _ := authRecordBytesAt(t, l.s, victim)
			if filepath.Ext(p) != agSuffix {
				t.Fatalf("pin not grouped: %s", p)
			}
			orig, err := os.ReadFile(p)
			mustOK(t, err)
			if mode == "lost-group" {
				mustOK(t, os.Rename(p, p+".lost"))
				l.close()
				l.open()
			} else {
				authDamageRecord(t, l.s, victim)
				l.close()
				l.open()
			}
			rr, err := l.s.computeReachability(false)
			if err == nil && rr.OK() {
				t.Fatal("a missing grouped base passed reachability")
			}
			l.offline(func() {
				if _, err := gcCollect(l.dir, true); err == nil {
					t.Fatal("gc applied despite a missing grouped base")
				}
			})
			l.close()
			os.Remove(p + ".lost")
			mustOK(t, os.WriteFile(p, orig, 0o644))
			l.open()
			l.exact("restored")
			l.verifyOK("restored")
		})
	}
}

// TestB10GroupStaleViews: what a stale group-index view can observe. In process, a frame location taken
// before compaction (group rewritten and unlinked) resolves to the re-appended frame. Across processes, a
// shared opener's superseded frames stay valid and exact while another process reverts and republishes
// every record, and online maintenance in a third process defers every unlink and compaction while the
// opener holds the reader lease; once it closes, maintenance proceeds and the store is exact.
func TestB10GroupStaleViews(t *testing.T) {
	b9Grouped(t, 4<<10)
	l := newAuthLab(t)
	b9SmallHistory(l, 23, 12, 3)
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.reclaim()
	files, _ := l.s.authFiles()
	if len(files) < 8 {
		t.Fatalf("%d records", len(files))
	}
	old := map[[32]byte]agLoc{}
	for _, f := range files {
		loc, known, err := l.s.agLookup(f)
		mustOK(t, err)
		if !known || loc.kind != agRecordFrame {
			t.Fatalf("%x not grouped", f[:4])
		}
		old[f] = loc
	}
	// In process: retire every other record (CDC again), compact, then resolve through the stale locations.
	keep := map[[32]byte]bool{}
	for i, f := range files {
		if i%2 == 0 {
			if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormPin {
				keep[f] = true
				continue
			}
			mustOK(t, l.s.AuthorityRevert(f))
		} else {
			keep[f] = true
		}
	}
	ag, err := l.s.agCompact(false)
	mustOK(t, err)
	if ag.Compacted == 0 {
		t.Fatalf("nothing compacted: %+v", ag)
	}
	moved := 0
	x := &l.s.auth.ag
	for f := range keep {
		lo := old[f]
		if _, err := os.Stat(l.s.agPath(lo.seq)); !os.IsNotExist(err) {
			continue
		}
		moved++
		if _, err := l.s.agReadRecord(f, lo); err == nil {
			t.Fatal("a frame of an unlinked group still read through the index")
		}
		x.mu.Lock()
		x.win[f] = lo // the view a lookup taken before compaction holds
		x.mu.Unlock()
		r, err := l.s.authLoad(f)
		if err != nil || r == nil || r.SHA != f {
			t.Fatalf("stale location after compaction: %v", err)
		}
	}
	if moved == 0 {
		t.Fatal("no live record moved by compaction")
	}
	l.exact("after in-process compaction")
	l.verifyOK("after in-process compaction")
	l.migrate(AuthPolicy{MinSaving: 0.01, MaxDepth: 2})
	l.reclaim()
	l.close()

	// Across processes.
	a, err := OpenStoreShared(l.dir)
	mustOK(t, err)
	files, _ = a.authFiles()
	stale := map[[32]byte]agLoc{}
	for _, f := range files {
		loc, _, err := a.agLookup(f)
		mustOK(t, err)
		stale[f] = loc
	}
	// readAll reads every content with a record through the authority path alone (no CDC fallback).
	readAll := func(stage string) {
		for k, w := range l.want {
			sha := sha256.Sum256(w)
			if r, _ := a.authLoad(sha); r == nil {
				continue
			}
			c := a.newAuthCtx()
			b, _, err := c.body(sha, int64(len(w)), 1)
			if err != nil {
				t.Fatalf("%s: %s: %v", stage, k, err)
			}
			got := make([]byte, len(w))
			if n, err := b.ReadAt(got, 0); n != len(w) || !bytes.Equal(got, w) {
				t.Fatalf("%s: %s: %d bytes: %v", stage, k, n, err)
			}
			c.close()
		}
	}
	// inject makes a's index decide the chosen contents by their pre-republication frames, as a view that
	// has not yet seen the other process's appends would.
	inject := func(pick func(*authRecord) bool) (n int) {
		ax := &a.auth.ag
		ax.mu.Lock()
		defer ax.mu.Unlock()
		for f, lo := range stale {
			g := ax.groups[lo.seq]
			if g == nil {
				t.Fatalf("group %d of a stale frame is gone", lo.seq)
			}
			buf := make([]byte, lo.n)
			_, err := g.f.ReadAt(buf, lo.off)
			mustOK(t, err)
			_, p, _, ok := agParse(buf)
			if !ok {
				t.Fatal("stale frame no longer parses")
			}
			r, err := unmarshalAuthRecord(p)
			mustOK(t, err)
			if pick(r) {
				ax.win[f] = lo
				n++
			}
		}
		return n
	}
	proveStale := func(stage string) {
		inject(func(*authRecord) bool { return true }) // a wholly stale view: the old generation, consistent
		readAll(stage + ": wholly stale view")
		if inject(func(r *authRecord) bool { return len(r.Bases) > 0 }) == 0 { // old dependents, new bases
			t.Fatal("no dependent record")
		}
		readAll(stage + ": mixed view")
	}
	readAll("shared open")
	runHelper(t, "revert", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	runHelper(t, "groupmigrate", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	proveStale("after another process republished")
	readAll("refreshed")
	changed := 0
	for f, lo := range stale {
		if loc, known, _ := a.agLookup(f); known && loc != lo {
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("the shared opener never saw the new generations")
	}
	groups := b9Groups(t, l.dir)
	_, out := runHelper(t, "b10online", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	var rep MaintReport
	mustOK(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &rep))
	if rep.Deferred == "" || rep.RecordGroups != nil {
		t.Fatalf("maintenance unlinked under a shared opener: %+v", rep)
	}
	if g := b9Groups(t, l.dir); !slices.Equal(g, groups) {
		t.Fatal("group files changed under a shared opener")
	}
	proveStale("after deferred maintenance")
	readAll("after deferred maintenance")
	mustOK(t, a.Close())
	_, out = runHelper(t, "b10online", l.dir, "", 0, "ZEROS3_RECORD_GROUPS=1", "ZEROS3_AG_TARGET=4096")
	rep = MaintReport{}
	mustOK(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &rep))
	if rep.Deferred != "" || rep.RecordGroups == nil {
		t.Fatalf("maintenance did not proceed after the opener closed: %+v", rep)
	}
	l.open()
	l.exact("after maintenance")
	l.verifyOK("after maintenance")
}
