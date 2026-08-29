package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"
)

// =============================================================================
// Shared test helpers
// =============================================================================

func genRandomBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

// =============================================================================
// Store / format tests
// =============================================================================

func TestStore_NewInitialization(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "FORMAT.json")); err != nil {
		t.Fatalf("FORMAT.json missing: %v", err)
	}
	for _, sub := range []string{"journal", "chunks", "manifests", "tmp"} {
		fi, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !fi.IsDir() {
			t.Fatalf("expected directory %s to exist", sub)
		}
	}
}

func TestStore_FormatJSONContents(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	data, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f storeFormat
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.StoreFormatVersion != 1 || f.CDCFormatVersion != 1 || f.HashAlgorithm != "sha256" || f.StoreID == "" {
		t.Fatalf("unexpected FORMAT.json contents: %+v", f)
	}
}

func TestStore_UnsupportedFormatRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	path := filepath.Join(dir, "FORMAT.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f storeFormat
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f.StoreFormatVersion = 99
	newData, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, newData, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err == nil {
		t.Fatalf("expected error opening a store with an unsupported format version")
	}
}

func TestStore_StableV1Constants(t *testing.T) {
	if storeFormatVersion != 1 || cdcFormatVersion != 1 || manifestFormatVersion != 1 {
		t.Fatalf("v1 format constants changed unexpectedly")
	}
	if recordTypeCreateBucket != 1 || recordTypePutObjectRoot != 2 {
		t.Fatalf("journal record type constants changed unexpectedly")
	}
	if cdcMinChunkSize != 16*1024 || cdcTargetChunkSize != 64*1024 || cdcMaxChunkSize != 256*1024 {
		t.Fatalf("CDC size constants changed unexpectedly")
	}
}

// =============================================================================
// CDC tests
// =============================================================================

func TestCDC_Deterministic(t *testing.T) {
	data := genRandomBytes(1, 2*1024*1024)
	p1, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != len(p2) {
		t.Fatalf("chunk count differs between identical runs: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i].sha != p2[i].sha || len(p1[i].data) != len(p2[i].data) {
			t.Fatalf("chunk %d differs between identical runs", i)
		}
	}
}

func TestCDC_EmptyInput(t *testing.T) {
	pieces, err := chunkData(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 0 {
		t.Fatalf("expected 0 chunks for an empty object, got %d", len(pieces))
	}
}

func TestCDC_TinyInput(t *testing.T) {
	data := []byte("hello")
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 1 {
		t.Fatalf("expected 1 chunk for tiny input, got %d", len(pieces))
	}
	if !bytes.Equal(pieces[0].data, data) {
		t.Fatalf("tiny chunk content mismatch")
	}
}

func TestCDC_MinMaxBounds(t *testing.T) {
	data := genRandomBytes(2, 8*1024*1024)
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range pieces {
		if len(p.data) > cdcMaxChunkSize {
			t.Fatalf("chunk %d exceeds max size: %d", i, len(p.data))
		}
		isLast := i == len(pieces)-1
		if len(p.data) < cdcMinChunkSize && !isLast {
			t.Fatalf("chunk %d is below min size and is not the final chunk: %d", i, len(p.data))
		}
	}
}

func TestCDC_ForcedMaximumOccurs(t *testing.T) {
	data := genRandomBytes(7, 64*1024*1024)
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	sawMax := false
	total := 0
	for _, p := range pieces {
		if len(p.data) > cdcMaxChunkSize {
			t.Fatalf("chunk exceeds max size: %d", len(p.data))
		}
		if len(p.data) == cdcMaxChunkSize {
			sawMax = true
		}
		total += len(p.data)
	}
	if total != len(data) {
		t.Fatalf("reconstructed length %d != input length %d", total, len(data))
	}
	if !sawMax {
		t.Fatalf("expected at least one forced-max-size chunk across a %d-byte random corpus", len(data))
	}
}

func TestCDC_ExactBoundarySizeInputs(t *testing.T) {
	sizes := []int{0, 1, cdcMinChunkSize - 1, cdcMinChunkSize, cdcMinChunkSize + 1,
		cdcTargetChunkSize, cdcMaxChunkSize - 1, cdcMaxChunkSize, cdcMaxChunkSize + 1}
	for _, n := range sizes {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			data := genRandomBytes(int64(n)+1000, n)
			pieces, err := chunkData(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, p := range pieces {
				total += len(p.data)
				if len(p.data) > cdcMaxChunkSize {
					t.Fatalf("chunk exceeds max size")
				}
			}
			if total != n {
				t.Fatalf("reconstructed length %d != %d", total, n)
			}
		})
	}
}

func TestCDC_RepetitiveData(t *testing.T) {
	data := bytes.Repeat([]byte{0}, 4*1024*1024)
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, p := range pieces {
		total += len(p.data)
		if len(p.data) > cdcMaxChunkSize {
			t.Fatalf("chunk exceeds max size on repetitive data")
		}
	}
	if total != len(data) {
		t.Fatalf("length mismatch on repetitive data")
	}
	pieces2, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != len(pieces2) {
		t.Fatalf("nondeterministic chunking on repetitive data")
	}
}

func TestCDC_MeanChunkSizePlausible(t *testing.T) {
	data := genRandomBytes(123, 16*1024*1024)
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) < 2 {
		t.Fatalf("expected multiple chunks from a 16MiB random corpus")
	}
	total, count := 0, 0
	for i, p := range pieces {
		if i == len(pieces)-1 {
			continue // ignore the possibly-short final chunk
		}
		total += len(p.data)
		count++
	}
	mean := float64(total) / float64(count)
	if mean < 45*1024 || mean > 85*1024 {
		t.Fatalf("mean chunk size %.0f bytes is far outside the ~56-72KiB target range", mean)
	}
}

func chunkShaSet(pieces []chunkPiece) map[[32]byte]int {
	m := map[[32]byte]int{}
	for _, p := range pieces {
		m[p.sha]++
	}
	return m
}

func countDifferingChunks(a, b []chunkPiece) int {
	ma, mb := chunkShaSet(a), chunkShaSet(b)
	diff := 0
	for k, v := range ma {
		if mb[k] < v {
			diff += v - mb[k]
		}
	}
	for k, v := range mb {
		if ma[k] < v {
			diff += v - ma[k]
		}
	}
	return diff
}

func fixedSizeChunks(data []byte, size int) [][]byte {
	var out [][]byte
	for i := 0; i < len(data); i += size {
		end := i + size
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[i:end])
	}
	return out
}

func countDifferingFixedChunks(a, b [][]byte) int {
	ma := map[[32]byte]int{}
	for _, c := range a {
		ma[sha256.Sum256(c)]++
	}
	mb := map[[32]byte]int{}
	for _, c := range b {
		mb[sha256.Sum256(c)]++
	}
	diff := 0
	for k, v := range ma {
		if mb[k] < v {
			diff += v - mb[k]
		}
	}
	for k, v := range mb {
		if ma[k] < v {
			diff += v - ma[k]
		}
	}
	return diff
}

func TestCDC_EditLocalityBeatsFixedSizeChunking(t *testing.T) {
	orig := genRandomBytes(55, 4*1024*1024)
	insertAt := len(orig) / 2
	edited := make([]byte, 0, len(orig)+1)
	edited = append(edited, orig[:insertAt]...)
	edited = append(edited, 0xAB)
	edited = append(edited, orig[insertAt:]...)

	cdcOrig, err := chunkData(bytes.NewReader(orig))
	if err != nil {
		t.Fatal(err)
	}
	cdcEdited, err := chunkData(bytes.NewReader(edited))
	if err != nil {
		t.Fatal(err)
	}
	cdcDiff := countDifferingChunks(cdcOrig, cdcEdited)

	fixedOrig := fixedSizeChunks(orig, 64*1024)
	fixedEdited := fixedSizeChunks(edited, 64*1024)
	fixedDiff := countDifferingFixedChunks(fixedOrig, fixedEdited)

	if cdcDiff >= fixedDiff {
		t.Fatalf("CDC did not resynchronize better than fixed-size chunking after a 1-byte insert: cdcDiff=%d fixedDiff=%d", cdcDiff, fixedDiff)
	}
	t.Logf("1-byte insert into a %d-byte object: CDC touched %d chunks, fixed-64KiB touched %d chunks", len(orig), cdcDiff, fixedDiff)
}

// =============================================================================
// CAS tests
// =============================================================================

func TestCAS_ChunkPathMatchesSHA256(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sum, err := s.casWrite([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(sum[:])
	path := s.chunkPath(sum)
	if filepath.Base(path) != want {
		t.Fatalf("chunk filename %q does not match digest %q", filepath.Base(path), want)
	}
	if !strings.Contains(path, filepath.Join(want[0:2], want[2:4])) {
		t.Fatalf("chunk path %q missing expected two-level shard dirs", path)
	}
}

func TestCAS_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := []byte("some chunk content")
	sum, err := s.casWrite(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.casRead(sum)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("CAS round trip mismatch")
	}
}

func TestCAS_DedupNoDuplication(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := []byte("duplicate me")
	sum1, err := s.casWrite(data)
	if err != nil {
		t.Fatal(err)
	}
	info1, err := os.Stat(s.chunkPath(sum1))
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := s.casWrite(data)
	if err != nil {
		t.Fatal(err)
	}
	if sum1 != sum2 {
		t.Fatalf("expected identical digests for identical content")
	}
	info2, err := os.Stat(s.chunkPath(sum2))
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("second write appears to have rewritten an already-published chunk")
	}
}

func TestCAS_CorruptExistingChunkDetected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sum, err := s.casWrite([]byte("original content"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.chunkPath(sum), []byte("TAMPERED CONTENT!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.casRead(sum); err == nil {
		t.Fatalf("expected corrupted chunk content to be detected on read, not trusted blindly")
	}
}

// =============================================================================
// UUID tests
//
// newUUIDv7 is a thin wrapper over the Go standard library's "uuid"
// package (uuid.NewV7().String()), added in Go 1.27. These tests prove
// the wrapper produces valid, correctly-versioned UUIDv7 values in the
// canonical string form the manifest/FORMAT.json on-disk representation
// has always expected, and that manifest/store-ID generation still works
// end to end -- i.e. no hand-rolled random UUID implementation is needed
// anymore.
// =============================================================================

// isCanonicalUUIDString reports whether s is 36 characters in the
// canonical 8-4-4-4-12 lowercase-hex-and-dash form, e.g.
// "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f".
func isCanonicalUUIDString(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

func TestUUID_CanonicalStringForm(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := newUUIDv7()
		if !isCanonicalUUIDString(id) {
			t.Fatalf("newUUIDv7() = %q is not in canonical 8-4-4-4-12 lowercase hex form", id)
		}
	}
}

func TestUUID_IsVersion7Variant10(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := newUUIDv7()
		// Per RFC 9562, the version nibble is the first hex digit of the
		// third group, and the variant's top two bits sit in the first
		// hex digit of the fourth group ("8", "9", "a", or "b" for the
		// standard variant).
		versionNibble := id[14]
		variantNibble := id[19]
		if versionNibble != '7' {
			t.Fatalf("newUUIDv7() = %q does not have version nibble 7 (got %q)", id, versionNibble)
		}
		switch variantNibble {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("newUUIDv7() = %q does not have a standard RFC 9562 variant nibble (got %q)", id, variantNibble)
		}
	}
}

func TestUUID_RoundTripsThroughStdlibParse(t *testing.T) {
	id := newUUIDv7()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("stdlib uuid.Parse rejected newUUIDv7()'s own output %q: %v", id, err)
	}
	if parsed.String() != id {
		t.Fatalf("round trip through uuid.Parse/String changed the value: %q -> %q", id, parsed.String())
	}
}

func TestUUID_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newUUIDv7()
		if seen[id] {
			t.Fatalf("newUUIDv7() produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestUUID_SuitableAsManifestAndVersionID(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", []byte("uuid-suitability-check"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !isCanonicalUUIDString(entry.manifestUUID) {
		t.Fatalf("manifest UUID %q is not in canonical form", entry.manifestUUID)
	}
	man, _, err := s.readManifest(entry.manifestUUID)
	if err != nil {
		t.Fatal(err)
	}
	if man.ManifestUUID != entry.manifestUUID {
		t.Fatalf("manifest's own recorded UUID %q does not match its filename UUID %q", man.ManifestUUID, entry.manifestUUID)
	}
	if !isCanonicalUUIDString(man.VersionID) {
		t.Fatalf("version ID %q is not in canonical UUID form", man.VersionID)
	}
	if man.VersionID != man.ManifestUUID {
		t.Fatalf("expected version ID to equal the manifest UUID for a single-version object, got %q vs %q", man.VersionID, man.ManifestUUID)
	}

	// FORMAT.json's store_id must also be a canonical UUID produced the
	// same way.
	data, err := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f storeFormat
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if !isCanonicalUUIDString(f.StoreID) {
		t.Fatalf("store_id %q is not in canonical UUID form", f.StoreID)
	}
}

// =============================================================================
// Manifest tests
// =============================================================================

func TestManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	body := []byte("manifest round trip body")
	pieces, err := chunkData(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	man, err := buildManifestV1(pieces, body, "text/plain", map[string]string{"b": "2", "a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	id, sum, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	got, raw, err := s.readManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(raw) != sum {
		t.Fatalf("returned manifest hash does not match actual file bytes")
	}
	if got.TotalLength != int64(len(body)) {
		t.Fatalf("total length mismatch: got %d want %d", got.TotalLength, len(body))
	}
	if len(got.Chunks) != len(pieces) {
		t.Fatalf("chunk count mismatch: got %d want %d", len(got.Chunks), len(pieces))
	}
	for i, c := range got.Chunks {
		if c.SHA256 != hex.EncodeToString(pieces[i].sha[:]) || c.Length != int64(len(pieces[i].data)) {
			t.Fatalf("chunk %d mismatch", i)
		}
	}
}

func TestManifest_MetadataDeterministicOrdering(t *testing.T) {
	m, err := buildManifestV1(nil, nil, "x", map[string]string{"zebra": "1", "apple": "2", "mango": "3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Metadata) != 3 || m.Metadata[0].Key != "apple" || m.Metadata[1].Key != "mango" || m.Metadata[2].Key != "zebra" {
		t.Fatalf("metadata is not deterministically sorted: %+v", m.Metadata)
	}
}

func TestManifest_ByteSHA256MatchesJournalReference(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", []byte("payload"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.readManifest(entry.manifestUUID)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(raw) != entry.manifestSHA256 {
		t.Fatalf("in-memory manifestSHA256 does not match the manifest file's actual bytes")
	}
}

func TestManifest_CorruptOrMissingDetected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("payload"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.GetObject("b", "does-not-exist"); err == nil {
		t.Fatalf("expected error for a missing key")
	}

	obj := s.buckets["b"].objects["k"]
	manPath := filepath.Join(dir, "manifests", obj.manifestUUID+".json")
	if err := os.WriteFile(manPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetObject("b", "k"); err == nil {
		t.Fatalf("expected error reading a corrupt manifest")
	}
}

// =============================================================================
// Journal tests
// =============================================================================

// buildTestFrame independently constructs a raw journal frame byte
// sequence (duplicating appendFrame's layout) so tests can inject
// hand-crafted or deliberately invalid frames directly onto disk.
func buildTestFrame(recType byte, seq uint64, payload []byte) []byte {
	header := make([]byte, journalHeaderSize)
	copy(header[0:4], journalMagic)
	binary.BigEndian.PutUint16(header[4:6], journalFrameVersion)
	header[6] = recType
	header[7] = 0
	binary.BigEndian.PutUint64(header[8:16], seq)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(payload)))
	frame := append([]byte{}, header...)
	frame = append(frame, payload...)
	crc := crc32.Checksum(frame, castagnoliTable)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	return append(frame, crcBytes...)
}

func TestJournal_FrameEncodeDecodeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	j, records, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no records in a fresh journal")
	}
	payload := []byte(`{"bucket":"x"}`)
	seq, err := j.appendFrame(recordTypeCreateBucket, payload)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("expected first sequence number to be 1, got %d", seq)
	}
	j.f.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, lastSeq, records2, err := replayJournal(f)
	if err != nil {
		t.Fatal(err)
	}
	if lastSeq != 1 || len(records2) != 1 {
		t.Fatalf("unexpected replay result: lastSeq=%d records=%d", lastSeq, len(records2))
	}
	if !bytes.Equal(records2[0].payload, payload) {
		t.Fatalf("payload mismatch after round trip")
	}
}

