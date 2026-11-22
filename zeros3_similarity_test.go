package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type similarityFixturePosting struct {
	feature uint64
	id      uint32
}

type similarityReadCounter struct {
	*bytes.Reader
	reads int
}

func (r *similarityReadCounter) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	return r.Reader.ReadAt(p, off)
}

// Encode the documented research layout independently of the production reader.
func similarityFixtureRun(entries []similarityFixturePosting) []byte {
	const header, directory, entry = 20, 65537 * 4, 10
	run := make([]byte, header+directory+entry*len(entries)+4)
	copy(run, "ZSR1")
	binary.LittleEndian.PutUint64(run[4:], 11)
	binary.LittleEndian.PutUint64(run[12:], uint64(len(entries)))
	i := 0
	for bucket := 0; bucket <= 65536; bucket++ {
		binary.LittleEndian.PutUint32(run[header+bucket*4:], uint32(i))
		for i < len(entries) && int(entries[i].feature>>48) == bucket {
			off := header + directory + entry*i
			for b := 0; b < 6; b++ {
				run[off+b] = byte(entries[i].feature >> (8 * b))
			}
			binary.LittleEndian.PutUint32(run[off+6:], entries[i].id)
			i++
		}
	}
	sealSimilarityFixture(run)
	return run
}

func sealSimilarityFixture(run []byte) {
	crc := crc32.Checksum(run[:len(run)-4], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(run[len(run)-4:], crc)
}

func TestAuthoritySimilarityRunLookup(t *testing.T) {
	const hot = 0x1234000000000004
	entries := []similarityFixturePosting{{2, 1}, {hot - 1, 201}}
	for i := uint32(1); i <= 1000; i++ {
		entries = append(entries, similarityFixturePosting{hot, i})
	}
	entries = append(entries, similarityFixturePosting{hot + 1, 202})
	const medium = 0x5678000000000001
	for id := uint32(1); id <= 88; id++ {
		entries = append(entries, similarityFixturePosting{medium, id})
	}
	entries = append(entries, similarityFixturePosting{^uint64(0), 999})
	body := similarityFixtureRun(entries)
	source := &similarityReadCounter{Reader: bytes.NewReader(body)}
	r, err := openAuthSimilarityRun(source, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if r.generation != 11 || r.count != uint32(len(entries)) || r.digest != sha256.Sum256(body) {
		t.Fatalf("wrong metadata: %d %d %x", r.generation, r.count, r.digest)
	}
	for _, tc := range []struct {
		feature uint64
		count   int
		want    []uint32
	}{
		{0, 64, nil}, {2, 1, []uint32{1}}, {hot, 3, []uint32{998, 999, 1000}},
		{hot + 1, 64, []uint32{202}}, {hot + 2, 64, nil}, {^uint64(0), 64, []uint32{999}},
	} {
		got, err := r.lookup(tc.feature, tc.count)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("lookup %x: got %v, want %v, err=%v", tc.feature, got, tc.want, err)
		}
	}
	source.reads = 0
	if got, err := r.lookup(hot, 5000); err != nil || len(got) != 64 || got[0] != 937 || source.reads > 11 {
		t.Fatalf("hot feature was not bounded: IDs %v, reads %d, error %v", got, source.reads, err)
	}
	source.reads = 0
	if got, err := r.lookup(medium, 3); err != nil || !slices.Equal(got, []uint32{86, 87, 88}) || source.reads != 1 {
		t.Fatalf("medium bucket: IDs %v, reads %d, error %v", got, source.reads, err)
	}
	f := filepath.Join(t.TempDir(), "research.zsr")
	if err := os.WriteFile(f, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"-run", f, "-sha256", fmt.Sprintf("%x", r.digest), "-feature", fmt.Sprint(hot)}
	if err := inspectAuthSimilarityRun(args, &out); err != nil {
		t.Fatal(err)
	}
	var report struct{ IDs []uint32 }
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.IDs) != 64 {
		t.Fatalf("report did not include bounded IDs: %v %v", report.IDs, err)
	}
	args[3] = fmt.Sprintf("%064x", 5)
	if err := inspectAuthSimilarityRun(args, &out); err == nil {
		t.Fatal("mismatched digest accepted")
	}
}

func TestAuthoritySimilarityHotPostingsAcrossRuns(t *testing.T) {
	const hot uint64 = 0x1234000000000004
	run := func(first, last uint32) *authSimilarityRun {
		t.Helper()
		var posts []similarityFixturePosting
		for id := first; id <= last; id++ {
			posts = append(posts, similarityFixturePosting{hot, id})
		}
		raw := similarityFixtureRun(posts)
		opened, err := openAuthSimilarityRun(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		return opened
	}
	old, middle, recent := run(1, 20), run(21, 80), run(81, 100)
	middle.directory = nil // Verified older runs retain only their file and metadata.
	q := authSimilarityQuery{
		report: authSimilarityStageReport{retired: []uint32{37, 99}},
		runs:   []*authSimilarityRun{old, middle, recent},
	}
	var want []uint32
	for id := uint32(35); id <= 100; id++ {
		if !slices.Contains(q.report.retired, id) {
			want = append(want, id)
		}
	}
	got, err := q.postings(hot)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("global hot feature candidates differ: got %v want %v error %v", got, want, err)
	}
	q.runs = []*authSimilarityRun{run(1, 100)}
	got, err = q.postings(hot)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("compacted hot feature candidates differ: got %v want %v error %v", got, want, err)
	}

	q.report.retired = nil
	for id := uint32(73); id <= 200; id++ {
		q.report.retired = append(q.report.retired, id)
	}
	for _, runs := range [][]*authSimilarityRun{{run(1, 100), run(101, 200)}, {run(1, 200)}} {
		q.runs = runs
		if got, err := q.postings(hot); err != nil || len(got) != 0 {
			t.Fatalf("hot feature exceeded its scan budget with %d runs: %v %v", len(runs), got, err)
		}
	}
}

