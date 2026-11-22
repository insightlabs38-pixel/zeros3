package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// Lets the scale probe observe replay spills without a rebuild.
	if v, err := strconv.Atoi(os.Getenv("ZEROS3_B3_REPLAY_FLOOR")); err == nil && v > 0 {
		nsReplayFloor = v
	}
}

// ---- overlay and table units ----

func TestNSOverlayMatchesMap(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	var o nsOverlay[int]
	model := map[string]int{}
	for i := range 20000 {
		k := fmt.Sprintf("k%05d", rng.IntN(3000))
		o.set(k, i)
		model[k] = i
	}
	if o.n != len(model) {
		t.Fatalf("size %d, want %d", o.n, len(model))
	}
	for k, v := range model {
		if got, ok := o.find(k); !ok || got != v {
			t.Fatalf("find %s = %d,%v want %d", k, got, ok, v)
		}
	}
	if _, ok := o.find("zzz"); ok {
		t.Fatal("absent key found")
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range model {
			if !yield(k) {
				return
			}
		}
	})
	it := &nsOvIter[int]{blocks: o.slice("k01000", "k02000")}
	it.seek("k01000")
	var got []string
	for ; it.valid(); it.next() {
		got = append(got, it.cur().k)
	}
	var want []string
	for _, k := range keys {
		if k >= "k01000" && k < "k02000" {
			want = append(want, k)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("range walk differs: %d vs %d keys", len(got), len(want))
	}
	for _, b := range o.blocks {
		if len(b) == 0 || len(b) > nsBlockMax {
			t.Fatalf("block size %d", len(b))
		}
	}
}

