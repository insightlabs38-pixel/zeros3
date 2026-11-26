package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFB09LegacyCopy(t *testing.T) {
	for _, mode := range []string{"legacy", "j2"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_FORMAT", mode)
			dir := t.TempDir()
			source, dst := filepath.Join(dir, "source"), filepath.Join(dir, "legacy")
			s, err := OpenStore(source)
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			mustOK(t, s.CreateBucket("empty"))
			created := s.ListBuckets()[0].createdAt
			_, err = s.PutObject("b", "json", []byte("legacy content"), "text/plain", nil)
			mustOK(t, err)
			s.writeBinaryManifests = true
			payload := b4Body(77, 2<<20)
			_, err = s.PutObject("b", "typed", payload, "application/octet-stream", map[string]string{"owner": "local fixture"})
			mustOK(t, err)
			_, original, err := s.HeadObject("b", "typed")
			mustOK(t, err)
			t.Setenv("ZEROS3_JOURNAL_FORMAT", "")
			if err := copyLegacyStore(source, dst); err == nil || !strings.Contains(err.Error(), "offline source") {
				t.Fatalf("live copy was accepted: %v", err)
			}
			mustOK(t, s.Close())
			mustOK(t, copyLegacyStore(source, dst))
			legacyStore := filepath.Join(dst, "store")
			d, err := OpenStoreShared(legacyStore)
			mustOK(t, err)
			defer d.Close()
			entry, body, err := d.GetObject("b", "typed")
			mustOK(t, err)
			m, raw, err := d.readManifestSHA(entry.manifestUUID, entry.manifestSHA256)
			mustOK(t, err)
			if !bytes.HasPrefix(raw, []byte("{")) || !reflect.DeepEqual(m, original) || !bytes.Equal(body, payload) {
				t.Fatalf("legacy copy changed object data, metadata or identity: %+v", m)
			}
			buckets := d.ListBuckets()
			if len(d.formatSnapshot().Features) != 0 || len(buckets) != 2 || !buckets[0].createdAt.Equal(created) {
				t.Fatal("legacy copy changed bucket metadata or published a new feature")
			}
			if err := copyLegacyStore(source, dst); err == nil {
				t.Fatal("legacy copy accepted an existing destination")
			}
			if old := os.Getenv("ZEROS3_TEST_PRE_FB09"); old != "" {
				if output, err := exec.Command(old, "verify", "-deep", "-store", source).CombinedOutput(); err == nil || !bytes.Contains(output, []byte("does not support")) {
					t.Fatalf("actual old binary did not refuse the typed original: %v\n%s", err, output)
				}
				if output, err := exec.Command(old, "verify", "-deep", "-store", legacyStore).CombinedOutput(); err != nil {
					t.Fatalf("actual old binary did not verify the copy: %v\n%s", err, output)
				}
				t.Log("actual pre-FB09 binary refused the typed source and deep-verified the separate legacy copy")
			}
		})
	}
}

func TestFB09LegacyCopyRefusesHistory(t *testing.T) {
	dir := t.TempDir()
	source, dst := filepath.Join(dir, "source"), filepath.Join(dir, "legacy")
	s, err := OpenStore(source)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	for _, body := range []string{"first", "second"} {
		_, err = s.PutObject("b", "versioned", []byte(body), "text/plain", nil)
		mustOK(t, err)
	}
	mustOK(t, s.Close())
	if err := copyLegacyStore(source, dst); err == nil || !strings.Contains(err.Error(), "current-only") {
		t.Fatalf("history-bearing source was accepted: %v", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("failed copy made a destination visible: %v", err)
	}
}

func fb09Manifest() manifestV1 {
	const uuid = "019ab000-0000-7000-8000-000000000001"
	first, second := sha256.Sum256([]byte("abc")), sha256.Sum256([]byte("1234"))
	whole := sha256.Sum256([]byte("abc1234"))
	return manifestV1{
		ManifestFormatVersion: manifestFormatVersion, CDCFormatVersion: cdcFormatVersion, HashAlgorithm: "sha256",
		ManifestUUID: uuid, VersionID: uuid, ContentType: "text/plain", ETag: "tag", TotalLength: 7,
		CreatedAt:    time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		Metadata:     []metadataKV{{Key: "a", Value: "é"}, {Key: "b", Value: "2"}},
		Chunks:       []chunkRef{{SHA256: hex.EncodeToString(first[:]), Length: 3}, {SHA256: hex.EncodeToString(second[:]), Length: 4}},
		ObjectSHA256: hex.EncodeToString(whole[:]),
	}
}

func TestFB09BinaryManifestGolden(t *testing.T) {
	text, err := os.ReadFile("testdata/fb09/manifest-v2.hex")
	mustOK(t, err)
	golden, err := hex.DecodeString(string(bytes.TrimSpace(text)))
	mustOK(t, err)
	original := fb09Manifest()
	encoded, err := encodeBinaryManifest(original)
	mustOK(t, err)
	if !bytes.Equal(encoded, golden) {
		t.Fatal("writer differs from the independently constructed binary golden")
	}
	decoded, err := decodeBinaryManifest(golden)
	mustOK(t, err)
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("decoded manifest differs: %+v", decoded)
	}
	original.ETag = "metadata-only"
	metadataChange, err := encodeBinaryManifest(original)
	mustOK(t, err)
	if bytes.Equal(golden, metadataChange) || !bytes.Equal(golden[len(golden)-32:], metadataChange[len(metadataChange)-32:]) {
		t.Fatal("chunk-list identity was not independent of manifest metadata")
	}
	original.Chunks[0], original.Chunks[1] = original.Chunks[1], original.Chunks[0]
	reordered, err := encodeBinaryManifest(original)
	mustOK(t, err)
	if bytes.Equal(reordered[len(reordered)-32:], golden[len(golden)-32:]) {
		t.Fatal("chunk-list identity accepted a different order")
	}
	for n := 0; n < len(golden); n++ {
		if _, err := decodeBinaryManifest(golden[:n]); err == nil {
			t.Fatalf("accepted truncation at %d", n)
		}
	}
	bad := bytes.Clone(golden)
	bad[len(bad)-5] ^= 1
	if _, err := decodeBinaryManifest(bad); err == nil {
		t.Fatal("accepted corrupted chunk-list identity")
	}
	t.Logf("binary manifest golden is %d bytes with two 36-byte refs and an independent chunk-list ID", len(golden))
}

