package main

import (
	"bytes"
	"crypto/hmac"
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