func TestAuthoritySimilarityRunCorruption(t *testing.T) {
	fresh := similarityFixtureRun([]similarityFixturePosting{{2, 1}, {2, 3}, {3, 1}})
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"magic", func(b []byte) []byte { b[3] = '2'; sealSimilarityFixture(b); return b }},
		{"zero generation", func(b []byte) []byte { clear(b[4:12]); sealSimilarityFixture(b); return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
		{"first directory", func(b []byte) []byte { b[20] = 1; sealSimilarityFixture(b); return b }},
		{"duplicate posting", func(b []byte) []byte { b[authSimilarityRunBase+16] = 1; sealSimilarityFixture(b); return b }},
		{"zero ID", func(b []byte) []byte { b[authSimilarityRunBase+6] = 0; sealSimilarityFixture(b); return b }},
		{"checksum", func(b []byte) []byte { b[len(b)-1] ^= 1; return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.edit(slices.Clone(fresh))
			if _, err := openAuthSimilarityRun(bytes.NewReader(b), int64(len(b))); err == nil {
				t.Fatal("invalid run accepted")
			}
		})
	}
}

func TestAuthoritySimilarityStage(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("retained object"), 200)
	b := bytes.Repeat([]byte("updated object"), 201)
	mustPutObject(t, s, "similar", "a", a, "", nil)
	mustPutObject(t, s, "similar", "b", a, "", nil)
	mustPutObject(t, s, "similar", "a", b, "", nil)
	first, err := s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if first.Objects != 2 || len(first.Runs) != 1 || first.JournalSeq == 0 || first.JournalEnd <= 0 ||
		first.StoreID != s.formatSnapshot().StoreID {
		t.Fatalf("invalid stage metadata: %+v", first)
	}
	blob, err := os.ReadFile(filepath.Join(first.Directory, "catalog.zsc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 12+44*2 || string(blob[:4]) != "ZSC1" || first.CatalogSHA != fmt.Sprintf("%x", sha256.Sum256(blob)) {
		t.Fatal("catalog metadata differs from its bytes")
	}
	expected := map[[32]byte]bool{sha256.Sum256(a): false, sha256.Sum256(b): false}
	for i := 0; i < 2; i++ {
		row := blob[12+44*i : 12+44*(i+1)]
		if binary.LittleEndian.Uint32(row) != uint32(i+1) {
			t.Fatal("nonmonotonic catalog IDs")
		}
		var sum [32]byte
		copy(sum[:], row[4:36])
		if seen, ok := expected[sum]; !ok || seen {
			t.Fatalf("unexpected or repeated catalog digest %x", sum)
		}
		expected[sum] = true
	}
	runPath := filepath.Join(first.Directory, first.Runs[0].Name)
	runData, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Runs[0].SHA256 != fmt.Sprintf("%x", sha256.Sum256(runData)) {
		t.Fatal("wrong run digest")
	}
	second, err := s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if first.Directory == second.Directory || first.JournalSHA != second.JournalSHA || first.SnapshotSHA != second.SnapshotSHA {
		t.Fatal("rebuild changed authoritative fencing or reused a directory")
	}
	if _, err := os.Stat(runPath); err != nil {
		t.Fatalf("second build removed the first run: %v", err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(first.Directory)); err != nil {
		t.Fatal(err)
	}
	t.Run("interrupted-publish", func(t *testing.T) {
		withTestHook(t, func(point string) {
			if point == "similarity-before-current-publish" {
				panic(simulatedCrash{point: point})
			}
		})
		runExpectingSimulatedCrash(t, func() {
			if err := s.publishAuthSimilarityStage(filepath.Base(second.Directory)); err != nil {
				t.Fatal(err)
			}
		})
	})
	current, err := s.readAuthSimilarityCurrent()
	if err != nil || current.Directory != first.Directory {
		t.Fatalf("interruption replaced the previous pointer: %+v %v", current, err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(second.Directory)); err != nil {
		t.Fatal(err)
	}
	current, err = s.readAuthSimilarityCurrent()
	if err != nil || current.Directory != second.Directory {
		t.Fatalf("complete publication did not advance the pointer: %+v %v", current, err)
	}
	mustPutObject(t, s, "similar", "new", a, "", nil)
	if _, err := s.readAuthSimilarityCurrent(); err == nil {
		t.Fatal("active generation was accepted after durable journal change")
	}
	_, body, err := s.GetObject("similar", "a")
	if err != nil || !bytes.Equal(body, b) {
		t.Fatal("staged index changed visible bytes")
	}
}

func TestAuthoritySimilarityRefresh(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("first indexed body"), 170)
	b := bytes.Repeat([]byte("second indexed body"), 171)
	mustPutObject(t, s, "similar", "a", a, "", nil)
	original, err := s.refreshAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(original.Directory)); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, s, "similar", "a", b, "", nil)
	mustPutObject(t, s, "similar", "alias", a, "", nil)
	next, err := s.refreshAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != original.Generation || next.Objects != 2 || len(next.Runs) != 2 || len(original.Runs) != 1 ||
		next.Runs[0] != original.Runs[0] || next.JournalSeq <= original.JournalSeq {
		t.Fatalf("refresh did not append exactly one distinct body: original=%+v next=%+v", original, next)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(next.Directory)); err != nil {
		t.Fatal(err)
	}
	// A small suffix can reuse one known digest without retaining every
	// prior identity or dropping any rows from the staged catalog.
	var copied bytes.Buffer
	var retired []uint32
	digest := sha256.Sum256(b)
	remembered, only := newAuthDigestTable(4), newAuthDigestTable(4)
	only.put(digest, 0)
	if err := reuseAuthSimilarityCatalog(next, t.TempDir(), &copied, remembered, only, false, &retired); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(next.Directory, "catalog.zsc"))
	if err != nil || len(catalog) < 12 || !bytes.Equal(copied.Bytes(), catalog[12:]) || len(retired) != 0 ||
		len(remembered.keys) != 1 || remembered.vals[0] != int64(len(b)) || remembered.keys[0] != digest {
		t.Fatalf("filtered catalog reuse changed rows or retained unrelated identities: %v, %v", remembered.keys, err)
	}
	mustPutObject(t, s, "similar", "reused", b, "", nil)
	reused, err := s.refreshAuthSimilarityStage()
	if err != nil || reused.Generation != next.Generation || reused.Objects != next.Objects || !slices.Equal(reused.Runs, next.Runs) {
		t.Fatalf("an incremental alias changed catalog identity: %+v %v", reused, err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(reused.Directory)); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.refreshAuthSimilarityStage()
	if err != nil || unchanged.Directory != reused.Directory {
		t.Fatalf("refresh of a current generation did unnecessary work: %+v %v", unchanged, err)
	}
	if err := s.DeleteObject("similar", "alias"); err != nil {
		t.Fatal(err)
	}
	retained, err := s.refreshAuthSimilarityStage()
	if err != nil || retained.Objects != 2 || retained.Generation != original.Generation {
		t.Fatalf("retained version lost its postings: %+v %v", retained, err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(retained.Directory)); err != nil {
		t.Fatal(err)
	}
	keep := 0
	prune, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "similar", KeepLast: &keep})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyHistoryPrune(&prune); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.refreshAuthSimilarityStage()
	if err != nil || rebuilt.LiveObjects != 1 || rebuilt.Objects != 2 || rebuilt.Tombstones != 1 ||
		rebuilt.Generation != original.Generation || !slices.Equal(rebuilt.retired, []uint32{1}) {
		t.Fatalf("pruned content was not tombstoned: %+v %v", rebuilt, err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(rebuilt.Directory)); err != nil {
		t.Fatal(err)
	}
	active, err := s.readAuthSimilarityCurrent()
	if err != nil {
		t.Fatal(err)
	}
	ids, err := similarityPostingIDs(active, authFeatures(a, 16)[0])
	if err != nil || slices.Contains(ids, uint32(1)) {
		t.Fatalf("retired ID appeared in diagnostic lookup: %v %v", ids, err)
	}
	tombstones := filepath.Join(rebuilt.Directory, "tombstones.bin")
	if err := os.Rename(tombstones, tombstones+".backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAuthSimilarityCurrent(); err == nil {
		t.Fatal("missing tombstones did not invalidate the active generation")
	}
	if err := os.Rename(tombstones+".backup", tombstones); err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(rebuilt.Directory, rebuilt.Runs[0].Name)
	runData, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatal(err)
	}
	runData[len(runData)-1] ^= 1
	if err := os.WriteFile(runPath, runData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAuthSimilarityCurrent(); err == nil {
		t.Fatal("corrupt inherited run was accepted")
	}
	if q, err := s.openCurrentAuthSimilarityQuery(); err == nil {
		q.close()
		t.Fatal("corrupt inherited run was opened for migration queries")
	}
	repaired, err := s.refreshAuthSimilarityStage()
	if err != nil || repaired.Objects != 1 || repaired.Generation == rebuilt.Generation {
		t.Fatalf("failed to rebuild damaged derived state: %+v %v", repaired, err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(repaired.Directory)); err != nil {
		t.Fatal(err)
	}
	entries, err := s.captureSnapshotEntries("similar", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		CreatedAt: time.Now().UTC(), SourceBucket: "similar", Entries: entries}
	if err := s.publishSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAuthSimilarityCurrent(); err == nil {
		t.Fatal("snapshot-only change did not invalidate the index fence")
	}
	withSnapshot, err := s.refreshAuthSimilarityStage()
	if err != nil || withSnapshot.JournalSHA != repaired.JournalSHA || withSnapshot.SnapshotSHA == repaired.SnapshotSHA ||
		withSnapshot.Generation == repaired.Generation {
		t.Fatalf("snapshot-only refresh reused stale state: %+v %v", withSnapshot, err)
	}
	_, got, err := s.GetObject("similar", "a")
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("authoritative body differs after refresh and rebuild: %v", err)
	}
}

