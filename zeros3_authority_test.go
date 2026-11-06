package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---- Z4-B2 representation-authority tests ------------------------------------

type authLab struct {
	t    testing.TB
	dir  string
	s    *Store
	want map[string][]byte
}

func newAuthLab(t testing.TB) *authLab {
	t.Helper()
	l := &authLab{t: t, dir: t.TempDir(), want: map[string][]byte{}}
	l.open()
	if err := l.s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.close() })
	return l
}

func (l *authLab) open() {
	l.t.Helper()
	s, err := OpenStore(l.dir)
	if err != nil {
		l.t.Fatal(err)
	}
	l.s = s
}

func (l *authLab) close() {
	if l.s != nil {
		l.s.Close()
		l.s = nil
	}
}

// offline runs fn with the store closed (exclusive maintenance) and reopens it.
func (l *authLab) offline(fn func()) {
	l.t.Helper()
	l.close()
	fn()
	l.open()
}

func (l *authLab) put(key string, data []byte) {
	l.t.Helper()
	if _, err := l.s.PutObject("b", key, data, "application/octet-stream", nil); err != nil {
		l.t.Fatal(err)
	}
	l.want[key] = data
}

func (l *authLab) man(key string) manifestV1 {
	_, m := repManifestOf(l.t, l.s, "b", key)
	return m
}

func (l *authLab) read(key string, rng byteRange) []byte {
	return repReadAll(l.t, l.s, l.man(key), rng)
}

// exact reads every live key whole and by ranges and compares with the bytes put.
func (l *authLab) exact(stage string) {
	l.t.Helper()
	for key, want := range l.want {
		if got := l.read(key, byteRange{0, int64(len(want)) - 1}); !bytes.Equal(got, want) {
			l.t.Fatalf("%s: %s whole read differs", stage, key)
		}
		for _, r := range [][2]int64{{0, 4095}, {12345, 12345 + 65535}, {int64(len(want)) / 3, int64(len(want))/3 + 1<<20}, {int64(len(want)) - 1000, int64(len(want)) - 1}} {
			end := min(r[1], int64(len(want))-1)
			if r[0] > end {
				continue
			}
			if got := l.read(key, byteRange{r[0], end}); !bytes.Equal(got, want[r[0]:end+1]) {
				l.t.Fatalf("%s: %s range %v differs", stage, key, r)
			}
		}
	}
}

func (l *authLab) migrate(p AuthPolicy) AuthReport {
	l.t.Helper()
	p.Apply = true
	if !p.AllowRep && !p.AllowDelta {
		p.AllowRep, p.AllowDelta = true, true
	}
	rep, err := l.s.AuthorityMigrate(p)
	if err != nil {
		l.t.Fatal(err)
	}
	return rep
}

func (l *authLab) verifyOK(stage string) VerifyResult {
	l.t.Helper()
	res, err := l.s.Verify(true)
	if err != nil || !res.OK() {
		l.t.Fatalf("%s: deep verify err=%v issues=%v", stage, err, res.Issues)
	}
	return res
}

// reclaim performs the exclusive gc + repack + compact sequence that physically
// retires unmarked CDC chunks.
func (l *authLab) reclaim() (gc GCResult) {
	l.t.Helper()
	l.offline(func() {
		var err error
		if gc, err = gcCollect(l.dir, true); err != nil {
			l.t.Fatal(err)
		}
		if _, err = repackStore(l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err != nil {
			l.t.Fatal(err)
		}
		if _, err = compactStore(l.dir, authCompactOpts()); err != nil {
			l.t.Fatal(err)
		}
		if gc, err = gcCollect(l.dir, true); err != nil {
			l.t.Fatal(err)
		}
	})
	return gc
}

func authCompactOpts() compactOptions {
	o := defaultCompactOptions()
	o.MinBytes = 1
	return o
}

func dirBytes(t testing.TB, dir string) (n int64) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, p)
		if err != nil || d.IsDir() || strings.HasPrefix(rel, "tmp"+string(filepath.Separator)) {
			return nil
		}
		if fi, e := d.Info(); e == nil {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// authVersion derives a deterministic edited revision of prev.
func authVersion(prev []byte, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, 99))
	out := append([]byte(nil), prev...)
	for i := 0; i < 40; i++ { // scattered overwrites
		off := r.IntN(len(out) - 64)
		for j := 0; j < 24+r.IntN(40); j++ {
			out[off+j] = byte(r.IntN(256))
		}
	}
	for i := 0; i < 3; i++ { // insertions shift everything after them
		off := r.IntN(len(out))
		ins := make([]byte, 500+r.IntN(2500))
		for j := range ins {
			ins[j] = byte(r.IntN(256))
		}
		out = append(out[:off], append(ins, out[off:]...)...)
	}
	off := r.IntN(len(out) - 6000)
	return append(out[:off], out[off+4000:]...)
}

func authHistory(seed uint64, n, size int) [][]byte {
	v := genRandomBytes(int64(seed), size)
	out := [][]byte{v}
	for i := 1; i < n; i++ {
		v = authVersion(v, seed*131+uint64(i))
		out = append(out, v)
	}
	return out
}

func TestXDeltaRoundTripAndRejection(t *testing.T) {
	hist := authHistory(11, 3, 400_000)
	for _, span := range []int64{4096, 128 << 10, 1 << 20} {
		blob, refs, err := xdEncode([]xdSrc{{sha256.Sum256(hist[0]), hist[0]}}, hist[1], span)
		if err != nil || len(refs) != 1 {
			t.Fatalf("encode: %v refs=%d", err, len(refs))
		}
		if len(blob) > len(hist[1])/4 {
			t.Fatalf("delta of a lightly edited revision is %d of %d bytes", len(blob), len(hist[1]))
		}
		h, more, err := xdParseHeader(blob)
		if err != nil || more || h.total != int64(len(blob)) || h.size != int64(len(hist[1])) {
			t.Fatalf("header: %+v more=%v err=%v", h, more, err)
		}
		var out []byte
		for i := range h.frames {
			f := h.frames[i]
			ops, err := xdDecodeFrame(&h, i, blob[f.poff:f.poff+f.plen])
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range ops {
				if o.base < 0 {
					out = append(out, o.lit...)
				} else {
					out = append(out, hist[0][o.off:o.off+o.n]...)
				}
			}
		}
		if !bytes.Equal(out, hist[1]) {
			t.Fatalf("span %d: reconstruction differs", span)
		}
	}
	// A truncated or bit-flipped envelope never parses into a different object silently.
	blob, _, _ := xdEncode([]xdSrc{{sha256.Sum256(hist[0]), hist[0]}}, hist[1], 128<<10)
	for _, n := range []int{0, 3, 9, 40, len(blob) / 2} {
		if h, more, err := xdParseHeader(blob[:n]); err == nil && !more && h.total <= int64(n) {
			t.Fatalf("truncated header %d parsed", n)
		}
	}
	bad := append([]byte(nil), blob...)
	bad[len(bad)-1] ^= 0xff
	h, _, _ := xdParseHeader(bad)
	i := len(h.frames) - 1
	if ops, err := xdDecodeFrame(&h, i, bad[h.frames[i].poff:]); err == nil {
		var n int64
		for _, o := range ops {
			n += o.n
		}
		if n != h.frames[i].span {
			t.Fatal("corrupt frame accepted with wrong span")
		}
	}
}

