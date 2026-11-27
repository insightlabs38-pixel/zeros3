package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func fb10JournalFixtures(t testing.TB) (journalPutPayloadV2, [32]byte) {
	t.Helper()
	const id = "019ab000-0000-7000-8000-000000000001"
	var digest, old [32]byte
	for i := range digest {
		digest[i], old[i] = byte(i), byte(31-i)
	}
	p := journalPutPayloadV2{Bucket: "golden", Key: "path\x00key", ManifestUUID: id, VersionID: id,
		Size: 4096, ETag: "00112233445566778899aabbccddeeff", ContentType: "text/plain",
		Previous: &journalArchivedVersionPayload{ManifestUUID: "019ab000-0000-7000-8000-000000000002",
			VersionID: "019ab000-0000-7000-8000-000000000003", ManifestSHA256: hex.EncodeToString(old[:]),
			Size: 7, ETag: "compound-2", ContentType: "application/octet-stream",
			ArchivedAt: time.Unix(-17, 123456).UTC(), Reason: historyReasonOverwritten}}
	return p, digest
}

func fb10JournalGolden(t testing.TB, name string) []byte {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("testdata", "fb10", name+".bin.hex"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := hex.DecodeString(string(bytes.TrimSpace(text)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFB10JournalBinGolden(t *testing.T) {
	p, digest := fb10JournalFixtures(t)
	for _, name := range []string{"overwrite", "put"} {
		if name == "put" {
			p.Previous = nil
		}
		golden := fb10JournalGolden(t, name)
		if b := encodeJournalBinPut(p, digest); !bytes.Equal(b, golden) {
			t.Fatalf("%s golden mismatch\n got %x\nwant %x", name, b, golden)
		}
		var out journalPutPayloadV2
		sum, err := decodeJournalBinPut(golden, &out)
		mustOK(t, err)
		if sum != digest || !reflect.DeepEqual(out, p) {
			t.Fatalf("%s round trip differs: %+v", name, out)
		}
		for n := range len(golden) {
			if _, err := decodeJournalBinPut(golden[:n], &out); err == nil {
				t.Fatalf("%s accepted truncation at %d", name, n)
			}
		}
		if _, err := decodeJournalBinPut(append(bytes.Clone(golden), 0), &out); err == nil {
			t.Fatalf("%s accepted trailing bytes", name)
		}
		legacy := p
		legacy.ManifestSHA256 = hex.EncodeToString(digest[:])
		raw, err := json.Marshal(legacy)
		mustOK(t, err)
		t.Logf("%s JSON=%d binary=%d reduction=%.1f%%", name, len(raw), len(golden), 100*(1-float64(len(golden))/float64(len(raw))))
	}
}

func FuzzFB10JournalBinPut(f *testing.F) {
	f.Add(fb10JournalGolden(f, "put"))
	f.Add(fb10JournalGolden(f, "overwrite"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var p journalPutPayloadV2
		sum, err := decodeJournalBinPut(data, &p)
		if err == nil && !bytes.Equal(data, encodeJournalBinPut(p, sum)) {
			t.Fatal("accepted noncanonical compact PUT")
		}
	})
}

func TestFB10JournalBinUpgradeReplay(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	_, err = s.PutObject("b", "mixed", []byte("first"), "text/plain", nil)
	mustOK(t, err)
	if _, err := openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{binary: true}); err == nil {
		t.Fatal("format upgrade raced a writer")
	}
	mustOK(t, s.Close())
	s, err = openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{binary: true})
	mustOK(t, err)
	_, err = s.PutObject("b", "mixed", []byte("second"), "text/plain", nil)
	mustOK(t, err)
	_, err = s.PutObject("b", "typed", []byte("typed"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.nsConsolidate(true))
	bound := s.ns.gen.meta
	_, err = s.PutObject("b", "suffix", []byte("suffix"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.Close())

	s, err = OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	if !slices.Contains(s.formatSnapshot().Features, journalBinFeature) || s.ns.gen == nil ||
		s.ns.gen.meta.Sequence != bound.Sequence || s.ns.gen.meta.Offset != bound.Offset {
		t.Fatal("mixed log did not reopen with its checkpoint")
	}
	for key, want := range map[string]string{"mixed": "second", "typed": "typed", "suffix": "suffix"} {
		_, got, err := s.GetObject("b", key)
		mustOK(t, err)
		if string(got) != want {
			t.Fatalf("%q reopened as %q, want %q", key, got, want)
		}
	}
	hist, err := s.histFind("b", "mixed")
	mustOK(t, err)
	if len(hist) != 1 {
		t.Fatalf("got %d archived versions", len(hist))
	}
	m, err := s.readVerifiedManifest(hist[0].manifestUUID, hist[0].manifestSHA256)
	mustOK(t, err)
	if m.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("first"))) {
		t.Fatal("compact overwrite changed the archived identity")
	}
	path := filepath.Join(dir, "journal", "visibility.log")
	f, err := os.Open(path)
	mustOK(t, err)
	defer f.Close()
	stat, err := f.Stat()
	mustOK(t, err)
	jsonRecords, compactRecords := 0, 0
	scan, _, applyErr, scanErr := scanJournalGroups(f, 0, stat.Size(), 0, false, func(rec journalRecord) error {
		if rec.recType == recordTypePutObjectRootV2 {
			if bytes.HasPrefix(rec.payload, []byte(journalBinPut)) {
				compactRecords++
			} else {
				jsonRecords++
			}
		}
		return nil
	})
	mustOK(t, scanErr)
	mustOK(t, applyErr)
	if scan.ValidEnd != stat.Size() || jsonRecords != 1 || compactRecords != 3 {
		t.Fatalf("wrong validated journal sequence: end=%d JSON=%d binary=%d", scan.ValidEnd, jsonRecords, compactRecords)
	}
	if old := os.Getenv("ZEROS3_TEST_PRE_FB10"); old != "" {
		if output, err := exec.Command(old, "verify", "-store", dir).CombinedOutput(); err == nil || !strings.Contains(string(output), journalBinFeature) {
			t.Fatalf("prior J2 reader did not refuse the activated feature: %v\n%s", err, output)
		}
	}
	t.Logf("reopened %d legacy and %d compact PUTs, exact archive, validated checkpoint and suffix", jsonRecords, compactRecords)
}

func TestFB10JournalBinProcessCuts(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	for _, point := range []string{hookJournalBinActivated, hookAfterManifestPublished, hookAfterJournalWriteBeforeSync, hookAfterJournalSync} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			_, err = s.PutObject("b", "k", []byte("acknowledged"), "text/plain", nil)
			mustOK(t, err)
			mustOK(t, s.Close())
			cmd := exec.Command(os.Args[0], "-test.run=^TestFB10JournalBinCutWorker$")
			cmd.Env = append(os.Environ(), "ZEROS3_FB10_CUT_STORE="+dir, "ZEROS3_FB10_CUT_AT="+point)
			output, err := cmd.CombinedOutput()
			var killed *exec.ExitError
			if !errors.As(err, &killed) || killed.ProcessState.ExitCode() != -1 {
				t.Fatalf("did not cut at %s: %v\n%s", point, err, output)
			}
			s, err = OpenStore(dir)
			mustOK(t, err)
			defer s.Close()
			_, data, err := s.GetObject("b", "k")
			mustOK(t, err)
			history, err := s.histFind("b", "k")
			mustOK(t, err)
			switch string(data) {
			case "acknowledged":
				if len(history) != 0 || point == hookAfterJournalSync {
					t.Fatalf("lost or partially published the durable overwrite at %s", point)
				}
			case "new":
				if len(history) != 1 || point == hookJournalBinActivated || point == hookAfterManifestPublished {
					t.Fatalf("unexpected overwrite and history at %s", point)
				}
				old, err := s.readVerifiedManifest(history[0].manifestUUID, history[0].manifestSHA256)
				mustOK(t, err)
				if old.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("acknowledged"))) {
					t.Fatal("lost earlier acknowledgement")
				}
			default:
				t.Fatalf("unrecognized visible data %q", data)
			}
			t.Logf("reopened after %s with %q and %d archived versions", point, data, len(history))
		})
	}
}

