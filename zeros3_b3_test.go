package main

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
	it := &nsOvIter[int]{blocks: o.snapshot()}
	it.seek("k01000")
	var got []string
	for ; it.valid() && it.cur().k < "k02000"; it.next() {
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

// Snapshots share the overlay's blocks; later writes (in place, inserts,
// splits, new first blocks) must never show through an earlier snapshot.
func TestNSOverlaySnapshotsAreImmutable(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	var o nsOverlay[int]
	model := map[string]int{}
	type snap struct {
		blocks [][]nsKV[int]
		want   []nsKV[int]
	}
	var snaps []snap
	flat := func(blocks [][]nsKV[int]) (out []nsKV[int]) {
		for _, b := range blocks {
			out = append(out, b...)
		}
		return out
	}
	for i := range 30000 {
		k := fmt.Sprintf("k%05d", rng.IntN(5000))
		if i%7 == 0 {
			k = fmt.Sprintf("a%05d", i) // new keys before everything
		}
		o.set(k, i)
		model[k] = i
		if i%997 == 0 {
			b := o.snapshot()
			snaps = append(snaps, snap{b, slices.Clone(flat(b))})
		}
	}
	for j, s := range snaps {
		if got := flat(s.blocks); !slices.Equal(got, s.want) {
			t.Fatalf("snapshot %d changed after later writes", j)
		}
	}
	got := flat(o.snapshot())
	if len(got) != len(model) || !slices.IsSortedFunc(got, func(a, b nsKV[int]) int { return strings.Compare(a.k, b.k) }) {
		t.Fatalf("overlay holds %d sorted? entries, want %d", len(got), len(model))
	}
	for _, e := range got {
		if model[e.k] != e.v {
			t.Fatalf("%s = %d, want %d", e.k, e.v, model[e.k])
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
		if pages > 1_000_000 {
			t.Fatalf("listing (%q,%q,%d) does not terminate at %q", prefix, delim, max, after)
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

func testLiveManifests(tb testing.TB, s *Store) []liveMan {
	lm, err := s.liveManifests()
	if err != nil {
		if tb == nil {
			panic(err)
		}
		tb.Helper()
		tb.Fatal(err)
	}
	return lm
}

func mustSnapshotNamespace(tb testing.TB, s *Store) []namespaceObject {
	tb.Helper()
	out, err := s.snapshotNamespace()
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

func mustSnapshotHistory(tb testing.TB, s *Store) []historyNamespaceObject {
	tb.Helper()
	out, err := s.snapshotHistory()
	if err != nil {
		tb.Fatal(err)
	}
	return out
}

// ---- B3-R: completeness and commit-outcome fences ----

// b3DamagePage flips bytes inside page i of a table of the open store's
// generation and drops cached pages, so the next read of that page fails.
func b3DamagePage(t *testing.T, s *Store, dir string, hist bool, i int) {
	t.Helper()
	tab := s.ns.obj
	if hist {
		tab = s.ns.hist
	}
	if i < 0 {
		i += tab.pages()
	}
	f, err := os.OpenFile(filepath.Join(dir, "journal", nsGenName), os.O_WRONLY, 0)
	mustOK(t, err)
	_, err = f.WriteAt([]byte{0xA5, 0x5A, 0xC3}, int64(tab.dir.Off[i])+5)
	mustOK(t, err)
	mustOK(t, f.Close())
	s.ns.cache = newNSPageCache()
	s.ns.gen.cache = s.ns.cache
}

func b3ReadVersion(t *testing.T, s *Store, bucket, key string, i int) []byte {
	t.Helper()
	vs, _, err := s.ListVersions(bucket, key)
	mustOK(t, err)
	man, err := s.readVerifiedManifest(vs[i].manifestUUID, vs[i].manifestSHA256)
	mustOK(t, err)
	return repReadAll(t, s, man, byteRange{0, man.TotalLength - 1})
}

// Authority retirement after an incomplete namespace walk would drop bodies
// whose CDC chunks were never re-created. The authority-backed contents sort
// after many filler rows and one lives only in retained history.
func TestAuthorityRevertAllRefusesIncompleteNamespace(t *testing.T) {
	l := newAuthLab(t)
	for i := range 1200 {
		l.put(fmt.Sprintf("a%04d", i), []byte(fmt.Sprintf("filler %d", i)))
	}
	hist := authHistory(131, 4, 300_000)
	for i, v := range hist {
		l.put(fmt.Sprintf("k%d", i), v)
	}
	l.put("k0", genRandomBytes(9, 50_000)) // hist[0] survives only as history
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.reclaim()
	dir := l.dir
	l.close()
	_, level := readFormatVersion(t, dir)
	if !levelHasAuthority(level) {
		t.Fatalf("authority not adopted: level %d", level)
	}
	{
		s, err := openStore(dir, true, false)
		mustOK(t, err)
		mustOK(t, s.nsConsolidate(true))
		mustOK(t, s.Close())
	}
	for _, c := range []struct {
		name string
		hist bool
		page int
	}{{"last-object-page", false, -1}, {"history-page", true, 0}} {
		t.Run(c.name, func(t *testing.T) {
			s, err := openStore(dir, true, true)
			mustOK(t, err)
			s.ns.auto = false
			if s.ns.gen == nil || s.ns.objMut.n != 0 {
				t.Fatal("expected the store to open from the published generation")
			}
			records, err := s.authFiles()
			mustOK(t, err)
			if len(records) == 0 {
				t.Fatal("no authority records")
			}
			b3DamagePage(t, s, dir, c.hist, c.page)
			if _, err := s.AuthorityRevertAll(); err == nil {
				t.Fatal("revert succeeded over an unreadable namespace page")
			}
			for _, sha := range records {
				if err := s.AuthorityRevert(sha); err == nil {
					t.Fatalf("single revert of %x succeeded over an unreadable page", sha[:4])
				}
			}
			after, err := s.authFiles()
			mustOK(t, err)
			if len(after) != len(records) {
				t.Fatalf("%d of %d authority records retired", len(records)-len(after), len(records))
			}
			mustOK(t, s.Close())
			if _, l2 := readFormatVersion(t, dir); l2 != level {
				t.Fatalf("format level %d after a refused revert, want %d", l2, level)
			}

			// Reopening rejects the damaged file and replays the journal; every
			// body, including the historical one, is still exact.
			s, err = openStore(dir, true, true)
			mustOK(t, err)
			defer s.Close()
			for i, v := range hist[1:] {
				if got := repReadAll(t, s, testHeadManifest(t, s, fmt.Sprintf("k%d", i+1)), byteRange{0, int64(len(v)) - 1}); !bytes.Equal(got, v) {
					t.Fatalf("k%d differs after recovery", i+1)
				}
			}
			if got := b3ReadVersion(t, s, "b", "k0", 0); !bytes.Equal(got, hist[0]) {
				t.Fatal("the historical body differs after recovery")
			}
		})
		// Restore the checkpoint for the next case.
		s, err := openStore(dir, true, false)
		mustOK(t, err)
		mustOK(t, s.nsConsolidate(true))
		mustOK(t, s.Close())
	}

	// With a readable namespace the revert completes and nothing is lost.
	s, err := openStore(dir, true, true)
	mustOK(t, err)
	defer s.Close()
	rep, err := s.AuthorityRevertAll()
	mustOK(t, err)
	if rep.Retired != rep.Records || rep.Records == 0 {
		t.Fatalf("revert report %+v", rep)
	}
	if got := b3ReadVersion(t, s, "b", "k0", 0); !bytes.Equal(got, hist[0]) {
		t.Fatal("the historical body differs after revert")
	}
}

func testHeadManifest(t *testing.T, s *Store, key string) manifestV1 {
	t.Helper()
	_, man, err := s.HeadObject("b", key)
	mustOK(t, err)
	return man
}

// A write whose history lookup fails must be rejected before its journal frame
// exists: an error reported for a durable frame would hide a committed change.
func TestCommitHistoryLookupFailsBeforeJournal(t *testing.T) {
	v1, v2, v3 := []byte("first"), []byte("second"), []byte("third")
	setup := func(t *testing.T) (string, *Store) {
		dir := t.TempDir()
		s := b3Store(t, dir, true)
		mustOK(t, s.CreateBucket("b"))
		mustPutObject(t, s, "b", "h", v1, "text/plain", nil)
		mustPutObject(t, s, "b", "h", v2, "text/plain", nil)
		for i := range 40 {
			mustPutObject(t, s, "b", fmt.Sprintf("x%03d", i), []byte("x"), "text/plain", nil)
		}
		mustOK(t, s.nsConsolidate(true))
		mustOK(t, s.Close())
		s = b3Store(t, dir, true)
		if s.ns.hist.pages() != 1 || s.ns.objMut.n != 0 {
			t.Fatalf("fixture: %d history pages, %d overlay rows", s.ns.hist.pages(), s.ns.objMut.n)
		}
		return dir, s
	}
	// After the failure, a replay from the journal alone must show h unchanged.
	recovered := func(t *testing.T, dir string) *Store {
		t.Helper()
		s := b3Store(t, dir, true)
		if _, got, err := s.GetObject("b", "h"); err != nil || !bytes.Equal(got, v2) {
			t.Fatalf("h after replay = %q, %v", got, err)
		}
		if vs, _, err := s.ListVersions("b", "h"); err != nil || len(vs) != 1 {
			t.Fatalf("h history after replay: %d rows, %v", len(vs), err)
		}
		return s
	}
	ops := map[string]func(s *Store) error{
		"put": func(s *Store) error { _, err := s.PutObject("b", "h", v3, "text/plain", nil); return err },
		"put-serial": func(s *Store) error {
			pubAblate.serialCommit = true
			defer func() { pubAblate.serialCommit = false }()
			_, err := s.PutObject("b", "h", v3, "text/plain", nil)
			return err
		},
		"delete": func(s *Store) error { return s.DeleteObject("b", "h") },
	}
	for _, name := range []string{"put", "put-serial", "delete", "multipart"} {
		op := ops[name]
		t.Run(name, func(t *testing.T) {
			dir, s := setup(t)
			var seq uint64
			if name == "multipart" { // the upload's own frames precede the damage
				op = func(s *Store) error {
					id, err := s.CreateMultipartUpload("b", "h", "text/plain", nil)
					mustOK(t, err)
					etag, err := s.UploadPart("b", "h", id, 1, v3)
					mustOK(t, err)
					seq = s.journal.nextSeq
					b3DamagePage(t, s, dir, true, 0)
					_, _, err = s.CompleteMultipartUpload("b", "h", id, []completedPart{{1, etag}})
					return err
				}
			} else {
				seq = s.journal.nextSeq
				b3DamagePage(t, s, dir, true, 0)
			}
			if err := op(s); !errors.Is(err, errNSCorrupt) {
				t.Fatalf("expected the history read to fail the write: %v", err)
			}
			if s.journal.nextSeq != seq {
				t.Fatalf("a rejected write appended %d journal frames", s.journal.nextSeq-seq)
			}
			s.Close()
			recovered(t, dir)
		})
	}

	t.Run("prune", func(t *testing.T) {
		dir, s := setup(t)
		keep := 0
		res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Key: "h", KeepLast: &keep})
		mustOK(t, err)
		if len(res.Versions) != 1 {
			t.Fatalf("prune plan %d versions", len(res.Versions))
		}
		mustOK(t, s.ensureStoreFormat(storeFormatVersionHistoryPrune))
		seq := s.journal.nextSeq
		b3DamagePage(t, s, dir, true, 0)
		if err := s.ApplyHistoryPrune(&res); !errors.Is(err, errNSCorrupt) || res.VersionsPruned != 0 {
			t.Fatalf("prune over an unreadable history page: %v, %d pruned", err, res.VersionsPruned)
		}
		if s.journal.nextSeq != seq {
			t.Fatal("a rejected prune appended a journal frame")
		}
		s.Close()
		recovered(t, dir)
	})

	// Concurrent grouped commits: every acknowledged write is durable and every
	// rejected one is absent after replay, whatever the batch composition.
	t.Run("grouped", func(t *testing.T) {
		for round := range 5 {
			dir, s := setup(t)
			b3DamagePage(t, s, dir, true, 0)
			keys := []string{"h", "n0", "n1", "n2", "n3", "n4", "n5", "x001"}
			errs := make([]error, len(keys))
			var start, wg sync.WaitGroup
			start.Add(1)
			for i, k := range keys {
				wg.Go(func() {
					start.Wait()
					_, errs[i] = s.PutObject("b", k, []byte(fmt.Sprintf("%s-%d", k, round)), "text/plain", nil)
				})
			}
			start.Done()
			wg.Wait()
			if errs[0] == nil {
				t.Fatal("the write needing the damaged history page succeeded")
			}
			s.Close()
			r := recovered(t, dir)
			for i, k := range keys[1:] {
				_, got, err := r.GetObject("b", k)
				switch {
				case errs[i+1] == nil && string(got) != fmt.Sprintf("%s-%d", k, round):
					t.Fatalf("acknowledged %s lost: %q %v", k, got, err)
				case errs[i+1] != nil && k != "x001" && !errors.Is(err, errNoSuchKey):
					t.Fatalf("rejected %s became visible: %q %v", k, got, err)
				case errs[i+1] != nil && k == "x001" && string(got) != "x":
					t.Fatalf("rejected x001 changed: %q", got)
				}
			}
			r.Close()
		}
	})
}

// ---- B3-R: V4 parser hardening ----

// b3PageOf encodes rows (already sorted) as one table page.
func b3PageOf(t testing.TB, keys []string, payload func(i int) []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	bw := bufio.NewWriter(&buf)
	w := &nsTableWriter{out: &nsFileOut{bw: bw, h: sha256.New()}}
	for i, k := range keys {
		if err := w.add([]byte(k), payload(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmp.Or(w.flush(), bw.Flush()); err != nil {
		t.Fatal(err)
	}
	if len(w.dir.Off) != 1 {
		t.Fatalf("%d pages", len(w.dir.Off))
	}
	return buf.Bytes()
}

func b3Reseal(b []byte) []byte {
	binary.LittleEndian.PutUint32(b[len(b)-4:], crc32.Checksum(b[:len(b)-4], castagnoliTable))
	return b
}

func b3ObjPayload(i int) []byte {
	d := newNSDict(nil)
	return d.appendObject(nil, &objectEntry{manifestUUID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", etag: fmt.Sprintf("%032x", i), contentType: "text/plain", size: int64(i), seq: uint64(i + 1)})
}

// b3Exercise runs every page reader over b; none may panic, whatever b holds.
func b3Exercise(b, first, next []byte, probes []string) error {
	verr := nsValidatePage(b, first, next)
	p, err := nsParsePage(b)
	if err != nil {
		return cmp.Or(verr, err)
	}
	for _, k := range probes {
		p.find(k)
	}
	for i := range len(p.restarts) / 4 {
		p.restartKey(i)
	}
	var key []byte
	for pos, n := 0, 0; pos < len(p.rows) && n < 1<<16; n++ {
		if pos, key, _, err = p.row(pos, key); err != nil {
			break
		}
	}
	return verr
}

func TestNSPageRejectsMalformedStructure(t *testing.T) {
	keys := make([]string, 40)
	for i := range keys {
		keys[i] = fmt.Sprintf("b\x00k%03d", i)
	}
	valid := b3PageOf(t, keys, b3ObjPayload)
	first, next := []byte(keys[0]), []byte("b\x00zzz")
	probes := []string{keys[0], keys[17], keys[39], "b\x00a", "b\x00k0175", "c"}
	if err := b3Exercise(valid, first, next, probes); err != nil {
		t.Fatalf("valid page rejected: %v", err)
	}
	trailer := len(valid) - 12
	restarts := trailer - 4*3 // 40 rows: restarts at 0, 16, 32
	mod := func(f func(b []byte) []byte) []byte { return b3Reseal(f(slices.Clone(valid))) }
	put := func(b []byte, off int, v uint32) []byte { binary.LittleEndian.PutUint32(b[off:], v); return b }
	cases := map[string][]byte{
		"restart beyond rows":     mod(func(b []byte) []byte { return put(b, restarts+4, uint32(restarts+100)) }),
		"restart at rows end":     mod(func(b []byte) []byte { return put(b, restarts+8, uint32(restarts)) }),
		"restart off row start":   mod(func(b []byte) []byte { return put(b, restarts+4, 3) }),
		"restart count too large": mod(func(b []byte) []byte { return put(b, trailer, 1<<30) }),
		"restart count zero":      mod(func(b []byte) []byte { return put(b, trailer, 0) }),
		"restart count short":     mod(func(b []byte) []byte { return put(b, trailer, 2) }),
		"row count high":          mod(func(b []byte) []byte { return put(b, trailer+4, 41) }),
		"row count low":           mod(func(b []byte) []byte { return put(b, trailer+4, 39) }),
		"overlong varint at restart": mod(func(b []byte) []byte {
			off := int(binary.LittleEndian.Uint32(b[restarts+4:]))
			copy(b[off:], bytes.Repeat([]byte{0xff}, 10))
			return b
		}),
		"truncated rows":        b3Reseal(append(slices.Clone(valid[:40]), valid[restarts:]...)),
		"duplicate key":         b3PageOf(t, append(slices.Clone(keys[:20]), keys[19:39]...), b3ObjPayload),
		"unsorted keys":         b3PageOf(t, append([]string{keys[5]}, keys[:39]...), b3ObjPayload),
		"key without separator": b3PageOf(t, []string{"plain"}, b3ObjPayload),
		"short":                 b3Reseal(make([]byte, 12)),
	}
	for name, b := range cases {
		if err := b3Exercise(b, first, next, probes); !errors.Is(err, errNSCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Directory context: the first key must match and the last stay below the next page.
	if err := nsValidatePage(valid, []byte(keys[1]), next); !errors.Is(err, errNSCorrupt) {
		t.Errorf("first key mismatch: %v", err)
	}
	if err := nsValidatePage(valid, first, []byte(keys[39])); !errors.Is(err, errNSCorrupt) {
		t.Errorf("overlapping next page: %v", err)
	}
}

func TestNSPayloadDecodersAgree(t *testing.T) {
	d := newNSDict([]string{"text/plain"})
	obj := d.appendObject(nil, &objectEntry{manifestUUID: "not-a-uuid", etag: "x", contentType: "a/b", size: 7, seq: 3})
	hist, err := d.appendHistory(nil, []*historyVersionEntry{
		{versionID: "v1", manifestUUID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", etag: "e", contentType: "text/plain", archivedAt: time.Unix(5, 6).UTC(), reason: "deleted", seq: 9},
		{versionID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c", manifestUUID: "m", archivedAt: time.Unix(7, 0).In(time.FixedZone("x", 3600)), seq: 12},
	})
	mustOK(t, err)
	g := &nsGen{strs: d.strs}
	for name, c := range map[string][]byte{
		"object": obj, "object trailing": append(slices.Clone(obj), 0),
		"object bad ref": append([]byte{99}, obj[1:]...), "object truncated": obj[:len(obj)-1],
		"object negative size": d.appendObject(nil, &objectEntry{manifestUUID: "u", size: -1}),
	} {
		_, derr := g.decodeObject(c)
		_, _, rerr := objectRoot(c, nil, g.strs)
		if (derr == nil) != (rerr == nil) || (name == "object") != (derr == nil) {
			t.Errorf("%s: decode %v, root %v", name, derr, rerr)
		}
	}
	zero := binary.AppendUvarint(nil, 0)
	for name, c := range map[string][]byte{
		"history": hist, "history trailing": append(slices.Clone(hist), 1), "history truncated": hist[:len(hist)-3],
		"history empty": zero, "history huge count": binary.AppendUvarint(nil, 1<<40),
		"history bad time": func() []byte {
			b := binary.AppendUvarint(nil, 1)
			b = append(b, 0)                   // flags: inline time
			b = binary.AppendUvarint(b, 1)     // seq delta
			b = binary.AppendUvarint(b, 3)     // time length
			return append(b, 9, 9, 9, 1, 1, 0) // garbage time, refs follow
		}(),
	} {
		_, derr := g.decodeHistory(c)
		_, rerr := historyRoots(c, nil, g.strs, func([]byte, [32]byte) {})
		if (derr == nil) != (rerr == nil) || (name == "history") != (derr == nil) {
			t.Errorf("%s: decode %v, roots %v", name, derr, rerr)
		}
	}
}

func FuzzNSPage(f *testing.F) {
	keys := []string{"b\x00a", "b\x00ab", "b\x00b", "c\x00"}
	f.Add(b3PageOf(f, keys, b3ObjPayload))
	many := make([]string, 33)
	for i := range many {
		many[i] = fmt.Sprintf("b\x00%02d", i)
	}
	f.Add(b3PageOf(f, many, func(int) []byte { return []byte{1} }))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 12 {
			return
		}
		b = b3Reseal(slices.Clone(b))
		err := b3Exercise(b, []byte("b\x00a"), nil, keys)
		if err == nil {
			// A page that validates has rows whose payloads either decode or fail cleanly.
			nsCheckRows(b, false, nil, new([]byte), map[string]int{})
			nsCheckRows(b, true, nil, new([]byte), map[string]int{})
		}
	})
}

func FuzzNSPayload(f *testing.F) {
	d := newNSDict(nil)
	f.Add(d.appendObject(nil, &objectEntry{manifestUUID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", etag: fmt.Sprintf("%032x", 1), size: 5}))
	h, _ := d.appendHistory(nil, []*historyVersionEntry{{versionID: "v", manifestUUID: "m", archivedAt: time.Unix(1, 0).UTC()}})
	f.Add(h)
	g := &nsGen{strs: d.strs}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, derr := g.decodeObject(b)
		if _, _, rerr := objectRoot(b, nil, g.strs); (derr == nil) != (rerr == nil) {
			t.Fatalf("object decode %v, root %v", derr, rerr)
		}
		_, derr = g.decodeHistory(b)
		if _, rerr := historyRoots(b, nil, g.strs, func([]byte, [32]byte) {}); (derr == nil) != (rerr == nil) {
			t.Fatalf("history decode %v, roots %v", derr, rerr)
		}
	})
}

// b3RewriteGen applies page and trailer edits to a generation file and
// recomputes every checksum, so only the interior structure is wrong.
func b3RewriteGen(t *testing.T, path string, page func(hist bool, i int, b []byte), meta func(m *nsMeta)) {
	t.Helper()
	raw, err := os.ReadFile(path)
	mustOK(t, err)
	f, err := os.Open(path)
	mustOK(t, err)
	g, _, err := openNSGen(f, newNSPageCache())
	mustOK(t, err)
	f.Close()
	body := len(raw) - sha256.Size
	metaOff := body - 16 - int(binary.LittleEndian.Uint32(raw[body-16:]))
	for _, tab := range []struct {
		hist bool
		dir  nsDirectory
	}{{false, g.meta.Obj}, {true, g.meta.Hist}} {
		for i := range tab.dir.Off {
			b := raw[tab.dir.Off[i] : tab.dir.Off[i]+uint64(tab.dir.Len[i])]
			if page != nil {
				page(tab.hist, i, b)
			}
			b3Reseal(b)
		}
	}
	m := g.meta
	if meta != nil {
		meta(&m)
	}
	var mb bytes.Buffer
	mustOK(t, gob.NewEncoder(&mb).Encode(m))
	out := append(slices.Clone(raw[:metaOff]), mb.Bytes()...)
	var tail [16]byte
	binary.LittleEndian.PutUint32(tail[0:], uint32(mb.Len()))
	binary.LittleEndian.PutUint32(tail[4:], crc32.Checksum(mb.Bytes(), castagnoliTable))
	copy(tail[8:], nsGenFooter)
	out = append(out, tail[:]...)
	sum := sha256.Sum256(out)
	mustOK(t, os.WriteFile(path, append(out, sum[:]...), 0o644))
}

// A generation whose checksums are right but whose interior is not (a writer
// defect) is refused at open in favor of replay, never served or kept.
func TestNamespaceCheckpointRejectsInvalidInterior(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	for i := range 1500 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i), []byte("x"), "text/plain", nil)
	}
	for i := range 300 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i*5), []byte("y"), "application/x-y", nil)
	}
	mustOK(t, s.nsConsolidate(true))
	mustOK(t, s.Close())
	want := nsDump(t, b3Store(t, dir, false))
	path := filepath.Join(dir, "journal", nsGenName)
	pristine, err := os.ReadFile(path)
	mustOK(t, err)
	restartOff := func(b []byte, r int) int {
		nr := int(binary.LittleEndian.Uint32(b[len(b)-12:]))
		return len(b) - 12 - 4*nr + 4*r
	}
	cases := map[string]struct {
		page func(hist bool, i int, b []byte)
		meta func(m *nsMeta)
	}{
		"restart into another row": {page: func(hist bool, i int, b []byte) {
			if !hist && i == 2 {
				copy(b[restartOff(b, 1):], b[restartOff(b, 2):restartOff(b, 2)+4])
			}
		}},
		"row count": {page: func(hist bool, i int, b []byte) {
			if hist && i == 0 {
				binary.LittleEndian.PutUint32(b[len(b)-8:], binary.LittleEndian.Uint32(b[len(b)-8:])+1)
			}
		}},
		"dictionary dropped": {meta: func(m *nsMeta) { m.Strings = nil }},
		"bucket count":       {meta: func(m *nsMeta) { m.Buckets[0].Count--; m.Obj.Rows-- }},
		"history rows":       {meta: func(m *nsMeta) { m.Hist.Rows++ }},
		"overlapping tables": {meta: func(m *nsMeta) { m.Hist.Off[0] = m.Obj.Off[len(m.Obj.Off)-1] }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustOK(t, os.WriteFile(path, pristine, 0o644))
			b3RewriteGen(t, path, c.page, c.meta)
			s := b3Store(t, dir, true)
			if s.ns.gen != nil {
				t.Fatal("a generation with an invalid interior was adopted")
			}
			if got := nsDump(t, s); got != want {
				t.Fatal("fallback open differs from the journal state")
			}
		})
	}

	// Damage with a valid page CRC after a successful open fails reads closed.
	mustOK(t, os.WriteFile(path, pristine, 0o644))
	s = b3Store(t, dir, true)
	if s.ns.gen == nil {
		t.Fatal("pristine generation not adopted")
	}
	raw, err := os.ReadFile(path)
	mustOK(t, err)
	i := s.ns.obj.pages() / 2
	b := raw[s.ns.obj.dir.Off[i] : s.ns.obj.dir.Off[i]+uint64(s.ns.obj.dir.Len[i])]
	binary.LittleEndian.PutUint32(b[restartOff(b, 1):], uint32(len(b))) // beyond the rows
	b3Reseal(b)
	mustOK(t, os.WriteFile(path, raw, 0o644))
	s.ns.cache = newNSPageCache()
	s.ns.gen.cache = s.ns.cache
	key := string(s.ns.obj.firstKey(i)[2:])
	if _, err := s.lookupObject("b", key); !errors.Is(err, errNSCorrupt) {
		t.Fatalf("lookup on a malformed page: %v", err)
	}
	if _, err := s.ListObjectsV2("b", "", "", "", 1000); !errors.Is(err, errNSCorrupt) {
		t.Fatalf("listing after damage: %v", err)
	}
}

// ---- B3-R: bounded listing over large overlays ----

// b3Inject puts n synthetic rows straight into the mutable overlay (listing
// cost only; nothing is journaled).
func b3Inject(s *Store, keys []string, tomb func(i int) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range keys {
		ck := nsKey("b", k)
		cur, _ := s.objFindCK(ck)
		var e *objectEntry
		if tomb == nil || !tomb(i) {
			e = &objectEntry{manifestUUID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", etag: "e", contentType: "text/plain", seq: uint64(i + 1)}
		}
		s.objSetCK("b", ck, e, cur != nil)
	}
}

func b3PageAllocs(t *testing.T, fn func()) (bytesPer uint64) {
	t.Helper()
	fn()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	const runs = 20
	for range runs {
		fn()
	}
	runtime.ReadMemStats(&b)
	return (b.TotalAlloc - a.TotalAlloc) / runs
}

func TestListSmallPagesOverLargeOverlay(t *testing.T) {
	dir := t.TempDir()
	s := b3Store(t, dir, true)
	mustOK(t, s.CreateBucket("b"))
	const groups, per = 6, 8000
	var keys []string
	for g := range groups {
		for i := range per {
			keys = append(keys, fmt.Sprintf("p/d%d/k%05d", g, i))
		}
	}
	keys = append(keys, "p/d3/", "p/top")
	// Half the rows live in a generation, half (and tombstones over some of the
	// table rows) in the mutable overlay.
	b3Inject(s, keys[:len(keys)/2], nil)
	mustOK(t, s.nsConsolidate(false))
	b3Inject(s, keys[len(keys)/2:], nil)
	b3Inject(s, keys[:2000], func(i int) bool { return i%3 == 0 })
	model := &b3Model{visible: map[string]string{}}
	for i, k := range keys {
		if i >= 2000 || i%3 != 0 {
			model.visible[k] = "x"
		}
	}
	if s.ns.objMut.n < 20000 {
		t.Fatalf("overlay holds %d rows", s.ns.objMut.n)
	}
	for _, c := range []struct{ prefix, delim string }{{"p/", "/"}, {"p/d4/", ""}, {"p/", ""}, {"p/d", "/"}} {
		want := model.units(c.prefix, c.delim)
		for _, max := range []int{1, 3, 1000} {
			if got := b3List(t, s, c.prefix, c.delim, max); !slices.Equal(got, want) {
				t.Fatalf("list(%q,%q,%d): %d units, want %d", c.prefix, c.delim, max, len(got), len(want))
			}
		}
	}
	// A 10-key page costs what the page needs, not the matching overlay range
	// (which is ~25,000 rows here, 600 KB as a copied slice).
	cost := b3PageAllocs(t, func() {
		if _, err := s.ListObjectsV2("b", "p/d4/", "", "", 10); err != nil {
			t.Fatal(err)
		}
	})
	groupCost := b3PageAllocs(t, func() {
		if _, err := s.ListObjectsV2("b", "p/", "/", "", 10); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("10-key page: %d B allocated; delimiter page over %d-member groups: %d B", cost, per, groupCost)
	if cost > 64<<10 || groupCost > 64<<10 {
		t.Fatalf("a small page allocated %d / %d bytes", cost, groupCost)
	}
	// Seeking over groups: the delimiter page decodes a few rows per group
	// rather than every member.
	s.ns.cache = newNSPageCache()
	s.ns.gen.cache = s.ns.cache
	before := s.ns.gen.reads.Load()
	page, err := s.ListObjectsV2("b", "p/", "/", "", 1000)
	mustOK(t, err)
	if len(page.commonPrefixes) != groups || s.ns.gen.reads.Load()-before > 3*groups {
		t.Fatalf("%d groups, %d table pages read", len(page.commonPrefixes), s.ns.gen.reads.Load()-before)
	}
}

// ---- B3-R: ordinary opens use the namespace checkpoint ----

func TestOrdinaryOpenUsesNamespaceCheckpoint(t *testing.T) {
	defer func(v int) { nsOverlayFloor = v }(nsOverlayFloor)
	nsOverlayFloor = 64
	dir := t.TempDir()
	path := filepath.Join(dir, "journal", nsGenName)
	open := func(shared bool) *Store {
		t.Helper()
		var s *Store
		var err error
		if shared {
			s, err = OpenStoreShared(dir)
		} else {
			s, err = OpenStore(dir)
		}
		mustOK(t, err)
		return s
	}
	journalOnly := func() string {
		t.Helper()
		t.Setenv("ZEROS3_NAMESPACE_CHECKPOINT", "off")
		s := open(true)
		defer s.Close()
		os.Unsetenv("ZEROS3_NAMESPACE_CHECKPOINT")
		if s.ns.gen != nil {
			t.Fatal("the opt-out loaded a checkpoint")
		}
		return nsDump(t, s)
	}

	// Fresh store, below the publication threshold: nothing is published and
	// the next open replays the journal.
	s := open(false)
	mustOK(t, s.CreateBucket("b"))
	for i := range 20 {
		mustPutObject(t, s, "b", fmt.Sprintf("a%03d", i), []byte("v"), "text/plain", nil)
	}
	mustOK(t, s.Close())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a small store published a checkpoint: %v", err)
	}
	s = open(false)
	if s.ns.gen != nil || s.ns.objMut.n != 20 {
		t.Fatalf("fresh reopen: gen %v, %d overlay rows", s.ns.gen != nil, s.ns.objMut.n)
	}
	// Server start publishes a replayed suffix past nsPublishFloor even below the
	// consolidation limit; a smaller one waits.
	s.nsKick()
	s.ns.wg.Wait()
	if s.ns.gen != nil {
		t.Fatal("a suffix below the publication floor was published")
	}
	func(v int) {
		defer func() { nsPublishFloor = v }()
		nsPublishFloor = 16
		s.nsKick()
		s.ns.wg.Wait()
	}(nsPublishFloor)
	if s.ns.gen == nil || !s.ns.gen.meta.Published {
		t.Fatal("server start did not publish the replayed suffix")
	}
	mustOK(t, os.Remove(path)) // the rest of the test starts without a checkpoint

	// Past the threshold an owner publishes in the background.
	for i := range 200 {
		mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i), []byte("v"), "text/plain", nil)
		if i%50 == 0 {
			mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i), []byte("w"), "text/plain", nil)
		}
	}
	s.ns.wg.Wait()
	mustOK(t, s.Close())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no checkpoint after %d changes: %v", 200, err)
	}
	want := journalOnly()

	// Checkpointed store: the ordinary open loads it and replays only the suffix.
	s = open(false)
	if s.ns.gen == nil || !s.ns.gen.meta.Published {
		t.Fatal("ordinary open did not load the published checkpoint")
	}
	if got := nsDump(t, s); got != want {
		t.Fatal("checkpoint open differs from the journal state")
	}
	suffix := s.ns.objMut.n + s.ns.histMut.n
	for i := range 10 {
		mustPutObject(t, s, "b", fmt.Sprintf("s%02d", i), []byte("v"), "text/plain", nil)
	}
	mustOK(t, s.DeleteObject("b", "a000"))
	mustOK(t, s.Close())
	want = journalOnly()

	// Journal with a suffix beyond the checkpoint: same state, the suffix in
	// the overlay. A shared open loads it too and never publishes.
	before, err := os.ReadFile(path)
	mustOK(t, err)
	s = open(true)
	if s.ns.gen == nil || s.ns.objMut.n+s.ns.histMut.n != suffix+12 {
		t.Fatalf("suffix open: gen %v, %d overlay rows, want %d", s.ns.gen != nil, s.ns.objMut.n+s.ns.histMut.n, suffix+12)
	}
	if got := nsDump(t, s); got != want {
		t.Fatal("suffix open differs from the journal state")
	}
	s.nsKick()
	s.ns.wg.Wait()
	mustOK(t, s.Close())
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a shared open published a checkpoint")
	}

	// The opt-out replays and keeps consolidations private.
	t.Setenv("ZEROS3_NAMESPACE_CHECKPOINT", "off")
	s = open(false)
	for i := range 100 {
		mustPutObject(t, s, "b", fmt.Sprintf("o%03d", i), []byte("v"), "text/plain", nil)
	}
	s.ns.wg.Wait()
	if s.ns.gen == nil || s.ns.gen.meta.Published {
		t.Fatal("expected a private consolidation under the opt-out")
	}
	mustOK(t, s.Close())
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("the opt-out published a checkpoint")
	}
	os.Unsetenv("ZEROS3_NAMESPACE_CHECKPOINT")

	// Back on the default path, the older checkpoint plus the longer suffix agree with replay.
	s = open(false)
	if got, want := nsDump(t, s), journalOnly(); got != want {
		t.Fatal("reopen after private consolidations differs")
	}
	mustOK(t, s.Close())
}