func TestAuthoritySimilarityMultipartAndCopy(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	refresh := func(want uint32) authSimilarityStageReport {
		t.Helper()
		report, err := s.refreshAuthSimilarityStage()
		if err != nil {
			t.Fatal(err)
		}
		if report.LiveObjects != want || report.Objects != want {
			t.Fatalf("catalog has %d live of %d total, want %d", report.LiveObjects, report.Objects, want)
		}
		if err := s.publishAuthSimilarityStage(filepath.Base(report.Directory)); err != nil {
			t.Fatal(err)
		}
		return report
	}
	upload := func(key string, body []byte) (string, string) {
		t.Helper()
		id, err := s.CreateMultipartUpload("similar", key, "application/octet-stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		etag, err := s.UploadPart("similar", key, id, 1, body)
		if err != nil {
			t.Fatal(err)
		}
		return id, etag
	}
	first := refresh(0)
	body := bytes.Repeat([]byte("complete multipart index identity"), 140)
	id, _ := upload("aborted", bytes.Repeat([]byte("unpublished multipart part"), 100))
	if report := refresh(0); len(report.Runs) != 0 || report.JournalSeq <= first.JournalSeq {
		t.Fatal("index did not advance past staged multipart without adding postings")
	}
	if err := s.AbortMultipartUpload("similar", "aborted", id); err != nil {
		t.Fatal(err)
	}
	id, etag := upload("complete", body)
	refresh(0)
	if _, _, err := s.CompleteMultipartUpload("similar", "complete", id, []completedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	complete := refresh(1)
	if complete.Generation != first.Generation || len(complete.Runs) != 1 {
		t.Fatal("multipart completion did not incrementally add the verified object")
	}
	if _, _, err := s.CopyObject(CopyObjectRequest{
		SrcBucket: "similar", SrcKey: "complete", DstBucket: "similar", DstKey: "copy",
		Directive: metadataDirectiveCopy,
	}); err != nil {
		t.Fatal(err)
	}
	copied := refresh(1)
	if copied.CatalogSHA != complete.CatalogSHA || !slices.Equal(copied.Runs, complete.Runs) {
		t.Fatal("alias copy changed catalog identities or duplicated postings")
	}
	_, got, err := s.GetObject("similar", "copy")
	if err != nil || sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("multipart copy changed logical bytes: %v", err)
	}
}

func TestAuthoritySimilaritySnapshotPinsAndReactivation(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("snapshot-retained index body"), 512)
	mustPutObject(t, s, "similar", "a", a, "", nil)
	entries, err := s.captureSnapshotEntries("similar", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		CreatedAt: time.Now().UTC(), SourceBucket: "similar", Entries: entries}
	if err := s.publishSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	refresh := func() authSimilarityStageReport {
		t.Helper()
		report, err := s.refreshAuthSimilarityStage()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.publishAuthSimilarityStage(filepath.Base(report.Directory)); err != nil {
			t.Fatal(err)
		}
		return report
	}
	prune := func(key string) {
		t.Helper()
		if err := s.DeleteObject("similar", key); err != nil {
			t.Fatal(err)
		}
		keep := 0
		plan, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "similar", KeepLast: &keep})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyHistoryPrune(&plan); err != nil {
			t.Fatal(err)
		}
	}
	initial := refresh()
	prune("a")
	pinned := refresh()
	if pinned.Generation != initial.Generation || pinned.LiveObjects != 1 || pinned.Tombstones != 0 {
		t.Fatalf("prune lost a snapshot-pinned identity: %+v", pinned)
	}
	if err := s.deleteSnapshot(snapshot.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if empty := refresh(); empty.LiveObjects != 0 || empty.Generation == pinned.Generation {
		t.Fatalf("snapshot deletion did not rebuild an empty index: %+v", empty)
	}
	mustPutObject(t, s, "similar", "a", a, "", nil)
	initial = refresh()
	prune("a")
	retired := refresh()
	if retired.LiveObjects != 0 || retired.Objects != 1 || retired.Tombstones != 1 {
		t.Fatalf("prune failed to retire the identity: %+v", retired)
	}
	mustPutObject(t, s, "similar", "alias", a, "", nil)
	restored := refresh()
	if restored.Generation != initial.Generation || restored.Objects != 1 || restored.LiveObjects != 1 ||
		restored.Tombstones != 0 || !slices.Equal(restored.Runs, initial.Runs) {
		t.Fatalf("returning content did not reactivate its original ID and postings: %+v", restored)
	}
	active, err := s.readAuthSimilarityCurrent()
	if err != nil {
		t.Fatal(err)
	}
	ids, err := similarityPostingIDs(active, authFeatures(a, 16)[0])
	if err != nil || !slices.Equal(ids, []uint32{1}) {
		t.Fatalf("lookup did not find the reactivated ID: %v %v", ids, err)
	}
}

