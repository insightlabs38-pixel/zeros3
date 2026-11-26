package main

// FB-01R: the ack watermark (journal/visibility.ack) and the automatic
// recovery it proves. A mutation is acknowledged when its call returned nil;
// fb01Truth records exactly those.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fb01rOtherBoot makes the running boot look like a later one: a host restart.
func fb01rOtherBoot(t testing.TB) {
	t.Helper()
	prev := testBootID
	other := currentBootID()
	other[0] ^= 0xff
	other[15] ^= 0x01
	testBootID = &other
	t.Cleanup(func() { testBootID = prev })
}

func fb01rAckPath(dir string) string { return filepath.Join(dir, "journal", journalWatermarkName) }

func fb01rReadAck(t testing.TB, dir string) *journalWatermark {
	t.Helper()
	w, err := readJournalWatermark(fb01rAckPath(dir))
	if err != nil {
		t.Fatalf("ack watermark: %v", err)
	}
	return w
}

func fb01rRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fb01rCheckRecovery checks an owner open's automatic discard against the
// image it opened: the journal now ends at v and holds image[:v] exactly, the
// quarantine holds image[v:] exactly, and its record says so.
func fb01rCheckRecovery(t testing.TB, dir string, s *Store, image []byte, v int64) {
	t.Helper()
	rec := s.JournalRecovery()
	if rec == nil {
		t.Fatal("no recovery reported")
	}
	if rec.Offset != v || rec.Bytes != int64(len(image))-v {
		t.Fatalf("recovery at %d of %d bytes, want %d of %d", rec.Offset, rec.Bytes, v, int64(len(image))-v)
	}
	if got := fb01rRead(t, filepath.Join(rec.Quarantine, "tail.bin")); !bytes.Equal(got, image[v:]) {
		t.Fatalf("quarantined %d bytes do not match the %d discarded", len(got), len(image)-int(v))
	}
	if got := fb01SHA(fb01Journal(dir)); got != fb01SHABytes(image[:v]) {
		t.Fatal("the journal is not exactly the kept prefix")
	}
	var onDisk tailRecovery
	if err := json.Unmarshal(fb01rRead(t, filepath.Join(rec.Quarantine, "record.json")), &onDisk); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image[v:])
	if onDisk.SHA256 != hex.EncodeToString(sum[:]) || onDisk.Offset != v || onDisk.Proof == "" || onDisk.Damage == nil {
		t.Fatalf("recovery record does not describe the discard: %+v", onDisk)
	}
}

// fb01rTornTailRecovers is the failed-rollback case: torn bytes of an
// unacknowledged group remain past the watermark of a cleanly closed store.
func fb01rTornTailRecovers(t testing.TB, dir string, tr *fb01Truth) {
	t.Helper()
	rep, err := inspectJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Damage == nil || !rep.Recoverable || !rep.SameBoot || rep.WatermarkStatus != "ok" {
		t.Fatalf("inspect: want a recoverable tail in this boot, got %+v", rep)
	}
	restarted, bare := fb01Copy(t, dir), fb01Copy(t, dir)
	image := fb01rRead(t, fb01Journal(dir))
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("an unacknowledged tail in this boot was refused: %v", err)
	}
	fb01rCheckRecovery(t, dir, s, image, rep.ValidEnd)
	if lost, _ := tr.lost(s); lost != 0 {
		t.Fatalf("%d acknowledged mutations lost", lost)
	}
	s.Close()

	// After a host restart in lazy mode, and without a watermark (FB-01), the
	// same bytes are refused, unchanged, and salvage remains the repair.
	func() {
		fb01rOtherBoot(t)
		defer func() { testBootID = nil }()
		if _, err := OpenStore(restarted); !errors.Is(err, errJournalRefused) || !strings.Contains(err.Error(), "host restarted") {
			t.Fatalf("after a restart in lazy mode: want a refusal, got %v", err)
		}
	}()
	os.Remove(fb01rAckPath(bare))
	if _, err := OpenStore(bare); !errors.Is(err, errJournalRefused) {
		t.Fatalf("without a watermark: want a refusal, got %v", err)
	}
	for _, d := range []string{restarted, bare} {
		if got := fb01SHA(fb01Journal(d)); got != fb01SHABytes(image) {
			t.Fatal("a refused open changed the journal")
		}
		if _, err := salvageJournal(d, rep.ValidEnd, true); err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(d)
		if err != nil {
			t.Fatal(err)
		}
		if lost, _ := tr.lost(s); lost != 0 {
			t.Fatalf("%d acknowledged mutations lost after salvage", lost)
		}
		s.Close()
	}
}

