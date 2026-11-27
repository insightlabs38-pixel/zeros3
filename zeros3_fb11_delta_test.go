package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
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

func TestFB11DeltaChangesOnly(t *testing.T) {
	src, err := openStoreWithFeatures(t.TempDir(), true, true, false, false, false, storeResearch{snapshotPages: true})
	mustOK(t, err)
	defer src.Close()
	mustOK(t, src.CreateBucket("b"))
	for i := range 512 {
		_, err := src.PutObject("b", fmt.Sprintf("key-%04d", i), []byte(fmt.Sprintf("local body %04d", i)), "text/plain", nil)
		mustOK(t, err)
	}
	capture := func() snapshotDescriptorV1 {
		entries, err := src.captureSnapshotEntries("b", "")
		mustOK(t, err)
		d := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
			CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
		mustOK(t, src.publishSnapshot(d))
		return d
	}
	base := capture()
	_, err = src.PutObject("b", "key-0008", []byte("new body"), "text/plain", map[string]string{"changed": "metadata"})
	mustOK(t, err)
	mustOK(t, src.DeleteObject("b", "key-0009"))
	_, err = src.PutObject("b", "key-0009a", []byte("inserted body"), "application/octet-stream", nil)
	mustOK(t, err)
	target := capture()

	var oldDelta, compact, fullBase bytes.Buffer
	_, err = src.exportDelta(base.SnapshotID, target.SnapshotID, &oldDelta, false)
	mustOK(t, err)
	st, err := src.exportDeltaMode(base.SnapshotID, target.SnapshotID, &compact, false, true)
	mustOK(t, err)
	_, err = src.exportBundle(base.SnapshotID, &fullBase, false)
	mustOK(t, err)
	if st.ChangedKeys != 3 || st.TransferredManifests != 2 || st.PayloadChunks != 2 || st.BaseReusedChunks != 510 ||
		st.FormatVersion != 2 || compact.Len() > oldDelta.Len()/20 {
		t.Fatalf("did not send only the changed closure: %+v bytes %d vs %d", st, compact.Len(), oldDelta.Len())
	}
	dst, err := openStoreWithFeatures(t.TempDir(), true, true, false, false, false, storeResearch{snapshotPages: true})
	mustOK(t, err)
	defer dst.Close()
	if _, err := dst.importDeltaBundle(bytes.NewReader(compact.Bytes())); err == nil {
		t.Fatal("accepted a changes-only bundle without its base")
	}
	_, err = dst.importBundle(bytes.NewReader(fullBase.Bytes()))
	mustOK(t, err)
	// Alter the target digest with a fresh whole-artifact checksum. The
	// decoder must still reject the inconsistent edit, before publication.
	bad := bytes.Clone(compact.Bytes())
	start := deltaHeaderSize
	n := int(binary.BigEndian.Uint32(bad[12:16]))
	patch, err := decodeSnapshotDelta(bad[start : start+n])
	mustOK(t, err)
	patch.TargetSHA = strings.Repeat("0", 64)
	modified, err := json.Marshal(patch)
	mustOK(t, err)
	if len(modified) != n {
		t.Fatal("fixture changed transport offsets")
	}
	copy(bad[start:], modified)
	digest := sha256.Sum256(bad[:len(bad)-sha256.Size])
	copy(bad[len(bad)-sha256.Size:], digest[:])
	if _, err := dst.importDeltaBundle(bytes.NewReader(bad)); err == nil {
		t.Fatal("accepted a rehashed artifact with a wrong target witness")
	}
	if _, err := dst.readSnapshot(target.SnapshotID); err == nil {
		t.Fatal("failed import published a target")
	}
	res, err := dst.importDeltaBundle(bytes.NewReader(compact.Bytes()))
	mustOK(t, err)
	got, portable, err := dst.readSnapshotFrame(target.SnapshotID)
	mustOK(t, err)
	original, err := encodeSnapshotDescriptor(target)
	mustOK(t, err)
	typed, err := os.ReadFile(dst.snapshotPath(target.SnapshotID))
	mustOK(t, err)
	if !bytes.Equal(portable, original) || !reflect.DeepEqual(got, target) || !bytes.HasPrefix(typed, []byte(snapshotPagesMagic)) ||
		res.ManifestsImported != 2 || res.BaseRefChunks != 510 {
		t.Fatal("sparse delta failed to reconstruct the exact target and its closed references")
	}
	var same bytes.Buffer
	sameStats, err := src.exportDeltaMode(base.SnapshotID, base.SnapshotID, &same, false, true)
	mustOK(t, err)
	res, err = dst.importDeltaBundle(bytes.NewReader(same.Bytes()))
	mustOK(t, err)
	if !res.SnapshotReused || sameStats.ChangedKeys != 0 || sameStats.TransferredManifests != 0 || sameStats.PayloadChunks != 0 {
		t.Fatalf("unchanged delta carried unnecessary records: %+v %+v", sameStats, res)
	}
	bundlePath := filepath.Join(t.TempDir(), "changes.zs3d")
	mustOK(t, os.WriteFile(bundlePath, compact.Bytes(), 0600))
	parsed, err := inspectDeltaHeader(bundlePath)
	mustOK(t, err)
	verified, err := verifyDeltaFile(bundlePath, dst.root)
	mustOK(t, err)
	if parsed.Objects != len(target.Entries) || parsed.TransferredManifests != 2 ||
		verified.ChangedKeys != 3 || verified.BaseReusedChunks != 510 {
		t.Fatal("inspect reported the wrong changes-only delta")
	}
	if old := os.Getenv("ZEROS3_TEST_PRE_FB11"); old != "" {
		if output, err := exec.Command(old, "bundle", "inspect", "-in", bundlePath).CombinedOutput(); err == nil ||
			!strings.Contains(string(output), "unsupported delta bundle format version 2") {
			t.Fatalf("old reader failed to identify the version boundary: %v\n%s", err, output)
		}
	}
	t.Logf("512-key v1 delta=%d bytes, v2=%d, zero-change=%d; changed=3 transferred manifests=2 reused payload refs=510",
		oldDelta.Len(), compact.Len(), same.Len())
}