func TestNSPrefixEnd(t *testing.T) {
	for in, want := range map[string]string{"a": "b", "a/": "a0", "": "", "\xff": "", "a\xff": "b"} {
		if got := nsPrefixEnd(in); got != want {
			t.Errorf("nsPrefixEnd(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every row shape survives a generation: non-UUID manifests, multipart ETags,
// non-UTC archive times, odd version IDs and keys that share long prefixes.
func TestNSGenerationEncodesEveryRowShape(t *testing.T) {
	s := b3Store(t, t.TempDir(), false)
	mustOK(t, s.CreateBucket("b"))
	odd := []*objectEntry{
		{manifestUUID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f", etag: "0123456789abcdef0123456789abcdef", contentType: "text/plain", size: 5, seq: 3},
		{manifestUUID: "not-a-uuid", etag: "17-3", contentType: "", size: 0, seq: 4},
		{manifestUUID: "018F4D2E-6B1A-7C3D-9E2F-1A2B3C4D5E6F", etag: "ABCDEF", contentType: "x/" + strings.Repeat("y", 300), size: 1 << 40, seq: 5},
	}
	for i, e := range odd {
		e.manifestSHA256[0] = byte(i + 1)
		s.tSetObj("b", fmt.Sprintf("%s/%d", strings.Repeat("long/prefix/", 20), i), e)
		s.tSetObj("b", "ü/日本/"+fmt.Sprint(i), e)
	}
	zone := time.FixedZone("", 5*3600+1800)
	hist := []*historyVersionEntry{
		{versionID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f", manifestUUID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f", etag: "0123456789abcdef0123456789abcdef", contentType: "t", archivedAt: time.Unix(1700000000, 12345).UTC(), reason: historyReasonOverwritten, seq: 3},
		{versionID: "v1", manifestUUID: "m1", etag: "e-2", archivedAt: time.Date(2026, 1, 2, 3, 4, 5, 6, zone), reason: historyReasonDeleted, seq: 9, size: 7},
		{versionID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e00", manifestUUID: "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f", archivedAt: time.Unix(-5, 0).UTC(), reason: "r", seq: 11},
	}
	s.mu.Lock()
	s.histSet("b", "k", hist)
	s.mu.Unlock()
	mustOK(t, s.nsConsolidate(false))
	if s.ns.gen == nil || s.ns.objMut.n != 0 {
		t.Fatal("overlay was not folded into a generation")
	}
	for i, e := range odd {
		for _, k := range []string{fmt.Sprintf("%s/%d", strings.Repeat("long/prefix/", 20), i), "ü/日本/" + fmt.Sprint(i)} {
			if got := s.tObj("b", k); !reflect.DeepEqual(got, e) {
				t.Fatalf("%q: got %+v want %+v", k, got, e)
			}
		}
	}
	got := s.tHist("b", "k")
	if len(got) != len(hist) {
		t.Fatalf("history rows %d", len(got))
	}
	for i, h := range hist {
		g := got[i]
		_, goff := g.archivedAt.Zone()
		_, hoff := h.archivedAt.Zone()
		if !g.archivedAt.Equal(h.archivedAt) || goff != hoff {
			t.Fatalf("archive time %v want %v", g.archivedAt, h.archivedAt)
		}
		g.archivedAt, h.archivedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(g, h) {
			t.Fatalf("history row %d: %+v want %+v", i, g, h)
		}
	}
	if e := s.tObj("b", "absent"); e != nil {
		t.Fatal("absent key found")
	}
}

// ---- store-level model ----

func b3Store(t *testing.T, dir string, checkpoint bool) *Store {
	t.Helper()
	s, err := openStore(dir, true, checkpoint)
	mustOK(t, err)
	s.ns.auto = false
	t.Cleanup(func() { s.Close() })
	return s
}

type b3Model struct {
	visible map[string]string
	hist    map[string]int
	open    int
}

func (m *b3Model) units(prefix, delim string) []string {
	var out []string
	last := ""
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range m.visible {
			if strings.HasPrefix(k, prefix) && !yield(k) {
				return
			}
		}
	}) {
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				cp := k[:len(prefix)+i+len(delim)]
				if cp != last {
					out = append(out, cp)
					last = cp
				}
				continue
			}
		}
		out = append(out, k)
	}
	return out
}

func b3List(t *testing.T, s *Store, prefix, delim string, max int) []string {
	t.Helper()
	var out []string
	after := ""
	for pages := 0; ; pages++ {
		page, err := s.ListObjectsV2("b", prefix, delim, after, max)
		mustOK(t, err)
		var names []string
		for _, c := range page.contents {
			names = append(names, c.key)
		}
		names = append(names, page.commonPrefixes...)
		sort.Strings(names)
		if len(names) > max {
			t.Fatalf("page of %d exceeds max %d", len(names), max)
		}
		out = append(out, names...)
		if !page.truncated {
			return out
		}
		if pages > 10000 {
			t.Fatal("listing does not terminate")
		}
		after = page.lastConsumedKey
	}
}

func b3Verify(t *testing.T, s *Store, m *b3Model, step string) {
	t.Helper()
	for _, prefix := range []string{"", "a/", "b/", "b/c/", "zz", "a/1", "e"} {
		for _, delim := range []string{"", "/"} {
			want := m.units(prefix, delim)
			for _, max := range []int{1, 4, 1000} {
				if got := b3List(t, s, prefix, delim, max); !slices.Equal(got, want) {
					t.Fatalf("%s: list(%q,%q,%d)\n got %v\nwant %v", step, prefix, delim, max, got, want)
				}
			}
		}
	}
	if n := s.buckets["b"].count; n != len(m.visible) {
		t.Fatalf("%s: bucket count %d, want %d", step, n, len(m.visible))
	}
	for k, body := range m.visible {
		if _, got, err := s.GetObject("b", k); err != nil || string(got) != body {
			t.Fatalf("%s: GET %s = %q, %v; want %q", step, k, got, err, body)
		}
	}
	for k, n := range m.hist {
		vs, cur, err := s.ListVersions("b", k)
		mustOK(t, err)
		if len(vs) != n || (cur != nil) != (m.visible[k] != "") {
			t.Fatalf("%s: versions of %s: %d rows (want %d), current %v", step, k, len(vs), n, cur != nil)
		}
	}
	if len(s.uploads) != m.open {
		t.Fatalf("%s: %d incomplete uploads, want %d", step, len(s.uploads), m.open)
	}
}

func TestNamespaceGenerationAgainstModel(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	m := &b3Model{visible: map[string]string{}, hist: map[string]int{}}
	rng := rand.New(rand.NewPCG(1, 2))
	var pool []string
	for _, d := range []string{"a/", "b/c/", "b/d/", "e/", ""} {
		for i := range 9 {
			pool = append(pool, fmt.Sprintf("%s%d", d, i+1))
		}
	}
	pool = append(pool, "zz", "a", "a/1/deep", "b/c/x/y")
	keep := 1
	reopens := 0
	for step := range 400 {
		k := pool[rng.IntN(len(pool))]
		body := fmt.Sprintf("body-%d", step)
		switch op := rng.IntN(100); {
		case op < 45:
			mustPutObject(t, s, "b", k, []byte(body), "text/plain", nil)
			if _, ok := m.visible[k]; ok {
				m.hist[k]++
			}
			m.visible[k] = body
		case op < 60:
			mustOK(t, s.DeleteObject("b", k))
			if _, ok := m.visible[k]; ok {
				m.hist[k]++
				delete(m.visible, k)
			}
		case op < 68:
			id, err := s.CreateMultipartUpload("b", k, "text/plain", nil)
			mustOK(t, err)
			etag, err := s.UploadPart("b", k, id, 1, []byte(body))
			mustOK(t, err)
			if rng.IntN(3) == 0 {
				mustOK(t, s.AbortMultipartUpload("b", k, id))
				break
			}
			if rng.IntN(5) == 0 {
				m.open++ // left incomplete
				break
			}
			_, _, err = s.CompleteMultipartUpload("b", k, id, []completedPart{{PartNumber: 1, ETag: etag}})
			mustOK(t, err)
			if _, ok := m.visible[k]; ok {
				m.hist[k]++
			}
			m.visible[k] = body
		case op < 72:
			plan, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Prefix: []string{"a/", "b/", ""}[rng.IntN(3)], KeepLast: &keep})
			mustOK(t, err)
			mustOK(t, s.ApplyHistoryPrune(&plan))
			for key := range m.hist {
				if strings.HasPrefix(key, plan.Prefix) {
					m.hist[key] = min(m.hist[key], keep)
				}
			}
		case op < 88:
			mustOK(t, s.nsConsolidate(rng.IntN(2) == 0))
		case op < 91:
			mustOK(t, s.Close())
			reopens++
			s = b3Store(t, dir, reopens%2 == 0)
		}
		if step%25 == 24 {
			b3Verify(t, s, m, fmt.Sprintf("step %d", step))
		}
	}
	b3Verify(t, s, m, "end")
	live := nsDump(t, s)
	mustOK(t, s.nsConsolidate(true))
	mustOK(t, s.Close())
	// Fresh opens through the published generation and through the bare journal agree.
	if a, b := nsDump(t, b3Store(t, dir, true)), nsDump(t, b3Store(t, dir, false)); a != b || a != live {
		t.Fatal("checkpoint open, journal open and the live namespace differ")
	}
}