func TestFB01RWatermarkEncoding(t *testing.T) {
	w := journalWatermark{Gen: 7, Flags: wmFlagSynced, Boot: [16]byte{1, 2}, Store: storeTag("s"), Offset: 4096, FrameOff: 4000, Seq: 9, LastCRC: 0xdeadbeef, PID: 3, Unix: 5}
	b := w.encode()
	got, ok := decodeJournalWatermark(b)
	if !ok || *got != w {
		t.Fatalf("round trip: %+v %t", got, ok)
	}
	for i := range len(b) {
		c := bytes.Clone(b)
		c[i] ^= 0x10
		if _, ok := decodeJournalWatermark(c); ok {
			t.Fatalf("a flip at byte %d still decodes", i)
		}
	}
	for _, bad := range []journalWatermark{
		{Offset: -1}, {Offset: 10, FrameOff: 11, Seq: 1}, {Offset: 10, FrameOff: -2, Seq: 1}, {Offset: 0, Seq: 1}, {Offset: 30, FrameOff: 0},
	} {
		if _, ok := decodeJournalWatermark(bad.encode()); ok {
			t.Fatalf("an impossible slot decodes: %+v", bad)
		}
	}
	// The newest valid slot counts; a damaged newer one leaves the older.
	path := filepath.Join(t.TempDir(), journalWatermarkName)
	older, newer := w, w
	newer.Gen, newer.Offset = 8, 5000
	file := make([]byte, journalWatermarkPage+journalWatermarkSlot)
	copy(file[journalWatermarkPage:], older.encode())
	copy(file, newer.encode())
	os.WriteFile(path, file, 0o644)
	if r, err := readJournalWatermark(path); err != nil || r.Gen != 8 {
		t.Fatalf("newest slot: %+v %v", r, err)
	}
	file[100] ^= 1
	os.WriteFile(path, file, 0o644)
	if r, err := readJournalWatermark(path); err != nil || r.Gen != 7 {
		t.Fatalf("older slot after damage to the newer: %+v %v", r, err)
	}
	file[journalWatermarkPage+100] ^= 1
	os.WriteFile(path, file, 0o644)
	if _, err := readJournalWatermark(path); !errors.Is(err, errWatermarkUnreadable) {
		t.Fatalf("no valid slot: %v", err)
	}
}

// fb01rSnapshot is a store image taken while a commit group was written but
// not yet synced or acknowledged: the page cache of a process killed there.
type fb01rSnapshot struct {
	dir        string
	tr         *fb01Truth        // acknowledged at the snapshot
	inflight   map[string]string // the group's puts: unacknowledged, so either outcome is right
	start, end int64             // the in-flight group's bytes
	frames     []fb01Frame
	image      []byte
}