func TestAuthoritySimilarityPublicationRecovery(t *testing.T) {
	if point := os.Getenv("ZEROS3_SIMILARITY_KILL_POINT"); point != "" {
		dir, name := os.Getenv("ZEROS3_SIMILARITY_STORE"), os.Getenv("ZEROS3_SIMILARITY_GENERATION")
		lock, err := acquireStoreLock(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.release()
		s, err := OpenStoreShared(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		withTestHook(t, func(p string) {
			if p == point {
				if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
		})
		switch point {
		case "similarity-after-prune-sync":
			if _, err := s.pruneAuthSimilarity(strings.Split(name, ","), true); err != nil {
				t.Fatal(err)
			}
		case "similarity-before-generation-sync", "similarity-before-generation-rename",
			"similarity-after-generation-rename", "similarity-after-generation-sync":
			if _, err := s.refreshAuthSimilarityStage(); err != nil {
				t.Fatal(err)
			}
		default:
			if err := s.publishAuthSimilarityStage(name); err != nil {
				t.Fatal(err)
			}
		}
		t.Fatal("publication kill point not reached")
	}
	for _, point := range []string{
		"similarity-before-generation-sync", "similarity-before-generation-rename",
		"similarity-after-generation-rename", "similarity-after-generation-sync",
		"similarity-before-current-publish", "similarity-after-current-publish", "similarity-after-current-sync",
	} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateBucket("similar"); err != nil {
				t.Fatal(err)
			}
			base := bytes.Repeat([]byte("stable base"), 200)
			want := bytes.Repeat([]byte("durable update"), 220)
			mustPutObject(t, s, "similar", "a", base, "", nil)
			first, err := s.buildAuthSimilarityStage()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.publishAuthSimilarityStage(filepath.Base(first.Directory)); err != nil {
				t.Fatal(err)
			}
			mustPutObject(t, s, "similar", "a", want, "", nil)
			next, err := s.refreshAuthSimilarityStage()
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			cmd := exec.Command(os.Args[0], "-test.run", "^TestAuthoritySimilarityPublicationRecovery$")
			cmd.Env = append(os.Environ(), "ZEROS3_SIMILARITY_KILL_POINT="+point,
				"ZEROS3_SIMILARITY_STORE="+dir, "ZEROS3_SIMILARITY_GENERATION="+filepath.Base(next.Directory))
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("child did not die at %s: %v %s", point, err, output)
			}
			s, err = OpenStoreShared(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, body, err := s.GetObject("similar", "a")
			if err != nil || !bytes.Equal(body, want) {
				t.Fatalf("authoritative replay differs after interruption: %v", err)
			}
			currentName, err := s.authSimilarityCurrentName()
			if err != nil {
				t.Fatalf("interrupted pointer is unreadable: %v", err)
			}
			if currentName == filepath.Base(next.Directory) {
				if _, err := s.readAuthSimilarityCurrent(); err != nil {
					t.Fatalf("new pointer references incomplete data: %v", err)
				}
			} else if currentName == filepath.Base(first.Directory) {
				if _, err := s.readAuthSimilarityCurrent(); err == nil {
					t.Fatal("old generation was accepted after journal advance")
				}
			} else {
				t.Fatalf("unexpected pointer %q", currentName)
			}
			retry, err := s.refreshAuthSimilarityStage()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.publishAuthSimilarityStage(filepath.Base(retry.Directory)); err != nil {
				t.Fatal(err)
			}
			if result, err := s.readAuthSimilarityCurrent(); err != nil || result.Objects != 2 {
				t.Fatalf("failed to recover complete candidate generation: %+v %v", result, err)
			}
		})
	}
}

