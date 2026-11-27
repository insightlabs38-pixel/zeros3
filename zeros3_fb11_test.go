package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fb11PageGolden(t testing.TB) []byte {
	t.Helper()
	text, err := os.ReadFile("testdata/fb11/snapshot-index.hex")
	if err != nil {
		t.Fatal(err)
	}
	body, err := hex.DecodeString(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFB11SnapshotPageIndexGolden(t *testing.T) {
	body := fb11PageGolden(t)
	i, err := decodeSnapshotPageIndex(body)
	mustOK(t, err)
	d, err := decodeSnapshotDescriptor(i.head)
	mustOK(t, err)
	if d.SourceBucket != "b" || d.Entries != nil || i.rows != 0 || i.root[31] != 31 ||
		i.legacy != sha256.Sum256(i.head) || !bytes.Equal(body, encodeSnapshotPageIndex(i)) {
		t.Fatal("snapshot index disagrees with independent golden")
	}
	for n := range len(body) {
		if _, err := decodeSnapshotPageIndex(body[:n]); err == nil {
			t.Fatalf("accepted truncated paged snapshot index at %d", n)
		}
	}
	for n := range len(body) {
		changed := bytes.Clone(body)
		changed[n] ^= 0x80
		if _, err := decodeSnapshotPageIndex(changed); err == nil {
			t.Fatalf("accepted damaged paged snapshot index byte %d", n)
		}
	}
}

func FuzzFB11SnapshotPageIndex(f *testing.F) {
	f.Add(fb11PageGolden(f))
	f.Add([]byte{})
	f.Add([]byte("ZSSP"))
	f.Add(encodeSnapshotPageIndex(snapshotPageIndex{}))
	f.Fuzz(func(t *testing.T, body []byte) {
		if i, err := decodeSnapshotPageIndex(body); err == nil && !bytes.Equal(body, encodeSnapshotPageIndex(i)) {
			t.Fatal("accepted an unbounded or noncanonical snapshot index")
		}
	})
}

func TestFB11SnapshotPagesAndPortableCompatibility(t *testing.T) {
	root := t.TempDir()
	s, err := OpenStore(root)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	for i := range 360 {
		_, err := s.PutObject("b", fmt.Sprintf("prefix/%04d", i), []byte("shared body"), "text/plain", nil)
		mustOK(t, err)
	}
	capture := func(s *Store) snapshotDescriptorV1 {
		entries, err := s.captureSnapshotEntries("b", "prefix")
		mustOK(t, err)
		return snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
			SourceBucket: "b", SourcePrefix: "prefix", CreatedAt: time.Now().UTC(), Entries: entries}
	}
	legacy := capture(s)
	mustOK(t, s.publishSnapshot(legacy))
	mustOK(t, s.Close())
	secret := sha256.Sum256([]byte("FB11 mixed-store fixture"))
	mustOK(t, os.WriteFile(filepath.Join(root, "journal", "metadata-page-key"), secret[:], 0600))
	s, err = openStoreWithFeatures(root, true, true, false, false, false, storeResearch{snapshotPages: true})
	mustOK(t, err)
	first := capture(s)
	mustOK(t, s.publishSnapshot(first))
	dir := filepath.Join(root, snapshotPagesDir)
	placed, err := os.ReadDir(dir)
	mustOK(t, err)
	_, err = s.PutObject("b", "prefix/0123", []byte("modified body"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.DeleteObject("b", "prefix/0149"))
	_, err = s.PutObject("b", "prefix/0123a", []byte("inserted body"), "application/octet-stream", nil)
	mustOK(t, err)
	second := capture(s)
	mustOK(t, s.publishSnapshot(second))
	after, err := os.ReadDir(dir)
	mustOK(t, err)
	if len(placed) < 4 || len(after) <= len(placed) || len(after) >= 2*len(placed) {
		t.Fatalf("unexpected shared closure: started %d pages, after %d unique files", len(placed), len(after))
	}
	for _, want := range []snapshotDescriptorV1{legacy, first, second} {
		d, raw, err := s.readSnapshotFrame(want.SnapshotID)
		mustOK(t, err)
		expected, err := encodeSnapshotDescriptor(want)
		mustOK(t, err)
		if !bytes.Equal(raw, expected) || !reflect.DeepEqual(d, want) {
			t.Fatal("physical page mode changed portable bytes or metadata")
		}
	}
	checked, err := s.Verify(true)
	mustOK(t, err)
	if !checked.OK() {
		t.Fatalf("mixed snapshot roots lost GC reachability: %+v", checked)
	}
	var bundle bytes.Buffer
	_, err = s.exportBundle(second.SnapshotID, &bundle, false)
	mustOK(t, err)
	// It must be possible to import the portable frame into both an
	// existing paged snapshot and a fresh engine using legacy snapshots.
	imported, err := s.importBundle(bytes.NewReader(bundle.Bytes()))
	mustOK(t, err)
	if !imported.SnapshotReused {
		t.Fatal("portable reimport did not reuse the equal paged snapshot")
	}
	destination := t.TempDir()
	d, err := OpenStore(destination)
	mustOK(t, err)
	_, err = d.importBundle(bytes.NewReader(bundle.Bytes()))
	mustOK(t, err)
	clone, _, err := d.readSnapshotFrame(second.SnapshotID)
	mustOK(t, err)
	if !reflect.DeepEqual(clone, second) {
		t.Fatal("portable bundle changed target descriptor")
	}
	mustOK(t, d.Close())
	if old := os.Getenv("ZEROS3_TEST_PRE_FB11"); old != "" {
		if output, err := exec.Command(old, "verify", "-store", root).CombinedOutput(); err == nil ||
			!strings.Contains(string(output), snapshotPagesFeature) {
			t.Fatalf("pre-feature reader did not refuse snapshot pages: %v\n%s", err, output)
		}
		if output, err := exec.Command(old, "verify", "-deep", "-store", destination).CombinedOutput(); err != nil {
			t.Fatalf("pre-feature reader could not verify the portable legacy export: %v\n%s", err, output)
		}
	}
	typed, err := os.ReadFile(s.snapshotPath(second.SnapshotID))
	mustOK(t, err)
	ix, err := decodeSnapshotPageIndex(typed)
	mustOK(t, err)
	_, targetPages, err := walkMetadataTree(ix.root, func(sum [32]byte) ([]byte, error) {
		return readBoundedFile(filepath.Join(dir, hex.EncodeToString(sum[:])), metaPageLimit)
	}, func([]byte, *objectEntry) error { return nil })
	mustOK(t, err)
	shared := targetPages - (len(after) - len(placed))
	if shared <= 0 {
		t.Fatal("second snapshot did not retain any exact original page")
	}
	page := filepath.Join(dir, hex.EncodeToString(ix.root[:]))
	fb07FlipPackByte(t, page, metaPageHeader)
	if _, err := s.readSnapshot(second.SnapshotID); err == nil {
		t.Fatal("accepted a corrupted shared page")
	}
	issues, err := s.Verify(false)
	mustOK(t, err)
	if issues.OK() {
		t.Fatal("exact mark ignored the compromised snapshot closure")
	}
	mustOK(t, s.Close())
	if report, err := gcCollect(root, true); err == nil || report.LiveSetOK {
		t.Fatalf("destructive GC accepted a damaged snapshot root: %v %+v", err, report)
	}
	if report, err := sweepSnapshotPages(root, true); err == nil || report.Removed != 0 {
		t.Fatalf("visible page reclamation accepted a damaged snapshot root: %v %+v", err, report)
	}
	t.Logf("reused %d of %d original pages and preserved exact portable bytes; corrupted page blocked GC", shared, len(placed))
}

func TestFB11SnapshotPagesPhysicalScale(t *testing.T) {
	if os.Getenv("ZEROS3_TEST_SNAPSHOT_PAGES_SCALE") == "" {
		t.Skip("set ZEROS3_TEST_SNAPSHOT_PAGES_SCALE=1 for generated one-million-entry metadata")
	}
	root := t.TempDir()
	s, err := openStoreWithFeatures(root, true, true, false, false, false, storeResearch{snapshotPages: true})
	mustOK(t, err)
	defer s.Close()
	// Deterministic seed for the scale test. Live activation uses a private
	// random secret and never exports it with a snapshot or bundle.
	seed := sha256.Sum256([]byte("FB11 physical scale"))
	mustOK(t, os.WriteFile(filepath.Join(root, "journal", "metadata-page-key"), seed[:], 0600))
	const count = 1_000_000
	entries := make([]snapshotEntryV1, count)
	for n := range entries {
		key := fmt.Sprintf("prefix/%010d", n)
		digest := sha256.Sum256([]byte(key))
		entries[n] = snapshotEntryV1{Key: key, ManifestUUID: "019ab000-0000-7000-8000-000000000001",
			ManifestSHA256: hex.EncodeToString(digest[:]), Size: 65536, ETag: "12345678", ContentType: "text/plain"}
	}
	snapshot := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		SourceBucket: "b", SourcePrefix: "prefix", CreatedAt: time.Now().UTC(), Entries: entries}
	fileBytes := func() (int, int64, int64) {
		children, err := os.ReadDir(filepath.Join(root, snapshotPagesDir))
		mustOK(t, err)
		var size, allocated int64
		for _, file := range children {
			fi, err := file.Info()
			mustOK(t, err)
			size += fi.Size()
			allocated += fi.Sys().(*syscall.Stat_t).Blocks * 512
		}
		return len(children), size, allocated
	}
	flat, err := encodeSnapshotDescriptor(snapshot)
	mustOK(t, err)
	flatBytes := int64(len(flat))
	mustOK(t, os.WriteFile(filepath.Join(root, "plain-snapshot"), flat, 0600))
	plain, err := os.Stat(filepath.Join(root, "plain-snapshot"))
	mustOK(t, err)
	flatAllocation := plain.Sys().(*syscall.Stat_t).Blocks * 512
	flat = nil
	begin := time.Now()
	mustOK(t, s.publishSnapshot(snapshot))
	initial := time.Since(begin)
	pages, pageBytes, pageAlloc := fileBytes()
	for i := 0; i < count; i += 1000 {
		entries[i].Size++
	}
	snapshot.SnapshotID = newUUIDv7()
	begin = time.Now()
	mustOK(t, s.publishSnapshot(snapshot))
	increment := time.Since(begin)
	endPages, endBytes, endAlloc := fileBytes()
	frame, err := os.Stat(s.snapshotPath(snapshot.SnapshotID))
	mustOK(t, err)
	diffBytes := endBytes - pageBytes + frame.Size()
	diffAlloc := endAlloc - pageAlloc + frame.Sys().(*syscall.Stat_t).Blocks*512
	if diffBytes > flatBytes/10 || diffAlloc > flatAllocation/10 {
		t.Fatalf("physical 0.1%% churn exceeds 10%% of flat copy: logical=%d/%d allocated=%d/%d",
			diffBytes, flatBytes, diffAlloc, flatAllocation)
	}
	t.Logf("rows=%d flat=%d flat_allocated=%d initial_pages=%d initial_bytes=%d initial_allocated=%d changed=%d new_pages=%d incremental=%d incremental_allocated=%d first=%s second=%s",
		count, flatBytes, flatAllocation, pages, pageBytes, pageAlloc, count/1000, endPages-pages, diffBytes, diffAlloc, initial, increment)
}

func TestFB11SnapshotPageSweepRandomized(t *testing.T) {
	root := t.TempDir()
	s, err := openStoreWithFeatures(root, true, true, false, false, false, storeResearch{snapshotPages: true})
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	seed := sha256.Sum256([]byte("FB11 page sweep fixture"))
	mustOK(t, os.WriteFile(filepath.Join(root, "journal", "metadata-page-key"), seed[:], 0600))
	rng := rand.New(rand.NewSource(71291))
	model := map[string][32]byte{}
	retained := make(map[string]map[string][32]byte)
	var ids []string
	for round := range 12 {
		for update := range 36 {
			key := fmt.Sprintf("folder/%03d", rng.Intn(280))
			if rng.Intn(8) == 0 {
				mustOK(t, s.DeleteObject("b", key))
				delete(model, key)
				continue
			}
			body := []byte(fmt.Sprintf("round=%d operation=%d", round, update))
			_, err := s.PutObject("b", key, body, "text/plain", nil)
			mustOK(t, err)
			model[key] = sha256.Sum256(body)
		}
		entries, err := s.captureSnapshotEntries("b", "folder")
		mustOK(t, err)
		id := newUUIDv7()
		mustOK(t, s.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion,
			SnapshotID: id, CreatedAt: time.Now().UTC(), SourceBucket: "b", SourcePrefix: "folder", Entries: entries}))
		retained[id] = maps.Clone(model)
		ids = append(ids, id)
	}
	for i, id := range ids {
		if i%3 == 0 {
			mustOK(t, s.deleteSnapshot(id))
			delete(retained, id)
		}
	}
	if _, err := sweepSnapshotPages(root, false); err == nil {
		t.Fatal("offline sweep accepted an active writer")
	}
	mustOK(t, s.Close())
	placeholder := []byte("unpublished page at a process cut")
	sum := sha256.Sum256(placeholder)
	orphan := filepath.Join(root, snapshotPagesDir, hex.EncodeToString(sum[:]))
	mustOK(t, os.WriteFile(orphan, placeholder, 0600))
	dry, err := sweepSnapshotPages(root, false)
	mustOK(t, err)
	if dry.Candidates == 0 || dry.Removed != 0 {
		t.Fatalf("unexpected exact dry-run: %+v", dry)
	}
	_, err = os.Stat(orphan)
	mustOK(t, err)
	applied, err := sweepSnapshotPages(root, true)
	mustOK(t, err)
	if applied.Candidates != dry.Candidates || applied.Removed != dry.Candidates || applied.Live != dry.Live {
		t.Fatalf("applied sweep differs from dry-run: %+v then %+v", dry, applied)
	}
	last, err := sweepSnapshotPages(root, true)
	mustOK(t, err)
	if last.Removed != 0 || last.Live != dry.Live {
		t.Fatal("exact sweep did not converge")
	}
	s, err = OpenStoreShared(root)
	mustOK(t, err)
	defer s.Close()
	wantRoots := 0
	for id, want := range retained {
		d, err := s.readSnapshot(id)
		mustOK(t, err)
		if len(d.Entries) != len(want) {
			t.Fatal("sweep changed the modeled root set")
		}
		wantRoots += len(want)
		for _, e := range d.Entries {
			digest, err := decodeHexSHA256(e.ManifestSHA256)
			mustOK(t, err)
			m, err := s.readVerifiedManifest(e.ManifestUUID, digest)
			mustOK(t, err)
			sha, ok := want[e.Key]
			if !ok || m.ObjectSHA256 != hex.EncodeToString(sha[:]) {
				t.Fatal("sweep changed a randomized snapshot's object identity")
			}
		}
	}
	report, err := s.Verify(true)
	mustOK(t, err)
	if !report.OK() || report.SnapshotRootCount != wantRoots {
		t.Fatalf("deep reachability disagrees with exact snapshot histories: %+v", report)
	}
	t.Logf("12 randomized snapshots, 432 mutations, %d exact retained roots; swept %d orphan pages, kept %d", wantRoots, applied.Removed, applied.Live)
}