// fb01rSnapshotGroup seeds a store (in the current ZEROS3_JOURNAL_WATERMARK
// mode) and snapshots it in the middle of a multi-frame group.
func fb01rSnapshotGroup(t testing.TB) fb01rSnapshot {
	t.Helper()
	dir := t.TempDir()
	tr := fb01Seed(t, dir, 8)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr.put(t, s, "k001", "value 001 v2")
	tr.del(t, s, "k002")
	snap := fb01rSnapshot{tr: tr.clone()}
	gate, held := make(chan struct{}), make(chan struct{})
	var hits int
	var mu sync.Mutex
	var snapErr error
	testHook = func(p string) {
		if p != hookAfterJournalWriteBeforeSync {
			return
		}
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		switch n {
		case 1:
			close(held)
			<-gate
		case 2:
			// Under the journal lock, before the fsync: the group is written,
			// unacknowledged, and the watermark still ends before it.
			snap.start = s.journal.writeOffset
			snap.dir = t.TempDir()
			snapErr = os.CopyFS(snap.dir, os.DirFS(dir))
		}
	}
	defer func() { testHook = nil }()
	errc := make(chan error, 8)
	go func() {
		_, err := s.PutObject("b", "gate", []byte("gate"), "text/plain", nil)
		errc <- err
	}()
	<-held
	kv := [][2]string{{"g0", "group 0"}, {"g1", "group 1"}, {"g2", "group 2"}, {"k003", "value 003 v2"}}
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
	if snapErr != nil || snap.dir == "" {
		t.Fatalf("snapshot: %v", snapErr)
	}
	snap.tr.live["gate"] = "gate" // acknowledged before the group was written
	snap.inflight = map[string]string{}
	for _, p := range kv {
		snap.inflight[p[0]] = p[1]
	}
	snap.image = fb01rRead(t, fb01Journal(snap.dir))
	snap.end = int64(len(snap.image))
	if journalIsGrouped(bytes.NewReader(snap.image)) {
		h := snap.image[snap.start : snap.start+journalGroupBegin]
		if int64(binary.BigEndian.Uint32(h[4:])) != snap.end-snap.start ||
			int(binary.BigEndian.Uint32(h[24:])) != len(kv) {
			t.Fatal("snapshot did not capture the full unacknowledged group")
		}
		return snap
	}
	for _, f := range fb01Frames(snap.image) {
		if f.off >= snap.start {
			snap.frames = append(snap.frames, f)
		}
	}
	if len(snap.frames) < 2 || snap.frames[len(snap.frames)-1].end != snap.end {
		t.Fatalf("snapshot holds %d group frames ending at %d of %d", len(snap.frames), snap.frames[len(snap.frames)-1].end, snap.end)
	}
	if w := fb01rReadAck(t, snap.dir); w.Offset != snap.start {
		t.Fatalf("the snapshot's watermark is at %d, want the group start %d", w.Offset, snap.start)
	}
	return snap
}

type fb01rTrial struct {
	name  string
	image func(sn fb01rSnapshot) []byte
	// outcome per condition: "ok" (opens, nothing discarded), "recover"
	// (opens after discarding the tail), "refuse", "below" (refused:
	// acknowledged history damaged).
	sameBoot, restartLazy, restartSync string
}

