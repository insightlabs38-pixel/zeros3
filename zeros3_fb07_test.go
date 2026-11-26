package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
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
