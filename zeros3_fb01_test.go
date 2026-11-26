package main

// FB-01 (M-A0/M-A1) journal recovery harness. Acknowledged state is recorded
// outside the store: a mutation is acknowledged when its call returned nil.
// Results go to FB01_RESULTS (JSONL, appended) labelled with FB01_BUILD.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fb01Frame struct {
	off, end int64
	seq      uint64
	plen     uint32
}

// fb01Frames parses the complete frames of a well-formed journal image.
func fb01Frames(d []byte) []fb01Frame {
	var out []fb01Frame
	for off := int64(0); off+journalHeaderSize <= int64(len(d)); {
		pl := binary.BigEndian.Uint32(d[off+16 : off+20])
		end := off + journalHeaderSize + int64(pl) + 4
		if end > int64(len(d)) {
			break
		}
		out = append(out, fb01Frame{off: off, end: end, seq: binary.BigEndian.Uint64(d[off+8 : off+16]), plen: pl})
		off = end
	}
	return out
}

func fb01Recrc(d []byte, f fb01Frame) {
	body := d[f.off : f.off+journalHeaderSize+int64(f.plen)]
	binary.BigEndian.PutUint32(d[f.off+journalHeaderSize+int64(f.plen):], crc32.Checksum(body, castagnoliTable))
}

func fb01Journal(dir string) string { return filepath.Join(dir, "journal", "visibility.log") }

// fb01Truth is the acknowledged state, kept outside the store.
type fb01Truth struct {
	live    map[string]string // key -> latest acknowledged value
	deleted map[string]bool   // acknowledged deletes not followed by a put
}

func newFB01Truth() *fb01Truth {
	return &fb01Truth{live: map[string]string{}, deleted: map[string]bool{}}
}

func (tr *fb01Truth) clone() *fb01Truth {
	c := newFB01Truth()
	for k, v := range tr.live {
		c.live[k] = v
	}
	for k := range tr.deleted {
		c.deleted[k] = true
	}
	return c
}

