package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	mustOK(t, s.Close())

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
