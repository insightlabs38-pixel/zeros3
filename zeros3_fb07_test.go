package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func fb07AddRawPack(t *testing.T, s *Store, data ...[]byte) (packInfo, []packEntry) {
	t.Helper()
	b, err := s.newPackBuilder(tierHot, nil, len(data))
	mustOK(t, err)
	for _, body := range data {
		b.Add(sha256.Sum256(body), body)
	}
	path, entries, err := b.Finish()
	mustOK(t, err)
	pack, err := s.publishPackExact(tierHot, path, entries, &b.sum)
	mustOK(t, err)
	return pack, entries
}

func fb07FlipPackByte(t *testing.T, path string, offset int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	mustOK(t, err)
	defer f.Close()
	var b [1]byte
	_, err = f.ReadAt(b[:], offset)
	mustOK(t, err)
	b[0] ^= 1
	_, err = f.WriteAt(b[:], offset)
	mustOK(t, err)
	mustOK(t, f.Sync())
}

func TestFB07ScrubPhysicalDamageAndResume(t *testing.T) {
	dir := t.TempDir()
	s := b4OpenMaint(t, dir)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	live := b4Body(700, 8<<10)
	cold := b4Body(701, 8<<10)
	_, err := s.PutObject("b", "object", live, "application/octet-stream", nil)
	mustOK(t, err)
	good, _ := fb07AddRawPack(t, s, live, b4Body(702, 8<<10))
	bad, records := fb07AddRawPack(t, s, live, cold)
	format := s.formatSnapshot().StoreFormatVersion
	before, err := scrubSealedPacks(dir, format, 0, 2, "")
	mustOK(t, err)
	if before.Packs != 2 || before.IssueCount != 0 || before.IndexedRecords != 4 {
		t.Fatalf("unexpected clean scrub: %+v", before)
	}
	fb07FlipPackByte(t, bad.path, int64(records[0].off))
	fb07FlipPackByte(t, bad.path, int64(records[1].off)-packRecordHeaderSize)
	fb07FlipPackByte(t, bad.path, int64(records[1].off)+11)
	_, got, err := s.GetObject("b", "object")
	mustOK(t, err)
	if !bytes.Equal(got, live) {
		t.Fatal("healthy duplicate did not return the original object")
	}
	liveID := hex.EncodeToString(records[0].sha[:])
	deadID := hex.EncodeToString(records[1].sha[:])
	found, err := scrubSealedPacks(dir, format, 0, 2, "")
	mustOK(t, err)
	want := map[string]bool{"pack-body/": true, "record-payload/" + liveID: true,
		"record-payload/" + deadID: true, "record-header/" + deadID: true}
	if found.Packs != 2 || found.IssueCount != len(want) || len(found.Issues) != len(want) {
		t.Fatalf("missed physical damage: %+v", found)
	}
	for _, issue := range found.Issues {
		key := issue.Kind + "/" + issue.SHA256
		if issue.Pack != "hot/"+bad.id || !want[key] {
			t.Fatalf("misclassified damage: %+v", issue)
		}
		delete(want, key)
	}
	one, err := scrubSealedPacks(dir, format, good.size, 2, "")
	mustOK(t, err)
	two, err := scrubSealedPacks(dir, format, 0, 2, one.Last)
	mustOK(t, err)
	if one.Packs != 1 || two.Packs != 1 || two.Last != one.Next ||
		one.AssignedBytes+two.AssignedBytes != found.AssignedBytes || one.IssueCount+two.IssueCount != found.IssueCount {
		t.Fatalf("budget resume changed scan coverage: %+v %+v", one, two)
	}
	t.Logf("scrub: %d packs, %d distinct anomalies; duplicate remains readable; exact byte cut and resume", found.Packs, found.IssueCount)
	fb07FlipPackByte(t, good.path, good.size-packFooterSize)
	failed, err := scrubSealedPacks(dir, format, 0, 2, "")
	mustOK(t, err)
	if failed.IssueCount != found.IssueCount+1 {
		t.Fatalf("footer damage not reported: %+v", failed)
	}
	for _, issue := range failed.Issues {
		if issue.Pack == "hot/"+good.id && issue.Kind == "pack-layout" {
			return
		}
	}
	t.Fatal("footer corruption lacked a layout classification")
}