func TestAuthoritySimilarityRebuildAfterIndexLoss(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte("independent rebuild fixture"), 300)
	mustPutObject(t, s, "similar", "a", a, "", nil)
	first, err := s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(first.Directory)); err != nil {
		t.Fatal(err)
	}
	feature := authFeatures(a, 16)[0]
	want, err := similarityPostingIDs(first, feature)
	if err != nil || len(want) == 0 {
		t.Fatalf("opening source generation returned %v %v", want, err)
	}
	manifest := filepath.Join(first.Directory, "index.json")
	report := first
	report.StoreID = newUUIDv7()
	wrongStore, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, wrongStore, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAuthSimilarityCurrent(); err == nil {
		t.Fatal("wrong-store index was accepted")
	}
	report = first
	report.Runs = make([]authSimilarityStageRun, authSimilarityMaxRuns+1)
	oversize, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, oversize, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readAuthSimilarityCurrent(); !errors.Is(err, errAuthSimilarityNeedsRebuild) {
		t.Fatalf("run count did not invalidate the generation before run I/O: %v", err)
	}
	s.Close()
	if err := os.Rename(filepath.Join(dir, "similarity-index"), filepath.Join(t.TempDir(), "lost-index")); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, got, err := s.GetObject("similar", "a")
	if err != nil || sha256.Sum256(got) != sha256.Sum256(a) {
		t.Fatalf("losing the complete index changed visible bytes: %v", err)
	}
	if _, err := s.readAuthSimilarityCurrent(); !os.IsNotExist(err) {
		t.Fatalf("index absence was not reported: %v", err)
	}
	rebuilt, err := s.refreshAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(rebuilt.Directory)); err != nil {
		t.Fatal(err)
	}
	active, err := s.readAuthSimilarityCurrent()
	if err != nil {
		t.Fatal(err)
	}
	gotIDs, err := similarityPostingIDs(active, feature)
	if err != nil || !slices.Equal(gotIDs, want) || active.JournalSHA != first.JournalSHA ||
		active.Generation == first.Generation || active.LiveObjects != first.LiveObjects {
		t.Fatalf("rebuild changed the fenced candidates: got %v want %v, error %v", gotIDs, want, err)
	}
}

