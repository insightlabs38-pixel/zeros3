package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
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
	mustOK(t, s.writeNamespaceCheckpoint())
	return dir, upload
}

func assertCheckpointState(t *testing.T, dir string) {
	t.Helper()
	normal := checkpointTestOpen(t, dir, false)
	accelerated := checkpointTestOpen(t, dir, true)
	if !reflect.DeepEqual(normal.buckets, accelerated.buckets) ||
		!reflect.DeepEqual(normal.uploads, accelerated.uploads) ||
		!reflect.DeepEqual(normal.history, accelerated.history) ||
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

func TestNamespaceCheckpointRejectsInvalidFramesAndPrefix(t *testing.T) {
	dir, _ := checkpointTestSeed(t)
	ck := filepath.Join(dir, "journal", namespaceCheckpointName)
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
		assertCheckpointState(t, dir)
	}
	broken := bytes.Clone(good)
	broken[len(broken)/2] ^= 1
	mustOK(t, os.WriteFile(ck, broken, 0o600))
	reject()
	assertCheckpointState(t, dir)

	foreign, _ := checkpointTestSeed(t)
	mustOK(t, os.WriteFile(ck, good, 0o600))
	other := checkpointTestOpen(t, foreign, false)
	other.replayIn = &strIntern{m: map[string]string{}}
	mustOK(t, os.WriteFile(filepath.Join(foreign, "journal", namespaceCheckpointName), good, 0o600))
	if _, _, err := other.loadNamespaceCheckpoint(other.journal.f); err == nil {
		t.Fatal("foreign store checkpoint was accepted")
	}
	assertCheckpointState(t, foreign)

	// A torn suffix can leave the journal behind the checkpoint.
	mustOK(t, os.WriteFile(journal, log[:len(log)-1], 0o600))
	reject()
	assertCheckpointState(t, dir)

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
	_, err = writer.PutObject("pilot", "retained", second, "text/plain", nil)
	mustOK(t, err)
	mustOK(t, writer.Close())
	intact, err := os.ReadFile(path)
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
		})
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
			ck := filepath.Join(dir, "journal", namespaceCheckpointName)
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
		for _, bucket := range slices.Sorted(maps.Keys(s.buckets)) {
			b := s.buckets[bucket]
			fmt.Fprintf(h, "%q %q\n", b.name, b.createdAt.UTC())
			for _, key := range slices.Sorted(maps.Keys(b.objects)) {
				o := b.objects[key]
				fmt.Fprintf(h, "%q %q %x %d %q %q %d\n", key, o.manifestUUID, o.manifestSHA256,
					o.size, o.etag, o.contentType, o.seq)
				answer.current++
			}
		}
		for _, bucket := range slices.Sorted(maps.Keys(s.history)) {
			fmt.Fprintf(h, "history %q\n", bucket)
			for _, key := range slices.Sorted(maps.Keys(s.history[bucket])) {
				for _, row := range s.history[bucket][key] {
					fmt.Fprintf(h, "%q %q %q %x %d %q %q %q %q %d\n", key, row.versionID, row.manifestUUID,
						row.manifestSHA256, row.size, row.etag, row.contentType, row.archivedAt.UTC(), row.reason, row.seq)
					answer.historical++
				}
			}
		}
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