func TestNamespaceGenerationOpensLazily(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for i := range 3000 {
		mustPutObject(t, s, "b", fmt.Sprintf("dir%02d/key%04d", i%40, i), []byte("x"), "text/plain", nil)
	}
	mustOK(t, s.nsConsolidate(true))
	mustOK(t, s.Close())
	s = b3Store(t, dir, true)
	g := s.ns.gen
	if g == nil || s.ns.obj.pages() < 8 {
		t.Fatalf("expected a multi-page generation, got %v", g)
	}
	if g.reads.Load() != 0 || s.ns.objMut.n != 0 {
		t.Fatalf("open read %d pages and replayed %d rows", g.reads.Load(), s.ns.objMut.n)
	}
	if _, err := s.lookupObject("b", "dir07/key0007"); err != nil {
		t.Fatal(err)
	}
	if r := g.reads.Load(); r != 1 {
		t.Fatalf("a point lookup read %d pages", r)
	}
	if _, err := s.lookupObject("b", "dir07/key0007"); err != nil || g.reads.Load() != 1 {
		t.Fatalf("a repeated lookup missed the page cache: %d reads, %v", g.reads.Load(), err)
	}
	if _, err := s.lookupObject("b", "dir07/nope"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("absent key: %v", err)
	}
	page, err := s.ListObjectsV2("b", "dir07/", "/", "", 5)
	mustOK(t, err)
	if len(page.contents) != 5 || !page.truncated || g.reads.Load() > 4 {
		t.Fatalf("listing a prefix read %d pages and returned %d keys", g.reads.Load(), len(page.contents))
	}
}

func TestNamespaceGenerationDamage(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for i := range 1500 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i), []byte("x"), "text/plain", nil)
	}
	mustOK(t, s.nsConsolidate(true))
	mustOK(t, s.Close())
	want := nsDump(t, b3Store(t, dir, false))
	path := filepath.Join(dir, "journal", nsGenName)

	// Damage that appears after a successful open is detected on first use, per
	// page; afterwards every namespace read fails instead of answering from a gap.
	s = b3Store(t, dir, true)
	s.ns.cache = newNSPageCache()
	s.ns.gen.cache = s.ns.cache
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	mustOK(t, err)
	_, err = f.WriteAt([]byte{0xA5, 0x5A}, int64(s.ns.obj.dir.Off[3])+20)
	mustOK(t, err)
	mustOK(t, f.Close())
	var failed bool
	for i := range 1500 {
		if _, err := s.lookupObject("b", fmt.Sprintf("k%04d", i)); err != nil {
			if !errors.Is(err, errNSCorrupt) {
				t.Fatalf("unexpected error %v", err)
			}
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("a damaged page was served")
	}
	if _, err := s.ListObjectsV2("b", "", "", "", 10); !errors.Is(err, errNSCorrupt) {
		t.Fatalf("list after damage: %v", err)
	}
	if err := s.DeleteObject("b", "k0001"); !errors.Is(err, errNSCorrupt) {
		t.Fatalf("mutation after damage: %v", err)
	}
	s.Close()

	// Reopening rejects the file (digest) and rebuilds from the journal.
	if got := nsDump(t, b3Store(t, dir, true)); got != want {
		t.Fatal("fallback open differs from the journal state")
	}

	// A truncated or stale file is rejected too.
	mustOK(t, os.Truncate(path, 100))
	if got := nsDump(t, b3Store(t, dir, true)); got != want {
		t.Fatal("truncated generation changed the namespace")
	}
}