func fb01rTrials() []fb01rTrial {
	cut := func(at func(sn fb01rSnapshot) int64) func(sn fb01rSnapshot) []byte {
		return func(sn fb01rSnapshot) []byte { return bytes.Clone(sn.image[:at(sn)]) }
	}
	flip := func(at func(sn fb01rSnapshot) int64) func(sn fb01rSnapshot) []byte {
		return func(sn fb01rSnapshot) []byte {
			b := bytes.Clone(sn.image)
			b[at(sn)] ^= 0x40
			return b
		}
	}
	f := func(i int) func(sn fb01rSnapshot) fb01Frame {
		return func(sn fb01rSnapshot) fb01Frame { return sn.frames[i] }
	}
	return []fb01rTrial{
		// Interrupted writes: every shape a crash before the group's fsync
		// can leave, in one boot or after a restart.
		{"group not written", cut(func(sn fb01rSnapshot) int64 { return sn.start }), "ok", "ok", "ok"},
		{"group written whole, unsynced", func(sn fb01rSnapshot) []byte { return bytes.Clone(sn.image) }, "ok", "ok", "ok"},
		{"torn first header", cut(func(sn fb01rSnapshot) int64 { return sn.start + 7 }), "recover", "refuse", "recover"},
		{"torn first payload", cut(func(sn fb01rSnapshot) int64 { return sn.start + journalHeaderSize + 9 }), "recover", "refuse", "recover"},
		{"torn first crc", cut(func(sn fb01rSnapshot) int64 { return f(0)(sn).end - 1 }), "recover", "refuse", "recover"},
		{"first frame complete", cut(func(sn fb01rSnapshot) int64 { return f(0)(sn).end }), "ok", "ok", "ok"},
		{"torn second frame", cut(func(sn fb01rSnapshot) int64 { return f(1)(sn).off + journalHeaderSize + 3 }), "recover", "refuse", "recover"},
		{"torn last crc", cut(func(sn fb01rSnapshot) int64 { return sn.end - 2 }), "recover", "refuse", "recover"},
		// Shapes only power loss can leave (sectors out of order), never an
		// interrupted write within one boot: past the watermark, so nothing
		// acknowledged is affected, but only sync mode proves that after a
		// restart.
		{"group sector lost, later frames present", func(sn fb01rSnapshot) []byte {
			b := bytes.Clone(sn.image)
			clear(b[f(0)(sn).off+journalHeaderSize : f(0)(sn).end])
			return b
		}, "refuse", "refuse", "recover"},
		{"crc mismatch in the group", flip(func(sn fb01rSnapshot) int64 { return f(1)(sn).off + journalHeaderSize + 2 }), "refuse", "refuse", "recover"},
		// Adversarial: acknowledged history cut or damaged. Always refused.
		{"acknowledged frame cut at a frame boundary", cut(func(sn fb01rSnapshot) int64 {
			fr := fb01Frames(sn.image)
			for i, x := range fr {
				if x.end == sn.start {
					return fr[i].off
				}
			}
			return 0
		}), "below", "below", "below"},
		{"acknowledged frame torn", cut(func(sn fb01rSnapshot) int64 { return sn.start - 3 }), "below", "below", "below"},
		{"acknowledged frame bit flip, group intact", flip(func(sn fb01rSnapshot) int64 { return sn.start - 10 }), "below", "below", "below"},
		{"early acknowledged frame bit flip", flip(func(sn fb01rSnapshot) int64 { return journalHeaderSize + 5 }), "below", "below", "below"},
		{"journal emptied", func(fb01rSnapshot) []byte { return nil }, "below", "below", "below"},
	}
}

// lost counts acknowledged mutations s does not show; an in-flight put may
// have taken effect or not.
func (sn fb01rSnapshot) lost(s *Store) int {
	n := 0
	for k, v := range sn.tr.live {
		_, data, err := s.GetObject("b", k)
		if err != nil || (string(data) != v && string(data) != sn.inflight[k]) {
			n++
		}
	}
	for k := range sn.tr.deleted {
		if _, _, err := s.GetObject("b", k); !errors.Is(err, errNoSuchKey) {
			n++
		}
	}
	return n
}