func TestFB07TargetedPhysicalRepair(t *testing.T) {
	dir := t.TempDir()
	s := b4OpenMaint(t, dir)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	local, remote, header := b4Body(721, 8<<10), b4Body(722, 9<<10), b4Body(723, 10<<10)
	p, records := fb07AddRawPack(t, s, local, remote, header)
	fb07AddRawPack(t, s, local, b4Body(724, 11<<10))
	put, err := s.PutObject("b", "object", remote, "application/octet-stream", map[string]string{"quality": "exact"})
	mustOK(t, err)
	manifest := mustManifestFor(t, s, put)
	if len(manifest.Chunks) != 1 || manifest.Chunks[0].SHA256 != hex.EncodeToString(records[1].sha[:]) {
		t.Fatal("fixture object does not reference the peer-only record")
	}
	original, err := os.ReadFile(p.path)
	mustOK(t, err)
	fb07FlipPackByte(t, p.path, int64(records[0].off)+4)
	fb07FlipPackByte(t, p.path, int64(records[1].off)+4)
	fb07FlipPackByte(t, p.path, int64(records[2].off)-packRecordHeaderSize)
	plan, err := scrubSealedPacks(dir, s.formatSnapshot().StoreFormatVersion, 0, 2, "")
	mustOK(t, err)
	if plan.IssueCount != 4 {
		t.Fatalf("expected three damaged records and a body mismatch: %+v", plan)
	}
	_, srv, creds, region := newSyncTestServer(t)
	_, err = srv.store.casWrite(remote)
	mustOK(t, err)
	var mu sync.Mutex
	var requested []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if digest, ok := strings.CutPrefix(r.URL.Path, zeros3SyncChunksPrefix); ok {
			mu.Lock()
			requested = append(requested, digest)
			mu.Unlock()
		}
		srv.ServeHTTP(w, r)
	}))
	defer peer.Close()
	cfg := mustPeerConfig(peer, creds, region)
	report, err := s.repairScrubReport(t.Context(), plan, func(ctx context.Context, digest string) ([]byte, error) {
		return fetchRepairChunk(ctx, cfg, digest)
	})
	mustOK(t, err)
	if len(report.Unresolved) != 0 || report.PacksRepaired != 1 || report.RecordsRepaired != 3 ||
		report.PeerRecords != 1 || report.PeerPayloadBytes != int64(len(remote)) {
		t.Fatalf("repair transferred extra data or missed a record: %+v", report)
	}
	mu.Lock()
	if len(requested) != 1 || requested[0] != manifest.Chunks[0].SHA256 {
		t.Errorf("peer requests: %v", requested)
	}
	mu.Unlock()
	restored, err := os.ReadFile(p.path)
	mustOK(t, err)
	if !bytes.Equal(restored, original) {
		t.Fatal("physical pack did not regain its original bytes")
	}
	retry, err := s.repairScrubReport(t.Context(), plan, nil)
	mustOK(t, err)
	if len(retry.Unresolved) != 0 || retry.RecordsRepaired != 0 || retry.PeerRecords != 0 {
		t.Fatalf("stale plan changed a healthy pack: %+v", retry)
	}
	mustOK(t, s.Close())
	s = b4OpenMaint(t, dir)
	defer s.Close()
	entry, object, err := s.GetObject("b", "object")
	mustOK(t, err)
	if !bytes.Equal(object, remote) || !reflect.DeepEqual(mustManifestFor(t, s, entry), manifest) {
		t.Fatal("reopen changed the live object's bytes or manifest")
	}
	verified, err := s.Verify(true)
	mustOK(t, err)
	if !verified.OK() {
		t.Fatalf("deep verification: %+v", verified)
	}
	t.Logf("repaired three records in the original pack; one signed peer request, %d payload bytes; exact reopen", len(remote))
}

func TestFB07PhysicalRepairPublicationCut(t *testing.T) {
	if root := os.Getenv("ZEROS3_FB07_REPAIR_ROOT"); root != "" {
		testHook = func(point string) {
			if point == hookScrubRepairBeforePublish {
				os.Exit(74)
			}
		}
		_, err := runScrubRepair(root, os.Getenv("ZEROS3_FB07_REPAIR_REPORT"), syncClientConfig{})
		t.Fatalf("repair process did not reach its publication cut: %v", err)
	}
	dir := t.TempDir()
	s := b4OpenMaint(t, dir)
	payload := b4Body(730, 8<<10)
	p, records := fb07AddRawPack(t, s, payload)
	fb07AddRawPack(t, s, payload, b4Body(731, 9<<10))
	original, err := os.ReadFile(p.path)
	mustOK(t, err)
	fb07FlipPackByte(t, p.path, int64(records[0].off)+5)
	damaged, err := os.ReadFile(p.path)
	mustOK(t, err)
	plan, err := scrubSealedPacks(dir, s.formatSnapshot().StoreFormatVersion, 0, 2, "")
	mustOK(t, err)
	mustOK(t, s.Close())
	planPath := filepath.Join(t.TempDir(), "scrub.json")
	body, err := json.Marshal(plan)
	mustOK(t, err)
	mustOK(t, os.WriteFile(planPath, body, 0o600))
	child := exec.Command(os.Args[0], "-test.run", "^TestFB07PhysicalRepairPublicationCut$")
	child.Env = append(os.Environ(), "ZEROS3_FB07_REPAIR_ROOT="+dir, "ZEROS3_FB07_REPAIR_REPORT="+planPath)
	out, err := child.CombinedOutput()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 74 {
		t.Fatalf("repair cut: %v %s", err, out)
	}
	current, err := os.ReadFile(p.path)
	mustOK(t, err)
	if !bytes.Equal(current, damaged) {
		t.Fatal("interrupted repair modified the published file")
	}
	retry, err := runScrubRepair(dir, planPath, syncClientConfig{})
	mustOK(t, err)
	current, err = os.ReadFile(p.path)
	mustOK(t, err)
	if retry.RecordsRepaired != 1 || len(retry.Unresolved) != 0 || !bytes.Equal(current, original) {
		t.Fatalf("retry did not regain exact original pack: %+v", retry)
	}
}