func TestNamespaceGenerationHashChain(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for round := range 3 {
		for i := range 20 {
			mustPutObject(t, s, "b", fmt.Sprintf("r%d/%d", round, i), []byte("x"), "text/plain", nil)
		}
		mustOK(t, s.nsConsolidate(true))
		m := s.ns.gen.meta
		blocks, err := journalBlockHashes(s.journal.f, s.journal.writeOffset, nil, 0)
		mustOK(t, err)
		crc, err := journalLastCRC(s.journal.f, s.journal.writeOffset)
		mustOK(t, err)
		if !slices.Equal(m.Blocks, blocks) || m.LastCRC != crc || m.Offset != s.journal.writeOffset || len(m.Blocks) == 0 {
			t.Fatalf("round %d: reused block hashes disagree with a full journal hash", round)
		}
	}
	mustOK(t, s.Close())
	s = b3Store(t, dir, true)
	if s.ns.objMut.n != 0 || s.buckets["b"].count != 60 {
		t.Fatalf("expected the whole journal to be covered: %d replayed, %d visible", s.ns.objMut.n, s.buckets["b"].count)
	}
}

// A failed build restores the overlays and loses nothing.
func TestNamespaceConsolidationFailureKeepsState(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for i := range 30 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%02d", i), []byte("v"), "text/plain", nil)
	}
	tmp := filepath.Join(dir, "tmp")
	mustOK(t, os.RemoveAll(tmp))
	if err := s.nsConsolidate(true); err == nil {
		t.Fatal("expected the build to fail")
	}
	if s.ns.objFrozen != nil || s.ns.histFrozen != nil || s.ns.objMut.n != 30 {
		t.Fatalf("overlay not restored: frozen=%v mut=%d", s.ns.objFrozen != nil, s.ns.objMut.n)
	}
	mustPutObject(t, s, "b", "k00", []byte("again"), "text/plain", nil)
	mustOK(t, os.MkdirAll(tmp, 0o755))
	mustOK(t, s.nsConsolidate(true))
	if _, got, err := s.GetObject("b", "k00"); err != nil || string(got) != "again" {
		t.Fatalf("k00 = %q, %v", got, err)
	}
	if vs, _, _ := s.ListVersions("b", "k00"); len(vs) != 1 {
		t.Fatalf("history rows %d", len(vs))
	}
}

func TestNamespaceAutoConsolidation(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	s.ns.auto, s.ns.limit = true, 16
	mustOK(t, s.CreateBucket("b"))
	for i := range 200 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%03d", i%80), []byte(fmt.Sprint(i)), "text/plain", nil)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		gen, busy, n := s.ns.gen, s.ns.busy, s.ns.overlayLen()
		s.mu.Unlock()
		if gen != nil && !busy && n < 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no consolidation: gen=%v busy=%v overlay=%d", gen != nil, busy, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	live := nsDump(t, s)
	s.ns.auto = false
	mustOK(t, s.Close())
	if got := nsDump(t, b3Store(t, dir, true)); got != live {
		t.Fatal("published generation plus suffix differs from the live namespace")
	}
}

// Readers and mutators race with consolidations; every acknowledged write must
// be visible to its own reader, and the final state must survive a reopen.
func TestNamespaceConcurrentConsolidation(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	const stable = 120
	for i := range stable {
		mustPutObject(t, s, "b", fmt.Sprintf("stable/%03d", i), []byte("s"), "text/plain", nil)
	}
	mustOK(t, s.nsConsolidate(false))
	var stop atomic.Bool
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	report := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}
	for r := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(r), 5))
			for !stop.Load() {
				k := fmt.Sprintf("stable/%03d", rng.IntN(stable))
				if _, err := s.lookupObject("b", k); err != nil {
					report(fmt.Errorf("lookup %s: %w", k, err))
					return
				}
				page, err := s.ListObjectsV2("b", "stable/", "", "", 1000)
				if err != nil || len(page.contents) != stable {
					report(fmt.Errorf("list: %d keys, %v", len(page.contents), err))
					return
				}
			}
		}()
	}
	var mu sync.Mutex
	acked := map[string]string{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			k, body := fmt.Sprintf("w/%03d", i%40), fmt.Sprintf("v%d", i)
			if _, err := s.PutObject("b", k, []byte(body), "text/plain", nil); err != nil {
				report(err)
				return
			}
			mu.Lock()
			acked[k] = body
			mu.Unlock()
			if _, got, err := s.GetObject("b", k); err != nil || string(got) != body {
				report(fmt.Errorf("read your write %s: %q, %v", k, got, err))
				return
			}
		}
	}()
	for i := range 6 {
		time.Sleep(20 * time.Millisecond)
		if err := s.nsConsolidate(i%2 == 0); err != nil {
			report(err)
			break
		}
	}
	stop.Store(true)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for k, body := range acked {
		if _, got, err := s.GetObject("b", k); err != nil || string(got) != body {
			t.Fatalf("final %s = %q, %v; want %q", k, got, err, body)
		}
	}
	live := nsDump(t, s)
	mustOK(t, s.Close())
	if got := nsDump(t, b3Store(t, dir, true)); got != live {
		t.Fatal("reopened namespace differs")
	}
}