func TestJournal_CRC32C(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	frame := buildTestFrame(recordTypeCreateBucket, 1, []byte(`{"bucket":"a"}`))
	if err := os.WriteFile(path, frame, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, records, err := replayJournal(f); err != nil || len(records) != 1 {
		t.Fatalf("expected a valid frame to replay cleanly, got records=%d err=%v", len(records), err)
	}
	f.Close()

	// Flip one payload byte; the CRC32C trailer must now fail to verify.
	corrupt := append([]byte{}, frame...)
	corrupt[journalHeaderSize] ^= 0xFF
	if err := os.WriteFile(path, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	f2, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if _, _, _, err := replayJournal(f2); err == nil {
		t.Fatalf("expected CRC32C mismatch to be detected")
	}
}

func TestJournal_SequenceEnforcement(t *testing.T) {
	cases := []struct {
		name string
		seqs []uint64
	}{
		{"gap", []uint64{1, 3}},
		{"duplicate", []uint64{1, 1}},
		{"does-not-start-at-one", []uint64{2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "test.log")
			var buf []byte
			for _, seq := range c.seqs {
				buf = append(buf, buildTestFrame(recordTypeCreateBucket, seq, []byte(`{"bucket":"x"}`))...)
			}
			if err := os.WriteFile(path, buf, 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, _, _, err := replayJournal(f); err == nil {
				t.Fatalf("expected a sequence error for case %q", c.name)
			}
		})
	}
}

func TestJournal_PayloadLengthBound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	j, _, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.f.Close()
	big := make([]byte, maxJournalPayload+1)
	if _, err := j.appendFrame(recordTypeCreateBucket, big); err == nil {
		t.Fatalf("expected an error for a payload exceeding the maximum")
	}
}

func TestJournal_CreateBucketReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("bucket1"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok := s2.buckets["bucket1"]; !ok {
		t.Fatalf("expected bucket1 to survive reopen via journal replay")
	}
}

func TestJournal_PutObjectRootReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("hello"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_, data, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data after replay: %q", data)
	}
}

func TestJournal_MultiFrameReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b1"))
	mustOK(t, s.CreateBucket("b2"))
	if _, err := s.PutObject("b1", "x", []byte("1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b2", "y", []byte("2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b1", "x", []byte("1-updated"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_, d, err := s2.GetObject("b1", "x")
	if err != nil || string(d) != "1-updated" {
		t.Fatalf("expected overwritten value to survive replay, got %q err=%v", d, err)
	}
	_, d2, err := s2.GetObject("b2", "y")
	if err != nil || string(d2) != "2" {
		t.Fatalf("unexpected b2/y after replay: %q err=%v", d2, err)
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournal_IncompleteFinalFrameIsTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "j.log")
	j, _, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	firstPayload := []byte(`{"bucket":"a"}`)
	if _, err := j.appendFrame(recordTypeCreateBucket, firstPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := j.appendFrame(recordTypeCreateBucket, []byte(`{"bucket":"b"}`)); err != nil {
		t.Fatal(err)
	}
	fullSize := j.writeOffset
	j.f.Close()

	if err := os.Truncate(path, fullSize-3); err != nil {
		t.Fatal(err)
	}

	j2, records, err := openJournal(path)
	if err != nil {
		t.Fatalf("expected an incomplete final frame to be tolerated as a torn tail, got: %v", err)
	}
	defer j2.f.Close()
	if len(records) != 1 {
		t.Fatalf("expected only the first complete frame to survive, got %d records", len(records))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != int64(journalHeaderSize+len(firstPayload)+4) {
		t.Fatalf("expected the journal to be truncated to the last valid frame, got size %d", fi.Size())
	}
}

func TestJournal_CorruptionInEarlierFrameFails(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	path := filepath.Join(dir, "journal", "visibility.log")
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{'X'}, journalHeaderSize+2); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := OpenStore(dir); err == nil {
		t.Fatalf("expected corruption in an earlier, complete frame to fail store open")
	}
}

func TestJournal_InvalidVersionAndTypeRejected(t *testing.T) {
	t.Run("bad version", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "j.log")
		header := make([]byte, journalHeaderSize)
		copy(header[0:4], journalMagic)
		binary.BigEndian.PutUint16(header[4:6], 99)
		header[6] = recordTypeCreateBucket
		binary.BigEndian.PutUint64(header[8:16], 1)
		binary.BigEndian.PutUint32(header[16:20], 0)
		crc := crc32.Checksum(header, castagnoliTable)
		crcBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(crcBytes, crc)
		frame := append(header, crcBytes...)
		if err := os.WriteFile(path, frame, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, _, _, err := replayJournal(f); err == nil {
			t.Fatalf("expected an error for an unsupported frame version")
		}
	})
	t.Run("bad type", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "j.log")
		frame := buildTestFrame(99, 1, nil)
		if err := os.WriteFile(path, frame, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, _, _, err := replayJournal(f); err == nil {
			t.Fatalf("expected an error for an unknown record type")
		}
	})
}

// =============================================================================
// SigV4 tests
//
// signTestRequest below is a self-contained, test-only SigV4 signer: it
// does not call any of zeros3.go's own sigv4* helper functions, so these
// tests exercise the server's implementation independently rather than
// checking it against itself.
// =============================================================================

func testHexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func testHMAC(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func testCanonicalEncode(raw []byte) string {
	var sb strings.Builder
	for _, b := range raw {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			sb.WriteByte(b)
		} else {
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

func testPercentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err == nil {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

func testCanonicalURI(rawPath string) string {
	if rawPath == "" {
		rawPath = "/"
	}
	segs := strings.Split(rawPath, "/")
	for i, seg := range segs {
		segs[i] = testCanonicalEncode(testPercentDecode(seg))
	}
	return strings.Join(segs, "/")
}

func testCanonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	var pairs [][2]string
	for _, p := range strings.Split(rawQuery, "&") {
		if p == "" {
			continue
		}
		k, v := p, ""
		if idx := strings.IndexByte(p, '='); idx >= 0 {
			k, v = p[:idx], p[idx+1:]
		}
		pairs = append(pairs, [2]string{testCanonicalEncode(testPercentDecode(k)), testCanonicalEncode(testPercentDecode(v))})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p[0] + "=" + p[1]
	}
	return strings.Join(parts, "&")
}

func TestSigV4_CanonicalURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/bucket/key", "/bucket/key"},
		{"/bucket//double", "/bucket//double"},
		{"/bucket/enc%2Fslash", "/bucket/enc%2Fslash"},
		{"/bucket/a%20b", "/bucket/a%20b"},
		{"/bucket/a+b", "/bucket/a%2Bb"},
		{"/bucket/trailing/", "/bucket/trailing/"},
		{"", "/"},
	}
	for _, c := range cases {
		got, err := sigv4CanonicalURI(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("sigv4CanonicalURI(%q) = %q, want %q", c.in, got, c.want)
		}
		// Cross-check against the independent test implementation.
		if want := testCanonicalURI(c.in); got != want {
			t.Fatalf("sigv4CanonicalURI(%q) = %q, independent implementation says %q", c.in, got, want)
		}
	}
}

func TestSigV4_CanonicalQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"b=2&a=1", "a=1&b=2"},
		{"a=1&a=2", "a=1&a=2"},
		{"empty=", "empty="},
		{"noeq", "noeq="},
		{"a=1&a=&a=2", "a=&a=1&a=2"},
	}
	for _, c := range cases {
		got, err := sigv4CanonicalQuery(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("sigv4CanonicalQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

type testSigner struct {
	accessKey, secretKey, region string
}

type signOpts struct {
	extraSignedHeaders          []string
	omitContentSha256FromSigned bool
	badPayloadHash              string
}

// signTestRequest computes and sets X-Amz-Date, X-Amz-Content-Sha256, and
// Authorization on req, following the AWS4-HMAC-SHA256 algorithm
// independently of zeros3.go's own implementation.
func signTestRequest(t *testing.T, req *http.Request, signer testSigner, rawPath, rawQuery string, body []byte, when time.Time, opts *signOpts) {
	t.Helper()
	if opts == nil {
		opts = &signOpts{}
	}
	amzDate := when.UTC().Format("20060102T150405Z")
	dateStamp := when.UTC().Format("20060102")
	payloadHash := testHexSHA256(body)

	req.Header.Set("X-Amz-Date", amzDate)
	if opts.badPayloadHash != "" {
		req.Header.Set("X-Amz-Content-Sha256", opts.badPayloadHash)
	} else {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}

	names := map[string]bool{"host": true, "x-amz-date": true}
	if !opts.omitContentSha256FromSigned {
		names["x-amz-content-sha256"] = true
	}
	for _, n := range opts.extraSignedHeaders {
		names[strings.ToLower(n)] = true
	}
	var sortedNames []string
	for n := range names {
		sortedNames = append(sortedNames, n)
	}
	sort.Strings(sortedNames)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	var chb strings.Builder
	for _, n := range sortedNames {
		var v string
		if n == "host" {
			v = host
		} else {
			v = req.Header.Get(n)
		}
		chb.WriteString(n + ":" + strings.TrimSpace(v) + "\n")
	}
	signedHeaders := strings.Join(sortedNames, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		testCanonicalURI(rawPath),
		testCanonicalQuery(rawQuery),
		chb.String(),
		signedHeaders,
		req.Header.Get("X-Amz-Content-Sha256"),
	}, "\n")

	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, signer.region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, testHexSHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := testHMAC([]byte("AWS4"+signer.secretKey), dateStamp)
	kRegion := testHMAC(kDate, signer.region)
	kService := testHMAC(kRegion, "s3")
	kSigning := testHMAC(kService, "aws4_request")
	sig := hex.EncodeToString(testHMAC(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		signer.accessKey, scope, signedHeaders, sig))
}

func newTestServerAndSigner(t *testing.T) (*Server, testSigner) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	creds := Credentials{AccessKeyID: "AKIATESTACCESSKEY0001", SecretAccessKey: "TestSecretKeyForZeroS3UnitTests0123456789"}
	srv := NewServer(store, creds, "us-east-1")
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	return srv, signer
}

func mustAuthTestRequest(method, rawTarget string, body []byte) (req *http.Request, rawPath, rawQuery string) {
	req = httptest.NewRequest(method, rawTarget, bytes.NewReader(body))
	rawPath, rawQuery = rawTarget, ""
	if idx := strings.IndexByte(rawTarget, '?'); idx >= 0 {
		rawPath, rawQuery = rawTarget[:idx], rawTarget[idx+1:]
	}
	return req, rawPath, rawQuery
}

func TestSigV4_ValidSignedRequestsAcrossRawPathShapes(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	targets := []string{
		"/mybucket",
		"/mybucket/mykey",
		"/mybucket//double//slash",
		"/mybucket/enc%2Fslash%2Fkey",
		"/mybucket/space%20key",
		"/mybucket/plus+key",
		"/mybucket/trailing/",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			body := []byte("payload-" + target)
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, target, body)
			signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
			if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
				t.Fatalf("expected a validly signed request to be accepted: %v", err)
			}
		})
	}
}

func TestSigV4_QueryEdgeCases(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	targets := []string{
		"/mybucket/key",
		"/mybucket/key?",
		"/mybucket/key?a=1&a=2",
		"/mybucket/key?empty=&b=2&a=1",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			body := []byte("q")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, target, body)
			signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
			if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
				t.Fatalf("expected valid signature to be accepted: %v", err)
			}
		})
	}
}

func TestSigV4_IncorrectSecretRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("x")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	bad := signer
	bad.secretKey = "wrong-secret-entirely"
	signTestRequest(t, req, bad, rawPath, rawQuery, body, time.Now(), nil)

	var ae *authError
	err := srv.authenticate(req, rawPath, rawQuery, body)
	if err == nil || !errors.As(err, &ae) || ae.code != "SignatureDoesNotMatch" {
		t.Fatalf("expected SignatureDoesNotMatch, got %v", err)
	}
}

func TestSigV4_WrongAccessKeyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("x")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	bad := signer
	bad.accessKey = "AKIAWRONGKEYWRONGKEY"
	signTestRequest(t, req, bad, rawPath, rawQuery, body, time.Now(), nil)

	var ae *authError
	err := srv.authenticate(req, rawPath, rawQuery, body)
	if err == nil || !errors.As(err, &ae) || ae.code != "InvalidAccessKeyId" {
		t.Fatalf("expected InvalidAccessKeyId, got %v", err)
	}
}

func TestSigV4_WrongRegionServiceScopeRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	cases := []func(*testSigner){
		func(s *testSigner) { s.region = "eu-west-1" },
	}
	for i, mutate := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			body := []byte("x")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
			bad := signer
			mutate(&bad)
			signTestRequest(t, req, bad, rawPath, rawQuery, body, time.Now(), nil)
			if err := srv.authenticate(req, rawPath, rawQuery, body); err == nil {
				t.Fatalf("expected mismatched scope to be rejected")
			}
		})
	}

	// A well-formed but wrong service name in the credential scope.
	t.Run("wrong-service", func(t *testing.T) {
		body := []byte("x")
		req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
		signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
		auth := req.Header.Get("Authorization")
		auth = strings.Replace(auth, "/s3/aws4_request", "/ec2/aws4_request", 1)
		req.Header.Set("Authorization", auth)
		if err := srv.authenticate(req, rawPath, rawQuery, body); err == nil {
			t.Fatalf("expected wrong service in credential scope to be rejected")
		}
	})
}

func TestSigV4_MissingSignedHeaderRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("x")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{omitContentSha256FromSigned: true})
	if err := srv.authenticate(req, rawPath, rawQuery, body); err == nil {
		t.Fatalf("expected a request missing a required signed header to be rejected")
	}
}

func TestSigV4_AlteredPayloadRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("original body")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)

	tampered := []byte("tampered body, different length and content")
	if err := srv.authenticate(req, rawPath, rawQuery, tampered); err == nil {
		t.Fatalf("expected a request whose body changed after signing to be rejected")
	}
}

func TestSigV4_RawPathIsNotCleaned(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("x")
	target := "/b//k"
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, target, body)

	cleanedPath := "/b/k"
	signTestRequest(t, req, signer, cleanedPath, rawQuery, body, time.Now(), nil)
	if err := srv.authenticate(req, rawPath, rawQuery, body); err == nil {
		t.Fatalf("expected a signature computed over a cleaned path to be rejected for the real, uncleaned raw path")
	}

	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
	if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
		t.Fatalf("expected a signature computed over the true raw path to be accepted: %v", err)
	}
}

// =============================================================================
// CRC32 tests
// =============================================================================

func TestCRC32_ValidAccepted(t *testing.T) {
	body := []byte("crc32 test payload")
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, crc32.ChecksumIEEE(body))
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(b))
	if err := validateCRC32Header(req, body); err != nil {
		t.Fatalf("expected a valid crc32 checksum to be accepted: %v", err)
	}
}

func TestCRC32_InvalidRejected(t *testing.T) {
	body := []byte("crc32 test payload")
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, crc32.ChecksumIEEE(body)+1)
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(b))
	if err := validateCRC32Header(req, body); err == nil {
		t.Fatalf("expected an incorrect crc32 checksum to be rejected")
	}
}

func TestCRC32_FailedChecksumLeavesNoVisibleObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := []byte("this will fail its checksum")
	bad := make([]byte, 4)
	binary.BigEndian.PutUint32(bad, crc32.ChecksumIEEE(body)+1)

	req := mustSignedRequest(t, signer, http.MethodPut, ts.URL+"/bucket1/badkey", body)
	req.Header.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(bad))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a checksum failure to be rejected, got 200")
	}

	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bucket1/badkey", nil, nil)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the object to not exist after a failed checksum, got status %d", getResp.StatusCode)
	}
}

// =============================================================================
// Content-MD5 tests
// =============================================================================

func TestContentMD5_ValidAccepted(t *testing.T) {
	body := []byte("content-md5 test payload")
	sum := md5.Sum(body) //nolint:gosec // test-only use, matching the request-integrity role under test.
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	if err := validateContentMD5Header(req, body); err != nil {
		t.Fatalf("expected a valid Content-MD5 to be accepted: %v", err)
	}
}

func TestContentMD5_MissingHeaderUnchangedBehavior(t *testing.T) {
	body := []byte("no content-md5 header at all")
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	if err := validateContentMD5Header(req, body); err != nil {
		t.Fatalf("expected ordinary PUTs without Content-MD5 to remain unaffected: %v", err)
	}
}

func TestContentMD5_MismatchedRejectedAsBadDigest(t *testing.T) {
	body := []byte("content-md5 test payload")
	wrong := md5.Sum([]byte("a completely different payload")) //nolint:gosec // test-only.
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(wrong[:]))
	err := validateContentMD5Header(req, body)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "BadDigest" {
		t.Fatalf("expected a well-formed but mismatched Content-MD5 to be rejected as BadDigest, got %v", err)
	}
}

func TestContentMD5_MalformedBase64RejectedAsInvalidDigest(t *testing.T) {
	body := []byte("content-md5 test payload")
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("Content-MD5", "not-valid-base64!!!")
	err := validateContentMD5Header(req, body)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "InvalidDigest" {
		t.Fatalf("expected malformed base64 to be rejected as InvalidDigest, got %v", err)
	}
}