// fb01rRun opens a copy of the snapshot with the trial's image and checks the
// outcome; a refused open must change nothing, an open must show every
// acknowledged mutation, and a recovery must discard only the tail.
func fb01rRun(t *testing.T, sn fb01rSnapshot, tr fb01rTrial, want string) {
	t.Helper()
	dir := fb01Copy(t, sn.dir)
	image := tr.image(sn)
	if err := os.WriteFile(fb01Journal(dir), image, 0o644); err != nil {
		t.Fatal(err)
	}
	ack := fb01rRead(t, fb01rAckPath(dir))
	// A shared open first: it changes nothing.
	if sh, err := OpenStoreShared(dir); err == nil {
		lost := sn.lost(sh)
		proven := sh.JournalTailProven()
		tail := sh.JournalTail()
		sh.Close()
		if want == "below" || want == "refuse" && tail == nil {
			t.Fatalf("shared open of %q served a view (tail %v)", tr.name, tail)
		}
		if lost != 0 && !(want == "refuse" && tail != nil) {
			t.Fatalf("shared open lost %d acknowledged mutations", lost)
		}
		if want == "recover" && !proven {
			t.Fatal("shared open did not prove its tail unacknowledged")
		}
	} else if want == "ok" || want == "recover" {
		t.Fatalf("shared open refused: %v", err)
	}
	if got := fb01SHA(fb01Journal(dir)); got != fb01SHABytes(image) || !bytes.Equal(fb01rRead(t, fb01rAckPath(dir)), ack) {
		t.Fatal("a shared open changed the journal or the watermark")
	}
	s, err := OpenStore(dir)
	switch want {
	case "ok", "recover":
		if err != nil {
			t.Fatalf("owner open refused: %v", err)
		}
		if lost := sn.lost(s); lost != 0 {
			t.Fatalf("%d acknowledged mutations lost", lost)
		}
		if want == "recover" {
			sc, _, err := scanJournalRange(bytes.NewReader(image), 0, int64(len(image)), 0, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if sc.ValidEnd < sn.start {
				t.Fatalf("recovered below the watermark: %d < %d", sc.ValidEnd, sn.start)
			}
			fb01rCheckRecovery(t, dir, s, image, sc.ValidEnd)
		} else if s.JournalRecovery() != nil {
			t.Fatal("a clean journal reported a recovery")
		}
		s.Close()
		s2, err := OpenStore(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if lost := sn.lost(s2); lost != 0 || s2.JournalRecovery() != nil {
			t.Fatalf("reopen: %d lost, recovery %v", lost, s2.JournalRecovery())
		}
		s2.Close()
	default:
		if err == nil {
			s.Close()
			t.Fatalf("owner open accepted %q", tr.name)
		}
		var wp *watermarkProblem
		if !errors.Is(err, errJournalRefused) || (want == "below") != errors.As(err, &wp) {
			t.Fatalf("want %s, got %v", want, err)
		}
		if got := fb01SHA(fb01Journal(dir)); got != fb01SHABytes(image) {
			t.Fatal("a refused open changed the journal")
		}
	}
}

// TestFB01RInterruptedGroupMatrix: every interrupted-write shape of an
// unacknowledged group recovers automatically where the watermark proves it,
// and every damage to acknowledged history is refused, in lazy and sync mode,
// in the same boot and after a restart.
func TestFB01RInterruptedGroupMatrix(t *testing.T) {
	fb01NoTailWait(t)
	for _, mode := range []string{"lazy", "sync"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_WATERMARK", mode)
			sn := fb01rSnapshotGroup(t)
			if w := fb01rReadAck(t, sn.dir); w.synced() != (mode == "sync") {
				t.Fatalf("watermark synced flag %t in %s mode", w.synced(), mode)
			}
			for _, tr := range fb01rTrials() {
				t.Run(tr.name, func(t *testing.T) {
					t.Run("same boot", func(t *testing.T) { fb01rRun(t, sn, tr, tr.sameBoot) })
					t.Run("after restart", func(t *testing.T) {
						fb01rOtherBoot(t)
						want := tr.restartLazy
						if mode == "sync" {
							want = tr.restartSync
						}
						fb01rRun(t, sn, tr, want)
					})
				})
			}
		})
	}
}

// TestFB01RWithoutWatermark: with ZEROS3_JOURNAL_WATERMARK=off, or a missing,
// unreadable or foreign watermark, opens behave exactly as FB-01's strict
// policy; an owner open with off removes the file.
func TestFB01RWithoutWatermark(t *testing.T) {
	fb01NoTailWait(t)
	sn := fb01rSnapshotGroup(t)
	torn := func(dir string) []byte {
		b := bytes.Clone(sn.image[:sn.start+journalHeaderSize+5])
		os.WriteFile(fb01Journal(dir), b, 0o644)
		return b
	}
	foreign := journalWatermark{Gen: 99, Store: storeTag("another store"), Boot: currentBootID(), Offset: sn.start, FrameOff: -1, Seq: 1}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"off", func(t *testing.T, _ string) { t.Setenv("ZEROS3_JOURNAL_WATERMARK", "off") }},
		{"missing", func(_ *testing.T, dir string) { os.Remove(fb01rAckPath(dir)) }},
		{"unreadable", func(_ *testing.T, dir string) { os.WriteFile(fb01rAckPath(dir), make([]byte, 600), 0o644) }},
		{"foreign", func(_ *testing.T, dir string) { os.WriteFile(fb01rAckPath(dir), foreign.encode(), 0o644) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := fb01Copy(t, sn.dir)
			image := torn(dir)
			c.setup(t, dir)
			if _, err := OpenStore(dir); !errors.Is(err, errJournalRefused) {
				t.Fatalf("want FB-01's refusal, got %v", err)
			}
			if fb01SHA(fb01Journal(dir)) != fb01SHABytes(image) {
				t.Fatal("refusal changed the journal")
			}
			if _, err := OpenStoreShared(dir); !errors.Is(err, errStoreDegraded) {
				t.Fatalf("shared open: want a degraded-view refusal, got %v", err)
			}
			if sh, err := OpenStoreSharedPrefix(dir); err != nil || sh.JournalTail() == nil || sh.JournalTailProven() {
				t.Fatalf("shared prefix open: want an unproven tail, got %v", err)
			} else {
				sh.Close()
			}
			// A clean journal opens; off removes the file, the others replace it.
			os.WriteFile(fb01Journal(dir), sn.image[:sn.start], 0o644)
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			_, err = os.Stat(fb01rAckPath(dir))
			if c.name == "off" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("off left the watermark: %v", err)
				}
				return
			}
			w := fb01rReadAck(t, dir)
			if w.Store != storeTag(s.format.StoreID) || w.Offset != sn.start || !w.clean() {
				t.Fatalf("the replacement watermark: %+v", w)
			}
		})
	}
}