// A journal larger than one hash block: published blocks are reused, the last
// partial block is rehashed, and any changed byte anywhere in the prefix is caught.
func TestNamespaceGenerationBlockHashes(t *testing.T) {
	old := nsHashBlock
	nsHashBlock = 4096
	t.Cleanup(func() { nsHashBlock = old })
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for round := range 3 {
		for i := range 30 {
			mustPutObject(t, s, "b", fmt.Sprintf("r%d/%02d", round, i), []byte("x"), "text/plain", nil)
		}
		mustOK(t, s.nsConsolidate(true))
		m := s.ns.gen.meta
		full, err := journalBlockHashes(s.journal.f, m.Offset, nil, 0)
		mustOK(t, err)
		if len(full) < round+3 || !slices.Equal(full, m.Blocks) {
			t.Fatalf("round %d: %d blocks, reuse differs from a full hash", round, len(full))
		}
	}
	want := nsDump(t, s)
	mustOK(t, s.Close())
	if got := nsDump(t, b3Store(t, dir, true)); got != want {
		t.Fatal("reopen differs")
	}
	// Flip one journal byte in the middle block; the frame CRC check of a full
	// replay catches it, and the checkpoint must not mask it.
	path := filepath.Join(dir, "journal", "visibility.log")
	data, err := os.ReadFile(path)
	mustOK(t, err)
	data[len(data)/2] ^= 0x40
	mustOK(t, os.WriteFile(path, data, 0o600))
	if s, err := openStore(dir, true, true); err == nil {
		s.Close()
		t.Fatal("checkpoint masked journal damage")
	}
}

// Replaying more rows than the overlay bound spills private generations and
// yields the same namespace as an unbounded replay.
func TestNamespaceReplaySpills(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, false)
	mustOK(t, s.CreateBucket("b"))
	for i := range 160 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%03d", i%100), []byte(fmt.Sprint(i)), "text/plain", nil)
	}
	for i := range 40 {
		mustOK(t, s.DeleteObject("b", fmt.Sprintf("k%03d", i*2)))
	}
	mustOK(t, s.Close())
	full := nsDump(t, b3Store(t, dir, false))

	old := nsReplayFloor
	nsReplayFloor = 25
	t.Cleanup(func() { nsReplayFloor = old })
	spilled := b3Store(t, dir, false)
	if spilled.ns.gen == nil || !spilled.ns.gen.private {
		t.Fatal("replay did not spill")
	}
	if got := nsDump(t, spilled); got != full {
		t.Fatal("spilled replay differs from the unbounded replay")
	}
	tmp, _ := os.ReadDir(filepath.Join(dir, "tmp"))
	for _, e := range tmp {
		if strings.HasPrefix(e.Name(), "namespace-") {
			t.Fatalf("spill left %s behind", e.Name())
		}
	}
}

func TestAuthDigestTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	tab := newAuthDigestTable(2) // forces several rehashes
	model := map[[32]byte]int64{}
	for i := range 20000 {
		var k [32]byte
		for j := range k {
			k[j] = byte(rng.Uint32())
		}
		if i%3 == 0 && len(model) > 0 {
			for old := range model {
				k = old
				break
			}
		}
		tab.put(k, int64(i))
		model[k] = int64(i)
	}
	if len(tab.keys) != len(model) {
		t.Fatalf("%d entries, want %d", len(tab.keys), len(model))
	}
	for k, v := range model {
		if got, ok := tab.get(k); !ok || got != v {
			t.Fatalf("get = %d,%v want %d", got, ok, v)
		}
	}
	if _, ok := tab.get([32]byte{1, 2, 3}); ok {
		t.Fatal("absent digest found")
	}
}

func TestAuthRootsSortAndStream(t *testing.T) {
	s := b3Store(t, t.TempDir(), false)
	mustOK(t, s.CreateBucket("b"))
	want := map[string]bool{}
	for i := range 30 {
		e := mustPutObject(t, s, "b", fmt.Sprintf("k%d", i%7), []byte(fmt.Sprint("body", i)), "text/plain", nil)
		want[e.manifestUUID] = true
	}
	mustOK(t, s.nsConsolidate(false))
	mustPutObject(t, s, "b", "late", []byte("late"), "text/plain", nil) // still in the overlay
	// Rows whose manifest names are not canonical UUIDs take the inline encoding.
	odd := &objectEntry{manifestUUID: "not-a-uuid", etag: "3-parts", contentType: "x/y"}
	odd.manifestSHA256[5] = 9
	s.tSetObj("b", "odd", odd)
	s.mu.Lock()
	s.histSet("b", "odd", []*historyVersionEntry{{versionID: "v", manifestUUID: "odd-hist", etag: "e", archivedAt: time.Unix(5, 0).UTC(), reason: "r", seq: 99}})
	s.mu.Unlock()
	mustOK(t, s.nsConsolidate(false))
	s.tSetObj("b", "odd2", odd)
	roots, err := s.authSimilarityRoots(nil)
	mustOK(t, err)
	objs, err := s.snapshotNamespace()
	mustOK(t, err)
	hist, err := s.snapshotHistory()
	mustOK(t, err)
	var wantRoots []string
	for _, o := range objs {
		wantRoots = append(wantRoots, fmt.Sprintf("%s/%x", o.entry.manifestUUID, o.entry.manifestSHA256))
	}
	for _, h := range hist {
		wantRoots = append(wantRoots, fmt.Sprintf("%s/%x", h.entry.manifestUUID, h.entry.manifestSHA256))
	}
	var gotRoots []string
	for i := range roots.len() {
		gotRoots = append(gotRoots, fmt.Sprintf("%s/%x", roots.uuid(i), roots.refs[i].sha))
	}
	slices.Sort(wantRoots)
	slices.Sort(gotRoots)
	if !slices.Equal(wantRoots, gotRoots) {
		t.Fatalf("streamed roots differ from the decoded namespace: %d vs %d", len(gotRoots), len(wantRoots))
	}
	got := map[string]bool{}
	prev := ""
	for i := range roots.len() {
		u := roots.uuid(i)
		if u < prev {
			t.Fatalf("roots unsorted at %d", i)
		}
		prev = u
		got[u] = true
	}
	if roots.len() != 7+23+1+3 || len(got) != roots.len()-1 {
		t.Fatalf("%d roots (%d distinct), want 34 with one shared", roots.len(), len(got))
	}
	for u := range want {
		if !got[u] {
			t.Fatalf("manifest %s missing from the roots", u)
		}
	}
}

