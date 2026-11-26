package main

// FB-01 M-A1 recovery tests: strict open, checkpoint refusal, writer rollback,
// inspect and salvage. Truth is kept outside the store (fb01Truth).

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fb01NoTailWait makes shared opens give up on an incomplete tail at once.
func fb01NoTailWait(t testing.TB) {
	r, w := journalTailRetries, journalTailRetryWait
	journalTailRetries, journalTailRetryWait = 0, 0
	t.Cleanup(func() { journalTailRetries, journalTailRetryWait = r, w })
}

// fb01PutGroup commits kv as one multi-frame journal group: a gate put holds
// the journal between its write and its fsync while the others queue. It
// returns the group's journal range.
func fb01PutGroup(t testing.TB, s *Store, tr *fb01Truth, gateKey string, kv [][2]string) (start, end int64) {
	t.Helper()
	gate, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHook = func(p string) {
		if p == hookAfterJournalWriteBeforeSync {
			once.Do(func() { close(held); <-gate })
		}
	}
	defer func() { testHook = nil }()
	s.journal.mu.Lock()
	pre := s.journal.writeOffset
	s.journal.mu.Unlock()
	errc := make(chan error, len(kv)+1)
	go func() {
		_, err := s.PutObject("b", gateKey, []byte("gate"), "text/plain", nil)
		errc <- err
	}()
	<-held
	for _, p := range kv {
		go func() {
			_, err := s.PutObject("b", p[0], []byte(p[1]), "text/plain", nil)
			errc <- err
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		s.commits.mu.Lock()
		n := len(s.commits.q)
		s.commits.mu.Unlock()
		if n == len(kv) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d puts queued", n, len(kv))
		}
	}
	close(gate)
	for range len(kv) + 1 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	tr.live[gateKey] = "gate"
	for _, p := range kv {
		tr.live[p[0]] = p[1]
	}
	s.journal.mu.Lock()
	end = s.journal.writeOffset
	s.journal.mu.Unlock()
	d, err := os.ReadFile(fb01Journal(s.root))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fb01Frames(d[:end]) {
		if f.off == pre {
			return f.end, end // the group follows the gate's own frame
		}
	}
	t.Fatal("gate frame not found")
	return 0, 0
}

// fb01Rich builds a store with versions, deletes, a multi-frame group and a
// close/reopen in between, returning its truth.
func fb01Rich(t testing.TB, dir string) (*fb01Truth, int64, int64) {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	tr := newFB01Truth()
	for i := range 12 {
		tr.put(t, s, fmt.Sprintf("k%02d", i), fmt.Sprintf("v1-%02d", i))
	}
	for i := range 4 {
		tr.put(t, s, fmt.Sprintf("k%02d", i), fmt.Sprintf("v2-%02d", i))
	}
	tr.del(t, s, "k05")
	tr.del(t, s, "k06")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	tr.put(t, s, "k06", "v3-06") // a put after a delete
	var kv [][2]string
	for i := range 6 {
		kv = append(kv, [2]string{fmt.Sprintf("g%02d", i), fmt.Sprintf("group-%02d", i)})
	}
	gs, ge := fb01PutGroup(t, s, tr, "gate", kv)
	tr.put(t, s, "k07", "v2-07")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return tr, gs, ge
}

func fb01Opener(owner bool) func(string) (*Store, error) {
	if owner {
		return OpenStore
	}
	return OpenStoreShared
}

func fb01ModeName(owner bool) string {
	if owner {
		return "owner"
	}
	return "shared"
}