// TestFB01RWatermarkLifecycle: each commit advances the watermark before its
// acknowledgement, a close seals it clean, and an open in this boot writes a
// fresh slot at the journal's end.
func TestFB01RWatermarkLifecycle(t *testing.T) {
	dir := t.TempDir()
	tr := fb01Seed(t, dir, 3)
	w := fb01rReadAck(t, dir)
	st, _ := os.Stat(fb01Journal(dir))
	if !w.clean() || w.Offset != st.Size() || !w.sameBoot() || w.Store != storeTag(fb01rStoreID(t, dir)) {
		t.Fatalf("after a clean close: %+v (journal %d bytes)", w, st.Size())
	}
	// In the same boot an open that changes nothing keeps the clean slot and
	// leaves the file's bytes as they were, through a close as well.
	ackBytes := fb01rRead(t, fb01rAckPath(dir))
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fb01rRead(t, fb01rAckPath(dir)), ackBytes) {
		t.Fatal("an open in the same boot rewrote an identical slot")
	}
	s.Close()
	if !bytes.Equal(fb01rRead(t, fb01rAckPath(dir)), ackBytes) {
		t.Fatal("a close with nothing appended rewrote the clean slot")
	}
	// After a host restart the open writes a fresh slot of this boot.
	func() {
		fb01rOtherBoot(t)
		defer func() { testBootID = nil }()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		opened := fb01rReadAck(t, dir)
		if opened.clean() || opened.Gen <= w.Gen || opened.Offset != w.Offset || opened.PID != uint32(os.Getpid()) || !opened.sameBoot() {
			t.Fatalf("at open after a restart: %+v", opened)
		}
		s.Close()
	}()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if opened := fb01rReadAck(t, dir); opened.Gen <= w.Gen || opened.Offset != w.Offset {
		t.Fatalf("at open: %+v", opened)
	}
	var seen []int64
	testHook = func(p string) {
		if p == hookAfterJournalSync {
			// Fires after the advance, before the acknowledgement.
			if a, err := readJournalWatermark(fb01rAckPath(dir)); err == nil {
				seen = append(seen, a.Offset-s.journal.writeOffset)
			}
		}
	}
	tr.put(t, s, "a", "1")
	tr.put(t, s, "b", "2")
	testHook = nil
	if len(seen) != 2 || seen[0] != 0 || seen[1] != 0 {
		t.Fatalf("watermark behind the acknowledged end: %v", seen)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if c := fb01rReadAck(t, dir); !c.clean() || c.Offset != s.journal.writeOffset {
		t.Fatalf("after close: %+v", c)
	}
}

func fb01rStoreID(t testing.TB, dir string) string {
	t.Helper()
	f, err := readStoreFormat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return f.StoreID
}

