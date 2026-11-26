package main

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"
)

func checkpointTestOpen(t *testing.T, dir string, useCheckpoint bool) *Store {
	t.Helper()
	s, err := openStore(dir, false, useCheckpoint)
	mustOK(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func checkpointTestSeed(t *testing.T) (dir, upload string) {
	t.Helper()
	dir = t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("pilot"))
	_, err = s.PutObject("pilot", "retained", []byte("older bytes"), "text/plain", nil)
	mustOK(t, err)
	_, err = s.PutObject("pilot", "retained", []byte("checkpoint bytes"), "text/plain", nil)
	mustOK(t, err)
	upload, err = s.CreateMultipartUpload("pilot", "active", "text/plain", map[string]string{"checkpoint": "metadata"})
	mustOK(t, err)
	_, err = s.UploadPart("pilot", "active", upload, 2, []byte("checkpoint part"))
	mustOK(t, err)
	mustOK(t, s.nsConsolidate(true))
	return dir, upload
}

func assertCheckpointState(t *testing.T, dir string) {
	t.Helper()
	normal := checkpointTestOpen(t, dir, false)
	accelerated := checkpointTestOpen(t, dir, true)
	if nsDump(t, normal) != nsDump(t, accelerated) ||
		normal.journal.writeOffset != accelerated.journal.writeOffset ||
		normal.journal.nextSeq != accelerated.journal.nextSeq {
		t.Fatal("checkpoint replay did not match a fresh full replay")
	}
}

func TestNamespaceCheckpointMatchesHistoryMultipartAndTail(t *testing.T) {
	dir, upload := checkpointTestSeed(t)
	s, err := openStore(dir, true, true)
	mustOK(t, err)
	// Change both a visible root and an upload behind the checkpoint.
	_, err = s.PutObject("pilot", "retained", []byte("exact journal tail"), "text/plain", nil)
	mustOK(t, err)
	_, err = s.UploadPart("pilot", "active", upload, 1, []byte("tail part"))
	mustOK(t, err)
	_, err = s.PutObject("pilot", "temporary", []byte("will be deleted"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.DeleteObject("pilot", "temporary"))
	tailSeq := s.journal.nextSeq - 1
	mustOK(t, s.Close())

	direct := checkpointTestOpen(t, dir, false)
	direct.replayIn = &strIntern{m: map[string]string{}}
	if _, seq, err := direct.loadNamespaceCheckpoint(direct.journal.f); err != nil || seq >= tailSeq {
		t.Fatalf("checkpoint prefix was not accepted before the journal tail: seq %d of %d, %v", seq, tailSeq, err)
	}
	assertCheckpointState(t, dir)
	accelerated := checkpointTestOpen(t, dir, true)
	_, got, err := accelerated.GetObject("pilot", "retained")
	if err != nil || string(got) != "exact journal tail" {
		t.Fatalf("get after checkpoint and tail: %q, %v", got, err)
	}
	out, err := accelerated.Verify(true)
	if err != nil || !out.OK() {
		t.Fatalf("proof after checkpoint and tail: %v, %+v", err, out.Issues)
	}
}

func TestNamespaceCheckpointCompactHistoryAndLegacyFallback(t *testing.T) {
	dir, _ := checkpointTestSeed(t)
	legacyPath := filepath.Join(dir, "journal", namespaceCheckpointName)
	s, err := openStore(dir, true, false)
	mustOK(t, err)
	mustOK(t, s.writeLegacyCheckpoint(false))
	legacy, err := os.ReadFile(legacyPath)
	mustOK(t, err)
	mustOK(t, os.Remove(filepath.Join(dir, "journal", nsGenName)))
	mustOK(t, s.writeLegacyCheckpoint(true))
	unchanged, err := os.ReadFile(legacyPath)
	mustOK(t, err)
	if !bytes.Equal(legacy, unchanged) {
		t.Fatal("compact writer changed the existing V2 checkpoint")
	}
	history, err := s.histFind("pilot", "retained")
	mustOK(t, err)
	if len(history) == 0 {
		t.Fatal("fixture has no retained history")
	}
	prior := *history[0]
	prior.versionID = "distinct-retained-version"
	wire, err := appendCheckpointHistory(nil, &prior)
	mustOK(t, err)
	row, err := readCheckpointHistory(wire, &strIntern{m: map[string]string{}})
	mustOK(t, err)
	if !row.archivedAt.Equal(prior.archivedAt) {
		t.Fatalf("archive time changed: %v wanted %v", row.archivedAt, prior.archivedAt)
	}
	prior.archivedAt = row.archivedAt
	if *row != prior {
		t.Fatalf("distinct retained version changed: %+v wanted %+v", row, prior)
	}
	_, err = s.PutObject("pilot", "retained", []byte("replay after version three"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.Close())
	assertCheckpointState(t, dir)

	compactPath := filepath.Join(dir, "journal", namespaceCompactCheckpointName)
	compact, err := os.ReadFile(compactPath)
	mustOK(t, err)
	compact[len(compact)-1] ^= 1
	mustOK(t, os.WriteFile(compactPath, compact, 0o600))
	direct := checkpointTestOpen(t, dir, false)
	direct.replayIn = &strIntern{m: map[string]string{}}
	if _, _, err := direct.loadNamespaceCheckpointLayout(direct.journal.f, true); err == nil {
		t.Fatal("tampered V3 checkpoint was accepted")
	}
	// FB-01 (E2): a present but unusable checkpoint is refused, never skipped
	// for an older layout; the retained V2 binds only once the V3 is gone.
	var cp *checkpointProblem
	if _, _, err := direct.loadNamespaceCheckpoint(direct.journal.f); !errors.As(err, &cp) || cp.File != namespaceCompactCheckpointName || cp.Kind != checkpointDamaged {
		t.Fatalf("a tampered V3 checkpoint was not refused by name: %v", err)
	}
	assertCheckpointRefused(t, dir, checkpointDamaged)
	mustOK(t, os.Remove(compactPath))
	if _, _, err := direct.loadNamespaceCheckpoint(direct.journal.f); err != nil {
		t.Fatalf("the retained V2 checkpoint did not load: %v", err)
	}
	assertCheckpointState(t, dir)
}

// assertCheckpointRefused: an open that uses the checkpoint refuses, names its
// problem and changes nothing; a full replay that ignores it still opens.
func assertCheckpointRefused(t *testing.T, dir, kind string) {
	t.Helper()
	jp := filepath.Join(dir, "journal", "visibility.log")
	before := fb01SHA(jp)
	s, err := openStore(dir, false, true)
	if s != nil {
		s.Close()
	}
	var cp *checkpointProblem
	if !errors.Is(err, errJournalRefused) || !errors.As(err, &cp) || cp.Kind != kind {
		t.Fatalf("want a %s checkpoint refusal, got %v", kind, err)
	}
	if fb01SHA(jp) != before {
		t.Fatal("a refused open changed the journal")
	}
	checkpointTestOpen(t, dir, false)
}

// assertCheckpointRebuilt: an owner open does not adopt a generation whose
// trailer binds the journal but whose body is unusable. It replays the whole
// journal to the same namespace as an open without the checkpoint, records the
// fallback, changes nothing on disk, and the trailer still witnesses the
// journal: a cut below it is refused.
func assertCheckpointRebuilt(t *testing.T, dir string) {
	t.Helper()
	jp := filepath.Join(dir, "journal", "visibility.log")
	ckp := filepath.Join(dir, "journal", nsGenName)
	before, ckBefore := fb01SHA(jp), fb01SHA(ckp)
	plain := checkpointTestOpen(t, dir, false)
	want := nsDump(t, plain)
	plain.Close()
	s, err := openStore(dir, true, true)
	if err != nil {
		t.Fatalf("want a rebuild from the journal, got %v", err)
	}
	s.ns.auto = false
	if s.ns.gen != nil || checkpointKind(s.CheckpointFallback()) != checkpointBodyDamaged {
		t.Fatalf("want a %s fallback, got gen=%v fallback=%v", checkpointBodyDamaged, s.ns.gen != nil, s.CheckpointFallback())
	}
	if got := nsDump(t, s); got != want {
		t.Fatal("the rebuilt namespace differs from a replay without the checkpoint")
	}
	mustOK(t, s.Close())
	if fb01SHA(jp) != before || fb01SHA(ckp) != ckBefore {
		t.Fatal("the rebuild changed the journal or the checkpoint")
	}
	cut := t.TempDir()
	mustOK(t, os.CopyFS(cut, os.DirFS(dir)))
	mustOK(t, os.Truncate(filepath.Join(cut, "journal", "visibility.log"), 0))
	for _, useCk := range []bool{true, false} {
		s, err := openStore(cut, true, useCk)
		if s != nil {
			s.Close()
		}
		if !errors.Is(err, errJournalRefused) || checkpointKind(err) != checkpointContradicts {
			t.Fatalf("checkpoint=%v: a journal cut below the damaged generation's binding was not refused: %v", useCk, err)
		}
	}
}

func TestNamespaceCheckpointRejectsInvalidFramesAndPrefix(t *testing.T) {
	dir, _ := checkpointTestSeed(t)
	ck := filepath.Join(dir, "journal", nsGenName)
	journal := filepath.Join(dir, "journal", "visibility.log")
	good, err := os.ReadFile(ck)
	mustOK(t, err)
	log, err := os.ReadFile(journal)
	mustOK(t, err)
	reject := func() {
		t.Helper()
		s := checkpointTestOpen(t, dir, false)
		s.replayIn = &strIntern{m: map[string]string{}}
		if _, _, err := s.loadNamespaceCheckpoint(s.journal.f); err == nil {
			t.Fatal("damaged checkpoint was accepted")
		}
	}
	for _, length := range []int{0, 4, len(good) / 2, len(good) - 1} {
		mustOK(t, os.WriteFile(ck, good[:length], 0o600))
		reject()
		assertCheckpointRefused(t, dir, checkpointDamaged)
	}
	broken := bytes.Clone(good)
	broken[len(broken)/2] ^= 1
	mustOK(t, os.WriteFile(ck, broken, 0o600))
	reject()
	assertCheckpointRefused(t, dir, checkpointDamaged)
	// FB-01R: damage to the body alone (the first page) leaves the trailer's
	// binding intact; the open rebuilds from the journal, still witnessed.
	body := bytes.Clone(good)
	body[len(nsGenMagic)+1] ^= 1
	mustOK(t, os.WriteFile(ck, body, 0o600))
	reject()
	assertCheckpointRebuilt(t, dir)

	foreign, _ := checkpointTestSeed(t)
	mustOK(t, os.WriteFile(ck, good, 0o600))
	other := checkpointTestOpen(t, foreign, false)
	other.replayIn = &strIntern{m: map[string]string{}}
	mustOK(t, os.WriteFile(filepath.Join(foreign, "journal", nsGenName), good, 0o600))
	if _, _, err := other.loadNamespaceCheckpoint(other.journal.f); err == nil {
		t.Fatal("foreign store checkpoint was accepted")
	}
	assertCheckpointRefused(t, foreign, checkpointForeign)

	// A torn suffix can leave the journal behind the checkpoint. The checkpoint
	// witnesses that acknowledged bytes are missing, so the full replay refuses
	// as well as the accelerated open (FB-01).
	mustOK(t, os.WriteFile(journal, log[:len(log)-1], 0o600))
	before := fb01SHA(journal)
	for _, useCk := range []bool{true, false} {
		s, err := openStore(dir, false, useCk)
		if s != nil {
			s.Close()
		}
		var cp *checkpointProblem
		if !errors.As(err, &cp) || cp.Kind != checkpointContradicts {
			t.Fatalf("checkpoint=%v: want a contradicts-journal refusal, got %v", useCk, err)
		}
	}
	if fb01SHA(journal) != before {
		t.Fatal("a refused open changed the journal")
	}

	// Corruption in the first payload leaves the last frame CRC intact.
	// Full replay must diagnose it even with the opt-in read path.
	broken = bytes.Clone(log)
	broken[journalHeaderSize+5] ^= 1
	mustOK(t, os.WriteFile(journal, broken, 0o600))
	s, err := openStore(dir, false, true)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "crc mismatch") {
		t.Fatalf("checkpoint masked early journal corruption: %v", err)
	}
}

func TestNamespaceCheckpointJournalFaultModel(t *testing.T) {
	dir, _ := checkpointTestSeed(t)
	path := filepath.Join(dir, "journal", "visibility.log")
	writer, err := OpenStore(dir)
	mustOK(t, err)
	prefix := writer.journal.writeOffset
	// The first commit is durable. Model the second frame at various
	// points between write and its durability boundary.
	first := []byte("durable revision")
	second := []byte("in-flight revision")
	_, err = writer.PutObject("pilot", "retained", first, "text/plain", nil)
	mustOK(t, err)
	seam := writer.journal.writeOffset
	// The ack watermark as it stood while the second frame was in flight:
	// the model's images below pair with it, not with the final one, which
	// records the second frame as acknowledged.
	ackPath := filepath.Join(dir, "journal", journalWatermarkName)
	ackAtSeam, err := os.ReadFile(ackPath)
	mustOK(t, err)
	_, err = writer.PutObject("pilot", "retained", second, "text/plain", nil)
	mustOK(t, err)
	mustOK(t, writer.Close())
	intact, err := os.ReadFile(path)
	mustOK(t, err)
	ackFinal, err := os.ReadFile(ackPath)
	mustOK(t, err)

	model := []struct {
		name  string
		image []byte
		want  []byte
	}{
		{"all-writes", intact, second},
		{"last-entire-write-lost", intact[:seam], first},
		{"last-header-torn", intact[:seam+7], first},
		{"last-payload-torn", intact[:seam+journalHeaderSize+1], first},
		{"last-crc-torn", intact[:len(intact)-2], first},
		{"earlier-write-dropped", append(bytes.Clone(intact[:prefix]), intact[seam:]...), nil},
		{"complete-writes-reordered", append(append(bytes.Clone(intact[:prefix]), intact[seam:]...), intact[prefix:seam]...), nil},
	}
	for _, trial := range model {
		t.Run(trial.name, func(t *testing.T) {
			mustOK(t, os.WriteFile(path, trial.image, 0o600))
			ack := ackAtSeam
			if trial.name == "all-writes" {
				ack = ackFinal
			}
			// With the matching watermark, and without one (FB-01's evidence).
			for _, withAck := range []bool{true, false} {
				os.Remove(ackPath)
				if withAck {
					mustOK(t, os.WriteFile(ackPath, ack, 0o600))
				}
				for _, use := range []bool{false, true} {
					s, err := openStore(dir, false, use)
					if trial.want == nil {
						if s != nil {
							s.Close()
						}
						if err == nil || !strings.Contains(err.Error(), "expected sequence") {
							t.Fatalf("checkpoint=%t accepted a broken ordering: %v", use, err)
						}
						continue
					}
					mustOK(t, err)
					_, got, err := s.GetObject("pilot", "retained")
					s.Close()
					if err != nil || sha256.Sum256(got) != sha256.Sum256(trial.want) {
						t.Fatalf("checkpoint=%t modeled current bytes mismatch: %q %v", use, got, err)
					}
				}
			}
		})
	}
	// The final watermark records the second frame as acknowledged, so an
	// image without it is acknowledged history lost: refused, not served.
	mustOK(t, os.WriteFile(path, intact[:seam], 0o600))
	mustOK(t, os.WriteFile(ackPath, ackFinal, 0o600))
	for _, use := range []bool{false, true} {
		s, err := openStore(dir, false, use)
		if s != nil {
			s.Close()
		}
		var wp *watermarkProblem
		if !errors.As(err, &wp) || !errors.Is(err, errJournalRefused) {
			t.Fatalf("checkpoint=%t: a journal cut below its acknowledged end opened: %v", use, err)
		}
	}
}

func TestNamespaceCheckpointPublicationCuts(t *testing.T) {
	if dir := os.Getenv("ZEROS3_TEST_CHECKPOINT_CHILD"); dir != "" {
		cut := os.Getenv("ZEROS3_TEST_CHECKPOINT_CUT")
		testHook = func(point string) {
			if point == cut {
				os.Exit(93)
			}
		}
		runNamespaceCheckpoint([]string{"-store", dir})
		t.Fatal("expected a publication cut")
	}
	for _, cut := range []string{hookNamespaceCheckpointSynced, hookNamespaceCheckpointRenamed} {
		t.Run(cut, func(t *testing.T) {
			dir, _ := checkpointTestSeed(t)
			ck := filepath.Join(dir, "journal", nsGenName)
			before, err := os.ReadFile(ck)
			mustOK(t, err)
			s, err := OpenStore(dir)
			mustOK(t, err)
			_, err = s.PutObject("pilot", "retained", []byte("durable before writer cut"), "text/plain", nil)
			mustOK(t, err)
			mustOK(t, s.Close())
			cmd := exec.Command(os.Args[0], "-test.run=^TestNamespaceCheckpointPublicationCuts$")
			cmd.Env = append(os.Environ(), "ZEROS3_TEST_CHECKPOINT_CHILD="+dir, "ZEROS3_TEST_CHECKPOINT_CUT="+cut)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 93 {
				t.Fatalf("did not reach cut %s: %v %s", cut, err, output)
			}
			after, err := os.ReadFile(ck)
			mustOK(t, err)
			if bytes.Equal(before, after) != (cut == hookNamespaceCheckpointSynced) {
				t.Fatalf("unexpected checkpoint at cut %s", cut)
			}
			assertCheckpointState(t, dir)
		})
	}
}

func TestNamespaceCheckpointScaleState(t *testing.T) {
	dir, fixture := os.Getenv("ZEROS3_P3_STORE"), os.Getenv("ZEROS3_P9_OBJECT_MANIFEST")
	if dir == "" || fixture == "" {
		t.Skip("set ZEROS3_P3_STORE and ZEROS3_P9_OBJECT_MANIFEST for an offline scale fixture")
	}
	type result struct {
		sha                 string
		current, historical int
	}
	inspect := func(use bool) result {
		t.Helper()
		s, err := openStore(dir, false, use)
		mustOK(t, err)
		defer s.Close()
		h := sha256.New()
		var answer result
		answer.current, answer.historical = nsCounts(t, s)
		io.WriteString(h, nsDump(t, s))
		answer.sha = hex.EncodeToString(h.Sum(nil))
		if !use {
			return answer
		}
		source, err := os.Open(fixture)
		mustOK(t, err)
		defer source.Close()
		lines := bufio.NewScanner(source)
		picked, objects := 0, 0
		for lines.Scan() {
			objects++
			if objects%4999 != 0 {
				continue
			}
			var expected struct {
				Bucket, Key string
				SHA         string
				Deleted     bool
			}
			mustOK(t, json.Unmarshal(lines.Bytes(), &expected))
			_, got, err := s.GetObject(expected.Bucket, expected.Key)
			if expected.Deleted {
				if !errors.Is(err, errNoSuchKey) {
					t.Fatalf("deleted key %s became visible: %v", expected.Key, err)
				}
			} else if err != nil || fmt.Sprintf("%x", sha256.Sum256(got)) != expected.SHA {
				t.Fatalf("object %s does not match its original digest: %v", expected.Key, err)
			}
			picked++
		}
		mustOK(t, lines.Err())
		if picked == 0 || answer.current+answer.historical != objects {
			t.Fatalf("fixture count mismatch: %+v, manifest %d, sampled %d", answer, objects, picked)
		}
		t.Logf("%d fresh reads and absence checks against original fixture stamps", picked)
		return answer
	}

	normal := inspect(false)
	runtime.GC()
	withCheckpoint := inspect(true)
	if withCheckpoint != normal {
		t.Fatalf("journal and checkpoint state differ: %+v vs %+v", normal, withCheckpoint)
	}
	t.Logf("%d visible objects and %d retained roots, state SHA-256 %s", normal.current, normal.historical, normal.sha)
}

func BenchmarkNamespaceCheckpointOpen(b *testing.B) {
	dir := os.Getenv("ZEROS3_P3_STORE")
	if dir == "" {
		b.Skip("ZEROS3_P3_STORE not set")
	}
	for _, mode := range []struct {
		name string
		use  bool
	}{{"journal", false}, {"checkpoint", true}} {
		b.Run(mode.name, func(b *testing.B) {
			if mode.use {
				check, err := openStore(dir, false, false)
				if err != nil {
					b.Fatal(err)
				}
				check.replayIn = &strIntern{m: map[string]string{}}
				_, _, err = check.loadNamespaceCheckpoint(check.journal.f)
				check.Close()
				if err != nil {
					b.Fatalf("benchmark requires an accepted checkpoint: %v", err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				s, err := openStore(dir, false, mode.use)
				if err != nil {
					b.Fatal(err)
				}
				s.Close()
			}
		})
	}
}

// Profile one held-open store so the heap profile retains its live namespace and locators.
func TestNamespaceOpenLiveHeap(t *testing.T) {
	dir, dest := os.Getenv("ZEROS3_P3_STORE"), os.Getenv("ZEROS3_P9_HEAP_PROFILE")
	if dir == "" || dest == "" {
		t.Skip("set ZEROS3_P3_STORE and ZEROS3_P9_HEAP_PROFILE to profile an offline fixture")
	}
	mode := os.Getenv("ZEROS3_P9_PROFILE_MODE")
	if mode != "journal" && mode != "checkpoint" {
		t.Fatal("ZEROS3_P9_PROFILE_MODE must be journal or checkpoint")
	}
	s, err := openStore(dir, false, mode == "checkpoint")
	mustOK(t, err)
	defer s.Close()

	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	mustOK(t, err)
	err = pprof.WriteHeapProfile(f)
	closeErr := f.Close()
	mustOK(t, err)
	mustOK(t, closeErr)
	t.Logf("%s heap live %d bytes in %d objects, allocation total %d bytes", mode,
		stats.HeapAlloc, stats.HeapObjects, stats.TotalAlloc)
	runtime.KeepAlive(s)
}

type legacyHistKey struct{ bucket, key string }

// The legacy V2/V3 checkpoint writers are kept for compatibility tests only:
// production reads those layouts but publishes the paged V4 generation.
func (s *Store) writeLegacyCheckpoint(compactHistory bool) error {
	end := s.journal.writeOffset
	sum, crc, err := journalCheckpointDigest(s.journal.f, end)
	if err != nil {
		return err
	}
	objs, err := s.snapshotNamespace()
	if err != nil {
		return err
	}
	hist, err := s.snapshotHistory()
	if err != nil {
		return err
	}
	byBucket := map[string][]namespaceObject{}
	for _, o := range objs {
		byBucket[o.bucket] = append(byBucket[o.bucket], o)
	}
	byKey := map[legacyHistKey][]*historyVersionEntry{}
	for _, h := range hist {
		k := legacyHistKey{h.bucket, h.key}
		byKey[k] = append(byKey[k], h.entry)
	}
	header := namespaceCheckpointHeader{
		StoreID: s.format.StoreID, Offset: end, Sequence: s.journal.nextSeq - 1, LastCRC: crc, PrefixSHA: sum,
		Buckets: len(s.buckets), Uploads: len(s.uploads), Histories: len(byKey),
	}
	dir := filepath.Join(s.root, "journal")
	f, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "namespace-*.zck")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	out := bufio.NewWriterSize(f, journalReadBuffer)
	hash := sha256.New()
	body := io.MultiWriter(out, hash)
	name, magic := namespaceCheckpointName, namespaceCheckpointMagic
	if compactHistory {
		name, magic = namespaceCompactCheckpointName, namespaceCompactCheckpointMagic
	}
	if _, err := io.WriteString(body, magic); err != nil {
		return err
	}
	enc := gob.NewEncoder(body)
	if err := enc.Encode(header); err != nil {
		return err
	}
	if err := s.encodeLegacyCheckpoint(enc, body, compactHistory, byBucket, byKey); err != nil {
		return err
	}
	if _, err := out.Write(hash.Sum(nil)); err != nil {
		return err
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fireTestHook(hookNamespaceCheckpointSynced)
	if err := os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	fireTestHook(hookNamespaceCheckpointRenamed)
	return syncDir(dir)
}

func (s *Store) encodeLegacyCheckpoint(enc *gob.Encoder, output io.Writer, compactHistory bool, byBucket map[string][]namespaceObject, byKey map[legacyHistKey][]*historyVersionEntry) error {
	var row []byte
	var prefix [4]byte
	for _, name := range slices.Sorted(maps.Keys(s.buckets)) {
		b := s.buckets[name]
		rows := byBucket[name]
		slices.SortFunc(rows, func(a, b namespaceObject) int { return strings.Compare(a.key, b.key) })
		if err := enc.Encode(namespaceCheckpointBucket{Name: name, Created: b.createdAt, Objects: len(rows)}); err != nil {
			return err
		}
		for _, o := range rows {
			row = appendCheckpointObject(row[:0], o.key, o.entry)
			if err := writeCheckpointRow(output, row, &prefix); err != nil {
				return err
			}
		}
	}
	keys := slices.SortedFunc(maps.Keys(byKey), func(a, b legacyHistKey) int {
		return cmp.Or(strings.Compare(a.bucket, b.bucket), strings.Compare(a.key, b.key))
	})
	for _, hk := range keys {
		rows := byKey[hk]
		slices.SortFunc(rows, func(a, b *historyVersionEntry) int { return cmp.Compare(a.seq, b.seq) })
		{
			if err := enc.Encode(namespaceCheckpointHistory{Bucket: hk.bucket, Key: hk.key, Count: len(rows)}); err != nil {
				return err
			}
			for _, v := range rows {
				if compactHistory {
					var err error
					row, err = appendCheckpointHistory(row[:0], v)
					if err != nil {
						return err
					}
					if err := writeCheckpointRow(output, row, &prefix); err != nil {
						return err
					}
					continue
				}
				row := namespaceCheckpointVersion{
					Root: namespaceCheckpointRoot{Manifest: v.manifestUUID, Digest: v.manifestSHA256, Size: v.size,
						ETag: v.etag, ContentType: v.contentType, Sequence: v.seq},
					ID: v.versionID, Archive: v.archivedAt, Reason: v.reason,
				}
				if err := enc.Encode(row); err != nil {
					return err
				}
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.uploads)) {
		u := s.uploads[id]
		row := namespaceCheckpointUpload{id, u.bucket, u.key, u.contentType, u.metadata, u.createdAt, len(u.parts)}
		if err := enc.Encode(row); err != nil {
			return err
		}
		for _, number := range slices.Sorted(maps.Keys(u.parts)) {
			p := u.parts[number]
			if err := enc.Encode(namespaceCheckpointPart{number, p.size, p.etag, p.chunks, p.uploadedAt}); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCheckpointRow(output io.Writer, row []byte, prefix *[4]byte) error {
	if len(row) > maxNamespaceCheckpointRow {
		return errors.New("checkpoint: root exceeds row size limit")
	}
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(row)))
	if _, err := output.Write(prefix[:]); err != nil {
		return err
	}
	_, err := output.Write(row)
	return err
}

func appendCheckpointHistory(buf []byte, v *historyVersionEntry) ([]byte, error) {
	when, err := v.archivedAt.MarshalBinary()
	if err != nil {
		return nil, err
	}
	id := v.versionID
	if id == v.manifestUUID {
		buf, id = append(buf, 1), ""
	} else {
		buf = append(buf, 0)
	}
	buf = binary.AppendUvarint(buf, uint64(len(when)))
	buf = append(buf, when...)
	buf = binary.AppendUvarint(buf, uint64(len(v.reason)))
	buf = append(buf, v.reason...)
	root := objectEntry{manifestUUID: v.manifestUUID, manifestSHA256: v.manifestSHA256, size: v.size,
		etag: v.etag, contentType: v.contentType, seq: v.seq}
	return appendCheckpointObject(buf, id, &root), nil
}

func appendCheckpointObject(buf []byte, key string, root *objectEntry) []byte {
	buf = append(buf, root.manifestSHA256[:]...)
	buf = binary.AppendUvarint(buf, uint64(root.size))
	buf = binary.AppendUvarint(buf, root.seq)
	for _, value := range [...]string{key, root.manifestUUID, root.etag, root.contentType} {
		buf = binary.AppendUvarint(buf, uint64(len(value)))
		buf = append(buf, value...)
	}
	return buf
}

// nsDump is a canonical rendering of the whole namespace: buckets, visible
// rows, retained history and incomplete uploads.
func nsDump(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(s.buckets)) {
		fmt.Fprintf(&b, "bucket %q %q %d\n", name, s.buckets[name].createdAt.UTC(), s.buckets[name].count)
	}
	objs, err := s.snapshotNamespace()
	mustOK(t, err)
	for _, o := range objs {
		e := o.entry
		fmt.Fprintf(&b, "%q %q %q %x %d %q %q %d\n", o.bucket, o.key, e.manifestUUID, e.manifestSHA256, e.size, e.etag, e.contentType, e.seq)
	}
	hist, err := s.snapshotHistory()
	mustOK(t, err)
	for _, h := range hist {
		e := h.entry
		fmt.Fprintf(&b, "history %q %q %q %q %x %d %q %q %s %q %d\n", h.bucket, h.key, e.versionID, e.manifestUUID, e.manifestSHA256,
			e.size, e.etag, e.contentType, e.archivedAt.Format(time.RFC3339Nano), e.reason, e.seq)
	}
	for _, id := range slices.Sorted(maps.Keys(s.uploads)) {
		u := s.uploads[id]
		fmt.Fprintf(&b, "upload %q %q %q %q %v %s\n", id, u.bucket, u.key, u.contentType, u.metadata, u.createdAt.UTC().Format(time.RFC3339Nano))
		for _, n := range slices.Sorted(maps.Keys(u.parts)) {
			p := u.parts[n]
			fmt.Fprintf(&b, "part %d %d %q %v %s\n", n, p.size, p.etag, p.chunks, p.uploadedAt.UTC().Format(time.RFC3339Nano))
		}
	}
	return b.String()
}

func nsCounts(t *testing.T, s *Store) (current, historical int) {
	t.Helper()
	objs, err := s.snapshotNamespace()
	mustOK(t, err)
	hist, err := s.snapshotHistory()
	mustOK(t, err)
	return len(objs), len(hist)
}

// Test accessors for the compact namespace; they bypass the journal.
func (s *Store) tObj(bucket, key string) *objectEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.objFind(bucket, key)
	if err != nil {
		panic(err)
	}
	return e
}

func (s *Store) tSetObj(bucket, key string, e *objectEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.objFind(bucket, key)
	if err != nil {
		panic(err)
	}
	s.objSet(bucket, key, e, old != nil)
}

func (s *Store) tDelObj(bucket, key string) { s.tSetObj(bucket, key, nil) }

func (s *Store) tHist(bucket, key string) []*historyVersionEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.histFind(bucket, key)
	if err != nil {
		panic(err)
	}
	return l
}

func (s *Store) tClearHistory(bucket string) {
	hist, err := s.snapshotHistory()
	if err != nil {
		panic(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hist {
		if h.bucket == bucket {
			s.histSet(bucket, h.key, nil)
		}
	}
}