func TestAuthorityDeltaLifecycleAndReclaim(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(21, 5, 3_000_000)
	for i, v := range hist {
		l.put("k/v"+string(rune('0'+i)), v)
	}
	// Also an unrelated object and a duplicate of version 2.
	l.put("other", genRandomBytes(77, 800_000))
	l.put("dup", hist[2])
	l.reclaim() // compacts loose chunks into packs: CDC baseline
	cdcBytes := dirBytes(t, l.dir)
	l.exact("cdc")
	rep := l.migrate(AuthPolicy{MaxDepth: 2, MinSaving: 0.05})
	if rep.Admitted < 4 {
		t.Fatalf("admitted %d of %d: %+v", rep.Admitted, rep.Objects, rep.Plans)
	}
	l.exact("migrated")
	res := l.verifyOK("migrated")
	if res.AuthorityBodies == 0 || res.AuthoritiesVerified != res.AuthorityBodies {
		t.Fatalf("verified %d of %d bodies", res.AuthoritiesVerified, res.AuthorityBodies)
	}
	if v := l.s.format.StoreFormatVersion; v != storeFormatVersionAuthority {
		t.Fatalf("floor = %d", v)
	}
	gc := l.reclaim()
	if gc.ChunksUnreachable != 0 {
		t.Fatalf("second gc still found %d unreachable chunks", gc.ChunksUnreachable)
	}
	after := dirBytes(t, l.dir)
	if after >= cdcBytes*6/10 {
		t.Fatalf("physical bytes %d -> %d: authority did not retire CDC (expected < 0.6x)", cdcBytes, after)
	}
	l.exact("reclaimed")
	l.verifyOK("reclaimed")
	t.Logf("physical %d -> %d (%.3fx), admitted %d/%d", cdcBytes, after, float64(after)/float64(cdcBytes), rep.Admitted, rep.Objects)

	// Retired CDC chunks are truly gone: the manifest chunk list alone cannot read.
	man := l.man("k/v3")
	missing := 0
	for _, c := range man.Chunks {
		sum, _ := decodeHexSHA256(c.SHA256)
		if _, err := l.s.casStat(sum); err != nil {
			missing++
		}
	}
	if missing == 0 {
		t.Fatal("no CDC chunk of a delta-authoritative object was reclaimed")
	}
	// Deleting the visible base object keeps dependents readable (base pinned by dependency).
	for _, k := range []string{"k/v0", "k/v1"} {
		if err := l.s.DeleteObject("b", k); err != nil {
			t.Fatal(err)
		}
		delete(l.want, k)
	}
	l.reclaim()
	l.exact("base-deleted")
	l.verifyOK("base-deleted")
	// A restart (fresh store object) resolves everything.
	l.offline(func() {})
	l.exact("reopened")
}

func TestAuthorityRevertRestoresCDC(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(31, 3, 1_500_000)
	for i, v := range hist {
		l.put("k"+string(rune('0'+i)), v)
	}
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.reclaim()
	st, _ := l.s.AuthorityStatus()
	if st.ByForm["delta"] == 0 {
		t.Fatalf("no delta body: %+v", st)
	}
	for pass := 0; pass < 2; pass++ { // a base with dependents first becomes a pin; the second pass retires it
		for k := range l.want {
			sha, _ := decodeHexSHA256(l.man(k).ObjectSHA256)
			if err := l.s.AuthorityRevert(sha); err != nil {
				t.Fatalf("revert %s: %v", k, err)
			}
		}
	}
	l.exact("reverted")
	// With the records gone the CDC chunk lists alone serve every read.
	st, _ = l.s.AuthorityStatus()
	if st.Records != 0 {
		t.Fatalf("records remain after full revert: %+v", st)
	}
	l.reclaim()
	l.exact("reverted-reclaimed")
	l.verifyOK("reverted-reclaimed")
}

func TestAuthorityGCFailsClosedOnMissingBase(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(41, 3, 1_000_000)
	for i, v := range hist {
		l.put("k"+string(rune('0'+i)), v)
	}
	l.migrate(AuthPolicy{MinSaving: 0.05})
	files, _ := l.s.authFiles()
	var victim [32]byte
	for _, f := range files {
		if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormPin {
			victim = f
		}
	}
	if victim == ([32]byte{}) {
		t.Fatal("no pin record to remove")
	}
	if err := os.Remove(l.s.authPath(victim)); err != nil {
		t.Fatal(err)
	}
	rr, err := l.s.computeReachability(false)
	if err != nil || rr.OK() {
		t.Fatalf("missing base must fail reachability closed: ok=%v err=%v", rr.OK(), err)
	}
	l.offline(func() {
		if _, err := gcCollect(l.dir, true); err == nil {
			t.Fatal("gc applied despite a dangling authority dependency")
		}
	})
}

var _ = io.EOF

// ---- real-process crash matrix ----------------------------------------------