func TestFB07SealedRawCarry(t *testing.T) {
	for _, lane := range []string{"two-hashes", "altered-after-proof", "compressed-source-fallback"} {
		t.Run(lane, func(t *testing.T) {
			dir := t.TempDir()
			s := b4OpenMaint(t, dir)
			mustOK(t, s.CreateBucket("b"))
			live := bytes.Repeat([]byte("raw carry live! "), 512)
			dead := b4Body(740, 45<<10)
			var source packInfo
			if lane == "compressed-source-fallback" {
				b, err := s.newPackBuilder(tierHot, newBlockCompressor(true, 0), 2)
				mustOK(t, err)
				b.Add(sha256.Sum256(live), live)
				b.Add(sha256.Sum256(dead), dead)
				file, records, err := b.Finish()
				mustOK(t, err)
				if records[0].codec != packCodecDeflate {
					t.Fatal("fixture did not compress the live record")
				}
				source, err = s.publishPack(tierHot, file, records)
				mustOK(t, err)
			} else {
				source, _ = fb07AddRawPack(t, s, live, dead)
			}
			_, err := s.PutObject("b", "object", live, "application/octet-stream", nil)
			mustOK(t, err)
			mustOK(t, s.Close())
			before, err := os.ReadFile(source.path)
			mustOK(t, err)
			if lane == "altered-after-proof" {
				testHook = func(event string) {
					if event == hookPackBeforePublish {
						files, err := filepath.Glob(filepath.Join(tierTmpDir(dir, tierHot), "pack-*.tmp"))
						mustOK(t, err)
						if len(files) != 1 {
							t.Fatalf("expected one sealed stage, got %v", files)
						}
						fb07FlipPackByte(t, files[0], packHeaderSize+packRecordHeaderSize)
					}
				}
				defer func() { testHook = nil }()
			}
			rep, err := repackStore(dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, VerifiedRaw: true})
			testHook = nil
			if lane == "altered-after-proof" {
				if err == nil || !strings.Contains(err.Error(), "verified staging file changed") {
					t.Fatalf("unexpected result after a staged change: %+v %v", rep, err)
				}
				after, err := os.ReadFile(source.path)
				mustOK(t, err)
				if !bytes.Equal(before, after) {
					t.Fatal("failed proof altered a source pack")
				}
			} else {
				mustOK(t, err)
				if _, err := os.Stat(source.path); !os.IsNotExist(err) {
					t.Fatal("expected old pack to retire")
				}
				if lane == "two-hashes" && (rep.RawCarryWriterSHABytes != int64(len(live)) ||
					rep.RawCarrySealSHABytes != int64(len(live)) || rep.RawCarryFallback) {
					t.Fatalf("raw carry hash accounting: %+v", rep)
				}
				if lane == "compressed-source-fallback" && !rep.RawCarryFallback {
					t.Fatalf("compressed source did not use verified fallback: %+v", rep)
				}
			}
			b4Verify(t, dir, map[string][]byte{"object": live})
			t.Logf("%s writer_SHA=%d sealed_SHA=%d fallback=%v", lane, rep.RawCarryWriterSHABytes,
				rep.RawCarrySealSHABytes, rep.RawCarryFallback)
		})
	}
}

func TestFB07PhysicalRepairRecode(t *testing.T) {
	dir := t.TempDir()
	s := b4OpenMaint(t, dir)
	defer s.Close()
	plain := bytes.Repeat([]byte("deterministic DEFLATE record\n"), 512)
	b, err := s.newPackBuilder(tierHot, newBlockCompressor(true, 0), 1)
	mustOK(t, err)
	b.Add(sha256.Sum256(plain), plain)
	path, records, err := b.Finish()
	mustOK(t, err)
	if records[0].codec != packCodecDeflate {
		t.Fatal("expected independent DEFLATE fixture")
	}
	p, err := s.publishPack(tierHot, path, records)
	mustOK(t, err)
	fb07AddRawPack(t, s, plain)
	good, err := os.ReadFile(p.path)
	mustOK(t, err)
	fb07FlipPackByte(t, p.path, int64(records[0].off)+int64(records[0].stored)/2)
	plan, err := scrubSealedPacks(dir, s.formatSnapshot().StoreFormatVersion, 0, 2, "")
	mustOK(t, err)
	rep, err := s.repairScrubReport(t.Context(), plan, nil)
	mustOK(t, err)
	got, err := os.ReadFile(p.path)
	mustOK(t, err)
	if len(rep.Unresolved) != 0 || rep.RecordsRepaired != 1 || rep.PeerRecords != 0 || !bytes.Equal(got, good) {
		t.Fatalf("DEFLATE recode failed to restore the exact file: %+v", rep)
	}
	t.Logf("DEFLATE recode: %d exact logical bytes from a raw local copy, %d physical bytes restored", len(plain), p.size)
}