// ---- zstd model-window reach ----

func reachRoot(t *testing.T, l *authLab, name string) (*repRoot, *repObject) {
	t.Helper()
	root, _, err := l.s.repConstructAt(l.man(name), nil, nil, 0, true)
	mustOK(t, err)
	src := l.s.newRepSource(root.Descs, nil)
	defer src.close()
	call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
	mustOK(t, err)
	return root, call.Rep
}

// Every zstd fixture gets a reach table; restarting from the reach is byte exact at
// every block and range, and fetches no more plaintext than the full model window.
func TestZstdReachRestartsAreExact(t *testing.T) {
	files := seekFixtures(t)
	var savedRatio []string
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range files {
			if strings.HasSuffix(n, ".zst") && n != "nested.zst" && !yield(n) {
				return
			}
		}
	}) {
		t.Run(name, func(t *testing.T) {
			l := newAuthLab(t)
			data := files[name]
			l.put(name, data)
			root, rep := reachRoot(t, l, name)
			_, sizes, reach, err := zstdRecipeReach(rep)
			mustOK(t, err)
			if reach == nil || len(reach) != len(sizes) {
				t.Fatalf("no reach table: %d entries for %d blocks", len(reach), len(sizes))
			}
			sk := seekOf(t, l.s, root).(*zsSeek)
			var window int64
			for _, h := range sk.hdrs {
				if h != nil {
					window = max(window, h.window)
				}
			}
			fetch := func(off, n int64) (int64, []byte) {
				var plain int64
				repSourceObserver = func(_ repReadStats, cw repCodecTrace) { plain += cw.PlainBytes }
				defer func() { repSourceObserver = nil }()
				got, err := seekRead(t, l.s, root, off, n)
				mustOK(t, err)
				return plain, got
			}
			var withReach, full int64
			var maxReach uint64
			for _, r := range reach {
				maxReach = max(maxReach, r)
			}
			for i, sg := range sk.segs {
				if sg.block < 0 || i%2 == 1 && len(sk.segs) > 200 {
					continue
				}
				for _, n := range []int64{1, 4096, 3*128<<10 + 5} {
					off := sg.off + int64(i%7)
					if off >= int64(len(data)) {
						continue
					}
					n = min(n, int64(len(data))-off)
					zsIgnoreReach = false
					l.s.purgeReadCaches()
					l.s.rep.rc.Store(nil)
					a, got := fetch(off, n)
					if !bytes.Equal(got, data[off:off+n]) {
						t.Fatalf("range %d+%d differs with the reach table", off, n)
					}
					zsIgnoreReach = true
					l.s.purgeReadCaches()
					l.s.rep.rc.Store(nil)
					b, got2 := fetch(off, n)
					zsIgnoreReach = false
					if !bytes.Equal(got2, data[off:off+n]) {
						t.Fatalf("range %d+%d differs with the full window", off, n)
					}
					if a > b {
						t.Fatalf("range %d+%d fetched %d plaintext bytes with reach, %d without", off, n, a, b)
					}
					withReach, full = withReach+a, full+b
				}
			}
			savedRatio = append(savedRatio, fmt.Sprintf("%s window=%d maxreach=%d fetched %d of %d", name, window, maxReach, withReach, full))
			if int64(maxReach) > window {
				t.Fatalf("reach %d exceeds the window %d", maxReach, window)
			}
		})
	}
	t.Log("\n" + strings.Join(savedRatio, "\n"))
}

