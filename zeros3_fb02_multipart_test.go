package main

// FB-02 (M-A4): a part whose chunk list cannot fit one journal frame is
// refused with EntityTooLarge before anything is recorded (ZS-MPU-01), with
// no false rejection at or below the cap. The adversarial content repeats a
// 1 KiB block whose period cuts every chunk at exactly cdcMinChunkSize, the
// worst case the audit reproduced (1,536 MiB -> 98,304 chunks).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fb02MinCutBlock is the first 1 KiB block genRandomBytes(seed, 1024), over
// seeds from 1, that repeated cuts at exactly cdcMinChunkSize (seed 13242).
// Its SHA-256 is pinned so the fixture cannot drift silently.
func fb02MinCutBlock(t testing.TB) []byte {
	t.Helper()
	for seed := int64(1); seed < 1<<20; seed++ {
		block := genRandomBytes(seed, 1024)
		if findCDCBoundary(bytes.Repeat(block, cdcMinChunkSize/1024+1), false) == cdcMinChunkSize {
			sum := sha256.Sum256(block)
			if got := hex.EncodeToString(sum[:]); got != fb02MinCutBlockSHA256 {
				t.Fatalf("min-cut block is seed %d with sha256 %s, want %s", seed, got, fb02MinCutBlockSHA256)
			}
			return block
		}
	}
	t.Fatal("no min-cut block found")
	return nil
}

const fb02MinCutBlockSHA256 = "ac4e1351a6f0e3fc7642f4228bb2fe926b911b67ce85ce52831d88d2edaed974"

// fb02Repeat reads n bytes of block repeated, without materializing them.
type fb02Repeat struct {
	block []byte
	off   int64
	n     int64
}

func (r *fb02Repeat) Read(p []byte) (int, error) {
	if r.off >= r.n {
		return 0, io.EOF
	}
	if int64(len(p)) > r.n-r.off {
		p = p[:r.n-r.off]
	}
	k := 0
	for k < len(p) {
		c := copy(p[k:], r.block[(r.off+int64(k))%int64(len(r.block)):])
		k += c
	}
	r.off += int64(k)
	return k, nil
}

// fb02PartPayloadLen is the exact UploadPart record length for chunks under
// the given clock: the production encoder, minus its cap.
func fb02PartPayloadLen(t *testing.T, uploadID string, part int, size int64, chunks []chunkRef, at time.Time) int {
	t.Helper()
	b, err := json.Marshal(journalUploadPartPayload{UploadID: uploadID, PartNumber: part, Size: size, ETag: strings.Repeat("0", 32), Chunks: chunks, UploadedAt: at.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

// TestFB02_PartRecordBoundary drives commitPart with synthetic chunk lists
// whose encoded record is exactly maxJournalPayload and one byte over it.
func TestFB02_PartRecordBoundary(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 26, 9, 0, 0, 123456789, time.UTC) // nine fraction digits
	prev := multipartNow
	multipartNow = func() time.Time { return at }
	defer func() { multipartNow = prev }()

	digest := strings.Repeat("ab", 32)
	// Find n chunks of 5-digit length that fit, then widen r of them to
	// six digits so the record is exactly the cap.
	chunks := func(n, wide int) ([]chunkRef, int64) {
		out := make([]chunkRef, n)
		var size int64
		for i := range out {
			l := int64(16384)
			if i < wide {
				l = 100000
			}
			out[i] = chunkRef{SHA256: digest, Length: l}
			size += l
		}
		return out, size
	}
	n := maxJournalPayload / 93
	for {
		c, size := chunks(n+1, 0)
		if fb02PartPayloadLen(t, id, 1, size, c, at) > maxJournalPayload {
			break
		}
		n++
	}
	for {
		c, size := chunks(n, 0)
		if fb02PartPayloadLen(t, id, 1, size, c, at) <= maxJournalPayload {
			break
		}
		n--
	}
	// size grows by digits too; widen until the record is exactly the cap.
	wide := 0
	for {
		c, size := chunks(n, wide)
		l := fb02PartPayloadLen(t, id, 1, size, c, at)
		if l == maxJournalPayload {
			break
		}
		if l > maxJournalPayload || wide >= n {
			t.Fatalf("cannot land on the cap: n=%d wide=%d len=%d", n, wide, l)
		}
		wide++
	}
	atCap, atCapSize := chunks(n, wide)
	over, overSize := chunks(n, wide+1)
	if l := fb02PartPayloadLen(t, id, 1, overSize, over, at); l != maxJournalPayload+1 {
		t.Fatalf("one-over record is %d bytes", l)
	}
	t.Logf("cap %d bytes = %d chunks (%d widened)", maxJournalPayload, n, wide)

	if _, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: atCap, size: atCapSize}); err != nil {
		t.Fatalf("a record of exactly maxJournalPayload bytes was refused: %v", err)
	}
	var tooLarge *errPartMetadataTooLarge
	if _, err := s.commitPart("b", "k", id, 2, ingestResult{chunks: over, size: overSize}); !errors.As(err, &tooLarge) {
		t.Fatalf("a record one byte over the cap: %v", err)
	}
	// A refused retry of an existing part number leaves the part as it was.
	if _, err := s.commitPart("b", "k", id, 1, ingestResult{chunks: over, size: overSize}); !errors.As(err, &tooLarge) {
		t.Fatalf("over-cap retry of part 1: %v", err)
	}
	parts, err := s.ListParts("b", "k", id)
	if err != nil || len(parts) != 1 || parts[0].partNumber != 1 || parts[0].size != atCapSize {
		t.Fatalf("parts after refusals: %+v %v", parts, err)
	}
	// The at-cap frame replays: reopen the store from its journal alone.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, true, false)
	if err != nil {
		t.Fatalf("reopen after an at-cap part record: %v", err)
	}
	defer s.Close()
	parts, err = s.ListParts("b", "k", id)
	if err != nil || len(parts) != 1 || parts[0].size != atCapSize || len(parts[0].chunks) != n {
		t.Fatalf("replayed parts: %d %v", len(parts), err)
	}
}