// TestFB01CleanJournalReopensUnchanged: a clean journal opens without a single
// byte, size or timestamp change, for owner and shared opens, with and without
// the checkpoint.
func TestFB01CleanJournalReopensUnchanged(t *testing.T) {
	dir := t.TempDir()
	tr, _, _ := fb01Rich(t, dir)
	jp := fb01Journal(dir)
	before, err := os.Stat(jp)
	if err != nil {
		t.Fatal(err)
	}
	sum := fb01SHA(jp)
	for _, owner := range []bool{true, false} {
		for _, ck := range []bool{true, false} {
			time.Sleep(10 * time.Millisecond) // a rewrite would move the mtime
			s, err := openStore(dir, owner, ck)
			if err != nil {
				t.Fatalf("%s checkpoint=%v: clean journal refused: %v", fb01ModeName(owner), ck, err)
			}
			if lost, _ := tr.lost(s); lost != 0 {
				t.Fatalf("%s checkpoint=%v: %d acknowledged mutations lost", fb01ModeName(owner), ck, lost)
			}
			s.Close()
			after, _ := os.Stat(jp)
			if fb01SHA(jp) != sum || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("%s checkpoint=%v: a clean open changed the journal", fb01ModeName(owner), ck)
			}
		}
	}
}

// TestFB01FlipSweep flips every bit of every frame header and CRC, and a
// sample of payload bits, in a journal with versions, deletes and a group.
// Each open must refuse (changing nothing) or show every acknowledged
// mutation. Any silent loss fails.
func TestFB01FlipSweep(t *testing.T) {
	fb01NoTailWait(t)
	base := t.TempDir()
	tr, _, _ := fb01Rich(t, base)
	d0, err := os.ReadFile(fb01Journal(base))
	if err != nil {
		t.Fatal(err)
	}
	frames := fb01Frames(d0)
	tally := map[string]int{}
	work := t.TempDir()
	if err := os.CopyFS(work, os.DirFS(base)); err != nil {
		t.Fatal(err)
	}
	try := func(where string, at int64, bit uint) {
		d := append([]byte(nil), d0...)
		d[at] ^= 1 << bit
		if err := os.WriteFile(fb01Journal(work), d, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, owner := range []bool{false, true} {
			s, err := fb01Opener(owner)(work)
			mode := fb01ModeName(owner)
			if err != nil {
				if !errors.Is(err, errJournalRefused) && !strings.Contains(err.Error(), "replay failed") {
					t.Fatalf("%s byte %d bit %d %s: refusal is not classified: %v", where, at, bit, mode, err)
				}
				if got := fb01SHA(fb01Journal(work)); got != fb01SHAOf(d) {
					t.Fatalf("%s byte %d bit %d %s: a refused open changed the journal", where, at, bit, mode)
				}
				tally[mode+" REFUSE"]++
				continue
			}
			lost, _ := tr.lost(s)
			tail := s.JournalTail()
			s.Close()
			switch {
			case lost == 0:
				tally[mode+" OK"]++
			case !owner && tail != nil:
				tally[mode+" PREFIX"]++
			default:
				t.Fatalf("%s byte %d bit %d %s: silent loss of %d acknowledged mutations", where, at, bit, mode, lost)
			}
			if owner {
				// An owner that opened must not have cut anything.
				if got := fb01SHA(fb01Journal(work)); got != fb01SHAOf(d) {
					t.Fatalf("%s byte %d bit %d: owner changed a journal it accepted", where, at, bit)
				}
			}
		}
	}
	n := 0
	for _, f := range frames {
		for b := f.off; b < f.off+journalHeaderSize; b++ {
			for bit := range uint(8) {
				try("header", b, bit)
				n++
			}
		}
		for b := f.end - 4; b < f.end; b++ {
			for bit := range uint(8) {
				try("crc", b, bit)
				n++
			}
		}
		for _, b := range []int64{f.off + journalHeaderSize, (f.off + f.end) / 2, f.end - 5} {
			try("payload", b, uint(b%8))
			n++
		}
	}
	t.Logf("flip sweep: %d flips over %d frames: %v", n, len(frames), tally)
	b, _ := json.Marshal(map[string]any{"frames": len(frames), "flips": n, "outcomes": tally})
	fb01Emit(t, fb01Result{Test: "M-A1-flip-sweep", Case: string(b), Mode: "owner+shared", Outcome: "OK", Acked: tr.acked(), Preserved: true})
}

func fb01SHAOf(d []byte) string { return fb01SHABytes(d) }

// TestFB01ConflictingCheckpoint covers every checkpoint state an open refuses:
// a damaged file, a foreign one, and a journal prefix it no longer matches,
// with the checkpoint in use and with ZEROS3_NAMESPACE_CHECKPOINT=off.
func TestFB01ConflictingCheckpoint(t *testing.T) {
	fb01NoTailWait(t)
	base := t.TempDir()
	tr := fb01SeedCheckpoint(t, base, 20, 10)
	ck := filepath.Join("journal", nsGenName)
	good, err := os.ReadFile(filepath.Join(base, ck))
	if err != nil {
		t.Fatal(err)
	}
	d0, _ := os.ReadFile(fb01Journal(base))
	fr := fb01Frames(d0)
	other := t.TempDir()
	fb01SeedCheckpoint(t, other, 5, 1)
	foreign, _ := os.ReadFile(filepath.Join(other, ck))
	cases := []struct {
		name     string
		mutate   func(dir string)
		kind     string
		offWorks bool // the off-mode witness sees it too
	}{
		{"checkpoint body bit flip", func(dir string) {
			b := append([]byte(nil), good...)
			b[len(b)/3] ^= 4
			os.WriteFile(filepath.Join(dir, ck), b, 0o644)
		}, checkpointDamaged, false},
		{"checkpoint truncated", func(dir string) { os.WriteFile(filepath.Join(dir, ck), good[:len(good)/2], 0o644) }, checkpointDamaged, false},
		{"foreign checkpoint", func(dir string) { os.WriteFile(filepath.Join(dir, ck), foreign, 0o644) }, checkpointForeign, false},
		{"journal prefix changed under the checkpoint", func(dir string) {
			d := append([]byte(nil), d0...)
			f := fr[3]
			d[f.off+journalHeaderSize+2] ^= 1 // bit rot under the checkpoint
			os.WriteFile(fb01Journal(dir), d, 0o644)
		}, checkpointContradicts, false},
		{"journal cut at a frame boundary below the checkpoint", func(dir string) { os.Truncate(fb01Journal(dir), fr[10].off) }, checkpointContradicts, true},
	}
	for _, c := range cases {
		for _, useCk := range []bool{true, false} {
			for _, owner := range []bool{false, true} {
				dir := fb01Copy(t, base)
				c.mutate(dir)
				mode := fmt.Sprintf("%s checkpoint=%v", fb01ModeName(owner), useCk)
				r := fb01Open(t, dir, tr, "M-A1-checkpoint", c.name, mode, func(d string) (*Store, error) { return openStore(d, owner, useCk) })
				fb01Must(t, r)
				if useCk || c.offWorks {
					if r.Outcome != "REFUSE" || !strings.Contains(r.Err, "cause checkpoint-"+c.kind) || !strings.Contains(r.Err, "refused at offset") && c.kind != checkpointDamaged {
						t.Fatalf("%s | %s: want a %s refusal, got %s %s", c.name, mode, c.kind, r.Outcome, r.Err)
					}
				}
			}
		}
		// Salvage moves the checkpoint aside (the journal cut also drops the
		// frames past it); the next open rebuilds from the journal.
		dir := fb01Copy(t, base)
		c.mutate(dir)
		rep, err := inspectJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		cut := rep.Bytes
		if rep.Damage != nil {
			cut = rep.Damage.Offset
		}
		if rep.Damage == nil && rep.ValidEnd != rep.Bytes {
			t.Fatalf("%s: inspect disagrees with itself", c.name)
		}
		if _, err := salvageJournal(dir, cut, true); err != nil {
			t.Fatalf("%s: salvage: %v", c.name, err)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatalf("%s: open after salvage: %v", c.name, err)
		}
		s.Close()
	}
}

// TestFB01PartialMultiframeGroup cuts a journal inside its final multi-frame
// group, as a crash before the group's fsync leaves it. That group was never
// acknowledged, but v1 cannot prove it: the owner refuses, inspect names the
// offset, and an explicit salvage at the group start recovers every
// acknowledged mutation.
func TestFB01PartialMultiframeGroup(t *testing.T) {
	fb01NoTailWait(t)
	base := t.TempDir()
	tr, gs, ge := fb01Rich(t, base)
	// Drop k07, written after the group, so the group is the journal's tail.
	d0, _ := os.ReadFile(fb01Journal(base))
	fr := fb01Frames(d0)
	last := fr[len(fr)-1]
	if last.end != int64(len(d0)) || last.off != ge {
		t.Fatalf("fixture: last frame %d..%d, group end %d", last.off, last.end, ge)
	}
	if err := os.Truncate(fb01Journal(base), ge); err != nil {
		t.Fatal(err)
	}
	delete(tr.live, "k07")
	tr.live["k07"] = "v1-07"
	acked := tr.clone()
	for i := range 6 {
		delete(acked.live, fmt.Sprintf("g%02d", i)) // the torn group: never acknowledged
	}
	var inGroup []fb01Frame
	for _, f := range fr {
		if f.off >= gs && f.end <= ge {
			inGroup = append(inGroup, f)
		}
	}
	if len(inGroup) != 6 {
		t.Fatalf("group has %d frames, want 6", len(inGroup))
	}
	for _, cut := range []int64{gs + 7, inGroup[2].off, inGroup[2].off + 30, inGroup[5].end - 1} {
		dir := fb01Copy(t, base)
		if err := os.Truncate(fb01Journal(dir), cut); err != nil {
			t.Fatal(err)
		}
		for _, owner := range []bool{false, true} {
			r := fb01Open(t, dir, acked, "M-A1-partial-group", fmt.Sprintf("cut at %d inside group %d..%d", cut, gs, ge), fb01ModeName(owner), fb01Opener(owner))
			fb01Must(t, r)
			if owner && cut != inGroup[2].off && r.Outcome != "REFUSE" {
				t.Fatalf("owner accepted a torn group tail at %d: %s", cut, r.Outcome)
			}
		}
		rep, err := inspectJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cut == inGroup[2].off {
			if rep.Damage != nil {
				t.Fatalf("a cut at a frame boundary is not damage in v1: %+v", rep.Damage)
			}
			continue // S6: indistinguishable from a clean journal
		}
		if rep.Damage == nil || rep.Confidence != "ambiguous" {
			t.Fatalf("inspect at cut %d: %+v", cut, rep)
		}
		if _, err := salvageJournal(dir, gs, false); err == nil {
			t.Fatal("salvage ran without -i-understand-data-loss")
		}
		if _, err := salvageJournal(dir, gs+1, true); err == nil {
			t.Fatal("salvage accepted a cut that is not a frame boundary")
		}
		rec, err := salvageJournal(dir, gs, true)
		if err != nil {
			t.Fatal(err)
		}
		if fb01SHA(filepath.Join(rec.Quarantine, "visibility.log")) != rec.OriginalSHA256 || rec.KeptBytes != gs {
			t.Fatalf("salvage record does not match: %+v", rec)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatalf("open after salvage: %v", err)
		}
		lost, _ := acked.lost(s)
		s.Close()
		if lost != 0 {
			t.Fatalf("salvage at the group start lost %d acknowledged mutations", lost)
		}
	}
}

// TestFB01SalvageNeedsOfflineStore: salvage takes the store's locks.
func TestFB01SalvageNeedsOfflineStore(t *testing.T) {
	dir := t.TempDir()
	fb01Seed(t, dir, 3)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, _ := os.Stat(fb01Journal(dir))
	if _, err := salvageJournal(dir, st.Size(), true); err == nil {
		t.Fatal("salvage ran beside an owner")
	}
}

// TestFB01WriterRollback: a failed append (short write, failed fsync) leaves
// no torn tail behind, so a clean close reopens without a refusal and with
// every acknowledged mutation; if the rollback itself fails, the next open
// refuses until salvage.
func TestFB01WriterRollback(t *testing.T) {
	fb01NoTailWait(t)
	for _, c := range []struct {
		name         string
		fault        func(point string, size int) (int, error)
		reopenRefuse bool
		errs         []error // the put's error matches each
		report       string  // ...and names the rollback outcome
	}{
		{"ENOSPC short write", func(p string, n int) (int, error) {
			if p == faultJournalWrite {
				return n / 2, syscall.ENOSPC
			}
			return 0, nil
		}, false, []error{syscall.ENOSPC}, "rolled back to offset"},
		{"EIO on fsync", func(p string, n int) (int, error) {
			if p == faultJournalSync {
				return 0, syscall.EIO
			}
			return 0, nil
		}, false, []error{syscall.EIO}, "rolled back to offset"},
		{"short write and failed rollback", func(p string, n int) (int, error) {
			switch p {
			case faultJournalWrite:
				return n / 2, syscall.ENOSPC
			case faultJournalRollback:
				return 0, syscall.EIO
			}
			return 0, nil
		}, true, []error{syscall.ENOSPC, syscall.EIO}, "also failed, so they may remain on disk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			tr := fb01Seed(t, dir, 5)
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			testJournalFault = c.fault
			_, perr := s.PutObject("b", "failed", []byte("never acknowledged"), "text/plain", nil)
			testJournalFault = nil
			if perr == nil {
				t.Fatal("the faulted put was acknowledged")
			}
			for _, want := range c.errs {
				if !errors.Is(perr, want) {
					t.Fatalf("the put's error %q does not match %v", perr, want)
				}
			}
			if !strings.Contains(perr.Error(), c.report) {
				t.Fatalf("the put's error %q does not report %q", perr, c.report)
			}
			_, err = s.PutObject("b", "after", []byte("x"), "text/plain", nil)
			if err == nil {
				t.Fatal("a poisoned journal accepted a later put")
			}
			if !strings.Contains(err.Error(), c.report) {
				t.Fatalf("a later put's error %q does not carry the rollback outcome %q", err, c.report)
			}
			s.Close()
			s2, err := OpenStore(dir)
			if c.reopenRefuse {
				if err == nil || !errors.Is(err, errJournalRefused) {
					t.Fatalf("want a refusal of the torn tail, got %v", err)
				}
				rep, _ := inspectJournal(dir)
				if _, err := salvageJournal(dir, rep.ValidEnd, true); err != nil {
					t.Fatal(err)
				}
				if s2, err = OpenStore(dir); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatalf("reopen after a rolled-back append: %v", err)
			}
			defer s2.Close()
			if lost, _ := tr.lost(s2); lost != 0 {
				t.Fatalf("%d acknowledged mutations lost", lost)
			}
			if _, _, err := s2.GetObject("b", "failed"); !errors.Is(err, errNoSuchKey) {
				t.Fatalf("the unacknowledged put is visible: %v", err)
			}
		})
	}
}