func TestContentMD5_WrongLengthDecodedDigestRejectedAsInvalidDigest(t *testing.T) {
	body := []byte("content-md5 test payload")
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	// Valid base64, but it decodes to far fewer than the 16 bytes an MD5
	// digest requires -- must be distinguished from a well-formed digest
	// that simply doesn't match.
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString([]byte("short")))
	err := validateContentMD5Header(req, body)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "InvalidDigest" {
		t.Fatalf("expected a wrong-length decoded digest to be rejected as InvalidDigest, got %v", err)
	}
}

func TestContentMD5_CoexistsWithValidCRC32(t *testing.T) {
	body := []byte("both checksums present and correct")
	sum := md5.Sum(body) //nolint:gosec // test-only.
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc32.ChecksumIEEE(body))
	req := httptest.NewRequest(http.MethodPut, "/b/k", nil)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	req.Header.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(crcBytes))
	if err := validateCRC32Header(req, body); err != nil {
		t.Fatalf("expected valid crc32 to still pass alongside Content-MD5: %v", err)
	}
	if err := validateContentMD5Header(req, body); err != nil {
		t.Fatalf("expected valid Content-MD5 to still pass alongside crc32: %v", err)
	}
}

func TestContentMD5_FailedDigestLeavesNoVisibleObjectOverRealHTTP(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := []byte("this will fail its content-md5 check")
	wrong := md5.Sum([]byte("mismatched payload")) //nolint:gosec // test-only.

	req := mustSignedRequest(t, signer, http.MethodPut, ts.URL+"/bucket1/mdkey", body)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(wrong[:]))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a Content-MD5 mismatch to be rejected, got 200")
	}

	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bucket1/mdkey", nil, nil)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the object to not exist after a failed Content-MD5 check, got status %d", getResp.StatusCode)
	}
}

func TestContentMD5_ValidPUTOverRealHTTPSucceedsAndETagUnaffected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := []byte("ordinary single-part object body for etag comparison")
	sum := md5.Sum(body) //nolint:gosec // test-only.

	req := mustSignedRequest(t, signer, http.MethodPut, ts.URL+"/bucket1/etagkey", body)
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected a valid Content-MD5 PUT to succeed, got status %d", resp.StatusCode)
	}
	wantETag := fmt.Sprintf("%q", hex.EncodeToString(sum[:]))
	if got := resp.Header.Get("ETag"); got != wantETag {
		t.Fatalf("expected ordinary single-part ETag behavior to be unaffected by Content-MD5, got %q want %q", got, wantETag)
	}
}

// =============================================================================
// End-to-end HTTP tests
// =============================================================================

func mustSignedRequest(t *testing.T, signer testSigner, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	signTestRequest(t, req, signer, req.URL.Path, req.URL.RawQuery, body, time.Now(), nil)
	return req
}

func doSignedRequest(t *testing.T, client *http.Client, baseURL string, signer testSigner, method, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req := mustSignedRequest(t, signer, method, baseURL+path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doCreateBucket(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket string) error {
	t.Helper()
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodPut, "/"+bucket, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create bucket failed: %d %s", resp.StatusCode, b)
	}
	return nil
}

func TestE2E_FullLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: "AKIAE2ETESTKEY000001", SecretAccessKey: "E2ETestSecretKeyForZeroS30123456789"}
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	srv := NewServer(store, creds, "us-east-1")
	ts := httptest.NewServer(srv)
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "e2e-bucket"); err != nil {
		t.Fatal(err)
	}

	body := []byte("hello, zeros3! this is the end-to-end payload.")
	putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/e2e-bucket/greeting.txt", body, map[string]string{"Content-Type": "text/plain"})
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", putResp.StatusCode)
	}

	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/e2e-bucket/greeting.txt", nil, nil)
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET failed: %d", getResp.StatusCode)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("GET returned different bytes: %q vs %q", got, body)
	}

	ts.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	srv2 := NewServer(store2, creds, "us-east-1")
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	client2 := ts2.Client()

	getResp2 := doSignedRequest(t, client2, ts2.URL, signer, http.MethodGet, "/e2e-bucket/greeting.txt", nil, nil)
	got2, _ := io.ReadAll(getResp2.Body)
	getResp2.Body.Close()
	if getResp2.StatusCode != http.StatusOK {
		t.Fatalf("GET after restart failed: %d", getResp2.StatusCode)
	}
	if !bytes.Equal(got2, body) {
		t.Fatalf("GET after restart returned different bytes")
	}
}

func TestE2E_ZeroByteObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "zb"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/zb/empty", []byte{}, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("zero-byte PUT failed: %d", put.StatusCode)
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/zb/empty", nil, nil)
	data, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || len(data) != 0 {
		t.Fatalf("expected an empty 200 response, got status=%d len=%d", get.StatusCode, len(data))
	}
}

func TestE2E_BinaryObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "bin"); err != nil {
		t.Fatal(err)
	}
	data := genRandomBytes(777, 3*1024*1024)
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bin/blob", data, map[string]string{"Content-Type": "application/octet-stream"})
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("binary PUT failed: %d", put.StatusCode)
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bin/blob", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("binary object round trip mismatch")
	}
}

func TestE2E_OverwriteThenRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: "AKIAOVERWRITETEST0001", SecretAccessKey: "OverwriteTestSecretKey0123456789AB"}
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	srv := NewServer(store, creds, "us-east-1")
	ts := httptest.NewServer(srv)
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "ow"); err != nil {
		t.Fatal(err)
	}
	r1 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/ow/k", []byte("version-one"), nil)
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first PUT failed: %d", r1.StatusCode)
	}
	r2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/ow/k", []byte("version-two-is-longer"), nil)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("second PUT failed: %d", r2.StatusCode)
	}
	ts.Close()
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, data, err := store2.GetObject("ow", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "version-two-is-longer" {
		t.Fatalf("expected the latest version to survive restart, got %q", data)
	}
}

func TestE2E_FailedPutNeverVisible(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	body := []byte("should never be visible")
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/nosuchbucket/key", body, nil)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a PUT to a nonexistent bucket to fail")
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/nosuchbucket/key", nil, nil)
	get.Body.Close()
	if get.StatusCode == http.StatusOK {
		t.Fatalf("expected the object to remain invisible after a failed PUT")
	}
}

// =============================================================================
// Crash / recovery tests
//
// Each test sets testHook to panic(simulatedCrash{...}) at one durability
// boundary inside the PUT pipeline, runs the operation expecting that
// panic, then discards the in-process Store entirely and opens a FRESH
// one on the same directory -- simulating a process restart -- to check
// exactly what a real crash at that point would leave durable.
// =============================================================================

func withTestHook(t *testing.T, fn func(point string)) {
	t.Helper()
	old := testHook
	testHook = fn
	t.Cleanup(func() { testHook = old })
}

func runExpectingSimulatedCrash(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected the test hook to panic (simulating a crash), but it did not")
		}
		if _, ok := r.(simulatedCrash); !ok {
			panic(r)
		}
	}()
	fn()
}

func TestCrash_DuringChunkStaging(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookBeforeChunkWrite {
			panic(simulatedCrash{point: point})
		}
	})
	runExpectingSimulatedCrash(t, func() {
		_, _ = store.PutObject("b", "k", genRandomBytes(1, 100*1024), "application/octet-stream", nil)
	})
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, _, err := store2.GetObject("b", "k"); err == nil {
		t.Fatalf("object must not be visible after a crash while staging chunks")
	}
}

func TestCrash_AfterChunksBeforeManifest(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterChunksPublished {
			panic(simulatedCrash{point: point})
		}
	})
	runExpectingSimulatedCrash(t, func() {
		_, _ = store.PutObject("b", "k", genRandomBytes(2, 100*1024), "application/octet-stream", nil)
	})
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, _, err := store2.GetObject("b", "k"); err == nil {
		t.Fatalf("object must not be visible after a crash before the manifest commits (orphan chunks are acceptable)")
	}
}

func TestCrash_AfterManifestBeforeJournal(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterManifestPublished {
			panic(simulatedCrash{point: point})
		}
	})
	runExpectingSimulatedCrash(t, func() {
		_, _ = store.PutObject("b", "k", genRandomBytes(3, 100*1024), "application/octet-stream", nil)
	})
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, _, err := store2.GetObject("b", "k"); err == nil {
		t.Fatalf("object must not be visible after a crash before the journal append (orphan manifest is acceptable)")
	}
}

func TestCrash_DuringPartialJournalFrame(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("b", "k1", []byte("committed-before-crash"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	pieces, err := chunkData(bytes.NewReader([]byte("never-committed")))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pieces {
		if _, err := store.casWrite(p.data); err != nil {
			t.Fatal(err)
		}
	}
	man, err := buildManifestV1(pieces, []byte("never-committed"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.publishManifest(man); err != nil {
		t.Fatal(err)
	}

	fullOffsetBefore := store.journal.writeOffset
	payload := []byte(`{"bucket":"b","key":"k2","manifest_uuid":"x","manifest_sha256":"` + strings.Repeat("0", 64) + `","size":0,"etag":"x","content_type":"text/plain","version_id":"x"}`)
	if _, err := store.journal.appendFrame(recordTypePutObjectRoot, payload); err != nil {
		t.Fatal(err)
	}
	fullOffsetAfter := store.journal.writeOffset
	store.Close()

	journalPath := filepath.Join(dir, "journal", "visibility.log")
	tornSize := fullOffsetBefore + (fullOffsetAfter-fullOffsetBefore)/2
	if err := os.Truncate(journalPath, tornSize); err != nil {
		t.Fatal(err)
	}

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("expected a torn tail to be tolerated on reopen, got: %v", err)
	}
	defer store2.Close()
	if _, _, err := store2.GetObject("b", "k1"); err != nil {
		t.Fatalf("expected the earlier, fully-committed object to survive: %v", err)
	}
	if _, _, err := store2.GetObject("b", "k2"); err == nil {
		t.Fatalf("expected the torn, never-synced record to not be visible")
	}
}

// TestCrash_AfterJournalWriteBeforeSync targets the gap between the raw
// Write() of a journal frame and the fsync that makes it durable. A real
// crash in that gap is not reproducible in-process: once WriteAt returns,
// the bytes sit in the same page cache a fresh *os.File in this same
// process would read right back, sync or no sync -- fsync only matters
// for surviving a real power loss, which nothing here can simulate (see
// TestCrash_SyncFailureRecoveryIsHonest for what IS honestly testable
// about that gap on restart).
//
// What this test covers is the in-process consequence of a failed sync:
// it must never be treated as a commit (PutObject reports failure, the
// append cursor does not advance so a later append can't skip a sequence
// number or write at a stale offset), and the journal must be poisoned so
// this same process can't paper over the uncertainty by just trying
// again -- a further mutation must be rejected outright, without ever
// touching the file, until the store is reopened.
func TestCrash_AfterJournalWriteBeforeSync(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	offsetBefore := store.journal.writeOffset
	seqBefore := store.journal.nextSeq

	withTestHook(t, func(point string) {
		if point == hookAfterJournalWriteBeforeSync {
			store.journal.f.Close()
		}
	})
	_, err = store.PutObject("b", "k", []byte("should never be considered committed"), "text/plain", nil)
	testHook = nil
	if err == nil {
		t.Fatalf("expected PutObject to fail when the journal sync fails")
	}
	if store.journal.writeOffset != offsetBefore || store.journal.nextSeq != seqBefore {
		t.Fatalf("journal append cursor advanced despite a failed sync: offset %d->%d seq %d->%d",
			offsetBefore, store.journal.writeOffset, seqBefore, store.journal.nextSeq)
	}

	store.journal.mu.Lock()
	poisoned := store.journal.poisoned
	store.journal.mu.Unlock()
	if poisoned == nil {
		t.Fatalf("expected the journal to be poisoned after a sync failure")
	}

	// The poisoned journal must reject further mutations in this process
	// without appending another record or moving the cursor, even for an
	// otherwise-unrelated bucket.
	if err := store.CreateBucket("after-sync-failure"); err == nil {
		t.Fatalf("expected a mutation after a sync failure to be rejected by the poisoned journal")
	}
	if store.journal.writeOffset != offsetBefore || store.journal.nextSeq != seqBefore {
		t.Fatalf("journal append cursor moved on a mutation that should have been rejected outright")
	}
}

// TestJournal_PoisonedAfterWriteFailure covers the write-failure half of
// the same contract: if the raw WriteAt() itself fails (not just the
// later Sync()), the journal must poison the same way.
func TestJournal_PoisonedAfterWriteFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("b", "k1", []byte("prior-committed"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	offsetBefore := store.journal.writeOffset
	seqBefore := store.journal.nextSeq

	// Close the journal's file out from under it so the next WriteAt
	// itself fails (not merely the subsequent Sync).
	if err := store.journal.f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = store.PutObject("b", "k2", []byte("never committed"), "text/plain", nil)
	if err == nil {
		t.Fatalf("expected PutObject to fail when the journal write fails")
	}

	store.journal.mu.Lock()
	poisoned := store.journal.poisoned
	store.journal.mu.Unlock()
	if poisoned == nil {
		t.Fatalf("expected the journal to be poisoned after a write failure")
	}
	if store.journal.writeOffset != offsetBefore || store.journal.nextSeq != seqBefore {
		t.Fatalf("journal append cursor advanced despite a failed write: offset %d->%d seq %d->%d",
			offsetBefore, store.journal.writeOffset, seqBefore, store.journal.nextSeq)
	}

	// A further mutation, even to a different bucket, must be rejected
	// without appending another record or advancing sequence/offset.
	if err := store.CreateBucket("after-write-failure"); err == nil {
		t.Fatalf("expected a mutation after a write failure to be rejected by the poisoned journal")
	}
	if store.journal.writeOffset != offsetBefore || store.journal.nextSeq != seqBefore {
		t.Fatalf("journal append cursor moved on a mutation that should have been rejected outright")
	}

	// Reopening (the only sanctioned way out of the poisoned state) must
	// show exactly the prior committed state and nothing from either
	// failed/rejected attempt: no partial state, and no k2/after-write-failure.
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, ok := store2.buckets["after-write-failure"]; ok {
		t.Fatalf("bucket from a rejected, poisoned-journal mutation must not exist after reopen")
	}
	if _, _, err := store2.GetObject("b", "k1"); err != nil {
		t.Fatalf("expected the prior committed object to survive: %v", err)
	}
	if _, _, err := store2.GetObject("b", "k2"); err == nil {
		t.Fatalf("k2 must not be visible: its journal write failed")
	}
}

// TestCrash_SyncFailureRecoveryIsHonest exercises the real, documented
// durability contract for the write-succeeded-but-sync-failed gap: a
// crash/failure there leaves durability genuinely indeterminate, so a
// fresh open of the store afterward may legitimately observe EITHER the
// previous complete state or the new complete state (whichever bytes
// actually made it to durable storage) -- but never anything partial or
// mixed. This test deliberately does NOT assert that the unsynced write
// vanishes, because nothing in-process can prove that one way or the
// other (see the comment on TestCrash_AfterJournalWriteBeforeSync).
func TestCrash_SyncFailureRecoveryIsHonest(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("b", "k1", []byte("prior-committed"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterJournalWriteBeforeSync {
			store.journal.f.Close()
		}
	})
	_, err = store.PutObject("b", "k2", []byte("uncertain-durability"), "text/plain", nil)
	testHook = nil
	if err == nil {
		t.Fatalf("a mutation whose journal sync fails must never be acknowledged as successful")
	}

	// This process must refuse to continue mutating against the now-
	// uncertain journal state.
	if err := store.CreateBucket("after-failure"); err == nil {
		t.Fatalf("expected the poisoned journal to reject further mutations in this process")
	}

	// A fresh open (standing in for a real restart) replays whatever is
	// actually, durably on disk. Per the corrected contract, either
	// outcome below is legitimate.
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("replay must not fail outright over an unsynced-but-possibly-complete frame: %v", err)
	}
	defer store2.Close()

	_, k1Data, err := store2.GetObject("b", "k1")
	if err != nil {
		t.Fatalf("the previously committed object must always survive: %v", err)
	}
	if string(k1Data) != "prior-committed" {
		t.Fatalf("k1 content corrupted: %q", k1Data)
	}

	_, k2Data, err := store2.GetObject("b", "k2")
	switch {
	case err != nil:
		// Legitimate: the unsynced frame did not survive, so only the
		// old state is visible. This must be an ordinary "not found",
		// not some other corruption-shaped error.
		if !errors.Is(err, errNoSuchKey) {
			t.Fatalf("expected a clean not-found for k2 if it didn't survive, got: %v", err)
		}
	case err == nil:
		// Also legitimate: the frame happened to reach durable storage
		// anyway. If so, it must be the COMPLETE object, never a
		// partial or corrupted one.
		if string(k2Data) != "uncertain-durability" {
			t.Fatalf("k2 survived but with wrong/partial content: %q", k2Data)
		}
	}
}

func TestCrash_AfterJournalSyncBeforeResponse(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterJournalSync {
			panic(simulatedCrash{point: point})
		}
	})
	runExpectingSimulatedCrash(t, func() {
		_, _ = store.PutObject("b", "k", []byte("committed-at-sync"), "text/plain", nil)
	})
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, data, err := store2.GetObject("b", "k")
	if err != nil {
		t.Fatalf("expected the object to be visible after restart: once the journal is synced the commit is final, even if the process died before applying it in memory or responding: %v", err)
	}
	if string(data) != "committed-at-sync" {
		t.Fatalf("unexpected data: %q", data)
	}
}

func TestCrash_AfterAck(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: "AKIAAFTERACKTEST00001", SecretAccessKey: "AfterAckTestSecretKey0123456789ABCDE"}
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	srv := NewServer(store, creds, "us-east-1")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterAck {
			panic(simulatedCrash{point: point})
		}
	})
	func() {
		defer func() { _ = recover() }()
		req := mustSignedRequest(t, signer, http.MethodPut, ts.URL+"/b/k", []byte("acked-data"))
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, data, err := store2.GetObject("b", "k")
	if err != nil || string(data) != "acked-data" {
		t.Fatalf("expected data committed before ack to survive regardless of a crash right after: data=%q err=%v", data, err)
	}
}