func TestFB09MixedManifestsAndBundle(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	legacy := []byte("legacy JSON body")
	_, err = s.PutObject("b", "legacy", legacy, "text/plain", nil)
	mustOK(t, err)
	s.writeBinaryManifests = true
	binary := b4Body(901, 2<<20)
	_, err = s.PutObject("b", "binary", binary, "application/octet-stream", map[string]string{"profile": "native"})
	mustOK(t, err)
	_, err = s.PutObject("b", "small", []byte("small segment"), "text/plain", nil)
	mustOK(t, err)
	mustOK(t, s.Close())
	s, err = OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	if !slices.Contains(s.formatSnapshot().Features, manifestBinFeature) {
		t.Fatal("binary manifest feature was not durable")
	}
	for _, fixture := range []struct {
		key    string
		want   []byte
		binary bool
	}{
		{"legacy", legacy, false},
		{"binary", binary, true},
		{"small", []byte("small segment"), true},
	} {
		entry, got, err := s.GetObject("b", fixture.key)
		mustOK(t, err)
		_, data, err := s.readManifestSHA(entry.manifestUUID, entry.manifestSHA256)
		mustOK(t, err)
		if !bytes.Equal(got, fixture.want) || bytes.HasPrefix(data, []byte(manifestBinMagic)) != fixture.binary {
			t.Fatalf("changed mixed object %q", fixture.key)
		}
	}
	_, _, ranged, err := s.GetObjectRange("b", "binary", byteRange{start: 10, end: 100})
	mustOK(t, err)
	if !bytes.Equal(ranged, binary[10:101]) {
		t.Fatal("binary manifest changed range reads")
	}
	verified, err := s.Verify(true)
	mustOK(t, err)
	if !verified.OK() {
		t.Fatalf("deep mixed-store mark: %+v", verified)
	}
	srv, signer := newTestServerAndSignerFor(t, s)
	peer := httptest.NewServer(srv)
	defer peer.Close()
	cfg := mustPeerConfig(peer, Credentials{AccessKeyID: signer.accessKey, SecretAccessKey: signer.secretKey}, signer.region)
	snapshot, err := createSnapshotRemote(cfg, "b", "")
	mustOK(t, err)
	var portable bytes.Buffer
	_, err = s.exportBundle(snapshot.SnapshotID, &portable, true)
	mustOK(t, err)
	dest, err := OpenStore(t.TempDir())
	mustOK(t, err)
	defer dest.Close()
	im, err := dest.importBundle(&portable)
	mustOK(t, err)
	if im.SnapshotID != snapshot.SnapshotID || !slices.Contains(dest.formatSnapshot().Features, manifestBinFeature) {
		t.Fatalf("mixed bundle changed identity or omitted its writer feature: %+v", im)
	}
	imported, err := dest.Verify(true)
	mustOK(t, err)
	if !imported.OK() {
		t.Fatalf("deep imported bundle: %+v", imported)
	}
	t.Log("mixed JSON and binary manifests survived current reads, a range, exact mark, and verified bundle import")
}

func FuzzFB09BinaryManifest(f *testing.F) {
	golden, err := encodeBinaryManifest(fb09Manifest())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(golden)
	f.Add([]byte(manifestBinMagic))
	f.Fuzz(func(t *testing.T, data []byte) {
		man, err := decodeBinaryManifest(data)
		if err != nil {
			return
		}
		out, err := encodeBinaryManifest(man)
		if err != nil || !bytes.Equal(out, data) {
			t.Fatal("binary manifest parser accepted a noncanonical representation")
		}
	})
}