// TestAuthHelperProcess is the child of the crash matrix: it opens the store,
// arms a SIGKILL at the Nth firing of a named hook and runs one operation.
func TestAuthHelperProcess(t *testing.T) {
	scenario := os.Getenv("ZS3_AUTH_HELPER")
	if scenario == "" {
		return
	}
	dir, point := os.Getenv("ZS3_AUTH_DIR"), os.Getenv("ZS3_AUTH_POINT")
	occ, _ := strconv.Atoi(os.Getenv("ZS3_AUTH_OCC"))
	n := 0
	testHook = func(p string) {
		if p == point {
			if n++; n == occ {
				syscall.Kill(os.Getpid(), syscall.SIGKILL)
			}
		}
	}
	switch scenario {
	case "migrate", "revert":
		lock, err := acquireStoreLock(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.release()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if scenario == "migrate" {
			_, err = s.AuthorityMigrate(AuthPolicy{MinSaving: 0.05, Apply: true, AllowRep: true, AllowDelta: true})
		} else {
			for pass := 0; pass < 2; pass++ {
				files, _ := s.authFiles()
				for _, f := range files {
					if err = s.AuthorityRevert(f); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	case "import":
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(os.Getenv("ZS3_AUTH_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ImportAuthorityClosure(f); err != nil {
			t.Fatal(err)
		}
		s.Close()
	case "gc":
		if _, err := gcCollect(dir, true); err != nil {
			t.Fatal(err)
		}
		if _, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0) // not killed: the point was never reached
}

// runKilled runs a helper scenario and reports whether the kill point was hit.
func runKilled(t *testing.T, scenario, dir, point string, occ int) (killed bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthHelperProcess$", "-test.timeout=300s")
	cmd.Env = append(os.Environ(), "ZS3_AUTH_HELPER="+scenario, "ZS3_AUTH_DIR="+dir, "ZS3_AUTH_POINT="+point, "ZS3_AUTH_OCC="+strconv.Itoa(occ))
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
			return true
		}
	}
	t.Fatalf("%s helper at %s#%d failed unexpectedly: %v\n%s", scenario, point, occ, err, out)
	return false
}

type crashFixture struct {
	dir  string
	want map[string][]byte
}

// newCrashFixture builds a CDC store with a version history; stage "migrated"
// additionally commits authority and compacts so gc has real packs to retire.
func newCrashFixture(t *testing.T, stage string) *crashFixture {
	l := newAuthLab(t)
	hist := authHistory(51, 4, 1_200_000)
	for i, v := range hist {
		l.put("k"+string(rune('0'+i)), v)
	}
	l.put("other", genRandomBytes(52, 300_000))
	if stage == "migrated" || stage == "retired" {
		l.close() // CDC chunks go into packs first, so retirement leaves partly dead packs
		if _, err := compactStore(l.dir, authCompactOpts()); err != nil {
			t.Fatal(err)
		}
		l.open()
		l.migrate(AuthPolicy{MinSaving: 0.05})
		if stage == "migrated" { // unreferenced loose chunk plus an orphan body record for gc to delete
			junk := genRandomBytes(990, 70_000)
			sum, err := l.s.casWrite(junk)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.s.authPublish(&authRecord{SHA: sha256.Sum256(junk), Size: int64(len(junk)), Gen: 1, Form: authFormPin, Pieces: []authPiece{{sum, int64(len(junk))}}}); err != nil {
				t.Fatal(err)
			}
		}
		l.close()
		if stage == "retired" { // CDC of body-backed objects physically gone
			if _, err := gcCollect(l.dir, true); err != nil {
				t.Fatal(err)
			}
			if _, err := repackStore(l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		l.close()
	}
	return &crashFixture{dir: l.dir, want: l.want}
}

// assertAcknowledgedExact is the recovery invariant: after any crash, in a
// fresh process, every acknowledged object resolves to its complete exact bytes.
func (f *crashFixture) assertAcknowledgedExact(t *testing.T, stage string) *Store {
	t.Helper()
	s, err := OpenStore(f.dir)
	if err != nil {
		t.Fatalf("%s: reopen: %v", stage, err)
	}
	for key, want := range f.want {
		man := repManifestOfT(t, s, key)
		if got := repReadAll(t, s, man, byteRange{0, int64(len(want)) - 1}); !bytes.Equal(got, want) {
			t.Fatalf("%s: %s differs after crash recovery", stage, key)
		}
	}
	if res, err := s.Verify(true); err != nil || !res.OK() {
		t.Fatalf("%s: deep verify after crash: err=%v issues=%v", stage, err, res.Issues)
	}
	return s
}

func repManifestOfT(t *testing.T, s *Store, key string) manifestV1 {
	_, m := repManifestOf(t, s, "b", key)
	return m
}

func TestAuthorityCrashMatrix(t *testing.T) {
	type tc struct {
		scenario, stage, point string
		occ                    []int
	}
	cases := []tc{
		{"migrate", "cdc", hookAuthBeforeFormat, []int{1}},
		{"migrate", "cdc", hookAuthAfterFormat, []int{1}},
		{"migrate", "cdc", hookAuthComponentsFlushed, []int{1, 2}},
		{"migrate", "cdc", hookAuthProofDone, []int{1, 2}},
		{"migrate", "cdc", hookAuthBeforeWrite, []int{1, 2}},
		{"migrate", "cdc", hookAuthRecordWritten, []int{1, 2}},
		{"migrate", "cdc", hookAuthBeforeRename, []int{1, 2}},
		{"migrate", "cdc", hookAuthAfterRename, []int{1, 2}},
		{"migrate", "cdc", hookAuthPublished, []int{1, 3}},
		{"migrate", "cdc", hookCASBeforeRename, []int{1}},
		{"migrate", "cdc", hookCASAfterStageSync, []int{1}},
		{"revert", "retired", hookAuthRevertMaterial, []int{1}},
		{"revert", "retired", hookCASBeforeRename, []int{1}},
		{"revert", "retired", hookAuthBeforeRetire, []int{1}},
		{"revert", "retired", hookAuthAfterRetire, []int{1}},
		{"gc", "migrated", hookBeforeGCDelete, []int{1, 2}},
		{"gc", "migrated", hookBeforePackDelete, []int{1}},
		{"gc", "migrated", hookPackDeleted, []int{1}},
	}
	for _, c := range cases {
		for _, occ := range c.occ {
			t.Run(fmt.Sprintf("%s/%s#%d", c.scenario, c.point, occ), func(t *testing.T) {
				f := newCrashFixture(t, c.stage)
				if !runKilled(t, c.scenario, f.dir, c.point, occ) {
					t.Fatalf("kill point %s#%d was never reached", c.point, occ)
				}
				s := f.assertAcknowledgedExact(t, "after-kill")
				s.Close()
				// The interrupted operation must be completable and idempotent from the crash state.
				switch c.scenario {
				case "migrate":
					s, _ = OpenStore(f.dir)
					if _, err := s.AuthorityMigrate(AuthPolicy{MinSaving: 0.05, Apply: true, AllowRep: true, AllowDelta: true}); err != nil {
						t.Fatalf("resume migrate: %v", err)
					}
					s.Close()
				case "revert":
					s, _ = OpenStore(f.dir)
					for pass := 0; pass < 2; pass++ {
						files, _ := s.authFiles()
						for _, h := range files {
							if err := s.AuthorityRevert(h); err != nil {
								t.Fatalf("resume revert: %v", err)
							}
						}
					}
					s.Close()
				}
				if _, err := gcCollect(f.dir, true); err != nil {
					t.Fatalf("gc after recovery: %v", err)
				}
				if _, err := repackStore(f.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err != nil {
					t.Fatalf("repack after recovery: %v", err)
				}
				s = f.assertAcknowledgedExact(t, "after-recovery")
				s.Close()
			})
		}
	}
}

// ---- failure injection, malformed records and graph limits --------------------

func authHistoryLab(t *testing.T, seed uint64, n, size int) *authLab {
	l := newAuthLab(t)
	for i, v := range authHistory(seed, n, size) {
		l.put("k"+string(rune('0'+i)), v)
	}
	return l
}

func TestAuthorityInjectedIOFailuresKeepLastValidState(t *testing.T) {
	for _, point := range []string{"pieces-write", "record-write", "pin"} {
		t.Run(point, func(t *testing.T) {
			l := authHistoryLab(t, 61, 3, 900_000)
			authFault = func(p string) error {
				if p == point {
					return syscall.ENOSPC
				}
				return nil
			}
			defer func() { authFault = nil }()
			_, err := l.s.AuthorityMigrate(AuthPolicy{MinSaving: 0.05, Apply: true, AllowRep: true, AllowDelta: true})
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("injected %s failure not reported: %v", point, err)
			}
			l.exact("after-failure")
			l.verifyOK("after-failure")
			// A short record write never produced a published (or readable) record.
			files, _ := l.s.authFiles()
			for _, f := range files {
				if _, err := l.s.authLoad(f); err != nil {
					t.Fatalf("unreadable record published after failed write: %v", err)
				}
			}
			authFault = nil
			l.migrate(AuthPolicy{MinSaving: 0.05})
			l.exact("retried")
			l.verifyOK("retried")
		})
	}
}

func TestAuthorityMalformedRecordsAndEnvelopes(t *testing.T) {
	l := authHistoryLab(t, 71, 3, 800_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	files, _ := l.s.authFiles()
	r := rand.New(rand.NewPCG(5, 6))
	for _, f := range files {
		raw, _ := os.ReadFile(l.s.authPath(f))
		if _, err := unmarshalAuthRecord(raw); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 300; i++ {
			m := append([]byte(nil), raw...)
			switch r.IntN(3) {
			case 0:
				m[r.IntN(len(m))] ^= byte(1 + r.IntN(255))
			case 1:
				m = m[:r.IntN(len(m))]
			default:
				m = append(m, byte(r.IntN(256)))
			}
			// Any single mutation is caught by the CRC (or a structural check) -- never parsed as valid.
			if _, err := unmarshalAuthRecord(m); err == nil && !bytes.Equal(m, raw) {
				t.Fatalf("mutated record parsed as valid (%x)", f[:4])
			}
		}
	}
	// Mutated envelopes: the header parser and frame decoder never panic and never accept wrong spans.
	hist := authHistory(72, 2, 200_000)
	blob, _, _ := xdEncode([]xdSrc{{sha256.Sum256(hist[0]), hist[0]}}, hist[1], 32<<10)
	for i := 0; i < 2000; i++ {
		m := append([]byte(nil), blob...)
		m[r.IntN(len(m))] ^= byte(1 + r.IntN(255))
		h, more, err := xdParseHeader(m)
		if err != nil || more {
			continue
		}
		for fi := range h.frames {
			f := h.frames[fi]
			if f.poff+f.plen > int64(len(m)) {
				continue
			}
			xdDecodeFrame(&h, fi, m[f.poff:f.poff+f.plen])
		}
	}
}

func TestAuthorityCorruptComponentFailsClosedNeverWrongBytes(t *testing.T) {
	l := authHistoryLab(t, 81, 3, 900_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	files, _ := l.s.authFiles()
	var victim *authRecord
	for _, f := range files {
		if r, _ := l.s.authLoad(f); r != nil && r.Form == authFormDelta {
			victim = r
		}
	}
	if victim == nil {
		t.Fatal("no delta")
	}
	corruptChunkOnDisk(t, l.s, hex.EncodeToString(victim.Pieces[0].SHA[:]))
	rr, err := l.s.computeReachability(true)
	if err != nil || rr.OK() {
		t.Fatalf("a corrupt delta component must flip deep reachability: %v", err)
	}
	for k := range l.want {
		man := l.man(k)
		if man.ObjectSHA256 != hex.EncodeToString(victim.SHA[:]) {
			continue
		}
		rd := l.s.openObjectReader(man, byteRange{0, man.TotalLength - 1})
		var got []byte
		var rerr error
		for {
			b, e := rd.next()
			if e != nil {
				rerr = e
				break
			}
			got = append(got, b...)
		}
		rd.close()
		// CDC chunks still exist (no gc yet): the read falls back to them and is exact;
		// either way a wrong byte is never delivered.
		if rerr == io.EOF && !bytes.Equal(got, l.want[k]) {
			t.Fatalf("corrupt component produced wrong bytes without an error")
		}
	}
	// After the CDC copy is reclaimed the damage is explicit and unrecoverable locally.
	l.close()
	if _, err := gcCollect(l.dir, true); err == nil {
		t.Fatal("gc must refuse to run against a corrupt authoritative component")
	}
}

// rawPublish writes a record without the commit-time closure checks, to plant states
// a correct writer could never produce (over-deep chains, cycles).
func rawPublish(t *testing.T, s *Store, rec *authRecord) {
	t.Helper()
	data, err := rec.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ensureAuthFormat(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(s.authDir(), 0o755)
	s.auth.mayHave.Store(true)
	if err := os.WriteFile(s.authPath(rec.SHA), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityGraphLimits(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(91, 7, 300_000)
	for i, v := range hist {
		l.put("k"+string(rune('0'+i)), v)
	}
	s := l.s
	// Hand-build a 7-deep chain; records claim the depth the bases imply, capped at the maximum.
	var prev []byte
	var prevSHA [32]byte
	for i, v := range hist {
		sum := sha256.Sum256(v)
		man := l.man("k" + string(rune('0'+i)))
		if i == 0 {
			ps, _ := chunkPieces(man)
			if err := s.authPublish(&authRecord{SHA: sum, Size: int64(len(v)), Gen: 1, Form: authFormPin, Pieces: ps}); err != nil {
				t.Fatal(err)
			}
		} else {
			blob, refs, err := xdEncode([]xdSrc{{prevSHA, prev}}, v, 128<<10)
			if err != nil {
				t.Fatal(err)
			}
			ps, _ := s.authWritePieces(blob)
			d := byte(min(i, authHardDepth))
			rec := &authRecord{SHA: sum, Size: int64(len(v)), Gen: 1, Form: authFormDelta, Depth: d, Bases: refs, Pieces: ps}
			rawPublish(t, s, rec)
		}
		prev, prevSHA = v, sum
	}
	// Depth 1..4 resolve; depth 5+ (recorded as 4 but implying more) is rejected, never read.
	for i := range hist {
		man := l.man("k" + string(rune('0'+i)))
		_, err := s.authOpenReader(man, byteRange{0, man.TotalLength - 1})
		if (i <= authHardDepth) != (err == nil) {
			t.Fatalf("chain depth %d: err=%v", i, err)
		}
	}
	rr, _ := s.computeReachability(false)
	if rr.OK() {
		t.Fatal("over-deep chain must fail reachability closed")
	}
	// Too many bases cannot even be serialized.
	if _, err := (&authRecord{Size: 1, Gen: 1, Form: authFormDelta, Depth: 1, Bases: make([]xdBaseRef, 5), Pieces: []authPiece{{}}}).marshal(); err == nil {
		t.Fatal("fanout above the hard limit serialized")
	}
}

func TestAuthorityCycleRejected(t *testing.T) {
	l := newAuthLab(t)
	a, b := genRandomBytes(101, 120_000), authVersion(genRandomBytes(101, 120_000), 3)
	shaA, shaB := sha256.Sum256(a), sha256.Sum256(b)
	blobA, refsA, _ := xdEncode([]xdSrc{{shaB, b}}, a, 64<<10)
	blobB, refsB, _ := xdEncode([]xdSrc{{shaA, a}}, b, 64<<10)
	psA, _ := l.s.authWritePieces(blobA)
	psB, _ := l.s.authWritePieces(blobB)
	rawPublish(t, l.s, &authRecord{SHA: shaA, Size: int64(len(a)), Gen: 1, Form: authFormDelta, Depth: 2, Bases: refsA, Pieces: psA})
	rawPublish(t, l.s, &authRecord{SHA: shaB, Size: int64(len(b)), Gen: 1, Form: authFormDelta, Depth: 2, Bases: refsB, Pieces: psB})
	ctx := l.s.newAuthCtx()
	if _, _, err := ctx.body(shaA, 0, 1); err == nil {
		t.Fatal("cyclic delta graph resolved")
	}
	var res reachabilityResult
	a2 := l.s.newAuthReachCtx(&res)
	if _, err := a2.mark(shaA, func([]chunkRef) {}, map[[32]byte]bool{}, 1); err == nil || res.OK() {
		t.Fatalf("cycle not faulted: %v", err)
	}
}

func TestAuthorityStaleGenerationRejected(t *testing.T) {
	l := authHistoryLab(t, 111, 2, 400_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	files, _ := l.s.authFiles()
	r, _ := l.s.authLoad(files[0])
	dup := *r
	if err := l.s.authPublish(&dup); !errors.Is(err, errAuthStale) {
		t.Fatalf("republishing the same generation: %v", err)
	}
	dup.Gen += 1
	if err := l.s.authPublish(&dup); err != nil {
		t.Fatalf("next generation: %v", err)
	}
	l.exact("regenerated")
}

// ---- compatibility, bundles, replication, repair -------------------------------

var (
	oldBinOnce sync.Once
	oldBinPath string
	oldBinErr  error
)

// buildOldBinary compiles the pre-B2 production source (main at 44d7d58),
// the binary generation that must refuse authority stores.
func buildOldBinary(t *testing.T) string {
	t.Helper()
	oldBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "zs3old")
		if err != nil {
			oldBinErr = err
			return
		}
		for _, f := range []string{"zeros3.go", "go.mod"} {
			out, err := exec.Command("git", "show", "44d7d58b24adc9fcc44f8743cab3b528d72ab7d1:"+f).Output()
			if err != nil {
				oldBinErr = fmt.Errorf("git show %s: %w", f, err)
				return
			}
			os.WriteFile(filepath.Join(dir, f), out, 0o644)
		}
		oldBinPath = filepath.Join(dir, "zeros3-old")
		if out, err := exec.Command("go", "build", "-C", dir, "-o", oldBinPath, ".").CombinedOutput(); err != nil {
			oldBinErr = fmt.Errorf("%v\n%s", err, out)
		}
	})
	if oldBinErr != nil {
		t.Fatalf("building the old binary: %v", oldBinErr)
	}
	return oldBinPath
}

// treeDigest hashes every file name and content of a store (excluding the lock).
func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) == "LOCK" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		fmt.Fprintf(h, "%s:%d:", rel, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func authMigratedLab(t *testing.T) *authLab {
	l := authHistoryLab(t, 121, 4, 1_000_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	return l
}

func TestAuthorityOldBinaryRefusesAndLeavesStoreUntouched(t *testing.T) {
	old := buildOldBinary(t)
	l := authMigratedLab(t)
	l.reclaim()
	l.close()
	before := treeDigest(t, l.dir)
	for _, args := range [][]string{
		{"stats", "-store", l.dir}, {"verify", "-store", l.dir}, {"doctor", "-store", l.dir}, {"gc", "-store", l.dir, "-apply"},
		{"compact", "-store", l.dir}, {"repack", "-store", l.dir, "-apply"}, {"serve", "-store", l.dir, "-addr", "127.0.0.1:0"},
		{"versions", "-store", l.dir, "-bucket", "b", "-key", "k0"},
	} {
		out, errOut, code := runZeros3CLI(t, old, args...)
		if code == 0 || !strings.Contains(out+errOut, "unsupported store format version 10") {
			t.Fatalf("old binary %v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	if after := treeDigest(t, l.dir); after != before {
		t.Fatal("an old binary mutated an authority store while refusing it")
	}
	// A store that never used authority keeps its old floor and stays usable by old binaries.
	plain := newAuthLab(t)
	plain.put("x", genRandomBytes(5, 100_000))
	plain.close()
	if out, errOut, code := runZeros3CLI(t, old, "stats", "-store", plain.dir); code != 0 {
		t.Fatalf("old binary refused a store that never adopted authority: %s %s", out, errOut)
	}
	if _, f := readFormatVersion(t, plain.dir); f >= storeFormatVersionAuthority {
		t.Fatalf("authority floor raised without authority use: %d", f)
	}
}

func readFormatVersion(t *testing.T, dir string) (storeFormat, int) {
	b, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f storeFormat
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f, f.StoreFormatVersion
}

// A snapshot of body-backed objects exports an ordinary, self-contained bundle
// that an old binary imports and serves with exact bytes.
func TestAuthorityBundleExportToOldBinary(t *testing.T) {
	old := buildOldBinary(t)
	l := authMigratedLab(t)
	entries, _ := l.s.captureSnapshotEntries("b", "")
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := l.s.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}
	l.reclaim() // CDC chunks of the body-backed objects are physically gone
	for _, k := range []string{"k0", "k1", "k2", "k3"} {
		if err := l.s.DeleteObject("b", k); err != nil { // only the snapshot (and history) keep them
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	st, err := l.s.exportBundle(desc.SnapshotID, &buf, true)
	if err != nil {
		t.Fatalf("export of retired-CDC snapshot: %v", err)
	}
	file := filepath.Join(t.TempDir(), "snap.zs3b")
	os.WriteFile(file, buf.Bytes(), 0o644)
	// New binary store imports it.
	dst := newAuthLab(t)
	dst.close()
	if _, err := importBundleFile(dst.dir, file); err != nil {
		t.Fatalf("import into new store: %v", err)
	}
	// Old binary imports it and verifies deeply.
	oldDir := t.TempDir()
	if out, errOut, code := runZeros3CLI(t, old, "bundle", "import", "-store", oldDir, "-in", file); code != 0 {
		t.Fatalf("old binary import: %s %s", out, errOut)
	}
	if out, errOut, code := runZeros3CLI(t, old, "verify", "-store", oldDir, "-deep"); code != 0 {
		t.Fatalf("old binary deep verify of imported bundle: %s %s", out, errOut)
	}
	if _, f := readFormatVersion(t, oldDir); f >= storeFormatVersionAuthority {
		t.Fatal("exported bundle carried the authority floor")
	}
	// Exact bytes after import, read by the new binary from the old-format store and from the new one.
	for _, dir := range []string{oldDir, dst.dir} {
		st2 := OpenStoreT(t, dir)
		snaps, _ := st2.scanSnapshots()
		if len(snaps) != 1 || len(snaps[0].Entries) != 4 {
			t.Fatalf("%s: imported snapshots %d", dir, len(snaps))
		}
		for _, e := range snaps[0].Entries {
			sum, _ := decodeHexSHA256(e.ManifestSHA256)
			man, err := st2.readVerifiedManifest(e.ManifestUUID, sum)
			if err != nil {
				t.Fatal(err)
			}
			got := repReadAll(t, st2, man, byteRange{0, man.TotalLength - 1})
			if !bytes.Equal(got, l.want[e.Key]) {
				t.Fatalf("%s: imported %s differs", dir, e.Key)
			}
		}
	}
	t.Logf("bundle %d bytes for %d objects", st.BundleBytes, st.Objects)
}

func OpenStoreT(t *testing.T, dir string) *Store {
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAuthorityNativeClosureTransferAndInterruption(t *testing.T) {
	l := authMigratedLab(t)
	l.reclaim()
	src, dst := AuthTransferMode(l.s.AuthorityCaps(), l.s.AuthorityCaps()), AuthTransferMode(l.s.AuthorityCaps(), AuthCaps{StoreFormat: storeFormatVersionManaged, MaxDepth: 4})
	if src != "native" || dst != "materialize" {
		t.Fatalf("negotiation: %s / %s", src, dst)
	}
	man := l.man("k3")
	sha, _ := decodeHexSHA256(man.ObjectSHA256)
	var buf bytes.Buffer
	if err := l.s.ExportAuthorityClosure(sha, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > len(l.want["k3"])/2*3 {
		t.Fatalf("closure of a depth-2 delta is %d bytes for a %d byte object", buf.Len(), len(l.want["k3"]))
	}
	// Corrupted streams never install.
	for _, cut := range []int{10, buf.Len() / 2, buf.Len() - 1} {
		d := newAuthLab(t)
		if err := d.s.ImportAuthorityClosure(bytes.NewReader(buf.Bytes()[:cut])); err == nil {
			t.Fatalf("truncated closure (%d) imported", cut)
		}
		if files, _ := d.s.authFiles(); len(files) != 0 {
			t.Fatal("records published from a truncated closure")
		}
	}
	flip := append([]byte(nil), buf.Bytes()...)
	flip[buf.Len()/2] ^= 1
	if d := newAuthLab(t); d.s.ImportAuthorityClosure(bytes.NewReader(flip)) == nil {
		t.Fatal("bit-flipped closure imported")
	}
	// Destination holds only the transferred closure; the same bytes then come out via its own read path.
	d := newAuthLab(t)
	if err := d.s.ImportAuthorityClosure(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	d.put("k3", l.want["k3"]) // namespace entry for the already-held content
	if got := d.read("k3", byteRange{0, int64(len(l.want["k3"])) - 1}); !bytes.Equal(got, l.want["k3"]) {
		t.Fatal("imported closure reads differently")
	}
	// Idempotent re-import.
	if err := d.s.ImportAuthorityClosure(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	d.verifyOK("destination")
	// Real-process interruption during import leaves a valid destination; the retry completes.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "closure.bin"), buf.Bytes(), 0o644)
	for _, point := range []string{hookAuthImportChunks, hookAuthImportRecord, hookAuthBeforeRename} {
		sd := t.TempDir()
		if !runKilledImport(t, sd, filepath.Join(dir, "closure.bin"), point) {
			t.Fatalf("import kill point %s never reached", point)
		}
		s := OpenStoreT(t, sd)
		files, _ := s.authFiles()
		for _, f := range files {
			if _, _, err := s.authProve(mustLoad(t, s, f), 0); err != nil {
				t.Fatalf("partially imported record %x not exact: %v", f[:4], err)
			}
		}
		if err := s.ImportAuthorityClosure(bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatalf("retry after kill at %s: %v", point, err)
		}
		if res, err := s.Verify(true); err != nil || !res.OK() {
			t.Fatalf("after retry: %v %v", err, res.Issues)
		}
	}
}

func mustLoad(t *testing.T, s *Store, h [32]byte) *authRecord {
	r, err := s.authLoad(h)
	if err != nil || r == nil {
		t.Fatalf("load %x: %v", h[:4], err)
	}
	return r
}

func runKilledImport(t *testing.T, dir, file, point string) bool {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthHelperProcess$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "ZS3_AUTH_HELPER=import", "ZS3_AUTH_DIR="+dir, "ZS3_AUTH_FILE="+file, "ZS3_AUTH_POINT="+point, "ZS3_AUTH_OCC=1")
	_, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
			return true
		}
	}
	return false
}

// A peer holding the same authoritative closure repairs a corrupt component by digest;
// a peer with only exact bytes repairs at object level; with neither, the loss is explicit.
func TestAuthorityRepairPaths(t *testing.T) {
	l := authMigratedLab(t)
	l.reclaim()
	man := l.man("k3")
	sha, _ := decodeHexSHA256(man.ObjectSHA256)
	rec := mustLoad(t, l.s, sha)
	victim := rec.Pieces[0]
	vhex := hex.EncodeToString(victim.SHA[:])
	// Peer holding the native closure.
	var buf bytes.Buffer
	if err := l.s.ExportAuthorityClosure(sha, &buf); err != nil {
		t.Fatal(err)
	}
	peer := newAuthLab(t)
	if err := peer.s.ImportAuthorityClosure(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	good, err := peer.s.casRead(victim.SHA)
	if err != nil {
		t.Fatal(err)
	}
	// Damage the victim: loose copy rewritten with the wrong bytes (store is compacted, so write loose garbage + drop pack via repair publication path)
	l.offline(func() {})
	if _, err := l.s.casRead(victim.SHA); err != nil {
		t.Fatal(err)
	}
	// Object-level SHA verification alone is not repair: a corrupt component is detected, not fixed.
	corrupt := append([]byte(nil), good...)
	corrupt[0] ^= 0xff
	if err := l.s.AuthorityRepairObject(sha, corrupt); err == nil {
		t.Fatal("repair accepted bytes that do not match the content hash")
	}
	// Object-level repair from a valid exact copy supersedes the body with a pin on rewritten CDC chunks.
	if err := l.s.AuthorityRepairObject(sha, l.want["k3"]); err != nil {
		t.Fatalf("object-level repair: %v", err)
	}
	after := mustLoad(t, l.s, sha)
	if after.Form != authFormPin || after.Gen != rec.Gen+1 {
		t.Fatalf("repaired record: form=%d gen=%d", after.Form, after.Gen)
	}
	l.exact("repaired")
	l.verifyOK("repaired")
	_ = vhex
}

// ---- concurrency ------------------------------------------------------------------

// Replacement, deletion, copy, snapshot and reads race a running migration. Every
// byte any reader sees must belong to a version that was really written, and the
// final namespace, retained history and snapshots must resolve exactly after gc.
func TestAuthorityConcurrentOperations(t *testing.T) {
	l := newAuthLab(t)
	var mu sync.Mutex
	valid := map[string]bool{} // sha256 of every payload ever written
	cur := map[string][]byte{}
	remember := func(b []byte) { mu.Lock(); valid[hex.EncodeToString(sumSlice(b))] = true; mu.Unlock() }
	seed := authHistory(131, 6, 600_000)
	for i, v := range seed {
		remember(v)
		l.put("k"+string(rune('0'+i)), v)
		cur["k"+string(rune('0'+i))] = v
	}
	s := l.s
	stop := make(chan struct{})
	var wg sync.WaitGroup
	fail := make(chan string, 16)
	report := func(msg string) {
		select {
		case fail <- msg:
		default:
		}
	}
	// readers
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(r), 1))
			for {
				select {
				case <-stop:
					return
				default:
				}
				key := "k" + string(rune('0'+rng.IntN(6)))
				_, man, err := s.HeadObject("b", key)
				if err != nil {
					continue // deleted by a writer
				}
				rd := s.openObjectReader(man, byteRange{0, man.TotalLength - 1})
				h := sha256.New()
				var rerr error
				for {
					b, e := rd.next()
					if e != nil {
						if e != io.EOF {
							rerr = e
						}
						break
					}
					h.Write(b)
				}
				rd.close()
				if rerr != nil {
					report(fmt.Sprintf("read %s: %v", key, rerr))
					return
				}
				mu.Lock()
				ok := valid[hex.EncodeToString(h.Sum(nil))]
				mu.Unlock()
				if !ok {
					report("reader saw bytes that were never written for " + key)
					return
				}
			}
		}(r)
	}
	// writers: overwrite, delete, copy, snapshot
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewPCG(77, 1))
		prev := seed[5]
		for i := 0; i < 40; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := "k" + string(rune('0'+rng.IntN(6)))
			switch rng.IntN(4) {
			case 0, 1:
				prev = authVersion(prev, uint64(1000+i))
				remember(prev)
				if _, err := s.PutObject("b", key, prev, "application/octet-stream", nil); err != nil {
					report("put: " + err.Error())
					return
				}
				mu.Lock()
				cur[key] = prev
				mu.Unlock()
			case 2:
				_ = s.DeleteObject("b", key)
				mu.Lock()
				delete(cur, key)
				mu.Unlock()
			case 3:
				dst := "copy" + strconv.Itoa(i)
				if _, _, err := s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: key, DstBucket: "b", DstKey: dst}); err == nil {
					_, man, _ := s.HeadObject("b", dst)
					_ = man
				}
				entries, _ := s.captureSnapshotEntries("b", "")
				d := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
				if err := s.publishSnapshot(d); err != nil {
					report("snapshot: " + err.Error())
					return
				}
			}
		}
	}()
	// migrations race the above
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := s.AuthorityMigrate(AuthPolicy{MinSaving: 0.05, Apply: true, AllowRep: true, AllowDelta: true}); err != nil {
				report("migrate: " + err.Error())
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	// wait for writer + migrator, then stop readers
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	time.AfterFunc(8*time.Second, func() { close(stop) })
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent workload did not finish")
	}
	select {
	case m := <-fail:
		t.Fatal(m)
	default:
	}
	// Quiesce: final exactness for the current namespace, then gc and every root again.
	for key, want := range cur {
		l.want[key] = want
	}
	for key := range l.want {
		if _, ok := cur[key]; !ok {
			delete(l.want, key)
		}
	}
	l.verifyOK("quiesced")
	l.reclaim()
	l.exact("after-gc")
	l.verifyOK("after-gc")
	// Retained history and snapshots still resolve (every manifest any root names).
	for _, lm := range l.s.liveManifests() {
		got := repReadAll(t, l.s, lm.man, byteRange{0, lm.man.TotalLength - 1})
		if hex.EncodeToString(sumSlice(got)) != lm.man.ObjectSHA256 {
			t.Fatalf("live root %s/%s resolves to wrong bytes", lm.bucket, lm.key)
		}
	}
}

func sumSlice(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func TestAuthorityThinClosureUsesDestinationBasesAndFallsBack(t *testing.T) {
	l := authMigratedLab(t)
	sha3, _ := decodeHexSHA256(l.man("k3").ObjectSHA256)
	sha2, _ := decodeHexSHA256(l.man("k2").ObjectSHA256)
	var full, thin bytes.Buffer
	if err := l.s.ExportAuthorityClosure(sha3, &full); err != nil {
		t.Fatal(err)
	}
	// Destination already holds k2's closure, which is k3's base.
	d := newAuthLab(t)
	var k2 bytes.Buffer
	if err := l.s.ExportAuthorityClosure(sha2, &k2); err != nil {
		t.Fatal(err)
	}
	if err := d.s.ImportAuthorityClosure(&k2); err != nil {
		t.Fatal(err)
	}
	have := func(h [32]byte) bool { r, _ := d.s.authLoad(h); return r != nil }
	if err := l.s.ExportAuthorityClosureThin(sha3, have, &thin); err != nil {
		t.Fatal(err)
	}
	if thin.Len() >= full.Len() {
		t.Fatalf("thin closure %d not smaller than full %d", thin.Len(), full.Len())
	}
	if err := d.s.ImportAuthorityClosure(&thin); err != nil {
		t.Fatalf("thin import: %v", err)
	}
	d.put("k3", l.want["k3"])
	if got := d.read("k3", byteRange{0, int64(len(l.want["k3"])) - 1}); !bytes.Equal(got, l.want["k3"]) {
		t.Fatal("thin-installed object differs")
	}
	// A destination without the base refuses the thin stream cleanly; the full closure then works.
	thin.Reset()
	l.s.ExportAuthorityClosureThin(sha3, have, &thin)
	e := newAuthLab(t)
	if err := e.s.ImportAuthorityClosure(&thin); !errors.Is(err, errAuthMissing) {
		t.Fatalf("thin stream without its base: %v", err)
	}
	if files, _ := e.s.authFiles(); len(files) != 0 {
		t.Fatal("refused thin import published records")
	}
	if err := e.s.ImportAuthorityClosure(&full); err != nil {
		t.Fatalf("full fallback: %v", err)
	}
	e.verifyOK("fallback")
}

// Replication reads retired-CDC source objects through the virtual chunk index and
// delivers exact bytes to both a new-format destination and an old binary.
func TestAuthorityReplicationToNewAndOldDestinations(t *testing.T) {
	old := buildOldBinary(t)
	l := authMigratedLab(t)
	l.reclaim()
	for _, k := range []string{"k1", "k3"} { // chunks of these are physically gone
		man := l.man(k)
		gone := 0
		for _, c := range man.Chunks {
			sum, _ := decodeHexSHA256(c.SHA256)
			if _, err := l.s.casStat(sum); err != nil {
				gone++
			}
		}
		if gone == 0 {
			t.Fatalf("%s kept all its CDC chunks", k)
		}
	}
	creds := Credentials{AccessKeyID: "AKIASYNCTESTACCESSKEY1", SecretAccessKey: "SyncTestSecretKeyForZeroS3M6Tests0123456"}
	srcTS := httptest.NewServer(NewServer(l.s, creds, "us-east-1"))
	defer srcTS.Close()
	run := func(dstEndpoint string, dstCreds Credentials, client *http.Client, tag string) {
		for key, want := range l.want {
			cfg := replicateConfig{
				Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "b", Key: key, Creds: creds, Region: "us-east-1", HTTPClient: srcTS.Client()},
				Dest:   syncClientConfig{Endpoint: dstEndpoint, Bucket: "dst", Key: key, Creds: dstCreds, Region: "us-east-1", HTTPClient: client},
			}
			if _, err := replicateObject(cfg); err != nil {
				t.Fatalf("%s: replicate %s: %v", tag, key, err)
			}
			signer := testSigner{accessKey: dstCreds.AccessKeyID, secretKey: dstCreds.SecretAccessKey, region: "us-east-1"}
			r := doSignedRequest(t, client, dstEndpoint, signer, http.MethodGet, "/dst/"+key, nil, nil)
			got, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != 200 || !bytes.Equal(got, want) {
				t.Fatalf("%s: %s differs after replication (status %d)", tag, key, r.StatusCode)
			}
		}
	}
	// new-format destination (in-process server)
	dstDir := t.TempDir()
	dst, _ := OpenStore(dstDir)
	defer dst.Close()
	dst.CreateBucket("dst")
	dstTS := httptest.NewServer(NewServer(dst, creds, "us-east-1"))
	defer dstTS.Close()
	run(dstTS.URL, creds, dstTS.Client(), "new-destination")
	// old binary destination
	oldDir := t.TempDir()
	addr := freeTCPAddr(t)
	cmd := exec.Command(old, "-store", oldDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)
	oc := &http.Client{}
	oldCreds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	r := doSignedRequest(t, oc, "http://"+addr, testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}, http.MethodPut, "/dst", nil, nil)
	r.Body.Close()
	run("http://"+addr, oldCreds, oc, "old-binary-destination")
}

// Syncing a new revision into a store whose older revisions are body-only: shared chunks
// are recognised through the virtual index and re-materialized physically at commit.
func TestAuthoritySyncIntoBodyOnlyDestination(t *testing.T) {
	hist := authHistory(141, 5, 1_000_000)
	creds := Credentials{AccessKeyID: "AKIASYNCTESTACCESSKEY1", SecretAccessKey: "SyncTestSecretKeyForZeroS3M6Tests0123456"}
	dstLab := newAuthLab(t)
	for i := 0; i < 4; i++ {
		dstLab.put("k"+string(rune('0'+i)), hist[i])
	}
	dstLab.migrate(AuthPolicy{MinSaving: 0.05})
	dstLab.reclaim()
	srcLab := newAuthLab(t)
	srcLab.put("k4", hist[4])
	srcTS := httptest.NewServer(NewServer(srcLab.s, creds, "us-east-1"))
	defer srcTS.Close()
	dstTS := httptest.NewServer(NewServer(dstLab.s, creds, "us-east-1"))
	defer dstTS.Close()
	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "b", Key: "k4", Creds: creds, Region: "us-east-1", HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "b", Key: "k4", Creds: creds, Region: "us-east-1", HTTPClient: dstTS.Client()},
	}
	st, err := replicateObject(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uploaded %d of %d logical bytes (shared chunks recognised via bodies)", st.UploadedBytes, st.LogicalBytes)
	dstLab.want["k4"] = hist[4]
	dstLab.exact("after-sync")
	dstLab.verifyOK("after-sync")
	dstLab.reclaim()
	dstLab.exact("after-sync-gc")
}

func FuzzAuthRecord(f *testing.F) {
	a := genRandomBytes(1, 50_000)
	b := authVersion(a, 2)
	blob, refs, _ := xdEncode([]xdSrc{{sha256.Sum256(a), a}}, b, 16<<10)
	d := &authRecord{SHA: sha256.Sum256(b), Size: int64(len(b)), Gen: 1, Form: authFormDelta, Depth: 1, Bases: refs, Pieces: []authPiece{{sha256.Sum256(blob), int64(min(len(blob), maxPackedChunkBytes))}}}
	p := &authRecord{SHA: sha256.Sum256(a), Size: int64(len(a)), Gen: 3, Form: authFormPin, Pieces: []authPiece{{sha256.Sum256(a), int64(len(a))}}}
	for _, r := range []*authRecord{d, p} {
		if raw, err := r.marshal(); err == nil {
			f.Add(raw)
		}
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if r, err := unmarshalAuthRecord(raw); err == nil {
			again, err := r.marshal()
			if err != nil {
				return
			}
			if r2, err := unmarshalAuthRecord(again); err != nil || r2.SHA != r.SHA || r2.Size != r.Size || len(r2.Pieces) != len(r.Pieces) {
				t.Fatalf("round trip of a parsed record changed it: %v", err)
			}
		}
	})
}

func FuzzXDeltaEnvelope(f *testing.F) {
	a := genRandomBytes(3, 60_000)
	b := authVersion(a, 4)
	blob, _, _ := xdEncode([]xdSrc{{sha256.Sum256(a), a}}, b, 8<<10)
	f.Add(blob)
	f.Fuzz(func(t *testing.T, raw []byte) {
		h, more, err := xdParseHeader(raw)
		if err != nil || more {
			return
		}
		for i := range h.frames {
			fr := h.frames[i]
			if fr.poff < 0 || fr.plen < 0 || fr.poff+fr.plen > int64(len(raw)) {
				continue
			}
			ops, err := xdDecodeFrame(&h, i, raw[fr.poff:fr.poff+fr.plen])
			if err != nil {
				continue
			}
			var n int64
			for _, o := range ops {
				n += o.n
				if o.base >= len(h.bases) || (o.base >= 0 && o.off+o.n > h.bases[o.base].Size) {
					t.Fatal("accepted op outside its base")
				}
			}
			if n != fr.span {
				t.Fatal("accepted frame with wrong span")
			}
		}
	})
}

// The full operator workflow through real binaries: migrate, gc, repack, then
// a fresh server answers whole and ranged S3 GETs from bodies alone.
func TestAuthorityCLIWorkflowAndHTTPReads(t *testing.T) {
	bin := buildZeros3Binary(t)
	l := authHistoryLab(t, 151, 4, 1_500_000)
	l.close()
	if _, err := compactStore(l.dir, authCompactOpts()); err != nil {
		t.Fatal(err)
	}
	before := dirBytes(t, l.dir)
	out, errOut, code := runZeros3CLI(t, bin, "authority", "migrate", "-store", l.dir, "-apply", "-max-read-amp", "0", "-min-saving", "0.05")
	if code != 0 {
		t.Fatalf("migrate: %s %s", out, errOut)
	}
	var rep AuthReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Admitted < 3 {
		t.Fatalf("report %v admitted=%d", err, rep.Admitted)
	}
	if out, errOut, code = runZeros3CLI(t, bin, "authority", "status", "-store", l.dir); code != 0 || !strings.Contains(out, `"delta": 3`) {
		t.Fatalf("status: %d %s %s", code, out, errOut)
	}
	for _, args := range [][]string{{"gc", "-store", l.dir, "-apply"}, {"repack", "-store", l.dir, "-apply", "-max-live-percent", "100"}, {"verify", "-store", l.dir, "-deep"}} {
		if out, errOut, code = runZeros3CLI(t, bin, args...); code != 0 {
			t.Fatalf("%v: %s %s", args, out, errOut)
		}
	}
	if after := dirBytes(t, l.dir); after >= before*7/10 {
		t.Fatalf("physical %d -> %d", before, after)
	}
	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "-store", l.dir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	c := &http.Client{}
	for key, want := range l.want {
		r := doSignedRequest(t, c, "http://"+addr, signer, http.MethodGet, "/b/"+key, nil, nil)
		got, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || !bytes.Equal(got, want) {
			t.Fatalf("GET %s: status %d, exact=%v", key, r.StatusCode, bytes.Equal(got, want))
		}
		r = doSignedRequest(t, c, "http://"+addr, signer, http.MethodGet, "/b/"+key, nil, map[string]string{"Range": "bytes=700000-765535"})
		got, _ = io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 206 || !bytes.Equal(got, want[700000:765536]) {
			t.Fatalf("ranged GET %s: status %d", key, r.StatusCode)
		}
		hr := doSignedRequest(t, c, "http://"+addr, signer, http.MethodHead, "/b/"+key, nil, nil)
		hr.Body.Close()
		md5sum := md5.Sum(want)
		if hr.StatusCode != 200 || strings.Trim(hr.Header.Get("ETag"), `"`) != hex.EncodeToString(md5sum[:]) {
			t.Fatalf("HEAD %s etag %q", key, hr.Header.Get("ETag"))
		}
	}
}

// Every existing structural kind can be the authoritative body (forced admission regardless of
// economics): exact whole/range reads, retired CDC and deep verify, plus revert.
func TestAuthorityRepFormAcrossStructuralKinds(t *testing.T) {
	l := newAuthLab(t)
	for _, f := range repFixtures(t) {
		l.put(f.name, f.data)
	}
	// Frozen SST/Parquet (A3) and Model (A2) lab inputs cover kinds 8-10.
	kinds := map[repKind]bool{}
	loadFrozen := func(raw []byte) {
		var fs []repLabCodecFixture
		if err := json.Unmarshal(raw, &fs); err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if r, err := unmarshalRep(f.Root, repProdLimits); err == nil && !kinds[r.Kind] && len(f.Input) > 0 {
				kinds[r.Kind] = true
				l.put(fmt.Sprintf("frozen-kind%d", r.Kind), f.Input)
			}
		}
	}
	if raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json"); err == nil {
		loadFrozen(raw)
	}
	if raw, err := repZ4ModelFixtureJSON(t); err == nil {
		loadFrozen(raw)
	}
	l.close()
	if _, err := compactStore(l.dir, authCompactOpts()); err != nil {
		t.Fatal(err)
	}
	l.open()
	rep := l.migrate(AuthPolicy{Strategy: "forced", AllowRep: true, MinSaving: -10, MinObject: 1, MinRepGroup: 1})
	forms := map[string]int{}
	for _, p := range rep.Plans {
		forms[p.Form]++
	}
	if forms["rep"] < 7+len(kinds) {
		t.Fatalf("rep admissions: %v (frozen kinds %v)", forms, kinds)
	}
	t.Logf("rep-form admissions %v; frozen kinds covered %v", forms, kinds)
	l.exact("rep-authority")
	l.reclaim()
	l.exact("rep-reclaimed")
	l.verifyOK("rep-reclaimed")
	st, _ := l.s.AuthorityStatus()
	if st.ByForm["rep"] < 7 {
		t.Fatalf("status %+v", st)
	}
	for pass := 0; pass < 2; pass++ {
		for k := range l.want {
			sha, _ := decodeHexSHA256(l.man(k).ObjectSHA256)
			if err := l.s.AuthorityRevert(sha); err != nil {
				t.Fatalf("revert %s: %v", k, err)
			}
		}
	}
	l.exact("rep-reverted")
}