func fb11DeltaGolden(t testing.TB) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/fb11/zero-change.json")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(body)
}

func TestFB11SnapshotDeltaGolden(t *testing.T) {
	body := fb11DeltaGolden(t)
	p, err := decodeSnapshotDelta(body)
	mustOK(t, err)
	d, frame, err := applySnapshotDelta(p, snapshotDescriptorV1{})
	mustOK(t, err)
	if d.SnapshotID != "019ab000-0000-7000-8000-000000000010" ||
		fmt.Sprintf("%x", sha256.Sum256(frame)) != p.TargetSHA {
		t.Fatal("portable reconstruction disagrees with independent golden")
	}
	for i := range body {
		if _, err := decodeSnapshotDelta(body[:i]); err == nil {
			t.Fatalf("accepted truncated edits at %d", i)
		}
	}
	if _, err := decodeSnapshotDelta(append(bytes.Clone(body), ' ')); err == nil {
		t.Fatal("accepted noncanonical edit encoding")
	}
}

func FuzzFB11SnapshotDelta(f *testing.F) {
	f.Add(fb11DeltaGolden(f))
	f.Add([]byte("{}"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, body []byte) {
		p, err := decodeSnapshotDelta(body)
		if err == nil {
			canonical, err := json.Marshal(p)
			if err != nil || !bytes.Equal(canonical, body) {
				t.Fatal("accepted invalid edit record")
			}
		}
	})
}

func TestFB11DeltaMetadataScalesWithChanges(t *testing.T) {
	var measurements []int
	for _, count := range []int{1_000, 100_000} {
		entries := make([]snapshotEntryV1, count)
		for n := range entries {
			entries[n] = snapshotEntryV1{Key: fmt.Sprintf("key-%010d", n), ManifestUUID: "019ab000-0000-7000-8000-000000000001",
				ManifestSHA256: strings.Repeat("a", 64), Size: 65536, ETag: "tag", ContentType: "text/plain"}
		}
		base := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
			CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
		target := base
		target.SnapshotID = newUUIDv7()
		target.Entries = append([]snapshotEntryV1(nil), entries...)
		for i := 0; i < 3; i++ {
			target.Entries[100*i].ContentType = "application/octet-stream"
		}
		frame, err := encodeSnapshotDescriptor(target)
		mustOK(t, err)
		refs, err := bundleManifestRefs(target)
		mustOK(t, err)
		p := buildSnapshotDelta(bundleSnapshotPlan{d: base}, bundleSnapshotPlan{d: target, frame: frame, refs: refs})
		data, err := json.Marshal(p)
		mustOK(t, err)
		q, err := decodeSnapshotDelta(data)
		mustOK(t, err)
		_, got, err := applySnapshotDelta(q, base)
		mustOK(t, err)
		if len(q.Edits) != 3 || !bytes.Equal(got, frame) {
			t.Fatal("edit scale test changed target identity")
		}
		measurements = append(measurements, len(data))
		t.Logf("keys=%d changed=3 full_descriptor=%d delta_metadata=%d", count, len(frame), len(data))
	}
	if measurements[1]-measurements[0] > 20 {
		t.Fatalf("size of nearly fixed changes grew with the namespace: %v", measurements)
	}
}

func TestFB11BundlePreservesBinaryProvenanceMarker(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	src, err := openStoreWithFeatures(t.TempDir(), true, true, false, false, false, storeResearch{multipartAdopt: true})
	mustOK(t, err)
	defer src.Close()
	src.writeBinaryManifests = true
	mustOK(t, src.CreateBucket("b"))
	_, err = src.PutObject("b", "k", []byte("locally verified CDC input"), "text/plain", nil)
	mustOK(t, err)
	entries, err := src.captureSnapshotEntries("b", "")
	mustOK(t, err)
	id := newUUIDv7()
	mustOK(t, src.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion,
		SnapshotID: id, SourceBucket: "b", CreatedAt: time.Now().UTC(), Entries: entries}))
	var bundle bytes.Buffer
	_, err = src.exportBundle(id, &bundle, false)
	mustOK(t, err)
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "")
	dst, err := OpenStore(t.TempDir())
	mustOK(t, err)
	defer dst.Close()
	_, err = dst.importBundle(bytes.NewReader(bundle.Bytes()))
	mustOK(t, err)
	format := dst.formatSnapshot()
	if !slices.Contains(format.Features, multipartAdoptFeature) || !slices.Contains(format.Features, manifestBinFeature) {
		t.Fatal("import failed to persist the binary manifest extension marker")
	}
	verified, err := dst.Verify(true)
	mustOK(t, err)
	if !verified.OK() {
		t.Fatalf("imported proof extension changed exact snapshot closure: %+v", verified)
	}
}
