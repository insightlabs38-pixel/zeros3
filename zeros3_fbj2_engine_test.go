package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestFBJ2GroupedReplay(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	snap := fb01rSnapshotGroup(t)
	for _, shape := range []string{"complete", "footer-cut", "reordered"} {
		t.Run(shape, func(t *testing.T) {
			dir := fb01Copy(t, snap.dir)
			image := bytes.Clone(snap.image)
			switch shape {
			case "footer-cut":
				image = image[:len(image)-1]
			case "reordered":
				clear(image[snap.start+journalGroupBegin : snap.start+journalGroupBegin+128])
			}
			if err := os.WriteFile(fb01Journal(dir), image, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ZEROS3_JOURNAL_FORMAT", "") // the stored feature selects replay
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := snap.tr.clone()
			at := snap.start
			if shape == "complete" {
				for key, val := range snap.inflight {
					want.live[key] = val
				}
				at = snap.end
				if s.JournalRecovery() != nil {
					t.Fatal("complete response-lost group was discarded")
				}
			} else {
				fb01rCheckRecovery(t, dir, s, image, at)
				for _, key := range []string{"g0", "g1", "g2"} {
					if _, _, err := s.GetObject("b", key); !errors.Is(err, errNoSuchKey) {
						t.Fatalf("partial visibility for %s: %v", key, err)
					}
				}
			}
			if !slices.Contains(s.formatSnapshot().Features, journalGroupFeature) {
				t.Fatal("missing format gate")
			}
			if _, err := os.Stat(fb01rAckPath(dir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("group writer used sidecar: %v", err)
			}
			if lost, _ := want.lost(s); lost != 0 {
				t.Fatalf("lost %d expected keys or deletions", lost)
			}
			if s.journal.writeOffset != at {
				t.Fatalf("wrong group boundary: %d, want %d", s.journal.writeOffset, at)
			}
			want.put(t, s, "restart", "new exact content")
			if err := s.nsConsolidate(true); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.ns.gen == nil {
				t.Fatal("grouped checkpoint was not adopted")
			}
			if lost, _ := want.lost(s); lost != 0 {
				t.Fatalf("checkpoint lost %d expected keys or deletions", lost)
			}
			report, err := s.Verify(true)
			if err != nil || !report.OK() {
				t.Fatalf("deep verify: %+v %v", report, err)
			}
		})
	}
}

func TestFBJ2GroupedDamageRefuses(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	truth := fb01Seed(t, dir, 3)
	image := fb01rRead(t, fb01Journal(dir))
	bad := bytes.Clone(image)
	bad[journalGroupFile+journalGroupBegin+4] ^= 1 // damage committed history before later complete groups
	if err := os.WriteFile(fb01Journal(dir), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := inspectJournal(dir)
	if err != nil || rep.Damage == nil || rep.Damage.LaterValid < 0 || rep.Recoverable {
		t.Fatalf("inspect: %+v %v", rep, err)
	}
	if s, err := OpenStore(dir); !errors.Is(err, errJournalRefused) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("accepted committed damage: %v", err)
	}
	if !bytes.Equal(fb01rRead(t, fb01Journal(dir)), bad) {
		t.Fatal("refusal rewrote evidence")
	}
	if err := os.WriteFile(fb01Journal(dir), image, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if lost, _ := truth.lost(s); lost != 0 {
		t.Fatalf("clean control lost %d items", lost)
	}
}

func TestFBJ2GroupedSyncFailure(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	truth := fb01Seed(t, dir, 2)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := fb01SHA(fb01Journal(dir))
	testJournalFault = func(point string, _ int) (int, error) {
		if point == faultJournalSync {
			return 0, syscall.EIO
		}
		return 0, nil
	}
	defer func() { testJournalFault = nil }()
	if _, err := s.PutObject("b", "unsynced", []byte("uncertain"), "text/plain", nil); !errors.Is(err, syscall.EIO) {
		t.Fatalf("unexpected write outcome: %v", err)
	}
	if _, err := s.PutObject("b", "next", []byte("next"), "text/plain", nil); err == nil || !strings.Contains(err.Error(), "poisoned") {
		t.Fatalf("continued after durability failure: %v", err)
	}
	if got := fb01SHA(fb01Journal(dir)); got != before {
		t.Fatal("successful rollback changed the old log")
	}
	testJournalFault = nil
	s.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if lost, _ := truth.lost(s); lost != 0 {
		t.Fatalf("lost %d acknowledged objects", lost)
	}
	for _, key := range []string{"unsynced", "next"} {
		if _, _, err := s.GetObject("b", key); !errors.Is(err, errNoSuchKey) {
			t.Fatalf("%s became visible: %v", key, err)
		}
	}
}

func TestFBJ2GroupedFeatureGate(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "")
	dir := t.TempDir()
	truth := fb01Seed(t, dir, 1)
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.journal.group != nil {
		t.Fatal("an existing v1 store was migrated")
	}
	if lost, _ := truth.lost(s); lost != 0 {
		t.Fatalf("lost %d v1 objects", lost)
	}
	s.Close()

	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	id := newUUIDv7()
	if _, err := initializeGroupedJournal(empty, id); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "")
	if _, err := OpenStore(empty); err == nil || !strings.Contains(err.Error(), "unfinished grouped initialization") {
		t.Fatalf("legacy opener overwrote the pending format: %v", err)
	}
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	s, err = OpenStore(empty)
	if err != nil {
		t.Fatal(err)
	}
	if s.formatSnapshot().StoreID != id || s.journal.group == nil {
		t.Fatal("initialization retry lost the log identity")
	}
	s.Close()
	if err := os.Remove(fb01Journal(empty)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(empty); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing grouped log was recreated: %v", err)
	}
}

func FuzzFBJ2GroupedDecode(f *testing.F) {
	header := journalGroupFileHeader("018f6849-5b29-7000-8000-000000000001")
	_, pos, err := journalGroupHeader(bytes.NewReader(header))
	if err != nil {
		f.Fatal(err)
	}
	body := make([]byte, journalGroupBegin)
	body = append(body, v2Frame(1, recordTypePutObjectRoot, []byte("example"))...)
	good, _ := journalSealGroup(body, pos, 1)
	f.Add(good)
	f.Add(good[:len(good)-1])
	f.Add(good[:8])
	f.Add(make([]byte, journalGroupBegin+journalGroupFooter))
	f.Fuzz(func(t *testing.T, group []byte) {
		if len(group) < journalGroupBegin || len(group) > 1<<20 {
			return
		}
		size, count, ok := journalGroupGeometry(group[:journalGroupBegin], pos)
		if !ok || size != len(group) {
			return
		}
		next, ok := journalCheckGroup(group, pos, count)
		if ok && (next.end != pos.end+int64(len(group)) || next.number != pos.number+1 || next.sequence != uint64(count)) {
			t.Fatalf("decoder did not advance to the checked group boundary: %+v", next)
		}
	})
}

// The fingerprint comes from an independent CRC32C and SHA-256 encoding of
// the fixed file header, two v1 records, and the first group footer.
func TestFBJ2GroupedGolden(t *testing.T) {
	header := journalGroupFileHeader("018f6849-5b29-7000-8000-000000000001")
	_, genesis, err := journalGroupHeader(bytes.NewReader(header))
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]byte, journalGroupBegin)
	frames = append(frames, v2Frame(1, recordTypePutObjectRoot, []byte("alpha"))...)
	frames = append(frames, v2Frame(2, recordTypePutObjectRoot, []byte("beta"))...)
	group, _ := journalSealGroup(frames, genesis, 2)
	image := append(header, group...)
	const fingerprint = "cf7aca40ffea3ce4ca6c7d248a37f962fd8351e845d38118bd8457337e39edb7"
	if got := fb01SHABytes(image); got != fingerprint {
		t.Fatalf("group wire fingerprint: %s", got)
	}
	var payloads []string
	scan, _, applyErr, err := scanJournalGroups(bytes.NewReader(image), 0, int64(len(image)), 0, false, func(r journalRecord) error {
		payloads = append(payloads, string(r.payload))
		return nil
	})
	if err != nil || applyErr != nil || scan.Damage != nil || !slices.Equal(payloads, []string{"alpha", "beta"}) {
		t.Fatalf("golden replay: %+v, %v, %v, %v", scan, applyErr, err, payloads)
	}
}

func TestFBJ2GroupedCheckpointAheadRefuses(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	truth := fb01Seed(t, dir, 1)
	old := fb01rRead(t, fb01Journal(dir))
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	truth.put(t, s, "covered", "captured by checkpoint")
	if err := s.nsConsolidate(true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.WriteFile(fb01Journal(dir), old, 0o644); err != nil {
		t.Fatal(err)
	}
	open, err := OpenStore(dir)
	if open != nil {
		open.Close()
	}
	if checkpointKind(err) != checkpointContradicts {
		t.Fatalf("accepted a journal cut behind its checkpoint: %v", err)
	}
	if !bytes.Equal(fb01rRead(t, fb01Journal(dir)), old) {
		t.Fatal("refusal changed the retained prefix")
	}
}
