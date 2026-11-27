package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func fb10AlignedPart(seed int) []byte {
	data := b4Body(seed, minMultipartPartSize+2*cdcMaxChunkSize)
	for at := 0; at < len(data); {
		at += findCDCBoundary(data[at:], false)
		if at >= minMultipartPartSize {
			return data[:at]
		}
	}
	panic("no boundary in bounded fixture")
}

func fb10UploadPartHTTP(t *testing.T, ts *httptest.Server, signer testSigner, key, upload string, part int, body []byte, hdr map[string]string) string {
	t.Helper()
	path := fmt.Sprintf("/b/%s?uploadId=%s&partNumber=%d", key, upload, part)
	resp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodPut, path, body, hdr)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	mustOK(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UploadPart: got HTTP %d: %s", resp.StatusCode, out)
	}
	if hdr["X-Amz-Copy-Source"] != "" {
		if !bytes.Contains(out, []byte("CopyPartResult")) {
			t.Fatalf("UploadPartCopy failed: %s", out)
		}
		return ""
	}
	return strings.Trim(resp.Header.Get("ETag"), "\"")
}

func TestFB10MultipartFrontierExact(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	opt := journalResearch{binary: true, multipartAdopt: true, multipartPages: true}
	s, err := openStoreWithFeatures(dir, true, true, false, false, false, opt)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	partA, partB := fb10AlignedPart(331), b4Body(332, 3<<20)
	full := append(bytes.Clone(partA), partB...)
	_, err = s.PutObject("b", "whole", full, "application/octet-stream", nil)
	mustOK(t, err)
	_, normal, err := s.HeadObject("b", "whole")
	mustOK(t, err)
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	upload, err := s.CreateMultipartUpload("b", "copy", "application/octet-stream", nil)
	mustOK(t, err)
	first := fb10UploadPartHTTP(t, ts, signer, "copy", upload, 1, partA, nil)
	second := fb10UploadPartHTTP(t, ts, signer, "copy", upload, 2, partB, nil)
	parts, err := s.ListParts("b", "copy", upload)
	mustOK(t, err)
	if len(parts) != 2 || parts[0].proof == nil || !parts[0].proof.Aligned || parts[1].proof == nil ||
		parts[1].proof.Prefix != fmt.Sprintf("%x", sha256.Sum256(full)) || parts[1].proof.Parent != parts[0].proof.Prefix {
		t.Fatal("signed in-order UploadPart did not form an independent exact SHA frontier")
	}
	wantETag, err := multipartETag(parts)
	mustOK(t, err)
	ts.Close()
	mustOK(t, s.nsConsolidate(true))
	mustOK(t, s.Close())

	s, err = openStoreWithFeatures(dir, true, true, false, false, false, opt)
	mustOK(t, err)
	defer s.Close()
	parts, err = s.ListParts("b", "copy", upload)
	mustOK(t, err)
	if len(parts[0].state) != 0 || len(parts[1].state) != 0 {
		t.Fatal("process-local SHA buffer was persisted")
	}
	var reads atomic.Int64
	old := testHook
	testHook = func(point string) {
		if point == hookMultipartSourcePayload {
			reads.Add(1)
		}
	}
	defer func() { testHook = old }()
	srv, signer = newTestServerAndSignerFor(t, s)
	ts = httptest.NewServer(srv)
	defer ts.Close()
	claimed := []completedPartXML{{PartNumber: 1, ETag: first}, {PartNumber: 2, ETag: second}}
	done, status, raw := doCompleteMultipartUpload(t, ts.Client(), ts.URL, signer, "b", "copy", upload, claimed)
	if status != http.StatusOK || done == nil || strings.Trim(done.ETag, "\"") != wantETag {
		t.Fatalf("CompleteMultipartUpload HTTP %d: %s", status, raw)
	}
	if reads.Load() != 0 {
		t.Fatalf("verified completion read %d chunks", reads.Load())
	}
	_, got, err := s.HeadObject("b", "copy")
	mustOK(t, err)
	if got.ObjectSHA256 != normal.ObjectSHA256 || !slices.Equal(got.Chunks, normal.Chunks) {
		t.Fatal("verified frontier changed exact SHA or continuous CDC references")
	}
	_, body, err := s.GetObject("b", "copy")
	mustOK(t, err)
	if !bytes.Equal(body, full) {
		t.Fatal("frontier completion did not recover the exact source")
	}
	t.Logf("signed in-order completion used zero payload reads after reopen; exact %d-byte object, %d native CDC references and layered MD5 ETag", len(full), len(got.Chunks))
}

func TestFB10MultipartCopyProvenance(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	body := b4Body(435, 5<<20)
	_, err = s.PutObject("b", "legacy", body, "application/octet-stream", nil)
	mustOK(t, err)
	mustOK(t, s.Close())
	s, err = openStoreWithFeatures(dir, true, true, false, false, false, journalResearch{multipartAdopt: true})
	mustOK(t, err)
	defer s.Close()
	s.writeBinaryManifests = true
	_, err = s.PutObject("b", "verified", body, "application/octet-stream", nil)
	mustOK(t, err)
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	var reads atomic.Int64
	old := testHook
	testHook = func(point string) {
		if point == hookMultipartSourcePayload {
			reads.Add(1)
		}
	}
	defer func() { testHook = old }()
	for _, tc := range []struct {
		key, source, rng string
		want             []byte
		mustRead         bool
	}{
		{"fast", "/b/verified", "", body, false},
		{"unproven", "/b/legacy", "", body, true},
		{"range", "/b/verified", "bytes=71-789", body[71:790], true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			upload, err := s.CreateMultipartUpload("b", tc.key, "application/octet-stream", nil)
			mustOK(t, err)
			reads.Store(0)
			fb10UploadPartHTTP(t, ts, signer, tc.key, upload, 1, nil,
				map[string]string{"X-Amz-Copy-Source": tc.source, "X-Amz-Copy-Source-Range": tc.rng})
			if n := reads.Load(); (n != 0) != tc.mustRead {
				t.Fatalf("read %d source chunks, want reader=%t", n, tc.mustRead)
			}
			parts, err := s.ListParts("b", tc.key, upload)
			mustOK(t, err)
			if len(parts) != 1 || parts[0].etag != fmt.Sprintf("%x", md5.Sum(tc.want)) {
				t.Fatal("adoption or fallback changed the raw part MD5")
			}
			reads.Store(0)
			_, m, err := s.CompleteMultipartUpload("b", tc.key, upload, []completedPart{{PartNumber: 1, ETag: parts[0].etag}})
			mustOK(t, err)
			if reads.Load() != 0 || m.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256(tc.want)) {
				t.Fatal("part proof did not produce zero-read exact completion")
			}
		})
	}
}