// TestFB01RWatermarkWriteFailure: a failed advance removes the watermark and
// poisons the journal without failing the durable commit; if the removal
// fails too, the commit reports failure (it stays durable and replays).
func TestFB01RWatermarkWriteFailure(t *testing.T) {
	for _, c := range []struct {
		name     string
		fault    func(p string, n int) (int, error)
		putFails bool
	}{
		{"advance fails", func(p string, n int) (int, error) {
			if p == faultWatermarkWrite {
				return 0, syscall.EIO
			}
			return 0, nil
		}, false},
		{"advance and removal fail", func(p string, n int) (int, error) {
			if p == faultWatermarkWrite || p == faultWatermarkDrop {
				return 0, syscall.EIO
			}
			return 0, nil
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			tr := fb01Seed(t, dir, 3)
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			testJournalFault = c.fault
			_, perr := s.PutObject("b", "durable", []byte("d"), "text/plain", nil)
			testJournalFault = nil
			if (perr != nil) != c.putFails {
				t.Fatalf("put: %v", perr)
			}
			if perr == nil {
				tr.live["durable"] = "d"
			}
			if _, err := s.PutObject("b", "later", []byte("x"), "text/plain", nil); err == nil {
				t.Fatal("a poisoned journal accepted a later put")
			}
			s.Close()
			_, statErr := os.Stat(fb01rAckPath(dir))
			if !c.putFails && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("the watermark survived a failed advance: %v", statErr)
			}
			s2, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			if lost, _ := tr.lost(s2); lost != 0 {
				t.Fatalf("%d acknowledged mutations lost", lost)
			}
			if _, _, err := s2.GetObject("b", "durable"); err != nil {
				t.Fatalf("the durable commit did not replay: %v", err)
			}
			if w := fb01rReadAck(t, dir); w.Offset != s2.journal.writeOffset {
				t.Fatalf("reopen did not write a fresh watermark: %+v", w)
			}
		})
	}
}

// TestFB01RTailDiscardInterrupted: a fault while the tail is quarantined, and
// before the cut, leaves the journal as it was; the next open recovers.
func TestFB01RTailDiscardInterrupted(t *testing.T) {
	fb01NoTailWait(t)
	sn := fb01rSnapshotGroup(t)
	dir := fb01Copy(t, sn.dir)
	image := bytes.Clone(sn.image[:sn.start+journalHeaderSize+5])
	os.WriteFile(fb01Journal(dir), image, 0o644)
	testJournalFault = func(p string, _ int) (int, error) {
		if p == faultTailDiscard {
			return 0, syscall.EIO
		}
		return 0, nil
	}
	_, err := OpenStore(dir)
	testJournalFault = nil
	if !errors.Is(err, syscall.EIO) || fb01SHA(fb01Journal(dir)) != fb01SHABytes(image) {
		t.Fatalf("an interrupted discard: %v, journal changed %t", err, fb01SHA(fb01Journal(dir)) != fb01SHABytes(image))
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fb01rCheckRecovery(t, dir, s, image, sn.start)
	if lost := sn.lost(s); lost != 0 {
		t.Fatalf("%d acknowledged mutations lost", lost)
	}
}

// TestFB01RInspect reports the watermark and its verdict.
func TestFB01RInspect(t *testing.T) {
	fb01NoTailWait(t)
	sn := fb01rSnapshotGroup(t)
	dir := fb01Copy(t, sn.dir)
	os.WriteFile(fb01Journal(dir), sn.image[:sn.start+9], 0o644)
	rep, err := inspectJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Recoverable || rep.WatermarkStatus != "ok" || !strings.HasPrefix(rep.Verdict, "recoverable:") || rep.Salvage != "" {
		t.Fatalf("inspect: %+v", rep)
	}
	var out strings.Builder
	printJournalReport(&out, rep)
	if !strings.Contains(out.String(), "ack watermark: ok, offset") || !strings.Contains(out.String(), "written in this boot") {
		t.Fatalf("printed report: %s", out.String())
	}
	os.WriteFile(fb01Journal(dir), sn.image[:sn.start-1], 0o644)
	if rep, err = inspectJournal(dir); err != nil {
		t.Fatal(err)
	}
	if rep.Recoverable || rep.WatermarkStatus != "below" || !strings.HasPrefix(rep.Verdict, "refused: acknowledged history damaged") || rep.Salvage == "" {
		t.Fatalf("inspect below the watermark: %+v", rep)
	}
	// Salvage moves the watermark aside with the original.
	rec, err := salvageJournal(dir, rep.ValidEnd, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rec.Quarantine, journalWatermarkName)); err != nil {
		t.Fatalf("salvage did not keep the watermark: %v", err)
	}
	if s, err := OpenStore(dir); err != nil {
		t.Fatalf("open after salvage: %v", err)
	} else {
		s.Close()
	}
}