func TestFB10JournalBinCutWorker(t *testing.T) {
	dir, point := os.Getenv("ZEROS3_FB10_CUT_STORE"), os.Getenv("ZEROS3_FB10_CUT_AT")
	if dir == "" || point == "" {
		t.Skip("standalone process-cut worker")
	}
	testHook = func(p string) {
		if p == point {
			proc, err := os.FindProcess(os.Getpid())
			if err != nil || proc.Kill() != nil {
				os.Exit(91)
			}
		}
	}
	s, err := openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{binary: true})
	mustOK(t, err)
	defer s.Close()
	_, err = s.PutObject("b", "k", []byte("new"), "text/plain", nil)
	mustOK(t, err)
	t.Fatal("did not reach the requested cut")
}

func BenchmarkFB10JournalBin(b *testing.B) {
	p, digest := fb10JournalFixtures(b)
	for _, overwrite := range []bool{true, false} {
		name := "overwrite"
		if !overwrite {
			p.Previous = nil
			name = "put"
		}
		legacy := p
		legacy.ManifestSHA256 = hex.EncodeToString(digest[:])
		raw, err := json.Marshal(legacy)
		if err != nil {
			b.Fatal(err)
		}
		compact := encodeJournalBinPut(p, digest)
		for _, form := range []string{"json", "binary"} {
			b.Run(name+"/"+form+"/encode", func(b *testing.B) {
				b.ReportAllocs()
				s := Store{writeBinaryJournal: form == "binary"}
				for range b.N {
					_, _ = s.marshalJournalPut(p, digest)
				}
			})
			b.Run(name+"/"+form+"/decode", func(b *testing.B) {
				b.ReportAllocs()
				intern := &strIntern{m: map[string]string{}}
				for range b.N {
					var out journalPutPayloadV2
					if form == "binary" {
						_, _ = decodeJournalBinPut(compact, &out)
					} else {
						if !decodePutV2Fast(raw, &out, intern) {
							_ = json.Unmarshal(raw, &out)
						}
						_, _ = decodeHexSHA256(out.ManifestSHA256)
					}
				}
			})
		}
	}
}