// TestFB01FaultSeams: an open that fails at its fault point changes nothing,
// and a failed checkpoint publication leaves the published one in force.
func TestFB01FaultSeams(t *testing.T) {
	dir := t.TempDir()
	tr := fb01SeedCheckpoint(t, dir, 6, 3)
	sum := fb01SHA(fb01Journal(dir))
	ck := fb01SHA(filepath.Join(dir, "journal", nsGenName))
	testJournalFault = func(p string, _ int) (int, error) {
		if p == faultJournalOpen {
			return 0, syscall.EIO
		}
		return 0, nil
	}
	_, err := OpenStore(dir)
	testJournalFault = nil
	if err == nil || fb01SHA(fb01Journal(dir)) != sum {
		t.Fatalf("faulted open: %v, journal changed=%v", err, fb01SHA(fb01Journal(dir)) != sum)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr.put(t, s, "more", "after the checkpoint")
	testJournalFault = func(p string, _ int) (int, error) {
		if p == faultCheckpointWrite {
			return 0, syscall.ENOSPC
		}
		return 0, nil
	}
	err = s.nsConsolidate(true)
	testJournalFault = nil
	if err == nil {
		t.Fatal("a faulted checkpoint publication reported success")
	}
	s.Close()
	if fb01SHA(filepath.Join(dir, "journal", nsGenName)) != ck {
		t.Fatal("a failed publication replaced the checkpoint")
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if lost, _ := tr.lost(s); lost != 0 {
		t.Fatalf("%d acknowledged mutations lost", lost)
	}
}

// TestFB01InspectReportsEvidence: inspect names the earliest invalid offset,
// later valid frames, the corrected length and the confidence, and changes
// nothing.
func TestFB01InspectReportsEvidence(t *testing.T) {
	base := t.TempDir()
	fb01Seed(t, base, 20)
	d0, _ := os.ReadFile(fb01Journal(base))
	fr := fb01Frames(d0)
	dir := fb01Copy(t, base)
	d := append([]byte(nil), d0...)
	f := fr[5]
	binary.BigEndian.PutUint32(d[f.off+16:], f.plen^(1<<20))
	os.WriteFile(fb01Journal(dir), d, 0o644)
	rep, err := inspectJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	dm := rep.Damage
	if dm == nil || dm.Offset != f.off || dm.Seq != f.seq || dm.LaterValid != fr[6].off || dm.CorrectLen != int64(f.plen) || rep.Confidence != "high" {
		t.Fatalf("inspect: %+v %+v", rep, dm)
	}
	if fb01SHA(fb01Journal(dir)) != fb01SHABytes(d) {
		t.Fatal("inspect changed the journal")
	}
	var buf strings.Builder
	printJournalReport(&buf, rep)
	for _, want := range []string{"earliest invalid offset", "later valid frames: yes", "length field is damaged", "confidence high", "salvage"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("report lacks %q:\n%s", want, buf.String())
		}
	}
}

// TestFB01SharedOpenDuringWrites races read-only opens against a live owner.
// A frame the owner is still writing is an incomplete tail to the reader: it
// must neither refuse for long nor lose an acknowledged object, and the
// owner's journal is never touched by a reader.
func TestFB01SharedOpenDuringWrites(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	acked := map[string]string{}
	stop := make(chan struct{})
	done := make(chan error, 4)
	for g := range 4 {
		go func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					done <- nil
					return
				default:
				}
				k, v := fmt.Sprintf("w%d-%05d", g, i), strings.Repeat(fmt.Sprintf("%d.", i), 1+i%300)
				if _, err := w.PutObject("b", k, []byte(v), "text/plain", nil); err != nil {
					done <- err
					return
				}
				mu.Lock()
				acked[k] = v
				mu.Unlock()
			}
		}()
	}
	opens, refused := 0, 0
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); opens++ {
		mu.Lock()
		before := make(map[string]string, len(acked))
		for k, v := range acked {
			before[k] = v
		}
		mu.Unlock()
		r, err := OpenStoreShared(dir)
		if err != nil {
			if !errors.Is(err, errJournalRefused) {
				t.Fatal(err)
			}
			refused++
			continue
		}
		for k, v := range before {
			if _, got, err := r.GetObject("b", k); err != nil || string(got) != v {
				t.Fatalf("open %d: acknowledged %s not visible to a reader: %v", opens, k, err)
			}
		}
		r.Close()
	}
	close(stop)
	for range 4 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d read-only opens during %d acknowledged puts, %d refused", opens, len(acked), refused)
	if refused > opens/10 {
		t.Fatalf("%d of %d read-only opens refused a live writer's tail", refused, opens)
	}
}