// TestFB02_LargePartsHTTP: adversarial parts over the cap are refused with
// EntityTooLarge and leave no state; parts just under it, an ordinary large
// part, a short last part and completion all work, for UploadPart and
// UploadPartCopy alike.
func TestFB02_LargePartsHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("ingests about 9 GiB of adversarial parts")
	}
	block := fb02MinCutBlock(t)
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	// 90,198 min-size chunks fit (the audit's figure); 98,304 do not.
	const under, over = 90000 * cdcMinChunkSize, 98304 * cdcMinChunkSize
	put := func(path string, n int64, hdr map[string]string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, ts.URL+path, &fb02Repeat{block: block, n: n})
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = n
		u := req.URL
		signTestRequest(t, req, signer, u.EscapedPath(), u.RawQuery, nil, time.Now(), &signOpts{payloadHash: "UNSIGNED-PAYLOAD"})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var e s3ErrorBody
		_ = xml.Unmarshal(raw, &e)
		return resp.StatusCode, e.Code, resp.Header.Get("ETag")
	}
	id := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "big")
	part := func(n int) string { return fmt.Sprintf("/b/big?partNumber=%d&uploadId=%s", n, id) }

	if st, code, _ := put(part(2), over, nil); st != 400 || code != "EntityTooLarge" {
		t.Fatalf("over-cap UploadPart: %d %q", st, code)
	}
	if lp, _ := doListParts(t, client, ts.URL, signer, "b", "big", id); len(lp.Part) != 0 {
		t.Fatalf("a refused part was recorded: %+v", lp.Part)
	}
	first := genRandomBytes(40, minMultipartPartSize)
	etag1, st, _ := doUploadPart(t, client, ts.URL, signer, "b", "big", id, 1, first)
	if st != 200 {
		t.Fatalf("ordinary part 1: %d", st)
	}
	st, code, etag2 := put(part(2), under, nil)
	if st != 200 {
		t.Fatalf("adversarial part just under the cap was refused: %d %q", st, code)
	}
	// Retrying part 2 over the cap keeps the accepted part 2.
	if st, code, _ := put(part(2), over, nil); st != 400 || code != "EntityTooLarge" {
		t.Fatalf("over-cap retry: %d %q", st, code)
	}
	lp, _ := doListParts(t, client, ts.URL, signer, "b", "big", id)
	if len(lp.Part) != 2 || lp.Part[1].ETag != etag2 || lp.Part[1].Size != under {
		t.Fatalf("parts after a refused retry: %+v", lp.Part)
	}
	etag3, st, _ := doUploadPart(t, client, ts.URL, signer, "b", "big", id, 3, []byte("last"))
	if st != 200 {
		t.Fatalf("short last part: %d", st)
	}
	res, st, body := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "big", id, []completedPartXML{{1, `"` + etag1 + `"`}, {2, etag2}, {3, `"` + etag3 + `"`}})
	if st != 200 || !strings.HasSuffix(res.ETag, `-3"`) {
		t.Fatalf("complete: %d %s", st, body)
	}
	head := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/big", nil, nil)
	head.Body.Close()
	if want := strconv.FormatInt(int64(len(first))+under+4, 10); head.Header.Get("Content-Length") != want {
		t.Fatalf("completed size %s, want %s", head.Header.Get("Content-Length"), want)
	}

	// UploadPartCopy: a source of adversarial content longer than the cap.
	if st, code, _ := put("/b/src", over, nil); st != 200 {
		t.Fatalf("source PUT: %d %q", st, code)
	}
	cid := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "copy")
	copyPart := func(n int, rng string) (int, string) {
		hdr := map[string]string{"X-Amz-Copy-Source": "b/src"}
		if rng != "" {
			hdr["X-Amz-Copy-Source-Range"] = rng
		}
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, fmt.Sprintf("/b/copy?partNumber=%d&uploadId=%s", n, cid), nil, hdr)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var e s3ErrorBody
		_ = xml.Unmarshal(raw, &e)
		return resp.StatusCode, e.Code
	}
	if st, code := copyPart(1, ""); st != 400 || code != "EntityTooLarge" {
		t.Fatalf("over-cap UploadPartCopy: %d %q", st, code)
	}
	if st, code := copyPart(1, fmt.Sprintf("bytes=0-%d", under-1)); st != 200 {
		t.Fatalf("under-cap UploadPartCopy: %d %q", st, code)
	}
	if lp, _ := doListParts(t, client, ts.URL, signer, "b", "copy", cid); len(lp.Part) != 1 || lp.Part[0].Size != under {
		t.Fatalf("copy parts: %+v", lp.Part)
	}
	if res, err := srv.store.Verify(true); err != nil || !res.OK() {
		t.Fatalf("verify: %v %+v", err, res)
	}
	// No frame in the journal exceeds the cap.
	f, err := os.Open(filepath.Join(srv.store.root, "journal", "visibility.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	largest := 0
	if _, _, _, applyErr, err := replayJournalRange(f, 0, fi.Size(), 0, false, func(rec journalRecord) error {
		largest = max(largest, len(rec.payload))
		return nil
	}); err != nil || applyErr != nil {
		t.Fatalf("journal replay: %v %v", err, applyErr)
	}
	if largest > maxJournalPayload {
		t.Fatalf("a journal frame of %d bytes", largest)
	}
	t.Logf("largest journal payload %d of %d", largest, maxJournalPayload)
}