// =============================================================================
// M2: Bucket tests -- ListBuckets, HeadBucket, DeleteBucket
// =============================================================================

func doDeleteBucket(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket string) *http.Response {
	t.Helper()
	return doSignedRequest(t, client, baseURL, signer, http.MethodDelete, "/"+bucket, nil, nil)
}

func doHeadBucket(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket string) *http.Response {
	t.Helper()
	return doSignedRequest(t, client, baseURL, signer, http.MethodHead, "/"+bucket, nil, nil)
}

func TestListBuckets_EmptyNonEmptyOrder(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for ListBuckets on an empty store, got %d", resp.StatusCode)
	}
	var empty listAllMyBucketsResult
	if err := xml.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(empty.Buckets) != 0 {
		t.Fatalf("expected no buckets, got %+v", empty.Buckets)
	}

	for _, name := range []string{"zzz-bucket", "aaa-bucket", "mmm-bucket"} {
		if err := doCreateBucket(t, client, ts.URL, signer, name); err != nil {
			t.Fatal(err)
		}
	}
	resp2 := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/", nil, nil)
	var result listAllMyBucketsResult
	if err := xml.NewDecoder(resp2.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if len(result.Buckets) != 3 {
		t.Fatalf("expected 3 buckets, got %d: %+v", len(result.Buckets), result.Buckets)
	}
	want := []string{"aaa-bucket", "mmm-bucket", "zzz-bucket"}
	for i, b := range result.Buckets {
		if b.Name != want[i] {
			t.Fatalf("expected deterministic sorted order %v, got %+v", want, result.Buckets)
		}
		if b.CreationDate == "" {
			t.Fatalf("expected a non-empty CreationDate for bucket %q", b.Name)
		}
	}
}

func TestHeadBucket_PresentMissing(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "present"); err != nil {
		t.Fatal(err)
	}
	resp := doHeadBucket(t, client, ts.URL, signer, "present")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for an existing bucket, got %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("expected no body on a HEAD response, got %q", body)
	}

	resp2 := doHeadBucket(t, client, ts.URL, signer, "missing-bucket")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing bucket, got %d", resp2.StatusCode)
	}
	if len(body2) != 0 {
		t.Fatalf("expected no XML body on a HEAD failure, got %q", body2)
	}
}

func TestDeleteBucket_EmptySucceeds(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "gone"); err != nil {
		t.Fatal(err)
	}
	resp := doDeleteBucket(t, client, ts.URL, signer, "gone")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for deleting an empty bucket, got %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("expected no body on a 204, got %q", body)
	}
	head := doHeadBucket(t, client, ts.URL, signer, "gone")
	head.Body.Close()
	if head.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the bucket to be gone, got %d", head.StatusCode)
	}
}

func TestDeleteBucket_MissingIsNoSuchBucket(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	resp := doDeleteBucket(t, client, ts.URL, signer, "never-existed")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != "NoSuchBucket" {
		t.Fatalf("expected NoSuchBucket, got %q", errBody.Code)
	}
}

func TestDeleteBucket_NonEmptyFails(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "full"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/full/key", []byte("x"), nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", put.StatusCode)
	}

	resp := doDeleteBucket(t, client, ts.URL, signer, "full")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected a conflict status for a non-empty bucket, got %d: %s", resp.StatusCode, body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != "BucketNotEmpty" {
		t.Fatalf("expected BucketNotEmpty, got %q", errBody.Code)
	}
	head := doHeadBucket(t, client, ts.URL, signer, "full")
	head.Body.Close()
	if head.StatusCode != http.StatusOK {
		t.Fatalf("expected the bucket to still exist, got %d", head.StatusCode)
	}
}

func TestDeleteBucket_SurvivesRestartAndRecreate(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b"))
	mustOK(t, s.DeleteBucket("b"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.HeadBucket("b"); !errors.Is(err, errNoSuchBucket) {
		t.Fatalf("expected the bucket to remain absent after restart, got %v", err)
	}

	// A deleted bucket name can only ever be recreated empty: DeleteBucket
	// refuses a non-empty bucket, so there is no prior-object vestige a
	// recreate could resurrect.
	mustOK(t, s2.CreateBucket("b"))
	if err := s2.HeadBucket("b"); err != nil {
		t.Fatalf("expected the recreated bucket to exist: %v", err)
	}
	page, err := s2.ListObjectsV2("b", "", "", "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.contents) != 0 {
		t.Fatalf("expected the recreated bucket to start empty, got %d objects", len(page.contents))
	}
	s2.Close()
}

// =============================================================================
// M2: Object tests -- HeadObject, DeleteObject, metadata/Content-Type
// =============================================================================

func doHeadObject(t *testing.T, client *http.Client, baseURL string, signer testSigner, path string) *http.Response {
	t.Helper()
	return doSignedRequest(t, client, baseURL, signer, http.MethodHead, path, nil, nil)
}

func doDeleteObject(t *testing.T, client *http.Client, baseURL string, signer testSigner, path string) *http.Response {
	t.Helper()
	return doSignedRequest(t, client, baseURL, signer, http.MethodDelete, path, nil, nil)
}

func TestHeadObject_HeadersNoBody(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	body := []byte("head me")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", body, map[string]string{
		"Content-Type":     "text/plain; charset=utf-8",
		"x-amz-meta-owner": "alice",
	})
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", put.StatusCode)
	}

	resp := doHeadObject(t, client, ts.URL, signer, "/b/k")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(got) != 0 {
		t.Fatalf("expected no body on HEAD, got %q", got)
	}
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("unexpected Content-Length: %q", resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected Content-Type: %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatalf("expected an ETag header")
	}
	if resp.Header.Get("x-amz-meta-owner") != "alice" {
		t.Fatalf("expected x-amz-meta-owner to round-trip, got %q", resp.Header.Get("x-amz-meta-owner"))
	}
}

func TestHeadObject_Missing(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	resp := doHeadObject(t, client, ts.URL, signer, "/b/nope")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing key, got %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("expected no body, got %q", body)
	}

	resp2 := doHeadObject(t, client, ts.URL, signer, "/missing-bucket/nope")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing bucket, got %d", resp2.StatusCode)
	}
	if len(body2) != 0 {
		t.Fatalf("expected no body, got %q", body2)
	}
}

func TestGetObject_MetadataAndContentTypeRoundTrip(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("payload"), map[string]string{
		"Content-Type":       "application/json",
		"x-amz-meta-project": "zeros3",
		"x-amz-meta-Stage":   "m2",
	})
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", put.StatusCode)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	data, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET failed: %d", get.StatusCode)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected body: %q", data)
	}
	if get.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected Content-Type: %q", get.Header.Get("Content-Type"))
	}
	if get.Header.Get("x-amz-meta-project") != "zeros3" {
		t.Fatalf("expected x-amz-meta-project to round trip, got %q", get.Header.Get("x-amz-meta-project"))
	}
	// HTTP header names are case-insensitive; metadata keys are lowercased
	// on PUT (matching how x-amz-meta-* headers are parsed), so this must
	// still be retrievable under its lowercase form.
	if get.Header.Get("x-amz-meta-stage") != "m2" {
		t.Fatalf("expected x-amz-meta-stage to round trip, got %q", get.Header.Get("x-amz-meta-stage"))
	}
}

func TestDeleteObject_ExistingThenMissingIdempotent(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("x"), nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", put.StatusCode)
	}

	del := doDeleteObject(t, client, ts.URL, signer, "/b/k")
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", del.StatusCode)
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the object to be gone, got %d", get.StatusCode)
	}

	del2 := doDeleteObject(t, client, ts.URL, signer, "/b/k")
	del2.Body.Close()
	if del2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected idempotent 204 for an already-deleted key, got %d", del2.StatusCode)
	}

	del3 := doDeleteObject(t, client, ts.URL, signer, "/b/never-existed")
	del3.Body.Close()
	if del3.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for a never-existed key, got %d", del3.StatusCode)
	}
}

func TestDeleteObject_MissingBucketErrors(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	resp := doDeleteObject(t, client, ts.URL, signer, "/nope/key")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected NoSuchBucket 404, got %d", resp.StatusCode)
	}
}

func TestDeleteObject_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b"))
	if _, err := s.PutObject("b", "k", []byte("v"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.DeleteObject("b", "k"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, _, err := s2.GetObject("b", "k"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected the key to remain absent after restart, got %v", err)
	}
}

func TestDeleteObject_SharedChunksRemainReadableThroughAnotherObject(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	payload := genRandomBytes(4242, 200*1024) // large enough to span several CDC chunks
	if _, err := s.PutObject("b", "one", payload, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "two", payload, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	mustOK(t, s.DeleteObject("b", "one"))
	if _, _, err := s.GetObject("b", "one"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected 'one' to be gone, got %v", err)
	}
	_, data, err := s.GetObject("b", "two")
	if err != nil {
		t.Fatalf("expected 'two' to remain readable through its shared chunks: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("data for 'two' was corrupted by deleting 'one'")
	}
}

// =============================================================================
// M2: ListObjectsV2 tests
// =============================================================================

func doListObjectsV2(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, query string) *listBucketResult {
	t.Helper()
	path := "/" + bucket + "?list-type=2"
	if query != "" {
		path += "&" + query
	}
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodGet, path, nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ListObjectsV2 %s failed: %d: %s", path, resp.StatusCode, body)
	}
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse ListObjectsV2 XML: %v\nbody: %s", err, body)
	}
	return &result
}

func TestListObjectsV2_Empty(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "empty"); err != nil {
		t.Fatal(err)
	}

	result := doListObjectsV2(t, client, ts.URL, signer, "empty", "")
	if result.KeyCount != 0 || result.IsTruncated || len(result.Contents) != 0 {
		t.Fatalf("expected an empty listing, got %+v", result)
	}
	if result.Name != "empty" {
		t.Fatalf("expected Name to echo the bucket, got %q", result.Name)
	}
	if result.MaxKeys != 1000 {
		t.Fatalf("expected default MaxKeys of 1000, got %d", result.MaxKeys)
	}
}

func TestListObjectsV2_LexicalOrderingAndUnicode(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "ord"); err != nil {
		t.Fatal(err)
	}

	keys := []string{"banana", "Apple", "apple", "zebra", "résumé", "日本語", "a b c", "10", "2"}
	for _, k := range keys {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/ord/"+url.PathEscape(k), []byte("v"), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %q failed: %d", k, resp.StatusCode)
		}
	}
	want := append([]string{}, keys...)
	sort.Strings(want)

	result := doListObjectsV2(t, client, ts.URL, signer, "ord", "")
	if len(result.Contents) != len(want) {
		t.Fatalf("expected %d keys, got %d: %+v", len(want), len(result.Contents), result.Contents)
	}
	for i, c := range result.Contents {
		if c.Key != want[i] {
			t.Fatalf("expected UTF-8 byte lexical order at index %d: got %q want %q", i, c.Key, want[i])
		}
	}
}

func TestListObjectsV2_XMLEscaping(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "esc"); err != nil {
		t.Fatal(err)
	}

	key := `weird&key<with>some"xml'chars`
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/esc/"+url.PathEscape(key), []byte("v"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", resp.StatusCode)
	}

	result := doListObjectsV2(t, client, ts.URL, signer, "esc", "")
	if len(result.Contents) != 1 || result.Contents[0].Key != key {
		t.Fatalf("expected an XML-special key to round trip exactly, got %+v", result.Contents)
	}
}

func TestListObjectsV2_PrefixDelimiterCommonPrefixes(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "tree"); err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"photos/2021/a.jpg",
		"photos/2021/b.jpg",
		"photos/2022/c.jpg",
		"photos/index.html",
		"videos/a.mp4",
		"readme.txt",
	}
	for _, k := range keys {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/tree/"+k, []byte("v"), nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %q failed: %d", k, resp.StatusCode)
		}
	}

	result := doListObjectsV2(t, client, ts.URL, signer, "tree", "prefix=photos%2F&delimiter=%2F")
	if len(result.Contents) != 1 || result.Contents[0].Key != "photos/index.html" {
		t.Fatalf("expected exactly photos/index.html as a direct Content entry, got %+v", result.Contents)
	}
	if len(result.CommonPrefixes) != 2 {
		t.Fatalf("expected two common prefixes, got %+v", result.CommonPrefixes)
	}
	gotCP := []string{result.CommonPrefixes[0].Prefix, result.CommonPrefixes[1].Prefix}
	sort.Strings(gotCP)
	want := []string{"photos/2021/", "photos/2022/"}
	for i := range want {
		if gotCP[i] != want[i] {
			t.Fatalf("expected common prefixes %v, got %v", want, gotCP)
		}
	}
	if result.KeyCount != 3 {
		t.Fatalf("expected KeyCount 3 (1 content + 2 common prefixes), got %d", result.KeyCount)
	}
	if result.Prefix != "photos/" || result.Delimiter != "/" {
		t.Fatalf("expected Prefix/Delimiter to be echoed, got Prefix=%q Delimiter=%q", result.Prefix, result.Delimiter)
	}
}

func TestListObjectsV2_MaxKeysZero(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "mk0"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/mk0/"+k, []byte("v"), nil)
		resp.Body.Close()
	}
	result := doListObjectsV2(t, client, ts.URL, signer, "mk0", "max-keys=0")
	if result.KeyCount != 0 || result.IsTruncated || len(result.Contents) != 0 {
		t.Fatalf("expected an empty, non-truncated result for max-keys=0, got %+v", result)
	}
	if result.MaxKeys != 0 {
		t.Fatalf("expected MaxKeys echoed as 0, got %d", result.MaxKeys)
	}
}

func TestListObjectsV2_MaxKeysOnePagination(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "mk1"); err != nil {
		t.Fatal(err)
	}
	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/mk1/"+k, []byte("v"), nil)
		resp.Body.Close()
	}

	var seen []string
	token := ""
	for i := 0; i < len(keys)+1; i++ {
		q := "max-keys=1"
		if token != "" {
			q += "&continuation-token=" + url.QueryEscape(token)
		}
		result := doListObjectsV2(t, client, ts.URL, signer, "mk1", q)
		if len(result.Contents) > 1 {
			t.Fatalf("expected at most one Content per page, got %+v", result.Contents)
		}
		for _, c := range result.Contents {
			seen = append(seen, c.Key)
		}
		if !result.IsTruncated {
			break
		}
		if result.NextContinuationToken == "" {
			t.Fatalf("expected a NextContinuationToken while truncated")
		}
		token = result.NextContinuationToken
	}
	if len(seen) != len(keys) {
		t.Fatalf("expected to see all %d keys across pages exactly once, got %v", len(keys), seen)
	}
	for i, k := range keys {
		if seen[i] != k {
			t.Fatalf("expected page order %v, got %v", keys, seen)
		}
	}
}

func TestListObjectsV2_DefaultAndLargeMaxKeysClamped(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "big"); err != nil {
		t.Fatal(err)
	}

	resultDefault := doListObjectsV2(t, client, ts.URL, signer, "big", "")
	if resultDefault.MaxKeys != 1000 {
		t.Fatalf("expected default MaxKeys of 1000, got %d", resultDefault.MaxKeys)
	}
	resultLarge := doListObjectsV2(t, client, ts.URL, signer, "big", "max-keys=5000")
	if resultLarge.MaxKeys != 1000 {
		t.Fatalf("expected max-keys to be clamped to 1000, got %d", resultLarge.MaxKeys)
	}
}