// The proof of a representation checks its reach table: an understated block reach
// is refused, an overstated one is merely conservative.
func TestZstdReachTableIsProven(t *testing.T) {
	l := newAuthLab(t)
	files := seekFixtures(t)
	name := "reach.l9w23.zst"
	l.put(name, files[name])
	man := l.man(name)
	root, rep := reachRoot(t, l, name)
	_, sizes, reach, err := zstdRecipeReach(rep)
	mustOK(t, err)
	tail := len(repSeekAppend(nil, sizes)) + len(repReachAppend(nil, reach))
	head := rep.Recipe[:len(rep.Recipe)-tail]
	prove := func(forgedReach []uint64) error {
		forged := *rep
		forged.Recipe = repReachAppend(repSeekAppend(slices.Clone(head), sizes), forgedReach)
		b := l.s.newRepBuilder(man.TotalLength, nil, nil)
		defer b.close()
		for id, d := range root.Descs {
			b.descs[id] = d
		}
		id, err := b.finishRep(&forged)
		mustOK(t, err)
		mustOK(t, b.flush())
		return b.proof(id, man.TotalLength, root.ObjSHA)
	}
	if err := prove(reach); err != nil {
		t.Fatalf("the recorded reach table failed its own proof: %v", err)
	}
	longer := slices.Clone(reach)
	deep := -1
	for i, r := range longer {
		longer[i] = r + 5
		if r > 0 {
			deep = i
		}
	}
	if err := prove(longer); err != nil {
		t.Fatalf("a conservative reach table was refused: %v", err)
	}
	if deep < 0 {
		t.Fatal("fixture has no block with a backward reach")
	}
	shorter := slices.Clone(reach)
	shorter[deep]--
	if err := prove(shorter); err == nil {
		t.Fatal("a reach table that understates a block's reach passed its proof")
	}
	if err := prove(reach[:len(reach)-1]); err == nil {
		t.Fatal("a reach table with a missing entry passed its proof")
	}
}

// TestB3ReachProbe measures cold restarts of one large zstd file with and without
// the reach table: ZEROS3_B3_ZST=file.zst go test -run TestB3ReachProbe -v .
func TestB3ReachProbe(t *testing.T) {
	path := os.Getenv("ZEROS3_B3_ZST")
	if path == "" {
		t.Skip("set ZEROS3_B3_ZST")
	}
	data, err := os.ReadFile(path)
	mustOK(t, err)
	l := newAuthLab(t)
	l.put("big.zst", data)
	t0 := time.Now()
	root, rep := reachRoot(t, l, "big.zst")
	built := time.Since(t0)
	_, sizes, reach, err := zstdRecipeReach(rep)
	mustOK(t, err)
	sk := seekOf(t, l.s, root).(*zsSeek)
	var window int64
	for _, h := range sk.hdrs {
		if h != nil {
			window = max(window, h.window)
		}
	}
	sorted := slices.Clone(reach)
	slices.Sort(sorted)
	rng := rand.New(rand.NewPCG(9, 9))
	type result struct {
		plain int64
		d     time.Duration
	}
	run := func(ignore bool) (res []result) {
		zsIgnoreReach = ignore
		defer func() { zsIgnoreReach = false }()
		for range 40 {
			off := rng.Int64N(int64(len(data)) - 4096)
			l.s.purgeReadCaches()
			l.s.rep.rc.Store(nil)
			var plain int64
			repSourceObserver = func(_ repReadStats, cw repCodecTrace) { plain += cw.PlainBytes }
			start := time.Now()
			got, err := seekRead(t, l.s, root, off, 4096)
			d := time.Since(start)
			repSourceObserver = nil
			mustOK(t, err)
			if !bytes.Equal(got, data[off:off+4096]) {
				t.Fatalf("read at %d differs", off)
			}
			res = append(res, result{plain, d})
		}
		return res
	}
	summary := func(rs []result) (mean int64, median time.Duration) {
		var sum int64
		ds := make([]time.Duration, len(rs))
		for i, r := range rs {
			sum += r.plain
			ds[i] = r.d
		}
		slices.Sort(ds)
		return sum / int64(len(rs)), ds[len(ds)/2]
	}
	rm, rd := summary(run(false))
	fm, fd := summary(run(true))
	out := map[string]any{"file": filepath.Base(path), "bytes": len(data), "blocks": len(sizes), "window": window,
		"reach_median": sorted[len(sorted)/2], "reach_p90": sorted[len(sorted)*9/10], "reach_max": sorted[len(sorted)-1],
		"reach_section_bytes": len(repReachAppend(nil, reach)), "seek_section_bytes": len(repSeekAppend(nil, sizes)),
		"build_ms": built.Milliseconds(), "plain_mean_reach": rm, "plain_mean_full": fm, "median_ms_reach": float64(rd.Microseconds()) / 1000, "median_ms_full": float64(fd.Microseconds()) / 1000}
	line, _ := json.Marshal(out)
	fmt.Printf("B3REACH %s\n", line)
}

// ---- admission pricing ----