// ---- B3-R: online consolidation lifecycle ----

// The background publication of an owner is cut (process exit) at each
// boundary; every acknowledged write survives and the reopened namespace is
// the journal's, from whatever checkpoint the cut left.
func TestNamespaceOnlinePublicationCuts(t *testing.T) {
	if dir := os.Getenv("ZEROS3_TEST_NS_CUT_DIR"); dir != "" {
		nsOverlayFloor = 48
		cut := os.Getenv("ZEROS3_TEST_NS_CUT")
		testHook = func(point string) {
			if point == cut {
				os.Exit(93)
			}
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; ; i++ {
			k := fmt.Sprintf("k%04d", i%300)
			if _, err := s.PutObject("b", k, []byte(fmt.Sprintf("v%d", i)), "text/plain", nil); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("ack %s v%d\n", k, i)
			if i%7 == 0 {
				if err := s.DeleteObject("b", fmt.Sprintf("k%04d", (i+150)%300)); err != nil {
					t.Fatal(err)
				}
				fmt.Printf("del k%04d\n", (i+150)%300)
			}
			if i > 5000 {
				t.Fatal("the cut was never reached")
			}
		}
	}
	for _, cut := range []string{hookNamespaceBeforeFreeze, hookNamespaceFrozen, hookNamespaceCheckpointSynced,
		hookNamespaceCheckpointRenamed, hookNamespaceBeforeSwap, hookNamespaceSwapped} {
		t.Run(cut, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			mustOK(t, s.Close())
			cmd := exec.Command(os.Args[0], "-test.run=^TestNamespaceOnlinePublicationCuts$")
			cmd.Env = append(os.Environ(), "ZEROS3_TEST_NS_CUT_DIR="+dir, "ZEROS3_TEST_NS_CUT="+cut)
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 93 {
				t.Fatalf("cut %s not reached: %v", cut, err)
			}
			want := map[string]string{}
			for _, line := range strings.Split(string(out), "\n") {
				f := strings.Fields(line)
				switch {
				case len(f) == 3 && f[0] == "ack":
					want[f[1]] = f[2]
				case len(f) == 2 && f[0] == "del":
					delete(want, f[1])
				}
			}
			_, statErr := os.Stat(filepath.Join(dir, "journal", nsGenName))
			published := statErr == nil
			s, err = OpenStore(dir)
			mustOK(t, err)
			defer s.Close()
			if afterRename := cut == hookNamespaceCheckpointRenamed || cut == hookNamespaceBeforeSwap || cut == hookNamespaceSwapped; published != afterRename || (s.ns.gen != nil) != afterRename {
				t.Fatalf("cut %s: checkpoint on disk %v, loaded %v", cut, published, s.ns.gen != nil)
			}
			for k, v := range want {
				if _, got, err := s.GetObject("b", k); err != nil || string(got) != v {
					t.Fatalf("acknowledged %s=%s lost: %q %v", k, v, got, err)
				}
			}
			got := nsDump(t, s)
			t.Setenv("ZEROS3_NAMESPACE_CHECKPOINT", "off")
			r, err := OpenStoreShared(dir)
			mustOK(t, err)
			defer r.Close()
			if nsDump(t, r) != got {
				t.Fatal("checkpoint open differs from journal replay")
			}
		})
	}
}