func TestAuthoritySimilarityCompaction(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	var prior, current authSimilarityStageReport
	want := make(map[uint64][]uint32)
	for i := 1; i <= 8; i++ {
		body := bytes.Repeat([]byte(fmt.Sprintf("posting-%d", i)), 250)
		for _, feature := range authFeatures(body, 16) {
			want[feature] = append(want[feature], uint32(i))
		}
		mustPutObject(t, s, "similar", fmt.Sprintf("key-%d", i), body, "", nil)
		prior = current
		current, err = s.refreshAuthSimilarityStage()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.publishAuthSimilarityStage(filepath.Base(current.Directory)); err != nil {
			t.Fatal(err)
		}
	}
	if current.Objects != 8 || len(current.Runs) != 1 || len(prior.Runs) != 7 {
		t.Fatalf("incorrect compaction: objects=%d, run count %d -> %d", current.Objects, len(prior.Runs), len(current.Runs))
	}
	raw, err := os.ReadFile(filepath.Join(current.Directory, current.Runs[0].Name))
	if err != nil {
		t.Fatal(err)
	}
	run, err := openAuthSimilarityRun(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for feature, ids := range want {
		got, err := run.lookup(feature, authSimilarityRunLimit)
		if err != nil || !slices.Equal(ids, got) {
			t.Fatalf("compaction changed %x: got %v want %v err=%v", feature, got, ids, err)
		}
	}
	if _, err := s.readAuthSimilarityGeneration(filepath.Base(prior.Directory)); err != nil {
		t.Fatalf("compaction damaged its input generation: %v", err)
	}
}

func TestAuthoritySimilarityPhysicalMaintenance(t *testing.T) {
	lab := authMigratedLab(t)
	generation, err := lab.s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := lab.s.publishAuthSimilarityStage(filepath.Base(generation.Directory)); err != nil {
		t.Fatal(err)
	}
	lab.reclaim()
	lab.exact("repacked store")
	lab.verifyOK("repacked store")
	current, err := lab.s.readAuthSimilarityCurrent()
	if err != nil || current.Generation != generation.Generation {
		t.Fatalf("physical maintenance invalidated content identity: %+v %v", current, err)
	}
	if _, err := lab.s.AuthorityRevertAll(); err != nil {
		t.Fatal(err)
	}
	lab.exact("reverted store")
	lab.verifyOK("reverted store")
	current, err = lab.s.readAuthSimilarityCurrent()
	if err != nil || current.Generation != generation.Generation {
		t.Fatalf("reversion invalidated unchanged logical roots: %+v %v", current, err)
	}
}

func TestAuthoritySimilarityPrunePreservesCurrentAcrossInterruption(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("similar"))
	want := genRandomBytes(910, 135_000)
	mustPutObject(t, s, "similar", "a", want, "", nil)
	old, err := s.buildAuthSimilarityStage()
	mustOK(t, err)
	priorName := filepath.Base(old.Directory)
	mustOK(t, s.publishAuthSimilarityStage(priorName))
	mustPutObject(t, s, "similar", "b", genRandomBytes(911, 135_000), "", nil)
	current, err := s.refreshAuthSimilarityStage()
	mustOK(t, err)
	currentName := filepath.Base(current.Directory)
	mustOK(t, s.publishAuthSimilarityStage(currentName))
	const building = ".building-123"
	parent := filepath.Dir(old.Directory)
	mustOK(t, os.Mkdir(filepath.Join(parent, building), 0o700))
	report, err := s.pruneAuthSimilarity(nil, false)
	mustOK(t, err)
	if report.Current != currentName || !slices.Equal(report.Candidates, []string{building, priorName}) {
		t.Fatalf("unexpected dry run: %+v", report)
	}
	for _, refused := range [][]string{{priorName, currentName}, {priorName, "../generation-123"}} {
		if _, err := s.pruneAuthSimilarity(refused, true); err == nil {
			t.Fatalf("unsafe prune accepted: %v", refused)
		}
		_, err := os.Stat(old.Directory)
		mustOK(t, err)
	}
	s.Close()

	names := []string{priorName, building}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestAuthoritySimilarityPublicationRecovery$")
	cmd.Env = append(os.Environ(), "ZEROS3_SIMILARITY_KILL_POINT=similarity-after-prune-sync",
		"ZEROS3_SIMILARITY_STORE="+dir, "ZEROS3_SIMILARITY_GENERATION="+strings.Join(names, ","))
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("prune child did not stop at the durable boundary: %v %s", err, output)
	}
	s, err = OpenStoreShared(dir)
	mustOK(t, err)
	defer s.Close()
	if _, err := os.Stat(old.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete generation remained after unlink and sync: %v", err)
	}
	report, err = s.pruneAuthSimilarity(names, true)
	mustOK(t, err)
	if !slices.Equal(report.Removed, []string{building}) {
		t.Fatalf("retry did not finish the interrupted prune: %+v", report)
	}
	verified, err := s.readAuthSimilarityCurrent()
	mustOK(t, err)
	if verified.Directory != current.Directory || verified.Objects != 2 {
		t.Fatalf("active index changed across prune: %+v", verified)
	}
	_, got, err := s.GetObject("similar", "a")
	mustOK(t, err)
	if !bytes.Equal(got, want) {
		t.Fatal("authoritative bytes changed across prune")
	}
}

func TestAuthoritySimilarityIndexedCandidatesScopeAndFallback(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, bucket := range []string{"scope", "other"} {
		if err := s.CreateBucket(bucket); err != nil {
			t.Fatal(err)
		}
	}
	body := genRandomBytes(207, 200_000)
	related := bytes.Clone(body)
	copy(related[95000:], "a small edit in related content")
	target := bytes.Clone(body)
	copy(target[165000:], "a later edit in a different key")
	mustPutObject(t, s, "scope", "public/base", body, "", nil)
	mustPutObject(t, s, "other", "public/other", related, "", nil)
	mustPutObject(t, s, "scope", "public/target", target, "", nil)
	report, err := s.refreshAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(report.Directory)); err != nil {
		t.Fatal(err)
	}
	q, err := s.openCurrentAuthSimilarityQuery()
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	targetFeatures := authFeatures(target, 16)
	otherFeatures := authFeatures(related, 16)
	indexIncludesOther := false
	for _, f := range targetFeatures {
		indexIncludesOther = indexIncludesOther || slices.Contains(otherFeatures, f)
	}
	if !indexIncludesOther {
		t.Fatal("isolation fixture has no common posting in the other bucket")
	}
	m := &authMigrator{
		pol:          AuthPolicy{MaxDepth: 2, Candidates: 4, Prefix: "public/", similarity: q},
		order:        []*authObj{{sha: sha256.Sum256(body)}, {sha: sha256.Sum256(related)}, {sha: sha256.Sum256(target)}},
		indexedBases: make(map[[32]byte]int),
		byKey:        make(map[string][]int),
		index:        make(map[uint64][]int),
	}
	for _, lm := range testLiveManifests(t, s) {
		for _, obj := range m.order {
			if lm.man.ObjectSHA256 == fmt.Sprintf("%x", obj.sha) {
				obj.man, obj.bucket, obj.key = lm.man, lm.bucket, lm.key
			}
		}
	}
	m.addBase(0)
	m.addBase(1)
	if got := m.candidates(2, targetFeatures, false); !slices.Equal(got, []int{0}) {
		t.Fatalf("expected only the same-bucket related key from the index, got %v", got)
	}
	m.pol.Prefix = "unrelated/"
	if got := m.candidates(2, targetFeatures, false); len(got) != 0 {
		t.Fatalf("indexed candidates escaped migration prefix: %v", got)
	}
	m.pol.Prefix = "public/"
	m.index[targetFeatures[0]] = []int{0}
	if err := q.catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if got := m.candidates(2, targetFeatures, false); !q.failed || !slices.Equal(got, []int{0}) {
		t.Fatalf("index read failure did not retain existing candidates: failed=%t candidates=%v", q.failed, got)
	}
}