func TestListObjectsV2_InvalidContinuationToken(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bad-token"); err != nil {
		t.Fatal(err)
	}

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bad-token?list-type=2&continuation-token=not-valid-base64%21%21", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected an invalid continuation token to be rejected, got 200: %s", body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "InvalidArgument" {
		t.Fatalf("expected InvalidArgument, got %q", errBody.Code)
	}
}

func TestListObjectsV2_PaginationNoDuplicateOrSkipWithDelimiter(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "pg"); err != nil {
		t.Fatal(err)
	}

	keys := []string{"a", "dir1/1", "dir1/2", "dir1/3", "dir2/1", "dir2/2", "z"}
	for _, k := range keys {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/pg/"+k, []byte("v"), nil)
		resp.Body.Close()
	}

	var allKeys, allPrefixes []string
	token := ""
	for i := 0; i < 20; i++ {
		q := "delimiter=%2F&max-keys=1"
		if token != "" {
			q += "&continuation-token=" + url.QueryEscape(token)
		}
		result := doListObjectsV2(t, client, ts.URL, signer, "pg", q)
		for _, c := range result.Contents {
			allKeys = append(allKeys, c.Key)
		}
		for _, cp := range result.CommonPrefixes {
			allPrefixes = append(allPrefixes, cp.Prefix)
		}
		if !result.IsTruncated {
			break
		}
		token = result.NextContinuationToken
	}
	wantKeys := []string{"a", "z"}
	wantPrefixes := []string{"dir1/", "dir2/"}
	if len(allKeys) != len(wantKeys) {
		t.Fatalf("expected keys %v, got %v", wantKeys, allKeys)
	}
	for i := range wantKeys {
		if allKeys[i] != wantKeys[i] {
			t.Fatalf("expected keys %v, got %v", wantKeys, allKeys)
		}
	}
	if len(allPrefixes) != len(wantPrefixes) {
		t.Fatalf("expected prefixes %v, got %v", wantPrefixes, allPrefixes)
	}
	for i := range wantPrefixes {
		if allPrefixes[i] != wantPrefixes[i] {
			t.Fatalf("expected prefixes %v, got %v", wantPrefixes, allPrefixes)
		}
	}
}

// =============================================================================
// M2: Journal tests -- delete-object-root / delete-bucket replay
// =============================================================================

func TestJournal_DeleteObjectRootReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b"))
	if _, err := s.PutObject("b", "k", []byte("v"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.DeleteObject("b", "k"))
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, _, err := s2.GetObject("b", "k"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected delete-object-root to replay correctly, got %v", err)
	}
}

func TestJournal_DeleteBucketReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b"))
	mustOK(t, s.DeleteBucket("b"))
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.HeadBucket("b"); !errors.Is(err, errNoSuchBucket) {
		t.Fatalf("expected delete-bucket to replay correctly, got %v", err)
	}
}

func TestJournal_MixedCreatePutDeleteRecreateSequence(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.CreateBucket("b"))
	if _, err := s.PutObject("b", "k1", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.DeleteObject("b", "k1"))
	if _, err := s.PutObject("b", "k1", []byte("v1-again"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k2", []byte("v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s.DeleteObject("b", "k2"))
	mustOK(t, s.DeleteObject("b", "k1"))
	mustOK(t, s.DeleteBucket("b"))
	mustOK(t, s.CreateBucket("b"))
	if _, err := s.PutObject("b", "k3", []byte("fresh"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, _, err := s2.GetObject("b", "k1"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected k1 to be absent, got %v", err)
	}
	if _, _, err := s2.GetObject("b", "k2"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected k2 to be absent, got %v", err)
	}
	_, data, err := s2.GetObject("b", "k3")
	if err != nil || string(data) != "fresh" {
		t.Fatalf("expected k3 to survive the mixed sequence and restart, got %q err=%v", data, err)
	}
}

// =============================================================================
// M2: Concurrency tests
//
// Concurrency policy (also recorded in STATUS.md): Store.mu is the single
// writer lock for the visible bucket/object namespace. Every mutation that
// changes what's visible -- CreateBucket, DeleteBucket, DeleteObject, and
// the final journal-append+namespace-apply step of PutObject -- holds
// Store.mu across both its journal append and its namespace update, so the
// two always happen atomically together and journal sequence order always
// matches namespace-apply order. The slow, CPU/IO-heavy part of PutObject
// (CDC chunking, CAS writes, manifest publication) runs *without* holding
// Store.mu, so multiple PUTs can prepare their data in parallel; only the
// brief final commit is serialized, and it re-checks bucket existence
// under the same lock (see PutObject) so a DeleteBucket racing a PutObject
// can never leave a nil bucket entry half-written into. Readers
// (GetObject/HeadObject/ListObjectsV2) take Store.mu only for their
// namespace lookup/snapshot, then read immutable manifest/chunk data
// afterward without holding it -- safe because manifests and chunks are
// never mutated in place, only ever superseded by a new root that a
// concurrent writer publishes under its own fresh UUID/digest.
//
// Required invariant, exercised below: a reader observes a complete old
// object or a complete new object for a given key, never a mix of the two.
// =============================================================================

func TestConcurrency_SameKeyPutPutSerializesToOneCompleteVersion(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "race"); err != nil {
		t.Fatal(err)
	}

	const n = 20
	versions := make([][]byte, n)
	for i := range versions {
		versions[i] = bytes.Repeat([]byte{byte('A' + i)}, 50000+i)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/race/key", versions[i], nil)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/race/key", nil, nil)
	data, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("expected the final GET to succeed, got %d", get.StatusCode)
	}
	matched := false
	for _, v := range versions {
		if bytes.Equal(v, data) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("expected the final object to be exactly one complete written version, got %d bytes matching none", len(data))
	}
}

func TestConcurrency_SameKeyPutVsDeleteNeverMixed(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "race2"); err != nil {
		t.Fatal(err)
	}

	body := []byte("initial-version-full-object")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/race2/key", body, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("initial PUT failed: %d", put.StatusCode)
	}

	newBody := bytes.Repeat([]byte("X"), 60000)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/race2/key", newBody, nil)
			resp.Body.Close()
		}()
		go func() {
			defer wg.Done()
			resp := doDeleteObject(t, client, ts.URL, signer, "/race2/key")
			resp.Body.Close()
		}()
	}
	wg.Wait()

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/race2/key", nil, nil)
	data, _ := io.ReadAll(get.Body)
	get.Body.Close()
	switch get.StatusCode {
	case http.StatusOK:
		if !bytes.Equal(data, newBody) && !bytes.Equal(data, body) {
			t.Fatalf("expected a complete old or complete new object, got %d unrecognized bytes", len(data))
		}
	case http.StatusNotFound:
		// Also coherent: DELETE was the last winning mutation.
	default:
		t.Fatalf("expected a coherent object or NoSuchKey, got status %d", get.StatusCode)
	}
}

func TestConcurrency_GetDuringOverwriteSeesCompleteObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "race3"); err != nil {
		t.Fatal(err)
	}

	v1 := bytes.Repeat([]byte("1"), 80000)
	v2 := bytes.Repeat([]byte("2"), 90000)
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/race3/key", v1, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("initial PUT failed: %d", put.StatusCode)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/race3/key", v2, nil)
			resp.Body.Close()
		}
	}()

	for i := 0; i < 50; i++ {
		get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/race3/key", nil, nil)
		data, _ := io.ReadAll(get.Body)
		get.Body.Close()
		if get.StatusCode != http.StatusOK {
			t.Fatalf("expected GET to succeed during a concurrent overwrite, got %d", get.StatusCode)
		}
		if !bytes.Equal(data, v1) && !bytes.Equal(data, v2) {
			t.Fatalf("observed a torn/mixed object during a concurrent overwrite: %d bytes", len(data))
		}
	}
	close(stop)
	wg.Wait()
}

func TestConcurrency_DeleteBucketVsPutObjectResolvesCoherently(t *testing.T) {
	for trial := 0; trial < 20; trial++ {
		srv, signer := newTestServerAndSigner(t)
		ts := httptest.NewServer(srv)
		client := ts.Client()
		if err := doCreateBucket(t, client, ts.URL, signer, "racebucket"); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/racebucket/k", []byte("v"), nil)
			resp.Body.Close()
		}()
		go func() {
			defer wg.Done()
			resp := doDeleteBucket(t, client, ts.URL, signer, "racebucket")
			resp.Body.Close()
		}()
		wg.Wait()

		// Whichever operation won, the store must land in one coherent
		// state: either the bucket is gone (DeleteBucket won; the racing
		// PUT must have failed with NoSuchBucket rather than corrupting
		// the namespace map), or the bucket exists with its object
		// visible (PutObject won; DeleteBucket must have failed with
		// BucketNotEmpty rather than leaving a bucket without its
		// object).
		head := doHeadBucket(t, client, ts.URL, signer, "racebucket")
		head.Body.Close()
		switch head.StatusCode {
		case http.StatusNotFound:
			// DeleteBucket won; nothing further to check.
		case http.StatusOK:
			get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/racebucket/k", nil, nil)
			get.Body.Close()
			if get.StatusCode != http.StatusOK {
				t.Fatalf("bucket exists but its object is missing: status %d", get.StatusCode)
			}
		default:
			t.Fatalf("unexpected HeadBucket status %d", head.StatusCode)
		}
		ts.Close()
	}
}

// =============================================================================
// M3: CDC/dedup evidence (Store + stats level)
//
// Frozen CDC v1 parameters are unchanged; these tests exercise the full
// Store+stats pipeline with real PutObject calls to produce concrete,
// measured dedup evidence -- every asserted value is read back from
// computeStats, never an invented number, and every measured value is
// also logged via t.Logf for a readable trail.
// =============================================================================

func TestDedup_IdenticalObjectReuseAcrossKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	body := genRandomBytes(9001, 6*1024*1024) // several MB: many CDC chunks
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PutObject("b", "first", body, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	after1, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.PutObject("b", "second", body, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	after2, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("after 1 copy:  logical=%d refs=%d(%d bytes) unique=%d(%d bytes) chunk-file-bytes=%d",
		after1.LogicalCurrentBytes, after1.LogicalChunkReferenceCount, after1.LogicalChunkReferenceBytes,
		after1.ScopeUniqueChunkCount, after1.ScopeUniqueChunkBytes, after1.ChunkStoreFileBytes)
	t.Logf("after 2 copies: logical=%d refs=%d(%d bytes) unique=%d(%d bytes) chunk-file-bytes=%d dedup_avoided=%d reduction=%.1f%%",
		after2.LogicalCurrentBytes, after2.LogicalChunkReferenceCount, after2.LogicalChunkReferenceBytes,
		after2.ScopeUniqueChunkCount, after2.ScopeUniqueChunkBytes, after2.ChunkStoreFileBytes,
		after2.DedupAvoidedBytes, after2.DedupReduction*100)

	if after2.LogicalCurrentBytes != 2*after1.LogicalCurrentBytes {
		t.Fatalf("expected logical current bytes to double, got %d vs %d", after2.LogicalCurrentBytes, after1.LogicalCurrentBytes)
	}
	if after2.LogicalChunkReferenceCount != 2*after1.LogicalChunkReferenceCount {
		t.Fatalf("expected chunk reference count to double, got %d vs %d", after2.LogicalChunkReferenceCount, after1.LogicalChunkReferenceCount)
	}
	if after2.LogicalChunkReferenceBytes != 2*after1.LogicalChunkReferenceBytes {
		t.Fatalf("expected chunk reference bytes to double, got %d vs %d", after2.LogicalChunkReferenceBytes, after1.LogicalChunkReferenceBytes)
	}
	if after2.ScopeUniqueChunkBytes != after1.ScopeUniqueChunkBytes {
		t.Fatalf("expected unique chunk payload bytes unchanged after an identical second copy, got %d vs %d", after2.ScopeUniqueChunkBytes, after1.ScopeUniqueChunkBytes)
	}
	if after2.ScopeUniqueChunkCount != after1.ScopeUniqueChunkCount {
		t.Fatalf("expected unique chunk count unchanged, got %d vs %d", after2.ScopeUniqueChunkCount, after1.ScopeUniqueChunkCount)
	}
	if after2.ChunkStoreFileBytes != after1.ChunkStoreFileBytes {
		t.Fatalf("expected actual CAS chunk file bytes on disk unchanged (chunks rewritten/duplicated), got %d vs %d", after2.ChunkStoreFileBytes, after1.ChunkStoreFileBytes)
	}
	if after2.DedupAvoidedBytes <= 0 {
		t.Fatalf("expected a positive dedup_avoided_bytes for a duplicate upload, got %d", after2.DedupAvoidedBytes)
	}
}

func TestDedup_IdenticalObjectReuseAcrossBuckets(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	body := genRandomBytes(4242, 3*1024*1024)
	if err := s.CreateBucket("b1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b1", "obj", body, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	storeAfterOne, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.PutObject("b2", "obj", body, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	storeAfterTwo, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	b1Scope, err := s.computeStats(statsScope{bucket: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	b2Scope, err := s.computeStats(statsScope{bucket: "b2"})
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("store-wide unique bytes: 1 bucket=%d, 2 buckets=%d", storeAfterOne.ScopeUniqueChunkBytes, storeAfterTwo.ScopeUniqueChunkBytes)
	t.Logf("bucket b1: unique=%d exclusive=%d shared=%d", b1Scope.ScopeUniqueChunkBytes, b1Scope.ScopeExclusiveChunkBytes, b1Scope.ScopeSharedChunkBytes)
	t.Logf("bucket b2: unique=%d exclusive=%d shared=%d", b2Scope.ScopeUniqueChunkBytes, b2Scope.ScopeExclusiveChunkBytes, b2Scope.ScopeSharedChunkBytes)

	if storeAfterTwo.ScopeUniqueChunkBytes != storeAfterOne.ScopeUniqueChunkBytes {
		t.Fatalf("store-wide unique chunk bytes must not grow when identical content is uploaded to a second bucket, got %d vs %d",
			storeAfterTwo.ScopeUniqueChunkBytes, storeAfterOne.ScopeUniqueChunkBytes)
	}
	if b1Scope.ScopeExclusiveChunkBytes != 0 {
		t.Fatalf("expected bucket b1's chunks to be entirely shared with b2 (0 exclusive), got %d exclusive", b1Scope.ScopeExclusiveChunkBytes)
	}
	if b1Scope.ScopeSharedChunkBytes != b1Scope.ScopeUniqueChunkBytes {
		t.Fatalf("expected all of b1's unique bytes to be shared with b2, got shared=%d unique=%d", b1Scope.ScopeSharedChunkBytes, b1Scope.ScopeUniqueChunkBytes)
	}
	if b2Scope.ScopeUniqueChunkBytes != b1Scope.ScopeUniqueChunkBytes {
		t.Fatalf("expected both buckets to reference the identical unique byte total, got b1=%d b2=%d", b1Scope.ScopeUniqueChunkBytes, b2Scope.ScopeUniqueChunkBytes)
	}
}

func TestDedup_EditedObjectReuseBeatsFixedSizeChunking(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	original := genRandomBytes(777, 2*1024*1024)
	// Insert 4001 bytes near the *start* of the file (not the middle):
	// everything before the edit trivially "reuses" for any chunker,
	// fixed-size included, since it's simply unchanged bytes a
	// deterministic chunker walks in the same order -- that is not a
	// meaningful comparison. Editing near the start instead means the
	// overwhelming majority of the file's bytes sit *downstream* of the
	// edit, which is exactly where fixed-size chunking fails (every
	// absolute chunk boundary after the edit shifts by the insertion
	// length, so no downstream chunk matches its old content) while CDC
	// resynchronizes within about one chunk of the edit.
	const editOffset = 50000
	insertion := genRandomBytes(778, 4001)
	edited := append(append(append([]byte{}, original[:editOffset]...), insertion...), original[editOffset:]...)

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "original", original, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "edited", edited, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	full, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	editedOnly, err := s.computeStats(statsScope{bucket: "b", key: "edited"})
	if err != nil {
		t.Fatal(err)
	}

	reusedBytes := editedOnly.ScopeSharedChunkBytes // bytes of "edited" that live in chunks also referenced by "original"
	reuseFraction := float64(reusedBytes) / float64(editedOnly.LogicalCurrentBytes)

	// Fixed-64KiB-chunking comparison over the exact same two byte
	// strings, computed independently of the Store/CAS, to show what a
	// non-content-defined chunker would have achieved.
	fixedOriginal := fixedSizeChunks(original, 64*1024)
	fixedEdited := fixedSizeChunks(edited, 64*1024)
	fixedOriginalSet := map[[32]byte]bool{}
	for _, c := range fixedOriginal {
		fixedOriginalSet[sha256.Sum256(c)] = true
	}
	var fixedReusedBytes int
	for _, c := range fixedEdited {
		if fixedOriginalSet[sha256.Sum256(c)] {
			fixedReusedBytes += len(c)
		}
	}
	fixedReuseFraction := float64(fixedReusedBytes) / float64(len(edited))

	t.Logf("original logical bytes:   %d", len(original))
	t.Logf("edited logical bytes:     %d", len(edited))
	t.Logf("store total chunk refs:   %d (%d bytes)", full.LogicalChunkReferenceCount, full.LogicalChunkReferenceBytes)
	t.Logf("store unique chunks:      %d (%d bytes)", full.ScopeUniqueChunkCount, full.ScopeUniqueChunkBytes)
	t.Logf("edited object: exclusive=%d bytes, reused(shared w/ original)=%d bytes (%.1f%% of edited object)",
		editedOnly.ScopeExclusiveChunkBytes, reusedBytes, reuseFraction*100)
	t.Logf("store dedup_avoided_bytes=%d dedup_reduction=%.1f%%", full.DedupAvoidedBytes, full.DedupReduction*100)
	t.Logf("fixed-64KiB-chunk comparison: reused=%d bytes (%.1f%% of edited object)", fixedReusedBytes, fixedReuseFraction*100)

	if reuseFraction < 0.90 {
		t.Fatalf("expected CDC to reuse the large majority of the edited object's bytes from the original upload, got %.1f%% reused", reuseFraction*100)
	}
	if fixedReuseFraction > 0.05 {
		t.Fatalf("expected fixed-size 64KiB chunking to reuse almost nothing after a mid-file insertion (edit locality is the point of this comparison), got %.1f%%", fixedReuseFraction*100)
	}
	if reuseFraction <= fixedReuseFraction {
		t.Fatalf("expected CDC reuse fraction (%.3f) to clearly beat fixed-size reuse fraction (%.3f)", reuseFraction, fixedReuseFraction)
	}
}

// =============================================================================
// M3: stats
// =============================================================================

// buildManualManifest and putManualObject construct an object from
// exact, hand-chosen chunk boundaries, bypassing CDC entirely, so stats/
// verify tests can assert exact arithmetic on a known chunk-sharing
// arrangement rather than depending on CDC's content-determined
// boundaries. ETag/ObjectSHA256 are placeholders: neither stats nor
// verify (Section 12/13) checks them against reconstructed content.
func buildManualManifest(chunks [][]byte, contentType string, metadata map[string]string) manifestV1 {
	id := newUUIDv7()
	refs := make([]chunkRef, len(chunks))
	var total int64
	objHash := sha256.New()
	for i, c := range chunks {
		sum := sha256.Sum256(c)
		refs[i] = chunkRef{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(c))}
		total += int64(len(c))
		objHash.Write(c)
	}
	objSum := objHash.Sum(nil)
	return manifestV1{
		ManifestFormatVersion: manifestFormatVersion,
		CDCFormatVersion:      cdcFormatVersion,
		HashAlgorithm:         "sha256",
		ManifestUUID:          id,
		TotalLength:           total,
		Chunks:                refs,
		ObjectSHA256:          hex.EncodeToString(objSum),
		ETag:                  "manualtestetag0000000000000000",
		ContentType:           contentType,
		Metadata:              sortedMetadataKV(metadata),
		CreatedAt:             time.Now().UTC(),
		VersionID:             id,
	}
}

func putManualObject(t *testing.T, s *Store, bucket, key string, chunks [][]byte) *objectEntry {
	t.Helper()
	for _, c := range chunks {
		if _, err := s.casWrite(c); err != nil {
			t.Fatal(err)
		}
	}
	man := buildManualManifest(chunks, "application/octet-stream", nil)
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.commitObjectRoot(bucket, key, manUUID, manSHA, man)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// TestStats_ExactSharingArrangement manually constructs:
//
//	chunk A = 1000 bytes, chunk B = 2000 bytes, chunk C = 3000 bytes
//	bucket "b":  x = [A,B] (3000B)   y = [B,C] (5000B)
//	bucket "b2": z = [C]   (3000B)
//
// and proves every STATS_SPEC.md field by hand-computed arithmetic.
func TestStats_ExactSharingArrangement(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunkA := bytes.Repeat([]byte{0xA1}, 1000)
	chunkB := bytes.Repeat([]byte{0xB2}, 2000)
	chunkC := bytes.Repeat([]byte{0xC3}, 3000)

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b2"); err != nil {
		t.Fatal(err)
	}
	putManualObject(t, s, "b", "x", [][]byte{chunkA, chunkB})
	putManualObject(t, s, "b", "y", [][]byte{chunkB, chunkC})
	putManualObject(t, s, "b2", "z", [][]byte{chunkC})

	whole, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if whole.BucketCount != 2 {
		t.Fatalf("bucket_count: got %d want 2", whole.BucketCount)
	}
	if whole.CurrentObjectCount != 3 {
		t.Fatalf("current_object_count: got %d want 3", whole.CurrentObjectCount)
	}
	if whole.LogicalCurrentBytes != 11000 {
		t.Fatalf("logical_current_bytes: got %d want 11000", whole.LogicalCurrentBytes)
	}
	if whole.LogicalChunkReferenceCount != 5 {
		t.Fatalf("logical_chunk_reference_count: got %d want 5", whole.LogicalChunkReferenceCount)
	}
	if whole.LogicalChunkReferenceBytes != 11000 {
		t.Fatalf("logical_chunk_reference_bytes: got %d want 11000", whole.LogicalChunkReferenceBytes)
	}
	if whole.ScopeUniqueChunkCount != 3 {
		t.Fatalf("scope_unique_chunk_count (whole store): got %d want 3", whole.ScopeUniqueChunkCount)
	}
	if whole.ScopeUniqueChunkBytes != 6000 {
		t.Fatalf("scope_unique_chunk_bytes (whole store): got %d want 6000", whole.ScopeUniqueChunkBytes)
	}
	if whole.ScopeExclusiveChunkBytes != 6000 || whole.ScopeSharedChunkBytes != 0 {
		t.Fatalf("whole-store scope must be entirely exclusive: exclusive=%d shared=%d", whole.ScopeExclusiveChunkBytes, whole.ScopeSharedChunkBytes)
	}
	if whole.UniqueReachableChunkBytes != 6000 {
		t.Fatalf("unique_reachable_chunk_bytes: got %d want 6000", whole.UniqueReachableChunkBytes)
	}
	if whole.ChunkStoreFileBytes != 6000 {
		t.Fatalf("chunk_store_file_bytes: got %d want 6000 (each of A/B/C published exactly once)", whole.ChunkStoreFileBytes)
	}
	if whole.DedupAvoidedBytes != 5000 {
		t.Fatalf("dedup_avoided_bytes: got %d want 5000", whole.DedupAvoidedBytes)
	}
	if wantReduction := 5000.0 / 11000.0; whole.DedupReduction < wantReduction-1e-9 || whole.DedupReduction > wantReduction+1e-9 {
		t.Fatalf("dedup_reduction: got %v want %v", whole.DedupReduction, wantReduction)
	}

	bScope, err := s.computeStats(statsScope{bucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if bScope.CurrentObjectCount != 2 || bScope.LogicalCurrentBytes != 8000 {
		t.Fatalf("bucket b scope: objects=%d bytes=%d, want 2/8000", bScope.CurrentObjectCount, bScope.LogicalCurrentBytes)
	}
	if bScope.LogicalChunkReferenceCount != 4 || bScope.LogicalChunkReferenceBytes != 8000 {
		t.Fatalf("bucket b chunk refs: count=%d bytes=%d, want 4/8000", bScope.LogicalChunkReferenceCount, bScope.LogicalChunkReferenceBytes)
	}
	if bScope.ScopeUniqueChunkCount != 3 || bScope.ScopeUniqueChunkBytes != 6000 {
		t.Fatalf("bucket b unique chunks: count=%d bytes=%d, want 3/6000", bScope.ScopeUniqueChunkCount, bScope.ScopeUniqueChunkBytes)
	}
	if bScope.ScopeExclusiveChunkBytes != 3000 {
		t.Fatalf("bucket b exclusive bytes (A+B): got %d want 3000", bScope.ScopeExclusiveChunkBytes)
	}
	if bScope.ScopeSharedChunkBytes != 3000 {
		t.Fatalf("bucket b shared bytes (C, also referenced by b2): got %d want 3000", bScope.ScopeSharedChunkBytes)
	}
	if bScope.DedupAvoidedBytes != 2000 {
		t.Fatalf("bucket b dedup_avoided_bytes (B referenced twice within scope): got %d want 2000", bScope.DedupAvoidedBytes)
	}

	b2Scope, err := s.computeStats(statsScope{bucket: "b2"})
	if err != nil {
		t.Fatal(err)
	}
	if b2Scope.CurrentObjectCount != 1 || b2Scope.LogicalCurrentBytes != 3000 {
		t.Fatalf("bucket b2 scope: objects=%d bytes=%d, want 1/3000", b2Scope.CurrentObjectCount, b2Scope.LogicalCurrentBytes)
	}
	if b2Scope.ScopeExclusiveChunkBytes != 0 || b2Scope.ScopeSharedChunkBytes != 3000 {
		t.Fatalf("bucket b2's only chunk (C) must be entirely shared: exclusive=%d shared=%d", b2Scope.ScopeExclusiveChunkBytes, b2Scope.ScopeSharedChunkBytes)
	}

	yScope, err := s.computeStats(statsScope{bucket: "b", key: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if yScope.CurrentObjectCount != 1 || yScope.LogicalCurrentBytes != 5000 {
		t.Fatalf("object y scope: objects=%d bytes=%d, want 1/5000", yScope.CurrentObjectCount, yScope.LogicalCurrentBytes)
	}
	if yScope.ScopeExclusiveChunkBytes != 0 {
		t.Fatalf("object y has no chunk exclusive to it (both B and C are used elsewhere): got exclusive=%d", yScope.ScopeExclusiveChunkBytes)
	}
	if yScope.ScopeSharedChunkBytes != 5000 {
		t.Fatalf("object y: expected all 5000 unique bytes to be shared with other objects, got %d", yScope.ScopeSharedChunkBytes)
	}
}

func TestStats_ReclaimableAfterDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	chunkA := bytes.Repeat([]byte{0xA1}, 1000)
	chunkB := bytes.Repeat([]byte{0xB2}, 2000)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	putManualObject(t, s, "b", "x", [][]byte{chunkA})
	entryY := putManualObject(t, s, "b", "y", [][]byte{chunkA, chunkB})
	yManInfo, err := os.Stat(filepath.Join(dir, "manifests", entryY.manifestUUID+".json"))
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if before.ReclaimableBytes != 0 {
		t.Fatalf("expected nothing reclaimable before any delete, got %d", before.ReclaimableBytes)
	}

	if err := s.DeleteObject("b", "y"); err != nil {
		t.Fatal(err)
	}

	after, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	// B is only referenced by the now-deleted y, so it becomes reclaimable;
	// A is still referenced by x and remains reachable. y's own manifest
	// file also becomes unreachable (no current root points at it any
	// more), which is real reclaimable bytes too -- deletion changes
	// roots, not chunks *or* the manifest that named them.
	if after.UniqueReachableChunkBytes != 1000 {
		t.Fatalf("unique_reachable_chunk_bytes after delete: got %d want 1000 (only A remains reachable)", after.UniqueReachableChunkBytes)
	}
	if after.ChunkStoreFileBytes != 3000 {
		t.Fatalf("chunk_store_file_bytes must be unchanged by a DELETE (deletion changes roots, not chunks): got %d want 3000", after.ChunkStoreFileBytes)
	}
	wantReclaimable := int64(2000) + yManInfo.Size()
	if after.ReclaimableBytes != wantReclaimable {
		t.Fatalf("reclaimable_bytes after deleting y: got %d want %d (chunk B's bytes + y's own now-unreachable manifest file)", after.ReclaimableBytes, wantReclaimable)
	}

	verifyRes, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("unreachable garbage must not be reported as an integrity failure: %+v", verifyRes)
	}
	if verifyRes.UnreachableChunks != 1 || verifyRes.UnreachableManifests != 1 || verifyRes.ReclaimableBytes != wantReclaimable {
		t.Fatalf("verify reclaimable accounting: unreachable_chunks=%d unreachable_manifests=%d reclaimable_bytes=%d, want 1/1/%d",
			verifyRes.UnreachableChunks, verifyRes.UnreachableManifests, verifyRes.ReclaimableBytes, wantReclaimable)
	}
}

func TestStats_JSONFieldNamesMatchSpec(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	for _, field := range []string{
		"bucket_count", "current_object_count", "version_count",
		"logical_current_bytes", "logical_version_bytes",
		"logical_chunk_reference_bytes", "logical_chunk_reference_count",
		"scope_unique_chunk_bytes", "scope_unique_chunk_count",
		"scope_exclusive_chunk_bytes", "scope_shared_chunk_bytes",
		"unique_reachable_chunk_bytes", "chunk_store_file_bytes",
		"manifest_file_bytes", "journal_file_bytes", "temporary_file_bytes",
		"reclaimable_bytes", "actual_store_file_bytes",
		"dedup_avoided_bytes", "dedup_reduction", "unique_to_logical_ratio",
	} {
		if !strings.Contains(js, `"`+field+`"`) {
			t.Fatalf("expected stable JSON field name %q in stats output, got: %s", field, js)
		}
	}
}

// =============================================================================
// M3: verify
// =============================================================================

func TestVerify_CleanStoreOK(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k1", genRandomBytes(10, 300*1024), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k2", genRandomBytes(11, 5000), "text/plain", map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}

	for _, deep := range []bool{false, true} {
		res, err := s.Verify(deep)
		if err != nil {
			t.Fatalf("Verify(deep=%v): %v", deep, err)
		}
		if !res.OK() {
			t.Fatalf("Verify(deep=%v) on a clean store must be OK, got %+v", deep, res)
		}
		if res.ManifestsChecked != 2 {
			t.Fatalf("Verify(deep=%v) manifests_checked: got %d want 2", deep, res.ManifestsChecked)
		}
		if res.ChunksChecked == 0 {
			t.Fatalf("Verify(deep=%v) chunks_checked must be nonzero", deep)
		}
		if !res.JournalOK {
			t.Fatalf("Verify(deep=%v) journal_ok must be true", deep)
		}
	}
}

func TestVerify_DetectsMissingChunk(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", genRandomBytes(20, 300*1024), "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := s.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := decodeHexSHA256(man.Chunks[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}

	res, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatalf("expected a missing chunk to fail verify")
	}
	if res.Missing == 0 {
		t.Fatalf("expected the missing chunk to be counted, got %+v", res)
	}
}

func TestVerify_DetectsCorruptChunkOnlyUnderDeep(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", genRandomBytes(21, 300*1024), "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := s.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	c := man.Chunks[0]
	sum, err := decodeHexSHA256(c.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Repeat([]byte{0x99}, int(c.Length)) // same length, different content
	if err := os.WriteFile(s.chunkPath(sum), tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	basic, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if !basic.OK() {
		t.Fatalf("basic verify only checks file length, not content; a same-length corruption must not be flagged: %+v", basic)
	}

	deep, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if deep.OK() {
		t.Fatalf("deep verify must detect content corruption that preserves file length")
	}
	if deep.Corrupt == 0 {
		t.Fatalf("expected the corrupted chunk to be counted as corrupt, got %+v", deep)
	}
}

func TestVerify_DetectsCorruptManifestHashMismatch(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", []byte("small object for manifest corruption test"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	manPath := filepath.Join(dir, "manifests", entry.manifestUUID+".json")
	orig, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte{}, orig...)
	tampered[0] ^= 0xFF // flip a byte inside the JSON: breaks the recorded manifest-file SHA256
	if err := os.WriteFile(manPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatalf("expected a manifest whose bytes no longer match the journal-recorded SHA256 to fail verify")
	}
	if res.Corrupt == 0 {
		t.Fatalf("expected the manifest hash mismatch to be counted as corrupt, got %+v", res)
	}
}

func TestVerify_DetectsUnparsableManifestJSON(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	garbage := []byte("this is not valid manifest json")
	manUUID := newUUIDv7()
	manPath := filepath.Join(dir, "manifests", manUUID+".json")
	if err := os.WriteFile(manPath, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	manSHA := sha256.Sum256(garbage)

	// Commit a root pointing at this deliberately-invalid manifest,
	// bypassing the normal PutObject pipeline (which would never publish
	// invalid JSON), so verify's "manifest JSON does not parse" branch is
	// exercised directly, isolated from the hash-mismatch check above.
	if _, err := s.commitObjectRoot("b", "badmanifest", manUUID, manSHA, manifestV1{TotalLength: 0, ETag: "x", ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatalf("expected an unparsable manifest to fail verify")
	}
	if res.Invalid == 0 {
		t.Fatalf("expected the unparsable manifest to be counted as invalid, got %+v", res)
	}
}

func TestVerify_DetectsChunkLengthMismatch(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	chunkBytes := []byte("some chunk content of a certain fixed length")
	sum, err := s.casWrite(chunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	badLength := int64(len(chunkBytes)) + 100 // does not match the real file size
	man := manifestV1{
		ManifestFormatVersion: manifestFormatVersion,
		CDCFormatVersion:      cdcFormatVersion,
		HashAlgorithm:         "sha256",
		ManifestUUID:          newUUIDv7(),
		TotalLength:           badLength, // matches the (wrong) declared chunk length, isolating this test to the chunk-file-vs-manifest check
		Chunks:                []chunkRef{{SHA256: hex.EncodeToString(sum[:]), Length: badLength}},
		ObjectSHA256:          strings.Repeat("0", 64),
		ETag:                  "deadbeef00000000000000000000000",
		ContentType:           "application/octet-stream",
		CreatedAt:             time.Now().UTC(),
	}
	man.VersionID = man.ManifestUUID
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commitObjectRoot("b", "badlen", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	res, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatalf("expected a chunk file whose actual size disagrees with its manifest-declared length to fail verify")
	}
	if res.Corrupt == 0 {
		t.Fatalf("expected the length mismatch to be reported as corrupt, got %+v", res)
	}
}

func TestVerify_DetectsManifestLengthSumMismatch(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	chunkBytes := []byte("chunk content whose length is correct on disk")
	sum, err := s.casWrite(chunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	man := manifestV1{
		ManifestFormatVersion: manifestFormatVersion,
		CDCFormatVersion:      cdcFormatVersion,
		HashAlgorithm:         "sha256",
		ManifestUUID:          newUUIDv7(),
		TotalLength:           int64(len(chunkBytes)) + 999, // deliberately inconsistent with the chunk list
		Chunks:                []chunkRef{{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(chunkBytes))}},
		ObjectSHA256:          strings.Repeat("0", 64),
		ETag:                  "deadbeef00000000000000000000001",
		ContentType:           "application/octet-stream",
		CreatedAt:             time.Now().UTC(),
	}
	man.VersionID = man.ManifestUUID
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commitObjectRoot("b", "badsum", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	res, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK() {
		t.Fatalf("expected a manifest whose chunk lengths don't sum to total_length to fail verify")
	}
	if res.Invalid == 0 {
		t.Fatalf("expected the sum mismatch to be reported as invalid, got %+v", res)
	}
	if res.Corrupt != 0 {
		t.Fatalf("the chunk itself is not corrupt (its file matches its own declared length); only the manifest's declared total is wrong, got corrupt=%d", res.Corrupt)
	}
}

// =============================================================================
// M3 correction pass: deep verify whole-object SHA-256 (A3)
// =============================================================================

func TestVerify_DeepWholeObjectDigest_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "empty", nil, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	res, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("deep verify of a correctly-hashed empty object must be OK, got %+v", res)
	}
}

func TestVerify_DeepWholeObjectDigest_MultiChunkObject(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	// Large enough to force multiple CDC chunks (see cdcMaxChunkSize), so
	// the streaming order actually exercises more than one chunk.
	if _, err := s.PutObject("b", "multi", genRandomBytes(910, 900*1024), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	res, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("deep verify of a correctly-hashed multi-chunk object must be OK, got %+v", res)
	}
}

// TestVerify_DeepWholeObjectDigest_MismatchDetected is the direct
// regression test for A3: a manifest whose object_sha256 has been
// tampered with, while every individual chunk reference stays valid
// (correct hash, correct length, correct total_length sum), must be
// caught only by -deep -- per-chunk checks alone have no way to notice
// this, since GetObject never checks object_sha256 either.
func TestVerify_DeepWholeObjectDigest_MismatchDetected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	chunkA := bytes.Repeat([]byte{0xAA}, 4000)
	chunkB := bytes.Repeat([]byte{0xBB}, 5000)
	man := buildManualManifest([][]byte{chunkA, chunkB}, "application/octet-stream", nil)
	// Tamper the object digest only -- chunk refs/lengths/total_length
	// are all left correct, isolating this test to A3's new check.
	man.ObjectSHA256 = strings.Repeat("f", 64)
	if _, err := s.casWrite(chunkA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.casWrite(chunkB); err != nil {
		t.Fatal(err)
	}
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commitObjectRoot("b", "tampered-digest", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	basic, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if !basic.OK() {
		t.Fatalf("basic (non-deep) verify does not check object_sha256 and must stay OK, got %+v", basic)
	}

	deep, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if deep.OK() {
		t.Fatalf("deep verify must detect a manifest object_sha256 that doesn't match its chunks")
	}
	if deep.Corrupt == 0 {
		t.Fatalf("expected the object digest mismatch to be reported as corrupt, got %+v", deep)
	}
}

// TestVerify_DeepWholeObjectDigest_MalformedReportedInvalid proves a
// malformed object_sha256 field is reported, not silently ignored.
func TestVerify_DeepWholeObjectDigest_MalformedReportedInvalid(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	chunk := []byte("some chunk content for the malformed-digest test")
	man := buildManualManifest([][]byte{chunk}, "application/octet-stream", nil)
	man.ObjectSHA256 = "not-a-valid-hex-sha256"
	if _, err := s.casWrite(chunk); err != nil {
		t.Fatal(err)
	}
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commitObjectRoot("b", "malformed-digest", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	deep, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if deep.OK() {
		t.Fatalf("deep verify must reject a malformed object_sha256 rather than silently ignoring it")
	}
	if deep.Invalid == 0 {
		t.Fatalf("expected the malformed object_sha256 to be reported as invalid, got %+v", deep)
	}
}

// =============================================================================
// M3 correction pass: per-root manifest hash verification (A4)
// =============================================================================

// TestVerify_PerRootManifestHashCheckedEvenWhenUUIDCached is the direct
// regression test for A4. It constructs an adversarial condition that
// cannot arise from normal journal-writing code: two roots referencing
// the SAME manifest UUID, where one root's journal-recorded manifest
// SHA256 is correct and the other's is deliberately wrong. Verify must
// still catch the wrong one no matter which root it happens to process
// first -- Go's map iteration order (Verify walks snapshotNamespace,
// which is built from map ranges) is intentionally randomized per call
// and not controlled by this test, so the two subtests below (which only
// vary which journal frame/map entry is *appended* first, not which one
// Verify necessarily *visits* first) exist to broaden coverage across
// runs rather than to pin down a specific visit order; what must hold
// regardless of order is that caching a manifest's parsed content/hash to
// avoid re-reading the file never lets a second root silently inherit a
// "verified" status it hasn't earned.
func TestVerify_PerRootManifestHashCheckedEvenWhenUUIDCached(t *testing.T) {
	for _, badRootFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("badRootAppendedFirst=%v", badRootFirst), func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}

			entry, err := s.PutObject("b", "good", genRandomBytes(72, 4096), "application/octet-stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			// A second root pointing at the exact same manifest UUID is a
			// legitimate, supported condition (e.g. two keys copied from
			// the same source, or -- pre-A1 -- a default-directive copy);
			// what's adversarial here is only the wrong recorded hash on
			// one of the two roots, constructed directly since no normal
			// Store operation ever appends a mismatched hash.
			wrongSHA := entry.manifestSHA256
			wrongSHA[0] ^= 0xFF
			payloadGood, err := json.Marshal(journalPutPayload{
				Bucket: "b", Key: "good2", ManifestUUID: entry.manifestUUID,
				ManifestSHA256: hex.EncodeToString(entry.manifestSHA256[:]),
				Size:           entry.size, ETag: entry.etag, ContentType: entry.contentType, VersionID: entry.manifestUUID,
			})
			if err != nil {
				t.Fatal(err)
			}
			payloadBad, err := json.Marshal(journalPutPayload{
				Bucket: "b", Key: "bad2", ManifestUUID: entry.manifestUUID,
				ManifestSHA256: hex.EncodeToString(wrongSHA[:]),
				Size:           entry.size, ETag: entry.etag, ContentType: entry.contentType, VersionID: entry.manifestUUID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if badRootFirst {
				if _, err := s.journal.appendFrame(recordTypePutObjectRoot, payloadBad); err != nil {
					t.Fatal(err)
				}
				if _, err := s.journal.appendFrame(recordTypePutObjectRoot, payloadGood); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				s.buckets["b"].objects["bad2"] = &objectEntry{manifestUUID: entry.manifestUUID, manifestSHA256: wrongSHA, size: entry.size, etag: entry.etag, contentType: entry.contentType}
				s.buckets["b"].objects["good2"] = &objectEntry{manifestUUID: entry.manifestUUID, manifestSHA256: entry.manifestSHA256, size: entry.size, etag: entry.etag, contentType: entry.contentType}
				s.mu.Unlock()
			} else {
				if _, err := s.journal.appendFrame(recordTypePutObjectRoot, payloadGood); err != nil {
					t.Fatal(err)
				}
				if _, err := s.journal.appendFrame(recordTypePutObjectRoot, payloadBad); err != nil {
					t.Fatal(err)
				}
				s.mu.Lock()
				s.buckets["b"].objects["good2"] = &objectEntry{manifestUUID: entry.manifestUUID, manifestSHA256: entry.manifestSHA256, size: entry.size, etag: entry.etag, contentType: entry.contentType}
				s.buckets["b"].objects["bad2"] = &objectEntry{manifestUUID: entry.manifestUUID, manifestSHA256: wrongSHA, size: entry.size, etag: entry.etag, contentType: entry.contentType}
				s.mu.Unlock()
			}

			res, err := s.Verify(false)
			if err != nil {
				t.Fatal(err)
			}
			if res.OK() {
				t.Fatalf("expected the root with a wrong recorded manifest hash to fail verify even though another root warmed the manifest cache first, got %+v", res)
			}
			if res.Corrupt == 0 {
				t.Fatalf("expected the mismatched root to be counted as corrupt, got %+v", res)
			}
			// The correctly-hashed root sharing the same UUID must still
			// verify fine -- this isn't "the manifest is corrupt", it's
			// "this one root's claim about the manifest is wrong".
			found := false
			for _, issue := range res.Issues {
				if issue.Subject == "b/good2" {
					found = true
				}
			}
			if found {
				t.Fatalf("the correctly-hashed root must not be reported as an issue, got %+v", res.Issues)
			}
		})
	}
}

func TestConcurrency_StatsDuringWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := fmt.Sprintf("k%d", i%20)
			if _, err := s.PutObject("b", key, genRandomBytes(int64(i), 2000), "application/octet-stream", nil); err != nil {
				t.Errorf("PutObject: %v", err)
				return
			}
		}
	}()

	for i := 0; i < 50; i++ {
		if _, err := s.computeStats(statsScope{}); err != nil {
			t.Fatalf("stats during concurrent writes: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestConcurrency_VerifyDuringWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := fmt.Sprintf("k%d", i%20)
			if _, err := s.PutObject("b", key, genRandomBytes(int64(i), 2000), "application/octet-stream", nil); err != nil {
				t.Errorf("PutObject: %v", err)
				return
			}
		}
	}()

	for i := 0; i < 50; i++ {
		res, err := s.Verify(false)
		if err != nil {
			t.Fatalf("verify during concurrent writes: %v", err)
		}
		if res.Missing > 0 || res.Corrupt > 0 || res.Invalid > 0 {
			t.Fatalf("verify spuriously reported integrity failures during concurrent writes: %+v", res)
		}
	}
	close(stop)
	wg.Wait()
}

// =============================================================================
// M3: CopyObject
// =============================================================================

func TestCopyObject_SameBucketZeroNewCASChunkBytes(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := genRandomBytes(55, 3*1024*1024)
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bucket1/src", body, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PutObject failed: %d", put.StatusCode)
	}

	before, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bucket1/dst", nil, map[string]string{
		"X-Amz-Copy-Source": "/bucket1/src",
	})
	defer copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(copyResp.Body)
		t.Fatalf("CopyObject failed: %d: %s", copyResp.StatusCode, data)
	}
	var result copyObjectResult
	if err := xml.NewDecoder(copyResp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ETag == "" {
		t.Fatalf("expected a non-empty ETag in CopyObjectResult")
	}

	after, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	// The measurable claim is zero new CAS *chunk* payload bytes -- not
	// zero bytes of any kind. Both metadata directives now publish a
	// fresh destination manifest (new UUID/version/CreatedAt), so
	// manifest_file_bytes is expected to grow; chunk_store_file_bytes
	// must not move at all.
	if after.ChunkStoreFileBytes != before.ChunkStoreFileBytes {
		t.Fatalf("CopyObject wrote new CAS chunk payload bytes: before=%d after=%d", before.ChunkStoreFileBytes, after.ChunkStoreFileBytes)
	}
	if after.ManifestFileBytes <= before.ManifestFileBytes {
		t.Fatalf("expected CopyObject to publish a new destination manifest file (manifest_file_bytes should grow): before=%d after=%d", before.ManifestFileBytes, after.ManifestFileBytes)
	}
	if after.LogicalCurrentBytes != before.LogicalCurrentBytes+int64(len(body)) {
		t.Fatalf("expected the destination object's logical bytes to be counted, got before=%d after=%d body=%d", before.LogicalCurrentBytes, after.LogicalCurrentBytes, len(body))
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bucket1/dst", nil, nil)
	defer get.Body.Close()
	gotBody, _ := io.ReadAll(get.Body)
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("copied object bytes do not match source")
	}
}

func TestCopyObject_CrossBucket(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "src-bucket"); err != nil {
		t.Fatal(err)
	}
	if err := doCreateBucket(t, client, ts.URL, signer, "dst-bucket"); err != nil {
		t.Fatal(err)
	}

	body := []byte("cross-bucket copy payload")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/src-bucket/key", body, nil)
	put.Body.Close()

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/dst-bucket/key2", nil, map[string]string{"X-Amz-Copy-Source": "/src-bucket/key"})
	copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", copyResp.StatusCode)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/dst-bucket/key2", nil, nil)
	defer get.Body.Close()
	got, _ := io.ReadAll(get.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("cross-bucket copy did not round trip bytes")
	}
}

func TestCopyObject_OverwritesExistingDestination(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	oldDst := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", []byte("stale destination content"), nil)
	oldDst.Body.Close()
	srcBody := []byte("fresh source content")
	src := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", srcBody, nil)
	src.Body.Close()

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{"X-Amz-Copy-Source": "/b/src"})
	copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", copyResp.StatusCode)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/dst", nil, nil)
	defer get.Body.Close()
	got, _ := io.ReadAll(get.Body)
	if !bytes.Equal(got, srcBody) {
		t.Fatalf("expected CopyObject to overwrite the existing destination, got %q", got)
	}
}