// Close while a background consolidation runs: it does not hang, the build
// gives up and restores its overlay, and writes made meanwhile stay visible.
func TestNamespaceCloseDuringConsolidation(t *testing.T) {
	defer func(v int) { nsOverlayFloor = v }(nsOverlayFloor)
	nsOverlayFloor = 32
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	frozen, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	withTestHook(t, func(point string) {
		if point == hookNamespaceFrozen {
			once.Do(func() { close(frozen); <-release })
		}
	})
	acked := map[string]string{}
	for i := 0; ; i++ {
		k := fmt.Sprintf("k%03d", i)
		mustPutObject(t, s, "b", k, []byte(k), "text/plain", nil)
		acked[k] = k
		select {
		case <-frozen:
		default:
			continue
		}
		break
	}
	// Writes and reads proceed while the frozen overlay is being folded.
	for i := range 20 {
		k := fmt.Sprintf("during%02d", i)
		mustPutObject(t, s, "b", k, []byte(k), "text/plain", nil)
		acked[k] = k
	}
	mustOK(t, s.DeleteObject("b", "k000"))
	delete(acked, "k000")
	if _, got, err := s.GetObject("b", "k001"); err != nil || string(got) != "k001" {
		t.Fatalf("frozen row unreadable: %q %v", got, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case err := <-closed:
		mustOK(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Close hung behind the consolidation")
	}
	if s.ns.objFrozen != nil {
		t.Fatal("the frozen overlay was not restored")
	}
	if _, err := s.lookupObject("b", "k001"); !errors.Is(err, errStoreClosed) {
		t.Fatalf("lookup after Close: %v", err)
	}
	testHook = nil
	s, err = OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	if n := s.buckets["b"].count; n != len(acked) {
		t.Fatalf("%d visible after reopen, want %d", n, len(acked))
	}
	for k, v := range acked {
		if _, got, err := s.GetObject("b", k); err != nil || string(got) != v {
			t.Fatalf("%s = %q, %v", k, got, err)
		}
	}
}