func TestFB10MultipartReplacedPredecessorUsesReader(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	s, err := openStoreWithFeatures(t.TempDir(), true, true, false, false, false, journalResearch{multipartAdopt: true})
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	oldPart, replacement := fb10AlignedPart(89), fb10AlignedPart(90)
	suffix := b4Body(91, 768<<10)
	id, err := s.CreateMultipartUpload("b", "replace", "application/octet-stream", nil)
	mustOK(t, err)
	fb10UploadPartHTTP(t, ts, signer, "replace", id, 1, oldPart, nil)
	second := fb10UploadPartHTTP(t, ts, signer, "replace", id, 2, suffix, nil)
	first := fb10UploadPartHTTP(t, ts, signer, "replace", id, 1, replacement, nil)
	parts, err := s.ListParts("b", "replace", id)
	mustOK(t, err)
	if parts[0].proof.Prefix == parts[1].proof.Parent {
		t.Fatal("fixture did not replace the earlier proved chain")
	}
	var reads atomic.Int64
	old := testHook
	testHook = func(point string) {
		if point == hookMultipartSourcePayload {
			reads.Add(1)
		}
	}
	defer func() { testHook = old }()
	_, m, err := s.CompleteMultipartUpload("b", "replace", id, []completedPart{{1, first}, {2, second}})
	mustOK(t, err)
	want := append(bytes.Clone(replacement), suffix...)
	if reads.Load() == 0 || m.ObjectSHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
		t.Fatal("completion trusted the stale proof instead of the selected replacement")
	}
}

func TestFB10PublishExactlyOnce(t *testing.T) {
	for _, mode := range []string{"legacy", "j2"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_FORMAT", mode)
			s, err := OpenStore(t.TempDir())
			mustOK(t, err)
			defer s.Close()
			mustOK(t, s.CreateBucket("b"))
			data := b4Body(74, 256<<10)
			ing, err := s.ingestStream(bytes.NewReader(data), true, ingestOptions{size: int64(len(data))})
			mustOK(t, err)
			_, baseSeq := s.journalEnd()
			var writes atomic.Int64
			old := testHook
			testHook = func(point string) {
				if point == hookAfterManifestPublished {
					writes.Add(1)
				}
			}
			defer func() { testHook = old }()
			type result struct {
				entry  *objectEntry
				cached bool
				err    error
			}
			start := make(chan struct{})
			const parallel = 24
			results := make(chan result, parallel)
			for range parallel {
				go func() {
					<-start
					man := buildManifestV1FromRefs(ing.chunks, ing.size, ing.objSHA256, fmt.Sprintf("%x", ing.etagMD5), "text/plain", map[string]string{"k": "v"})
					entry, unchanged, err := s.publish(publicationIntent{bucket: "b", key: "same", man: man, idempotent: true})
					results <- result{entry, unchanged, err}
				}()
			}
			close(start)
			var kept string
			total := 0
			for range parallel {
				res := <-results
				mustOK(t, res.err)
				if kept == "" {
					kept = res.entry.manifestUUID
				}
				if kept != res.entry.manifestUUID {
					t.Fatal("concurrent equivalents returned different publication identities")
				}
				if !res.cached {
					total++
				}
			}
			_, seq := s.journalEnd()
			if total != 1 || writes.Load() != 1 || seq != baseSeq+1 {
				t.Fatalf("issued %d new publications, %d manifest writes and %d journal records", total, writes.Load(), seq-baseSeq)
			}
			hist, err := s.histFind("b", "same")
			mustOK(t, err)
			if len(hist) != 0 {
				t.Fatal("idempotent races created history")
			}
			t.Logf("one manifest write and journal record for %d concurrent requests", parallel)
		})
	}
}

func TestFB10MultipartVerifiedManifestGolden(t *testing.T) {
	text, err := os.ReadFile("testdata/fb10/manifest-ingest.hex")
	mustOK(t, err)
	body, err := hex.DecodeString(strings.TrimSpace(string(text)))
	mustOK(t, err)
	m := fb09Manifest()
	m.VerifiedIngest = true
	encoded, err := encodeBinaryManifest(m)
	mustOK(t, err)
	if !bytes.Equal(body, encoded) {
		t.Fatal("provenance changed more than its designated binary flag")
	}
	decoded, err := decodeBinaryManifest(body)
	mustOK(t, err)
	if !decoded.VerifiedIngest || decoded.ObjectSHA256 != m.ObjectSHA256 || !slices.Equal(decoded.Chunks, m.Chunks) {
		t.Fatal("provenance flag changed chunk or whole-object identity")
	}
}