func TestCopyObject_MissingSource(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{"X-Amz-Copy-Source": "/b/does-not-exist"})
	defer copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing copy source, got %d", copyResp.StatusCode)
	}
}

func TestCopyObject_MissingDestinationBucket(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "src-bucket"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/src-bucket/key", []byte("x"), nil)
	put.Body.Close()

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/no-such-bucket/dst", nil, map[string]string{"X-Amz-Copy-Source": "/src-bucket/key"})
	defer copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing destination bucket, got %d", copyResp.StatusCode)
	}
}

func TestCopyObject_MetadataDirectiveCopyPreservesSourceMetadata(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("payload"), map[string]string{
		"Content-Type":      "application/json",
		"x-amz-meta-origin": "source",
	})
	put.Body.Close()

	// No X-Amz-Metadata-Directive header: default is COPY.
	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{"X-Amz-Copy-Source": "/b/src"})
	copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", copyResp.StatusCode)
	}

	head := doHeadObject(t, client, ts.URL, signer, "/b/dst")
	defer head.Body.Close()
	if head.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("expected COPY directive to preserve Content-Type, got %q", head.Header.Get("Content-Type"))
	}
	if head.Header.Get("x-amz-meta-origin") != "source" {
		t.Fatalf("expected COPY directive to preserve source metadata, got %q", head.Header.Get("x-amz-meta-origin"))
	}
}

