package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Z4-P1C: small-object segment tests (see testing-harnesses/z4/p1c/DURABILITY_MODEL.md).

func segFiles(t *testing.T, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, segDirName, "*"+segFileSuffix))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestP1C_SmallPutUsesSegmentAndSurvivesRestart(t *testing.T) {
	useProductionLayout(t)
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(1, 20_000)
	entry, err := s.PutObject("b", "k", body, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(segFiles(t, dir)); n != 1 {
		t.Fatalf("segment files = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifests", entry.manifestUUID+".json")); err == nil {
		t.Fatalf("small manifest should be inline, found a manifest file")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "chunks")); len(ents) != 0 {
		t.Fatalf("small chunks should not be loose")
	}
	if s.format.StoreFormatVersion != storeFormatVersionSegments {
		t.Fatalf("format = %d, want %d", s.format.StoreFormatVersion, storeFormatVersionSegments)
	}
	_, got, err := s.GetObject("b", "k")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("get: %v equal=%v", err, bytes.Equal(got, body))
	}
	s.Close()
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_, got, err = s2.GetObject("b", "k")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("get after restart: %v", err)
	}
	res, err := s2.Verify(true)
	if err != nil || !res.OK() {
		t.Fatalf("verify: %v %+v", err, res)
	}
}