// The planner prices a not yet packed component as the pack writer will store it: the
// level it used to price at (BestSpeed) overstated compressible components by far more.
func TestAuthPriceMatchesPackedBytes(t *testing.T) {
	l := newAuthLab(t)
	rng := rand.New(rand.NewPCG(11, 12))
	words := make([]string, 400)
	for i := range words {
		words[i] = strings.Repeat(string(rune('a'+rng.IntN(26))), 1+rng.IntN(3)) + fmt.Sprint(rng.IntN(1000))
	}
	var sb strings.Builder
	for sb.Len() < 3<<20 {
		sb.WriteString(words[rng.IntN(len(words))])
		sb.WriteByte(" \n;{}"[rng.IntN(5)])
	}
	l.put("text", []byte(sb.String()))
	man := l.man("text")
	var chunks [][32]byte
	var sizes []int64
	for _, c := range man.Chunks {
		sum, err := decodeHexSHA256(c.SHA256)
		mustOK(t, err)
		chunks, sizes = append(chunks, sum), append(sizes, c.Length)
	}
	price := func(level int) (n int64) {
		for i, sum := range chunks {
			data, err := l.s.casRead(sum)
			mustOK(t, err)
			n += int64(xdDeflateLen(data, level))
			_ = sizes[i]
		}
		return n
	}
	priced := map[[32]byte]int64{}
	var planned int64
	for i, sum := range chunks {
		planned += l.s.physChunk(sum, sizes[i], priced, false) - packRecordBytes
	}
	if planned != price(packPriceLevel) {
		t.Fatalf("physChunk prices %d bytes, level %d deflate gives %d", planned, packPriceLevel, price(packPriceLevel))
	}
	l.offline(func() {
		_, err := compactStore(l.dir, authCompactOpts())
		mustOK(t, err)
		_, err = repackStore(l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true, Recompress: true})
		mustOK(t, err)
	})
	var stored int64
	for _, sum := range chunks {
		loc, ok := l.s.packLookup(sum)
		if !ok {
			t.Fatal("chunk not packed")
		}
		stored += int64(loc.stored)
	}
	if d := float64(planned-stored) / float64(stored); d < -0.02 || d > 0.02 {
		t.Fatalf("planned %d bytes, the pack stores %d (%.1f%% off)", planned, stored, 100*d)
	}
	fast := price(1)
	t.Logf("component bytes: pack stores %d, planner prices %d (level %d), the old level-1 price was %d (+%.1f%%)", stored, planned, packPriceLevel, fast, 100*float64(fast-stored)/float64(stored))
	if float64(fast) < 1.03*float64(stored) {
		t.Fatalf("fixture too incompressible to show the old pricing error: level 1 %d vs stored %d", fast, stored)
	}
}

// A representation group's economics are reported next to the per-object ones, and
// the estimate does not depend on the policy threshold that accepts or refuses it.
func TestAuthGroupEconomicsAreReported(t *testing.T) {
	files := seekFixtures(t)
	run := func(minSaving float64) AuthReport {
		l := newAuthLab(t)
		l.put("a.gz", files["text.l6.gz"])
		l.put("b.gz", files["text.l9.gz"])
		rep, err := l.s.AuthorityMigrate(AuthPolicy{Order: "oldest", AllowRep: true, CodecReplay: true, NoZN7: true, MinSaving: minSaving})
		mustOK(t, err)
		return rep
	}
	open, closed := run(-10), run(0.999)
	var est []float64
	for name, rep := range map[string]AuthReport{"admitted": open, "refused": closed} {
		if len(rep.Plans) != 2 {
			t.Fatalf("%s: %d plans", name, len(rep.Plans))
		}
		for _, p := range rep.Plans {
			g := p.Group
			if g == nil || g.Kind != "codec" || g.Members != 2 || g.AddedBytes <= 0 || g.CDCBytes <= 0 {
				t.Fatalf("%s: plan lacks its group economics: %+v", name, p)
			}
			if want := 1 - float64(g.AddedBytes)/float64(g.CDCBytes); math.Abs(g.Saving-want) > 1e-9 {
				t.Fatalf("%s: group saving %.4f, expected %.4f", name, g.Saving, want)
			}
			if g.Admitted != (strings.TrimSuffix(p.Form, "?") != "cdc") {
				t.Fatalf("%s: group admitted=%v but the object's form is %s", name, g.Admitted, p.Form)
			}
			if !g.Admitted && !strings.Contains(p.Reason, "group priced") {
				t.Fatalf("%s: refusal does not name the group estimate: %q", name, p.Reason)
			}
			est = append(est, g.Saving)
		}
	}
	if !open.Plans[0].Group.Admitted || closed.Plans[0].Group.Admitted {
		t.Fatal("the policy threshold did not decide admission")
	}
	for _, e := range est {
		if math.Abs(e-est[0]) > 1e-9 {
			t.Fatalf("the group estimate moved with the policy: %v", est)
		}
	}
}

func rootsLen(r *authRoots) int { return r.len() }

// A content-digest table of a million entries against the Go map it replaces.
func BenchmarkB3DigestTable(b *testing.B) {
	keys := make([][32]byte, 1000000)
	for i := range keys {
		keys[i] = sha256.Sum256(fmt.Appendf(nil, "key %d", i))
	}
	b.Run("map", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			m := make(map[[32]byte]int64)
			for i, k := range keys {
				m[k] = int64(i)
			}
		}
	})
	b.Run("table", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			t := newAuthDigestTable(len(keys))
			for i, k := range keys {
				t.put(k, int64(i))
			}
		}
	})
}