func TestCopyObject_MetadataDirectiveReplaceUsesNewMetadataZeroNewChunkBytes(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	body := genRandomBytes(66, 200*1024)
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", body, map[string]string{
		"Content-Type":      "application/octet-stream",
		"x-amz-meta-origin": "source",
	})
	put.Body.Close()

	before, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":        "/b/src",
		"X-Amz-Metadata-Directive": "REPLACE",
		"Content-Type":             "text/replaced",
		"x-amz-meta-origin":        "replaced",
	})
	copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", copyResp.StatusCode)
	}

	after, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if after.ChunkStoreFileBytes != before.ChunkStoreFileBytes {
		t.Fatalf("REPLACE metadata directive must not touch chunk payload bytes: before=%d after=%d", before.ChunkStoreFileBytes, after.ChunkStoreFileBytes)
	}

	head := doHeadObject(t, client, ts.URL, signer, "/b/dst")
	defer head.Body.Close()
	if head.Header.Get("Content-Type") != "text/replaced" {
		t.Fatalf("expected REPLACE directive to use the new Content-Type, got %q", head.Header.Get("Content-Type"))
	}
	if head.Header.Get("x-amz-meta-origin") != "replaced" {
		t.Fatalf("expected REPLACE directive to use the new metadata, got %q", head.Header.Get("x-amz-meta-origin"))
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/dst", nil, nil)
	defer get.Body.Close()
	gotBody, _ := io.ReadAll(get.Body)
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("REPLACE directive must still copy the exact source bytes")
	}
}

// TestCopyObject_DestinationGetsNewManifestIdentityAndTimestamp is the
// direct regression test for the M3 correction pass's CopyObject fix:
// under BOTH metadata directives, the destination root must publish a
// brand-new manifest (new UUID, new version ID, new CreatedAt), never
// reuse the source's -- because the destination's Last-Modified/version
// identity must represent this copy, not the source object.
func TestCopyObject_DestinationGetsNewManifestIdentityAndTimestamp(t *testing.T) {
	for _, directive := range []metadataDirective{metadataDirectiveCopy, metadataDirectiveReplace} {
		t.Run(fmt.Sprintf("directive=%d", directive), func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			srcEntry, err := s.PutObject("b", "src", genRandomBytes(71, 40*1024), "text/plain", map[string]string{"a": "1"})
			if err != nil {
				t.Fatal(err)
			}
			srcManBefore, err := s.readVerifiedManifest(srcEntry.manifestUUID, srcEntry.manifestSHA256)
			if err != nil {
				t.Fatal(err)
			}
			_, srcBytesBefore, err := s.readManifest(srcEntry.manifestUUID)
			if err != nil {
				t.Fatal(err)
			}

			// Ensure the clock actually advances between the source PUT
			// and the copy, so a reused CreatedAt can't accidentally look
			// "new" by coincidence.
			time.Sleep(2 * time.Millisecond)

			req := CopyObjectRequest{SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "dst", Directive: directive}
			if directive == metadataDirectiveReplace {
				req.ContentType = "text/replaced"
				req.Metadata = map[string]string{"b": "2"}
			}
			dstEntry, dstMan, err := s.CopyObject(req)
			if err != nil {
				t.Fatal(err)
			}

			if dstEntry.manifestUUID == srcEntry.manifestUUID {
				t.Fatalf("destination must get a new manifest UUID, got the source's: %s", dstEntry.manifestUUID)
			}
			if dstMan.VersionID != dstMan.ManifestUUID {
				t.Fatalf("destination version ID must match its own new manifest UUID, got version=%s uuid=%s", dstMan.VersionID, dstMan.ManifestUUID)
			}
			if dstMan.VersionID == srcManBefore.VersionID {
				t.Fatalf("destination must get a new version ID, got the source's: %s", dstMan.VersionID)
			}
			if !dstMan.CreatedAt.After(srcManBefore.CreatedAt) {
				t.Fatalf("destination CreatedAt (Last-Modified) must be a new, later timestamp: dst=%v src=%v", dstMan.CreatedAt, srcManBefore.CreatedAt)
			}

			// Payload identity is still byte-for-byte cloned: same chunks,
			// same object digest, same ETag -- only the manifest's own
			// identity/time (and, for REPLACE, metadata/content-type) is new.
			if dstMan.ObjectSHA256 != srcManBefore.ObjectSHA256 {
				t.Fatalf("copy must preserve the exact object SHA-256")
			}
			if dstMan.ETag != srcManBefore.ETag {
				t.Fatalf("copy must preserve the exact ETag")
			}
			if len(dstMan.Chunks) != len(srcManBefore.Chunks) {
				t.Fatalf("copy must preserve the exact ordered chunk list")
			}
			for i := range dstMan.Chunks {
				if dstMan.Chunks[i] != srcManBefore.Chunks[i] {
					t.Fatalf("chunk reference %d differs between source and destination manifest", i)
				}
			}

			// Source manifest on disk must be completely untouched by the copy.
			if _, err := s.readVerifiedManifest(srcEntry.manifestUUID, srcEntry.manifestSHA256); err != nil {
				t.Fatalf("source manifest must remain readable/unchanged after copy: %v", err)
			}
			_, srcBytesAfter, err := s.readManifest(srcEntry.manifestUUID)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(srcBytesBefore, srcBytesAfter) {
				t.Fatalf("source manifest file must be byte-for-byte unchanged by CopyObject")
			}
		})
	}
}

// TestParseCopySource_TrickyKeys is the regression test for the M3
// correction pass's x-amz-copy-source decoding fix. Inspecting the pinned
// AWS SDK Go v2's actual wire traffic (a real CopyObject call captured
// against a raw HTTP server) showed it applies ZERO percent-encoding of
// its own to CopySource: whatever bytes the caller supplies -- including
// raw spaces, '%', '+', '?', '#', and Unicode -- are sent completely
// unescaped. A strict decoder (url.PathUnescape, correct for a request
// path that the HTTP client library itself guarantees is well-formed)
// would reject the common raw case outright, since a literal '%' not part
// of a valid escape is a parse error to it. These cases are exactly the
// ones exercised over real HTTP by TestCopyObject_TrickySourceKeys below;
// this table isolates the parser itself.
func TestParseCopySource_TrickyKeys(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantBucket string
		wantKey    string
		wantErr    bool
	}{
		{"plain", "bucket/key.txt", "bucket", "key.txt", false},
		{"leading slash", "/bucket/key.txt", "bucket", "key.txt", false},
		{"raw space, unencoded (real SDK wire form)", "bucket/with space.txt", "bucket", "with space.txt", false},
		{"raw literal percent not part of an escape (real SDK wire form)", "bucket/100%done.txt", "bucket", "100%done.txt", false},
		{"pre-encoded space", "bucket/already%20encoded.txt", "bucket", "already encoded.txt", false},
		{"raw unicode, unencoded (real SDK wire form)", "bucket/héllo-世界.txt", "bucket", "héllo-世界.txt", false},
		{"raw plus stays literal, never becomes a space", "bucket/a+b plus.txt", "bucket", "a+b plus.txt", false},
		{"slash-containing key preserved", "bucket/dir/sub/key.txt", "bucket", "dir/sub/key.txt", false},
		{"pre-encoded slash decodes to a literal slash in the key", "bucket/a%2Fb.txt", "bucket", "a/b.txt", false},
		{"pre-encoded percent decodes to a literal percent", "bucket/percent%25literal.txt", "bucket", "percent%literal.txt", false},
		{"pre-encoded question mark survives (not confused with query syntax)", "bucket/weird%3Fchars%23here.txt", "bucket", "weird?chars#here.txt", false},
		{"trailing percent with nothing to decode", "bucket/trailing%", "bucket", "trailing%", false},
		{"versionId query is rejected", "bucket/key?versionId=abc123", "", "", true},
		{"versionId query with leading slash is rejected", "/bucket/key?versionId=abc123", "", "", true},
		{"empty source is rejected", "", "", "", true},
		{"bucket with no key is rejected", "bucket", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bucket, key, err := parseCopySource(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseCopySource(%q): expected an error, got bucket=%q key=%q", c.raw, bucket, key)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCopySource(%q): unexpected error: %v", c.raw, err)
			}
			if bucket != c.wantBucket || key != c.wantKey {
				t.Fatalf("parseCopySource(%q): got bucket=%q key=%q, want bucket=%q key=%q", c.raw, bucket, key, c.wantBucket, c.wantKey)
			}
		})
	}
}

// TestCopyObject_TrickySourceKeys exercises the same tricky source keys as
// TestParseCopySource_TrickyKeys, but end-to-end over real signed HTTP
// requests, with the x-amz-copy-source header set to the exact RAW,
// unescaped form the pinned AWS SDK Go v2 actually sends on the wire (see
// the comment on parseCopySource) -- proving the fix works through the
// full request pipeline, not just in the unit-level parser.
func TestCopyObject_TrickySourceKeys(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"with space.txt",
		"100%done.txt",
		"a+b plus.txt",
		"dir/sub/key.txt",
	}
	for i, key := range keys {
		t.Run(key, func(t *testing.T) {
			body := genRandomBytes(int64(900+i), 2048)
			// The object key itself goes in the request PATH, which real
			// HTTP client libraries always properly percent-encode -- so
			// build it via url.URL like doSignedRequest's callers rely on.
			putPath := "/b/" + (&url.URL{Path: key}).EscapedPath()
			put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, putPath, body, nil)
			put.Body.Close()
			if put.StatusCode != http.StatusOK {
				t.Fatalf("PUT %q failed: %d", key, put.StatusCode)
			}

			// x-amz-copy-source, by contrast, is a raw header VALUE: send
			// the exact unescaped bytes, matching the real SDK's observed
			// wire behavior.
			copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst-"+fmt.Sprint(i), nil, map[string]string{
				"X-Amz-Copy-Source": "b/" + key,
			})
			defer copyResp.Body.Close()
			if copyResp.StatusCode != http.StatusOK {
				data, _ := io.ReadAll(copyResp.Body)
				t.Fatalf("CopyObject for source key %q failed: %d: %s", key, copyResp.StatusCode, data)
			}

			get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/dst-"+fmt.Sprint(i), nil, nil)
			defer get.Body.Close()
			got, _ := io.ReadAll(get.Body)
			if !bytes.Equal(got, body) {
				t.Fatalf("copy of source key %q did not round-trip bytes", key)
			}
		})
	}
}

func TestCrash_CopyObjectReplaceBeforeJournalLeavesOldState(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	srcBody := genRandomBytes(41, 50*1024)
	if _, err := store.PutObject("b", "src", srcBody, "text/plain", map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}

	withTestHook(t, func(point string) {
		if point == hookAfterManifestPublished {
			panic(simulatedCrash{point: point})
		}
	})
	runExpectingSimulatedCrash(t, func() {
		_, _, _ = store.CopyObject(CopyObjectRequest{
			SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "dst",
			Directive: metadataDirectiveReplace, ContentType: "text/other", Metadata: map[string]string{"b": "2"},
		})
	})
	testHook = nil
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, _, err := store2.GetObject("b", "dst"); err == nil {
		t.Fatalf("copy destination must not be visible after a crash before its journal commit")
	}
	_, gotSrc, err := store2.GetObject("b", "src")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSrc, srcBody) {
		t.Fatalf("source object must be unaffected by a crashed copy")
	}
}

func TestCrash_CopyObjectAfterJournalSyncIsDurable(t *testing.T) {
	for _, directive := range []metadataDirective{metadataDirectiveCopy, metadataDirectiveReplace} {
		t.Run(fmt.Sprintf("directive=%d", directive), func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			srcBody := genRandomBytes(42, 50*1024)
			if _, err := store.PutObject("b", "src", srcBody, "text/plain", nil); err != nil {
				t.Fatal(err)
			}

			withTestHook(t, func(point string) {
				if point == hookAfterJournalSync {
					panic(simulatedCrash{point: point})
				}
			})
			req := CopyObjectRequest{SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "dst", Directive: directive}
			if directive == metadataDirectiveReplace {
				req.ContentType = "text/replaced"
			}
			runExpectingSimulatedCrash(t, func() {
				_, _, _ = store.CopyObject(req)
			})
			testHook = nil
			store.Close()

			store2, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store2.Close()
			_, gotDst, err := store2.GetObject("b", "dst")
			if err != nil {
				t.Fatalf("copy destination must be visible after restart: its journal frame was synced before the simulated crash: %v", err)
			}
			if !bytes.Equal(gotDst, srcBody) {
				t.Fatalf("copy destination bytes must match source after restart")
			}
		})
	}
}

// =============================================================================
// M3: single-range GET
// =============================================================================

func TestRange_VariousForms(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	body := genRandomBytes(321, 500*1024) // spans several CDC chunks
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/obj", body, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed: %d", put.StatusCode)
	}
	size := int64(len(body))

	cases := []struct {
		name               string
		rangeHeader        string
		wantStart, wantEnd int64
	}{
		{"one byte at start", "bytes=0-0", 0, 0},
		{"one byte in middle", fmt.Sprintf("bytes=%d-%d", size/2, size/2), size / 2, size / 2},
		{"one byte at end", fmt.Sprintf("bytes=%d-%d", size-1, size-1), size - 1, size - 1},
		{"explicit start/end", "bytes=100-199", 100, 199},
		{"open-ended start", fmt.Sprintf("bytes=%d-", size-500), size - 500, size - 1},
		{"suffix range", "bytes=-1000", size - 1000, size - 1},
		{"near a chunk boundary region", fmt.Sprintf("bytes=%d-%d", cdcMaxChunkSize-100, cdcMaxChunkSize+100), int64(cdcMaxChunkSize - 100), int64(cdcMaxChunkSize + 100)},
		{"clamp end beyond length", fmt.Sprintf("bytes=%d-%d", size-10, size+100000), size - 10, size - 1},
		{"whole object as an explicit range", fmt.Sprintf("bytes=0-%d", size-1), 0, size - 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/obj", nil, map[string]string{"Range": c.rangeHeader})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("expected 206, got %d", resp.StatusCode)
			}
			wantLen := c.wantEnd - c.wantStart + 1
			wantContentRange := fmt.Sprintf("bytes %d-%d/%d", c.wantStart, c.wantEnd, size)
			if got := resp.Header.Get("Content-Range"); got != wantContentRange {
				t.Fatalf("Content-Range: got %q want %q", got, wantContentRange)
			}
			if got := resp.Header.Get("Content-Length"); got != strconv.FormatInt(wantLen, 10) {
				t.Fatalf("Content-Length: got %q want %d", got, wantLen)
			}
			if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
				t.Fatalf("expected Accept-Ranges: bytes, got %q", got)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(data)) != wantLen {
				t.Fatalf("body length: got %d want %d", len(data), wantLen)
			}
			want := body[c.wantStart : c.wantEnd+1]
			if !bytes.Equal(data, want) {
				t.Fatalf("range body mismatch for %s", c.name)
			}
		})
	}
}

func TestRange_Unsatisfiable(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := []byte("short body")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/obj", body, nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/obj", nil, map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", len(body)+10, len(body)+20),
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", resp.StatusCode)
	}
	wantCR := fmt.Sprintf("bytes */%d", len(body))
	if got := resp.Header.Get("Content-Range"); got != wantCR {
		t.Fatalf("Content-Range: got %q want %q", got, wantCR)
	}
}

func TestRange_EmptyObjectUnsatisfiable(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/empty", []byte{}, nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/empty", nil, map[string]string{"Range": "bytes=0-0"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416 for a range on an empty object, got %d", resp.StatusCode)
	}
}

func TestRange_MalformedHeaderIgnoredServesFullObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello range world")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/obj", body, nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/obj", nil, map[string]string{"Range": "bytes=abc-def"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected a malformed Range header to be ignored (200), got %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(data, body) {
		t.Fatalf("expected the full body when Range is malformed")
	}
}

func TestRange_MultiRangeUnsupportedIgnoredServesFullObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello multi range world")
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/obj", body, nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/obj", nil, map[string]string{"Range": "bytes=0-1,3-4"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected an unsupported multi-range header to be ignored (200), got %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(data, body) {
		t.Fatalf("expected the full body when Range specifies multiple ranges")
	}
}

// TestRange_ReadsOnlyOverlappingChunks is a white-box test over a
// manually constructed 3-chunk object with known, exact boundaries: it
// proves readManifestRange reconstructs precisely the requested interval
// (here, straddling the A/B boundary) rather than the whole object.
func TestRange_ReadsOnlyOverlappingChunks(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	chunkA := bytes.Repeat([]byte{0xAA}, 1000)
	chunkB := bytes.Repeat([]byte{0xBB}, 2000)
	chunkC := bytes.Repeat([]byte{0xCC}, 3000)
	entry := putManualObject(t, s, "b", "obj", [][]byte{chunkA, chunkB, chunkC})
	man, err := s.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.readManifestRange(man, byteRange{start: 999, end: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, chunkA[999:]...), chunkB[:1]...)
	if !bytes.Equal(got, want) {
		t.Fatalf("range straddling the A/B boundary: got %v want %v", got, want)
	}

	got2, err := s.readManifestRange(man, byteRange{start: 3000, end: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, chunkC[:1]) {
		t.Fatalf("range at the exact start of chunk C: got %v want %v", got2, chunkC[:1])
	}
}