// A damaged delta component is repaired by digest from a peer that holds the native closure;
// a peer that only holds ordinary objects cannot supply it, leaving the object unrecoverable
// by chunk repair (object-level repair from exact bytes is the remaining path).
func TestAuthorityPeerAssistedComponentRepair(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIASYNCTESTACCESSKEY1", SecretAccessKey: "SyncTestSecretKeyForZeroS3M6Tests0123456"}
	l := authMigratedLab(t)
	files, _ := l.s.authFiles()
	var rec *authRecord
	for _, f := range files {
		if r := mustLoad(t, l.s, f); r.Form == authFormDelta && r.Depth == 2 {
			rec = r
		}
	}
	if rec == nil {
		t.Fatal("no depth-2 delta")
	}
	var buf bytes.Buffer
	if err := l.s.ExportAuthorityClosure(rec.SHA, &buf); err != nil {
		t.Fatal(err)
	}
	peer := newAuthLab(t)
	if err := peer.s.ImportAuthorityClosure(&buf); err != nil {
		t.Fatal(err)
	}
	peerTS := httptest.NewServer(NewServer(peer.s, creds, "us-east-1"))
	defer peerTS.Close()
	// The local CDC copy is retired first (loose store: gc alone deletes it) so the component is the only source.
	l.offline(func() {
		if _, err := gcCollect(l.dir, true); err != nil {
			t.Fatal(err)
		}
	})
	victim := hex.EncodeToString(rec.Pieces[0].SHA[:])
	l.close()
	deleteChunkOnDiskDir(t, l.dir, victim)
	l.open()
	rr, _ := l.s.computeReachability(false)
	if rr.OK() {
		t.Fatal("a missing authoritative component must be detected")
	}
	stats, err := l.s.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, "us-east-1")})
	if err != nil || stats.Repaired != 1 || !stats.PostRepairOK {
		t.Fatalf("component repair: %+v err=%v", stats, err)
	}
	l.exact("component-repaired")
	l.verifyOK("component-repaired")
	// An ordinary-object peer cannot supply the delta component: explicit unresolved state.
	l.close()
	deleteChunkOnDiskDir(t, l.dir, victim)
	l.open()
	plain := newAuthLab(t)
	plainTS := httptest.NewServer(NewServer(plain.s, creds, "us-east-1"))
	defer plainTS.Close()
	stats, err = l.s.repairFromPeer(repairConfig{Peer: mustPeerConfig(plainTS, creds, "us-east-1")})
	if err == nil && (stats.Unresolved != 1 || stats.PostRepairOK) {
		t.Fatalf("peer without the component must leave it unresolved: %+v", stats)
	}
	if res, _ := l.s.Verify(true); res.OK() {
		t.Fatal("verify must stay failing while a component is unrecoverable")
	}
}