func TestAuthoritySimilarityHotPostingsRetainOlderScopedBase(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("scope"))
	mustOK(t, s.CreateBucket("other"))
	body := genRandomBytes(208, 196_608)
	mustPutObject(t, s, "scope", "public/base", body, "", nil)
	first, err := s.refreshAuthSimilarityStage()
	mustOK(t, err)
	mustOK(t, s.publishAuthSimilarityStage(filepath.Base(first.Directory)))

	for i := range authSimilarityRunLimit + 1 {
		noise := bytes.Clone(body)
		noise[95_000] = byte(i)
		noise[95_001] ^= 1
		mustPutObject(t, s, "other", fmt.Sprintf("public/noise-%d", i), noise, "", nil)
	}
	target := bytes.Clone(body)
	target[165_000] ^= 1
	mustPutObject(t, s, "scope", "public/target", target, "", nil)
	next, err := s.refreshAuthSimilarityStage()
	mustOK(t, err)
	mustOK(t, s.publishAuthSimilarityStage(filepath.Base(next.Directory)))
	q, err := s.openCurrentAuthSimilarityQuery()
	mustOK(t, err)
	defer q.close()

	objs := make([]*authObj, 0, next.Objects)
	for _, lm := range testLiveManifests(t, s) {
		sha, err := decodeHexSHA256(lm.man.ObjectSHA256)
		mustOK(t, err)
		objs = append(objs, &authObj{sha: sha, man: lm.man, bucket: lm.bucket, key: lm.key})
	}
	m := &authMigrator{
		pol:          AuthPolicy{MaxDepth: 2, Candidates: 4, Prefix: "public/", similarity: q},
		order:        objs,
		indexedBases: make(map[[32]byte]int),
		byKey:        make(map[string][]int),
	}
	var basePos, targetPos int
	for i, obj := range objs {
		if obj.key == "public/target" {
			targetPos = i
			continue
		}
		m.addBase(i)
		if obj.key == "public/base" {
			basePos = i
		}
	}
	for _, feature := range authFeatures(target, 16) {
		newest, err := q.postings(feature)
		mustOK(t, err)
		older, err := q.postingWindow(feature, authSimilarityRunScanLimit)
		mustOK(t, err)
		if len(newest) != authSimilarityRunLimit || slices.Contains(newest, uint32(1)) || !slices.Contains(older, uint32(1)) {
			continue
		}
		if got := m.candidates(targetPos, []uint64{feature}, false); !slices.Equal(got, []int{basePos}) {
			t.Fatalf("scope filtered out the older eligible base: got %v, want %d", got, basePos)
		}
		// Route the base and target through aliases in the crowded bucket.
		// Index content IDs and feature postings are independent of aliases.
		m.order[basePos].bucket, m.order[targetPos].bucket = "other", "other"
		m.depth = make(map[int]int)
		for j := range m.order {
			if j != basePos && j != targetPos {
				m.depth[j] = m.pol.MaxDepth
			}
		}
		if got := m.candidates(targetPos, []uint64{feature}, false); !slices.Equal(got, []int{basePos}) {
			t.Fatalf("unusable delta bases consumed the search budget: got %v, want %d", got, basePos)
		}
		return
	}
	t.Fatal("fixture has no hot feature shared with the older base")
}

func TestAuthoritySimilarityUsesScopedAliasForIndexedDiscovery(t *testing.T) {
	l := newAuthLab(t)
	if err := l.s.CreateBucket("a"); err != nil {
		t.Fatal(err)
	}
	base := genRandomBytes(834, 530_000)
	target := bytes.Clone(base)
	copy(target[210000:], genRandomBytes(845, 10_000))
	mustPutObject(t, l.s, "a", "outside-alias", base, "", nil)
	l.put("keep/base", base)
	l.put("keep/target", target)
	l.reclaim()
	if err := l.s.authEnsurePin(&authObj{sha: sha256.Sum256(base), man: l.man("keep/base")}); err != nil {
		t.Fatal(err)
	}
	report, err := l.s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.s.publishAuthSimilarityStage(filepath.Base(report.Directory)); err != nil {
		t.Fatal(err)
	}
	query, err := l.s.openCurrentAuthSimilarityQuery()
	if err != nil {
		t.Fatal(err)
	}
	defer query.close()
	pol := AuthPolicy{Bucket: "b", Prefix: "keep/", Order: "oldest", Apply: true, AllowDelta: true,
		NoZN7: true, MaxDepth: 2, MaxReadAmp: -1, similarity: query}
	migration, err := l.s.AuthorityMigrate(pol)
	if err != nil || migration.SimilarityMatches == 0 || migration.Admitted != 1 {
		t.Fatalf("scoped alias failed to resolve the indexed base: %+v %v", migration, err)
	}
	l.reclaim()
	l.exact("scoped indexed migration")
	l.verifyOK("scoped indexed migration")
	_, outside, err := l.s.GetObject("a", "outside-alias")
	if err != nil || !bytes.Equal(outside, base) {
		t.Fatalf("aliased bytes outside the scoped migration changed: %v", err)
	}
}

