package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
		sum, crc, err := journalCheckpointDigest(s.journal.f, s.journal.writeOffset)
		mustOK(t, err)
		if m.PrefixSHA != sum || m.LastCRC != crc || m.Offset != s.journal.writeOffset || len(m.HashState) == 0 {
			t.Fatalf("round %d: chained hash disagrees with a full journal hash", round)
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