// TestFB01RHelperProcess runs the CLI in a child: FB01R_HELPER_ARGS holds its
// arguments, one per line.
func TestFB01RHelperProcess(t *testing.T) {
	args := os.Getenv("FB01R_HELPER_ARGS")
	if args == "" {
		return
	}
	os.Args = append([]string{"zeros3"}, strings.Split(args, "\n")...)
	main()
	os.Exit(0)
}

func fb01rCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFB01RHelperProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "FB01R_HELPER_ARGS="+strings.Join(args, "\n"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestFB01RDegradedViews (#8): a shared view that is only the valid prefix of a
// journal with an unaccounted tail cannot pass for a complete store.
// OpenStoreShared refuses it; the reporting commands serve it with a warning;
// verify, export and namespace-checkpoint refuse it. A tail the watermark
// proves unacknowledged is a consistent view and opens normally.
func TestFB01RDegradedViews(t *testing.T) {
	fb01NoTailWait(t)
	sn := fb01rSnapshotGroup(t)
	proven := fb01Copy(t, sn.dir)
	image := sn.image[:sn.start+journalHeaderSize+5]
	os.WriteFile(fb01Journal(proven), image, 0o644)
	degraded := fb01Copy(t, proven)
	os.Remove(fb01rAckPath(degraded))

	s, err := OpenStoreShared(proven)
	if err != nil {
		t.Fatalf("a proven tail: %v", err)
	}
	if s.Degraded() != nil || s.JournalTail() == nil || !s.JournalTailProven() || sn.lost(s) != 0 {
		t.Fatalf("a proven tail: degraded %v", s.Degraded())
	}
	s.Close()

	_, err = OpenStoreShared(degraded)
	var d *journalDamage
	if !errors.Is(err, errStoreDegraded) || !errors.As(err, &d) || d.Offset != sn.start {
		t.Fatalf("OpenStoreShared served a degraded view: %v", err)
	}
	s, err = OpenStoreSharedPrefix(degraded)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.Degraded(), errStoreDegraded) {
		t.Fatalf("the prefix view does not report itself degraded: %v", s.Degraded())
	}
	s.Close()
	if _, err := exportBundleFile(degraded, "any", filepath.Join(t.TempDir(), "b.zs3b"), false); !errors.Is(err, errStoreDegraded) {
		t.Fatalf("export from a degraded view: %v", err)
	}

	for _, c := range []struct {
		args    []string
		fails   bool
		mention string
	}{
		{[]string{"verify", "-store", degraded}, true, "degraded read-only view"},
		{[]string{"namespace-checkpoint", "-store", degraded}, true, "refusing to publish a checkpoint of store: degraded"},
		{[]string{"stats", "-store", degraded}, false, "stats: WARNING: store: degraded read-only view"},
		{[]string{"versions", "-store", degraded, "-bucket", "b", "-key", "k000"}, false, "versions: WARNING"},
		{[]string{"verify", "-store", proven}, false, ""},
	} {
		out, err := fb01rCLI(t, c.args...)
		if (err != nil) != c.fails || !strings.Contains(out, c.mention) {
			t.Fatalf("%v: err %v, output:\n%s", c.args, err, out)
		}
	}
	if fb01SHA(fb01Journal(degraded)) != fb01SHABytes(image) {
		t.Fatal("a read-only command changed the journal")
	}
}