func TestAuthoritySimilarityQueryBoundsResidentDirectories(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("query directory fixture"), 250)
	mustPutObject(t, s, "similar", "one", body, "", nil)
	report, err := s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(report.Directory, report.Runs[0].Name)
	for i := 0; i < authSimilarityCachedDirectories; i++ {
		desc := report.Runs[0]
		desc.Name = fmt.Sprintf("copy-%d.zsr", i)
		if err := os.Link(source, filepath.Join(report.Directory, desc.Name)); err != nil {
			t.Fatal(err)
		}
		report.Runs = append(report.Runs, desc)
	}
	query, err := openAuthSimilarityQuery(report)
	if err != nil {
		t.Fatal(err)
	}
	defer query.close()
	kept := 0
	for _, run := range query.runs {
		if run.directory != nil {
			kept++
		}
	}
	if kept != authSimilarityCachedDirectories || query.runs[0].directory != nil {
		t.Fatalf("wrong resident directory count: %d of %d", kept, len(query.runs))
	}
	ids, err := query.postings(authFeatures(body, 16)[0])
	if err != nil || !slices.Equal(ids, []uint32{1}) {
		t.Fatalf("older-file reads did not preserve deduplicated candidates: %v %v", ids, err)
	}
}

func TestAuthoritySimilarityIndexInventory(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if info, err := s.inspectAuthSimilarityIndex(); err != nil || info.State != "missing" {
		t.Fatalf("missing index state: %+v %v", info, err)
	}
	if err := s.CreateBucket("similar"); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("index inventory object"), 100)
	mustPutObject(t, s, "similar", "first", body, "", nil)
	active, err := s.buildAuthSimilarityStage()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.publishAuthSimilarityStage(filepath.Base(active.Directory)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.buildAuthSimilarityStage(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.MkdirTemp(filepath.Dir(active.Directory), ".building-"); err != nil {
		t.Fatal(err)
	}
	info, err := s.inspectAuthSimilarityIndex()
	if err != nil || info.State != "current" || info.Generation == nil ||
		info.Current != filepath.Base(active.Directory) || info.Inactive != 1 || info.Incomplete != 1 {
		t.Fatalf("incorrect index inventory: %+v %v", info, err)
	}
	want := uint64(12 + 44 + authSimilarityRunBase + 4 + authSimilarityRunEntry*int(active.Runs[0].Postings))
	if info.ActivePayloadBytes != want || info.RebuildPayloadUpperBytes < want {
		t.Fatalf("incorrect verified payload accounting: got %+v want at least %d bytes", info, want)
	}
	mustPutObject(t, s, "similar", "second", body, "", nil)
	info, err = s.inspectAuthSimilarityIndex()
	if err != nil || info.State != "stale" || info.Generation != nil || info.Inactive != 1 || info.Incomplete != 1 {
		t.Fatalf("incorrect stale index inventory: %+v %v", info, err)
	}
	_, got, err := s.GetObject("similar", "first")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("index inspection changed the object: %v", err)
	}
}

func similarityPostingIDs(report authSimilarityStageReport, feature uint64) ([]uint32, error) {
	q, err := openAuthSimilarityQuery(report)
	if err != nil {
		return nil, err
	}
	defer q.close()
	return q.postings(feature)
}

// BenchmarkAuthoritySimilarityLookup uses an already published production index.
func BenchmarkAuthoritySimilarityLookup(b *testing.B) {
	dir, raw := os.Getenv("ZEROS3_C2_INDEX_STORE"), os.Getenv("ZEROS3_C2_INDEX_FEATURES")
	if dir == "" || raw == "" {
		b.Skip("set ZEROS3_C2_INDEX_STORE and ZEROS3_C2_INDEX_FEATURES")
	}
	var features []uint64
	for _, value := range strings.Split(raw, ",") {
		f, err := strconv.ParseUint(strings.TrimSpace(value), 0, 64)
		if err != nil {
			b.Fatal(err)
		}
		features = append(features, f)
	}
	lock, err := acquireStoreLock(dir, true)
	if err != nil {
		b.Fatal(err)
	}
	defer lock.release()
	s, err := OpenStoreShared(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	query, err := s.openCurrentAuthSimilarityQuery()
	if err != nil {
		b.Fatal(err)
	}
	defer query.close()
	b.ReportAllocs()
	var matched, i int
	for b.Loop() {
		ids, err := query.postings(features[i%len(features)])
		if err != nil {
			b.Fatal(err)
		}
		for _, id := range ids {
			if _, _, err := query.content(id); err != nil {
				b.Fatal(err)
			}
		}
		matched += len(ids)
		i++
	}
	b.ReportMetric(float64(matched)/float64(b.N), "candidates/op")
}