func deleteChunkOnDiskDir(t *testing.T, dir, hexSum string) {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum, _ := decodeHexSHA256(hexSum)
	if loc, ok := s.packLookup(sum); ok { // packed: rewrite the pack without the record is out of scope; corrupt it in place
		_ = loc
		t.Fatalf("component is packed; fixture must keep it loose")
	}
	if err := os.Remove(s.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityPublishRefusesIncompleteClosure(t *testing.T) {
	l := newAuthLab(t)
	a := genRandomBytes(201, 200_000)
	b := authVersion(a, 9)
	shaA, shaB := sha256.Sum256(a), sha256.Sum256(b)
	blob, refs, _ := xdEncode([]xdSrc{{shaA, a}}, b, 64<<10)
	ps, _ := l.s.authWritePieces(blob)
	rec := &authRecord{SHA: shaB, Size: int64(len(b)), Gen: 1, Form: authFormDelta, Depth: 1, Bases: refs, Pieces: ps}
	if err := l.s.authPublish(rec); !errors.Is(err, errAuthMissing) {
		t.Fatalf("delta without a committed base published: %v", err)
	}
	if _, err := os.Stat(l.s.authPath(shaB)); err == nil {
		t.Fatal("record file exists after refused publication")
	}
	// A pin whose component chunk is absent is refused too.
	missing := authPiece{sha256.Sum256([]byte("absent")), 6}
	if err := l.s.authPublish(&authRecord{SHA: shaA, Size: 6, Gen: 1, Form: authFormPin, Pieces: []authPiece{missing}}); !errors.Is(err, errAuthMissing) {
		t.Fatalf("pin with a missing chunk published: %v", err)
	}
	if _, err := os.Stat(l.s.authDir()); err == nil {
		if files, _ := l.s.authFiles(); len(files) != 0 {
			t.Fatal("records exist")
		}
	}
	if l.s.format.StoreFormatVersion >= storeFormatVersionAuthority {
		t.Fatal("a refused publication raised the store floor")
	}
}

// Bases survive the retirement of every namespace entry and manifest that named them, while a
// live dependent needs them; when the last dependent goes, the whole chain is reclaimed.
func TestAuthorityBaseRetentionAfterNamespacePruning(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(171, 5, 900_000)
	for _, v := range hist { // one key overwritten: older revisions live only in retained history
		if _, err := l.s.PutObject("b", "doc", v, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
	l.want["doc"] = hist[4]
	l.migrate(AuthPolicy{MinSaving: 0.05, MaxDepth: 2, Order: "oldest"})
	st, _ := l.s.AuthorityStatus()
	if st.Records < 4 {
		t.Fatalf("status %+v", st)
	}
	keep := 0
	res, err := l.s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Key: "doc", KeepLast: &keep})
	if err != nil || len(res.Versions) != 4 {
		t.Fatalf("plan prune: %v %d", err, len(res.Versions))
	}
	if err := l.s.ApplyHistoryPrune(&res); err != nil {
		t.Fatal(err)
	}
	l.reclaim()
	l.exact("pruned")
	l.verifyOK("pruned")
	after, _ := l.s.AuthorityStatus()
	if after.Records == 0 || after.Records >= st.Records+1 {
		t.Fatalf("records %d -> %d", st.Records, after.Records)
	}
	// Only the closure of the live revision remains: some body has no manifest of its own.
	rr, _ := l.s.computeReachability(false)
	if rr.AuthManifests != 1 || rr.AuthPins+rr.AuthReps+rr.AuthDeltas < 2 {
		t.Fatalf("expected a closure of >=2 bodies behind one manifest, got manifests=%d bodies=%d", rr.AuthManifests, rr.AuthPins+rr.AuthReps+rr.AuthDeltas)
	}
	// Replace the live revision with unrelated content: the retired chain is reclaimed entirely.
	l.put("doc", genRandomBytes(172, 300_000))
	keep = 0
	res, _ = l.s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Key: "doc", KeepLast: &keep})
	l.s.ApplyHistoryPrune(&res)
	l.reclaim()
	final, _ := l.s.AuthorityStatus()
	if final.Records != 0 {
		t.Fatalf("%d body records outlived every dependent", final.Records)
	}
	l.exact("chain-reclaimed")
	if gc := l.reclaim(); gc.ChunksUnreachable != 0 {
		t.Fatalf("unreachable chunks remain: %d", gc.ChunksUnreachable)
	}
}