func (tr *fb01Truth) put(t testing.TB, s *Store, key, val string) {
	t.Helper()
	if _, err := s.PutObject("b", key, []byte(val), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	tr.live[key] = val
	delete(tr.deleted, key)
}

func (tr *fb01Truth) del(t testing.TB, s *Store, key string) {
	t.Helper()
	if err := s.DeleteObject("b", key); err != nil {
		t.Fatal(err)
	}
	delete(tr.live, key)
	tr.deleted[key] = true
}

// lost counts acknowledged mutations the store does not show: a live key
// missing or stale, or a deleted key visible again.
func (tr *fb01Truth) lost(s *Store) (lost, readable int) {
	for k, v := range tr.live {
		_, data, err := s.GetObject("b", k)
		if err == nil && string(data) == v {
			readable++
		} else {
			lost++
		}
	}
	for k := range tr.deleted {
		if _, _, err := s.GetObject("b", k); !errors.Is(err, errNoSuchKey) {
			lost++
		}
	}
	return lost, readable
}

func (tr *fb01Truth) acked() int { return len(tr.live) + len(tr.deleted) }

func fb01Seed(t testing.TB, dir string, n int) *fb01Truth {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	tr := newFB01Truth()
	for i := 0; i < n; i++ {
		tr.put(t, s, fmt.Sprintf("k%03d", i), fmt.Sprintf("value %03d", i))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return tr
}

func fb01Copy(t testing.TB, src string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func fb01SHABytes(d []byte) string {
	sum := sha256.Sum256(d)
	return hex.EncodeToString(sum[:])
}

func fb01SHA(path string) string {
	d, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(d)
	return hex.EncodeToString(sum[:])
}

// fb01Result is one JSONL row.
type fb01Result struct {
	Build          string `json:"build"`
	Test           string `json:"test"`
	Case           string `json:"case"`
	Mode           string `json:"mode"`
	Outcome        string `json:"outcome"` // OK, LOSS (silent), REFUSE
	Acked          int    `json:"acked"`
	Readable       int    `json:"readable"`
	Lost           int    `json:"lost"`
	JournalBefore  int64  `json:"journal_bytes_before"`
	JournalAfter   int64  `json:"journal_bytes_after"`
	SHABefore      string `json:"journal_sha256_before"`
	SHAAfter       string `json:"journal_sha256_after"`
	Preserved      bool   `json:"original_preserved"`
	OpenMicros     int64  `json:"open_us"`
	Err            string `json:"error,omitempty"`
	ReopenReadable int    `json:"reopen_readable,omitempty"`
	ReopenErr      string `json:"reopen_error,omitempty"`
}

var fb01Mu sync.Mutex

func fb01Emit(t testing.TB, r fb01Result) {
	t.Helper()
	if r.Build == "" {
		r.Build = os.Getenv("FB01_BUILD")
	}
	t.Logf("%s | %s | %s: %s readable=%d/%d lost=%d journal %d->%d preserved=%v %s",
		r.Test, r.Case, r.Mode, r.Outcome, r.Readable, r.Acked, r.Lost, r.JournalBefore, r.JournalAfter, r.Preserved, r.Err)
	path := os.Getenv("FB01_RESULTS")
	if path == "" {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	fb01Mu.Lock()
	defer fb01Mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// fb01Open opens dir with open, measures the outcome against tr and, for an
// owner that opened, reopens once to show the effect is durable.
func fb01Open(t testing.TB, dir string, tr *fb01Truth, test, name, mode string, open func(string) (*Store, error)) fb01Result {
	t.Helper()
	jp := fb01Journal(dir)
	st0, _ := os.Stat(jp)
	r := fb01Result{Test: test, Case: name, Mode: mode, Acked: tr.acked(), SHABefore: fb01SHA(jp)}
	if st0 != nil {
		r.JournalBefore = st0.Size()
	}
	start := time.Now()
	s, err := open(dir)
	r.OpenMicros = time.Since(start).Microseconds()
	if err != nil {
		r.Outcome, r.Err = "REFUSE", err.Error()
	} else {
		r.Lost, r.Readable = tr.lost(s)
		r.Outcome = "OK"
		if r.Lost > 0 {
			r.Outcome = "LOSS"
			if d := fb01Tail(s); d != "" {
				// A read-only view of the valid prefix that names the tail it
				// left out: nothing was changed and nothing is silent.
				r.Outcome, r.Err = "PREFIX", d
			}
		}
		s.Close()
	}
	if st1, _ := os.Stat(jp); st1 != nil {
		r.JournalAfter = st1.Size()
	}
	r.SHAAfter = fb01SHA(jp)
	r.Preserved = r.SHAAfter == r.SHABefore
	if err == nil && mode == "owner" {
		if s2, err2 := OpenStore(dir); err2 != nil {
			r.ReopenErr = err2.Error()
		} else {
			_, r.ReopenReadable = tr.lost(s2)
			s2.Close()
		}
	}
	return r
}

// fb01Tail is the tail a shared open reported, if the build reports one.
func fb01Tail(s *Store) string {
	if d := s.JournalTail(); d != nil {
		return "tail reported: " + d.Detail
	}
	return ""
}

// fb01Must checks the M-A1 invariant on a result: an open either refuses, or
// shows every acknowledged mutation. Set FB01_EXPECT_DEFECT=1 to run the
// harness against an unfixed build without failing.
func fb01Must(t testing.TB, r fb01Result) {
	t.Helper()
	fb01Emit(t, r)
	if os.Getenv("FB01_EXPECT_DEFECT") == "1" {
		return
	}
	if r.Outcome == "LOSS" {
		t.Errorf("%s | %s | %s: silent loss of %d acknowledged mutations", r.Test, r.Case, r.Mode, r.Lost)
	}
	if r.Outcome == "PREFIX" && (!strings.HasPrefix(r.Mode, "shared") || !r.Preserved) {
		t.Errorf("%s | %s | %s: only a shared open may serve a reported prefix, and unchanged", r.Test, r.Case, r.Mode)
	}
	if r.Outcome == "REFUSE" && !r.Preserved {
		t.Errorf("%s | %s | %s: a refused open changed the journal", r.Test, r.Case, r.Mode)
	}
}

// TestFB01ReproF6JNL01 is F6-JNL-01: a bit flip in an early frame's length
// field made the owner open treat 36 acknowledged frames as a torn tail and
// truncate them (4 of 40 objects left at 2337f48).
func TestFB01ReproF6JNL01(t *testing.T) {
	const n = 40
	base := t.TempDir()
	tr := fb01Seed(t, base, n)
	d0, err := os.ReadFile(fb01Journal(base))
	if err != nil {
		t.Fatal(err)
	}
	frames := fb01Frames(d0)
	if len(frames) != n+1 {
		t.Fatalf("journal has %d frames, want %d", len(frames), n+1)
	}
	cases := []struct {
		name  string
		frame int
		xor   uint32
	}{
		{"early frame length bit 20 (+1 MiB)", 5, 1 << 20},
		{"early frame length bit 16 (+64 KiB)", 5, 1 << 16},
		{"early frame length bit 0", 5, 1},
		{"final frame length bit 12 (+4 KiB)", len(frames) - 1, 1 << 12},
		{"early frame length bit 23 (over the 8 MiB cap)", 5, 1 << 23},
	}
	for _, c := range cases {
		for _, mode := range []string{"shared", "owner"} {
			dir := fb01Copy(t, base)
			d := append([]byte(nil), d0...)
			f := frames[c.frame]
			binary.BigEndian.PutUint32(d[f.off+16:], f.plen^c.xor)
			if err := os.WriteFile(fb01Journal(dir), d, 0o644); err != nil {
				t.Fatal(err)
			}
			open := OpenStore
			if mode == "shared" {
				open = OpenStoreShared
			}
			fb01Must(t, fb01Open(t, dir, tr, "F6-JNL-01", c.name, mode, open))
		}
	}
}

// fb01RebuildAfterRefusal is the one path E2 leaves for an unusable namespace
// checkpoint: an owner open with the checkpoint refuses, names it and changes
// nothing; then an explicit salvage that keeps the whole journal moves the
// checkpoint into quarantine, so the next open rebuilds from the journal.
func fb01RebuildAfterRefusal(t testing.TB, dir string) {
	t.Helper()
	jp := fb01Journal(dir)
	before := fb01SHA(jp)
	s, err := openStore(dir, true, true)
	if err == nil {
		s.Close()
	}
	var cp *checkpointProblem
	if !errors.Is(err, errJournalRefused) || !errors.As(err, &cp) {
		t.Fatalf("want a refusal that names the checkpoint, got %v", err)
	}
	if fb01SHA(jp) != before {
		t.Fatal("a refused open changed the journal")
	}
	st, err := os.Stat(jp)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := salvageJournal(dir, st.Size(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rec.DroppedBytes != 0 || len(rec.MovedAside) == 0 {
		t.Fatalf("salvage of a whole journal dropped %d bytes and moved %v aside", rec.DroppedBytes, rec.MovedAside)
	}
}
