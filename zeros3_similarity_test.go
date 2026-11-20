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
	"syscall"
	"testing"
	"time"
)

type similarityFixturePosting struct {
	feature uint64
	id      uint32
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
	entries = append(entries, similarityFixturePosting{hot + 1, 202}, similarityFixturePosting{^uint64(0), 999})
	body := similarityFixtureRun(entries)
	r, err := openAuthSimilarityRun(bytes.NewReader(body), int64(len(body)))
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
	if got, err := r.lookup(hot, 5000); err != nil || len(got) != 64 || got[0] != 937 {
		t.Fatalf("hot feature was not bounded: %v %v", got, err)
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
	unchanged, err := s.refreshAuthSimilarityStage()
	if err != nil || unchanged.Directory != next.Directory {
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
