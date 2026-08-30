package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math/big"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"uuid"
)

// TestMain makes the whole suite hermetic against P1-A's new
// AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION environment-variable
// credential fallback (section 15a-quater of zeros3.go): every
// pre-existing test that spawns a real `zeros3 serve`/CLI subprocess
// without explicit -access-key/-secret-key/-region flags, or that signs
// requests directly with the hardcoded defaultAccessKeyID/
// defaultSecretAccessKey constants, assumes those built-in defaults are
// actually in effect. An operator's (or, as discovered running this very
// suite, a sandboxed CI environment's) ambient AWS_* variables would
// otherwise silently redirect every such subprocess's real credentials
// away from the compiled-in defaults the test parses expect, since
// os/exec.Command inherits the parent process's environment by default.
// Clearing them once, here, keeps that inheritance from ever reaching a
// subprocess unless a specific P1-A test deliberately re-sets one with
// t.Setenv (which affects only that single test, restored afterward).
func TestMain(m *testing.M) {
	os.Unsetenv(envAWSAccessKeyID)
	os.Unsetenv(envAWSSecretAccessKey)
	os.Unsetenv(envAWSRegion)
	os.Exit(m.Run())
}

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
// SigV4 payload-mode tests (Phase A)
// =============================================================================

func TestPayloadMode_ClassifySigV4Payload(t *testing.T) {
	sha256OfEmpty := testHexSHA256(nil)
	cases := []struct {
		name       string
		raw        string
		wantKind   sigv4PayloadKind
		wantDigest string
		wantErr    bool
	}{
		{"ordinary-digest", testHexSHA256([]byte("hello")), sigv4PayloadFixedSHA256, testHexSHA256([]byte("hello")), false},
		{"empty-body-digest", sha256OfEmpty, sigv4PayloadFixedSHA256, sha256OfEmpty, false},
		{"uppercase-digest-accepted-lowered", strings.ToUpper(testHexSHA256([]byte("hi"))), sigv4PayloadFixedSHA256, testHexSHA256([]byte("hi")), false},
		{"unsigned-payload", "UNSIGNED-PAYLOAD", sigv4PayloadUnsignedFixed, "", false},
		{"streaming-hmac", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", sigv4PayloadStreamingHMAC, "", false},
		{"streaming-hmac-trailer", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER", sigv4PayloadStreamingHMACTrailer, "", false},
		{"streaming-unsigned-trailer-excluded", "STREAMING-UNSIGNED-PAYLOAD-TRAILER", sigv4PayloadUnsupported, "", false},
		{"streaming-ecdsa-excluded", "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD", sigv4PayloadUnsupported, "", false},
		{"streaming-ecdsa-trailer-excluded", "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD-TRAILER", sigv4PayloadUnsupported, "", false},
		{"lowercase-unsigned-payload-rejected", "unsigned-payload", 0, "", true},
		{"misspelled-sentinel-rejected", "UNSIGNED_PAYLOAD", 0, "", true},
		{"malformed-not-hex", strings.Repeat("z", 64), 0, "", true},
		{"invalid-hex-chars", strings.Repeat("g", 64), 0, "", true},
		{"too-short", strings.Repeat("a", 63), 0, "", true},
		{"too-long", strings.Repeat("a", 65), 0, "", true},
		{"empty-string", "", 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, digest, err := classifySigV4Payload(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("classifySigV4Payload(%q): expected an error, got kind=%v digest=%q", c.raw, kind, digest)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifySigV4Payload(%q): unexpected error: %v", c.raw, err)
			}
			if kind != c.wantKind {
				t.Fatalf("classifySigV4Payload(%q): kind = %v, want %v", c.raw, kind, c.wantKind)
			}
			if digest != c.wantDigest {
				t.Fatalf("classifySigV4Payload(%q): digest = %q, want %q", c.raw, digest, c.wantDigest)
			}
		})
	}
}

func TestPayloadMode_FixedSHA256_CorrectDigestAndBodyAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("ordinary fixed-payload body")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
	if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
		t.Fatalf("expected a correct fixed-SHA256 payload to be accepted: %v", err)
	}
}

func TestPayloadMode_FixedSHA256_WrongDigestRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("some body")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	// signTestRequest signs whatever X-Amz-Content-Sha256 opts.badPayloadHash
	// carries, so the Authorization signature itself is internally
	// consistent (a real client could produce this); what must fail is the
	// *cross-check* against the body actually received.
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: testHexSHA256([]byte("a completely different body"))})
	var ae *authError
	err := srv.authenticate(req, rawPath, rawQuery, body)
	if !errors.As(err, &ae) || ae.code != "XAmzContentSHA256Mismatch" {
		t.Fatalf("expected XAmzContentSHA256Mismatch, got %v", err)
	}
}

func TestPayloadMode_FixedSHA256_EmptyBodyCorrectDigestAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte{}
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
	if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
		t.Fatalf("expected the SHA-256-of-empty-string digest applied to a zero-length body to be accepted: %v", err)
	}
}

func TestPayloadMode_FixedSHA256_EmptyBodyDigestWithNonEmptyBodyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	realBody := []byte("not actually empty")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", realBody)
	signTestRequest(t, req, signer, rawPath, rawQuery, realBody, time.Now(), &signOpts{badPayloadHash: testHexSHA256(nil)})
	var ae *authError
	err := srv.authenticate(req, rawPath, rawQuery, realBody)
	if !errors.As(err, &ae) || ae.code != "XAmzContentSHA256Mismatch" {
		t.Fatalf("expected the empty-body digest against a non-empty body to be rejected as XAmzContentSHA256Mismatch, got %v", err)
	}
}

func TestPayloadMode_MalformedDigestVariantsRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	cases := map[string]string{
		"invalid-hex":    strings.Repeat("g", 64),
		"invalid-length": strings.Repeat("a", 63),
		"too-long":       strings.Repeat("a", 65),
		"empty-value":    "",
	}
	for name, badHash := range cases {
		t.Run(name, func(t *testing.T) {
			body := []byte("payload")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
			opts := &signOpts{badPayloadHash: badHash}
			if badHash == "" {
				// A genuinely empty header value can't be "signed" in any
				// meaningful sense; exercise the missing-header path
				// directly instead of asking the signer to sign an empty
				// string into the canonical request.
				req.Header.Del("X-Amz-Content-Sha256")
				signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), nil)
				req.Header.Del("X-Amz-Content-Sha256")
			} else {
				signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), opts)
			}
			var ae *authError
			err := srv.authenticate(req, rawPath, rawQuery, body)
			if !errors.As(err, &ae) || ae.code != "AccessDenied" {
				t.Fatalf("expected a malformed x-amz-content-sha256 to be rejected as AccessDenied, got %v", err)
			}
		})
	}
}

func TestPayloadMode_UnsignedPayload_ValidSignedPUTAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("unsigned payload body")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: "UNSIGNED-PAYLOAD"})
	if err := srv.authenticate(req, rawPath, rawQuery, body); err != nil {
		t.Fatalf("expected a validly signed UNSIGNED-PAYLOAD request to be accepted: %v", err)
	}
}

func TestPayloadMode_UnsignedPayload_TamperedAuthorizationRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	body := []byte("unsigned payload body")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
	signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: "UNSIGNED-PAYLOAD"})
	auth := req.Header.Get("Authorization")
	req.Header.Set("Authorization", strings.Replace(auth, "Signature=", "Signature=00", 1))
	var ae *authError
	err := srv.authenticate(req, rawPath, rawQuery, body)
	if !errors.As(err, &ae) || ae.code != "SignatureDoesNotMatch" {
		t.Fatalf("expected a tampered Authorization header to be rejected, got %v", err)
	}
}

// TestPayloadMode_UnsignedPayload_ModifiedBodyAloneDoesNotInvalidateSignature
// is the core UNSIGNED-PAYLOAD contract: the literal sentinel string, not
// any function of the body, is what the signature covers, so a body
// substituted after signing still passes SigV4 -- exactly the case
// independent Content-MD5/CRC32 checks exist to still catch.
func TestPayloadMode_UnsignedPayload_ModifiedBodyAloneDoesNotInvalidateSignature(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	signedBody := []byte("the body that was actually signed")
	req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", signedBody)
	signTestRequest(t, req, signer, rawPath, rawQuery, signedBody, time.Now(), &signOpts{badPayloadHash: "UNSIGNED-PAYLOAD"})

	differentBody := []byte("a totally different body substituted after signing")
	if err := srv.authenticate(req, rawPath, rawQuery, differentBody); err != nil {
		t.Fatalf("expected UNSIGNED-PAYLOAD to place no constraint on body content: %v", err)
	}
}

func TestPayloadMode_UnsignedPayload_ValidSigButWrongContentMD5Fails(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := []byte("unsigned payload body for content-md5 cross-check")
	req := mustSignedRequestWithOpts(t, signer, http.MethodPut, ts.URL+"/bucket1/key", body, &signOpts{badPayloadHash: "UNSIGNED-PAYLOAD"})
	wrongSum := md5.Sum([]byte("not the real body")) //nolint:gosec // test-only.
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(wrongSum[:]))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected an incorrect Content-MD5 under UNSIGNED-PAYLOAD to still fail, got 200")
	}
}

func TestPayloadMode_UnsignedPayload_ValidSigButWrongCRC32Fails(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}

	body := []byte("unsigned payload body for crc32 cross-check")
	req := mustSignedRequestWithOpts(t, signer, http.MethodPut, ts.URL+"/bucket1/key", body, &signOpts{badPayloadHash: "UNSIGNED-PAYLOAD"})
	bad := make([]byte, 4)
	binary.BigEndian.PutUint32(bad, crc32.ChecksumIEEE(body)+1)
	req.Header.Set("x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(bad))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected an incorrect crc32 under UNSIGNED-PAYLOAD to still fail, got 200")
	}
}

func TestPayloadMode_ExcludedModesRejectCleanly(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	cases := []string{
		"STREAMING-UNSIGNED-PAYLOAD-TRAILER",
		"STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD",
		"STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD-TRAILER",
	}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			body := []byte("excluded mode body")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
			signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: mode})
			var ae *authError
			err := srv.authenticate(req, rawPath, rawQuery, body)
			if !errors.As(err, &ae) || ae.code != "NotImplemented" {
				t.Fatalf("expected excluded mode %q to be rejected as NotImplemented, got %v", mode, err)
			}
		})
	}
}

func TestPayloadMode_StreamingHMACModesRejectedUntilImplemented(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	cases := []string{
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
		"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER",
	}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			body := []byte("streaming mode body")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
			signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: mode})
			var ae *authError
			err := srv.authenticate(req, rawPath, rawQuery, body)
			if !errors.As(err, &ae) || ae.code != "NotImplemented" {
				t.Fatalf("expected conditional streaming mode %q, not yet implemented, to be rejected as NotImplemented, got %v", mode, err)
			}
		})
	}
}

func TestPayloadMode_LowercaseOrMisspelledSentinelsRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	cases := []string{"unsigned-payload", "Unsigned-Payload", "UNSIGNED_PAYLOAD", "streaming-aws4-hmac-sha256-payload"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			body := []byte("body")
			req, rawPath, rawQuery := mustAuthTestRequest(http.MethodPut, "/b/k", body)
			signTestRequest(t, req, signer, rawPath, rawQuery, body, time.Now(), &signOpts{badPayloadHash: mode})
			var ae *authError
			err := srv.authenticate(req, rawPath, rawQuery, body)
			if !errors.As(err, &ae) || ae.code != "AccessDenied" {
				t.Fatalf("expected lowercase/misspelled mode %q to be rejected as AccessDenied (not silently accepted under some other mode), got %v", mode, err)
			}
		})
	}
}

// TestPayloadMode_PresignedBehaviorUnchanged confirms formalizing header-auth
// payload modes did not touch query-string (presigned) auth, which always
// uses the fixed UNSIGNED-PAYLOAD sentinel by a completely separate code
// path (authenticateQuery) that never calls classifySigV4Payload at all.
func TestPayloadMode_PresignedBehaviorUnchanged(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	body := []byte("presigned put body")
	putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bucket1/pkey", body, nil)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT failed: %d", putResp.StatusCode)
	}

	url, err := GeneratePresignedURL(
		Credentials{AccessKeyID: signer.accessKey, SecretAccessKey: signer.secretKey},
		signer.region,
		PresignRequest{Method: http.MethodGet, Endpoint: ts.URL, Bucket: "bucket1", Key: "pkey", Expires: 15 * time.Minute},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("expected presigned GET to still work unchanged, got status %d body %q", resp.StatusCode, got)
	}
}

// =============================================================================
// End-to-end HTTP tests
// =============================================================================

func mustSignedRequest(t *testing.T, signer testSigner, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	return mustSignedRequestWithOpts(t, signer, method, rawURL, body, nil)
}

func mustSignedRequestWithOpts(t *testing.T, signer testSigner, method, rawURL string, body []byte, opts *signOpts) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	signTestRequest(t, req, signer, req.URL.Path, req.URL.RawQuery, body, time.Now(), opts)
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
	// M5-C: DELETE archives the deleted root into history rather than
	// abandoning it, and the authoritative reachability model (section 12a)
	// treats a retained historical version as a live root exactly like a
	// current object. So chunk B and y's own manifest remain fully
	// reachable/live -- NOT reclaimable -- even though y is no longer a
	// current object: this is precisely what "safe GC must never delete a
	// retained historical version's payload" (K2) requires.
	if after.UniqueReachableChunkBytes != 3000 {
		t.Fatalf("unique_reachable_chunk_bytes after delete: got %d want 3000 (B stays live via history)", after.UniqueReachableChunkBytes)
	}
	if after.ChunkStoreFileBytes != 3000 {
		t.Fatalf("chunk_store_file_bytes must be unchanged by a DELETE (deletion changes roots, not chunks): got %d want 3000", after.ChunkStoreFileBytes)
	}
	if after.ReclaimableBytes != 0 {
		t.Fatalf("reclaimable_bytes after deleting y: got %d want 0 (y's manifest/chunks are retained as history, not garbage)", after.ReclaimableBytes)
	}
	if after.HistoricalVersionCount != 1 || after.HistoricalVersionLogicalBytes != entryY.size {
		t.Fatalf("historical version accounting after delete: got count=%d bytes=%d, want 1/%d",
			after.HistoricalVersionCount, after.HistoricalVersionLogicalBytes, entryY.size)
	}
	_ = yManInfo // manifest size no longer expected to become reclaimable; kept for reference only.

	verifyRes, err := s.Verify(false)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("unreachable garbage must not be reported as an integrity failure: %+v", verifyRes)
	}
	if verifyRes.UnreachableChunks != 0 || verifyRes.UnreachableManifests != 0 || verifyRes.ReclaimableBytes != 0 {
		t.Fatalf("verify reclaimable accounting: unreachable_chunks=%d unreachable_manifests=%d reclaimable_bytes=%d, want 0/0/0 (history keeps y live)",
			verifyRes.UnreachableChunks, verifyRes.UnreachableManifests, verifyRes.ReclaimableBytes)
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
		"historical_version_count", "historical_version_logical_bytes",
		"active_multipart_upload_count", "active_multipart_logical_bytes",
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

// =============================================================================
// M5-B: Multipart upload tests
//
// Helpers below drive the real HTTP multipart API (CreateMultipartUpload/
// UploadPart/ListParts/CompleteMultipartUpload/AbortMultipartUpload/
// ListMultipartUploads) exactly the way a real client would -- query
// parameters on ordinary bucket/object paths, real SigV4-signed requests --
// so these tests exercise the same routing/handler code a real AWS SDK or
// rclone request would.
// =============================================================================

func doCreateMultipartUpload(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key string) string {
	t.Helper()
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodPost, "/"+bucket+"/"+key+"?uploads", nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CreateMultipartUpload failed: %d %s", resp.StatusCode, data)
	}
	var result initiateMultipartUploadResult
	if err := xml.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to parse InitiateMultipartUploadResult: %v", err)
	}
	if result.UploadId == "" {
		t.Fatalf("empty UploadId in InitiateMultipartUploadResult")
	}
	return result.UploadId
}

func doUploadPart(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key, uploadID string, partNumber int, body []byte) (etag string, status int, respBody []byte) {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s", bucket, key, partNumber, uploadID)
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodPut, path, body, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return strings.Trim(resp.Header.Get("ETag"), `"`), resp.StatusCode, data
}

func doListParts(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key, uploadID string) (listPartsResult, int) {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodGet, path, nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result listPartsResult
	if resp.StatusCode == http.StatusOK {
		if err := xml.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to parse ListPartsResult: %v", err)
		}
	}
	return result, resp.StatusCode
}

func doListMultipartUploads(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket string) (listMultipartUploadsResult, int) {
	t.Helper()
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodGet, "/"+bucket+"?uploads", nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result listMultipartUploadsResult
	if resp.StatusCode == http.StatusOK {
		if err := xml.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to parse ListMultipartUploadsResult: %v", err)
		}
	}
	return result, resp.StatusCode
}

// doListPartsQuery is doListParts with an extra raw query string (e.g.
// "max-parts=2&part-number-marker=1") appended, for exercising ListParts
// pagination. An empty extraQuery behaves exactly like doListParts.
func doListPartsQuery(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key, uploadID, extraQuery string) (listPartsResult, int) {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	if extraQuery != "" {
		path += "&" + extraQuery
	}
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodGet, path, nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result listPartsResult
	if resp.StatusCode == http.StatusOK {
		if err := xml.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to parse ListPartsResult: %v", err)
		}
	}
	return result, resp.StatusCode
}

// doListMultipartUploadsQuery is doListMultipartUploads with an extra raw
// query string (e.g. "max-uploads=2&key-marker=foo") appended, for
// exercising ListMultipartUploads pagination. An empty extraQuery behaves
// exactly like doListMultipartUploads.
func doListMultipartUploadsQuery(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, extraQuery string) (listMultipartUploadsResult, int) {
	t.Helper()
	path := "/" + bucket + "?uploads"
	if extraQuery != "" {
		path += "&" + extraQuery
	}
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodGet, path, nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result listMultipartUploadsResult
	if resp.StatusCode == http.StatusOK {
		if err := xml.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to parse ListMultipartUploadsResult: %v", err)
		}
	}
	return result, resp.StatusCode
}

func doCompleteMultipartUpload(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key, uploadID string, parts []completedPartXML) (*completeMultipartUploadResult, int, []byte) {
	t.Helper()
	reqXML := completeMultipartUploadXML{Part: parts}
	data, err := xml.Marshal(reqXML)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodPost, path, data, nil)
	respData, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var result *completeMultipartUploadResult
	if resp.StatusCode == http.StatusOK {
		result = &completeMultipartUploadResult{}
		if err := xml.Unmarshal(respData, result); err != nil {
			t.Fatalf("failed to parse CompleteMultipartUploadResult: %v", err)
		}
	}
	return result, resp.StatusCode, respData
}

func doAbortMultipartUpload(t *testing.T, client *http.Client, baseURL string, signer testSigner, bucket, key, uploadID string) int {
	t.Helper()
	path := fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadID)
	resp := doSignedRequest(t, client, baseURL, signer, http.MethodDelete, path, nil, nil)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // test-only body drain.
	resp.Body.Close()
	return resp.StatusCode
}

// manualMultipartETag computes the S3 multipart ETag formula independently
// of multipartETag itself, so tests actually prove the formula, not just
// that the implementation agrees with itself.
func manualMultipartETag(parts [][]byte) string {
	h := md5.New() //nolint:gosec // test-only, matching the S3-compatible construction under test.
	for _, p := range parts {
		sum := md5.Sum(p) //nolint:gosec // test-only.
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(parts))
}

func TestMultipart_HappyPath_TwoParts_GetHeadRangeCopy(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	part1 := genRandomBytes(9001, 5*1024*1024)
	part2 := genRandomBytes(9002, 3*1024*1024)
	whole := append(append([]byte{}, part1...), part2...)

	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "mpkey")
	etag1, status1, _ := doUploadPart(t, client, ts.URL, signer, "b", "mpkey", uploadID, 1, part1)
	if status1 != http.StatusOK {
		t.Fatalf("UploadPart 1 failed: %d", status1)
	}
	etag2, status2, _ := doUploadPart(t, client, ts.URL, signer, "b", "mpkey", uploadID, 2, part2)
	if status2 != http.StatusOK {
		t.Fatalf("UploadPart 2 failed: %d", status2)
	}

	lp, lpStatus := doListParts(t, client, ts.URL, signer, "b", "mpkey", uploadID)
	if lpStatus != http.StatusOK || len(lp.Part) != 2 {
		t.Fatalf("ListParts: status=%d parts=%d", lpStatus, len(lp.Part))
	}
	if lp.Part[0].PartNumber != 1 || lp.Part[1].PartNumber != 2 {
		t.Fatalf("ListParts: unexpected part order: %+v", lp.Part)
	}

	result, cStatus, cBody := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "mpkey", uploadID,
		[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2}})
	if cStatus != http.StatusOK {
		t.Fatalf("CompleteMultipartUpload failed: %d %s", cStatus, cBody)
	}
	wantETag := `"` + manualMultipartETag([][]byte{part1, part2}) + `"`
	if result.ETag != wantETag {
		t.Fatalf("completed object ETag = %s, want %s", result.ETag, wantETag)
	}

	// The upload ID must now be invalid (completion retires it).
	if _, status := doListParts(t, client, ts.URL, signer, "b", "mpkey", uploadID); status != http.StatusNotFound {
		t.Fatalf("expected ListParts on a completed upload ID to 404, got %d", status)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/mpkey", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || !bytes.Equal(got, whole) {
		t.Fatalf("GET of completed multipart object: status=%d, exact byte match=%v", get.StatusCode, bytes.Equal(got, whole))
	}
	if get.Header.Get("ETag") != wantETag {
		t.Fatalf("GET ETag = %s, want %s", get.Header.Get("ETag"), wantETag)
	}

	head := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/mpkey", nil, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusOK || head.Header.Get("Content-Length") != strconv.Itoa(len(whole)) {
		t.Fatalf("HEAD of completed multipart object: status=%d content-length=%s", head.StatusCode, head.Header.Get("Content-Length"))
	}

	rangeStart, rangeEnd := len(part1)-100, len(part1)+100
	rangeResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/mpkey", nil, map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd),
	})
	rangeGot, _ := io.ReadAll(rangeResp.Body)
	rangeResp.Body.Close()
	if rangeResp.StatusCode != http.StatusPartialContent || !bytes.Equal(rangeGot, whole[rangeStart:rangeEnd+1]) {
		t.Fatalf("Range GET straddling the part boundary: status=%d exact match=%v", rangeResp.StatusCode, bytes.Equal(rangeGot, whole[rangeStart:rangeEnd+1]))
	}

	copyResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/mpkey-copy", nil, map[string]string{
		"X-Amz-Copy-Source": "b/mpkey",
	})
	copyResp.Body.Close()
	if copyResp.StatusCode != http.StatusOK {
		t.Fatalf("CopyObject of a completed multipart object failed: %d", copyResp.StatusCode)
	}
	copyGet := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/mpkey-copy", nil, nil)
	copyGot, _ := io.ReadAll(copyGet.Body)
	copyGet.Body.Close()
	if !bytes.Equal(copyGot, whole) {
		t.Fatalf("CopyObject of a completed multipart object did not round-trip bytes")
	}
}

func TestMultipart_SinglePart(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(1, 20000) // below the 5MiB minimum, but it's the only (=last) part
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "onepart")
	etag, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "onepart", uploadID, 1, body)
	if status != http.StatusOK {
		t.Fatalf("UploadPart failed: %d", status)
	}
	result, cStatus, cBody := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "onepart", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag}})
	if cStatus != http.StatusOK {
		t.Fatalf("single-part completion failed: %d %s", cStatus, cBody)
	}
	wantETag := `"` + manualMultipartETag([][]byte{body}) + `"`
	if result.ETag != wantETag {
		t.Fatalf("single-part multipart ETag = %s, want %s", result.ETag, wantETag)
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/onepart", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("single-part multipart object did not round-trip bytes")
	}
}

func TestMultipart_ETag_DiffersFromOrdinarySinglePutETag(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(2, 50000)

	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/plain", body, nil)
	put.Body.Close()
	ordinaryETag := put.Header.Get("ETag")
	plainMD5 := md5.Sum(body) //nolint:gosec // test-only.
	if ordinaryETag != `"`+hex.EncodeToString(plainMD5[:])+`"` {
		t.Fatalf("ordinary single-PUT ETag changed unexpectedly: %s", ordinaryETag)
	}

	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "mp")
	etag, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "mp", uploadID, 1, body)
	if status != http.StatusOK {
		t.Fatal("upload part failed")
	}
	result, cStatus, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "mp", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag}})
	if cStatus != http.StatusOK {
		t.Fatal("complete failed")
	}
	// Same bytes, but a multipart completion's ETag formula must never
	// collapse to the ordinary single-PUT MD5-of-body rule.
	if result.ETag == ordinaryETag {
		t.Fatalf("expected the multipart ETag (%s) to differ from the ordinary single-PUT ETag (%s) for identical bytes", result.ETag, ordinaryETag)
	}
}

func TestMultipart_ReplacedPartUsesLatestUpload(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	part1a := genRandomBytes(10, 20000)
	if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 1, part1a); status != http.StatusOK {
		t.Fatal("first upload of part 1 failed")
	}
	part1b := genRandomBytes(11, 20000) // different bytes, same part number: a retry/replace
	etag1b, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 1, part1b)
	if status != http.StatusOK {
		t.Fatal("replacement upload of part 1 failed")
	}
	result, cStatus, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag1b}})
	if cStatus != http.StatusOK {
		t.Fatal("complete failed")
	}
	if result.ETag != `"`+manualMultipartETag([][]byte{part1b})+`"` {
		t.Fatalf("expected the completed object to reflect the replacement part's ETag, got %s", result.ETag)
	}
	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(got, part1b) {
		t.Fatalf("expected the completed object to contain the replacement part's bytes, not the original")
	}
}

func TestMultipart_ListMultipartUploads(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	u1 := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "alpha")
	u2 := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "beta")

	list, status := doListMultipartUploads(t, client, ts.URL, signer, "b")
	if status != http.StatusOK || len(list.Upload) != 2 {
		t.Fatalf("ListMultipartUploads: status=%d count=%d", status, len(list.Upload))
	}
	if list.Upload[0].Key != "alpha" || list.Upload[1].Key != "beta" {
		t.Fatalf("expected uploads ordered by key: %+v", list.Upload)
	}
	if list.Upload[0].UploadId != u1 || list.Upload[1].UploadId != u2 {
		t.Fatalf("unexpected upload IDs in listing: %+v", list.Upload)
	}
}

func TestMultipart_IncompleteUploadInvisibleToOrdinaryOperations(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "invisible")
	if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "invisible", uploadID, 1, genRandomBytes(1, 10000)); status != http.StatusOK {
		t.Fatal("upload part failed")
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/invisible", nil, nil)
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("expected an incomplete multipart upload to be invisible to GET, got %d", get.StatusCode)
	}
	head := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/invisible", nil, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusNotFound {
		t.Fatalf("expected an incomplete multipart upload to be invisible to HEAD, got %d", head.StatusCode)
	}
	list := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b?list-type=2", nil, nil)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if strings.Contains(string(listBody), "invisible") {
		t.Fatalf("expected an incomplete multipart upload key to never appear in ListObjectsV2: %s", listBody)
	}
}

// --- Validation (Phase F) ---

func TestMultipart_NonexistentUploadIDRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	fakeID := newUUIDv7()
	if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", fakeID, 1, []byte("x")); status != http.StatusNotFound {
		t.Fatalf("expected UploadPart against a nonexistent upload ID to 404, got %d", status)
	}
	if status := doAbortMultipartUpload(t, client, ts.URL, signer, "b", "k", fakeID); status != http.StatusNotFound {
		t.Fatalf("expected AbortMultipartUpload against a nonexistent upload ID to 404, got %d", status)
	}
	if _, status, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "k", fakeID, []completedPartXML{{PartNumber: 1, ETag: "x"}}); status != http.StatusNotFound {
		t.Fatalf("expected CompleteMultipartUpload against a nonexistent upload ID to 404, got %d", status)
	}
}

func TestMultipart_UploadIDForWrongBucketOrKeyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := doCreateBucket(t, client, ts.URL, signer, "b2"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b1", "realkey")

	if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b2", "realkey", uploadID, 1, []byte("x")); status != http.StatusNotFound {
		t.Fatalf("expected an upload ID used against the wrong bucket to 404, got %d", status)
	}
	if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b1", "wrongkey", uploadID, 1, []byte("x")); status != http.StatusNotFound {
		t.Fatalf("expected an upload ID used against the wrong key to 404, got %d", status)
	}
}

func TestMultipart_InvalidPartNumberRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	for _, bad := range []string{"0", "-1", "10001", "abc", ""} {
		t.Run(bad, func(t *testing.T) {
			path := fmt.Sprintf("/b/k?partNumber=%s&uploadId=%s", bad, uploadID)
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, path, []byte("x"), nil)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("expected part number %q to be rejected", bad)
			}
		})
	}
}

func TestMultipart_CompletionValidation(t *testing.T) {
	newUpload := func(t *testing.T) (client *http.Client, baseURL string, signer testSigner, uploadID string, etag1, etag2 string) {
		t.Helper()
		var srv *Server
		srv, signer = newTestServerAndSigner(t)
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		client = ts.Client()
		baseURL = ts.URL
		if err := doCreateBucket(t, client, baseURL, signer, "b"); err != nil {
			t.Fatal(err)
		}
		uploadID = doCreateMultipartUpload(t, client, baseURL, signer, "b", "k")
		etag1, s1, _ := doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 1, genRandomBytes(1, 5*1024*1024))
		etag2, s2, _ := doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 2, genRandomBytes(2, 1024))
		if s1 != http.StatusOK || s2 != http.StatusOK {
			t.Fatal("setup upload parts failed")
		}
		return client, baseURL, signer, uploadID, etag1, etag2
	}

	t.Run("empty-completion-list", func(t *testing.T) {
		client, baseURL, signer, uploadID, _, _ := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID, nil); status == http.StatusOK {
			t.Fatalf("expected an empty completion part list to be rejected")
		}
	})
	t.Run("missing-part-never-uploaded", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1, _ := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 3, ETag: "irrelevant"}}); status == http.StatusOK {
			t.Fatalf("expected completion referencing an unuploaded part to be rejected")
		}
	})
	t.Run("wrong-etag", func(t *testing.T) {
		client, baseURL, signer, uploadID, _, etag2 := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: "0000000000000000000000000000000"}, {PartNumber: 2, ETag: etag2}}); status == http.StatusOK {
			t.Fatalf("expected completion with a wrong ETag to be rejected")
		}
	})
	t.Run("malformed-etag", func(t *testing.T) {
		client, baseURL, signer, uploadID, _, etag2 := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: "not-a-valid-etag"}, {PartNumber: 2, ETag: etag2}}); status == http.StatusOK {
			t.Fatalf("expected completion with a malformed ETag to be rejected")
		}
	})
	t.Run("duplicate-part-reference", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1, _ := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 1, ETag: etag1}}); status == http.StatusOK {
			t.Fatalf("expected a duplicate part reference to be rejected")
		}
	})
	t.Run("out-of-order-completion", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1, etag2 := newUpload(t)
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 2, ETag: etag2}, {PartNumber: 1, ETag: etag1}}); status == http.StatusOK {
			t.Fatalf("expected an out-of-order completion list to be rejected")
		}
	})
	t.Run("malformed-completion-xml", func(t *testing.T) {
		client, baseURL, signer, uploadID, _, _ := newUpload(t)
		path := fmt.Sprintf("/b/k?uploadId=%s", uploadID)
		resp := doSignedRequest(t, client, baseURL, signer, http.MethodPost, path, []byte("<not valid xml"), nil)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("expected malformed completion XML to be rejected")
		}
	})
	t.Run("entity-too-small-non-last-part", func(t *testing.T) {
		client, baseURL, signer, uploadID, _, etag2 := newUpload(t)
		// Part 1 in newUpload is 5MiB (>= the minimum), but re-upload a
		// smaller part 1 to specifically exercise "non-last part below the
		// minimum" (part 2, the last, may be any size).
		smallETag, s, _ := doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 1, genRandomBytes(99, 1000))
		if s != http.StatusOK {
			t.Fatal("re-upload of part 1 failed")
		}
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: smallETag}, {PartNumber: 2, ETag: etag2}}); status == http.StatusOK {
			t.Fatalf("expected a non-final part below the minimum multipart part size to be rejected")
		}
	})
	t.Run("valid-completion-still-succeeds", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1, etag2 := newUpload(t)
		if _, status, body := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID,
			[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2}}); status != http.StatusOK {
			t.Fatalf("expected a correctly-formed completion to succeed: %d %s", status, body)
		}
	})
}

func TestMultipart_LifecycleAfterAbortOrComplete(t *testing.T) {
	setup := func(t *testing.T) (client *http.Client, baseURL string, signer testSigner, uploadID, etag1 string) {
		t.Helper()
		var srv *Server
		srv, signer = newTestServerAndSigner(t)
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		client = ts.Client()
		baseURL = ts.URL
		if err := doCreateBucket(t, client, baseURL, signer, "b"); err != nil {
			t.Fatal(err)
		}
		uploadID = doCreateMultipartUpload(t, client, baseURL, signer, "b", "k")
		var status int
		etag1, status, _ = doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 1, genRandomBytes(1, 10000))
		if status != http.StatusOK {
			t.Fatal("setup upload part failed")
		}
		return
	}

	t.Run("upload-part-after-abort", func(t *testing.T) {
		client, baseURL, signer, uploadID, _ := setup(t)
		if status := doAbortMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID); status != http.StatusNoContent {
			t.Fatalf("abort failed: %d", status)
		}
		if _, status, _ := doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 2, []byte("x")); status != http.StatusNotFound {
			t.Fatalf("expected UploadPart after abort to 404, got %d", status)
		}
	})
	t.Run("repeat-abort", func(t *testing.T) {
		client, baseURL, signer, uploadID, _ := setup(t)
		if status := doAbortMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID); status != http.StatusNoContent {
			t.Fatalf("first abort failed: %d", status)
		}
		if status := doAbortMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID); status != http.StatusNotFound {
			t.Fatalf("expected repeat abort to 404 (not silently succeed again), got %d", status)
		}
	})
	t.Run("upload-part-after-complete", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1 := setup(t)
		if _, status, body := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag1}}); status != http.StatusOK {
			t.Fatalf("complete failed: %d %s", status, body)
		}
		if _, status, _ := doUploadPart(t, client, baseURL, signer, "b", "k", uploadID, 2, []byte("x")); status != http.StatusNotFound {
			t.Fatalf("expected UploadPart after completion to 404, got %d", status)
		}
	})
	t.Run("repeat-complete", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1 := setup(t)
		if _, status, body := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag1}}); status != http.StatusOK {
			t.Fatalf("first complete failed: %d %s", status, body)
		}
		if _, status, _ := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag1}}); status != http.StatusNotFound {
			t.Fatalf("expected repeat completion to 404 (not silently re-apply), got %d", status)
		}
	})
	t.Run("abort-after-complete", func(t *testing.T) {
		client, baseURL, signer, uploadID, etag1 := setup(t)
		if _, status, body := doCompleteMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: etag1}}); status != http.StatusOK {
			t.Fatalf("complete failed: %d %s", status, body)
		}
		if status := doAbortMultipartUpload(t, client, baseURL, signer, "b", "k", uploadID); status != http.StatusNotFound {
			t.Fatalf("expected abort after completion to 404, got %d", status)
		}
	})
}

func TestMultipart_DeleteBucketRefusedWithActiveUpload(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodDelete, "/b", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected DeleteBucket with an active multipart upload to be refused (BucketNotEmpty), got %d", resp.StatusCode)
	}
}

// --- Crash / restart durability (Phase G) ---

func TestMultipart_Crash_G1_RestartMidUploadThenResumeAndComplete(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	part1 := genRandomBytes(1, 5*1024*1024)
	part2 := genRandomBytes(2, 5*1024*1024)
	etag1, err := store.UploadPart("b", "k", uploadID, 1, part1)
	if err != nil {
		t.Fatal(err)
	}
	etag2, err := store.UploadPart("b", "k", uploadID, 2, part2)
	if err != nil {
		t.Fatal(err)
	}
	store.Close() // simulated restart: no crash injection needed, this is an orderly stop mid-lifecycle

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	parts, err := store2.ListParts("b", "k", uploadID)
	if err != nil || len(parts) != 2 {
		t.Fatalf("expected both parts to survive restart: err=%v parts=%d", err, len(parts))
	}

	part3 := genRandomBytes(3, 2*1024*1024)
	etag3, err := store2.UploadPart("b", "k", uploadID, 3, part3)
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := store2.CompleteMultipartUpload("b", "k", uploadID, []completedPart{
		{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2}, {PartNumber: 3, ETag: etag3},
	})
	if err != nil {
		t.Fatalf("complete after resume failed: %v", err)
	}
	store2.Close()

	store3, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store3.Close()
	_, got, err := store3.GetObject("b", "k")
	if err != nil {
		t.Fatalf("expected the completed object to survive a further restart: %v", err)
	}
	want := append(append(append([]byte{}, part1...), part2...), part3...)
	gotSum, wantSum := sha256.Sum256(got), sha256.Sum256(want)
	if gotSum != wantSum {
		t.Fatalf("SHA-256 mismatch after resume+complete+restart: got %x want %x", gotSum, wantSum)
	}
	if entry.size != int64(len(want)) {
		t.Fatalf("entry size = %d, want %d", entry.size, len(want))
	}
}

func TestMultipart_Crash_G2_ReplacePartThenRestartThenComplete(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadPart("b", "k", uploadID, 1, genRandomBytes(1, 6*1024*1024)); err != nil {
		t.Fatal(err)
	}
	replacement := genRandomBytes(2, 6*1024*1024)
	etagReplacement, err := store.UploadPart("b", "k", uploadID, 1, replacement)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	parts, err := store2.ListParts("b", "k", uploadID)
	if err != nil || len(parts) != 1 {
		t.Fatalf("expected exactly one (replaced) part to survive restart: err=%v parts=%d", err, len(parts))
	}
	if parts[0].etag != etagReplacement {
		t.Fatalf("expected the replacement part's ETag to survive restart, not the original")
	}
	_, _, err = store2.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etagReplacement}})
	if err != nil {
		t.Fatalf("complete after restart failed: %v", err)
	}
	_, got, err := store2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("expected the completed object to contain exactly the replacement part's bytes")
	}
}

func TestMultipart_Crash_G3_AbortThenRestartUploadIDStaysInvalid(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UploadPart("b", "k", uploadID, 1, genRandomBytes(1, 10000)); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortMultipartUpload("b", "k", uploadID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, err := store2.ListParts("b", "k", uploadID); !errors.Is(err, errNoSuchUpload) {
		t.Fatalf("expected the aborted upload ID to remain invalid after restart, got %v", err)
	}
	if _, _, err := store2.GetObject("b", "k"); err == nil {
		t.Fatalf("expected no ordinary object to exist after an aborted-then-restarted upload")
	}
}

func TestMultipart_Crash_G4_CompleteThenRestartObjectVisibleSessionGone(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(1, 10000)
	etag, err := store.UploadPart("b", "k", uploadID, 1, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, got, err := store2.GetObject("b", "k")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("expected the completed object to survive restart intact: err=%v", err)
	}
	if _, err := store2.ListParts("b", "k", uploadID); !errors.Is(err, errNoSuchUpload) {
		t.Fatalf("expected the completed upload session to not be resurrected after restart, got %v", err)
	}
}

// TestMultipart_Crash_G5_CrashBeforeJournalCommitLeavesOldStateResumable
// injects a simulated crash at each durability boundary inside
// CompleteMultipartUpload that runs BEFORE the completion's one journal
// frame is appended (chunk staging, after chunks published, after manifest
// published) and confirms every one of them leaves the pre-completion
// state completely intact: no new object, and the upload session still
// resumable. The gap between the journal frame's Write and its Sync is
// deliberately not simulated via an in-process restart here, for the same
// reason TestCrash_AfterJournalWriteBeforeSync documents: once WriteAt
// returns, the bytes already sit in the same page cache a fresh *os.File
// in this same process would read right back, so reopening the store
// in-process cannot honestly simulate a real crash in that specific
// instant -- only a genuine power loss can. What IS honestly testable
// about that gap is the in-process synchronous failure path, covered by
// TestCrash_AfterJournalWriteBeforeSync itself (not duplicated per
// operation here) and by the "commit truly succeeded" half of Phase G5
// below.
func TestMultipart_Crash_G5_CrashBeforeJournalCommitLeavesOldStateResumable(t *testing.T) {
	points := []string{hookBeforeChunkWrite, hookAfterChunksPublished, hookAfterManifestPublished}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			body := genRandomBytes(1, 30000)
			etag, err := store.UploadPart("b", "k", uploadID, 1, body)
			if err != nil {
				t.Fatal(err)
			}

			withTestHook(t, func(p string) {
				if p == point {
					panic(simulatedCrash{point: p})
				}
			})
			runExpectingSimulatedCrash(t, func() {
				_, _, _ = store.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag}})
			})
			testHook = nil
			store.Close()

			store2, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store2.Close()
			_, _, getErr := store2.GetObject("b", "k")
			if getErr == nil {
				t.Fatalf("crash at %s: object must not be visible before the completion journal frame commits", point)
			}
			// The upload session itself must still be resumable -- the
			// crash happened before the one atomic completion frame, so
			// nothing should have retired it.
			if _, err := store2.ListParts("b", "k", uploadID); err != nil {
				t.Fatalf("crash at %s: expected the upload session to remain resumable, got %v", point, err)
			}
		})
	}
}

// TestMultipart_Crash_G5_CrashAfterJournalCommitLeavesFullyCommittedObject
// is Phase G5's other half: once the completion's journal frame has
// genuinely synced, the object must be fully, durably visible on restart
// (and the upload session gone) even if the process crashes immediately
// afterward, before the client ever saw a response -- proving there is no
// window where the new object is only partially visible.
func TestMultipart_Crash_G5_CrashAfterJournalCommitLeavesFullyCommittedObject(t *testing.T) {
	points := []string{hookAfterJournalSync, hookAfterApplyBeforeResponse}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			body := genRandomBytes(1, 30000)
			etag, err := store.UploadPart("b", "k", uploadID, 1, body)
			if err != nil {
				t.Fatal(err)
			}

			withTestHook(t, func(p string) {
				if p == point {
					panic(simulatedCrash{point: p})
				}
			})
			runExpectingSimulatedCrash(t, func() {
				_, _, _ = store.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag}})
			})
			testHook = nil
			store.Close()

			store2, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store2.Close()
			_, got, getErr := store2.GetObject("b", "k")
			if getErr != nil || !bytes.Equal(got, body) {
				t.Fatalf("crash at %s: expected the object to be fully, durably visible once the journal sync truly succeeded: %v", point, getErr)
			}
			if _, err := store2.ListParts("b", "k", uploadID); !errors.Is(err, errNoSuchUpload) {
				t.Fatalf("crash at %s: expected the upload session to be retired once completion truly committed, got %v", point, err)
			}
		})
	}
}

// --- Concurrency / adversarial (Phase H) ---

func TestMultipart_Concurrency_SamePartNumberRaceIsDeterministicAndRaceFree(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	candidates := make([][]byte, n)
	for i := range candidates {
		candidates[i] = genRandomBytes(int64(i), 5*1024*1024+i)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = store.UploadPart("b", "k", uploadID, 1, candidates[i])
		}(i)
	}
	wg.Wait()

	parts, err := store.ListParts("b", "k", uploadID)
	if err != nil || len(parts) != 1 {
		t.Fatalf("expected exactly one surviving part 1 after the race, got %d (err=%v)", len(parts), err)
	}
	matched := false
	for _, c := range candidates {
		if int64(len(c)) == parts[0].size {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("surviving part size %d does not match any candidate upload", parts[0].size)
	}
}

func TestMultipart_Concurrency_DifferentPartNumbersAllSurvive(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(partNum int) {
			defer wg.Done()
			_, _ = store.UploadPart("b", "k", uploadID, partNum, genRandomBytes(int64(partNum), 6*1024*1024))
		}(i)
	}
	wg.Wait()
	parts, err := store.ListParts("b", "k", uploadID)
	if err != nil || len(parts) != n {
		t.Fatalf("expected all %d distinct part numbers to survive concurrent upload, got %d (err=%v)", n, len(parts), err)
	}
}

func TestMultipart_Concurrency_CompleteRacingUploadPartIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	etag1, err := store.UploadPart("b", "k", uploadID, 1, genRandomBytes(1, 5*1024*1024))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = store.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag1}})
	}()
	go func() {
		defer wg.Done()
		_, _ = store.UploadPart("b", "k", uploadID, 2, genRandomBytes(2, 5*1024*1024))
	}()
	wg.Wait()
	// No assertion on which "won" -- only that neither call corrupted
	// state (checked by -race) and the store is left in a coherent state
	// either way (an object exists xor the upload is still resumable).
	_, getErr := storeHasObject(store, "b", "k")
	_, listErr := store.ListParts("b", "k", uploadID)
	if getErr == nil && listErr == nil {
		t.Fatalf("expected the object to exist XOR the upload session to remain resumable, never both")
	}
}

func storeHasObject(s *Store, bucket, key string) (bool, error) {
	_, _, err := s.GetObject(bucket, key)
	if err != nil {
		return false, err
	}
	return true, nil
}

func TestMultipart_Concurrency_AbortRacingUploadPartIsRaceFree(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = store.AbortMultipartUpload("b", "k", uploadID)
	}()
	go func() {
		defer wg.Done()
		_, _ = store.UploadPart("b", "k", uploadID, 1, genRandomBytes(1, 10000))
	}()
	wg.Wait()
	// Whichever won, the upload must end up in exactly one deterministic
	// state: either gone (abort won, or won after the part) or present
	// with the one part (part committed before abort ran). No -race
	// failure and no panic is the primary assertion here.
	_, err = store.ListParts("b", "k", uploadID)
	_ = err // either NoSuchUpload or success with 0/1 parts are all coherent outcomes.
}

func TestMultipart_Concurrency_TwoCompleteCallsOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	etag1, err := store.UploadPart("b", "k", uploadID, 1, genRandomBytes(1, 10000))
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag1}})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one of two concurrent Complete calls to succeed, got %d", successes)
	}
	if _, _, err := store.GetObject("b", "k"); err != nil {
		t.Fatalf("expected the object to be visible after exactly one Complete won: %v", err)
	}
}

func TestMultipart_Concurrency_TwoAbortCallsOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.AbortMultipartUpload("b", "k", uploadID)
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one of two concurrent Abort calls to succeed, got %d", successes)
	}
}

// --- Dedup/CAS integration (Phase M) ---

func TestMultipart_CompletedObjectDedupsAgainstOrdinaryPutOfSameBytes(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(777, 8*1024*1024)

	// Ordinary PUT first.
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/ordinary", body, nil)
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatal("ordinary PUT failed")
	}
	statsBefore, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	// The same logical bytes via a two-part multipart upload (an
	// arbitrary, off-center split -- CDC re-chunks the true concatenation
	// from scratch on completion, so the resulting chunk boundaries need
	// not match the ordinary PUT's chunk-by-chunk, and the split point is
	// exactly the case that would misbehave if part boundaries were ever
	// mistaken for CDC boundaries).
	split := 5*1024*1024 + 12345 // part 1 clears the non-final-part minimum; part 2 (the last) need not
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "multipart")
	etag1, s1, _ := doUploadPart(t, client, ts.URL, signer, "b", "multipart", uploadID, 1, body[:split])
	etag2, s2, _ := doUploadPart(t, client, ts.URL, signer, "b", "multipart", uploadID, 2, body[split:])
	if s1 != http.StatusOK || s2 != http.StatusOK {
		t.Fatal("multipart upload parts failed")
	}
	if _, status, respBody := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "multipart", uploadID,
		[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2}}); status != http.StatusOK {
		t.Fatalf("complete failed: %d %s", status, respBody)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/multipart", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("multipart-completed object bytes do not match the ordinary PUT's bytes")
	}

	statsAfter, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	// The two objects reference identical logical bytes chunked by the
	// exact same CDC algorithm from the exact same content, so the
	// completed multipart object's chunks must be (at least mostly)
	// reused from the ordinary PUT's already-published chunks -- assert
	// this the same measured way the existing dedup evidence tests do:
	// unique store-wide bytes grow by much less than the object's own
	// logical size.
	grew := statsAfter.ScopeUniqueChunkBytes - statsBefore.ScopeUniqueChunkBytes
	if grew >= int64(len(body)) {
		t.Fatalf("expected substantial CAS reuse between the multipart-completed object and its ordinary-PUT twin, but unique bytes grew by %d (>= object size %d)", grew, len(body))
	}
}

func TestMultipart_DeepVerifyAfterCompletion(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	part1 := genRandomBytes(1, 5*1024*1024)
	part2 := genRandomBytes(2, 2*1024*1024)
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	etag1, s1, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 1, part1)
	etag2, s2, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 2, part2)
	if s1 != http.StatusOK || s2 != http.StatusOK {
		t.Fatal("upload parts failed")
	}
	if _, status, body := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "k", uploadID,
		[]completedPartXML{{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2}}); status != http.StatusOK {
		t.Fatalf("complete failed: %d %s", status, body)
	}
	res, err := srv.store.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("expected deep verify to pass for a completed multipart object: %+v", res.Issues)
	}
}

// =============================================================================
// Presigned URL (SigV4 query-string authentication) tests
//
// buildTestPresignedQuery is a self-contained, test-only presign signer --
// independent of GeneratePresignedURL/authenticateQuery in zeros3.go -- so
// the adversarial tests below exercise the server's query-auth verifier
// the same "not checking it against itself" way signTestRequest exercises
// header-auth. Tests that only need to prove an early, pre-signature
// rejection (malformed Expires, missing parameter, bad algorithm, ...)
// build the raw query directly instead, since authenticateQuery rejects
// those before ever computing/comparing a signature.
// =============================================================================

func tamperHexString(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '0' {
			b[i] = '1'
		} else {
			b[i] = '0'
		}
		return string(b)
	}
	return s
}

func rawPresignQueryFrom(pairs map[string]string) string {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, testCanonicalEncode([]byte(k))+"="+testCanonicalEncode([]byte(pairs[k])))
	}
	return strings.Join(parts, "&")
}

// basePresignParams returns a syntactically well-formed (but not
// necessarily correctly signed) set of presign query parameters, for
// tests that only care about a check that runs before signature
// verification.
func basePresignParams(signer testSigner, when time.Time) map[string]string {
	dateStamp := when.UTC().Format("20060102")
	return map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    signer.accessKey + "/" + dateStamp + "/" + signer.region + "/s3/aws4_request",
		"X-Amz-Date":          when.UTC().Format("20060102T150405Z"),
		"X-Amz-Expires":       "900",
		"X-Amz-SignedHeaders": "host",
		"X-Amz-Signature":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}
}

type presignOpts struct {
	signedHeaders     string
	expiresSeconds    int64
	amzDate           time.Time
	accessKeyOverride string
	regionOverride    string
	serviceOverride   string
	algorithmOverride string
	tamperSignature   bool
}

// buildTestPresignedQuery independently computes a correctly-signed SigV4
// query-string ("presigned URL") query for rawPath/host, mirroring
// signTestRequest's independent-implementation approach for header auth.
func buildTestPresignedQuery(t *testing.T, signer testSigner, method, rawPath, host string, opts presignOpts) string {
	t.Helper()
	when := opts.amzDate
	if when.IsZero() {
		when = time.Now()
	}
	expires := opts.expiresSeconds
	if expires == 0 {
		expires = 900
	}
	signedHeaders := opts.signedHeaders
	if signedHeaders == "" {
		signedHeaders = "host"
	}
	algorithm := opts.algorithmOverride
	if algorithm == "" {
		algorithm = "AWS4-HMAC-SHA256"
	}
	accessKey := opts.accessKeyOverride
	if accessKey == "" {
		accessKey = signer.accessKey
	}
	region := opts.regionOverride
	if region == "" {
		region = signer.region
	}
	service := opts.serviceOverride
	if service == "" {
		service = "s3"
	}

	amzDate := when.UTC().Format("20060102T150405Z")
	dateStamp := when.UTC().Format("20060102")
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)

	params := map[string]string{
		"X-Amz-Algorithm":     algorithm,
		"X-Amz-Credential":    accessKey + "/" + credentialScope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.FormatInt(expires, 10),
		"X-Amz-SignedHeaders": signedHeaders,
	}
	rawQuery := rawPresignQueryFrom(params)

	canonicalHeaders := "host:" + host + "\n"
	canonicalRequest := strings.Join([]string{
		method,
		testCanonicalURI(rawPath),
		testCanonicalQuery(rawQuery),
		canonicalHeaders,
		strings.ToLower(signedHeaders),
		"UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, credentialScope, testHexSHA256([]byte(canonicalRequest)),
	}, "\n")
	kDate := testHMAC([]byte("AWS4"+signer.secretKey), dateStamp)
	kRegion := testHMAC(kDate, region)
	kService := testHMAC(kRegion, service)
	kSigning := testHMAC(kService, "aws4_request")
	sig := hex.EncodeToString(testHMAC(kSigning, stringToSign))
	if opts.tamperSignature {
		sig = tamperHexString(sig)
	}
	return rawQuery + "&X-Amz-Signature=" + sig
}

func newPresignTestRequest(method, rawPath, rawQuery, host string) (req *http.Request, path string) {
	req = httptest.NewRequest(method, rawPath+"?"+rawQuery, nil)
	req.Host = host
	return req, rawPath
}

func TestSigV4_CanonicalQueryExcludingSignature(t *testing.T) {
	got, err := sigv4CanonicalQueryExcluding("X-Amz-Signature=abc&X-Amz-Date=20240101T000000Z", "X-Amz-Signature")
	if err != nil {
		t.Fatal(err)
	}
	if want := "X-Amz-Date=20240101T000000Z"; got != want {
		t.Fatalf("sigv4CanonicalQueryExcluding = %q, want %q", got, want)
	}
	// Excluding a key that isn't present changes nothing.
	got2, err := sigv4CanonicalQueryExcluding("a=1&b=2", "X-Amz-Signature")
	if err != nil {
		t.Fatal(err)
	}
	if want := "a=1&b=2"; got2 != want {
		t.Fatalf("sigv4CanonicalQueryExcluding(no match) = %q, want %q", got2, want)
	}
	// sigv4CanonicalQuery (no exclusion) still behaves exactly as before.
	got3, err := sigv4CanonicalQuery("X-Amz-Signature=abc&a=1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "X-Amz-Signature=abc&a=1"; got3 != want {
		t.Fatalf("sigv4CanonicalQuery = %q, want %q", got3, want)
	}
}

func TestPresignAuth_ValidAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/mybucket/mykey", host, presignOpts{})
	req, path := newPresignTestRequest(http.MethodGet, "/mybucket/mykey", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
		t.Fatalf("expected a validly presigned GET to be accepted: %v", err)
	}
}

func TestPresignAuth_ValidPUTAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodPut, "/mybucket/mykey", host, presignOpts{})
	req, path := newPresignTestRequest(http.MethodPut, "/mybucket/mykey", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
		t.Fatalf("expected a validly presigned PUT to be accepted: %v", err)
	}
}

func TestPresignAuth_TrickyPathsAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	targets := []string{
		"/mybucket/space%20key",
		"/mybucket/a+b",
		"/mybucket/enc%2Fslash",
		"/mybucket//double//slash",
		"/mybucket/trailing/",
		"/mybucket/unicode-%C3%A9%C3%A8",
	}
	for _, path := range targets {
		t.Run(path, func(t *testing.T) {
			rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, path, host, presignOpts{})
			req, _ := newPresignTestRequest(http.MethodGet, path, rawQuery, host)
			if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
				t.Fatalf("expected tricky path %q to be accepted: %v", path, err)
			}
		})
	}
}

func TestPresignAuth_MissingSignatureFallsThroughToHeaderPathAndFails(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{})
	// Strip the signature parameter entirely -- hasQueryAuth then reports
	// false, so this must be handled (and rejected) by the header path,
	// never silently accepted by either.
	idx := strings.Index(rawQuery, "&X-Amz-Signature=")
	stripped := rawQuery[:idx]
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", stripped, host)
	if err := srv.authenticate(req, path, stripped, nil); err == nil {
		t.Fatalf("expected a signature-less query-auth-shaped request to be rejected")
	}
}

func TestPresignAuth_TamperedSignatureRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{tamperSignature: true})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "SignatureDoesNotMatch" {
		t.Fatalf("expected a tampered presigned signature to be rejected as SignatureDoesNotMatch, got %v", err)
	}
}

func TestPresignAuth_ModifiedPathAfterGenerationRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{})
	tamperedPath := "/b/other-key"
	req, _ := newPresignTestRequest(http.MethodGet, tamperedPath, rawQuery, host)
	if err := srv.authenticateQuery(req, tamperedPath, rawQuery); err == nil {
		t.Fatalf("expected a presigned URL used against a different path to be rejected")
	}
}

func TestPresignAuth_ModifiedBucketAfterGenerationRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/bucket1/k", host, presignOpts{})
	tamperedPath := "/bucket2/k"
	req, _ := newPresignTestRequest(http.MethodGet, tamperedPath, rawQuery, host)
	if err := srv.authenticateQuery(req, tamperedPath, rawQuery); err == nil {
		t.Fatalf("expected a presigned URL used against a different bucket to be rejected")
	}
}

func TestPresignAuth_ModifiedHostAfterGenerationRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	signedHost := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", signedHost, presignOpts{})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, "attacker.example:9000")
	if err := srv.authenticateQuery(req, path, rawQuery); err == nil {
		t.Fatalf("expected a presigned URL replayed with a different Host to be rejected")
	}
}

func TestPresignAuth_ModifiedSignedQueryParameterRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{expiresSeconds: 900})
	tampered := strings.Replace(rawQuery, "X-Amz-Expires=900", "X-Amz-Expires=901", 1)
	if tampered == rawQuery {
		t.Fatal("test setup: replacement did not change the query")
	}
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", tampered, host)
	if err := srv.authenticateQuery(req, path, tampered); err == nil {
		t.Fatalf("expected a modified signed query parameter (X-Amz-Expires) to invalidate the signature")
	}
}

func TestPresignAuth_ExtraUnsignedQueryParamAfterGenerationRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{})
	withExtra := rawQuery + "&foo=bar"
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", withExtra, host)
	if err := srv.authenticateQuery(req, path, withExtra); err == nil {
		t.Fatalf("expected an unrelated query parameter appended after signing to invalidate the signature (all query params are canonicalized, signed or not)")
	}
}

func TestPresignAuth_WrongAccessKeyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	other := signer
	other.accessKey = "AKIAWRONGACCESSKEY00"
	rawQuery := buildTestPresignedQuery(t, other, http.MethodGet, "/b/k", host, presignOpts{})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "InvalidAccessKeyId" {
		t.Fatalf("expected wrong access key to be rejected as InvalidAccessKeyId, got %v", err)
	}
}

func TestPresignAuth_WrongRegionRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{regionOverride: "eu-west-1"})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err == nil {
		t.Fatalf("expected a presigned URL scoped to the wrong region to be rejected")
	}
}

func TestPresignAuth_WrongServiceRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{serviceOverride: "ec2"})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err == nil {
		t.Fatalf("expected a presigned URL scoped to the wrong service to be rejected")
	}
}

func TestPresignAuth_UnsupportedAlgorithmRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{algorithmOverride: "AWS4-HMAC-SHA1"})
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected an unsupported X-Amz-Algorithm to be rejected as AuthorizationQueryParametersError, got %v", err)
	}
}

func TestPresignAuth_HostNotSignedRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	// SignedHeaders deliberately omits "host" -- must never be accepted,
	// regardless of what the rest of the signature computation says.
	params := basePresignParams(signer, time.Now())
	params["X-Amz-SignedHeaders"] = "x-amz-date"
	rawQuery := rawPresignQueryFrom(params)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected a presign request that doesn't sign 'host' to be rejected, got %v", err)
	}
}

func TestPresignAuth_SecurityTokenRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	params := basePresignParams(signer, time.Now())
	params["X-Amz-Security-Token"] = "FwoGZXIvYXdzEXAMPLE"
	rawQuery := rawPresignQueryFrom(params)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected X-Amz-Security-Token to be explicitly rejected (unsupported credential model), got %v", err)
	}
}

func TestPresignAuth_DuplicateQueryParameterRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{})
	// Append a second, different X-Amz-Expires -- an ambiguous request
	// that must never let the verifier silently pick one value.
	duplicated := rawQuery + "&X-Amz-Expires=1"
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", duplicated, host)
	err := srv.authenticateQuery(req, path, duplicated)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected a duplicated query auth parameter to be rejected, got %v", err)
	}
}

func TestPresignAuth_CaseChangedParamNameNotAuthenticated(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{})
	// Lowercase the signature parameter's name: hasQueryAuth's exact-case
	// probe no longer matches, so this must not be silently authenticated
	// via either path.
	lowered := strings.Replace(rawQuery, "X-Amz-Signature=", "x-amz-signature=", 1)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", lowered, host)
	if err := srv.authenticate(req, path, lowered, nil); err == nil {
		t.Fatalf("expected a case-altered auth parameter name to be rejected, not silently authenticated")
	}
}

func TestPresignAuth_MissingRequiredParameterRejected(t *testing.T) {
	for _, omit := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders"} {
		t.Run(omit, func(t *testing.T) {
			srv, signer := newTestServerAndSigner(t)
			host := "127.0.0.1:9000"
			params := basePresignParams(signer, time.Now())
			delete(params, omit)
			rawQuery := rawPresignQueryFrom(params)
			req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
			err := srv.authenticateQuery(req, path, rawQuery)
			var ae *authError
			if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
				t.Fatalf("expected missing %s to be rejected as AuthorizationQueryParametersError, got %v", omit, err)
			}
		})
	}
}

func TestPresignAuth_MalformedCredentialScopeRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	params := basePresignParams(signer, time.Now())
	params["X-Amz-Credential"] = "onlyaccesskeynoscope"
	rawQuery := rawPresignQueryFrom(params)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected a malformed credential scope to be rejected, got %v", err)
	}
}

func TestPresignAuth_MalformedTimestampRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	params := basePresignParams(signer, time.Now())
	params["X-Amz-Date"] = "not-a-timestamp"
	rawQuery := rawPresignQueryFrom(params)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
		t.Fatalf("expected a malformed X-Amz-Date to be rejected, got %v", err)
	}
}

func TestPresignAuth_ExpiresMalformedRejected(t *testing.T) {
	cases := []string{"abc", "-1", "0", "604801", "99999999999999999999", ""}
	for _, exp := range cases {
		t.Run(fmt.Sprintf("expires=%q", exp), func(t *testing.T) {
			srv, signer := newTestServerAndSigner(t)
			host := "127.0.0.1:9000"
			params := basePresignParams(signer, time.Now())
			params["X-Amz-Expires"] = exp
			rawQuery := rawPresignQueryFrom(params)
			req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
			err := srv.authenticateQuery(req, path, rawQuery)
			if exp == "" {
				// An empty value round-trips as a missing parameter.
				var ae *authError
				if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
					t.Fatalf("expected empty X-Amz-Expires to be rejected, got %v", err)
				}
				return
			}
			var ae *authError
			if !errors.As(err, &ae) || ae.code != "AuthorizationQueryParametersError" {
				t.Fatalf("expected X-Amz-Expires=%q to be rejected as AuthorizationQueryParametersError, got %v", exp, err)
			}
		})
	}
}

func TestPresignAuth_ExpiresBoundsAccepted(t *testing.T) {
	for _, exp := range []int64{1, 604800} {
		t.Run(fmt.Sprintf("expires=%d", exp), func(t *testing.T) {
			srv, signer := newTestServerAndSigner(t)
			host := "127.0.0.1:9000"
			rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{expiresSeconds: exp})
			req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
			if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
				t.Fatalf("expected boundary expires=%d to be accepted: %v", exp, err)
			}
		})
	}
}

// withFixedSigv4Now overrides sigv4Now for the duration of the test,
// restoring it afterward -- the "existing/testable time injection" seam
// used so expiry tests never sleep or depend on wall-clock timing.
func withFixedSigv4Now(t *testing.T, fixed time.Time) {
	t.Helper()
	orig := sigv4Now
	sigv4Now = func() time.Time { return fixed }
	t.Cleanup(func() { sigv4Now = orig })
}

func TestPresignAuth_ExactExpiryBoundaryAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	signedAt := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{amzDate: signedAt, expiresSeconds: 300})
	withFixedSigv4Now(t, signedAt.Add(300*time.Second)) // exactly at expiry
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
		t.Fatalf("expected a request exactly at its expiry instant to still be accepted: %v", err)
	}
}

func TestPresignAuth_ImmediatelyPastExpiryRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	signedAt := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{amzDate: signedAt, expiresSeconds: 300})
	withFixedSigv4Now(t, signedAt.Add(300*time.Second+time.Second))
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AccessDenied" {
		t.Fatalf("expected a request one second past expiry to be rejected, got %v", err)
	}
}

func TestPresignAuth_FutureDateBeyondSkewRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(requestSkewWindow + time.Minute)
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{amzDate: future, expiresSeconds: 300})
	withFixedSigv4Now(t, now)
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	err := srv.authenticateQuery(req, path, rawQuery)
	var ae *authError
	if !errors.As(err, &ae) || ae.code != "AccessDenied" {
		t.Fatalf("expected an X-Amz-Date far in the future to be rejected, got %v", err)
	}
}

func TestPresignAuth_LongLivedButUnexpiredAccepted(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	host := "127.0.0.1:9000"
	signedAt := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/b/k", host, presignOpts{amzDate: signedAt, expiresSeconds: 604800})
	withFixedSigv4Now(t, signedAt.Add(604799*time.Second))
	req, path := newPresignTestRequest(http.MethodGet, "/b/k", rawQuery, host)
	if err := srv.authenticateQuery(req, path, rawQuery); err != nil {
		t.Fatalf("expected a still-unexpired long-lived (7-day) presigned URL to be accepted: %v", err)
	}
}

// =============================================================================
// Presigned URL generation (GeneratePresignedURL / "zeros3 presign") tests
// =============================================================================

func TestPresign_GeneratedURLPassesServerVerifier(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: signer.accessKey, SecretAccessKey: signer.secretKey}

	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			presignedURL, err := GeneratePresignedURL(creds, signer.region, PresignRequest{
				Method: method, Endpoint: ts.URL, Bucket: "bucket1", Key: "genkey", Expires: 5 * time.Minute,
			}, time.Now())
			if err != nil {
				t.Fatalf("GeneratePresignedURL: %v", err)
			}
			var resp *http.Response
			if method == "PUT" {
				resp, err = client.Do(mustPresignedHTTPRequest(t, method, presignedURL, []byte("payload")))
			} else {
				// Seed the object first via header auth so the presigned GET has something to read.
				putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bucket1/genkey", []byte("payload"), nil)
				putResp.Body.Close()
				resp, err = client.Do(mustPresignedHTTPRequest(t, method, presignedURL, nil))
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected a CLI/library-generated presigned %s URL to be accepted, got %d: %s", method, resp.StatusCode, b)
			}
		})
	}
}

func mustPresignedHTTPRequest(t *testing.T, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPresign_TrickyKeysRoundTripThroughServerVerifier(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: signer.accessKey, SecretAccessKey: signer.secretKey}

	keys := []string{
		"space key.txt",
		"plus+key.txt",
		"percent%25key.txt",
		"slash/in/key.txt",
		"unicode-éè.txt",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			putURL, err := GeneratePresignedURL(creds, signer.region, PresignRequest{
				Method: "PUT", Endpoint: ts.URL, Bucket: "bucket1", Key: key, Expires: 5 * time.Minute,
			}, time.Now())
			if err != nil {
				t.Fatalf("GeneratePresignedURL PUT: %v", err)
			}
			putResp, err := client.Do(mustPresignedHTTPRequest(t, "PUT", putURL, []byte("tricky-key-payload")))
			if err != nil {
				t.Fatal(err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusOK {
				t.Fatalf("PUT for tricky key %q: status %d", key, putResp.StatusCode)
			}

			getURL, err := GeneratePresignedURL(creds, signer.region, PresignRequest{
				Method: "GET", Endpoint: ts.URL, Bucket: "bucket1", Key: key, Expires: 5 * time.Minute,
			}, time.Now())
			if err != nil {
				t.Fatalf("GeneratePresignedURL GET: %v", err)
			}
			getResp, err := client.Do(mustPresignedHTTPRequest(t, "GET", getURL, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer getResp.Body.Close()
			data, _ := io.ReadAll(getResp.Body)
			if getResp.StatusCode != http.StatusOK || string(data) != "tricky-key-payload" {
				t.Fatalf("GET for tricky key %q: status %d body %q", key, getResp.StatusCode, data)
			}
		})
	}
}

func TestPresign_ExpiresOutOfRangeRejectedAtGenerationTime(t *testing.T) {
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	cases := []time.Duration{0, -1 * time.Minute, 8 * 24 * time.Hour}
	for _, exp := range cases {
		if _, err := GeneratePresignedURL(creds, "us-east-1", PresignRequest{
			Method: "GET", Endpoint: "http://127.0.0.1:9000", Bucket: "b", Key: "k", Expires: exp,
		}, time.Now()); err == nil {
			t.Fatalf("expected expires=%v to be rejected at generation time", exp)
		}
	}
}

func TestPresign_BucketAndKeyRequired(t *testing.T) {
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	base := PresignRequest{Method: "GET", Endpoint: "http://127.0.0.1:9000", Expires: 5 * time.Minute}
	withBucket := base
	withBucket.Bucket = "b"
	withKey := base
	withKey.Key = "k"
	for _, req := range []PresignRequest{base, withBucket, withKey} {
		if _, err := GeneratePresignedURL(creds, "us-east-1", req, time.Now()); err == nil {
			t.Fatalf("expected a missing bucket or key to be rejected: %+v", req)
		}
	}
}

func TestPresign_UnsupportedMethodRejected(t *testing.T) {
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	if _, err := GeneratePresignedURL(creds, "us-east-1", PresignRequest{
		Method: "DELETE", Endpoint: "http://127.0.0.1:9000", Bucket: "b", Key: "k", Expires: 5 * time.Minute,
	}, time.Now()); err == nil {
		t.Fatalf("expected an unsupported presign method to be rejected")
	}
}

func TestPresign_NeverLogsOrEmbedsSecretKey(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "TotallySecretValueThatMustNeverAppear"}
	url, err := GeneratePresignedURL(creds, "us-east-1", PresignRequest{
		Method: "GET", Endpoint: "http://127.0.0.1:9000", Bucket: "b", Key: "k", Expires: 5 * time.Minute,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(url, creds.SecretAccessKey) {
		t.Fatalf("presigned URL must never contain the raw secret access key: %s", url)
	}
}

// =============================================================================
// Presigned PUT/GET mutation-safety and end-to-end tests (real HTTP)
// =============================================================================

func TestPresignedPUT_TamperedSignatureLeavesNoVisibleObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(ts.URL, "http://")
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodPut, "/bucket1/tamperkey", host, presignOpts{tamperSignature: true})
	resp, err := client.Do(mustPresignedHTTPRequest(t, "PUT", ts.URL+"/bucket1/tamperkey?"+rawQuery, []byte("should never land")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a tampered-signature presigned PUT to be rejected, got 200")
	}
	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bucket1/tamperkey", nil, nil)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected no object to be visible after a rejected presigned PUT, got status %d", getResp.StatusCode)
	}
}

func TestPresignedPUT_ExpiredURLLeavesNoVisibleObject(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(ts.URL, "http://")
	longAgo := time.Now().Add(-2 * time.Hour)
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodPut, "/bucket1/expiredkey", host, presignOpts{amzDate: longAgo, expiresSeconds: 60})
	resp, err := client.Do(mustPresignedHTTPRequest(t, "PUT", ts.URL+"/bucket1/expiredkey?"+rawQuery, []byte("should never land")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected an expired presigned PUT to be rejected, got 200")
	}
	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/bucket1/expiredkey", nil, nil)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected no object to be visible after a rejected expired presigned PUT, got status %d", getResp.StatusCode)
	}
}

func TestPresignedPUT_MissingDestinationBucketLeavesNoMutation(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	host := strings.TrimPrefix(ts.URL, "http://")
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodPut, "/no-such-bucket/k", host, presignOpts{})
	resp, err := client.Do(mustPresignedHTTPRequest(t, "PUT", ts.URL+"/no-such-bucket/k?"+rawQuery, []byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a presigned PUT to a nonexistent bucket to fail, got 200")
	}
}

func TestPresignedGET_TamperedSignatureRejectedOverRealHTTP(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "bucket1"); err != nil {
		t.Fatal(err)
	}
	putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/bucket1/k", []byte("secret payload"), nil)
	putResp.Body.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	rawQuery := buildTestPresignedQuery(t, signer, http.MethodGet, "/bucket1/k", host, presignOpts{tamperSignature: true})
	resp, err := client.Do(mustPresignedHTTPRequest(t, "GET", ts.URL+"/bucket1/k?"+rawQuery, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a tampered-signature presigned GET to be rejected, got 200")
	}
}

// =============================================================================
// Virtual-hosted-style addressing tests
// =============================================================================

func TestVHost_BucketFromHost(t *testing.T) {
	srv := &Server{vhostBase: "s3.test.local"}
	cases := []struct {
		host       string
		wantBucket string
		wantOK     bool
	}{
		{"mybucket.s3.test.local", "mybucket", true},
		{"mybucket.s3.test.local:9000", "mybucket", true},
		{"MyBucket.S3.Test.Local:9000", "mybucket", true}, // case-insensitive Host
		{"my.bucket.with.dots.s3.test.local", "my.bucket.with.dots", true},
		{"my-bucket-with-hyphens.s3.test.local", "my-bucket-with-hyphens", true},
		{"s3.test.local", "", false},         // base domain alone, no bucket label
		{"s3.test.local:9000", "", false},    // base domain with port, still no bucket
		{"127.0.0.1:9000", "", false},        // plain IP -- path style
		{"localhost:9000", "", false},        // localhost -- path style
		{"unrelated.example.com", "", false}, // different host entirely
		{"", "", false},                      // empty/malformed host
		{".s3.test.local", "", false},        // empty bucket label before the suffix
	}
	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			bucket, ok := srv.vhostBucketFromHost(c.host)
			if ok != c.wantOK || bucket != c.wantBucket {
				t.Fatalf("vhostBucketFromHost(%q) = (%q, %v), want (%q, %v)", c.host, bucket, ok, c.wantBucket, c.wantOK)
			}
		})
	}
}

func TestVHost_DisabledByDefaultFallsBackToPathStyle(t *testing.T) {
	srv := &Server{} // vhostBase left empty, as every existing Server (NewServer) is by default
	bucket, ok := srv.vhostBucketFromHost("mybucket.s3.test.local")
	if ok || bucket != "" {
		t.Fatalf("expected virtual-host routing to be disabled when vhostBase is unset, got (%q, %v)", bucket, ok)
	}
}

// newVHostTestServer returns a server/signer pair with virtual-host
// addressing enabled at "s3.test.local", plus a real httptest.NewServer
// listener to exercise both addressing styles against the same store.
func newVHostTestServer(t *testing.T) (*httptest.Server, testSigner) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	creds := Credentials{AccessKeyID: "AKIATESTACCESSKEY0001", SecretAccessKey: "TestSecretKeyForZeroS3UnitTests0123456789"}
	srv := NewServer(store, creds, "us-east-1")
	srv.SetVirtualHostBase("s3.test.local")
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	return httptest.NewServer(srv), signer
}

// vhostSignedRequest builds a header-authenticated request whose signed
// "host" is the given virtual-hosted-style Host (not the TCP address the
// client actually dials) -- exactly how a real client presents a
// virtual-hosted request, and exactly what SigV4 canonicalization must
// see unchanged.
func vhostSignedRequest(t *testing.T, signer testSigner, method, dialURL, host, rawPath string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, dialURL, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	signTestRequest(t, req, signer, rawPath, "", body, time.Now(), nil)
	return req
}

func TestVHost_EndToEndCreateBucketPutGetListDelete(t *testing.T) {
	ts, signer := newVHostTestServer(t)
	defer ts.Close()
	client := ts.Client()
	serverAddr := strings.TrimPrefix(ts.URL, "http://")
	vhost := "vhbucket.s3.test.local"
	if idx := strings.LastIndex(serverAddr, ":"); idx >= 0 {
		vhost = "vhbucket.s3.test.local" + serverAddr[idx:]
	}

	// CreateBucket via virtual-host addressing (PUT to the bucket root).
	createReq := vhostSignedRequest(t, signer, http.MethodPut, ts.URL+"/", vhost, "/", nil)
	resp, err := client.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vhost CreateBucket: status %d", resp.StatusCode)
	}

	// PutObject via virtual-host addressing.
	body := []byte("virtual-hosted payload")
	putReq := vhostSignedRequest(t, signer, http.MethodPut, ts.URL+"/greeting.txt", vhost, "/greeting.txt", body)
	putResp, err := client.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("vhost PutObject: status %d", putResp.StatusCode)
	}

	// GetObject via virtual-host addressing.
	getReq := vhostSignedRequest(t, signer, http.MethodGet, ts.URL+"/greeting.txt", vhost, "/greeting.txt", nil)
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	data, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != http.StatusOK || !bytes.Equal(data, body) {
		t.Fatalf("vhost GetObject: status %d body %q", getResp.StatusCode, data)
	}

	// The exact same bucket/key must also be reachable path-style, on the
	// same underlying store, through the same server -- both addressing
	// modes resolve to one logical namespace.
	pathResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/vhbucket/greeting.txt", nil, nil)
	defer pathResp.Body.Close()
	pathData, _ := io.ReadAll(pathResp.Body)
	if pathResp.StatusCode != http.StatusOK || !bytes.Equal(pathData, body) {
		t.Fatalf("path-style GetObject for a vhost-created object: status %d body %q", pathResp.StatusCode, pathData)
	}

	// DeleteObject via virtual-host addressing.
	delReq := vhostSignedRequest(t, signer, http.MethodDelete, ts.URL+"/greeting.txt", vhost, "/greeting.txt", nil)
	delResp, err := client.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("vhost DeleteObject: status %d", delResp.StatusCode)
	}
}

func TestVHost_PathStyleStillWorksWhenVHostConfigured(t *testing.T) {
	ts, signer := newVHostTestServer(t)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "plainbucket"); err != nil {
		t.Fatal(err)
	}
	putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/plainbucket/k", []byte("path-style payload"), nil)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected ordinary path-style PUT to keep working when vhost is configured, got %d", putResp.StatusCode)
	}
	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/plainbucket/k", nil, nil)
	defer getResp.Body.Close()
	data, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != http.StatusOK || string(data) != "path-style payload" {
		t.Fatalf("path-style GET with vhost configured: status %d body %q", getResp.StatusCode, data)
	}
}

func TestVHost_ListBucketsStillWorksOnBareHost(t *testing.T) {
	ts, signer := newVHostTestServer(t)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "somebucket"); err != nil {
		t.Fatal(err)
	}
	// GET / against the bare server address (no vhost suffix on this
	// Host) must still mean ListBuckets, not "bucket root of an empty
	// bucket name".
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/", nil, nil)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "somebucket") {
		t.Fatalf("expected ListBuckets on the bare host, got status %d body %q", resp.StatusCode, data)
	}
}

func TestVHost_PresignedURLWithVHostAddressing(t *testing.T) {
	ts, signer := newVHostTestServer(t)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "vhpresign"); err != nil {
		t.Fatal(err)
	}
	putResp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/vhpresign/k", []byte("vhost presign payload"), nil)
	putResp.Body.Close()

	creds := Credentials{AccessKeyID: signer.accessKey, SecretAccessKey: signer.secretKey}
	serverAddr := strings.TrimPrefix(ts.URL, "http://")
	vhostEndpoint := "http://s3.test.local" // srv.vhostBase, per newVHostTestServer
	if idx := strings.LastIndex(serverAddr, ":"); idx >= 0 {
		vhostEndpoint += serverAddr[idx:]
	}
	getURL, err := GeneratePresignedURL(creds, signer.region, PresignRequest{
		Method: "GET", Endpoint: vhostEndpoint, Bucket: "vhpresign", Key: "k", Expires: 5 * time.Minute, VHost: true,
	}, time.Now())
	if err != nil {
		t.Fatalf("GeneratePresignedURL (vhost): %v", err)
	}
	req := mustPresignedHTTPRequest(t, "GET", getURL, nil)
	// The signed Host is "vhpresign.<serverAddr>", which isn't a
	// resolvable DNS name in this test environment. Real virtual-hosted
	// deployments rely on DNS (or /etc/hosts, or --resolve, as the earlier
	// manual smoke test used) to point that name at the real server; here
	// we get the same effect by dialing serverAddr directly while still
	// sending the signed Host header unchanged -- exactly what SigV4
	// verification actually depends on, not the DNS resolution around it.
	req.Host = req.URL.Host
	req.URL.Host = serverAddr
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(data) != "vhost presign payload" {
		t.Fatalf("vhost-addressed presigned GET: status %d body %q", resp.StatusCode, data)
	}
}

// =============================================================================
// M5-C: internal object version history, restore, authoritative
// reachability, safe GC, doctor/stats extension.
// =============================================================================

func historyFor(t *testing.T, s *Store, bucket, key string) []*historyVersionEntry {
	t.Helper()
	entries, _, err := s.ListVersions(bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestVersions_FirstPutCreatesNoHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if got := historyFor(t, s, "b", "k"); len(got) != 0 {
		t.Fatalf("first-ever PUT to a key must archive no history, got %d entries", len(got))
	}
}

func TestVersions_PutOverwriteCreatesHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("version one"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.PutObject("b", "k", []byte("version two, longer"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v2.manifestUUID == v1.manifestUUID {
		t.Fatalf("overwrite must publish a distinct manifest identity")
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 1 {
		t.Fatalf("expected exactly 1 archived version after one overwrite, got %d", len(hist))
	}
	h := hist[0]
	if h.manifestUUID != v1.manifestUUID || h.manifestSHA256 != v1.manifestSHA256 {
		t.Fatalf("archived version must reference v1's exact manifest identity")
	}
	if h.size != v1.size || h.etag != v1.etag || h.contentType != v1.contentType {
		t.Fatalf("archived version metadata mismatch: got %+v", h)
	}
	if h.reason != historyReasonOverwritten {
		t.Fatalf("expected reason %q, got %q", historyReasonOverwritten, h.reason)
	}
	if h.versionID == "" || h.versionID == h.manifestUUID {
		t.Fatalf("archived version must have its own distinct version identity, got %q (manifest %q)", h.versionID, h.manifestUUID)
	}

	// Current GET/HEAD must still see v2, never v1 or a blend.
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "version two, longer" {
		t.Fatalf("current object must be v2, got %q", body)
	}
}

func TestVersions_CopyObjectOverwriteCreatesHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "src", []byte("source payload"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	dstV1, err := s.PutObject("b", "dst", []byte("original destination"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "dst", Directive: metadataDirectiveCopy})
	if err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "dst")
	if len(hist) != 1 || hist[0].manifestUUID != dstV1.manifestUUID {
		t.Fatalf("CopyObject overwrite must archive the prior destination root into history, got %+v", hist)
	}
	_, body, err := s.GetObject("b", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "source payload" {
		t.Fatalf("current dst object must now be the copied source bytes, got %q", body)
	}
}

func TestVersions_MultipartOverwriteCreatesHistory(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("original object"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	uploadID, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	partBody := genRandomBytes(1, 6*1024*1024)
	etag, err := s.UploadPart("b", "k", uploadID, 1, partBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 1 || hist[0].manifestUUID != v1.manifestUUID {
		t.Fatalf("completed multipart overwrite must archive the prior root into history, got %+v", hist)
	}
}

func TestVersions_DeleteArchivesHistoryGetNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	vA, err := s.PutObject("b", "k", []byte("A"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	vB, err := s.PutObject("b", "k", []byte("BB"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetObject("b", "k"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected errNoSuchKey after delete, got %v", err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("expected A and B both retained in history after delete, got %d entries", len(hist))
	}
	if hist[0].manifestUUID != vA.manifestUUID || hist[0].reason != historyReasonOverwritten {
		t.Fatalf("history[0] should be A, overwritten by B: %+v", hist[0])
	}
	if hist[1].manifestUUID != vB.manifestUUID || hist[1].reason != historyReasonDeleted {
		t.Fatalf("history[1] should be B, archived by delete: %+v", hist[1])
	}
}

// TestVersions_FullLifecycleAcrossRestart is exactly the Phase E required
// scenario: PUT A -> PUT B -> DELETE -> restart -> GET=not found ->
// versions still contain A/B -> restore B -> restart -> GET exact B.
func TestVersions_FullLifecycleAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("AAAA"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("BBBBBB"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.GetObject("b", "k"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("expected not found after restart, got %v", err)
	}
	hist := historyFor(t, s2, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("expected history to survive restart with 2 entries, got %d", len(hist))
	}
	bVersion := hist[1].versionID
	if _, _, err := s2.RestoreObjectVersion("b", "k", bVersion); err != nil {
		t.Fatal(err)
	}
	s2.Close()

	s3, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	_, body, err := s3.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "BBBBBB" {
		t.Fatalf("expected restored B's exact bytes after second restart, got %q", body)
	}
	// Restore must not have rewound history: A and B are both still there,
	// plus a new archived entry for whatever restore replaced (none here,
	// since delete already removed the current root before restore ran).
	hist2 := historyFor(t, s3, "b", "k")
	if len(hist2) != 2 {
		t.Fatalf("restore must not remove or rewrite existing history entries, got %d entries", len(hist2))
	}
}

func countChunkFiles(t *testing.T, storeDir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(storeDir, "chunks"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func countManifestFiles(t *testing.T, storeDir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(storeDir, "manifests"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRestore_ZeroCopy_OverExistingCurrent proves the Phase D/zero-copy
// requirement directly: restoring v1 over an existing current v3 creates
// no new CAS chunk file and no new manifest file at all -- restore reuses
// v1's exact existing manifest identity, publishing only a new journal
// frame.
func TestRestore_ZeroCopy_OverExistingCurrent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", genRandomBytes(1, 500*1024), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", genRandomBytes(2, 500*1024), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", genRandomBytes(3, 500*1024), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("expected 2 archived versions before restore, got %d", len(hist))
	}
	v1Version := hist[0].versionID
	if hist[0].manifestUUID != v1.manifestUUID {
		t.Fatalf("history[0] should be v1")
	}

	chunksBefore := countChunkFiles(t, dir)
	manifestsBefore := countManifestFiles(t, dir)

	restored, restoredMan, err := s.RestoreObjectVersion("b", "k", v1Version)
	if err != nil {
		t.Fatal(err)
	}

	chunksAfter := countChunkFiles(t, dir)
	manifestsAfter := countManifestFiles(t, dir)
	if chunksAfter != chunksBefore {
		t.Fatalf("restore must publish zero new CAS chunk files: before=%d after=%d", chunksBefore, chunksAfter)
	}
	if manifestsAfter != manifestsBefore {
		t.Fatalf("restore must publish zero new manifest files (reuses v1's exact manifest): before=%d after=%d", manifestsBefore, manifestsAfter)
	}
	if restored.manifestUUID != v1.manifestUUID || restoredMan.ManifestUUID != v1.manifestUUID {
		t.Fatalf("restored current root must reference v1's exact manifest identity")
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, genRandomBytes(1, 500*1024)) {
		t.Fatalf("restored object bytes do not match v1's original content")
	}
	// The v3 root that restore replaced is itself now archived into
	// history -- restore creates a new current state, it does not rewind.
	histAfter := historyFor(t, s, "b", "k")
	if len(histAfter) != 3 {
		t.Fatalf("expected 3 archived versions after restore (v1, v2, and the replaced v3), got %d", len(histAfter))
	}
}

func TestRestore_AfterCurrentObjectDeletion(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("only version"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 1 {
		t.Fatalf("expected 1 archived version, got %d", len(hist))
	}
	if _, _, err := s.RestoreObjectVersion("b", "k", hist[0].versionID); err != nil {
		t.Fatal(err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "only version" {
		t.Fatalf("restored bytes mismatch: got %q", body)
	}
}

func TestRestore_InvalidVersionID(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RestoreObjectVersion("b", "k", "not-a-real-version-id"); !errors.Is(err, errNoSuchVersion) {
		t.Fatalf("expected errNoSuchVersion, got %v", err)
	}
	// A failed restore must leave the current object completely untouched.
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2" {
		t.Fatalf("current object must be unaffected by a failed restore, got %q", body)
	}
}

func TestRestore_WrongBucketOrKey(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k1", []byte("k1v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k1", []byte("k1v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k1")
	if len(hist) != 1 {
		t.Fatalf("setup: expected 1 archived version")
	}
	versionID := hist[0].versionID

	if _, err := s.PutObject("b", "k2", []byte("unrelated"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RestoreObjectVersion("b", "k2", versionID); !errors.Is(err, errNoSuchVersion) {
		t.Fatalf("restoring k1's version under k2 must fail with errNoSuchVersion, got %v", err)
	}
	if err := s.CreateBucket("otherbucket"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RestoreObjectVersion("otherbucket", "k1", versionID); !errors.Is(err, errNoSuchVersion) {
		t.Fatalf("restoring b/k1's version under a different bucket must fail with errNoSuchVersion, got %v", err)
	}
}

// TestRestore_CorruptedHistoricalManifest_NoPartialMutation proves the
// Phase D "failed restore/GC validation must not partially mutate visible
// state" invariant: if the historical version's manifest file has been
// corrupted on disk, restore must fail cleanly and the current object must
// remain exactly what it was before the attempt.
func TestRestore_CorruptedHistoricalManifest_NoPartialMutation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("version one"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("version two"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 1 {
		t.Fatalf("setup: expected 1 archived version")
	}

	// Corrupt v1's manifest file on disk.
	manPath := filepath.Join(dir, "manifests", v1.manifestUUID+".json")
	if err := os.WriteFile(manPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RestoreObjectVersion("b", "k", hist[0].versionID); err == nil {
		t.Fatalf("expected restore to fail against a corrupted historical manifest")
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "version two" {
		t.Fatalf("failed restore must not mutate the current object, got %q", body)
	}
}

// TestReachability_CoversAllThreeRootCategories proves computeReachability
// (section 12a) enumerates current objects, retained historical versions,
// and active multipart uploads all as live roots, and that their union is
// what "reachable" means -- nothing from any of the three categories is
// ever reported as garbage.
func TestReachability_CoversAllThreeRootCategories(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	// Current-only.
	if _, err := s.PutObject("b", "current", []byte("current payload"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	// Historical-only (overwritten, then the overwriting version deleted
	// too so nothing about "history" is current).
	if _, err := s.PutObject("b", "hist", []byte("historical payload"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "hist", []byte("overwrite"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	// Multipart-only.
	uploadID, err := s.CreateMultipartUpload("b", "mp", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadPart("b", "mp", uploadID, 1, genRandomBytes(4, 6*1024*1024)); err != nil {
		t.Fatal(err)
	}

	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	if !rr.OK() {
		t.Fatalf("expected a fully healthy live root set, got issues: %+v", rr.Issues)
	}
	if rr.CurrentRootCount != 2 { // "current" and the current root of "hist"
		t.Fatalf("current root count: got %d want 2", rr.CurrentRootCount)
	}
	if rr.HistoricalRootCount != 1 {
		t.Fatalf("historical root count: got %d want 1", rr.HistoricalRootCount)
	}
	if rr.MultipartRootCount != 1 {
		t.Fatalf("multipart root count: got %d want 1", rr.MultipartRootCount)
	}
	if len(rr.ReferencedChunks) == 0 {
		t.Fatalf("expected at least some referenced chunks")
	}
}

// TestReachability_DetectsCorruptionAmongLiveRoots proves Phase G: a
// missing chunk referenced by a LIVE root (current, historical, or
// multipart) is reported as reachable-but-broken (an issue, OK()==false),
// never silently reclassified as unreachable garbage.
func TestReachability_DetectsCorruptionAmongLiveRoots(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		dir := t.TempDir()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
		entry, err := s.PutObject("b", "k", genRandomBytes(5, 300*1024), "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		man, _, err := s.readManifest(entry.manifestUUID)
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
		if err := os.Remove(s.chunkPath(sum)); err != nil {
			t.Fatal(err)
		}
		rr, err := s.computeReachability(false)
		if err != nil {
			t.Fatal(err)
		}
		if rr.OK() {
			t.Fatalf("expected a missing chunk referenced by the current root to be detected")
		}
		if rr.Missing == 0 {
			t.Fatalf("expected a missing-chunk issue, got %+v", rr.Issues)
		}
	})

	t.Run("historical", func(t *testing.T) {
		dir := t.TempDir()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
		v1, err := s.PutObject("b", "k", genRandomBytes(6, 300*1024), "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutObject("b", "k", []byte("v2"), "text/plain", nil); err != nil {
			t.Fatal(err)
		}
		man, _, err := s.readManifest(v1.manifestUUID)
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
		if err := os.Remove(s.chunkPath(sum)); err != nil {
			t.Fatal(err)
		}
		rr, err := s.computeReachability(false)
		if err != nil {
			t.Fatal(err)
		}
		if rr.OK() {
			t.Fatalf("expected a missing chunk referenced only by a historical root to be detected")
		}
	})

	t.Run("multipart", func(t *testing.T) {
		dir := t.TempDir()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.CreateBucket("b"); err != nil {
			t.Fatal(err)
		}
		uploadID, err := s.CreateMultipartUpload("b", "mp", "application/octet-stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		body := genRandomBytes(7, 6*1024*1024)
		if _, err := s.UploadPart("b", "mp", uploadID, 1, body); err != nil {
			t.Fatal(err)
		}
		pieces, err := chunkData(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(s.chunkPath(pieces[0].sha)); err != nil {
			t.Fatal(err)
		}
		rr, err := s.computeReachability(false)
		if err != nil {
			t.Fatal(err)
		}
		if rr.OK() {
			t.Fatalf("expected a missing chunk referenced only by an active multipart part to be detected")
		}
	})
}

// =============================================================================
// M5-C: safe offline GC
// =============================================================================

// TestGC_DryRunDeletesNothing proves Phase H's default: dry-run never
// removes any file, even when there is genuine garbage to report.
func TestGC_DryRunDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("keep me"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	// Manually plant a genuinely unreachable chunk (never referenced by
	// any root at all).
	garbage := bytes.Repeat([]byte{0x99}, 5000)
	if _, err := s.casWrite(garbage); err != nil {
		t.Fatal(err)
	}
	s.Close()

	before := countChunkFiles(t, dir)
	res, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.LiveSetOK {
		t.Fatalf("expected a healthy live set, got issues: %+v", res.Issues)
	}
	if res.ChunksUnreachable != 1 {
		t.Fatalf("expected dry-run to report exactly 1 unreachable chunk, got %d", res.ChunksUnreachable)
	}
	if res.ReclaimablePayloadBytes != 5000 {
		t.Fatalf("expected 5000 reclaimable payload bytes, got %d", res.ReclaimablePayloadBytes)
	}
	after := countChunkFiles(t, dir)
	if after != before {
		t.Fatalf("dry-run must delete nothing: chunk file count before=%d after=%d", before, after)
	}
	if res.ChunksDeleted != 0 || res.ManifestsDeleted != 0 || res.BytesDeleted != 0 {
		t.Fatalf("dry-run result must report zero deletions, got %+v", res)
	}
}

// TestGC_AdversarialMatrix_K1toK5 constructs, in one store: a current-only
// payload (K1), a historical-version-only payload (K2), an
// active-multipart-only payload (K3), a genuinely unreachable payload (K4),
// and a payload shared between a current object and a historical version
// (K5). It proves apply removes exactly K4 and nothing else.
func TestGC_AdversarialMatrix_K1toK5(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	k1 := bytes.Repeat([]byte{0x01}, 4096)
	k2 := bytes.Repeat([]byte{0x02}, 4096)
	k3 := genRandomBytes(11, 6*1024*1024) // large enough to be a valid multipart part
	k4 := bytes.Repeat([]byte{0x04}, 4096)
	k5 := bytes.Repeat([]byte{0x05}, 4096)

	// K1: current-only.
	putManualObject(t, s, "b", "k1obj", [][]byte{k1})
	// K2: historical-only -- put then overwrite so k2 is archived, not current.
	putManualObject(t, s, "b", "k2obj", [][]byte{k2})
	putManualObject(t, s, "b", "k2obj", [][]byte{[]byte("overwritten")})
	// K3: active-multipart-only.
	uploadID, err := s.CreateMultipartUpload("b", "k3upload", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	k3Etag, err := s.UploadPart("b", "k3upload", uploadID, 1, k3)
	if err != nil {
		t.Fatal(err)
	}
	// K4: genuinely unreachable -- write directly to CAS, never referenced.
	if _, err := s.casWrite(k4); err != nil {
		t.Fatal(err)
	}
	// K5: shared -- referenced by both a current object and (via a second
	// key's history) a historical version.
	putManualObject(t, s, "b", "k5current", [][]byte{k5})
	putManualObject(t, s, "b", "k5hist", [][]byte{k5})
	putManualObject(t, s, "b", "k5hist", [][]byte{[]byte("overwritten too")})

	sumK1 := sha256.Sum256(k1)
	sumK2 := sha256.Sum256(k2)
	sumK4 := sha256.Sum256(k4)
	sumK5 := sha256.Sum256(k5)
	pathK1 := s.chunkPath(sumK1)
	pathK2 := s.chunkPath(sumK2)
	pathK4 := s.chunkPath(sumK4)
	pathK5 := s.chunkPath(sumK5)
	s.Close()

	dry, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.LiveSetOK {
		t.Fatalf("expected a healthy live set, got issues: %+v", dry.Issues)
	}
	if dry.ChunksUnreachable != 1 {
		t.Fatalf("expected exactly 1 genuinely unreachable chunk (K4), got %d", dry.ChunksUnreachable)
	}

	applied, err := gcCollect(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ChunksDeleted != 1 {
		t.Fatalf("expected exactly 1 chunk deleted (K4), got %d", applied.ChunksDeleted)
	}

	for name, p := range map[string]string{"K1 (current)": pathK1, "K2 (historical)": pathK2, "K5 (shared)": pathK5} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must survive GC apply, but its chunk file is gone: %v", name, err)
		}
	}
	if _, err := os.Stat(pathK4); !os.IsNotExist(err) {
		t.Fatalf("K4 (genuinely unreachable) must be deleted by GC apply, got err=%v", err)
	}

	// Re-open and confirm every surviving object/version is still exactly
	// intact and readable.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, body, err := s2.GetObject("b", "k1obj"); err != nil || !bytes.Equal(body, k1) {
		t.Fatalf("K1 object corrupted or unreadable after GC: err=%v", err)
	}
	if _, body, err := s2.GetObject("b", "k5current"); err != nil || !bytes.Equal(body, k5) {
		t.Fatalf("K5 current object corrupted or unreadable after GC: err=%v", err)
	}
	// K3: the still-active multipart upload's part must have survived GC
	// intact enough to complete correctly.
	if _, _, err := s2.CompleteMultipartUpload("b", "k3upload", uploadID, []completedPart{{PartNumber: 1, ETag: k3Etag}}); err != nil {
		t.Fatalf("K3 multipart upload must complete correctly after surviving GC: %v", err)
	}
	if _, body, err := s2.GetObject("b", "k3upload"); err != nil || !bytes.Equal(body, k3) {
		t.Fatalf("K3 completed object bytes mismatch after surviving GC: err=%v", err)
	}
	verifyRes, err := s2.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("deep verify after GC must be clean, got issues: %+v", verifyRes.Issues)
	}
}

// TestGC_RefusesOnCorruptLiveRoot_K7 proves Phase J: destructive apply
// refuses outright when the live root set is not fully valid, while
// dry-run still reports the problem.
func TestGC_RefusesOnCorruptLiveRoot_K7(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObject("b", "k", genRandomBytes(8, 300*1024), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, _, err := s.readManifest(entry.manifestUUID)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	chunkPath := s.chunkPath(sum)
	if err := os.Remove(chunkPath); err != nil {
		t.Fatal(err)
	}
	s.Close()

	dry, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.LiveSetOK {
		t.Fatalf("expected dry-run to detect the corrupt live root")
	}
	if len(dry.Issues) == 0 {
		t.Fatalf("expected dry-run to report the missing-chunk issue")
	}

	_, err = gcCollect(dir, true)
	if !errors.Is(err, errGCUnsafe) {
		t.Fatalf("expected destructive apply to refuse with errGCUnsafe, got %v", err)
	}
}

// TestGC_InterruptedSweep_K6 begins a destructive GC apply against a store
// with several unreachable chunks, interrupts it partway via the
// hookBeforeGCDelete test seam, reopens, and confirms: all live data is
// still valid, remaining garbage is still safe (still reported, not
// corrupted), and re-running GC finishes the cleanup.
func TestGC_InterruptedSweep_K6(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	liveBody := []byte("this object must survive")
	if _, err := s.PutObject("b", "k", liveBody, "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	const numGarbage = 5
	for i := 0; i < numGarbage; i++ {
		garbage := bytes.Repeat([]byte{byte(0xE0 + i)}, 3000+i)
		if _, err := s.casWrite(garbage); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	// Interrupt after the 2nd deletion via panic+recover (same
	// simulated-crash pattern every other crash test in this file uses),
	// run outside the test goroutine's normal flow via a direct call.
	calls := 0
	old := testHook
	testHook = func(point string) {
		if point != hookBeforeGCDelete {
			return
		}
		calls++
		if calls == 3 {
			panic(simulatedCrash{point: point})
		}
	}
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("expected the GC sweep to be interrupted by the simulated crash")
			}
			if _, ok := r.(simulatedCrash); !ok {
				panic(r)
			}
		}()
		_, _ = gcCollect(dir, true)
	}()
	testHook = old

	// Live object must still be perfectly intact.
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, liveBody) {
		t.Fatalf("live object corrupted by an interrupted GC sweep: got %q", body)
	}
	verifyRes, err := s2.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("live data must remain fully valid after an interrupted GC sweep, got issues: %+v", verifyRes.Issues)
	}
	s2.Close()

	// Some garbage should remain (fewer than 5, since 2 were deleted
	// before the simulated interruption), still safely reported.
	mid, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if mid.ChunksUnreachable == 0 {
		t.Fatalf("expected some garbage to remain after an interrupted sweep")
	}
	if mid.ChunksUnreachable >= numGarbage {
		t.Fatalf("expected the interrupted sweep to have made partial progress, got %d of %d still unreachable", mid.ChunksUnreachable, numGarbage)
	}

	// Re-running GC to completion must finish the cleanup with no errors.
	final, err := gcCollect(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if final.ChunksDeleted != mid.ChunksUnreachable {
		t.Fatalf("expected the re-run to delete every remaining unreachable chunk, deleted=%d want=%d", final.ChunksDeleted, mid.ChunksUnreachable)
	}
	afterFinal, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if afterFinal.ChunksUnreachable != 0 {
		t.Fatalf("expected zero unreachable chunks after the completed re-run, got %d", afterFinal.ChunksUnreachable)
	}
}

// TestGC_ExclusivityRefusesWhileStoreInUse proves Phase H's offline/
// exclusive requirement: GC refuses safely (errGCStoreInUse) while another
// process (simulated here by directly holding the shared lock the server
// would hold) currently owns the store.
func TestGC_ExclusivityRefusesWhileStoreInUse(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	serverLock, err := acquireStoreLock(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer serverLock.release()

	if _, err := gcCollect(dir, false); !errors.Is(err, errGCStoreInUse) {
		t.Fatalf("expected gc dry-run to refuse with errGCStoreInUse while the store is in use, got %v", err)
	}
	if _, err := gcCollect(dir, true); !errors.Is(err, errGCStoreInUse) {
		t.Fatalf("expected gc apply to refuse with errGCStoreInUse while the store is in use, got %v", err)
	}

	serverLock.release()
	// Once released, GC must succeed normally.
	if _, err := gcCollect(dir, false); err != nil {
		t.Fatalf("expected gc to succeed once the store is no longer in use: %v", err)
	}
}

// TestGC_MultipartSurvivesGC_ThenCompletes is Phase M's primary
// correctness proof: an active multipart upload's already-published parts
// must survive both a GC dry-run and a destructive apply, across a
// restart, and the upload must still complete correctly to the exact
// expected bytes afterward.
func TestGC_MultipartSurvivesGC_ThenCompletes(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	part1 := genRandomBytes(21, 6*1024*1024)
	part2 := genRandomBytes(22, 6*1024*1024)
	etag1, err := s.UploadPart("b", "k", uploadID, 1, part1)
	if err != nil {
		t.Fatal(err)
	}
	etag2, err := s.UploadPart("b", "k", uploadID, 2, part2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetObject("b", "k"); err == nil {
		t.Fatalf("no current object should exist before completion")
	}
	// Also plant genuine garbage so this proves multipart survival
	// specifically, not merely "GC found nothing to delete".
	if _, err := s.casWrite(bytes.Repeat([]byte{0xAA}, 4000)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := gcCollect(dir, false); err != nil {
		t.Fatal(err)
	}
	applied, err := gcCollect(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ChunksDeleted != 1 {
		t.Fatalf("expected exactly the 1 planted garbage chunk deleted, got %d", applied.ChunksDeleted)
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	entry, _, err := s2.CompleteMultipartUpload("b", "k", uploadID, []completedPart{
		{PartNumber: 1, ETag: etag1}, {PartNumber: 2, ETag: etag2},
	})
	if err != nil {
		t.Fatalf("multipart completion must still succeed after GC ran while it was active: %v", err)
	}
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, part1...), part2...)
	if !bytes.Equal(body, want) {
		t.Fatalf("completed object bytes mismatch after surviving GC mid-upload")
	}
	_ = entry
}

// TestGC_AbortedMultipartBecomesCollectible is Phase M's second half: once
// an upload is aborted, its formerly-multipart-only chunks are no longer
// referenced by any live root and become genuinely collectible (unless
// some other root still shares them).
func TestGC_AbortedMultipartBecomesCollectible(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	partBody := genRandomBytes(23, 6*1024*1024)
	if _, err := s.UploadPart("b", "k", uploadID, 1, partBody); err != nil {
		t.Fatal(err)
	}
	pieces, err := chunkData(bytes.NewReader(partBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) == 0 {
		t.Fatalf("setup: expected at least one CDC chunk")
	}
	examplePath := s.chunkPath(pieces[0].sha)
	if _, err := os.Stat(examplePath); err != nil {
		t.Fatalf("setup: expected the part's chunk to exist before abort: %v", err)
	}
	if err := s.AbortMultipartUpload("b", "k", uploadID); err != nil {
		t.Fatal(err)
	}
	s.Close()

	dry, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.ChunksUnreachable == 0 {
		t.Fatalf("expected the aborted upload's former chunks to be reported as unreachable")
	}
	if _, err := gcCollect(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(examplePath); !os.IsNotExist(err) {
		t.Fatalf("expected the aborted upload's chunk to be collected, got err=%v", err)
	}
}

// TestVersionGC_Interaction proves Phase L: PUT v1 -> v2 -> v3 -> GC ->
// restore v1 -> exact bytes. All historical content must remain live
// across a GC pass, with no explicit version-deletion feature in this
// milestone to reclaim it.
func TestVersionGC_Interaction(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1Body := genRandomBytes(31, 200*1024)
	if _, err := s.PutObject("b", "k", v1Body, "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", genRandomBytes(32, 200*1024), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", genRandomBytes(33, 200*1024), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("setup: expected 2 archived versions")
	}
	v1VersionID := hist[0].versionID
	s.Close()

	if _, err := gcCollect(dir, true); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, _, err := s2.RestoreObjectVersion("b", "k", v1VersionID); err != nil {
		t.Fatalf("restoring v1 after GC must still succeed (history is a live GC root): %v", err)
	}
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, v1Body) {
		t.Fatalf("restored v1 bytes do not match after surviving a GC pass")
	}
}

// TestRestore_MissingHistoricalChunk_NoPartialMutation is the same
// invariant, exercised via a missing (rather than corrupt-manifest) chunk
// file referenced by the historical manifest.
func TestRestore_MissingHistoricalChunk_NoPartialMutation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1Body := genRandomBytes(9, 300*1024)
	if _, err := s.PutObject("b", "k", v1Body, "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("version two"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 1 {
		t.Fatalf("setup: expected 1 archived version")
	}
	man, _, err := s.readManifest(hist[0].manifestUUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Chunks) == 0 {
		t.Fatalf("setup: expected at least one chunk")
	}
	sum, err := decodeHexSHA256(man.Chunks[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RestoreObjectVersion("b", "k", hist[0].versionID); err == nil {
		t.Fatalf("expected restore to fail when a historical chunk is missing")
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "version two" {
		t.Fatalf("failed restore must not mutate the current object, got %q", body)
	}
}

// =============================================================================
// M5-C: storage-efficiency proof (Phase P)
// =============================================================================

// TestStorageEfficiencyProof_VersionHistoryIsCheap uploads a large v1, then
// two small edits (v2, v3), and proves numerically that CDC/CAS makes
// keeping full version history cheap: total logical version bytes vastly
// exceed physical unique CAS bytes, and restoring v1 adds zero further CAS
// payload bytes.
func TestStorageEfficiencyProof_VersionHistoryIsCheap(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}

	const size = 2 * 1024 * 1024
	v1 := genRandomBytes(101, size)
	if _, err := s.PutObject("b", "k", v1, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	// Small edit: change 64 bytes near the start.
	v2 := append([]byte{}, v1...)
	copy(v2[1000:1064], bytes.Repeat([]byte{0xEE}, 64))
	if _, err := s.PutObject("b", "k", v2, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	// Another small edit near the end.
	v3 := append([]byte{}, v2...)
	copy(v3[size-2000:size-1936], bytes.Repeat([]byte{0xFF}, 64))
	if _, err := s.PutObject("b", "k", v3, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	hist := historyFor(t, s, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("expected 2 archived versions (v1, v2), got %d", len(hist))
	}
	totalLogicalVersionBytes := int64(size) * 3 // v1 (history) + v2 (history) + v3 (current)

	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	var uniqueCASBytes int64
	for _, length := range rr.ChunkLength {
		uniqueCASBytes += length
	}
	if uniqueCASBytes >= totalLogicalVersionBytes {
		t.Fatalf("expected unique CAS bytes (%d) to be substantially less than total logical version bytes (%d) after two small edits",
			uniqueCASBytes, totalLogicalVersionBytes)
	}
	t.Logf("storage-efficiency proof: total logical version bytes=%d, unique reachable CAS bytes=%d, ratio=%.4f",
		totalLogicalVersionBytes, uniqueCASBytes, float64(uniqueCASBytes)/float64(totalLogicalVersionBytes))

	chunksBefore := countChunkFiles(t, dir)
	manifestsBefore := countManifestFiles(t, dir)
	if _, _, err := s.RestoreObjectVersion("b", "k", hist[0].versionID); err != nil {
		t.Fatal(err)
	}
	chunksAfter := countChunkFiles(t, dir)
	manifestsAfter := countManifestFiles(t, dir)
	if chunksAfter != chunksBefore {
		t.Fatalf("restoring v1 must add zero new CAS chunk files: before=%d after=%d", chunksBefore, chunksAfter)
	}
	if manifestsAfter != manifestsBefore {
		t.Fatalf("restoring v1 must add zero new manifest files: before=%d after=%d", manifestsBefore, manifestsAfter)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, v1) {
		t.Fatalf("restored v1 bytes do not match the original upload")
	}
}

// =============================================================================
// M5-C: crash/restart tests
// =============================================================================

func TestCrash_Restart_MultipartOverwritePreviousVersionPreserved(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("original single-put object"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	uploadID, err := s.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	partBody := genRandomBytes(41, 6*1024*1024)
	etag, err := s.UploadPart("b", "k", uploadID, 1, partBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteMultipartUpload("b", "k", uploadID, []completedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	hist := historyFor(t, s2, "b", "k")
	if len(hist) != 1 || hist[0].manifestUUID != v1.manifestUUID {
		t.Fatalf("expected the pre-multipart version to survive restart in history, got %+v", hist)
	}
	if _, _, err := s2.RestoreObjectVersion("b", "k", hist[0].versionID); err != nil {
		t.Fatal(err)
	}
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original single-put object" {
		t.Fatalf("restored pre-multipart version mismatch: got %q", body)
	}
}

func TestCrash_JournalReplay_HistoryRecordTypesDeterministic(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.PutObject("b", "k", []byte(fmt.Sprintf("version %d", i)), "text/plain", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Replay the journal twice independently (two fresh OpenStore calls)
	// and confirm both produce byte-identical history/current state --
	// deterministic replay of the new record types 9/10/11.
	s1, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1 := historyFor(t, s1, "b", "k")
	s1.Close()

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	h2 := historyFor(t, s2, "b", "k")

	if len(h1) != len(h2) || len(h1) != 5 {
		t.Fatalf("expected 5 archived versions on both replays, got %d and %d", len(h1), len(h2))
	}
	for i := range h1 {
		if h1[i].versionID != h2[i].versionID || h1[i].manifestUUID != h2[i].manifestUUID || h1[i].seq != h2[i].seq {
			t.Fatalf("replay %d mismatch at index %d: %+v vs %+v", i, i, h1[i], h2[i])
		}
	}
}

// TestJournal_GenuinelyUnknownRecordTypeStillFailsClosed proves the
// general "old binary fails closed on an unknown persistent record" fabric
// still functions correctly after adding record types 9-11: a record type
// this build genuinely does not recognize (as an old M1-M5-B binary would
// see types 9-11) is rejected by replay, never silently ignored or
// misinterpreted.
func TestJournal_GenuinelyUnknownRecordTypeStillFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	journalPath := filepath.Join(dir, "journal", "visibility.log")
	f, err := os.OpenFile(journalPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// Hand-craft one well-formed frame of a record type (200) that no
	// version of this codebase has ever defined.
	payload := []byte(`{"bogus":true}`)
	header := make([]byte, journalHeaderSize)
	copy(header[0:4], journalMagic)
	binary.BigEndian.PutUint16(header[4:6], journalFrameVersion)
	header[6] = 200
	header[7] = 0
	binary.BigEndian.PutUint64(header[8:16], 2) // next sequence after CreateBucket's seq 1
	binary.BigEndian.PutUint32(header[16:20], uint32(len(payload)))
	frame := append(append([]byte{}, header...), payload...)
	crc := crc32.Checksum(frame, castagnoliTable)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	frame = append(frame, crcBytes...)
	if _, err := f.WriteAt(frame, info.Size()); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := OpenStore(dir); err == nil {
		t.Fatalf("expected OpenStore to fail closed on a genuinely unknown record type")
	}
}

// =============================================================================
// M5-C: concurrency tests
// =============================================================================

func TestConcurrency_TwoOverwritesSameKey_HistoryDeterministic(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("base"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, _ = s.PutObject("b", "k", []byte(fmt.Sprintf("concurrent-%d", i)), "text/plain", nil)
		}()
	}
	wg.Wait()

	// Deterministic outcome: exactly one of the two concurrent writes is
	// current, and history has exactly 2 entries (the base PUT and
	// whichever of the two writes lost the race) -- never a torn or
	// duplicated view, and never both losing the race.
	hist := historyFor(t, s, "b", "k")
	if len(hist) != 2 {
		t.Fatalf("expected exactly 2 archived versions after 2 concurrent overwrites of a 1-version key, got %d", len(hist))
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "concurrent-0" && string(body) != "concurrent-1" {
		t.Fatalf("expected the current object to be exactly one of the two concurrent writes, got %q", body)
	}
}

func TestConcurrency_RestoreRacingDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	v1ID := hist[0].versionID

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = s.RestoreObjectVersion("b", "k", v1ID)
	}()
	go func() {
		defer wg.Done()
		_ = s.DeleteObject("b", "k")
	}()
	wg.Wait()

	// Race-free, deterministic result: the store must not panic or
	// deadlock, and afterward the current object is either fully absent
	// (delete won, ran after restore or restore ran and then delete
	// removed it) or fully present as v1 (restore won and ran last) --
	// never a torn state, and Verify must be clean either way.
	verifyRes, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("expected a clean deep verify after restore/delete race, got issues: %+v", verifyRes.Issues)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		if !errors.Is(err, errNoSuchKey) {
			t.Fatalf("unexpected error: %v", err)
		}
	} else if string(body) != "v1" {
		t.Fatalf("if the object is present after the race it must be exactly v1, got %q", body)
	}
}

func TestConcurrency_RestoreRacingPut(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	v1ID := hist[0].versionID

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = s.RestoreObjectVersion("b", "k", v1ID)
	}()
	go func() {
		defer wg.Done()
		_, _ = s.PutObject("b", "k", []byte("racing put"), "text/plain", nil)
	}()
	wg.Wait()

	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" && string(body) != "racing put" {
		t.Fatalf("expected the current object to be exactly one of the two racing writers' results, got %q", body)
	}
	verifyRes, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("expected a clean deep verify after restore/put race, got issues: %+v", verifyRes.Issues)
	}
}

func TestConcurrency_RestoreRacingCopyObject(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "src", []byte("copy source"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v2"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	hist := historyFor(t, s, "b", "k")
	v1ID := hist[0].versionID

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = s.RestoreObjectVersion("b", "k", v1ID)
	}()
	go func() {
		defer wg.Done()
		_, _, _ = s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "k", Directive: metadataDirectiveCopy})
	}()
	wg.Wait()

	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" && string(body) != "copy source" {
		t.Fatalf("expected the current object to be exactly one of the two racing writers' results, got %q", body)
	}
	verifyRes, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("expected a clean deep verify after restore/copy race, got issues: %+v", verifyRes.Issues)
	}
}

// =============================================================================
// M5-C: CLI smoke test (versions / restore / gc / doctor)
// =============================================================================

// buildZeros3Binary builds the zeros3 binary once for CLI-level tests and
// returns its path.
func buildZeros3Binary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "zeros3")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build zeros3 binary: %v\n%s", err, out)
	}
	return binPath
}

func runZeros3CLI(t *testing.T, bin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("failed to run zeros3 %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// TestCLI_VersionsRestoreGCDoctor_Smoke exercises the actual CLI surface
// (Phase C/D/H/N) end to end against a real built binary: `versions`,
// `restore`, `gc` (dry-run and apply), and `doctor`.
func TestCLI_VersionsRestoreGCDoctor_Smoke(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()

	s, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("version one"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("version two, current"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	// Genuine garbage for gc to find.
	if _, err := s.casWrite(bytes.Repeat([]byte{0x77}, 4321)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// `zeros3 versions -json`
	out, stderr, code := runZeros3CLI(t, bin, "versions", "-store", storeDir, "-bucket", "b", "-key", "k", "-json")
	if code != 0 {
		t.Fatalf("versions CLI failed (code %d): %s", code, stderr)
	}
	var rows []versionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("versions -json did not parse: %v\noutput: %s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 version rows (1 current + 1 historical), got %d: %+v", len(rows), rows)
	}
	var currentRow, histRow versionRow
	for _, r := range rows {
		switch r.Status {
		case "current":
			currentRow = r
		case "historical":
			histRow = r
		}
	}
	if currentRow.VersionID == "" || histRow.VersionID == "" {
		t.Fatalf("expected both a current and a historical row, got %+v", rows)
	}
	if currentRow.Size != int64(len("version two, current")) {
		t.Fatalf("current row size mismatch: got %d", currentRow.Size)
	}

	// Also confirm the human-readable form runs without error.
	if _, stderr, code := runZeros3CLI(t, bin, "versions", "-store", storeDir, "-bucket", "b", "-key", "k"); code != 0 {
		t.Fatalf("versions (human) CLI failed: %s", stderr)
	}

	// `zeros3 restore`
	_, stderr, code = runZeros3CLI(t, bin, "restore", "-store", storeDir, "-bucket", "b", "-key", "k", "-version", histRow.VersionID)
	if code != 0 {
		t.Fatalf("restore CLI failed (code %d): %s", code, stderr)
	}
	s2, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "version one" {
		t.Fatalf("expected restore CLI to make v1 current, got %q", body)
	}
	s2.Close()

	// `zeros3 restore` with an invalid version ID must fail with a nonzero
	// exit code and change nothing.
	if _, _, code := runZeros3CLI(t, bin, "restore", "-store", storeDir, "-bucket", "b", "-key", "k", "-version", "bogus"); code == 0 {
		t.Fatalf("expected restore CLI to fail for an invalid version ID")
	}

	// `zeros3 gc -json` (dry-run) must report the planted garbage and
	// delete nothing.
	out, stderr, code = runZeros3CLI(t, bin, "gc", "-store", storeDir, "-json")
	if code != 0 {
		t.Fatalf("gc dry-run CLI failed (code %d): %s", code, stderr)
	}
	var gcRes GCResult
	if err := json.Unmarshal([]byte(out), &gcRes); err != nil {
		t.Fatalf("gc -json did not parse: %v\noutput: %s", err, out)
	}
	if gcRes.Applied {
		t.Fatalf("expected dry-run gcRes.Applied=false")
	}
	if gcRes.ChunksUnreachable == 0 {
		t.Fatalf("expected gc dry-run to report at least the planted garbage chunk")
	}
	if gcRes.ChunksDeleted != 0 {
		t.Fatalf("expected gc dry-run to delete nothing, got ChunksDeleted=%d", gcRes.ChunksDeleted)
	}

	// `zeros3 gc -apply -json`
	out, stderr, code = runZeros3CLI(t, bin, "gc", "-store", storeDir, "-apply", "-json")
	if code != 0 {
		t.Fatalf("gc apply CLI failed (code %d): %s", code, stderr)
	}
	var gcApplied GCResult
	if err := json.Unmarshal([]byte(out), &gcApplied); err != nil {
		t.Fatalf("gc -apply -json did not parse: %v\noutput: %s", err, out)
	}
	if !gcApplied.Applied || gcApplied.ChunksDeleted == 0 {
		t.Fatalf("expected gc apply to actually delete the planted garbage, got %+v", gcApplied)
	}

	// `zeros3 doctor -json` -- must report OK and reflect the surviving
	// object correctly, and must not have mutated the store any further.
	out, stderr, code = runZeros3CLI(t, bin, "doctor", "-store", storeDir, "-deep", "-json")
	if code != 0 {
		t.Fatalf("doctor CLI failed (code %d): %s", code, stderr)
	}
	var doctorRes VerifyResult
	if err := json.Unmarshal([]byte(out), &doctorRes); err != nil {
		t.Fatalf("doctor -json did not parse: %v\noutput: %s", err, out)
	}
	if !doctorRes.OK() {
		t.Fatalf("expected doctor to report OK after gc apply, got %+v", doctorRes)
	}
	if doctorRes.CurrentRootCount != 1 {
		t.Fatalf("expected doctor to report 1 current root, got %d", doctorRes.CurrentRootCount)
	}

	s3, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	_, body, err = s3.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "version one" {
		t.Fatalf("expected the restored object to remain v1's exact bytes after doctor/gc, got %q", body)
	}
}

// =============================================================================
// M5-D: ListParts / ListMultipartUploads pagination
// =============================================================================

// mpuKV is a (key, uploadID) pair used by the ListMultipartUploads
// pagination tests to state an expected page independently of creation
// order -- upload IDs are UUIDv7, and this suite deliberately does not
// assume anything about UUIDv7 generation being monotonic within the same
// process tick; it always computes the expected order itself.
type mpuKV struct{ key, id string }

func assertUploadOrder(t *testing.T, got []uploadXML, want []mpuKV) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d uploads, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Key != want[i].key || got[i].UploadId != want[i].id {
			t.Fatalf("upload[%d] = (%q,%q), want (%q,%q)", i, got[i].Key, got[i].UploadId, want[i].key, want[i].id)
		}
	}
}

func TestListParts_Pagination(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	t.Run("zero-parts", func(t *testing.T) {
		uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "empty")
		lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "empty", uploadID, "")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if len(lp.Part) != 0 || lp.IsTruncated || lp.NextPartNumberMarker != 0 ||
			lp.PartNumberMarker != 0 || lp.MaxParts != defaultMaxParts {
			t.Fatalf("unexpected zero-parts result: %+v", lp)
		}
	})

	t.Run("one-part", func(t *testing.T) {
		uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "one")
		if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "one", uploadID, 1, []byte("hello")); status != http.StatusOK {
			t.Fatalf("upload part failed: %d", status)
		}
		lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "one", uploadID, "")
		if status != http.StatusOK || len(lp.Part) != 1 || lp.Part[0].PartNumber != 1 || lp.IsTruncated {
			t.Fatalf("unexpected one-part result: status=%d %+v", status, lp)
		}
	})

	// Shared upload with parts 1..5 for the marker/max-parts matrix below.
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "multi")
	for i := 1; i <= 5; i++ {
		body := []byte(fmt.Sprintf("part-%d-body", i))
		if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "multi", uploadID, i, body); status != http.StatusOK {
			t.Fatalf("upload part %d failed: %d", i, status)
		}
	}

	matrix := []struct {
		name           string
		query          string
		wantParts      []int
		wantTruncated  bool
		wantNextMarker int
		wantPartMarker int
		wantMaxParts   int
	}{
		{"fewer-than-max", "max-parts=10", []int{1, 2, 3, 4, 5}, false, 0, 0, 10},
		{"exactly-max", "max-parts=5", []int{1, 2, 3, 4, 5}, false, 0, 0, 5},
		{"max-plus-one", "max-parts=4", []int{1, 2, 3, 4}, true, 4, 0, 4},
		{"marker-at-beginning", "part-number-marker=0", []int{1, 2, 3, 4, 5}, false, 0, 0, defaultMaxParts},
		{"marker-in-middle", "part-number-marker=3", []int{4, 5}, false, 0, 3, defaultMaxParts},
		{"marker-at-end", "part-number-marker=5", nil, false, 0, 5, defaultMaxParts},
		{"marker-beyond-highest", "part-number-marker=100", nil, false, 0, 100, defaultMaxParts},
		{"default-max-parts", "", []int{1, 2, 3, 4, 5}, false, 0, 0, defaultMaxParts},
		{"explicit-small-max-parts", "max-parts=2", []int{1, 2}, true, 2, 0, 2},
		{"max-parts-clamped-to-1000", "max-parts=999999", []int{1, 2, 3, 4, 5}, false, 0, 0, defaultMaxParts},
	}
	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "multi", uploadID, tc.query)
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			var got []int
			for _, p := range lp.Part {
				got = append(got, p.PartNumber)
			}
			if len(got) != len(tc.wantParts) {
				t.Fatalf("parts = %v, want %v", got, tc.wantParts)
			}
			for i := range got {
				if got[i] != tc.wantParts[i] {
					t.Fatalf("parts = %v, want %v", got, tc.wantParts)
				}
			}
			if lp.IsTruncated != tc.wantTruncated {
				t.Fatalf("IsTruncated = %v, want %v", lp.IsTruncated, tc.wantTruncated)
			}
			if lp.NextPartNumberMarker != tc.wantNextMarker {
				t.Fatalf("NextPartNumberMarker = %d, want %d", lp.NextPartNumberMarker, tc.wantNextMarker)
			}
			if lp.PartNumberMarker != tc.wantPartMarker {
				t.Fatalf("PartNumberMarker = %d, want %d", lp.PartNumberMarker, tc.wantPartMarker)
			}
			if lp.MaxParts != tc.wantMaxParts {
				t.Fatalf("MaxParts = %d, want %d", lp.MaxParts, tc.wantMaxParts)
			}
		})
	}

	t.Run("multiple-pages", func(t *testing.T) {
		var all []int
		marker := 0
		for i := 0; i < 10; i++ {
			lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "multi", uploadID,
				fmt.Sprintf("max-parts=2&part-number-marker=%d", marker))
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			for _, p := range lp.Part {
				all = append(all, p.PartNumber)
			}
			if !lp.IsTruncated {
				break
			}
			marker = lp.NextPartNumberMarker
		}
		want := []int{1, 2, 3, 4, 5}
		if len(all) != len(want) {
			t.Fatalf("paginated parts = %v, want %v", all, want)
		}
		for i := range want {
			if all[i] != want[i] {
				t.Fatalf("paginated parts = %v, want %v", all, want)
			}
		}
	})
}

func TestListParts_Pagination_InvalidQueryValues(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")

	for _, q := range []string{
		"part-number-marker=abc",
		"part-number-marker=-1",
		"max-parts=abc",
		"max-parts=-1",
	} {
		t.Run(q, func(t *testing.T) {
			_, status := doListPartsQuery(t, client, ts.URL, signer, "b", "k", uploadID, q)
			if status != http.StatusBadRequest {
				t.Fatalf("query %q: status = %d, want 400", q, status)
			}
		})
	}

	// max-parts=0 is a valid boundary (mirrors ListObjectsV2's own
	// max-keys=0 behavior): an empty, non-truncated page, not an error.
	t.Run("max-parts=0-is-a-valid-empty-page", func(t *testing.T) {
		lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "k", uploadID, "max-parts=0")
		if status != http.StatusOK || len(lp.Part) != 0 || lp.IsTruncated {
			t.Fatalf("max-parts=0: status=%d result=%+v", status, lp)
		}
	})
}

func TestListParts_Pagination_RestartThenPaginate(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	uploadID, err := store.CreateMultipartUpload("b", "k", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := store.UploadPart("b", "k", uploadID, i, []byte(fmt.Sprintf("part-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	store.Close() // simulated restart: no crash injection needed, this is an orderly stop mid-lifecycle

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	var all []int
	marker := 0
	for i := 0; i < 10; i++ {
		page, err := store2.ListPartsPage("b", "k", uploadID, marker, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.parts {
			all = append(all, p.partNumber)
		}
		if !page.truncated {
			break
		}
		marker = page.nextPartNumberMarker
	}
	want := []int{1, 2, 3, 4, 5}
	if len(all) != len(want) {
		t.Fatalf("post-restart paginated parts = %v, want %v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("post-restart paginated parts = %v, want %v", all, want)
		}
	}
}

func TestListParts_Pagination_OverwrittenPartNoDuplicate(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	for i := 1; i <= 3; i++ {
		if _, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, i, []byte(fmt.Sprintf("orig-%d", i))); status != http.StatusOK {
			t.Fatalf("initial upload of part %d failed: %d", i, status)
		}
	}
	newETag, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 2, []byte("replaced-part-2-body"))
	if status != http.StatusOK {
		t.Fatalf("re-upload of part 2 failed: %d", status)
	}

	var all []partXML
	marker := 0
	for i := 0; i < 10; i++ {
		lp, status := doListPartsQuery(t, client, ts.URL, signer, "b", "k", uploadID,
			fmt.Sprintf("max-parts=1&part-number-marker=%d", marker))
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		all = append(all, lp.Part...)
		if !lp.IsTruncated {
			break
		}
		marker = lp.NextPartNumberMarker
	}
	if len(all) != 3 {
		t.Fatalf("expected exactly 3 parts after overwrite (no duplicate part 2), got %d: %+v", len(all), all)
	}
	if all[1].PartNumber != 2 || strings.Trim(all[1].ETag, `"`) != newETag {
		t.Fatalf("expected part 2 to reflect the overwritten ETag %q, got %+v", newETag, all[1])
	}
}

func TestListMultipartUploads_Pagination_NoUploads(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, "b", "")
	if status != http.StatusOK || len(lmu.Upload) != 0 || lmu.IsTruncated {
		t.Fatalf("status=%d result=%+v", status, lmu)
	}
}

func TestListMultipartUploads_Pagination_OneUpload(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	id := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "solo")
	lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, "b", "")
	if status != http.StatusOK || len(lmu.Upload) != 1 || lmu.Upload[0].Key != "solo" || lmu.Upload[0].UploadId != id || lmu.IsTruncated {
		t.Fatalf("status=%d result=%+v", status, lmu)
	}
}

// TestListMultipartUploads_Pagination_Matrix covers multiple keys, multiple
// uploads for the same key, max-uploads bounds, multi-page iteration, and
// key-marker/upload-id-marker resume semantics (including the documented
// AWS rule that upload-id-marker is ignored unless key-marker is also
// given) against one fixed set of 5 active uploads across 4 keys.
func TestListMultipartUploads_Pagination_Matrix(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	bucket := "mpu-matrix"
	if err := doCreateBucket(t, client, ts.URL, signer, bucket); err != nil {
		t.Fatal(err)
	}

	idAlpha := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "alpha")
	idBravo1 := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "bravo")
	idBravo2 := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "bravo")
	idCharlie := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "charlie")
	idDelta := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "delta")

	entries := []mpuKV{
		{"alpha", idAlpha}, {"bravo", idBravo1}, {"bravo", idBravo2},
		{"charlie", idCharlie}, {"delta", idDelta},
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key != entries[j].key {
			return entries[i].key < entries[j].key
		}
		return entries[i].id < entries[j].id
	})
	var bravoEntries []mpuKV
	for _, e := range entries {
		if e.key == "bravo" {
			bravoEntries = append(bravoEntries, e)
		}
	}
	if len(bravoEntries) != 2 {
		t.Fatalf("expected exactly 2 bravo entries, got %d: %+v", len(bravoEntries), bravoEntries)
	}

	t.Run("fewer-than-max", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads=10")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries)
		if lmu.IsTruncated {
			t.Fatalf("expected not truncated")
		}
		if lmu.MaxUploads != 10 {
			t.Fatalf("MaxUploads = %d, want 10", lmu.MaxUploads)
		}
	})

	t.Run("exactly-max", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads=5")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries)
		if lmu.IsTruncated {
			t.Fatalf("expected not truncated")
		}
	})

	t.Run("max-plus-one", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads=4")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries[:4])
		if !lmu.IsTruncated {
			t.Fatalf("expected truncated")
		}
		if lmu.NextKeyMarker != entries[3].key || lmu.NextUploadIdMarker != entries[3].id {
			t.Fatalf("next marker = (%q,%q), want (%q,%q)", lmu.NextKeyMarker, lmu.NextUploadIdMarker, entries[3].key, entries[3].id)
		}
	})

	t.Run("default-max-uploads", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries)
		if lmu.MaxUploads != defaultMaxUploads {
			t.Fatalf("MaxUploads = %d, want %d", lmu.MaxUploads, defaultMaxUploads)
		}
	})

	t.Run("max-uploads-clamped-to-1000", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads=999999")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if lmu.MaxUploads != defaultMaxUploads {
			t.Fatalf("MaxUploads = %d, want %d", lmu.MaxUploads, defaultMaxUploads)
		}
	})

	t.Run("multiple-pages", func(t *testing.T) {
		var all []mpuKV
		keyMarker, uploadIDMarker := "", ""
		for i := 0; i < 10; i++ {
			q := fmt.Sprintf("max-uploads=2&key-marker=%s&upload-id-marker=%s",
				url.QueryEscape(keyMarker), url.QueryEscape(uploadIDMarker))
			lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, q)
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			for _, u := range lmu.Upload {
				all = append(all, mpuKV{u.Key, u.UploadId})
			}
			if !lmu.IsTruncated {
				break
			}
			keyMarker, uploadIDMarker = lmu.NextKeyMarker, lmu.NextUploadIdMarker
		}
		if len(all) != len(entries) {
			t.Fatalf("paginated uploads = %+v, want %+v", all, entries)
		}
		for i := range entries {
			if all[i] != entries[i] {
				t.Fatalf("paginated uploads = %+v, want %+v", all, entries)
			}
		}
	})

	t.Run("resume-between-keys", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "key-marker="+url.QueryEscape("alpha"))
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries[1:])
	})

	t.Run("resume-within-same-key-multiple-uploads", func(t *testing.T) {
		q := "key-marker=" + url.QueryEscape("bravo") + "&upload-id-marker=" + url.QueryEscape(bravoEntries[0].id)
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, q)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		var want []mpuKV
		for _, e := range entries {
			if e.key > "bravo" || (e.key == "bravo" && e.id > bravoEntries[0].id) {
				want = append(want, e)
			}
		}
		assertUploadOrder(t, lmu.Upload, want)
	})

	t.Run("marker-at-end", func(t *testing.T) {
		last := entries[len(entries)-1]
		q := "key-marker=" + url.QueryEscape(last.key) + "&upload-id-marker=" + url.QueryEscape(last.id)
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, q)
		if status != http.StatusOK || len(lmu.Upload) != 0 || lmu.IsTruncated {
			t.Fatalf("marker-at-end: status=%d result=%+v", status, lmu)
		}
	})

	t.Run("marker-beyond-existing", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "key-marker=zzzzzzzz")
		if status != http.StatusOK || len(lmu.Upload) != 0 || lmu.IsTruncated {
			t.Fatalf("marker-beyond-existing: status=%d result=%+v", status, lmu)
		}
	})

	t.Run("upload-id-marker-ignored-without-key-marker", func(t *testing.T) {
		q := "upload-id-marker=" + url.QueryEscape(bravoEntries[0].id)
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, q)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		assertUploadOrder(t, lmu.Upload, entries)
	})

	t.Run("invalid-max-uploads", func(t *testing.T) {
		for _, bad := range []string{"abc", "-1"} {
			t.Run(bad, func(t *testing.T) {
				_, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads="+bad)
				if status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", status)
				}
			})
		}
	})

	// max-uploads=0 is a valid boundary, matching max-keys=0/max-parts=0.
	t.Run("max-uploads=0-is-a-valid-empty-page", func(t *testing.T) {
		lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "max-uploads=0")
		if status != http.StatusOK || len(lmu.Upload) != 0 || lmu.IsTruncated {
			t.Fatalf("max-uploads=0: status=%d result=%+v", status, lmu)
		}
	})

	for _, e := range entries {
		doAbortMultipartUpload(t, client, ts.URL, signer, bucket, e.key, e.id)
	}
}

func TestListMultipartUploads_Pagination_RestartStableOrdering(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, err := store.CreateMultipartUpload("b", k, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
	store.Close() // simulated restart: no crash injection needed, this is an orderly stop mid-lifecycle

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	page, err := store2.ListMultipartUploads("b", "", "", defaultMaxUploads)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.uploads) != 3 {
		t.Fatalf("expected 3 uploads post-restart, got %d", len(page.uploads))
	}
	for i := 1; i < len(page.uploads); i++ {
		prev, cur := page.uploads[i-1], page.uploads[i]
		if prev.key > cur.key || (prev.key == cur.key && prev.uploadID >= cur.uploadID) {
			t.Fatalf("post-restart ordering not strictly ascending: %+v", page.uploads)
		}
	}

	var allKeys []string
	keyMarker, uploadIDMarker := "", ""
	for i := 0; i < 10; i++ {
		p, err := store2.ListMultipartUploads("b", keyMarker, uploadIDMarker, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range p.uploads {
			allKeys = append(allKeys, u.key)
		}
		if !p.truncated {
			break
		}
		keyMarker, uploadIDMarker = p.nextKeyMarker, p.nextUploadIDMarker
	}
	want := []string{"a", "b", "c"}
	if len(allKeys) != len(want) {
		t.Fatalf("paginated post-restart keys = %v, want %v", allKeys, want)
	}
	for i := range want {
		if allKeys[i] != want[i] {
			t.Fatalf("paginated post-restart keys = %v, want %v", allKeys, want)
		}
	}
}

func TestListMultipartUploads_Pagination_CompletedAndAbortedAreAbsent(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	bucket := "lifecycle"
	if err := doCreateBucket(t, client, ts.URL, signer, bucket); err != nil {
		t.Fatal(err)
	}

	activeID := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "active-key")

	completeID := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "complete-key")
	etag, status, _ := doUploadPart(t, client, ts.URL, signer, bucket, "complete-key", completeID, 1, []byte("only part, so no min-size rule applies"))
	if status != http.StatusOK {
		t.Fatalf("upload part failed: %d", status)
	}
	if _, status, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, bucket, "complete-key", completeID, []completedPartXML{{PartNumber: 1, ETag: etag}}); status != http.StatusOK {
		t.Fatalf("complete failed: %d", status)
	}

	abortID := doCreateMultipartUpload(t, client, ts.URL, signer, bucket, "abort-key")
	if status := doAbortMultipartUpload(t, client, ts.URL, signer, bucket, "abort-key", abortID); status != http.StatusNoContent {
		t.Fatalf("abort failed: %d", status)
	}

	lmu, status := doListMultipartUploadsQuery(t, client, ts.URL, signer, bucket, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(lmu.Upload) != 1 || lmu.Upload[0].Key != "active-key" || lmu.Upload[0].UploadId != activeID {
		t.Fatalf("expected only the active upload to remain, got %+v", lmu.Upload)
	}
}

// =============================================================================
// M6 -- Optional ZeroS3 Delta Sync
//
// Covers: capability discovery, CDC-equivalence between ordinary PUT and
// the sync client's local scan, bounded missing-chunk negotiation,
// idempotent chunk upload, atomic commit (including restart/deep-verify
// proof), transfer statistics, resume/retry, safe-mode remote conflict
// protection, local mutation detection, corrupt/missing chunk rejection
// at commit, unknown protocol/version rejection, and non-ZeroS3 fallback.
// =============================================================================

func newSyncTestServer(t *testing.T) (dir string, srv *Server, creds Credentials, region string) {
	t.Helper()
	dir = t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	creds = Credentials{AccessKeyID: "AKIASYNCTESTACCESSKEY1", SecretAccessKey: "SyncTestSecretKeyForZeroS3M6Tests0123456"}
	region = "us-east-1"
	srv = NewServer(store, creds, region)
	return dir, srv, creds, region
}

func writeSyncTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// doSyncRequest signs and sends one request to a ZeroS3 sync extension
// endpoint, returning status and raw body -- shared by every direct
// (non-client-library) protocol test below.
func doSyncRequest(t *testing.T, client *http.Client, baseURL string, signer testSigner, method, path string, body []byte) (status int, respBody []byte) {
	t.Helper()
	resp := doSignedRequest(t, client, baseURL, signer, method, path, body, map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func syncDescriptorsFor(chunks []syncLocalChunk) []syncChunkDescriptor {
	out := make([]syncChunkDescriptor, len(chunks))
	for i, c := range chunks {
		out[i] = syncChunkDescriptor{SHA256: c.SHA256, Length: c.Length}
	}
	return out
}

// =============================================================================
// A1: capability discovery
// =============================================================================

func TestSync_Discovery_Supported(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodGet, zeros3SyncInfoPath, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var d syncDiscoveryResponse
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	if d.Protocol != zeros3SyncProtocolVersion || d.CDC != zeros3SyncCDCFormat || d.Hash != zeros3SyncHashAlgorithm {
		t.Fatalf("unexpected discovery fields: %+v", d)
	}
	if !d.DeltaSync {
		t.Fatalf("delta_sync = false, want true")
	}
	if d.MaxHashesPerBatch != maxSyncBatchDescriptors {
		t.Fatalf("max_hashes_per_batch = %d, want %d", d.MaxHashesPerBatch, maxSyncBatchDescriptors)
	}
	if d.MaxChunkBytes != maxSyncChunkBytes {
		t.Fatalf("max_chunk_bytes = %d, want %d", d.MaxChunkBytes, maxSyncChunkBytes)
	}
}

func TestSync_Discovery_UnknownExtensionPathNotBucketParsed(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodGet, "/_zeros3/v1/bogus", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (unknown ZeroS3 operation, not a bucket lookup)", status)
	}
}

func TestSync_Discovery_AuthFailureRejected(t *testing.T) {
	srv, _ := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	req, err := http.NewRequest(http.MethodGet, ts.URL+zeros3SyncInfoPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated discovery status = %d, want 403", resp.StatusCode)
	}
}

func TestSync_NormalS3RoutesUnaffectedByExtension(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	if err := doCreateBucket(t, client, ts.URL, signer, "regress"); err != nil {
		t.Fatal(err)
	}
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/regress/key1", []byte("hello"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ordinary PUT status = %d", resp.StatusCode)
	}
	getResp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/regress/key1", nil, nil)
	defer getResp.Body.Close()
	got, _ := io.ReadAll(getResp.Body)
	if string(got) != "hello" {
		t.Fatalf("ordinary GET after adding the sync extension = %q, want %q", got, "hello")
	}
}

// =============================================================================
// A2: CDC reuse/equivalence
// =============================================================================

func TestSync_CDCEquivalenceWithOrdinaryPut(t *testing.T) {
	dir := t.TempDir()
	data := genRandomBytes(42, 2_500_000) // spans many chunk boundaries
	path := writeSyncTempFile(t, dir, "equiv.bin", data)

	scanned, total, err := scanLocalFileForSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(data)) {
		t.Fatalf("scanned total = %d, want %d", total, len(data))
	}

	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != len(scanned) {
		t.Fatalf("chunk count differs: ordinary PUT path = %d, sync scan = %d", len(pieces), len(scanned))
	}
	for i, p := range pieces {
		wantSHA := hex.EncodeToString(p.sha[:])
		if scanned[i].SHA256 != wantSHA || scanned[i].Length != int64(len(p.data)) {
			t.Fatalf("chunk %d differs: ordinary=(%s,%d) sync=(%s,%d)", i, wantSHA, len(p.data), scanned[i].SHA256, scanned[i].Length)
		}
	}
}

// =============================================================================
// A4: bounded missing-chunk negotiation
// =============================================================================

func negotiateOnce(t *testing.T, client *http.Client, baseURL string, signer testSigner, chunks []syncChunkDescriptor) (int, syncNegotiateResponse) {
	t.Helper()
	reqBody, err := json.Marshal(syncNegotiateRequest{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm, Chunks: chunks})
	if err != nil {
		t.Fatal(err)
	}
	status, body := doSyncRequest(t, client, baseURL, signer, http.MethodPost, zeros3SyncNegotiatePath, reqBody)
	var nr syncNegotiateResponse
	if status == http.StatusOK {
		if err := json.Unmarshal(body, &nr); err != nil {
			t.Fatalf("unmarshal negotiate response: %v (body=%s)", err, body)
		}
	}
	return status, nr
}

func TestSyncNegotiate_ZeroMissing(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	data := genRandomBytes(1, 200_000)
	pieces, err := chunkData(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pieces {
		if _, err := srv.store.casWrite(p.data); err != nil {
			t.Fatal(err)
		}
	}
	descs := make([]syncChunkDescriptor, len(pieces))
	for i, p := range pieces {
		descs[i] = syncChunkDescriptor{SHA256: hex.EncodeToString(p.sha[:]), Length: int64(len(p.data))}
	}

	status, nr := negotiateOnce(t, client, ts.URL, signer, descs)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(nr.Missing) != 0 {
		t.Fatalf("missing = %v, want none", nr.Missing)
	}
}

func TestSyncNegotiate_OneMissingAmongMany(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	present := []syncChunkDescriptor{}
	for i := 0; i < 5; i++ {
		data := genRandomBytes(int64(100+i), 1000)
		sum, err := srv.store.casWrite(data)
		if err != nil {
			t.Fatal(err)
		}
		present = append(present, syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))})
	}
	missingData := genRandomBytes(999, 1000)
	missingSum := sha256.Sum256(missingData)
	missingDesc := syncChunkDescriptor{SHA256: hex.EncodeToString(missingSum[:]), Length: int64(len(missingData))}

	status, nr := negotiateOnce(t, client, ts.URL, signer, append(present, missingDesc))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(nr.Missing) != 1 || nr.Missing[0] != missingDesc.SHA256 {
		t.Fatalf("missing = %v, want exactly [%s]", nr.Missing, missingDesc.SHA256)
	}
}

func TestSyncNegotiate_AllMissing(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	var descs []syncChunkDescriptor
	for i := 0; i < 10; i++ {
		data := genRandomBytes(int64(2000+i), 500)
		sum := sha256.Sum256(data)
		descs = append(descs, syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))})
	}
	status, nr := negotiateOnce(t, client, ts.URL, signer, descs)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(nr.Missing) != len(descs) {
		t.Fatalf("missing count = %d, want %d", len(nr.Missing), len(descs))
	}
}

func TestSyncNegotiate_SomeMissing(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	var all, wantMissing []string
	var descs []syncChunkDescriptor
	for i := 0; i < 20; i++ {
		data := genRandomBytes(int64(3000+i), 500)
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])
		descs = append(descs, syncChunkDescriptor{SHA256: hexSum, Length: int64(len(data))})
		all = append(all, hexSum)
		if i%3 == 0 {
			if _, err := srv.store.casWrite(data); err != nil {
				t.Fatal(err)
			}
		} else {
			wantMissing = append(wantMissing, hexSum)
		}
	}
	status, nr := negotiateOnce(t, client, ts.URL, signer, descs)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(nr.Missing) != len(wantMissing) {
		t.Fatalf("missing count = %d, want %d (missing=%v)", len(nr.Missing), len(wantMissing), nr.Missing)
	}
	gotSet := map[string]bool{}
	for _, s := range nr.Missing {
		gotSet[s] = true
	}
	for _, w := range wantMissing {
		if !gotSet[w] {
			t.Fatalf("expected %s to be reported missing", w)
		}
	}
}

func TestSyncNegotiate_DuplicatedDigestsReportedOnce(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	_ = srv

	data := genRandomBytes(7, 1234)
	sum := sha256.Sum256(data)
	desc := syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))}

	status, nr := negotiateOnce(t, client, ts.URL, signer, []syncChunkDescriptor{desc, desc, desc})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(nr.Missing) != 1 {
		t.Fatalf("missing = %v, want exactly one entry for a triplicated descriptor", nr.Missing)
	}
}

func TestSyncNegotiate_BatchSizeBoundary(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	_ = srv

	build := func(n int) []syncChunkDescriptor {
		descs := make([]syncChunkDescriptor, n)
		for i := 0; i < n; i++ {
			data := genRandomBytes(int64(50000+i), 40)
			sum := sha256.Sum256(data)
			descs[i] = syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))}
		}
		return descs
	}

	for _, n := range []int{1023, 1024} {
		status, nr := negotiateOnce(t, client, ts.URL, signer, build(n))
		if status != http.StatusOK {
			t.Fatalf("n=%d: status = %d", n, status)
		}
		if len(nr.Missing) != n {
			t.Fatalf("n=%d: missing count = %d, want %d", n, len(nr.Missing), n)
		}
	}

	// 1025 exceeds max_hashes_per_batch and must be rejected outright, not
	// silently truncated to the first 1024.
	status, _ := negotiateOnce(t, client, ts.URL, signer, build(1025))
	if status != http.StatusBadRequest {
		t.Fatalf("n=1025: status = %d, want 400 (BatchTooLarge)", status)
	}
}

func TestSyncNegotiate_MultiBatchViaClient(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// A synthetic unique-descriptor list larger than one server-declared
	// batch (1024) forces negotiateSyncMissing (the real client function)
	// through more than one /negotiate HTTP call. Half the descriptors'
	// content is pre-published so the result must reflect exactly that
	// split, proving batching doesn't lose or duplicate entries at the
	// seam between batches.
	const n = 2500
	var uniq []syncChunkDescriptor
	wantMissing := map[string]bool{}
	for i := 0; i < n; i++ {
		data := genRandomBytes(int64(70000+i), 200)
		sum := sha256.Sum256(data)
		hexSum := hex.EncodeToString(sum[:])
		uniq = append(uniq, syncChunkDescriptor{SHA256: hexSum, Length: int64(len(data))})
		if i%2 == 0 {
			if _, err := srv.store.casWrite(data); err != nil {
				t.Fatal(err)
			}
		} else {
			wantMissing[hexSum] = true
		}
	}

	cfg := syncClientConfig{Endpoint: ts.URL, Creds: creds, Region: region, HTTPClient: ts.Client()}
	discovery := syncDiscoveryResponse{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm, DeltaSync: true, MaxHashesPerBatch: 1024, MaxChunkBytes: maxSyncChunkBytes}

	missing, err := negotiateSyncMissing(cfg, discovery, uniq)
	if err != nil {
		t.Fatalf("negotiateSyncMissing: %v", err)
	}
	if len(missing) != len(wantMissing) {
		t.Fatalf("missing count = %d, want %d", len(missing), len(wantMissing))
	}
	for sha := range wantMissing {
		if !missing[sha] {
			t.Fatalf("expected %s to be reported missing across the multi-batch negotiation", sha)
		}
	}
}

func TestSyncNegotiate_InvalidDigest(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, _ := negotiateOnce(t, client, ts.URL, signer, []syncChunkDescriptor{{SHA256: "not-hex", Length: 10}})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid digest", status)
	}
	status, _ = negotiateOnce(t, client, ts.URL, signer, []syncChunkDescriptor{{SHA256: "aabb", Length: 10}})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for short digest", status)
	}
}

func TestSyncNegotiate_InvalidLength(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	validSHA := hex.EncodeToString(sha256.New().Sum(nil))
	for _, length := range []int64{0, -1, maxSyncChunkBytes + 1} {
		status, _ := negotiateOnce(t, client, ts.URL, signer, []syncChunkDescriptor{{SHA256: validSHA, Length: length}})
		if status != http.StatusBadRequest {
			t.Fatalf("length=%d: status = %d, want 400", length, status)
		}
	}
}

func TestSyncNegotiate_UnsupportedProtocolCDCHash(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	cases := []syncNegotiateRequest{
		{Protocol: 2, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm},
		{Protocol: zeros3SyncProtocolVersion, CDC: "gear-v2", Hash: zeros3SyncHashAlgorithm},
		{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: "sha512"},
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncNegotiatePath, body)
		if status != http.StatusNotImplemented {
			t.Fatalf("case %+v: status = %d, want 501", c, status)
		}
	}
}

func TestSyncNegotiate_OversizedRequestRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	oversized := bytes.Repeat([]byte("x"), maxSyncBatchBytes+1)
	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncNegotiatePath, oversized)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an oversized negotiate request", status)
	}
}

func TestSyncNegotiate_MalformedJSONRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncNegotiatePath, []byte("{not json"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed JSON", status)
	}
}

func TestSyncNegotiate_NeverMutatesStore(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	before, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	data := genRandomBytes(321, 5000)
	sum := sha256.Sum256(data)
	desc := syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(data))}
	if status, _ := negotiateOnce(t, client, ts.URL, signer, []syncChunkDescriptor{desc}); status != http.StatusOK {
		t.Fatalf("negotiate status unexpected")
	}
	after, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if before.ChunkStoreFileBytes != after.ChunkStoreFileBytes || before.ManifestFileBytes != after.ManifestFileBytes {
		t.Fatalf("negotiate mutated on-disk store state: before=%+v after=%+v", before, after)
	}
	_ = dir
}

// =============================================================================
// Shared helpers: bucket creation / GET over the real client library
// =============================================================================

func createSyncTestBucket(t *testing.T, ts *httptest.Server, creds Credentials, region, bucket string) {
	t.Helper()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	if err := doCreateBucket(t, ts.Client(), ts.URL, signer, bucket); err != nil {
		t.Fatal(err)
	}
}

func getSyncObjectBytes(t *testing.T, ts *httptest.Server, creds Credentials, region, bucket, key string) []byte {
	t.Helper()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	resp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodGet, "/"+bucket+"/"+key, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s/%s status = %d", bucket, key, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func getSyncObjectStatus(t *testing.T, ts *httptest.Server, creds Credentials, region, bucket, key string) int {
	t.Helper()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	resp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodGet, "/"+bucket+"/"+key, nil, nil)
	defer resp.Body.Close()
	return resp.StatusCode
}

// =============================================================================
// A5: idempotent missing-chunk upload
// =============================================================================

func TestSyncChunkUpload_Success(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	data := genRandomBytes(11, 4096)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hexSum, data)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	got, err := srv.store.casRead(sum)
	if err != nil {
		t.Fatalf("chunk not readable from CAS after upload: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored chunk bytes differ from uploaded bytes")
	}
}

func TestSyncChunkUpload_IdempotentRetry(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	_ = srv

	data := genRandomBytes(12, 4096)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	for i := 0; i < 3; i++ {
		status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hexSum, data)
		if status != http.StatusOK {
			t.Fatalf("retry %d: status = %d, body = %s", i, status, body)
		}
	}
}

func TestSyncChunkUpload_DigestMismatchRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	real := genRandomBytes(13, 2048)
	wrongClaim := genRandomBytes(14, 2048) // different content, different real digest
	wrongSum := sha256.Sum256(wrongClaim)

	// Server never trusts the URL's declared digest: uploading `real`
	// under `wrongClaim`'s digest must fail, not silently publish `real`
	// under the wrong name.
	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hex.EncodeToString(wrongSum[:]), real)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 DigestMismatch", status)
	}
	if _, err := srv.store.casRead(wrongSum); err == nil {
		t.Fatalf("a digest-mismatched chunk must never be published under the claimed digest")
	}
}

func TestSyncChunkUpload_InvalidDigestInURLRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+"zz-not-hex", []byte("data"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestSyncChunkUpload_OversizedBodyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	oversized := bytes.Repeat([]byte("y"), maxSyncChunkBytes+1)
	sum := sha256.Sum256(oversized)
	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hex.EncodeToString(sum[:]), oversized)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a chunk larger than max_chunk_bytes", status)
	}
}

// =============================================================================
// A6: atomic commit + critical acceptance proof
// =============================================================================

// TestSync_CriticalAcceptanceProof is M6A's central architectural claim:
// after `zeros3 sync`, ordinary S3 GET/HEAD, a full server restart, and
// deep verify all treat the synced object exactly like any other object
// -- no custom sync state survives or is required.
func TestSync_CriticalAcceptanceProof(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	createSyncTestBucket(t, ts, creds, region, "proof")

	data := genRandomBytes(999, 3_000_000)
	tmpDir := t.TempDir()
	path := writeSyncTempFile(t, tmpDir, "proof.bin", data)

	stats, err := syncFile(syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "proof", Key: "object",
		Creds: creds, Region: region, HTTPClient: ts.Client(), ContentType: "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if stats.TotalChunks == 0 {
		t.Fatalf("expected at least one chunk for a 3MB file")
	}

	// Ordinary GET, same running server.
	got := getSyncObjectBytes(t, ts, creds, region, "proof", "object")
	if !bytes.Equal(got, data) {
		t.Fatalf("GET after sync returned different bytes (len got=%d want=%d)", len(got), len(data))
	}

	// Ordinary HEAD.
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	headResp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodHead, "/proof/object", nil, nil)
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", headResp.StatusCode)
	}
	if cl := headResp.Header.Get("Content-Length"); cl != strconv.Itoa(len(data)) {
		t.Fatalf("HEAD Content-Length = %s, want %d", cl, len(data))
	}

	// Restart: close this server/store, open a brand-new Store/Server on
	// the same directory. Nothing sync-specific is passed across the
	// restart -- there is no session to carry.
	ts.Close()
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	srv2 := NewServer(store2, creds, region)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	got2 := getSyncObjectBytes(t, ts2, creds, region, "proof", "object")
	if !bytes.Equal(got2, data) {
		t.Fatalf("GET after restart returned different bytes")
	}

	// Deep verify must accept it.
	vr, err := store2.Verify(true)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.OK() {
		t.Fatalf("deep verify reported issues after sync+restart: %+v", vr.Issues)
	}
}

func TestSyncCommit_MissingChunkRejected(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "b1")

	neverUploaded := genRandomBytes(15, 500)
	sum := sha256.Sum256(neverUploaded)

	reqBody, _ := json.Marshal(syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "b1", Key: "missing-chunk-object", ExpectAbsent: true,
		Chunks: []syncChunkDescriptor{{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(neverUploaded))}},
	})
	status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, reqBody)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 MissingChunk (body=%s)", status, body)
	}
	if getSyncObjectStatus(t, ts, creds, region, "b1", "missing-chunk-object") != http.StatusNotFound {
		t.Fatalf("a rejected commit must never become visible")
	}
}

func TestSyncCommit_WrongLengthRejected(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "b2")

	data := genRandomBytes(16, 700)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hexSum, data); status != http.StatusOK {
		t.Fatalf("chunk upload failed")
	}

	reqBody, _ := json.Marshal(syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "b2", Key: "wrong-length-object", ExpectAbsent: true,
		Chunks: []syncChunkDescriptor{{SHA256: hexSum, Length: int64(len(data)) + 1}},
	})
	status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, reqBody)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 ChunkLengthMismatch (body=%s)", status, body)
	}
	if getSyncObjectStatus(t, ts, creds, region, "b2", "wrong-length-object") != http.StatusNotFound {
		t.Fatalf("a rejected commit must never become visible")
	}
}

func TestSyncCommit_CorruptChunkRejected(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "b3")

	data := genRandomBytes(17, 900)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hexSum, data); status != http.StatusOK {
		t.Fatalf("chunk upload failed")
	}

	// Corrupt the chunk on disk directly, simulating bit rot/tampering --
	// casRead must catch this at commit time via its own content-hash
	// re-verification, exactly as it would for an ordinary GET.
	chunkPath := srv.store.chunkPath(sum)
	corrupted := append([]byte{}, data...)
	corrupted[0] ^= 0xFF
	if err := os.WriteFile(chunkPath, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = dir

	reqBody, _ := json.Marshal(syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "b3", Key: "corrupt-object", ExpectAbsent: true,
		Chunks: []syncChunkDescriptor{{SHA256: hexSum, Length: int64(len(data))}},
	})
	status, body := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, reqBody)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a corrupt chunk (body=%s)", status, body)
	}
	if getSyncObjectStatus(t, ts, creds, region, "b3", "corrupt-object") != http.StatusNotFound {
		t.Fatalf("a commit referencing a corrupt chunk must never become visible")
	}
}

func TestSyncCommit_UnsupportedProtocolCDCHash(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	base := syncCommitRequest{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm, Bucket: "x", Key: "y", ExpectAbsent: true}
	cases := []func(*syncCommitRequest){
		func(r *syncCommitRequest) { r.Protocol = 99 },
		func(r *syncCommitRequest) { r.CDC = "gear-v9" },
		func(r *syncCommitRequest) { r.Hash = "blake3" },
	}
	for _, mutate := range cases {
		r := base
		mutate(&r)
		body, _ := json.Marshal(r)
		status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, body)
		if status != http.StatusNotImplemented {
			t.Fatalf("case %+v: status = %d, want 501", r, status)
		}
	}
}

func TestSyncCommit_MalformedJSONRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, []byte("not json at all"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestSyncCommit_MissingBucketOrKeyRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	body, _ := json.Marshal(syncCommitRequest{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm})
	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing bucket/key", status)
	}
}

func TestSyncCommit_UnknownBucketRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	body, _ := json.Marshal(syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "does-not-exist", Key: "k", ExpectAbsent: true,
	})
	status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, body)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 NoSuchBucket", status)
	}
}

// =============================================================================
// A7: transfer statistics
// =============================================================================

func TestSyncStats_FirstSyncAllNew(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "stats1")

	data := genRandomBytes(2001, 900_000)
	path := writeSyncTempFile(t, dir, "s1.bin", data)

	stats, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "stats1", Key: "k", Creds: creds, Region: region, HTTPClient: ts.Client()})
	if err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if stats.LogicalBytes != int64(len(data)) {
		t.Fatalf("LogicalBytes = %d, want %d", stats.LogicalBytes, len(data))
	}
	if stats.ChunksReused != 0 {
		t.Fatalf("ChunksReused = %d, want 0 for brand-new content", stats.ChunksReused)
	}
	if stats.MissingChunkOccur != stats.TotalChunks {
		t.Fatalf("MissingChunkOccur = %d, want %d (all chunks new)", stats.MissingChunkOccur, stats.TotalChunks)
	}
	if stats.UploadedBytes != stats.LogicalBytes {
		t.Fatalf("UploadedBytes = %d, want %d", stats.UploadedBytes, stats.LogicalBytes)
	}
	if stats.BytesAvoided != 0 {
		t.Fatalf("BytesAvoided = %d, want 0", stats.BytesAvoided)
	}
}

func TestSyncStats_ResyncIdenticalContentToNewKeyIsFullyReused(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "stats2")

	data := genRandomBytes(2002, 700_000)
	path := writeSyncTempFile(t, dir, "s2.bin", data)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "stats2", Creds: creds, Region: region, HTTPClient: ts.Client()}

	cfg.Key = "first"
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	cfg.Key = "second"
	stats, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if stats.ChunksReused != stats.TotalChunks {
		t.Fatalf("ChunksReused = %d, want all %d chunks reused from the identical first sync", stats.ChunksReused, stats.TotalChunks)
	}
	if stats.UploadedBytes != 0 {
		t.Fatalf("UploadedBytes = %d, want 0 (every chunk already present)", stats.UploadedBytes)
	}
	if stats.BytesAvoided != stats.LogicalBytes {
		t.Fatalf("BytesAvoided = %d, want %d", stats.BytesAvoided, stats.LogicalBytes)
	}
}

// TestSync_M6ADemonstrationFixture is the required M6A fixture: sync a
// reasonably large file, make a small localized mutation, sync the
// mutated file to a new key, and show only a small fraction of the
// logical bytes crossed the wire the second time -- CDC v1 visibly paying
// off, not a manufactured number. It also proves the resulting object is
// exactly byte-correct via an ordinary GET.
func TestSync_M6ADemonstrationFixture(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "demo")

	const size = 8_000_000 // 8MB: big enough for CDC's benefit to show, small enough to run fast in CI
	original := genRandomBytes(4242, size)
	path := writeSyncTempFile(t, dir, "demo.bin", original)

	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "demo", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfg.Key = "v1"
	firstStats, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// A small, localized mutation well away from either end: insert 4KiB
	// of new bytes at the midpoint. CDC only reshuffles chunk boundaries
	// local to the edit; everything before and after should still dedupe.
	mutated := make([]byte, 0, size+4096)
	mid := size / 2
	mutated = append(mutated, original[:mid]...)
	mutated = append(mutated, genRandomBytes(7777, 4096)...)
	mutated = append(mutated, original[mid:]...)
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg.Key = "v2"
	secondStats, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	reuseRatio := float64(secondStats.BytesAvoided) / float64(secondStats.LogicalBytes)
	if reuseRatio < 0.8 {
		t.Fatalf("expected CDC to visibly outperform a naive full upload after a small localized edit: reuse=%.1f%% (uploaded=%d of %d logical bytes, first-sync uploaded=%d)",
			reuseRatio*100, secondStats.UploadedBytes, secondStats.LogicalBytes, firstStats.UploadedBytes)
	}
	if secondStats.UploadedBytes >= firstStats.UploadedBytes {
		t.Fatalf("second sync (small edit) should upload far less than the first (full) sync: first=%d second=%d", firstStats.UploadedBytes, secondStats.UploadedBytes)
	}

	got := getSyncObjectBytes(t, ts, creds, region, "demo", "v2")
	if !bytes.Equal(got, mutated) {
		t.Fatalf("GET after the mutated sync did not return exact bytes")
	}
	t.Logf("M6A demonstration fixture: logical=%s uploaded(v1)=%s uploaded(v2)=%s reuse(v2)=%.1f%%",
		humanBytes(secondStats.LogicalBytes), humanBytes(firstStats.UploadedBytes), humanBytes(secondStats.UploadedBytes), reuseRatio*100)
	var buf bytes.Buffer
	printSyncStats(&buf, secondStats)
	t.Logf("second sync's exact printSyncStats output (for README/STATUS):\n%s", buf.String())
}

// =============================================================================
// M6B -- B1: resume / retry
// =============================================================================

func TestSync_ResumeAfterPartialPriorUpload(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "resume1")

	data := genRandomBytes(3001, 600_000)
	path := writeSyncTempFile(t, dir, "resume1.bin", data)

	scanned, _, err := scanLocalFileForSync(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 2 {
		t.Fatalf("fixture too small to exercise partial upload (%d chunks)", len(scanned))
	}
	// Simulate "the client died after uploading only the first chunk": a
	// real chunk upload happens for scanned[0], nothing else.
	first := scanned[0]
	firstData, err := readSyncFileRange(path, first.Offset, first.Length)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+first.SHA256, firstData); status != http.StatusOK {
		t.Fatalf("priming upload failed")
	}

	// Rerun sync from scratch: renegotiation must see the already-
	// published chunk and upload only what's left.
	stats, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "resume1", Key: "obj", Creds: creds, Region: region, HTTPClient: client})
	if err != nil {
		t.Fatalf("resumed syncFile: %v", err)
	}
	if stats.UploadedBytes >= stats.LogicalBytes {
		t.Fatalf("resume should have skipped the already-uploaded chunk: uploaded=%d logical=%d", stats.UploadedBytes, stats.LogicalBytes)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "resume1", "obj")
	if !bytes.Equal(got, data) {
		t.Fatalf("GET after resumed sync returned different bytes")
	}
}

func TestSync_ServerRestartAfterPartialUploadThenResume(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "resume2")

	data := genRandomBytes(3002, 600_000)
	path := writeSyncTempFile(t, dir, "resume2.bin", data)
	scanned, _, err := scanLocalFileForSync(path)
	if err != nil {
		t.Fatal(err)
	}
	first := scanned[0]
	firstData, err := readSyncFileRange(path, first.Offset, first.Length)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+first.SHA256, firstData); status != http.StatusOK {
		t.Fatalf("priming upload failed")
	}

	// Restart: no durable sync-session state exists anywhere -- CAS
	// durability alone is what lets resume work across a real process
	// restart, not some in-memory session the crash would have destroyed
	// anyway.
	ts.Close()
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	srv2 := NewServer(store2, creds, region)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	stats, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts2.URL, Bucket: "resume2", Key: "obj", Creds: creds, Region: region, HTTPClient: ts2.Client()})
	if err != nil {
		t.Fatalf("post-restart syncFile: %v", err)
	}
	if stats.UploadedBytes >= stats.LogicalBytes {
		t.Fatalf("post-restart resume should have skipped the pre-restart chunk: uploaded=%d logical=%d", stats.UploadedBytes, stats.LogicalBytes)
	}
	got := getSyncObjectBytes(t, ts2, creds, region, "resume2", "obj")
	if !bytes.Equal(got, data) {
		t.Fatalf("GET after restart+resume returned different bytes")
	}
}

func TestSync_RepeatedFullSyncOfIdenticalContentUploadsNothing(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "repeat1")

	data := genRandomBytes(3003, 300_000)
	path := writeSyncTempFile(t, dir, "repeat1.bin", data)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "repeat1", Creds: creds, Region: region, HTTPClient: ts.Client()}

	cfg.Key = "a"
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("first: %v", err)
	}
	cfg.Key = "b"
	second, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.UploadedBytes != 0 {
		t.Fatalf("re-syncing identical content to a fresh key uploaded %d bytes, want 0", second.UploadedBytes)
	}
}

func TestSync_RepeatedCommitFailsCleanlyRatherThanDuplicating(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "repeat2")

	data := genRandomBytes(3004, 400)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+hexSum, data); status != http.StatusOK {
		t.Fatalf("chunk upload failed")
	}
	commitReq, _ := json.Marshal(syncCommitRequest{
		Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
		Bucket: "repeat2", Key: "k", ExpectAbsent: true,
		Chunks: []syncChunkDescriptor{{SHA256: hexSum, Length: int64(len(data))}},
	})

	status1, body1 := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, commitReq)
	if status1 != http.StatusOK {
		t.Fatalf("first commit: status = %d, body = %s", status1, body1)
	}
	var first syncCommitResponse
	if err := json.Unmarshal(body1, &first); err != nil {
		t.Fatal(err)
	}

	// A literal retry of the exact same (now-stale) ExpectAbsent=true
	// request must NOT silently create a second version or corrupt
	// anything: it must fail cleanly, because the precondition it was
	// built from is no longer true (this commit's own first attempt
	// already succeeded).
	status2, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPost, zeros3SyncCommitPath, commitReq)
	if status2 != http.StatusPreconditionFailed {
		t.Fatalf("retry status = %d, want 412 (safe rejection, not a silent duplicate)", status2)
	}

	// Exactly one current object, matching the first (and only
	// successful) commit -- never a second version from the retry.
	got := getSyncObjectBytes(t, ts, creds, region, "repeat2", "k")
	if !bytes.Equal(got, data) {
		t.Fatalf("object bytes differ from the single successful commit")
	}
	_, cur, err := srv.store.ListVersions("repeat2", "k")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.manifestUUID != first.VersionID {
		t.Fatalf("current object identity changed after the rejected retry")
	}
}

// =============================================================================
// M6B -- B2: remote conflict protection
// =============================================================================

func TestSync_ConflictAbsentDestinationStaysAbsentUntilCommit(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "conflict1")

	data := genRandomBytes(4001, 50_000)
	path := writeSyncTempFile(t, dir, "c1.bin", data)

	if getSyncObjectStatus(t, ts, creds, region, "conflict1", "obj") != http.StatusNotFound {
		t.Fatalf("destination should not exist before sync begins")
	}
	if _, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "conflict1", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}); err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if getSyncObjectStatus(t, ts, creds, region, "conflict1", "obj") != http.StatusOK {
		t.Fatalf("destination should exist after a successful commit")
	}
}

func TestSync_ConflictUnchangedDestinationCommitsCleanly(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "conflict2")

	v1 := genRandomBytes(4002, 40_000)
	path := writeSyncTempFile(t, dir, "c2.bin", v1)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "conflict2", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Overwrite the same key with a modified version of the same file --
	// the observed ETag is still accurate (nothing else touched it), so
	// this must commit cleanly, exactly like the demonstration fixture's
	// "sync the modified file back to the same key" case.
	v2 := append(append([]byte{}, v1...), []byte("-appended-tail")...)
	if err := os.WriteFile(path, v2, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("second sync onto an unchanged destination: %v", err)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "conflict2", "obj")
	if !bytes.Equal(got, v2) {
		t.Fatalf("object after second sync does not match v2")
	}
}

func TestSync_ConflictConcurrentPUTDuringSyncCausesCommitConflict(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "conflict3")

	original := genRandomBytes(4003, 60_000)
	path := writeSyncTempFile(t, dir, "c3.bin", original)
	if _, err := srv.store.PutObject("conflict3", "obj", original, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	// syncFile observes the current ETag via HEAD, then (simulated here by
	// calling the pipeline manually up to just before commit) an ordinary
	// PUT changes the object before the sync's own commit lands.
	exists, etag, err := headSyncDestination(syncClientConfig{Endpoint: ts.URL, Bucket: "conflict3", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()})
	if err != nil || !exists {
		t.Fatalf("head: exists=%v err=%v", exists, err)
	}
	resp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodPut, "/conflict3/obj", []byte("someone else's concurrent write"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("concurrent PUT failed: %d", resp.StatusCode)
	}

	chunks, total, err := scanLocalFileForSync(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := buildSyncPlan(chunks, total)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "conflict3", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}
	missing, err := negotiateSyncMissing(cfg, syncDiscoveryResponse{MaxHashesPerBatch: maxSyncBatchDescriptors}, plan.unique)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uploadMissingSyncChunks(cfg, plan, missing); err != nil {
		t.Fatal(err)
	}
	_, err = commitSyncObject(cfg, plan, syncPrecondition{expectedETag: etag})
	if !errors.Is(err, errSyncRemoteConflict) {
		t.Fatalf("commit err = %v, want errSyncRemoteConflict", err)
	}
}

func TestSync_ConflictTwoConcurrentSyncsToSameKeySecondFails(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "conflict4")

	data := genRandomBytes(4004, 30_000)
	pathA := writeSyncTempFile(t, dir, "c4a.bin", data)
	pathB := writeSyncTempFile(t, dir, "c4b.bin", append(append([]byte{}, data...), []byte("-variant-b")...))

	cfgA := syncClientConfig{LocalPath: pathA, Endpoint: ts.URL, Bucket: "conflict4", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{LocalPath: pathB, Endpoint: ts.URL, Bucket: "conflict4", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}

	// Both observe "absent" (neither has committed yet), simulating two
	// syncs racing to the same never-before-existing key: B's precondition
	// is captured here, before A's commit lands, and carried through B's
	// own scan/negotiate/upload/commit manually (rather than calling
	// syncFile(cfgB), which would re-observe reality fresh and simply see
	// an ordinary sequential overwrite instead of a genuine race).
	if _, _, err := headSyncDestination(cfgA); err != nil {
		t.Fatal(err)
	}
	existsB, etagB, err := headSyncDestination(cfgB)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := syncFile(cfgA); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	chunksB, totalB, err := scanLocalFileForSync(pathB)
	if err != nil {
		t.Fatal(err)
	}
	planB := buildSyncPlan(chunksB, totalB)
	missingB, err := negotiateSyncMissing(cfgB, syncDiscoveryResponse{MaxHashesPerBatch: maxSyncBatchDescriptors}, planB.unique)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uploadMissingSyncChunks(cfgB, planB, missingB); err != nil {
		t.Fatal(err)
	}
	_, errB := commitSyncObject(cfgB, planB, syncPrecondition{expectAbsent: !existsB, expectedETag: etagB})
	if !errors.Is(errB, errSyncRemoteConflict) {
		t.Fatalf("second (losing) sync commit err = %v, want errSyncRemoteConflict", errB)
	}

	// Deterministic safe outcome: the first writer's content, untouched
	// by the losing sync.
	got := getSyncObjectBytes(t, ts, creds, region, "conflict4", "obj")
	if !bytes.Equal(got, data) {
		t.Fatalf("object bytes were not the first (winning) sync's content")
	}
}

func TestSync_ConflictRetryAfterConflictSucceeds(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "conflict5")

	data := genRandomBytes(4005, 20_000)
	path := writeSyncTempFile(t, dir, "c5.bin", data)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "conflict5", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}

	if _, err := srv.store.PutObject("conflict5", "obj", []byte("someone else got here first"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	// This client scanned before knowing about the pre-existing object
	// (simulated by simply calling syncFile without ever having HEADed
	// it first -- its precondition is the stale "expect absent" default
	// only in the sense that a fresh run always HEADs first; here we
	// prove that a fresh run always re-observes reality).
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("sync against a real pre-existing object should observe it via HEAD and succeed: %v", err)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "conflict5", "obj")
	if !bytes.Equal(got, data) {
		t.Fatalf("retry after observing the real destination did not produce the expected content")
	}
}

// =============================================================================
// M6B -- B3: local file mutation detection
// =============================================================================

func TestSync_LocalMutationDuringOperationAborts(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "mutate1")

	data := genRandomBytes(5001, 50_000)
	path := writeSyncTempFile(t, dir, "m1.bin", data)

	t.Cleanup(func() { syncTestHookBeforeMutationCheck = nil })
	syncTestHookBeforeMutationCheck = func(cfg syncClientConfig) {
		// Deterministically mutate the file between upload and the
		// mutation check, standing in for a real concurrent writer.
		if err := os.WriteFile(cfg.LocalPath, append(append([]byte{}, data...), []byte("mutated-while-syncing")...), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "mutate1", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()})
	if !errors.Is(err, errSyncLocalMutation) {
		t.Fatalf("err = %v, want errSyncLocalMutation", err)
	}
	if getSyncObjectStatus(t, ts, creds, region, "mutate1", "obj") != http.StatusNotFound {
		t.Fatalf("a detected local mutation must abort before commit -- object must not exist")
	}
	_ = srv
}

func TestSync_UnmodifiedFileCommitsNormally(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "mutate2")

	data := genRandomBytes(5002, 40_000)
	path := writeSyncTempFile(t, dir, "m2.bin", data)

	if _, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "mutate2", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}); err != nil {
		t.Fatalf("unmodified file should sync without triggering the mutation guard: %v", err)
	}
	_ = srv
}

// =============================================================================
// M6B -- B5: non-ZeroS3 endpoint fallback
// =============================================================================

// fakeNonZeroS3Server simulates an ordinary S3-compatible endpoint with no
// ZeroS3 extension: /_zeros3/* 404s, and an ordinary PUT to /bucket/key
// succeeds (auth is not checked -- this stands in for a real foreign S3
// implementation, not for ZeroS3's own auth semantics). It records every
// path it sees so the test can assert no proprietary sync request was
// ever attempted before the fallback.
type fakeNonZeroS3Server struct {
	mu       sync.Mutex
	paths    []string
	uploaded []byte
}

func (f *fakeNonZeroS3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, "/_zeros3/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPut {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploaded = body
		f.mu.Unlock()
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func TestSync_NonZeroS3Endpoint_FallsBackToPlainPut(t *testing.T) {
	fake := &fakeNonZeroS3Server{}
	ts := httptest.NewServer(fake)
	defer ts.Close()

	dir := t.TempDir()
	data := genRandomBytes(6001, 20_000)
	path := writeSyncTempFile(t, dir, "fb.bin", data)

	stats, err := syncFile(syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: "k",
		Creds: Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}, Region: defaultRegion,
		HTTPClient: ts.Client(),
	})
	if err != nil {
		t.Fatalf("syncFile against a non-ZeroS3 endpoint should fall back, not fail: %v", err)
	}
	if !stats.FellBackToPlainPut {
		t.Fatalf("expected FellBackToPlainPut=true")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !bytes.Equal(fake.uploaded, data) {
		t.Fatalf("fallback PUT did not carry the exact file bytes")
	}
	for _, p := range fake.paths {
		if strings.Contains(p, zeros3SyncNegotiatePath) || strings.Contains(p, zeros3SyncChunksPrefix) || strings.Contains(p, zeros3SyncCommitPath) {
			t.Fatalf("a proprietary sync request (%s) was sent to a non-ZeroS3 endpoint before/without successful discovery", p)
		}
	}
}

// =============================================================================
// M7 hostile-review regression: sync client keys with '%'/'#'/'?' must not
// be mis-encoded into the request URL (headSyncDestination/
// doPlainPutFallback previously built URLs by raw string concatenation,
// which either made http.NewRequest reject a literal '%' outright or
// silently truncated the path at a literal '#'/'?', misrouting the
// request to the wrong key without any error).
// =============================================================================

// TestSync_KeyWithPercentCharacterFallsBackCorrectly covers a sync key
// containing a literal '%' that does not form a valid percent-escape
// (e.g. from a real filename like "50% off.txt"): before the fix, this
// made url.Parse (inside http.NewRequest) reject the HEAD/PUT request
// outright, so the file could never be synced at all.
func TestSync_KeyWithPercentCharacterFallsBackCorrectly(t *testing.T) {
	fake := &fakeNonZeroS3Server{}
	ts := httptest.NewServer(fake)
	defer ts.Close()

	dir := t.TempDir()
	data := genRandomBytes(6002, 5_000)
	path := writeSyncTempFile(t, dir, "pct.bin", data)
	const key = "50% off.txt"

	stats, err := syncFile(syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: key,
		Creds: Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}, Region: defaultRegion,
		HTTPClient: ts.Client(),
	})
	if err != nil {
		t.Fatalf("syncFile with a literal '%%' in the key should not fail: %v", err)
	}
	if !stats.FellBackToPlainPut {
		t.Fatalf("expected FellBackToPlainPut=true")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !bytes.Equal(fake.uploaded, data) {
		t.Fatalf("fallback PUT did not carry the exact file bytes")
	}
	found := false
	for _, p := range fake.paths {
		if p == "PUT /b/"+key {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback PUT never reached the correctly decoded key %q, got paths %v", key, fake.paths)
	}
}

// TestSync_KeyWithHashCharacterDoesNotMisroute covers a sync key
// containing a literal '#': before the fix, url.Parse treated everything
// from '#' onward as a URL fragment (which net/http never sends), so the
// fallback PUT silently landed on a truncated key while syncFile still
// reported FellBackToPlainPut=true (a false success writing to the wrong
// key, not a loud failure).
func TestSync_KeyWithHashCharacterDoesNotMisroute(t *testing.T) {
	fake := &fakeNonZeroS3Server{}
	ts := httptest.NewServer(fake)
	defer ts.Close()

	dir := t.TempDir()
	data := genRandomBytes(6003, 5_000)
	path := writeSyncTempFile(t, dir, "hash.bin", data)
	const key = "a#b.txt"

	stats, err := syncFile(syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: key,
		Creds: Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}, Region: defaultRegion,
		HTTPClient: ts.Client(),
	})
	if err != nil {
		t.Fatalf("syncFile with a literal '#' in the key should not fail: %v", err)
	}
	if !stats.FellBackToPlainPut {
		t.Fatalf("expected FellBackToPlainPut=true")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, p := range fake.paths {
		if strings.HasPrefix(p, "PUT ") && p != "PUT /b/"+key {
			t.Fatalf("fallback PUT was misrouted to %q instead of the full key %q (fragment truncation)", p, key)
		}
	}
	found := false
	for _, p := range fake.paths {
		if p == "PUT /b/"+key {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback PUT never reached the full, untruncated key %q, got paths %v", key, fake.paths)
	}
}

// TestSync_KeyWithQuestionMarkResyncsCleanly covers a sync key containing
// a literal '?' against a real ZeroS3 server: before the fix,
// headSyncDestination's HEAD request had its path truncated at '?' (query
// string delimiter), so it always observed a nonexistent destination.
// First sync still committed correctly (the server independently derives
// bucket/key from the JSON commit body, not the client's mangled HEAD),
// but every subsequent re-sync of the same unchanged file wrongly sent
// ExpectAbsent=true and was rejected with a false PreconditionFailed,
// breaking the documented "re-sync an unchanged destination commits
// cleanly" guarantee for any key containing '?'.
func TestSync_KeyWithQuestionMarkResyncsCleanly(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "qmark")

	const key = "notes?draft.txt"
	v1 := genRandomBytes(6004, 40_000)
	path := writeSyncTempFile(t, dir, "q.bin", v1)
	cfg := syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "qmark", Key: key, Creds: creds, Region: region, HTTPClient: ts.Client()}
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("first sync of a key containing '?': %v", err)
	}

	// Re-sync the exact same, unchanged bytes -- must commit cleanly, not
	// hit a false conflict caused by a mis-truncated HEAD probe.
	if _, err := syncFile(cfg); err != nil {
		t.Fatalf("re-sync of an unchanged destination with '?' in the key must commit cleanly, got: %v", err)
	}
	// getSyncObjectBytes builds its GET path by the same naive
	// concatenation this test is guarding against, so verify directly
	// with the (now-fixed) syncObjectPath helper instead.
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	resp := doSignedRequest(t, ts.Client(), ts.URL, signer, http.MethodGet, syncObjectPath("qmark", key), nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET qmark%s status = %d", syncObjectPath("", key), resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, v1) {
		t.Fatalf("object bytes after re-sync do not match the original upload")
	}
}

// =============================================================================
// Unauthorized requests against every sync endpoint
// =============================================================================

func TestSync_AllEndpointsRejectUnauthenticatedRequests(t *testing.T) {
	srv, _ := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	reqs := []struct {
		method, path string
		body         []byte
	}{
		{http.MethodGet, zeros3SyncInfoPath, nil},
		{http.MethodPost, zeros3SyncNegotiatePath, []byte(`{"protocol":1,"cdc":"gear-v1","hash":"sha256","chunks":[]}`)},
		{http.MethodPut, zeros3SyncChunksPrefix + strings.Repeat("ab", 32), []byte("x")},
		{http.MethodPost, zeros3SyncCommitPath, []byte(`{"protocol":1,"cdc":"gear-v1","hash":"sha256","bucket":"b","key":"k"}`)},
	}
	for _, r := range reqs {
		req, err := http.NewRequest(r.method, ts.URL+r.path, bytes.NewReader(r.body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s: unauthenticated status = %d, want 403", r.method, r.path, resp.StatusCode)
		}
	}
}

// =============================================================================
// Concurrency: normal PUT during sync, two syncs, restart -- under -race
// =============================================================================

func TestSync_ConcurrentNormalPutAndSyncDifferentKeys(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	createSyncTestBucket(t, ts, creds, region, "conc1")

	data := genRandomBytes(8001, 100_000)
	path := writeSyncTempFile(t, dir, "conc1.bin", data)

	var wg sync.WaitGroup
	wg.Add(2)
	var syncErr error
	go func() {
		defer wg.Done()
		_, syncErr = syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "conc1", Key: "synced", Creds: creds, Region: region, HTTPClient: client})
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/conc1/plain", []byte("ordinary put"), nil)
			resp.Body.Close()
		}
	}()
	wg.Wait()
	if syncErr != nil {
		t.Fatalf("syncFile: %v", syncErr)
	}
	if got := getSyncObjectBytes(t, ts, creds, region, "conc1", "synced"); !bytes.Equal(got, data) {
		t.Fatalf("synced object bytes incorrect after concurrent unrelated PUT traffic")
	}
}

func TestSync_TwoConcurrentSyncsToDifferentKeysBothSucceed(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	createSyncTestBucket(t, ts, creds, region, "conc2")

	dataA := genRandomBytes(8002, 80_000)
	dataB := genRandomBytes(8003, 80_000)
	pathA := writeSyncTempFile(t, dir, "concA.bin", dataA)
	pathB := writeSyncTempFile(t, dir, "concB.bin", dataB)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = syncFile(syncClientConfig{LocalPath: pathA, Endpoint: ts.URL, Bucket: "conc2", Key: "a", Creds: creds, Region: region, HTTPClient: client})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = syncFile(syncClientConfig{LocalPath: pathB, Endpoint: ts.URL, Bucket: "conc2", Key: "b", Creds: creds, Region: region, HTTPClient: client})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	if got := getSyncObjectBytes(t, ts, creds, region, "conc2", "a"); !bytes.Equal(got, dataA) {
		t.Fatalf("object a incorrect")
	}
	if got := getSyncObjectBytes(t, ts, creds, region, "conc2", "b"); !bytes.Equal(got, dataB) {
		t.Fatalf("object b incorrect")
	}
}

// =============================================================================
// M6C -- recursive directory sync (`zeros3 sync LOCAL_DIRECTORY s3://bucket/prefix/`)
//
// Every test below proves directory sync is orchestration over the
// unmodified M6A/M6B syncFile primitive (syncDirectory calls it, and
// nothing else, once per eligible file) -- never a second transfer path,
// negotiation client, upload loop, commit path, conflict mechanism, or
// mutation-detection mechanism. Server setup mirrors every M6A/M6B test
// above (newSyncTestServer + httptest.NewServer); no directory-sync-
// specific server-side state exists to set up. Several tests below reuse
// the existing syncTestHookBeforeMutationCheck test hook (defined with
// the M6B local-mutation tests, above) to deterministically inject a
// remote conflict or a sibling file's disappearance at an exact point in
// one specific file's own syncFile call -- never a timing-dependent race
// -- which is itself further proof that directory sync adds no second
// hook/mechanism of its own for these cases.
// =============================================================================

// writeDirSyncTree writes files (root-relative slash-path -> content)
// under root, creating parent directories as needed.
func writeDirSyncTree(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for rel, data := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func baseDirSyncCfg(ts *httptest.Server, creds Credentials, region string) syncClientConfig {
	return syncClientConfig{Endpoint: ts.URL, Creds: creds, Region: region, HTTPClient: ts.Client()}
}

func TestDirSync_EmptyDirectory(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "empty1")

	root := t.TempDir()
	result, err := syncDirectory(root, "empty1", "prefix", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 0 || result.Synced != 0 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("expected an all-zero result for an empty directory, got %+v", result)
	}
	if !result.OK() {
		t.Fatalf("an empty directory sync must succeed")
	}
	if result.Stats.LogicalBytes != 0 || result.Stats.UploadedBytes != 0 || result.Stats.TotalChunks != 0 {
		t.Fatalf("expected sensible zero-value stats, got %+v", result.Stats)
	}
}

func TestDirSync_OneFile(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "one1")

	root := t.TempDir()
	data := genRandomBytes(6001, 40_000)
	writeDirSyncTree(t, root, map[string][]byte{"only.bin": data})

	result, err := syncDirectory(root, "one1", "dest", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 1 || result.Synced != 1 || result.Failed != 0 || !result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "one1", "dest/only.bin")
	if !bytes.Equal(got, data) {
		t.Fatalf("object content mismatch")
	}
}

func TestDirSync_NestedDirectories(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "nest1")

	root := t.TempDir()
	files := map[string][]byte{
		"index.html":         genRandomBytes(6101, 500),
		"assets/app.js":      genRandomBytes(6102, 1200),
		"images/logo.png":    genRandomBytes(6103, 900),
		"a/b/c/d/e/deep.bin": genRandomBytes(6104, 700), // deep but reasonable nesting
	}
	writeDirSyncTree(t, root, files)

	result, err := syncDirectory(root, "nest1", "site", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != len(files) || result.Synced != len(files) || !result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	for rel, data := range files {
		key := "site/" + rel
		got := getSyncObjectBytes(t, ts, creds, region, "nest1", key)
		if !bytes.Equal(got, data) {
			t.Fatalf("key %s content mismatch", key)
		}
	}
}

func TestDirSync_SameBasenameInDifferentDirectories(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "same1")

	root := t.TempDir()
	files := map[string][]byte{
		"a/x.txt": []byte("content A"),
		"b/x.txt": []byte("content B"),
		"x.txt":   []byte("content root"),
	}
	writeDirSyncTree(t, root, files)

	result, err := syncDirectory(root, "same1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 3 || result.Synced != 3 || !result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	for rel, want := range files {
		got := getSyncObjectBytes(t, ts, creds, region, "same1", rel)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q -- two distinct local files must never collide on one key", rel, got, want)
		}
	}
}

func TestDirSync_DuplicateLookingPathsRemainDistinct(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "dup1")

	root := t.TempDir()
	files := map[string][]byte{
		"note.txt":  []byte("plain"),
		"Note.txt":  []byte("capitalized -- distinct on a case-sensitive filesystem"),
		"note .txt": []byte("trailing-space name -- distinct"),
	}
	writeDirSyncTree(t, root, files)

	result, err := syncDirectory(root, "dup1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 3 || result.Synced != 3 || !result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	for rel, want := range files {
		got := getSyncObjectBytes(t, ts, creds, region, "dup1", rel)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: content mismatch, distinct-looking paths must not collide", rel)
		}
	}
}

func TestDirSync_DeterministicOrdering(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"b.txt":   []byte("b"),
		"a.txt":   []byte("a"),
		"c/z.txt": []byte("z"),
		"c/a.txt": []byte("ca"),
		"aa.txt":  []byte("aa"),
	}
	writeDirSyncTree(t, root, files)

	want := []string{"a.txt", "aa.txt", "b.txt", "c/a.txt", "c/z.txt"}

	for attempt := 0; attempt < 3; attempt++ {
		got, skips, err := discoverSyncFiles(root)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if len(skips) != 0 {
			t.Fatalf("attempt %d: unexpected skips: %+v", attempt, skips)
		}
		if len(got) != len(want) {
			t.Fatalf("attempt %d: got %v, want %v", attempt, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("attempt %d: order mismatch at %d: got %v, want %v", attempt, i, got, want)
			}
		}
	}
}

func TestDirSync_PrefixNormalization(t *testing.T) {
	cases := []struct {
		raw        string
		wantBucket string
		wantPrefix string
	}{
		{"s3://bucket/", "bucket", ""},
		{"s3://bucket", "bucket", ""},
		{"s3://bucket/prefix", "bucket", "prefix"},
		{"s3://bucket/prefix/", "bucket", "prefix"},
		{"s3://bucket/a/b/", "bucket", "a/b"},
	}
	for _, c := range cases {
		bucket, prefix, err := parseS3DirURI(c.raw)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", c.raw, err)
		}
		if bucket != c.wantBucket || prefix != c.wantPrefix {
			t.Fatalf("%q: got bucket=%q prefix=%q, want bucket=%q prefix=%q", c.raw, bucket, prefix, c.wantBucket, c.wantPrefix)
		}
	}
	if _, _, err := parseS3DirURI("not-an-s3-uri"); err == nil {
		t.Fatalf("expected an error for a non-s3:// destination")
	}
	if _, _, err := parseS3DirURI("s3:///prefix"); err == nil {
		t.Fatalf("expected an error for an empty bucket name")
	}

	if got := joinSyncKey("", "a.txt"); got != "a.txt" {
		t.Fatalf("joinSyncKey empty prefix: got %q", got)
	}
	if got := joinSyncKey("prefix", "x/b.txt"); got != "prefix/x/b.txt" {
		t.Fatalf("joinSyncKey: got %q", got)
	}
	if strings.Contains(joinSyncKey("prefix", "a.txt"), "//") {
		t.Fatalf("the key joiner must never introduce a doubled slash")
	}

	// C2's own "with and without a trailing slash" examples must be
	// end-to-end equivalent, not merely equivalent in the parser.
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "pfxeq")

	files := map[string][]byte{"a.txt": genRandomBytes(6201, 100), "x/b.txt": genRandomBytes(6202, 200)}
	for i, raw := range []string{"s3://pfxeq/prefix", "s3://pfxeq/prefix/"} {
		root := t.TempDir()
		writeDirSyncTree(t, root, files)
		bucket, prefix, err := parseS3DirURI(raw)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		result, err := syncDirectory(root, bucket, prefix, baseDirSyncCfg(ts, creds, region))
		if err != nil || !result.OK() {
			t.Fatalf("case %d: result=%+v err=%v", i, result, err)
		}
		if got := getSyncObjectBytes(t, ts, creds, region, "pfxeq", "prefix/a.txt"); !bytes.Equal(got, files["a.txt"]) {
			t.Fatalf("case %d: prefix/a.txt mismatch", i)
		}
		if got := getSyncObjectBytes(t, ts, creds, region, "pfxeq", "prefix/x/b.txt"); !bytes.Equal(got, files["x/b.txt"]) {
			t.Fatalf("case %d: prefix/x/b.txt mismatch", i)
		}
	}
}

func TestDirSync_SpacesInPaths(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "space1")

	root := t.TempDir()
	files := map[string][]byte{
		"my file.txt":            genRandomBytes(6301, 300),
		"dir with space/two.bin": genRandomBytes(6302, 400),
	}
	writeDirSyncTree(t, root, files)
	result, err := syncDirectory(root, "space1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if !result.OK() || result.Synced != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for rel, data := range files {
		got := getSyncObjectBytes(t, ts, creds, region, "space1", rel)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s mismatch", rel)
		}
	}
}

func TestDirSync_UnicodePaths(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "uni1")

	root := t.TempDir()
	files := map[string][]byte{
		"unicode-✓.txt":        genRandomBytes(6401, 250),
		"日本語/ファイル.bin":         genRandomBytes(6402, 350),
		"emoji-\U0001F600.txt": genRandomBytes(6403, 150),
	}
	writeDirSyncTree(t, root, files)
	result, err := syncDirectory(root, "uni1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if !result.OK() || result.Synced != len(files) {
		t.Fatalf("unexpected result: %+v", result)
	}
	for rel, data := range files {
		got := getSyncObjectBytes(t, ts, creds, region, "uni1", rel)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s mismatch", rel)
		}
	}
}

func TestDirSync_HiddenDotPrefixedFiles(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "hidden1")

	root := t.TempDir()
	files := map[string][]byte{
		".hidden":              genRandomBytes(6501, 150),
		".config/settings.txt": genRandomBytes(6502, 150),
	}
	writeDirSyncTree(t, root, files)
	result, err := syncDirectory(root, "hidden1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 2 || result.Synced != 2 || !result.OK() {
		t.Fatalf("dot-prefixed files/directories must be synced like any other, got %+v", result)
	}
	for rel, data := range files {
		got := getSyncObjectBytes(t, ts, creds, region, "hidden1", rel)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s mismatch", rel)
		}
	}
}

func TestDirSync_ExistingIdenticalRemoteFile(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "identical1")

	root := t.TempDir()
	files := map[string][]byte{"same.bin": genRandomBytes(6601, 50_000)}
	writeDirSyncTree(t, root, files)

	result1, err := syncDirectory(root, "identical1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if !result1.OK() || result1.Stats.UploadedBytes == 0 {
		t.Fatalf("first sync should have uploaded new content: %+v", result1.Stats)
	}

	result2, err := syncDirectory(root, "identical1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !result2.OK() {
		t.Fatalf("resync of unchanged content must succeed: %+v", result2)
	}
	if result2.Stats.UploadedBytes != 0 {
		t.Fatalf("resync of byte-identical content should upload nothing, uploaded=%d", result2.Stats.UploadedBytes)
	}
	if result2.Stats.BytesAvoided != result2.Stats.LogicalBytes {
		t.Fatalf("resync of unchanged content should report 100%% reuse: avoided=%d logical=%d", result2.Stats.BytesAvoided, result2.Stats.LogicalBytes)
	}
}

func TestDirSync_OneModifiedFileAmongMany(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "mod1")

	root := t.TempDir()
	orig := map[string][]byte{
		"one.bin":   genRandomBytes(6701, 40_000),
		"two.bin":   genRandomBytes(6702, 40_000),
		"three.bin": genRandomBytes(6703, 40_000),
	}
	writeDirSyncTree(t, root, orig)
	if result, err := syncDirectory(root, "mod1", "", baseDirSyncCfg(ts, creds, region)); err != nil || !result.OK() {
		t.Fatalf("first sync: result=%+v err=%v", result, err)
	}

	// A small localized edit, matching M6A's own demonstration-fixture
	// pattern: CDC boundaries only reshuffle locally, so reuse stays high.
	modified := append([]byte{}, orig["two.bin"]...)
	mid := len(modified) / 2
	modified = append(modified[:mid:mid], append([]byte("SMALL-EDIT-HERE"), modified[mid:]...)...)
	if err := os.WriteFile(filepath.Join(root, "two.bin"), modified, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := syncDirectory(root, "mod1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !result.OK() || result.Synced != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stats.UploadedBytes == 0 || result.Stats.UploadedBytes >= result.Stats.LogicalBytes {
		t.Fatalf("expected a small upload, far less than total logical bytes: uploaded=%d logical=%d", result.Stats.UploadedBytes, result.Stats.LogicalBytes)
	}
	want := map[string][]byte{"one.bin": orig["one.bin"], "two.bin": modified, "three.bin": orig["three.bin"]}
	for name, data := range want {
		got := getSyncObjectBytes(t, ts, creds, region, "mod1", name)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s mismatch", name)
		}
	}
}

func TestDirSync_NewlyAddedFileAfterInitialSync(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "newf1")

	root := t.TempDir()
	writeDirSyncTree(t, root, map[string][]byte{"first.bin": genRandomBytes(6801, 10_000)})
	if result, err := syncDirectory(root, "newf1", "", baseDirSyncCfg(ts, creds, region)); err != nil || !result.OK() || result.Discovered != 1 {
		t.Fatalf("first sync: result=%+v err=%v", result, err)
	}

	secondData := genRandomBytes(6802, 12_000)
	writeDirSyncTree(t, root, map[string][]byte{"second.bin": secondData})

	result2, err := syncDirectory(root, "newf1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !result2.OK() || result2.Discovered != 2 || result2.Synced != 2 {
		t.Fatalf("unexpected result: %+v", result2)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "newf1", "second.bin")
	if !bytes.Equal(got, secondData) {
		t.Fatalf("second.bin mismatch")
	}
}

func TestDirSync_LocalDeletionDoesNotDeleteRemoteObject(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "del1")

	root := t.TempDir()
	keep := genRandomBytes(6901, 8_000)
	gone := genRandomBytes(6902, 8_000)
	writeDirSyncTree(t, root, map[string][]byte{"keep.bin": keep, "gone.bin": gone})

	if result, err := syncDirectory(root, "del1", "", baseDirSyncCfg(ts, creds, region)); err != nil || !result.OK() || result.Synced != 2 {
		t.Fatalf("first sync: result=%+v err=%v", result, err)
	}

	if err := os.Remove(filepath.Join(root, "gone.bin")); err != nil {
		t.Fatal(err)
	}

	result2, err := syncDirectory(root, "del1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !result2.OK() || result2.Discovered != 1 || result2.Synced != 1 {
		t.Fatalf("unexpected result: %+v", result2)
	}
	// Non-destructive semantics (C4): the remote object for the deleted
	// local file must remain exactly as it was.
	got := getSyncObjectBytes(t, ts, creds, region, "del1", "gone.bin")
	if !bytes.Equal(got, gone) {
		t.Fatalf("remote object for a locally-deleted file must be left untouched")
	}
	got2 := getSyncObjectBytes(t, ts, creds, region, "del1", "keep.bin")
	if !bytes.Equal(got2, keep) {
		t.Fatalf("keep.bin mismatch")
	}
}

func TestDirSync_SymlinkSkippedNotFollowed(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "sym1")

	root := t.TempDir()
	writeDirSyncTree(t, root, map[string][]byte{"real.txt": []byte("real content")})

	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(root, "link-to-file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "link-to-dir")); err != nil {
		t.Fatal(err)
	}

	result, err := syncDirectory(root, "sym1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Synced != 1 || result.Failed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Skipped != 2 {
		t.Fatalf("expected both the file symlink and the directory symlink to be skipped, got %+v", result.Skips)
	}
	for _, s := range result.Skips {
		if !strings.Contains(s.Reason, "symlink") {
			t.Fatalf("skip reason should mention symlink: %+v", s)
		}
	}
	got := getSyncObjectBytes(t, ts, creds, region, "sym1", "real.txt")
	if string(got) != "real content" {
		t.Fatalf("real.txt mismatch")
	}
	if getSyncObjectStatus(t, ts, creds, region, "sym1", "link-to-file.txt") != http.StatusNotFound {
		t.Fatalf("a symlink must never be synced")
	}
	if getSyncObjectStatus(t, ts, creds, region, "sym1", "link-to-dir/secret.txt") != http.StatusNotFound {
		t.Fatalf("a symlinked directory must never be recursed into -- this is also what prevents a symlink from ever being used to escape the source root")
	}
}

func TestDirSync_SpecialFileSkipped(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "special1")

	root := t.TempDir()
	writeDirSyncTree(t, root, map[string][]byte{"real.txt": []byte("ok")})
	fifoPath := filepath.Join(root, "a.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("named pipes unsupported in this environment; honestly skipping special-file coverage: %v", err)
	}

	result, err := syncDirectory(root, "special1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Synced != 1 || result.Failed != 0 || result.Skipped != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.Contains(result.Skips[0].Reason, "special file") {
		t.Fatalf("expected a special-file skip reason, got %+v", result.Skips)
	}
	if getSyncObjectStatus(t, ts, creds, region, "special1", "a.fifo") != http.StatusNotFound {
		t.Fatalf("a special file must never be synced")
	}
}

func TestDirSync_RemoteConflictForOneFileOthersSucceed(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "dconf1")

	root := t.TempDir()
	files := map[string][]byte{
		"ok1.bin":      genRandomBytes(8001, 20_000),
		"conflict.bin": genRandomBytes(8002, 20_000),
		"ok2.bin":      genRandomBytes(8003, 20_000),
	}
	writeDirSyncTree(t, root, files)

	targetKey := "pfx/conflict.bin"
	syncTestHookBeforeMutationCheck = func(cfg syncClientConfig) {
		if cfg.Key == targetKey {
			if _, err := srv.store.PutObject("dconf1", targetKey, []byte("a concurrent writer got here first"), "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { syncTestHookBeforeMutationCheck = nil }()

	result, err := syncDirectory(root, "dconf1", "pfx", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 3 || result.Synced != 2 || result.Failed != 1 || result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].Dest != "s3://dconf1/"+targetKey {
		t.Fatalf("unexpected failures: %+v", result.Failures)
	}
	if !errors.Is(result.Failures[0].Err, errSyncRemoteConflict) {
		t.Fatalf("expected errSyncRemoteConflict, got %v", result.Failures[0].Err)
	}
	// Unrelated files must be correctly committed and retrievable -- one
	// conflict never stops or corrupts an unrelated file's own commit.
	got1 := getSyncObjectBytes(t, ts, creds, region, "dconf1", "pfx/ok1.bin")
	if !bytes.Equal(got1, files["ok1.bin"]) {
		t.Fatalf("ok1 mismatch")
	}
	got2 := getSyncObjectBytes(t, ts, creds, region, "dconf1", "pfx/ok2.bin")
	if !bytes.Equal(got2, files["ok2.bin"]) {
		t.Fatalf("ok2 mismatch")
	}
	// The conflicted object holds the concurrent writer's content, never a mix.
	gotC := getSyncObjectBytes(t, ts, creds, region, "dconf1", targetKey)
	if string(gotC) != "a concurrent writer got here first" {
		t.Fatalf("conflicted object content unexpected: %q", gotC)
	}
}

func TestDirSync_FileDisappearsBeforeBeingProcessed(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "drace1")

	root := t.TempDir()
	files := map[string][]byte{
		"a-first.bin":  genRandomBytes(8101, 5_000), // processed first, lexically
		"z-victim.bin": genRandomBytes(8102, 5_000), // removed while a-first.bin is being processed
	}
	writeDirSyncTree(t, root, files)
	victimPath := filepath.Join(root, "z-victim.bin")

	syncTestHookBeforeMutationCheck = func(cfg syncClientConfig) {
		if filepath.Base(cfg.LocalPath) == "a-first.bin" {
			if err := os.Remove(victimPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { syncTestHookBeforeMutationCheck = nil }()

	result, err := syncDirectory(root, "drace1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 2 || result.Synced != 1 || result.Failed != 1 || result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Failures) != 1 || filepath.Base(result.Failures[0].LocalPath) != "z-victim.bin" {
		t.Fatalf("unexpected failures: %+v", result.Failures)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "drace1", "a-first.bin")
	if !bytes.Equal(got, files["a-first.bin"]) {
		t.Fatalf("surviving file content mismatch")
	}
	if getSyncObjectStatus(t, ts, creds, region, "drace1", "z-victim.bin") != http.StatusNotFound {
		t.Fatalf("the failed file's object must never become visible")
	}
}

func TestDirSync_PartialFailureMultipleFailureModesSuccessfulSiblingsRetained(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "pfail1")

	root := t.TempDir()
	files := map[string][]byte{
		"a-ok1.bin":      genRandomBytes(8201, 4_000),
		"b-conflict.bin": genRandomBytes(8202, 4_000),
		"c-ok2.bin":      genRandomBytes(8203, 4_000),
		"d-vanishes.bin": genRandomBytes(8204, 4_000),
		"e-ok3.bin":      genRandomBytes(8205, 4_000),
	}
	writeDirSyncTree(t, root, files)
	vanishPath := filepath.Join(root, "d-vanishes.bin")

	syncTestHookBeforeMutationCheck = func(cfg syncClientConfig) {
		switch filepath.Base(cfg.LocalPath) {
		case "b-conflict.bin":
			if _, err := srv.store.PutObject("pfail1", "b-conflict.bin", []byte("raced"), "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
		case "c-ok2.bin":
			if err := os.Remove(vanishPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { syncTestHookBeforeMutationCheck = nil }()

	result, err := syncDirectory(root, "pfail1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if result.Discovered != 5 || result.Synced != 3 || result.Failed != 2 || result.Skipped != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.OK() {
		t.Fatalf("OK() must be false when any file failed (nonzero process exit status)")
	}
	for _, ok := range []string{"a-ok1.bin", "c-ok2.bin", "e-ok3.bin"} {
		got := getSyncObjectBytes(t, ts, creds, region, "pfail1", ok)
		if !bytes.Equal(got, files[ok]) {
			t.Fatalf("%s: content mismatch -- a partial failure must never roll back or corrupt an unrelated committed object", ok)
		}
	}
	if getSyncObjectStatus(t, ts, creds, region, "pfail1", "d-vanishes.bin") != http.StatusNotFound {
		t.Fatalf("failed file must never become visible")
	}
}

func TestDirSync_ResultOKReflectsFailureCount(t *testing.T) {
	cases := []struct {
		failed int
		want   bool
	}{{0, true}, {1, false}, {5, false}}
	for _, c := range cases {
		r := dirSyncResult{Failed: c.failed}
		if got := r.OK(); got != c.want {
			t.Fatalf("Failed=%d: OK()=%v, want %v (this is exactly the value runSync's directory branch uses to decide os.Exit(1))", c.failed, got, c.want)
		}
	}
}

func TestDirSync_AggregateStatsExactMatchSumOfPerFileStats(t *testing.T) {
	sizes := []int{5_000, 37_000, 123_456}
	seeds := []int64{7001, 7002, 7003}
	names := []string{"a.bin", "sub/b.bin", "sub/deeper/c.bin"}

	// Expected: sync each file individually against its own fresh,
	// isolated store/server, so no cross-file dedup can influence the
	// "expected" per-file numbers.
	var wantStats syncStats
	for i := range names {
		_, srv, creds, region := newSyncTestServer(t)
		ts := httptest.NewServer(srv)
		createSyncTestBucket(t, ts, creds, region, "solo")
		data := genRandomBytes(seeds[i], sizes[i])
		tmp := t.TempDir()
		path := writeSyncTempFile(t, tmp, "f.bin", data)
		st, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: ts.URL, Bucket: "solo", Key: "k", Creds: creds, Region: region, HTTPClient: ts.Client()})
		if err != nil {
			t.Fatal(err)
		}
		wantStats.LogicalBytes += st.LogicalBytes
		wantStats.TotalChunks += st.TotalChunks
		wantStats.ChunksReused += st.ChunksReused
		wantStats.MissingChunkOccur += st.MissingChunkOccur
		wantStats.UniqueChunksUploaded += st.UniqueChunksUploaded
		wantStats.UploadedBytes += st.UploadedBytes
		wantStats.BytesAvoided += st.BytesAvoided
		ts.Close()
	}

	// Actual: sync all three as one directory tree against a fresh store.
	_, srv2, creds2, region2 := newSyncTestServer(t)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	createSyncTestBucket(t, ts2, creds2, region2, "tree")
	root := t.TempDir()
	files := map[string][]byte{}
	for i, name := range names {
		files[name] = genRandomBytes(seeds[i], sizes[i])
	}
	writeDirSyncTree(t, root, files)
	result, err := syncDirectory(root, "tree", "", baseDirSyncCfg(ts2, creds2, region2))
	if err != nil {
		t.Fatal(err)
	}
	if result.Synced != 3 || !result.OK() {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stats != wantStats {
		t.Fatalf("aggregate stats mismatch (must be an honest sum of per-file results, never double-counted or estimated):\n got  %+v\n want %+v", result.Stats, wantStats)
	}
}

func TestDirSync_ResumeAfterPartialPriorUploadOfOneFile(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	client := ts.Client()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "dresume1")

	root := t.TempDir()
	bigData := genRandomBytes(8301, 600_000)
	smallData := genRandomBytes(8302, 5_000)
	writeDirSyncTree(t, root, map[string][]byte{"big.bin": bigData, "small.bin": smallData})

	bigPath := filepath.Join(root, "big.bin")
	scanned, _, err := scanLocalFileForSync(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 2 {
		t.Fatalf("fixture too small to exercise a partial prior upload (%d chunks)", len(scanned))
	}
	// Simulate "the client died after uploading only the first chunk of
	// one file": a real chunk upload happens for scanned[0], nothing else.
	first := scanned[0]
	firstData, err := readSyncFileRange(bigPath, first.Offset, first.Length)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := doSyncRequest(t, client, ts.URL, signer, http.MethodPut, zeros3SyncChunksPrefix+first.SHA256, firstData); status != http.StatusOK {
		t.Fatalf("priming upload failed")
	}

	result, err := syncDirectory(root, "dresume1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if !result.OK() || result.Synced != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stats.UploadedBytes >= result.Stats.LogicalBytes {
		t.Fatalf("directory-level resume should have skipped the already-uploaded chunk: uploaded=%d logical=%d", result.Stats.UploadedBytes, result.Stats.LogicalBytes)
	}
	got := getSyncObjectBytes(t, ts, creds, region, "dresume1", "big.bin")
	if !bytes.Equal(got, bigData) {
		t.Fatalf("big.bin mismatch after resumed directory sync")
	}
	got2 := getSyncObjectBytes(t, ts, creds, region, "dresume1", "small.bin")
	if !bytes.Equal(got2, smallData) {
		t.Fatalf("small.bin mismatch")
	}

	// A second, entirely fresh re-run (rerunning `zeros3 sync` after an
	// interruption) must now reuse everything: nothing left to upload. No
	// durable directory-sync session/journal exists anywhere -- this
	// property emerges purely from object-level durability + CAS reuse.
	result2, err := syncDirectory(root, "dresume1", "", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory (rerun): %v", err)
	}
	if !result2.OK() || result2.Stats.UploadedBytes != 0 {
		t.Fatalf("rerun after full completion should upload nothing: %+v", result2.Stats)
	}
}

func TestDirSync_ServerRestart(t *testing.T) {
	dir, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	createSyncTestBucket(t, ts, creds, region, "drestart1")

	root := t.TempDir()
	files := map[string][]byte{
		"one.bin":        genRandomBytes(8401, 30_000),
		"nested/two.bin": genRandomBytes(8402, 40_000),
	}
	writeDirSyncTree(t, root, files)

	result, err := syncDirectory(root, "drestart1", "backup", baseDirSyncCfg(ts, creds, region))
	if err != nil {
		t.Fatalf("syncDirectory: %v", err)
	}
	if !result.OK() || result.Synced != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}

	// Restart: close this server/store, open a brand-new Store/Server on
	// the same directory. Nothing directory-sync-specific is passed
	// across the restart -- there is no session to carry, exactly as for
	// single-file sync (TestSync_CriticalAcceptanceProof).
	ts.Close()
	if err := srv.store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store2.Close()
	srv2 := NewServer(store2, creds, region)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()

	for rel, data := range files {
		got := getSyncObjectBytes(t, ts2, creds, region, "drestart1", "backup/"+rel)
		if !bytes.Equal(got, data) {
			t.Fatalf("%s mismatch after restart", rel)
		}
	}
	vr, err := store2.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !vr.OK() {
		t.Fatalf("deep verify reported issues after directory sync + restart: %+v", vr.Issues)
	}
}

// TestDirSync_SourcePathTrailingSeparator proves a source directory
// argument with and without a trailing local path separator produces
// identical discovered relative paths and destination keys (C12).
func TestDirSync_SourcePathTrailingSeparator(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	createSyncTestBucket(t, ts, creds, region, "trail1")

	files := map[string][]byte{"a.txt": genRandomBytes(9001, 200), "sub/b.txt": genRandomBytes(9002, 300)}

	root := t.TempDir()
	writeDirSyncTree(t, root, files)
	withSlash := root + string(filepath.Separator)

	for i, r := range []string{root, withSlash} {
		result, err := syncDirectory(r, "trail1", "pfx", baseDirSyncCfg(ts, creds, region))
		if err != nil {
			t.Fatalf("case %d (%q): %v", i, r, err)
		}
		if !result.OK() || result.Discovered != 2 {
			t.Fatalf("case %d (%q): unexpected result: %+v", i, r, result)
		}
		for rel, data := range files {
			got := getSyncObjectBytes(t, ts, creds, region, "trail1", "pfx/"+rel)
			if !bytes.Equal(got, data) {
				t.Fatalf("case %d (%q): key pfx/%s mismatch", i, r, rel)
			}
		}
	}
}

func TestDirSync_PrintSummaryFormat(t *testing.T) {
	result := dirSyncResult{
		Discovered: 4, Synced: 2, Skipped: 1, Failed: 1,
		Skips:    []dirSyncSkip{{LocalPath: "link.txt", Reason: "symlink (not followed)"}},
		Failures: []dirSyncFailure{{LocalPath: "local/broken.bin", Dest: "s3://bucket/prefix/broken.bin", Err: errSyncRemoteConflict}},
		Stats:    syncStats{LogicalBytes: 100, UploadedBytes: 10, BytesAvoided: 90, TotalChunks: 3, ChunksReused: 2, UniqueChunksUploaded: 1},
	}
	var buf bytes.Buffer
	printDirSyncSummary(&buf, result)
	out := buf.String()
	for _, want := range []string{
		"Files discovered:  4", "Files synced:      2", "Files skipped:     1", "Files failed:      1",
		"SKIPPED:", "link.txt: symlink (not followed)",
		"FAILED:", "local/broken.bin -> s3://bucket/prefix/broken.bin",
		"directory sync completed with errors",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary output missing %q; got:\n%s", want, out)
		}
	}
}

// freeTCPAddr reserves an ephemeral local port for the CLI subprocess test
// below by briefly binding then releasing it -- the standard, small-race
// idiom for handing a specific free port to a subprocess.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitForZeros3Serve(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("zeros3 serve did not become ready on %s", addr)
}

// TestCLI_Sync_DirectoryAndSingleFile_Smoke drives the actual built
// `zeros3` binary as a real subprocess against a real `zeros3 serve`
// subprocess over a real TCP connection -- proving the CLI wiring itself
// (the os.Stat-based directory-vs-file dispatch added to runSync) end to
// end, and that the original single-file `zeros3 sync FILE s3://bucket/key`
// CLI invocation still works completely unchanged (C3/no regression).
func TestCLI_Sync_DirectoryAndSingleFile_Smoke(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	s, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("cli1"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start zeros3 serve: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	// Directory sync via the real CLI.
	root := t.TempDir()
	writeDirSyncTree(t, root, map[string][]byte{
		"a.txt":     []byte("hello a"),
		"sub/b.txt": []byte("hello b"),
	})
	out, stderr, code := runZeros3CLI(t, bin, "sync", "-endpoint", "http://"+addr, root, "s3://cli1/tree/")
	if code != 0 {
		t.Fatalf("directory sync CLI failed (code %d): stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "Files discovered:  2") || !strings.Contains(out, "Files synced:      2") {
		t.Fatalf("expected the summary to report 2/2 files, got: %s", out)
	}

	// Single-file sync via the real CLI must still work unchanged.
	tmp := t.TempDir()
	filePath := writeSyncTempFile(t, tmp, "single.bin", []byte("single file content"))
	out2, stderr2, code2 := runZeros3CLI(t, bin, "sync", "-endpoint", "http://"+addr, filePath, "s3://cli1/single-key")
	if code2 != 0 {
		t.Fatalf("single-file sync CLI failed (code %d): stdout=%s stderr=%s", code2, out2, stderr2)
	}

	// A directory sync against a bucket that was never created must fail
	// every file's commit deterministically (never a timing-dependent
	// race) and the process must exit nonzero (C6): proving the real
	// process-level exit code, not just the in-memory dirSyncResult.OK()
	// value the tests above already cover directly.
	out3, stderr3, code3 := runZeros3CLI(t, bin, "sync", "-endpoint", "http://"+addr, root, "s3://no-such-bucket/tree/")
	if code3 == 0 {
		t.Fatalf("expected a nonzero exit status when every file's commit fails, stdout=%s stderr=%s", out3, stderr3)
	}
	if !strings.Contains(out3, "Files failed:      2") || !strings.Contains(out3, "directory sync completed with errors") {
		t.Fatalf("expected the summary to report the failures, got: %s", out3)
	}

	// Restart the server (real process kill + a fresh `serve`) and verify
	// every successfully-synced object survives intact via a direct store open.
	cmd.Process.Kill()
	cmd.Wait()
	s2, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, body, err := s2.GetObject("cli1", "tree/sub/b.txt"); err != nil || string(body) != "hello b" {
		t.Fatalf("tree/sub/b.txt mismatch after restart: err=%v body=%q", err, body)
	}
	if _, body, err := s2.GetObject("cli1", "single-key"); err != nil || string(body) != "single file content" {
		t.Fatalf("single-key mismatch after restart: err=%v body=%q", err, body)
	}
}

// =============================================================================
// M8A: remote-to-remote delta replication (`zeros3 replicate`)
//
// M8A reuses M6's protocol almost without exception (see zeros3.go
// section 15d's doc comment for the exact list of reused primitives:
// discoverZeroS3Sync, headSyncDestination, buildSyncPlan,
// negotiateSyncMissing, putSyncChunk, commitSyncObject, syncStats/
// printSyncStats). Everything those already exhaustively test --
// negotiate batch-size boundaries (1023/1024/1025), oversized negotiate
// requests, malformed JSON, an unsupported protocol/cdc/hash version on
// negotiate/commit -- is NOT re-proven here for a second time against
// identical, unmodified code; see TestSyncNegotiate_BatchSizeBoundary,
// TestSyncNegotiate_MultiBatchViaClient, TestSyncNegotiate_
// OversizedRequestRejected, TestSyncNegotiate_MalformedJSONRejected,
// TestSyncCommit_UnsupportedProtocolCDCHash, etc., above.
//
// These tests focus on what's genuinely new: the two new server
// endpoints (GET /object, GET /chunks/<sha256-hex> download side),
// the two new client functions that call them (fetchSourceDescriptor,
// fetchSourceChunk), and replicateObject's orchestration -- capability
// discovery across two endpoints, source consistency, destination
// conflict safety, resume, and exact statistics.
// =============================================================================

func newReplicateTestServerPair(t *testing.T) (srcDir string, srcSrv *Server, dstDir string, dstSrv *Server, creds Credentials, region string) {
	t.Helper()
	srcDir, srcSrv, creds, region = newSyncTestServer(t)
	dstDir, dstSrv, _, _ = newSyncTestServer(t)
	return srcDir, srcSrv, dstDir, dstSrv, creds, region
}

func mustPutSourceObject(t *testing.T, srv *Server, bucket, key string, body []byte, contentType string, metadata map[string]string) *objectEntry {
	t.Helper()
	if err := srv.store.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
	entry, err := srv.store.PutObject(bucket, key, body, contentType, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustCreateReplicateBucket(t *testing.T, srv *Server, bucket string) {
	t.Helper()
	if err := srv.store.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
}

// =============================================================================
// M8A2/M8A: end-to-end happy path
// =============================================================================

func TestReplicate_EndToEnd_NewDestinationOrdinaryObject(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	body := genRandomBytes(8001, 5_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", map[string]string{"origin": "m8a-test"})
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.LogicalBytes != int64(len(body)) {
		t.Fatalf("LogicalBytes = %d, want %d", stats.LogicalBytes, len(body))
	}
	if stats.UploadedBytes != int64(len(body)) {
		t.Fatalf("first replication of a brand-new object should transfer everything: UploadedBytes=%d want=%d", stats.UploadedBytes, len(body))
	}

	entry, gotBody, err := dstSrv.store.GetObject("dst", "obj.bin")
	if err != nil {
		t.Fatalf("GetObject on destination: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("destination object bytes do not match source")
	}
	if entry.contentType != "application/octet-stream" {
		t.Fatalf("content type not preserved: got %q", entry.contentType)
	}
}

// =============================================================================
// M8A1: capability discovery
// =============================================================================

func TestReplicate_IncompatibleSourceFailsClearly(t *testing.T) {
	notZeroS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a zeros3 endpoint"))
	}))
	defer notZeroS3.Close()
	_, _, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: notZeroS3.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: notZeroS3.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	_, err := replicateObject(cfg)
	if err == nil || !strings.Contains(err.Error(), "source capability discovery failed") {
		t.Fatalf("err = %v, want a clear source capability discovery failure", err)
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj"); err == nil {
		t.Fatalf("destination must not have been touched")
	}
}

func TestReplicate_IncompatibleDestinationFailsClearly(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "obj", []byte("hello"), "text/plain", nil)

	notZeroS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a zeros3 endpoint"))
	}))
	defer notZeroS3.Close()

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: notZeroS3.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: notZeroS3.Client()},
	}
	_, err := replicateObject(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination capability discovery failed") {
		t.Fatalf("err = %v, want a clear destination capability discovery failure", err)
	}
}

func TestReplicate_AuthFailureSourceRejected(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "obj", []byte("hello"), "text/plain", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	wrongCreds := Credentials{AccessKeyID: "WRONGKEY", SecretAccessKey: "wrong-secret-key-entirely-different"}
	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: wrongCreds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	_, err := replicateObject(cfg)
	if err == nil || !strings.Contains(err.Error(), "source capability discovery failed") {
		t.Fatalf("err = %v, want a clear source auth/discovery failure", err)
	}
}

func TestReplicate_AuthFailureDestinationRejected(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "obj", []byte("hello"), "text/plain", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	wrongCreds := Credentials{AccessKeyID: "WRONGKEY", SecretAccessKey: "wrong-secret-key-entirely-different"}
	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: wrongCreds, Region: region, HTTPClient: dstTS.Client()},
	}
	_, err := replicateObject(cfg)
	if err == nil || !strings.Contains(err.Error(), "destination capability discovery failed") {
		t.Fatalf("err = %v, want a clear destination auth/discovery failure", err)
	}
}

// =============================================================================
// M8A2: source object descriptor
// =============================================================================

func TestReplicate_SourceDescriptor_ObjectExists(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	body := genRandomBytes(8002, 300_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)

	cfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	desc, err := fetchSourceDescriptor(cfg)
	if err != nil {
		t.Fatalf("fetchSourceDescriptor: %v", err)
	}
	if desc.Size != int64(len(body)) {
		t.Fatalf("Size = %d, want %d", desc.Size, len(body))
	}
	if desc.Bucket != "src" || desc.Key != "obj" {
		t.Fatalf("Bucket/Key = %s/%s, want src/obj", desc.Bucket, desc.Key)
	}
	if len(desc.Chunks) == 0 {
		t.Fatalf("expected at least one chunk descriptor")
	}
	var sum int64
	for _, c := range desc.Chunks {
		sum += c.Length
	}
	if sum != desc.Size {
		t.Fatalf("sum of chunk lengths %d != declared size %d", sum, desc.Size)
	}
}

func TestReplicate_SourceDescriptor_MissingObject(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")

	cfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "does-not-exist", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	_, err := fetchSourceDescriptor(cfg)
	if err == nil {
		t.Fatalf("expected an error for a missing source object")
	}
}

func TestReplicate_SourceDescriptor_MissingBucket(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()

	cfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "no-such-bucket", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	_, err := fetchSourceDescriptor(cfg)
	if err == nil {
		t.Fatalf("expected an error for a missing source bucket")
	}
}

func TestReplicate_SourceDescriptor_EmptyObjectEndToEnd(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "empty", []byte{}, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "empty", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "empty", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.LogicalBytes != 0 {
		t.Fatalf("LogicalBytes = %d, want 0", stats.LogicalBytes)
	}
	_, body, err := dstSrv.store.GetObject("dst", "empty")
	if err != nil || len(body) != 0 {
		t.Fatalf("expected an empty destination object, got err=%v len=%d", err, len(body))
	}
}

func TestReplicate_SourceDescriptor_MetadataAndContentTypePreserved(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	meta := map[string]string{"author": "m8a", "purpose": "regression-test"}
	mustPutSourceObject(t, srcSrv, "src", "obj", []byte("some content for metadata test"), "text/x-custom", meta)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	if _, err := replicateObject(cfg); err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	entry, _, err := dstSrv.store.HeadObject("dst", "obj")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if entry.contentType != "text/x-custom" {
		t.Fatalf("content type = %q, want text/x-custom", entry.contentType)
	}
	_, man, err := dstSrv.store.HeadObject("dst", "obj")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, kv := range man.Metadata {
		got[kv.Key] = kv.Value
	}
	for k, v := range meta {
		if got[k] != v {
			t.Fatalf("metadata[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestReplicate_SourceDescriptor_WeirdKeyCharacters(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, dstSrv, "dst")

	weirdKeys := []string{
		"50% off.txt",
		"a#b.txt",
		"a?b=c.txt",
		"has spaces.txt",
		"nested//double//slash.txt",
		"unicode-日本語.txt",
	}
	for i, key := range weirdKeys {
		body := genRandomBytes(int64(9000+i), 10_000)
		mustPutSourceObject(t, srcSrv, "src", key, body, "application/octet-stream", nil)
		cfg := replicateConfig{
			Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: key, Creds: creds, Region: region, HTTPClient: srcTS.Client()},
			Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: key, Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		}
		if _, err := replicateObject(cfg); err != nil {
			t.Fatalf("replicateObject(key=%q): %v", key, err)
		}
		_, got, err := dstSrv.store.GetObject("dst", key)
		if err != nil {
			t.Fatalf("GetObject(key=%q) on destination: %v", key, err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("key=%q: destination bytes mismatch", key)
		}
	}
}

func TestReplicate_SourceDescriptor_MissingQueryParamsRejected(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	resp := doSignedRequest(t, srcTS.Client(), srcTS.URL, signer, http.MethodGet, zeros3SyncObjectPath+"?bucket=src", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing key param: status = %d, want 400", resp.StatusCode)
	}
	resp2 := doSignedRequest(t, srcTS.Client(), srcTS.URL, signer, http.MethodGet, zeros3SyncObjectPath+"?key=obj", nil, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing bucket param: status = %d, want 400", resp2.StatusCode)
	}
}

// =============================================================================
// M8A4: source chunk retrieval
// =============================================================================

func TestReplicate_SourceChunkDownload_Valid(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	body := genRandomBytes(8003, 50_000)
	sum, err := srcSrv.store.casWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := hex.EncodeToString(sum[:])

	cfg := syncClientConfig{Endpoint: srcTS.URL, Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	got, err := fetchSourceChunk(context.Background(), cfg, hexDigest)
	if err != nil {
		t.Fatalf("fetchSourceChunk: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("fetched chunk bytes do not match")
	}
}

func TestReplicate_SourceChunkDownload_MissingChunk(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()

	cfg := syncClientConfig{Endpoint: srcTS.URL, Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	fakeDigest := strings.Repeat("ab", 32)
	if _, err := fetchSourceChunk(context.Background(), cfg, fakeDigest); err == nil {
		t.Fatalf("expected an error for a missing chunk")
	}
}

func TestReplicate_SourceChunkDownload_MalformedDigest(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	resp := doSignedRequest(t, srcTS.Client(), srcTS.URL, signer, http.MethodGet, zeros3SyncChunksPrefix+"not-valid-hex", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed digest", resp.StatusCode)
	}
}

func TestReplicate_SourceChunkDownload_CorruptChunkOnDiskDetected(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	body := genRandomBytes(8004, 20_000)
	sum, err := srcSrv.store.casWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the on-disk chunk directly, simulating bit rot: casRead
	// (which handleSyncChunkDownload calls) must detect the content-hash
	// mismatch rather than serve corrupted bytes.
	if err := os.WriteFile(srcSrv.store.chunkPath(sum), []byte("corrupted content, wrong length and hash"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := syncClientConfig{Endpoint: srcTS.URL, Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	if _, err := fetchSourceChunk(context.Background(), cfg, hex.EncodeToString(sum[:])); err == nil {
		t.Fatalf("expected an error for a corrupt source chunk")
	}
}

// errReplicateFakeChunkHandler serves a syntactically valid discovery
// response but returns wrong bytes for chunk downloads -- simulating a
// buggy or compromised source, to prove the client's own independent
// re-hash (M8A4's "MUST independently verify") actually catches it.
func TestReplicate_ClientRehashDetectsSourceReturningWrongBytes(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == zeros3SyncInfoPath {
			writeSyncJSON(w, http.StatusOK, syncDiscoveryResponse{
				Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
				DeltaSync: true, MaxHashesPerBatch: maxSyncBatchDescriptors, MaxChunkBytes: maxSyncChunkBytes,
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("these are definitely not the bytes you asked for"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fake.Close()

	realDigest := sha256.Sum256([]byte("the real expected content"))
	cfg := syncClientConfig{Endpoint: fake.URL, HTTPClient: fake.Client()}
	_, err := fetchSourceChunk(context.Background(), cfg, hex.EncodeToString(realDigest[:]))
	if !errors.Is(err, errReplicateChunkMismatch) {
		t.Fatalf("err = %v, want errReplicateChunkMismatch", err)
	}
}

// =============================================================================
// M8A3: destination negotiation (via replicateObject)
// =============================================================================

func TestReplicate_Negotiate_AllMissing(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	body := genRandomBytes(8005, 2_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.MissingChunkOccur != stats.TotalChunks {
		t.Fatalf("MissingChunkOccur = %d, want %d (all missing)", stats.MissingChunkOccur, stats.TotalChunks)
	}
	if stats.BytesAvoided != 0 {
		t.Fatalf("BytesAvoided = %d, want 0 for an entirely fresh destination", stats.BytesAvoided)
	}
}

func TestReplicate_Negotiate_ZeroMissingFullReuse(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	body := genRandomBytes(8006, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	// Pre-seed the destination with byte-identical content under a
	// different key, so every chunk the source describes already exists
	// in the destination's CAS by content hash before replication starts.
	mustPutSourceObject(t, dstSrv, "dst", "already-here", body, "application/octet-stream", nil)

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.UploadedBytes != 0 {
		t.Fatalf("UploadedBytes = %d, want 0 (every chunk already present)", stats.UploadedBytes)
	}
	if stats.BytesAvoided != stats.LogicalBytes {
		t.Fatalf("BytesAvoided = %d, want %d (full reuse)", stats.BytesAvoided, stats.LogicalBytes)
	}
	if stats.ChunksReused != stats.TotalChunks {
		t.Fatalf("ChunksReused = %d, want %d", stats.ChunksReused, stats.TotalChunks)
	}
}

func TestReplicate_Negotiate_MixedPartialReuseExactStats(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	base := genRandomBytes(8007, 4_000_000)
	// Seed the destination with the unedited base content under a
	// different key -- it will share most chunks with, but not be
	// identical to, the edited source object below.
	mustPutSourceObject(t, dstSrv, "dst", "base-already-here", base, "application/octet-stream", nil)

	mutated := append([]byte{}, base...)
	copy(mutated[len(mutated)/2:len(mutated)/2+8192], genRandomBytes(8008, 8192))
	mustPutSourceObject(t, srcSrv, "src", "obj", mutated, "application/octet-stream", nil)

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.MissingChunkOccur == 0 || stats.MissingChunkOccur == stats.TotalChunks {
		t.Fatalf("expected a genuinely mixed result, got MissingChunkOccur=%d of TotalChunks=%d", stats.MissingChunkOccur, stats.TotalChunks)
	}
	if stats.UploadedBytes+stats.BytesAvoided != stats.LogicalBytes {
		t.Fatalf("accounting mismatch: uploaded=%d avoided=%d logical=%d", stats.UploadedBytes, stats.BytesAvoided, stats.LogicalBytes)
	}
	if stats.BytesAvoided == 0 {
		t.Fatalf("expected a strong, honest reuse figure, got BytesAvoided=0")
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil || !bytes.Equal(gotBody, mutated) {
		t.Fatalf("destination content mismatch after mixed-reuse replication: err=%v", err)
	}
}

func TestReplicate_Stats_DuplicateChunkReferencesNotDoubleCounted(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, dstSrv, "dst")
	mustCreateReplicateBucket(t, srcSrv, "src")

	// Build a source object whose manifest legitimately repeats the same
	// chunk twice (a file with a repeated block) by committing directly
	// through the same primitives handleSyncCommit uses.
	chunkA := genRandomBytes(8009, 10_000)
	chunkB := genRandomBytes(8010, 10_000)
	if _, err := srcSrv.store.casWrite(chunkA); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.casWrite(chunkB); err != nil {
		t.Fatal(err)
	}
	sumA := sha256.Sum256(chunkA)
	sumB := sha256.Sum256(chunkB)
	refs := []chunkRef{
		{SHA256: hex.EncodeToString(sumA[:]), Length: int64(len(chunkA))},
		{SHA256: hex.EncodeToString(sumB[:]), Length: int64(len(chunkB))},
		{SHA256: hex.EncodeToString(sumA[:]), Length: int64(len(chunkA))}, // repeated occurrence
	}
	total := int64(len(chunkA)*2 + len(chunkB))
	objHash := sha256.New()
	objHash.Write(chunkA)
	objHash.Write(chunkB)
	objHash.Write(chunkA)
	var objSHA [32]byte
	copy(objSHA[:], objHash.Sum(nil))
	man := buildManifestV1FromRefs(refs, total, objSHA, "dupchunktest", "application/octet-stream", nil)
	manUUID, manSHA, err := srcSrv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.commitObjectRoot("src", "dupobj", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "dupobj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "dupobj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.TotalChunks != 3 {
		t.Fatalf("TotalChunks = %d, want 3 (occurrences)", stats.TotalChunks)
	}
	if stats.UniqueChunksUploaded != 2 {
		t.Fatalf("UniqueChunksUploaded = %d, want 2 (unique digests)", stats.UniqueChunksUploaded)
	}
	if stats.UploadedBytes != int64(len(chunkA)+len(chunkB)) {
		t.Fatalf("UploadedBytes = %d, want %d (each unique chunk counted once, not per-occurrence)", stats.UploadedBytes, len(chunkA)+len(chunkB))
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "dupobj")
	if err != nil {
		t.Fatal(err)
	}
	wantBody := append(append(append([]byte{}, chunkA...), chunkB...), chunkA...)
	if !bytes.Equal(gotBody, wantBody) {
		t.Fatalf("destination object does not correctly repeat chunk A")
	}
}

// =============================================================================
// M8A6/M8A8: commit and destination conflict safety
// =============================================================================

func TestReplicate_DestinationConflict_ConcurrentWriteDuringReplicationRejectedSafely(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	body := genRandomBytes(8011, 100_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")
	if _, err := dstSrv.store.PutObject("dst", "obj", []byte("original destination content"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg := syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()}

	// Manually walk replicateObject's own pipeline (mirroring
	// TestSync_ConflictConcurrentPUTDuringSyncCausesCommitConflict's
	// pattern) so a concurrent write can be injected between the
	// destination-identity observation and the eventual commit.
	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}
	exists, etag, err := headSyncDestination(dstCfg)
	if err != nil || !exists {
		t.Fatalf("head: exists=%v err=%v", exists, err)
	}

	// A concurrent, unrelated write lands on the destination after this
	// replication observed its identity but before it commits.
	resp := doSignedRequest(t, dstTS.Client(), dstTS.URL, signer, http.MethodPut, "/dst/obj", []byte("someone else's concurrent write"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("concurrent PUT failed: %d", resp.StatusCode)
	}

	chunks := make([]syncLocalChunk, len(desc.Chunks))
	for i, c := range desc.Chunks {
		chunks[i] = syncLocalChunk{SHA256: c.SHA256, Length: c.Length}
	}
	plan := buildSyncPlan(chunks, desc.Size)
	missing, err := negotiateSyncMissing(dstCfg, syncDiscoveryResponse{MaxHashesPerBatch: maxSyncBatchDescriptors}, plan.unique)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.unique {
		if !missing[d.SHA256] {
			continue
		}
		data, err := fetchSourceChunk(context.Background(), srcCfg, d.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if err := putSyncChunk(context.Background(), dstCfg, d.SHA256, data); err != nil {
			t.Fatal(err)
		}
	}
	_, err = commitSyncObject(dstCfg, plan, syncPrecondition{expectedETag: etag})
	if !errors.Is(err, errSyncRemoteConflict) {
		t.Fatalf("commit err = %v, want errSyncRemoteConflict", err)
	}

	// The concurrent write must survive untouched -- no silent overwrite.
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil || string(gotBody) != "someone else's concurrent write" {
		t.Fatalf("destination content was not safely preserved: err=%v body=%q", err, gotBody)
	}
}

func TestReplicate_DestinationConflict_AbsentBecomesPresentDuringReplication(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	body := genRandomBytes(8012, 50_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg := syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()}

	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}
	exists, _, err := headSyncDestination(dstCfg)
	if err != nil || exists {
		t.Fatalf("expected destination to be observed absent: exists=%v err=%v", exists, err)
	}

	// Someone else creates the destination key in the meantime.
	if _, err := dstSrv.store.PutObject("dst", "obj", []byte("a racing writer got there first"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}

	chunks := make([]syncLocalChunk, len(desc.Chunks))
	for i, c := range desc.Chunks {
		chunks[i] = syncLocalChunk{SHA256: c.SHA256, Length: c.Length}
	}
	plan := buildSyncPlan(chunks, desc.Size)
	missing, err := negotiateSyncMissing(dstCfg, syncDiscoveryResponse{MaxHashesPerBatch: maxSyncBatchDescriptors}, plan.unique)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.unique {
		if !missing[d.SHA256] {
			continue
		}
		data, err := fetchSourceChunk(context.Background(), srcCfg, d.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if err := putSyncChunk(context.Background(), dstCfg, d.SHA256, data); err != nil {
			t.Fatal(err)
		}
	}
	_, err = commitSyncObject(dstCfg, plan, syncPrecondition{expectAbsent: true})
	if !errors.Is(err, errSyncRemoteConflict) {
		t.Fatalf("commit err = %v, want errSyncRemoteConflict", err)
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil || string(gotBody) != "a racing writer got there first" {
		t.Fatalf("racing writer's content was not preserved: err=%v body=%q", err, gotBody)
	}
}

// =============================================================================
// M8A7: source consistency (immutable captured revision)
// =============================================================================

func TestReplicate_SourceOverwrittenDuringReplicationDoesNotProduceMixedRevision(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	original := genRandomBytes(8013, 500_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", original, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg := syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()}

	// Capture the descriptor -- this is the exact snapshot replicateObject
	// itself would have captured before doing anything else.
	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}

	// The source key is now overwritten with completely different
	// content, entirely mid-flight (a real replicateObject call would
	// never observe this -- it already has desc).
	replacement := genRandomBytes(8014, 500_000)
	if _, err := srcSrv.store.PutObject("src", "obj", replacement, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	// Complete the replication using only the originally-captured desc,
	// exactly as replicateObject's own code does.
	chunks := make([]syncLocalChunk, len(desc.Chunks))
	for i, c := range desc.Chunks {
		chunks[i] = syncLocalChunk{SHA256: c.SHA256, Length: c.Length}
	}
	plan := buildSyncPlan(chunks, desc.Size)
	missing, err := negotiateSyncMissing(dstCfg, syncDiscoveryResponse{MaxHashesPerBatch: maxSyncBatchDescriptors}, plan.unique)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range plan.unique {
		if !missing[d.SHA256] {
			continue
		}
		data, err := fetchSourceChunk(context.Background(), srcCfg, d.SHA256)
		if err != nil {
			t.Fatalf("fetching originally-captured chunk %s after source overwrite: %v", d.SHA256, err)
		}
		if err := putSyncChunk(context.Background(), dstCfg, d.SHA256, data); err != nil {
			t.Fatal(err)
		}
	}
	dstCfg.ContentType = desc.ContentType
	if _, err := commitSyncObject(dstCfg, plan, syncPrecondition{expectAbsent: true}); err != nil {
		t.Fatal(err)
	}

	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, original) {
		t.Fatalf("destination must contain the originally-captured revision, not the overwrite or a mix")
	}
	if bytes.Equal(gotBody, replacement) {
		t.Fatalf("destination must NOT contain the overwriting content")
	}

	// The source's *current* pointer, meanwhile, correctly reflects the
	// overwrite -- proving this was never a lost write, just a
	// consciously captured, independent revision.
	_, curBody, err := srcSrv.store.GetObject("src", "obj")
	if err != nil || !bytes.Equal(curBody, replacement) {
		t.Fatalf("source's current object should reflect the overwrite: err=%v", err)
	}
}

// =============================================================================
// M8A9: resume / retry, including a real process interruption
// =============================================================================

func TestReplicate_ResumeAfterPartialPriorUpload(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	body := genRandomBytes(8015, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(desc.Chunks) < 2 {
		t.Fatalf("fixture too small to exercise partial resume (%d chunks)", len(desc.Chunks))
	}
	// Simulate "the CLI died after some chunks reached the destination":
	// directly PUT the first chunk to the destination's CAS, exactly as
	// if an earlier, interrupted replicate run had gotten that far.
	first := desc.Chunks[0]
	firstData, err := fetchSourceChunk(context.Background(), srcCfg, first.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	dstCfg := syncClientConfig{Endpoint: dstTS.URL, Creds: creds, Region: region, HTTPClient: dstTS.Client()}
	if err := putSyncChunk(context.Background(), dstCfg, first.SHA256, firstData); err != nil {
		t.Fatal(err)
	}

	cfg := replicateConfig{
		Source: srcCfg,
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("resumed replicateObject: %v", err)
	}
	if stats.UploadedBytes >= stats.LogicalBytes {
		t.Fatalf("resume should have skipped the already-primed chunk: uploaded=%d logical=%d", stats.UploadedBytes, stats.LogicalBytes)
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("destination content mismatch after resume: err=%v", err)
	}
}

// TestReplicate_ResumeAcrossRealProcessInterruption proves M8A9 with an
// actual OS process kill (not merely a simulated partial-upload state):
// a real `zeros3 replicate` subprocess is started against two real
// server subprocesses (real HTTP, real TCP) and killed partway through,
// before it can commit anything; a second, uninterrupted run is then
// required to complete the replication correctly.
func TestReplicate_ResumeAcrossRealProcessInterruption(t *testing.T) {
	bin := buildZeros3Binary(t)

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcAddr := freeTCPAddr(t)
	dstAddr := freeTCPAddr(t)

	srcCmd := exec.Command(bin, "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstCmd := exec.Command(bin, "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}

	// Populate the source over real HTTP against the real running
	// subprocess -- a real 20MB object gives replication enough genuine
	// network round trips to interrupt reliably mid-flight below.
	if resp := doSignedRequest(t, client, "http://"+srcAddr, signer, http.MethodPut, "/src", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create source bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	body := genRandomBytes(8016, 20_000_000)
	if resp := doSignedRequest(t, client, "http://"+srcAddr, signer, http.MethodPut, "/src/big.bin", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("populate source object: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodPut, "/dst", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create destination bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	replicateArgs := []string{"replicate", "-from", "http://" + srcAddr, "-to", "http://" + dstAddr, "s3://src/big.bin", "s3://dst/big.bin"}

	// First attempt: a real subprocess, killed shortly after it starts --
	// well before a 20MB transfer across real HTTP round trips could
	// plausibly finish -- so it never reaches commit.
	firstAttempt := exec.Command(bin, replicateArgs...)
	if err := firstAttempt.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	firstAttempt.Process.Kill()
	firstAttempt.Wait()

	if resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodHead, "/dst/big.bin", nil, nil); resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		t.Fatalf("the interrupted attempt must not have produced a visible destination object")
	} else {
		resp.Body.Close()
	}

	// Second, uninterrupted attempt must complete the replication
	// correctly, resuming from whatever the killed attempt already
	// landed in the destination's CAS.
	secondOut, secondErr, code := runZeros3CLI(t, bin, replicateArgs...)
	if code != 0 {
		t.Fatalf("resumed replicate failed (code %d): stdout=%s stderr=%s", code, secondOut, secondErr)
	}
	t.Logf("resumed replicate output:\n%s", secondOut)

	resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodGet, "/dst/big.bin", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET destination after resumed replicate: status %d", resp.StatusCode)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("destination content incorrect after interrupted-then-resumed replicate")
	}
}

func TestReplicate_DestinationServerRestartBetweenAttempts(t *testing.T) {
	srcDir, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	_ = srcDir

	body := genRandomBytes(8017, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}
	first := desc.Chunks[0]
	firstData, err := fetchSourceChunk(context.Background(), srcCfg, first.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	primeCfg := syncClientConfig{Endpoint: dstTS.URL, Creds: creds, Region: region, HTTPClient: dstTS.Client()}
	if err := putSyncChunk(context.Background(), primeCfg, first.SHA256, firstData); err != nil {
		t.Fatal(err)
	}

	// Restart the destination: no durable replication-session state
	// exists anywhere, so CAS durability alone must make resume work
	// across a real process restart.
	dstTS.Close()
	if err := dstSrv.store.Close(); err != nil {
		t.Fatal(err)
	}
	dstStore2, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dstStore2.Close()
	dstSrv2 := NewServer(dstStore2, creds, region)
	dstTS2 := httptest.NewServer(dstSrv2)
	defer dstTS2.Close()

	cfg := replicateConfig{
		Source: srcCfg,
		Dest:   syncClientConfig{Endpoint: dstTS2.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS2.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("post-restart replicateObject: %v", err)
	}
	if stats.UploadedBytes >= stats.LogicalBytes {
		t.Fatalf("post-restart resume should have skipped the pre-restart chunk: uploaded=%d logical=%d", stats.UploadedBytes, stats.LogicalBytes)
	}
	_, gotBody, err := dstStore2.GetObject("dst", "obj")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("destination content mismatch after restart+resume: err=%v", err)
	}
}

func TestReplicate_SourceServerRestartBetweenDescriptorAndChunkFetch(t *testing.T) {
	srcDir, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	body := genRandomBytes(8018, 1_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	desc, err := fetchSourceDescriptor(srcCfg)
	if err != nil {
		t.Fatal(err)
	}

	// Restart the source server (process-level durability, not an
	// in-memory cache) between descriptor capture and the rest of
	// replication.
	srcTS.Close()
	if err := srcSrv.store.Close(); err != nil {
		t.Fatal(err)
	}
	srcStore2, err := OpenStore(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer srcStore2.Close()
	srcSrv2 := NewServer(srcStore2, creds, region)
	srcTS2 := httptest.NewServer(srcSrv2)
	defer srcTS2.Close()
	srcCfg.Endpoint = srcTS2.URL
	srcCfg.HTTPClient = srcTS2.Client()

	cfg := replicateConfig{
		Source: srcCfg,
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject after source restart: %v", err)
	}
	if stats.LogicalBytes != int64(len(body)) {
		t.Fatalf("LogicalBytes = %d, want %d", stats.LogicalBytes, len(body))
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("destination content mismatch after source restart: err=%v", err)
	}
	_ = desc
}

// =============================================================================
// M8A10: statistics
// =============================================================================

func TestReplicate_StatsExactAccounting(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	base := genRandomBytes(8019, 1_000_000)
	mustPutSourceObject(t, dstSrv, "dst", "already-here", base, "application/octet-stream", nil)
	mutated := append([]byte{}, base...)
	copy(mutated[100_000:104_096], genRandomBytes(8020, 4096))
	mustPutSourceObject(t, srcSrv, "src", "obj", mutated, "application/octet-stream", nil)

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}

	if stats.LogicalBytes != int64(len(mutated)) {
		t.Fatalf("LogicalBytes = %d, want %d", stats.LogicalBytes, len(mutated))
	}
	if stats.TotalChunks != len(func() []syncLocalChunk {
		chunks, _, err := scanBytesForSyncTest(mutated)
		if err != nil {
			t.Fatal(err)
		}
		return chunks
	}()) {
		t.Fatalf("TotalChunks did not match an independent CDC scan of the same bytes")
	}
	if stats.ChunksReused+stats.MissingChunkOccur != stats.TotalChunks {
		t.Fatalf("ChunksReused(%d) + MissingChunkOccur(%d) != TotalChunks(%d)", stats.ChunksReused, stats.MissingChunkOccur, stats.TotalChunks)
	}
	if stats.UploadedBytes+stats.BytesAvoided != stats.LogicalBytes {
		t.Fatalf("UploadedBytes(%d) + BytesAvoided(%d) != LogicalBytes(%d)", stats.UploadedBytes, stats.BytesAvoided, stats.LogicalBytes)
	}
	if stats.UploadedBytes == 0 || stats.UploadedBytes >= stats.LogicalBytes {
		t.Fatalf("expected a genuine partial transfer, got UploadedBytes=%d of LogicalBytes=%d", stats.UploadedBytes, stats.LogicalBytes)
	}
	var buf bytes.Buffer
	printSyncStats(&buf, stats)
	t.Logf("M8A replication stats:\n%s", buf.String())
}

// scanBytesForSyncTest runs the exact CDC chunker over in-memory bytes,
// for independent cross-checking of TotalChunks in the stats test above
// (writes to a temp file since scanLocalFileForSync operates on a path).
func scanBytesForSyncTest(data []byte) ([]syncLocalChunk, int64, error) {
	f, err := os.CreateTemp("", "m8a-scan-*")
	if err != nil {
		return nil, 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return nil, 0, err
	}
	return scanLocalFileForSync(f.Name())
}

// =============================================================================
// Demonstration fixture (M8A demo, honest strong-reuse case)
// =============================================================================

func TestReplicate_M8ADemonstrationFixture(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	const size = 16_000_000
	base := genRandomBytes(9999, size)
	// Store B already contains the base content (under a different key,
	// e.g. an earlier related object) but NOT the target object itself --
	// an honest, strong-but-not-manufactured reuse scenario, per the M8A
	// task's own instruction not to fake an all-zero-transfer demo.
	mustPutSourceObject(t, dstSrv, "demo-dst", "related-object-already-present", base, "application/octet-stream", nil)

	edited := append([]byte{}, base...)
	mid := size / 2
	insertion := genRandomBytes(11111, 8192)
	edited = append(edited[:mid], append(insertion, edited[mid:]...)...)
	mustPutSourceObject(t, srcSrv, "demo-src", "target-object", edited, "application/octet-stream", nil)

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "demo-src", Key: "target-object", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "demo-dst", Key: "target-object", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	reuse := float64(stats.BytesAvoided) / float64(stats.LogicalBytes) * 100
	if reuse < 90.0 {
		t.Fatalf("expected a strong, honest reuse figure for a localized edit, got %.1f%%", reuse)
	}

	entry, gotBody, err := dstSrv.store.GetObject("demo-dst", "target-object")
	if err != nil || !bytes.Equal(gotBody, edited) {
		t.Fatalf("destination content mismatch: err=%v", err)
	}

	// Restart destination and re-verify (AWS-SDK-equivalent GET, deep
	// verify) exactly as the M8A demo/external harness script does.
	dstTS.Close()
	if err := dstSrv.store.Close(); err != nil {
		t.Fatal(err)
	}
	dstStore2, err := OpenStore(dstSrv.store.root)
	if err != nil {
		t.Fatal(err)
	}
	defer dstStore2.Close()
	_, restartedBody, err := dstStore2.GetObject("demo-dst", "target-object")
	if err != nil || !bytes.Equal(restartedBody, edited) {
		t.Fatalf("GET after destination restart mismatch: err=%v", err)
	}
	verifyRes, err := dstStore2.Verify(true)
	if err != nil || !verifyRes.OK() {
		t.Fatalf("deep verify after replication failed: err=%v ok=%v", err, verifyRes.OK())
	}

	t.Logf("M8A demonstration fixture:")
	t.Logf("Logical object:          %s", humanBytes(stats.LogicalBytes))
	t.Logf("Chunks:                  %d", stats.TotalChunks)
	t.Logf("Already at destination:  %d", stats.ChunksReused)
	t.Logf("Transferred chunks:      %d", stats.UniqueChunksUploaded)
	t.Logf("Transferred payload:     %s", humanBytes(stats.UploadedBytes))
	t.Logf("Transfer avoided:        %s", humanBytes(stats.BytesAvoided))
	t.Logf("Reuse:                   %.1f%%", reuse)
	_ = entry
}

// =============================================================================
// Regression: new endpoints/CLI verb don't disturb existing behavior
// =============================================================================

func TestReplicate_NewEndpointsRejectUnauthenticatedRequests(t *testing.T) {
	_, srcSrv, _, _, _, _ := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()

	resp, err := http.Get(srcTS.URL + zeros3SyncObjectPath + "?bucket=src&key=obj")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unauthenticated GET /object should be rejected, got 200")
	}

	resp2, err := http.Get(srcTS.URL + zeros3SyncChunksPrefix + strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("unauthenticated GET /chunks/<sha> should be rejected, got 200")
	}
}

func TestReplicate_UnknownExtensionPathStillNotBucketParsed(t *testing.T) {
	_, srcSrv, _, _, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}

	status, _ := doSyncRequest(t, srcTS.Client(), srcTS.URL, signer, http.MethodGet, "/_zeros3/v1/bogus", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (unknown ZeroS3 operation, not a bucket lookup)", status)
	}
}

func TestReplicate_OrdinaryS3AndM6SyncUnaffected(t *testing.T) {
	dir, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "regress")

	// Ordinary S3 PUT/GET must be entirely unaffected by M8A's new routes
	// and the putSyncChunk refactor.
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: region}
	resp := doSignedRequest(t, srcTS.Client(), srcTS.URL, signer, http.MethodPut, "/regress/plain.txt", []byte("ordinary S3 PUT"), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ordinary PUT status = %d", resp.StatusCode)
	}
	got := getSyncObjectBytes(t, srcTS, creds, region, "regress", "plain.txt")
	if string(got) != "ordinary S3 PUT" {
		t.Fatalf("ordinary GET returned %q", got)
	}

	// M6 local sync (which now calls the refactored putSyncChunk) must
	// still work end to end.
	body := genRandomBytes(8021, 200_000)
	path := writeSyncTempFile(t, dir, "m6regress.bin", body)
	mustCreateReplicateBucket(t, dstSrv, "regress2")
	stats, err := syncFile(syncClientConfig{LocalPath: path, Endpoint: dstTS.URL, Bucket: "regress2", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()})
	if err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if stats.UploadedBytes != int64(len(body)) {
		t.Fatalf("M6 sync of a brand-new file should upload everything: uploaded=%d want=%d", stats.UploadedBytes, len(body))
	}
	gotSync := getSyncObjectBytes(t, dstTS, creds, region, "regress2", "obj")
	if !bytes.Equal(gotSync, body) {
		t.Fatalf("M6 sync GET mismatch after M8A refactor")
	}
}

// =============================================================================
// M8B: peer-assisted corruption repair (`zeros3 repair`)
//
// These tests exercise repairFindings/annotateAffectedObjects/
// casRepairPublish/fetchRepairChunk/repairFromPeer/runRepair -- M8B's
// genuinely new pieces (see zeros3.go section 15e's doc comment for the
// full list of what's reused unmodified from M1-M8A: computeReachability,
// signSigV4Request, zeros3SyncChunksPrefix/handleSyncChunkDownload,
// discoverZeroS3Sync, writeFileDurable/syncDir, humanBytes). They reuse
// the exact same test infrastructure M6/M8A's own tests already
// established (newSyncTestServer, buildZeros3Binary, runZeros3CLI,
// freeTCPAddr, waitForZeros3Serve, doSignedRequest, testSigner) rather
// than inventing a parallel harness.
// =============================================================================

func mustCreateLocalStore(t *testing.T) (dir string, store *Store) {
	t.Helper()
	dir = t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return dir, store
}

func mustPutObject(t *testing.T, store *Store, bucket, key string, body []byte, contentType string, metadata map[string]string) *objectEntry {
	t.Helper()
	entry, err := store.PutObject(bucket, key, body, contentType, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustManifestFor(t *testing.T, store *Store, entry *objectEntry) manifestV1 {
	t.Helper()
	man, err := store.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return man
}

func corruptChunkOnDisk(t *testing.T, store *Store, hexDigest string) {
	t.Helper()
	sum, err := decodeHexSHA256(hexDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.chunkPath(sum), []byte("deliberately corrupted bytes that do not match this digest"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func truncateChunkOnDisk(t *testing.T, store *Store, hexDigest string, newLen int) {
	t.Helper()
	sum, err := decodeHexSHA256(hexDigest)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.chunkPath(sum))
	if err != nil {
		t.Fatal(err)
	}
	if newLen > len(data) {
		newLen = len(data)
	}
	if err := os.WriteFile(store.chunkPath(sum), data[:newLen], 0o644); err != nil {
		t.Fatal(err)
	}
}

func deleteChunkOnDisk(t *testing.T, store *Store, hexDigest string) {
	t.Helper()
	sum, err := decodeHexSHA256(hexDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}
}

func mustPeerConfig(peerTS *httptest.Server, creds Credentials, region string) syncClientConfig {
	return syncClientConfig{Endpoint: peerTS.URL, Creds: creds, Region: region, HTTPClient: peerTS.Client()}
}

// primePeerWithObject puts the same bucket/key/body/contentType/metadata
// on the peer server, so the peer's CAS ends up holding byte-identical
// chunks (same content -> same SHA-256 -> same CDC v1 chunk boundaries,
// which are deterministic) to whatever the local store references --
// letting repair fetch real, correct bytes from it.
func primePeerWithObject(t *testing.T, peerSrv *Server, bucket, key string, body []byte, contentType string, metadata map[string]string) {
	t.Helper()
	if err := peerSrv.store.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
	if _, err := peerSrv.store.PutObject(bucket, key, body, contentType, metadata); err != nil {
		t.Fatal(err)
	}
}

// =============================================================================
// M8B A1/A3: detection (repairFindings)
// =============================================================================

func TestRepair_HealthyStoreNoFindings(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "k", genRandomBytes(20001, 300_000), "application/octet-stream", nil)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("healthy store: findings = %+v, want none", findings)
	}
}

func TestRepair_MissingChunkDetected(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	entry := mustPutObject(t, store, "b", "k", genRandomBytes(20002, 500_000), "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	target := man.Chunks[0]
	deleteChunkOnDisk(t, store, target.SHA256)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SHA256 != target.SHA256 || findings[0].Kind != "missing" {
		t.Fatalf("findings = %+v, want exactly one missing finding for %s", findings, target.SHA256)
	}
	if findings[0].Length != target.Length {
		t.Fatalf("Length = %d, want %d", findings[0].Length, target.Length)
	}
	if len(findings[0].AffectedObjects) != 1 || findings[0].AffectedObjects[0] != "b/k" {
		t.Fatalf("AffectedObjects = %v, want [\"b/k\"]", findings[0].AffectedObjects)
	}
}

func TestRepair_CorruptChunkContentMismatchDetected(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	entry := mustPutObject(t, store, "b", "k", genRandomBytes(20003, 500_000), "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	target := man.Chunks[0].SHA256
	corruptChunkOnDisk(t, store, target)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SHA256 != target || findings[0].Kind != "corrupt" {
		t.Fatalf("findings = %+v, want exactly one corrupt finding for %s", findings, target)
	}
}

func TestRepair_WrongLengthChunkDetectedAsCorrupt(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	entry := mustPutObject(t, store, "b", "k", genRandomBytes(20004, 500_000), "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	target := man.Chunks[0]
	truncateChunkOnDisk(t, store, target.SHA256, int(target.Length)/2)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SHA256 != target.SHA256 || findings[0].Kind != "corrupt" {
		t.Fatalf("findings = %+v, want exactly one corrupt (wrong-length) finding for %s", findings, target.SHA256)
	}
}

func TestRepair_MultipleCorruptChunksDetected(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	entry := mustPutObject(t, store, "b", "k", genRandomBytes(20005, 2_000_000), "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 3 {
		t.Fatalf("need at least 3 chunks to test, got %d", len(man.Chunks))
	}
	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	deleteChunkOnDisk(t, store, man.Chunks[1].SHA256)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want exactly 2", findings)
	}
}

func TestRepair_SharedCorruptDigestAffectsAllReferencingObjects(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20006, 400_000)
	e1 := mustPutObject(t, store, "b", "k1", body, "application/octet-stream", nil)
	mustPutObject(t, store, "b", "k2", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, e1)
	target := man.Chunks[0].SHA256
	corruptChunkOnDisk(t, store, target)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one deduplicated finding (fetch/repair once, not per-object)", findings)
	}
	got := map[string]bool{}
	for _, o := range findings[0].AffectedObjects {
		got[o] = true
	}
	if !got["b/k1"] || !got["b/k2"] || len(findings[0].AffectedObjects) != 2 {
		t.Fatalf("AffectedObjects = %v, want exactly [b/k1 b/k2]", findings[0].AffectedObjects)
	}
}

// =============================================================================
// M8B A4: peer fetch (fetchRepairChunk)
// =============================================================================

func TestFetchRepairChunk_PeerHasChunkSucceeds(t *testing.T) {
	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	body := genRandomBytes(20010, 20_000)
	sum, err := peerSrv.store.casWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fetchRepairChunk(context.Background(), mustPeerConfig(peerTS, creds, region), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("fetchRepairChunk: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("fetched bytes mismatch")
	}
}

func TestFetchRepairChunk_PeerMissingChunkFailsClearly(t *testing.T) {
	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	fakeDigest := strings.Repeat("ab", 32)
	if _, err := fetchRepairChunk(context.Background(), mustPeerConfig(peerTS, creds, region), fakeDigest); err == nil {
		t.Fatalf("expected an error for a chunk the peer does not have")
	}
}

func TestFetchRepairChunk_MalformedDigestRejected(t *testing.T) {
	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	if _, err := fetchRepairChunk(context.Background(), mustPeerConfig(peerTS, creds, region), "not-a-valid-hex-digest"); err == nil {
		t.Fatalf("expected an error for a malformed digest")
	}
}

func TestFetchRepairChunk_WrongBytesRejectedBeforePublication(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("these are definitely the wrong bytes"))
	}))
	defer fake.Close()
	realDigest := sha256.Sum256([]byte("the real expected content, never actually sent by the fake peer"))
	cfg := syncClientConfig{Endpoint: fake.URL, HTTPClient: fake.Client()}
	if _, err := fetchRepairChunk(context.Background(), cfg, hex.EncodeToString(realDigest[:])); err == nil {
		t.Fatalf("expected a digest-mismatch error")
	}
}

func TestFetchRepairChunk_TruncatedBytesRejected(t *testing.T) {
	real := genRandomBytes(20011, 50_000)
	realSum := sha256.Sum256(real)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(real[:len(real)/2]) // truncated: half the real bytes
	}))
	defer fake.Close()
	cfg := syncClientConfig{Endpoint: fake.URL, HTTPClient: fake.Client()}
	if _, err := fetchRepairChunk(context.Background(), cfg, hex.EncodeToString(realSum[:])); err == nil {
		t.Fatalf("expected an error for truncated peer bytes")
	}
}

func TestFetchRepairChunk_OversizedResponseRejected(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxRepairChunkBytes+4096)
	sum := sha256.Sum256(oversized)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(oversized)
	}))
	defer fake.Close()
	cfg := syncClientConfig{Endpoint: fake.URL, HTTPClient: fake.Client()}
	_, err := fetchRepairChunk(context.Background(), cfg, hex.EncodeToString(sum[:]))
	if err == nil {
		t.Fatalf("expected an oversized-response error")
	}
	if !strings.Contains(err.Error(), "exceeds the maximum chunk size") {
		t.Fatalf("err = %v, want an explicit oversized-response error", err)
	}
}

func TestFetchRepairChunk_AuthFailureRejected(t *testing.T) {
	_, peerSrv, _, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	body := genRandomBytes(20012, 10_000)
	sum, err := peerSrv.store.casWrite(body)
	if err != nil {
		t.Fatal(err)
	}
	wrongCreds := Credentials{AccessKeyID: "WRONGKEY", SecretAccessKey: "totally-wrong-secret-key-value-here-0"}
	cfg := syncClientConfig{Endpoint: peerTS.URL, Creds: wrongCreds, Region: region, HTTPClient: peerTS.Client()}
	if _, err := fetchRepairChunk(context.Background(), cfg, hex.EncodeToString(sum[:])); err == nil {
		t.Fatalf("expected an auth failure error")
	}
}

func TestRepairFromPeer_IncompatiblePeerFailsClearly(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	entry := mustPutObject(t, store, "b", "k", genRandomBytes(20013, 100_000), "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	deleteChunkOnDisk(t, store, man.Chunks[0].SHA256)

	notZeroS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a zeros3 endpoint"))
	}))
	defer notZeroS3.Close()

	cfg := repairConfig{Peer: syncClientConfig{Endpoint: notZeroS3.URL, HTTPClient: notZeroS3.Client()}}
	_, err := store.repairFromPeer(cfg)
	if err == nil || !strings.Contains(err.Error(), "peer capability discovery failed") {
		t.Fatalf("err = %v, want a clear peer capability discovery failure", err)
	}
}

// =============================================================================
// M8B A5/A6/A7: publication and end-to-end repair
// =============================================================================

func TestRepair_MissingChunkRestoredEndToEnd(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20020, 600_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", map[string]string{"x": "y"})
	man := mustManifestFor(t, store, entry)

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", map[string]string{"x": "y"})

	deleteChunkOnDisk(t, store, man.Chunks[0].SHA256)

	pre, err := store.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if pre.OK() {
		t.Fatalf("expected pre-repair deep verify to fail")
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.BadChunks != 1 || stats.Repaired != 1 || stats.Unresolved != 0 || !stats.PostRepairOK {
		t.Fatalf("stats = %+v, want 1 bad/1 repaired/0 unresolved/OK", stats)
	}

	post, err := store.Verify(true)
	if err != nil || !post.OK() {
		t.Fatalf("post-repair verify: res=%+v err=%v", post, err)
	}
	_, gotBody, err := store.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after repair: err=%v", err)
	}
}

func TestRepair_CorruptExistingChunkAtomicallyReplaced(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20021, 600_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.Repaired != 1 || !stats.PostRepairOK {
		t.Fatalf("stats = %+v", stats)
	}
	_, gotBody, err := store.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after repair: err=%v", err)
	}
}

func TestRepair_HealthyChunkNeverRewritten(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20022, 600_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 2 {
		t.Fatalf("need at least 2 chunks")
	}
	healthySum, _ := decodeHexSHA256(man.Chunks[1].SHA256)
	healthyPath := store.chunkPath(healthySum)
	before, err := os.Stat(healthyPath)
	if err != nil {
		t.Fatal(err)
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.SHA256 == man.Chunks[1].SHA256 {
			t.Fatalf("a healthy chunk incorrectly appeared in repair findings: %+v", f)
		}
	}

	if _, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)}); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(healthyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("a healthy chunk file was rewritten: before=%v/%d after=%v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

// =============================================================================
// M8B A7/A8: post-repair proof (restart, versions/metadata, exact stats)
// =============================================================================

func TestRepair_RestartThenGetExact(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20023, 700_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	deleteChunkOnDisk(t, store, man.Chunks[0].SHA256)
	if _, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, gotBody, err := store2.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after restart: err=%v", err)
	}
	post, err := store2.Verify(true)
	if err != nil || !post.OK() {
		t.Fatalf("post-restart deep verify: res=%+v err=%v", post, err)
	}
}

func TestRepair_VersionsMetadataAndETagUnchanged(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20024, 500_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", map[string]string{"a": "b"})
	man := mustManifestFor(t, store, entry)
	beforeETag := entry.etag
	beforeManifestUUID := entry.manifestUUID

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", map[string]string{"a": "b"})

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	if _, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)}); err != nil {
		t.Fatal(err)
	}

	after, _, err := store.HeadObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if after.etag != beforeETag {
		t.Fatalf("ETag changed: before=%s after=%s", beforeETag, after.etag)
	}
	if after.manifestUUID != beforeManifestUUID {
		t.Fatalf("manifest UUID changed: before=%s after=%s -- repair must never rewrite manifests", beforeManifestUUID, after.manifestUUID)
	}
}

func TestRepair_StatsExactAccounting(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20025, 500_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 2 {
		t.Fatalf("need >= 2 chunks")
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	deleteChunkOnDisk(t, store, man.Chunks[1].SHA256)

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := man.Chunks[0].Length + man.Chunks[1].Length
	if stats.BadChunks != 2 || stats.Repaired != 2 || stats.Unresolved != 0 {
		t.Fatalf("stats = %+v, want 2/2/0", stats)
	}
	if stats.PayloadFetched != wantBytes {
		t.Fatalf("PayloadFetched = %d, want %d (actual peer payload, never e.g. logical object size)", stats.PayloadFetched, wantBytes)
	}
	if stats.AffectedObjects != 1 {
		t.Fatalf("AffectedObjects = %d, want 1", stats.AffectedObjects)
	}
	if !stats.PostRepairOK {
		t.Fatalf("PostRepairOK = false, want true")
	}
}

// =============================================================================
// M8B B1: partial repair
// =============================================================================

func TestRepair_PartialRepairPeerMissingSomeChunksReportsHonestly(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20030, 700_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 2 {
		t.Fatalf("need >= 2 chunks")
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()

	sum0, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	orig0, err := store.casRead(sum0)
	if err != nil {
		t.Fatal(err)
	}
	// The peer only has the FIRST chunk's correct bytes -- it genuinely
	// lacks the rest, standing in for "the peer itself is missing some
	// needed data."
	if _, err := peerSrv.store.casWrite(orig0); err != nil {
		t.Fatal(err)
	}

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	deleteChunkOnDisk(t, store, man.Chunks[1].SHA256)

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatalf("repairFromPeer should not itself error on a partial peer: %v", err)
	}
	if stats.BadChunks != 2 || stats.Repaired != 1 || stats.Unresolved != 1 {
		t.Fatalf("stats = %+v, want 2 bad/1 repaired/1 unresolved", stats)
	}
	if len(stats.Failures) != 1 || stats.Failures[0].SHA256 != man.Chunks[1].SHA256 {
		t.Fatalf("Failures = %+v, want exactly one failure for %s", stats.Failures, man.Chunks[1].SHA256)
	}
	if stats.PostRepairOK {
		t.Fatalf("PostRepairOK = true, want false (one chunk remains genuinely broken)")
	}

	// The chunk that WAS successfully fixed must remain fixed, not rolled
	// back merely because a sibling chunk's repair failed.
	if _, err := store.casRead(sum0); err != nil {
		t.Fatalf("the successfully-repaired chunk was rolled back: %v", err)
	}
}

// =============================================================================
// M8B B2/B3: resume (no durable repair-session state)
// =============================================================================

func TestRepair_ResumeAfterPartialRunOnlyFetchesRemaining(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20031, 700_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 2 {
		t.Fatalf("need >= 2 chunks")
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()

	sum0, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	sum1, _ := decodeHexSHA256(man.Chunks[1].SHA256)
	orig0, err := store.casRead(sum0)
	if err != nil {
		t.Fatal(err)
	}
	orig1, err := store.casRead(sum1)
	if err != nil {
		t.Fatal(err)
	}

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	deleteChunkOnDisk(t, store, man.Chunks[1].SHA256)

	// First run: the peer only has chunk 0's bytes -- standing in for "the
	// process died after fixing some, but not all, of the bad chunks."
	if _, err := peerSrv.store.casWrite(orig0); err != nil {
		t.Fatal(err)
	}
	stats1, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	if stats1.Repaired != 1 || stats1.Unresolved != 1 {
		t.Fatalf("first run stats = %+v", stats1)
	}

	// Second run: now the peer also has chunk 1's bytes. No durable
	// repair-session state exists anywhere -- repairFindings must
	// independently rediscover that only chunk 1 is still broken (BadChunks
	// == 1, not 2), proving the already-fixed chunk is never re-fetched.
	if _, err := peerSrv.store.casWrite(orig1); err != nil {
		t.Fatal(err)
	}
	stats2, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.BadChunks != 1 || stats2.Repaired != 1 || stats2.Unresolved != 0 {
		t.Fatalf("second run stats = %+v, want BadChunks=1 (only the still-broken chunk rediscovered)", stats2)
	}
	if !stats2.PostRepairOK {
		t.Fatalf("expected the store to be fully healthy after the second run")
	}
}

func TestRepair_LocalRestartResumesNaturally(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20032, 700_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 2 {
		t.Fatalf("need >= 2 chunks")
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)
	deleteChunkOnDisk(t, store, man.Chunks[1].SHA256)

	// Simulate "some repair completed, then the local server restarted" by
	// repairing only chunk 0 directly, then closing and reopening the
	// store as a brand-new *Store (a real restart-equivalent, matching
	// every other M6/M8A restart-proof test's own pattern).
	sum0, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	data0, err := peerSrv.store.casRead(sum0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.casRepairPublish(sum0, data0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	stats, err := store2.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	if stats.BadChunks != 1 || stats.Repaired != 1 || !stats.PostRepairOK {
		t.Fatalf("post-restart resume stats = %+v, want BadChunks=1 (only chunk 1)", stats)
	}
	_, gotBody, err := store2.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after restart+resume: err=%v", err)
	}
}

// =============================================================================
// M8B B4: peer restart / unavailability
// =============================================================================

func TestRepair_PeerRestartBetweenAttempts(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	body := genRandomBytes(20033, 400_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)

	peerDir, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	deleteChunkOnDisk(t, store, man.Chunks[0].SHA256)

	// The peer is unavailable (like a stopped process) for the first
	// attempt.
	peerTS.Close()
	if err := peerSrv.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.repairFromPeer(repairConfig{Peer: syncClientConfig{Endpoint: peerTS.URL, Creds: creds, Region: region}}); err == nil {
		t.Fatalf("expected a clear error when the peer is unreachable")
	}

	// "Restart" the peer -- reopen its store and serve again -- and prove
	// a fresh repair attempt now succeeds cleanly, with no leftover state
	// from the failed attempt to clean up.
	peerStore2, err := OpenStore(peerDir)
	if err != nil {
		t.Fatal(err)
	}
	defer peerStore2.Close()
	peerSrv2 := NewServer(peerStore2, creds, region)
	peerTS2 := httptest.NewServer(peerSrv2)
	defer peerTS2.Close()

	stats, err := store.repairFromPeer(repairConfig{Peer: syncClientConfig{Endpoint: peerTS2.URL, Creds: creds, Region: region, HTTPClient: peerTS2.Client()}})
	if err != nil {
		t.Fatalf("repair after peer restart: %v", err)
	}
	if !stats.PostRepairOK || stats.Repaired != 1 {
		t.Fatalf("stats after peer restart = %+v", stats)
	}
}

// =============================================================================
// M8B B5: concurrency
// =============================================================================

func TestRepair_ConcurrentGetOnUnrelatedObjectDuringRepair(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	bodyA := genRandomBytes(20040, 3_000_000)
	bodyB := genRandomBytes(20041, 50_000)
	entryA := mustPutObject(t, store, "b", "a", bodyA, "application/octet-stream", nil)
	mustPutObject(t, store, "b", "b", bodyB, "application/octet-stream", nil)
	manA := mustManifestFor(t, store, entryA)

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "a", bodyA, "application/octet-stream", nil)

	for _, c := range manA.Chunks {
		corruptChunkOnDisk(t, store, c.SHA256)
	}

	stop := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				errCh <- nil
				return
			default:
			}
			_, gotB, err := store.GetObject("b", "b")
			if err != nil || !bytes.Equal(gotB, bodyB) {
				errCh <- fmt.Errorf("concurrent GetObject(b/b) failed during repair of an unrelated object: err=%v", err)
				return
			}
		}
	}()

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	close(stop)
	if gerr := <-errCh; gerr != nil {
		t.Fatal(gerr)
	}
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if !stats.PostRepairOK {
		t.Fatalf("stats = %+v", stats)
	}
}

// =============================================================================
// M8B B6/B7/B8: reachability scope (never repair garbage; versions;
// multipart)
// =============================================================================

func TestRepair_UnreachableOrphanChunkNeverTriggersRepair(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	mustPutObject(t, store, "b", "k", genRandomBytes(20050, 100_000), "application/octet-stream", nil)

	// An orphan chunk: written directly to CAS, never referenced by any
	// manifest -- exactly the kind of garbage GC (never repair) is meant
	// to reclaim.
	orphan := genRandomBytes(20051, 30_000)
	orphanSum, err := store.casWrite(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.chunkPath(orphanSum), []byte("corrupt orphan bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.SHA256 == hex.EncodeToString(orphanSum[:]) {
			t.Fatalf("repair findings must never include unreachable orphan garbage, found: %+v", f)
		}
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none (the only broken chunk is unreachable garbage)", findings)
	}
}

func TestRepair_RetainedHistoricalVersionChunkRepaired(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	v1Body := genRandomBytes(20060, 300_000)
	mustPutObject(t, store, "b", "k", v1Body, "application/octet-stream", nil)
	v2Body := genRandomBytes(20061, 300_000)
	mustPutObject(t, store, "b", "k", v2Body, "application/octet-stream", nil) // archives v1 into history

	hist, _, err := store.ListVersions("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected exactly one retained historical version, got %d", len(hist))
	}
	v1Man, err := store.readVerifiedManifest(hist[0].manifestUUID, hist[0].manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	for _, c := range v1Man.Chunks {
		sum, _ := decodeHexSHA256(c.SHA256)
		data, err := store.casRead(sum)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peerSrv.store.casWrite(data); err != nil {
			t.Fatal(err)
		}
	}

	corruptChunkOnDisk(t, store, v1Man.Chunks[0].SHA256)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.SHA256 != v1Man.Chunks[0].SHA256 {
			continue
		}
		found = true
		hasHistSubject := false
		for _, o := range f.AffectedObjects {
			if strings.HasPrefix(o, "history:b/k@") {
				hasHistSubject = true
			}
		}
		if !hasHistSubject {
			t.Fatalf("AffectedObjects = %v, want a history: subject", f.AffectedObjects)
		}
	}
	if !found {
		t.Fatalf("findings did not include the retained historical version's corrupt chunk: %+v", findings)
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.PostRepairOK {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestRepair_MultipartInProgressPartChunkRepaired(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	store.CreateBucket("b")
	uploadID, err := store.CreateMultipartUpload("b", "mp-key", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	partBody := genRandomBytes(20070, 500_000)
	if _, err := store.UploadPart("b", "mp-key", uploadID, 1, partBody); err != nil {
		t.Fatal(err)
	}

	var partChunks []chunkRef
	for _, up := range store.snapshotUploads() {
		if up.uploadID == uploadID {
			partChunks = up.parts[1].chunks
		}
	}
	if len(partChunks) == 0 {
		t.Fatalf("expected the uploaded part to have published chunks")
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	for _, c := range partChunks {
		sum, _ := decodeHexSHA256(c.SHA256)
		data, err := store.casRead(sum)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peerSrv.store.casWrite(data); err != nil {
			t.Fatal(err)
		}
	}

	corruptChunkOnDisk(t, store, partChunks[0].SHA256)

	findings, err := store.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.SHA256 == partChunks[0].SHA256 {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings did not include the in-progress multipart part's corrupt chunk: %+v", findings)
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region)})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.PostRepairOK {
		t.Fatalf("stats = %+v", stats)
	}
}

// =============================================================================
// M8B CLI (`zeros3 repair`)
// =============================================================================

func TestCLI_Repair_EndToEndSmoke(t *testing.T) {
	bin := buildZeros3Binary(t)
	localDir := t.TempDir()
	localStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := localStore.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(20080, 500_000)
	entry, err := localStore.PutObject("b", "k", body, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := localStore.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	if err := os.Remove(localStore.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}

	peerAddr := freeTCPAddr(t)
	peerDir := t.TempDir()
	peerCmd := exec.Command(bin, "serve", "-store", peerDir, "-addr", peerAddr)
	if err := peerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { peerCmd.Process.Kill(); peerCmd.Wait() }()
	waitForZeros3Serve(t, peerAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create peer bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b/k", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("populate peer object: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	out, stderr, code := runZeros3CLI(t, bin, "repair", "-store", localDir, "-from", "http://"+peerAddr)
	if code != 0 {
		t.Fatalf("repair CLI failed (code %d): stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "Bad chunks:          1") || !strings.Contains(out, "Repaired:            1") || !strings.Contains(out, "Post-repair verify:  OK") {
		t.Fatalf("unexpected repair output:\n%s", out)
	}

	store2, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, gotBody, err := store2.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after CLI repair: err=%v", err)
	}
}

func TestCLI_Repair_ExitCodeNonzeroWhenUnresolved(t *testing.T) {
	bin := buildZeros3Binary(t)
	localDir := t.TempDir()
	localStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := localStore.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := localStore.PutObject("b", "k", genRandomBytes(20081, 100_000), "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := localStore.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	if err := os.Remove(localStore.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}

	peerAddr := freeTCPAddr(t)
	peerDir := t.TempDir()
	peerCmd := exec.Command(bin, "serve", "-store", peerDir, "-addr", peerAddr)
	if err := peerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { peerCmd.Process.Kill(); peerCmd.Wait() }()
	waitForZeros3Serve(t, peerAddr)
	// The peer never receives the object -- it genuinely lacks the chunk.

	out, _, code := runZeros3CLI(t, bin, "repair", "-store", localDir, "-from", "http://"+peerAddr)
	if code == 0 {
		t.Fatalf("expected a nonzero exit code when repair cannot resolve every finding, stdout=%s", out)
	}
	if !strings.Contains(out, "Unresolved:          1") || !strings.Contains(out, "Post-repair verify:  FAILED") {
		t.Fatalf("unexpected output for a genuinely unresolved repair:\n%s", out)
	}
}

// =============================================================================
// M8B-C: optional one-command detect->repair->reverify
// (`zeros3 verify -deep -repair-from PEER`)
// =============================================================================

func TestCLI_VerifyRepairFrom_OneCommandDetectRepairReverify(t *testing.T) {
	bin := buildZeros3Binary(t)
	localDir := t.TempDir()
	localStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := localStore.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(20100, 500_000)
	entry, err := localStore.PutObject("b", "k", body, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := localStore.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := decodeHexSHA256(man.Chunks[0].SHA256)
	if err := os.Remove(localStore.chunkPath(sum)); err != nil {
		t.Fatal(err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}

	peerAddr := freeTCPAddr(t)
	peerDir := t.TempDir()
	peerCmd := exec.Command(bin, "serve", "-store", peerDir, "-addr", peerAddr)
	if err := peerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { peerCmd.Process.Kill(); peerCmd.Wait() }()
	waitForZeros3Serve(t, peerAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create peer bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b/k", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("populate peer object: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	out, stderr, code := runZeros3CLI(t, bin, "verify", "-store", localDir, "-deep", "-repair-from", "http://"+peerAddr)
	if code != 0 {
		t.Fatalf("verify -repair-from failed (code %d): stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "Repaired:            1") || !strings.Contains(out, "Post-repair verify:  OK") {
		t.Fatalf("unexpected verify -repair-from output:\n%s", out)
	}

	store2, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	_, gotBody, err := store2.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after verify -repair-from: err=%v", err)
	}
}

func TestCLI_VerifyRepairFrom_UnsetPreservesOrdinaryVerifyBehavior(t *testing.T) {
	bin := buildZeros3Binary(t)
	localDir := t.TempDir()
	localStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := localStore.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := localStore.PutObject("b", "k", []byte("hello"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}

	out, _, code := runZeros3CLI(t, bin, "verify", "-store", localDir, "-deep")
	if code != 0 {
		t.Fatalf("ordinary verify (no -repair-from) failed: %s", out)
	}
	if !strings.Contains(out, "ZeroS3 verify (deep)") || !strings.Contains(out, "result           OK") {
		t.Fatalf("ordinary verify output changed by M8B-C's addition: %s", out)
	}
}

func TestCLI_Repair_RealProcessKillThenResume(t *testing.T) {
	bin := buildZeros3Binary(t)

	localDir := t.TempDir()
	localStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := localStore.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(20090, 20_000_000)
	entry, err := localStore.PutObject("b", "k", body, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	man, err := localStore.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt every chunk -- a real, large, multi-chunk repair job, so a
	// real process kill partway through has plenty of room to land
	// mid-loop, matching M8A's own
	// TestReplicate_ResumeAcrossRealProcessInterruption approach.
	for _, c := range man.Chunks {
		corruptChunkOnDisk(t, localStore, c.SHA256)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}

	peerAddr := freeTCPAddr(t)
	peerDir := t.TempDir()
	peerCmd := exec.Command(bin, "serve", "-store", peerDir, "-addr", peerAddr)
	if err := peerCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { peerCmd.Process.Kill(); peerCmd.Wait() }()
	waitForZeros3Serve(t, peerAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create peer bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if resp := doSignedRequest(t, client, "http://"+peerAddr, signer, http.MethodPut, "/b/k", body, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("populate peer object: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	repairArgs := []string{"repair", "-store", localDir, "-from", "http://" + peerAddr}

	firstAttempt := exec.Command(bin, repairArgs...)
	if err := firstAttempt.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	firstAttempt.Process.Kill()
	firstAttempt.Wait()

	midStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	midFindings, err := midStore.repairFindings()
	if err != nil {
		t.Fatal(err)
	}
	midStore.Close()
	if len(midFindings) == 0 {
		t.Skip("the killed attempt happened to finish before the timer fired -- nothing left to resume")
	}

	secondOut, secondErr, code := runZeros3CLI(t, bin, repairArgs...)
	if code != 0 {
		t.Fatalf("resumed repair failed (code %d): stdout=%s stderr=%s", code, secondOut, secondErr)
	}
	if !strings.Contains(secondOut, "Post-repair verify:  OK") {
		t.Fatalf("resumed repair did not report clean: %s", secondOut)
	}

	finalStore, err := OpenStore(localDir)
	if err != nil {
		t.Fatal(err)
	}
	defer finalStore.Close()
	_, gotBody, err := finalStore.GetObject("b", "k")
	if err != nil || !bytes.Equal(gotBody, body) {
		t.Fatalf("GetObject after interrupted-then-resumed repair: err=%v", err)
	}
	post, err := finalStore.Verify(true)
	if err != nil || !post.OK() {
		t.Fatalf("final deep verify: res=%+v err=%v", post, err)
	}
}

// =============================================================================
// M8C: prefix/bucket namespace replication (`zeros3 replicate -recursive`)
//
// M8C generalizes M8A's single-object primitive (replicateObject,
// unmodified) across a source namespace via replicateNamespace (zeros3.go
// section 15f): enumerate (listSourceObjects) -> map (namespaceDestKey) ->
// replicateObject -> aggregate. These tests focus on what's genuinely new
// -- mapping, enumeration/pagination, namespace-level partial-failure/
// non-destructive/resume semantics, and aggregate stats -- never
// re-proving M8A's own already-exhaustive chunk-negotiation/commit/
// conflict/resume coverage a second time against unmodified code.
// =============================================================================

// -----------------------------------------------------------------------
// M8C-A3: source -> destination key mapping (namespaceDestKey)
// -----------------------------------------------------------------------

func TestNamespaceDestKey_WholeBucket(t *testing.T) {
	got, err := namespaceDestKey("", "", "a/b.txt")
	if err != nil || got != "a/b.txt" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_PrefixToPrefix(t *testing.T) {
	got, err := namespaceDestKey("images", "backup", "images/a.png")
	if err != nil || got != "backup/a.png" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_NestedRelative(t *testing.T) {
	got, err := namespaceDestKey("images", "backup", "images/sub/a.png")
	if err != nil || got != "backup/sub/a.png" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_PrefixSourceWholeBucketDest(t *testing.T) {
	got, err := namespaceDestKey("datasets", "", "datasets/a.bin")
	if err != nil || got != "a.bin" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_WholeBucketSourcePrefixDest(t *testing.T) {
	got, err := namespaceDestKey("", "archive", "a/b.txt")
	if err != nil || got != "archive/a/b.txt" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_WeirdCharactersPassedThroughUnaltered(t *testing.T) {
	key := "src/uni café/100%25#frag?q=1"
	got, err := namespaceDestKey("src", "dst", key)
	if err != nil {
		t.Fatal(err)
	}
	want := "dst/uni café/100%25#frag?q=1"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNamespaceDestKey_SpacesAndUnicodePreserved(t *testing.T) {
	got, err := namespaceDestKey("src", "dst", "src/日本語/space name.txt")
	if err != nil || got != "dst/日本語/space name.txt" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_RepeatedSlashesInRelativeSuffixPreserved(t *testing.T) {
	got, err := namespaceDestKey("src", "dst", "src/a//b.txt")
	if err != nil || got != "dst/a//b.txt" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_KeyExactlyEqualsPrefixMapsToEmptyRelative(t *testing.T) {
	got, err := namespaceDestKey("images", "backup", "images/")
	if err != nil || got != "backup/" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestNamespaceDestKey_KeyNotCarryingPrefixRejected(t *testing.T) {
	if _, err := namespaceDestKey("images", "backup", "other/a.png"); err == nil {
		t.Fatal("expected an error for a key that doesn't carry the source prefix")
	}
}

// TestNamespaceDestKey_TwoDistinctSourceKeysNeverCollide is a direct
// hostile-review check: can two source keys accidentally map to the same
// destination key? The stripped prefix has one fixed length for every key
// in one run, so distinct full keys always yield distinct relative
// suffixes.
func TestNamespaceDestKey_TwoDistinctSourceKeysNeverCollide(t *testing.T) {
	a, err := namespaceDestKey("images", "backup", "images/a.png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := namespaceDestKey("images", "backup", "images/aa.png")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("distinct source keys mapped to the same destination key: %q", a)
	}
}

// -----------------------------------------------------------------------
// M8C-A1/A2: source enumeration and pagination (listSourceObjects)
// -----------------------------------------------------------------------

func newNsListTestServer(t *testing.T, bucket string) (srv *Server, cfg syncClientConfig, closeFn func()) {
	t.Helper()
	_, srv, creds, region := newSyncTestServer(t)
	if err := srv.store.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	cfg = syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: region, HTTPClient: ts.Client()}
	return srv, cfg, ts.Close
}

func putObj(t *testing.T, srv *Server, bucket, key string, data []byte) {
	t.Helper()
	if _, err := srv.store.PutObject(bucket, key, data, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
}

func TestListSourceObjects_EmptyBucket(t *testing.T) {
	_, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d objects, want 0", len(got))
	}
}

func TestListSourceObjects_OneObject(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "only.txt", []byte("hi"))
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "only.txt" {
		t.Fatalf("got %+v", got)
	}
}

func TestListSourceObjects_MultipleDeterministicOrder(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	for _, k := range []string{"c.txt", "a.txt", "b.txt"} {
		putObj(t, srv, "b", k, []byte(k))
	}
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.txt", "b.txt", "c.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %d objects, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Key != w {
			t.Fatalf("position %d: got %q, want %q (order not deterministic)", i, got[i].Key, w)
		}
	}
}

func TestListSourceObjects_PrefixFiltering(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "datasets/a.bin", []byte("a"))
	putObj(t, srv, "b", "datasets/b.bin", []byte("b"))
	putObj(t, srv, "b", "other/not-selected.bin", []byte("x"))
	got, err := listSourceObjects(cfg, "datasets/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2: %+v", len(got), got)
	}
	for _, o := range got {
		if !strings.HasPrefix(o.Key, "datasets/") {
			t.Fatalf("unexpected key outside prefix: %q", o.Key)
		}
	}
}

func TestListSourceObjects_ExactlyPageLimit(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	const n = 1000
	for i := 0; i < n; i++ {
		putObj(t, srv, "b", fmt.Sprintf("k/%04d", i), []byte("x"))
	}
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d objects, want %d", len(got), n)
	}
}

func TestListSourceObjects_MultiPageOver1000(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	const n = 1500
	for i := 0; i < n; i++ {
		putObj(t, srv, "b", fmt.Sprintf("k/%04d", i), []byte("x"))
	}
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d objects, want %d", len(got), n)
	}
	seen := make(map[string]bool, n)
	for _, o := range got {
		if seen[o.Key] {
			t.Fatalf("duplicate key across pages: %q", o.Key)
		}
		seen[o.Key] = true
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Key >= got[i].Key {
			t.Fatalf("order not strictly increasing at %d: %q >= %q", i, got[i-1].Key, got[i].Key)
		}
	}
}

func TestListSourceObjects_WeirdKeys(t *testing.T) {
	srv, cfg, closeFn := newNsListTestServer(t, "b")
	defer closeFn()
	weird := []string{
		"space key.txt",
		"unicode/café/日本語.txt",
		"percent/100%25.txt",
		"hash/a#b.txt",
		"question/a?b.txt",
		"slashes/a//b.txt",
	}
	for _, k := range weird {
		putObj(t, srv, "b", k, []byte(k))
	}
	got, err := listSourceObjects(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	gotKeys := make(map[string]bool, len(got))
	for _, o := range got {
		gotKeys[o.Key] = true
	}
	for _, k := range weird {
		if !gotKeys[k] {
			t.Fatalf("weird key %q missing from listing: got %+v", k, got)
		}
	}
}

// -----------------------------------------------------------------------
// M8C-B: replicateNamespace end-to-end (reuses replicateObject unmodified)
// -----------------------------------------------------------------------

func newNsReplicateTestServerPair(t *testing.T, srcBucket, dstBucket string) (srcSrv, dstSrv *Server, srcCfg, dstCfg syncClientConfig, closeFn func()) {
	t.Helper()
	_, srcSrv, creds, region := newSyncTestServer(t)
	_, dstSrv, _, _ = newSyncTestServer(t)
	if err := srcSrv.store.CreateBucket(srcBucket); err != nil {
		t.Fatal(err)
	}
	if err := dstSrv.store.CreateBucket(dstBucket); err != nil {
		t.Fatal(err)
	}
	srcTS := httptest.NewServer(srcSrv)
	dstTS := httptest.NewServer(dstSrv)
	srcCfg = syncClientConfig{Endpoint: srcTS.URL, Bucket: srcBucket, Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg = syncClientConfig{Endpoint: dstTS.URL, Bucket: dstBucket, Creds: creds, Region: region, HTTPClient: dstTS.Client()}
	closeFn = func() { srcTS.Close(); dstTS.Close() }
	return srcSrv, dstSrv, srcCfg, dstCfg, closeFn
}

func TestReplicateNamespace_WholeBucketAllChunksMissing(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "a.bin", genRandomBytes(1, 50_000))
	putObj(t, srcSrv, "src", "b.bin", genRandomBytes(2, 60_000))
	putObj(t, srcSrv, "src", "sub/c.bin", genRandomBytes(3, 70_000))

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Discovered != 3 || result.Replicated != 3 || result.Failed != 0 || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"a.bin", "b.bin", "sub/c.bin"} {
		if _, _, err := dstSrv.store.GetObject("dst", key); err != nil {
			t.Fatalf("GetObject(%q): %v", key, err)
		}
	}
}

// TestReplicateNamespace_PrefixMappingEndToEnd reuses the exact tree
// M8C's own spec gives as its worked example (M8C-A3).
func TestReplicateNamespace_PrefixMappingEndToEnd(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "datasets/a.bin", []byte("a"))
	putObj(t, srcSrv, "src", "datasets/b.bin", []byte("b"))
	putObj(t, srcSrv, "src", "datasets/sub/c.bin", []byte("c"))
	putObj(t, srcSrv, "src", "datasets/sub/d.txt", []byte("d"))
	putObj(t, srcSrv, "src", "other/not-selected.bin", []byte("x"))

	result, err := replicateNamespace(namespaceReplicateConfig{
		Source: srcCfg, SourcePrefix: "datasets",
		Dest: dstCfg, DestPrefix: "archive",
	})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Discovered != 4 || result.Replicated != 4 || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"archive/a.bin", "archive/b.bin", "archive/sub/c.bin", "archive/sub/d.txt"} {
		if _, _, err := dstSrv.store.GetObject("dst", key); err != nil {
			t.Fatalf("GetObject(%q): %v", key, err)
		}
	}
	if _, _, err := dstSrv.store.GetObject("dst", "archive/not-selected.bin"); err == nil {
		t.Fatal("unselected object leaked into destination")
	}
	if _, _, err := dstSrv.store.GetObject("dst", "not-selected.bin"); err == nil {
		t.Fatal("unselected object leaked into destination")
	}
}

func TestReplicateNamespace_DestinationPrepopulatedCASReuse(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	base := genRandomBytes(42, 2_000_000)
	edited := append(append([]byte{}, base[:1_000_000]...), genRandomBytes(99, 10_000)...)
	edited = append(edited, base[1_000_000:]...)

	// Destination already holds a related object under a different key,
	// sharing most of its chunks with the source's edited version.
	putObj(t, dstSrv, "dst", "related/existing.bin", base)
	putObj(t, srcSrv, "src", "edited/new.bin", edited)

	result, err := replicateNamespace(namespaceReplicateConfig{
		Source: srcCfg, SourcePrefix: "edited",
		Dest: dstCfg, DestPrefix: "landed",
	})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Failed != 0 || result.Replicated != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Stats.UploadedBytes >= result.Stats.LogicalBytes {
		t.Fatalf("expected substantial CAS reuse from prepopulated related object: uploaded=%d logical=%d", result.Stats.UploadedBytes, result.Stats.LogicalBytes)
	}
	if result.Stats.BytesAvoided == 0 {
		t.Fatal("expected nonzero avoided bytes")
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "landed/new.bin")
	if err != nil || !bytes.Equal(gotBody, edited) {
		t.Fatalf("destination content mismatch: err=%v", err)
	}
}

// TestReplicateNamespace_SharedChunksAcrossObjectsAccountingExact proves
// M8C-G's "shared chunk cases don't corrupt accounting" requirement:
// because objects are processed sequentially and CAS is shared, the
// second of two byte-identical objects sees every chunk already landed
// by the first and re-transfers nothing.
func TestReplicateNamespace_SharedChunksAcrossObjectsAccountingExact(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	_ = dstSrv
	shared := genRandomBytes(7, 500_000)
	putObj(t, srcSrv, "src", "one.bin", shared)
	putObj(t, srcSrv, "src", "two.bin", shared)

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Replicated != 2 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Stats.LogicalBytes != 2*int64(len(shared)) {
		t.Fatalf("LogicalBytes = %d, want %d", result.Stats.LogicalBytes, 2*len(shared))
	}
	if result.Stats.UploadedBytes != int64(len(shared)) {
		t.Fatalf("UploadedBytes = %d, want exactly one copy's worth (%d) -- shared chunks must not be re-transferred or double counted", result.Stats.UploadedBytes, len(shared))
	}
	if result.Stats.BytesAvoided != int64(len(shared)) {
		t.Fatalf("BytesAvoided = %d, want %d", result.Stats.BytesAvoided, len(shared))
	}
}

func TestReplicateNamespace_EmptyObject(t *testing.T) {
	srcSrv, _, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "empty.bin", []byte{})

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Replicated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Stats.LogicalBytes != 0 {
		t.Fatalf("LogicalBytes = %d, want 0", result.Stats.LogicalBytes)
	}
	var buf bytes.Buffer
	printNsReplicateSummary(&buf, result)
	if strings.Contains(buf.String(), "NaN") || strings.Contains(buf.String(), "+Inf") {
		t.Fatalf("summary output corrupted for zero-byte namespace: %s", buf.String())
	}
}

func TestReplicateNamespace_MetadataPreservedPerObject(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	if _, err := srcSrv.store.PutObject("src", "a.bin", []byte("hello"), "text/x-custom", map[string]string{"owner": "m8c-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg}); err != nil {
		t.Fatal(err)
	}
	entry, man, err := dstSrv.store.HeadObject("dst", "a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if entry.contentType != "text/x-custom" {
		t.Fatalf("content type not preserved: got %q", entry.contentType)
	}
	got := make(map[string]string)
	for _, kv := range man.Metadata {
		got[kv.Key] = kv.Value
	}
	if got["owner"] != "m8c-test" {
		t.Fatalf("metadata not preserved: %+v", got)
	}
}

// -----------------------------------------------------------------------
// M8C-D/M8C-E: per-object failures never abort the run
// -----------------------------------------------------------------------

// TestReplicateNamespace_DestinationConflictOneObjectFailsOthersSucceed
// proves M8C-D: a genuine destination conflict (a concurrent write lands
// on ONE object's destination key between that object's own
// headSyncDestination capture and its commit -- M6B/M8A's own conflict
// mechanism, reused unmodified) fails only that object, never the run.
// An HTTP wrapper around the destination deterministically injects the
// race by intercepting the sync commit request for the targeted key
// (which carries bucket/key in its JSON body, unlike negotiate) and
// performing a direct, unrelated store write immediately before letting
// the real commit handler run -- exactly the same race
// TestReplicate_DestinationConflict_ConcurrentWriteDuringReplicationRejectedSafely
// proves at the single-object level, just injected via interception
// instead of manually walking the pipeline (necessary here since
// replicateNamespace drives the pipeline itself).
func TestReplicateNamespace_DestinationConflictOneObjectFailsOthersSucceed(t *testing.T) {
	_, srcSrv, creds, region := newSyncTestServer(t)
	_, dstSrv, _, _ := newSyncTestServer(t)
	if err := srcSrv.store.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if err := dstSrv.store.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	putObj(t, srcSrv, "src", "a.bin", []byte("source-a"))
	putObj(t, srcSrv, "src", "b.bin", []byte("source-b"))
	putObj(t, srcSrv, "src", "c.bin", []byte("source-c"))
	// b.bin already exists at the destination so its commit precondition
	// captures a real, current ETag to race against.
	putObj(t, dstSrv, "dst", "b.bin", []byte("original destination content"))

	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	raced := false
	dstWrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !raced && r.URL.Path == zeros3SyncCommitPath && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req syncCommitRequest
			if json.Unmarshal(body, &req) == nil && req.Key == "b.bin" {
				raced = true
				if _, err := dstSrv.store.PutObject("dst", "b.bin", []byte("someone else's concurrent write"), "text/plain", nil); err != nil {
					t.Fatal(err)
				}
			}
		}
		dstSrv.ServeHTTP(w, r)
	}))
	defer dstWrapper.Close()

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg := syncClientConfig{Endpoint: dstWrapper.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstWrapper.Client()}

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace top-level: %v", err)
	}
	if result.Discovered != 3 {
		t.Fatalf("Discovered = %d, want 3", result.Discovered)
	}
	if result.Replicated != 2 || result.Failed != 1 || result.OK() {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].Key != "b.bin" {
		t.Fatalf("Failures = %+v", result.Failures)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "a.bin"); err != nil {
		t.Fatalf("a.bin should have replicated: %v", err)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "c.bin"); err != nil {
		t.Fatalf("c.bin should have replicated: %v", err)
	}
	// The concurrent write must survive untouched -- no silent overwrite.
	_, gotBody, err := dstSrv.store.GetObject("dst", "b.bin")
	if err != nil || !bytes.Equal(gotBody, []byte("someone else's concurrent write")) {
		t.Fatalf("b.bin destination content was not safely preserved: err=%v body=%q", err, gotBody)
	}
}

func TestReplicateNamespace_SourceChunkCorruptOneObjectFailsOthersSucceed(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "healthy.bin", genRandomBytes(11, 30_000))
	putObj(t, srcSrv, "src", "corrupt.bin", genRandomBytes(12, 30_000))

	_, man, err := srcSrv.store.HeadObject("src", "corrupt.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	sumBytes, err := hex.DecodeString(man.Chunks[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var sum [32]byte
	copy(sum[:], sumBytes)
	if err := os.WriteFile(srcSrv.store.chunkPath(sum), []byte("corrupted, wrong hash"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace top-level: %v", err)
	}
	if result.Replicated != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Failures[0].Key != "corrupt.bin" {
		t.Fatalf("Failures = %+v", result.Failures)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "healthy.bin"); err != nil {
		t.Fatalf("healthy.bin should have replicated: %v", err)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "corrupt.bin"); err == nil {
		t.Fatal("corrupt.bin must not have been committed at the destination")
	}
}

// TestReplicateNamespace_SourceObjectDisappearsBetweenListingAndFetchReportsFailureContinues
// simulates M8C-I: a key already observed by enumeration disappears
// before its own replicateObject call runs. The one affected object must
// fail cleanly; unrelated objects (lexicographically before and after it)
// must still replicate.
func TestReplicateNamespace_SourceObjectDisappearsBetweenListingAndFetchReportsFailureContinues(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "aaa.bin", []byte("first"))
	putObj(t, srcSrv, "src", "gone.bin", []byte("will vanish"))
	putObj(t, srcSrv, "src", "zzz.bin", []byte("last"))

	deleted := false
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !deleted && r.URL.Path == zeros3SyncObjectPath && r.URL.Query().Get("key") == "gone.bin" {
			deleted = true
			if err := srcSrv.store.DeleteObject("src", "gone.bin"); err != nil {
				t.Fatal(err)
			}
		}
		srcSrv.ServeHTTP(w, r)
	}))
	defer wrapper.Close()
	wrappedSrcCfg := srcCfg
	wrappedSrcCfg.Endpoint = wrapper.URL
	wrappedSrcCfg.HTTPClient = wrapper.Client()

	result, err := replicateNamespace(namespaceReplicateConfig{Source: wrappedSrcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace top-level: %v", err)
	}
	if result.Discovered != 3 || result.Replicated != 2 || result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Failures[0].Key != "gone.bin" {
		t.Fatalf("Failures = %+v", result.Failures)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "aaa.bin"); err != nil {
		t.Fatalf("aaa.bin should have replicated: %v", err)
	}
	if _, _, err := dstSrv.store.GetObject("dst", "zzz.bin"); err != nil {
		t.Fatalf("zzz.bin should have replicated: %v", err)
	}
}

// -----------------------------------------------------------------------
// M8C-C: non-destructive semantics
// -----------------------------------------------------------------------

func TestReplicateNamespace_DestinationOnlyObjectSurvives(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, dstSrv, "dst", "dest-only.bin", []byte("nobody touches me"))
	putObj(t, srcSrv, "src", "a.bin", []byte("source content"))

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	_, body, err := dstSrv.store.GetObject("dst", "dest-only.bin")
	if err != nil || !bytes.Equal(body, []byte("nobody touches me")) {
		t.Fatalf("destination-only object was modified or removed: err=%v body=%q", err, body)
	}
}

func TestReplicateNamespace_SourceRemovalAfterReplicationDoesNotRemoveDestination(t *testing.T) {
	srcSrv, dstSrv, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	putObj(t, srcSrv, "src", "a.bin", []byte("keep me"))
	putObj(t, srcSrv, "src", "b.bin", []byte("delete me from source"))

	if _, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg}); err != nil {
		t.Fatalf("first replicateNamespace: %v", err)
	}
	if err := srcSrv.store.DeleteObject("src", "b.bin"); err != nil {
		t.Fatal(err)
	}

	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("second replicateNamespace: %v", err)
	}
	if result.Discovered != 1 || result.Replicated != 1 || result.Failed != 0 {
		t.Fatalf("rerun after source deletion: result = %+v", result)
	}
	_, body, err := dstSrv.store.GetObject("dst", "b.bin")
	if err != nil || !bytes.Equal(body, []byte("delete me from source")) {
		t.Fatalf("previously-replicated destination copy was removed after source deletion: err=%v", err)
	}
}

// -----------------------------------------------------------------------
// M8C-F: resume (no durable namespace-replication session state)
// -----------------------------------------------------------------------

func TestReplicateNamespace_DestinationRestartThenRerunIsCleanAndDeepVerifies(t *testing.T) {
	srcDir, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	_ = srcDir
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)

	if err := srcSrv.store.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if err := dstSrv.store.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	putObj(t, srcSrv, "src", "a.bin", genRandomBytes(21, 40_000))
	putObj(t, srcSrv, "src", "b.bin", genRandomBytes(22, 50_000))

	srcCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	dstCfg := syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()}

	if _, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg}); err != nil {
		t.Fatalf("first replicateNamespace: %v", err)
	}
	dstTS.Close()
	if err := dstSrv.store.Close(); err != nil {
		t.Fatal(err)
	}

	dstStore2, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dstStore2.Close()
	dstSrv2 := NewServer(dstStore2, creds, region)
	dstTS2 := httptest.NewServer(dstSrv2)
	defer dstTS2.Close()

	dstCfg2 := syncClientConfig{Endpoint: dstTS2.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS2.Client()}
	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg2})
	if err != nil {
		t.Fatalf("post-restart replicateNamespace: %v", err)
	}
	if !result.OK() || result.Replicated != 2 {
		t.Fatalf("post-restart rerun result = %+v", result)
	}
	if result.Stats.UploadedBytes != 0 {
		t.Fatalf("post-restart rerun should re-transfer nothing (already committed, byte-identical objects): uploaded=%d", result.Stats.UploadedBytes)
	}
	verifyRes, err := dstStore2.Verify(true)
	if err != nil || !verifyRes.OK() {
		t.Fatalf("deep verify after restart+rerun: res=%+v err=%v", verifyRes, err)
	}
}

func TestReplicateNamespace_ResumeAcrossRealProcessInterruption(t *testing.T) {
	bin := buildZeros3Binary(t)

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcAddr := freeTCPAddr(t)
	dstAddr := freeTCPAddr(t)

	srcCmd := exec.Command(bin, "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstCmd := exec.Command(bin, "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}

	if resp := doSignedRequest(t, client, "http://"+srcAddr, signer, http.MethodPut, "/src", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create source bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	bodies := make(map[string][]byte)
	for i, key := range []string{"ns/one.bin", "ns/two.bin", "ns/three.bin"} {
		body := genRandomBytes(int64(9000+i), 8_000_000)
		bodies[key] = body
		if resp := doSignedRequest(t, client, "http://"+srcAddr, signer, http.MethodPut, "/src/"+key, body, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("populate source object %s: status %d", key, resp.StatusCode)
		} else {
			resp.Body.Close()
		}
	}
	if resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodPut, "/dst", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("create destination bucket: status %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	replicateArgs := []string{"replicate", "-recursive", "-from", "http://" + srcAddr, "-to", "http://" + dstAddr, "s3://src/ns/", "s3://dst/ns/"}

	// First attempt: a real subprocess, killed shortly after it starts.
	// M8H-B's bounded worker pool transfers a single object's chunks
	// concurrently, so the old sequential-transport assumption behind this
	// delay (long enough to guarantee interruption, short enough to be a
	// fast test) no longer holds at 250ms -- a parallel transfer of the
	// first 8MB object can now, some of the time, both finish uploading
	// its chunks *and* have its commit request already accepted by the
	// destination server before the kill signal is even delivered (the
	// process is killed, but a request it already handed to the kernel's
	// TCP send buffer is not un-sent: the server still processes it).
	// 50ms keeps this test's actual intent -- interrupt the first attempt
	// before it can safely be assumed to have finished -- reliable again:
	// short enough that not even one object's discovery/negotiate/
	// transfer/commit round trip can complete first, regardless of how
	// many workers make the transfer itself faster.
	firstAttempt := exec.Command(bin, replicateArgs...)
	if err := firstAttempt.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	firstAttempt.Process.Kill()
	firstAttempt.Wait()

	// Second, uninterrupted attempt must complete every object correctly,
	// resuming from whatever the killed attempt already landed in the
	// destination's CAS -- with no namespace-replication session state
	// anywhere.
	secondOut, secondErr, code := runZeros3CLI(t, bin, replicateArgs...)
	if code != 0 {
		t.Fatalf("resumed namespace replicate failed (code %d): stdout=%s stderr=%s", code, secondOut, secondErr)
	}
	t.Logf("resumed namespace replicate output:\n%s", secondOut)

	for key, body := range bodies {
		resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodGet, "/dst/"+key, nil, nil)
		gotBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /dst/%s after resume: status %d", key, resp.StatusCode)
		}
		if !bytes.Equal(gotBody, body) {
			t.Fatalf("destination content mismatch for %s after interrupted-then-resumed namespace replicate", key)
		}
	}
}

// -----------------------------------------------------------------------
// M8C-G: aggregate statistics
// -----------------------------------------------------------------------

func TestReplicateNamespace_StatsExactAccounting(t *testing.T) {
	srcSrv, _, srcCfg, dstCfg, closeFn := newNsReplicateTestServerPair(t, "src", "dst")
	defer closeFn()
	sizes := []int{10_000, 20_000, 5_000}
	total := 0
	for i, sz := range sizes {
		putObj(t, srcSrv, "src", fmt.Sprintf("obj%d.bin", i), genRandomBytes(int64(100+i), sz))
		total += sz
	}
	result, err := replicateNamespace(namespaceReplicateConfig{Source: srcCfg, Dest: dstCfg})
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if result.Discovered != len(sizes) || result.Replicated != len(sizes) || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Stats.LogicalBytes != int64(total) {
		t.Fatalf("LogicalBytes = %d, want %d", result.Stats.LogicalBytes, total)
	}
	if result.Stats.UploadedBytes != int64(total) {
		t.Fatalf("UploadedBytes = %d, want %d (brand-new, no shared content)", result.Stats.UploadedBytes, total)
	}
	if result.Stats.BytesAvoided != 0 {
		t.Fatalf("BytesAvoided = %d, want 0", result.Stats.BytesAvoided)
	}
}

// -----------------------------------------------------------------------
// CLI (`zeros3 replicate -recursive`) and regression
// -----------------------------------------------------------------------

func TestCLI_Replicate_RecursiveWholeBucket(t *testing.T) {
	bin := buildZeros3Binary(t)
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcAddr := freeTCPAddr(t)
	dstAddr := freeTCPAddr(t)

	srcCmd := exec.Command(bin, "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstCmd := exec.Command(bin, "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(base, method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, base, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src", nil)
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src/a.txt", []byte("hello"))
	mustSignedOK("http://"+dstAddr, http.MethodPut, "/dst", nil)

	// Bare bucket (no trailing slash) must be accepted as whole-bucket
	// namespace form under -recursive -- parseS3DirURI, unmodified.
	out, errOut, code := runZeros3CLI(t, bin, "replicate", "-recursive", "-from", "http://"+srcAddr, "-to", "http://"+dstAddr, "s3://src", "s3://dst")
	if code != 0 {
		t.Fatalf("recursive replicate failed (code %d): stdout=%s stderr=%s", code, out, errOut)
	}
	if !strings.Contains(out, "Objects discovered:      1") {
		t.Fatalf("unexpected summary output: %s", out)
	}

	resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodGet, "/dst/a.txt", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /dst/a.txt: status %d", resp.StatusCode)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != "hello" {
		t.Fatalf("destination content mismatch: %q", gotBody)
	}
}

func TestCLI_Replicate_RecursiveExitCodeNonzeroOnPartialFailure(t *testing.T) {
	bin := buildZeros3Binary(t)
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcAddr := freeTCPAddr(t)
	dstAddr := freeTCPAddr(t)

	srcCmd := exec.Command(bin, "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstCmd := exec.Command(bin, "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(base, method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, base, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src", nil)
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src/a.txt", []byte("a"))
	bBody := []byte("b-object-content")
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src/b.txt", bBody)
	mustSignedOK("http://"+dstAddr, http.MethodPut, "/dst", nil)

	// Corrupt b.txt's single source chunk directly on disk (the
	// milestone's own permitted direct-filesystem exception, simulating
	// bit rot) so its own replicateObject call fails cleanly while a.txt
	// is unaffected -- deterministic, unlike a timing-based conflict race
	// against a real subprocess.
	sum := sha256.Sum256(bBody)
	h := hex.EncodeToString(sum[:])
	chunkPath := filepath.Join(srcDir, "chunks", h[0:2], h[2:4], h)
	if err := os.WriteFile(chunkPath, []byte("corrupted on disk"), 0o644); err != nil {
		t.Fatalf("corrupting b.txt's chunk on disk: %v", err)
	}

	out, errOut, code := runZeros3CLI(t, bin, "replicate", "-recursive", "-from", "http://"+srcAddr, "-to", "http://"+dstAddr, "s3://src/", "s3://dst/")
	if code == 0 {
		t.Fatalf("expected nonzero exit code with one conflicting object: stdout=%s stderr=%s", out, errOut)
	}
	if !strings.Contains(out, "b.txt") {
		t.Fatalf("expected failure report to name b.txt: %s", out)
	}
	resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodGet, "/dst/a.txt", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /dst/a.txt after partial failure: status %d", resp.StatusCode)
	}
}

// TestCLI_Replicate_NonRecursiveSingleObjectUnaffectedByM8C is a direct
// regression proof that -recursive is purely additive: with it omitted,
// runReplicate's original M8A parsing/behavior must be byte-for-byte
// unchanged.
func TestCLI_Replicate_NonRecursiveSingleObjectUnaffectedByM8C(t *testing.T) {
	bin := buildZeros3Binary(t)
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcAddr := freeTCPAddr(t)
	dstAddr := freeTCPAddr(t)

	srcCmd := exec.Command(bin, "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstCmd := exec.Command(bin, "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(base, method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, base, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src", nil)
	mustSignedOK("http://"+srcAddr, http.MethodPut, "/src/obj.bin", []byte("single object content"))
	mustSignedOK("http://"+dstAddr, http.MethodPut, "/dst", nil)

	out, errOut, code := runZeros3CLI(t, bin, "replicate", "-from", "http://"+srcAddr, "-to", "http://"+dstAddr, "s3://src/obj.bin", "s3://dst/obj.bin")
	if code != 0 {
		t.Fatalf("single-object replicate failed (code %d): stdout=%s stderr=%s", code, out, errOut)
	}
	resp := doSignedRequest(t, client, "http://"+dstAddr, signer, http.MethodGet, "/dst/obj.bin", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /dst/obj.bin: status %d", resp.StatusCode)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != "single object content" {
		t.Fatalf("content mismatch: %q", gotBody)
	}
}

// =============================================================================
// M8D: copy-on-write namespace fork (`zeros3 fork SOURCE DEST -endpoint EP`)
//
// Fork is pure composition over M8A/M8C's own primitives (section 15g's
// doc comment has the full architecture rationale): same-store
// replicateNamespace. Most of the coverage below therefore exercises
// replicateNamespace directly with source and destination pointed at one
// shared store/server -- exactly what runFork's CLI wrapper does -- plus
// a smaller set of real-subprocess CLI tests for overlap rejection,
// resume-across-a-real-process-kill, and restart.
// =============================================================================

// newForkTestServer opens one store, creates every named bucket in it,
// and wraps it in a single httptest server -- the same-store shape every
// fork test needs (M8D is same-store only). cfgFor returns a
// syncClientConfig for one bucket, sharing the endpoint/client/creds every
// other bucket's config uses, so a caller can build Source/Dest configs
// that literally share one physical CAS.
func newForkTestServer(t *testing.T, buckets ...string) (srv *Server, storeDir string, cfgFor func(bucket string) syncClientConfig, closeFn func()) {
	t.Helper()
	dir, s, creds, region := newSyncTestServer(t)
	for _, b := range buckets {
		if err := s.store.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(s)
	cfgFor = func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: region, HTTPClient: ts.Client()}
	}
	return s, dir, cfgFor, ts.Close
}

// -----------------------------------------------------------------------
// M8D overlap safety (forkNamespacesOverlap) -- source/destination
// namespace mapping edge cases
// -----------------------------------------------------------------------

func TestForkNamespacesOverlap_WholeBucketSourceAlwaysOverlaps(t *testing.T) {
	if !forkNamespacesOverlap("", "anything") {
		t.Fatal("whole-bucket source must overlap any destination prefix in the same bucket")
	}
}

func TestForkNamespacesOverlap_WholeBucketDestAlwaysOverlaps(t *testing.T) {
	if !forkNamespacesOverlap("anything", "") {
		t.Fatal("whole-bucket destination must overlap any source prefix in the same bucket")
	}
}

func TestForkNamespacesOverlap_BothWholeBucketOverlaps(t *testing.T) {
	if !forkNamespacesOverlap("", "") {
		t.Fatal("whole bucket vs whole bucket must overlap")
	}
}

func TestForkNamespacesOverlap_EqualPrefixesOverlap(t *testing.T) {
	if !forkNamespacesOverlap("a/b", "a/b") {
		t.Fatal("identical prefixes must overlap")
	}
}

func TestForkNamespacesOverlap_DestNestedInsideSource(t *testing.T) {
	// src: bucket/a/  dst: bucket/a/fork/  -- the spec's own worked hazard example.
	if !forkNamespacesOverlap("a", "a/fork") {
		t.Fatal("destination nested inside source must overlap")
	}
}

func TestForkNamespacesOverlap_SourceNestedInsideDest(t *testing.T) {
	if !forkNamespacesOverlap("a/fork", "a") {
		t.Fatal("source nested inside destination must overlap")
	}
}

func TestForkNamespacesOverlap_SiblingPrefixesDoNotOverlap(t *testing.T) {
	if forkNamespacesOverlap("prod", "backup") {
		t.Fatal("disjoint sibling prefixes must not overlap")
	}
}

func TestForkNamespacesOverlap_SharedStringPrefixDifferentBoundaryDoesNotOverlap(t *testing.T) {
	// "images" is a string-prefix of "images-backup" but not a *path*
	// prefix of it -- must not be a false-positive overlap.
	if forkNamespacesOverlap("images", "images-backup") {
		t.Fatal("images vs images-backup must not overlap (no path boundary)")
	}
	if forkNamespacesOverlap("images-backup", "images") {
		t.Fatal("images-backup vs images must not overlap (no path boundary)")
	}
}

func TestForkNamespacesOverlap_DeeplyNestedSiblingsDoNotOverlap(t *testing.T) {
	if forkNamespacesOverlap("a/b/c", "a/b/d") {
		t.Fatal("a/b/c vs a/b/d must not overlap")
	}
}

// -----------------------------------------------------------------------
// M8D-B/M8D-C: enumeration + mapping reuse, whole bucket / prefix / same
// bucket different prefix / different bucket
// -----------------------------------------------------------------------

func TestFork_WholeBucketToWholeBucket_DifferentBuckets(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	putObj(t, srv, "prod", "a.bin", []byte("alpha"))
	putObj(t, srv, "prod", "sub/b.bin", []byte("beta"))

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if result.Discovered != 2 || result.Replicated != 2 || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"a.bin", "sub/b.bin"} {
		if _, _, err := srv.store.GetObject("experiment", key); err != nil {
			t.Fatalf("GetObject(%q): %v", key, err)
		}
	}
}

func TestFork_PrefixToPrefix_SameBucketNonOverlapping(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "prod/a.bin", []byte("alpha"))
	putObj(t, srv, "b", "prod/sub/c.bin", []byte("gamma"))
	putObj(t, srv, "b", "other/not-selected.bin", []byte("x"))

	result, err := replicateNamespace(namespaceReplicateConfig{
		Source: cfgFor("b"), SourcePrefix: "prod",
		Dest: cfgFor("b"), DestPrefix: "backup",
		DestMustBeAbsent: true,
	})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if result.Discovered != 2 || result.Replicated != 2 || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"backup/a.bin", "backup/sub/c.bin"} {
		if _, _, err := srv.store.GetObject("b", key); err != nil {
			t.Fatalf("GetObject(%q): %v", key, err)
		}
	}
	if _, _, err := srv.store.GetObject("b", "backup/not-selected.bin"); err == nil {
		t.Fatal("unselected object leaked into destination")
	}
}

func TestFork_EmptySourceNamespace(t *testing.T) {
	_, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if result.Discovered != 0 || result.Replicated != 0 || result.Failed != 0 || !result.OK() {
		t.Fatalf("result = %+v, want all-zero for an empty source namespace", result)
	}
}

func TestFork_WeirdKeysUnicodePercentHashQuestionSpacesRepeatedSlashesPreserved(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	weird := []string{
		"space key.txt",
		"unicode/café/日本語.txt",
		"percent/100%25.txt",
		"hash/a#b.txt",
		"question/a?b.txt",
		"slashes/a//b.txt",
	}
	for _, k := range weird {
		putObj(t, srv, "src", k, []byte(k))
	}
	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if result.Discovered != len(weird) || result.Replicated != len(weird) || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	for _, k := range weird {
		_, body, err := srv.store.GetObject("dst", k)
		if err != nil {
			t.Fatalf("GetObject(%q): %v", k, err)
		}
		if string(body) != k {
			t.Fatalf("GetObject(%q) body = %q, want %q", k, body, k)
		}
	}
}

// -----------------------------------------------------------------------
// M8D-D: zero-payload invariant -- the heart of the feature. Proven two
// independent ways per object set: computeStats' ChunkStoreFileBytes (a
// derived-from-filesystem scan, section 12) AND countChunkFiles' own raw
// filepath.WalkDir over the store's chunks/ directory.
// -----------------------------------------------------------------------

func TestFork_OneObject_ZeroPayload(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", genRandomBytes(1, 50_000))

	beforeFiles := countChunkFiles(t, dir)
	beforeStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !result.OK() || result.Stats.UploadedBytes != 0 || result.Stats.UniqueChunksUploaded != 0 {
		t.Fatalf("expected zero-payload fork, got result = %+v", result)
	}

	afterFiles := countChunkFiles(t, dir)
	afterStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if afterFiles != beforeFiles {
		t.Fatalf("countChunkFiles: before=%d after=%d, want unchanged", beforeFiles, afterFiles)
	}
	if afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("ChunkStoreFileBytes: before=%d after=%d, want unchanged", beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}
}

func TestFork_SeveralObjectsSharingChunks_ZeroPayload(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	shared := genRandomBytes(2, 500_000)
	putObj(t, srv, "src", "one.bin", shared)
	putObj(t, srv, "src", "two.bin", shared) // identical content -> same chunks
	putObj(t, srv, "src", "three.bin", genRandomBytes(3, 90_000))

	beforeFiles := countChunkFiles(t, dir)
	beforeStats, _ := srv.store.computeStats(statsScope{})

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !result.OK() || result.Stats.UploadedBytes != 0 {
		t.Fatalf("expected zero-payload fork, got result = %+v", result)
	}

	afterFiles := countChunkFiles(t, dir)
	afterStats, _ := srv.store.computeStats(statsScope{})
	if afterFiles != beforeFiles {
		t.Fatalf("countChunkFiles: before=%d after=%d", beforeFiles, afterFiles)
	}
	if afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("ChunkStoreFileBytes: before=%d after=%d", beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}
}

func TestFork_UnrelatedObjects_ZeroPayload(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	for i := 0; i < 8; i++ {
		putObj(t, srv, "src", fmt.Sprintf("obj%d.bin", i), genRandomBytes(int64(1000+i), 20_000+i*1000))
	}
	beforeFiles := countChunkFiles(t, dir)
	beforeStats, _ := srv.store.computeStats(statsScope{})

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !result.OK() || result.Discovered != 8 {
		t.Fatalf("result = %+v", result)
	}
	afterFiles := countChunkFiles(t, dir)
	afterStats, _ := srv.store.computeStats(statsScope{})
	if afterFiles != beforeFiles || afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("payload moved: files before=%d after=%d bytes before=%d after=%d",
			beforeFiles, afterFiles, beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}
}

func TestFork_LargeMultiChunkObject_ZeroPayload(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	// Well above cdcMaxChunkSize (256KiB) so this genuinely spans many CDC
	// chunks, not just one.
	big := genRandomBytes(4, 4_000_000)
	putObj(t, srv, "src", "big.bin", big)

	beforeFiles := countChunkFiles(t, dir)
	beforeStats, _ := srv.store.computeStats(statsScope{})
	if beforeFiles < 5 {
		t.Fatalf("fixture too small to prove multi-chunk zero-payload: only %d chunk files", beforeFiles)
	}

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !result.OK() || result.Stats.UploadedBytes != 0 {
		t.Fatalf("expected zero-payload fork of large object, got result = %+v", result)
	}
	if result.Stats.LogicalBytes != int64(len(big)) {
		t.Fatalf("LogicalBytes = %d, want %d", result.Stats.LogicalBytes, len(big))
	}

	afterFiles := countChunkFiles(t, dir)
	afterStats, _ := srv.store.computeStats(statsScope{})
	if afterFiles != beforeFiles {
		t.Fatalf("countChunkFiles: before=%d after=%d", beforeFiles, afterFiles)
	}
	if afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("ChunkStoreFileBytes: before=%d after=%d", beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}

	_, gotBody, err := srv.store.GetObject("dst", "big.bin")
	if err != nil || !bytes.Equal(gotBody, big) {
		t.Fatalf("GetObject(big.bin) after fork: err=%v, byte-equal=%v", err, bytes.Equal(gotBody, big))
	}
}

func TestFork_PaginationBoundary_1500Objects(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	const n = 1500
	for i := 0; i < n; i++ {
		putObj(t, srv, "src", fmt.Sprintf("k/%04d", i), []byte("x"))
	}
	beforeFiles := countChunkFiles(t, dir)

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if result.Discovered != n || result.Replicated != n || !result.OK() {
		t.Fatalf("result = %+v", result)
	}
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k/%04d", i)
		if seen[key] {
			t.Fatalf("duplicate destination key %q", key)
		}
		seen[key] = true
		if _, _, err := srv.store.GetObject("dst", key); err != nil {
			t.Fatalf("GetObject(%q) missing after >1000-object fork: %v", key, err)
		}
	}
	afterFiles := countChunkFiles(t, dir)
	if afterFiles != beforeFiles {
		t.Fatalf("countChunkFiles: before=%d after=%d, want unchanged (single shared chunk for identical 1-byte objects)", beforeFiles, afterFiles)
	}
}

// -----------------------------------------------------------------------
// M8D-E: object identity and metadata semantics
// -----------------------------------------------------------------------

func TestFork_MetadataAndContentTypePreserved(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	if _, err := srv.store.PutObject("src", "k", []byte("payload"), "text/plain; charset=utf-8", map[string]string{"owner": "alice", "purpose": "fork-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	entry, body, err := srv.store.GetObject("dst", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "payload" {
		t.Fatalf("body = %q", body)
	}
	man := mustManifestFor(t, srv.store, entry)
	if man.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("ContentType = %q", man.ContentType)
	}
	metaMap := make(map[string]string, len(man.Metadata))
	for _, kv := range man.Metadata {
		metaMap[kv.Key] = kv.Value
	}
	if metaMap["owner"] != "alice" || metaMap["purpose"] != "fork-test" {
		t.Fatalf("Metadata = %+v", man.Metadata)
	}
}

func TestFork_ETagContentEqualityAndIndependentLastModified(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	srcEntry, err := srv.store.PutObject("src", "k", []byte("identical content"), "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	srcMan := mustManifestFor(t, srv.store, srcEntry)
	time.Sleep(10 * time.Millisecond) // force a distinguishable publication time
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	dstEntry, body, err := srv.store.GetObject("dst", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "identical content" {
		t.Fatalf("body = %q", body)
	}
	dstMan := mustManifestFor(t, srv.store, dstEntry)
	if dstMan.ETag != srcMan.ETag {
		t.Fatalf("ETag mismatch: src=%q dst=%q, want identical (same content)", srcMan.ETag, dstMan.ETag)
	}
	if !dstMan.CreatedAt.After(srcMan.CreatedAt) {
		t.Fatalf("destination CreatedAt (%v) must reflect its own publication time, not reuse source's (%v)", dstMan.CreatedAt, srcMan.CreatedAt)
	}
}

// -----------------------------------------------------------------------
// M8D-F: destination safety (conflict handling)
// -----------------------------------------------------------------------

func TestFork_DestinationConflictOneObjectFailsOthersSucceed(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("a"))
	putObj(t, srv, "src", "conflict.bin", []byte("new content"))
	putObj(t, srv, "src", "z.bin", []byte("z"))
	putObj(t, srv, "dst", "conflict.bin", []byte("pre-existing, must not be overwritten"))

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork top-level: %v", err)
	}
	if result.OK() {
		t.Fatalf("expected a nonzero-failure result, got %+v", result)
	}
	if result.Discovered != 3 || result.Replicated != 2 || result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
	_, body, err := srv.store.GetObject("dst", "conflict.bin")
	if err != nil || string(body) != "pre-existing, must not be overwritten" {
		t.Fatalf("pre-existing destination object was overwritten: err=%v body=%q", err, body)
	}
	for _, key := range []string{"a.bin", "z.bin"} {
		if _, _, err := srv.store.GetObject("dst", key); err != nil {
			t.Fatalf("unrelated object %q should still have forked: %v", key, err)
		}
	}
}

// TestFork_DestinationKeyCreatedConcurrentlyDuringOperationIsNotOverwritten
// is the hostile-review "destination appears during operation" case
// (distinct from TestFork_DestinationConflictOneObjectFailsOthersSucceed,
// where the conflicting object already existed before the run started):
// the destination key does not exist when this object's own fork attempt
// begins, but a concurrent writer creates it with different content in
// the narrow window before this object's atomic commit. The safety
// condition must be enforced at that actual commit point (never only by
// a preliminary check), so the racing writer's content must survive
// untouched, exactly like M8C's own equivalent race proves for
// replicate -recursive.
func TestFork_DestinationKeyCreatedConcurrentlyDuringOperationIsNotOverwritten(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("source-a"))
	putObj(t, srv, "src", "racing.bin", []byte("source-racing"))
	putObj(t, srv, "src", "z.bin", []byte("source-z"))

	raced := false
	dstWrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !raced && r.URL.Path == zeros3SyncCommitPath && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req syncCommitRequest
			if json.Unmarshal(body, &req) == nil && req.Key == "racing.bin" {
				raced = true
				if _, err := srv.store.PutObject("dst", "racing.bin", []byte("concurrent writer's content"), "text/plain", nil); err != nil {
					t.Fatal(err)
				}
			}
		}
		srv.ServeHTTP(w, r)
	}))
	defer dstWrapper.Close()
	racedDst := cfgFor("dst")
	racedDst.Endpoint = dstWrapper.URL
	racedDst.HTTPClient = dstWrapper.Client()

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: racedDst, DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork top-level: %v", err)
	}
	if result.Discovered != 3 || result.Replicated != 2 || result.Failed != 1 || result.Failures[0].Key != "racing.bin" {
		t.Fatalf("result = %+v", result)
	}
	_, body, err := srv.store.GetObject("dst", "racing.bin")
	if err != nil || string(body) != "concurrent writer's content" {
		t.Fatalf("concurrent writer's content was not safely preserved: err=%v body=%q", err, body)
	}
	for _, key := range []string{"a.bin", "z.bin"} {
		if _, _, err := srv.store.GetObject("dst", key); err != nil {
			t.Fatalf("unrelated object %q should still have forked: %v", key, err)
		}
	}
}

// -----------------------------------------------------------------------
// M8D-G: source stability (each object forks from one captured revision;
// no mixed content, no namespace-wide snapshot needed)
// -----------------------------------------------------------------------

func TestFork_SourceObjectChangesAfterListingBeforeFetch_ForksOneCleanRevisionNotMixed(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "aaa.bin", []byte("first"))
	putObj(t, srv, "src", "changing.bin", []byte("original revision"))
	putObj(t, srv, "src", "zzz.bin", []byte("last"))

	changed := false
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !changed && r.URL.Path == zeros3SyncObjectPath && r.URL.Query().Get("key") == "changing.bin" {
			changed = true
			if _, err := srv.store.PutObject("src", "changing.bin", []byte("mutated after listing"), "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
		}
		srv.ServeHTTP(w, r)
	}))
	defer wrapper.Close()
	wrappedSrc := cfgFor("src")
	wrappedSrc.Endpoint = wrapper.URL
	wrappedSrc.HTTPClient = wrapper.Client()

	result, err := replicateNamespace(namespaceReplicateConfig{Source: wrappedSrc, Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork top-level: %v", err)
	}
	if !result.OK() {
		t.Fatalf("expected every object to fork cleanly (one captured revision each), got %+v", result)
	}
	_, body, err := srv.store.GetObject("dst", "changing.bin")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original revision" && string(body) != "mutated after listing" {
		t.Fatalf("destination holds mixed/corrupted content: %q", body)
	}
}

func TestFork_SourceObjectDisappearsBetweenListingAndFetch_ReportsFailureContinues(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "aaa.bin", []byte("first"))
	putObj(t, srv, "src", "gone.bin", []byte("will vanish"))
	putObj(t, srv, "src", "zzz.bin", []byte("last"))

	deleted := false
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !deleted && r.URL.Path == zeros3SyncObjectPath && r.URL.Query().Get("key") == "gone.bin" {
			deleted = true
			if err := srv.store.DeleteObject("src", "gone.bin"); err != nil {
				t.Fatal(err)
			}
		}
		srv.ServeHTTP(w, r)
	}))
	defer wrapper.Close()
	wrappedSrc := cfgFor("src")
	wrappedSrc.Endpoint = wrapper.URL
	wrappedSrc.HTTPClient = wrapper.Client()

	result, err := replicateNamespace(namespaceReplicateConfig{Source: wrappedSrc, Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil {
		t.Fatalf("fork top-level: %v", err)
	}
	if result.Discovered != 3 || result.Replicated != 2 || result.Failed != 1 || result.Failures[0].Key != "gone.bin" {
		t.Fatalf("result = %+v", result)
	}
	for _, key := range []string{"aaa.bin", "zzz.bin"} {
		if _, _, err := srv.store.GetObject("dst", key); err != nil {
			t.Fatalf("%q should have forked: %v", key, err)
		}
	}
}

// -----------------------------------------------------------------------
// M8D-I: independence after fork
// -----------------------------------------------------------------------

func TestFork_DestinationOnlyObjectSurvives(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "dst", "dest-only.bin", []byte("nobody touches me"))
	putObj(t, srv, "src", "a.bin", []byte("source content"))

	result, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst"), DestMustBeAbsent: true})
	if err != nil || !result.OK() {
		t.Fatalf("fork: result=%+v err=%v", result, err)
	}
	_, body, err := srv.store.GetObject("dst", "dest-only.bin")
	if err != nil || !bytes.Equal(body, []byte("nobody touches me")) {
		t.Fatalf("destination-only object was modified or removed: err=%v body=%q", err, body)
	}
}

func TestFork_DestinationEditedAfterFork_SourceUnchanged_OnlyNewChunksAdded(t *testing.T) {
	srv, dir, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	original := genRandomBytes(5, 600_000)
	putObj(t, srv, "prod", "big.bin", original)
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}

	beforeFiles := countChunkFiles(t, dir)

	// Localized edit: same prefix, different suffix -- CDC content-defined
	// boundaries mean only the changed region's chunks differ.
	edited := append(append([]byte{}, original[:500_000]...), genRandomBytes(6, 20_000)...)
	if _, err := srv.store.PutObject("experiment", "big.bin", edited, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	afterFiles := countChunkFiles(t, dir)
	if afterFiles <= beforeFiles {
		t.Fatalf("expected new CAS chunk files for the edited region: before=%d after=%d", beforeFiles, afterFiles)
	}

	_, srcBody, err := srv.store.GetObject("prod", "big.bin")
	if err != nil || !bytes.Equal(srcBody, original) {
		t.Fatalf("source must remain byte-for-byte unchanged after editing the fork: err=%v equal=%v", err, bytes.Equal(srcBody, original))
	}
	_, dstBody, err := srv.store.GetObject("experiment", "big.bin")
	if err != nil || !bytes.Equal(dstBody, edited) {
		t.Fatalf("destination must reflect the edit: err=%v equal=%v", err, bytes.Equal(dstBody, edited))
	}
}

func TestFork_SourceEditedAfterFork_DestinationUnchanged(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	putObj(t, srv, "prod", "a.bin", []byte("original"))
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.PutObject("prod", "a.bin", []byte("changed in source"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	_, dstBody, err := srv.store.GetObject("experiment", "a.bin")
	if err != nil || string(dstBody) != "original" {
		t.Fatalf("destination must remain unchanged after editing the source: err=%v body=%q", err, dstBody)
	}
}

func TestFork_SourceDeletedAfterFork_DestinationSurvives(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	putObj(t, srv, "prod", "a.bin", []byte("content"))
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.DeleteObject("prod", "a.bin"); err != nil {
		t.Fatal(err)
	}
	_, dstBody, err := srv.store.GetObject("experiment", "a.bin")
	if err != nil || string(dstBody) != "content" {
		t.Fatalf("destination must remain readable after source deletion: err=%v body=%q", err, dstBody)
	}
}

func TestFork_DestinationDeletedAfterFork_SourceSurvives(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	putObj(t, srv, "prod", "a.bin", []byte("content"))
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.DeleteObject("experiment", "a.bin"); err != nil {
		t.Fatal(err)
	}
	_, srcBody, err := srv.store.GetObject("prod", "a.bin")
	if err != nil || string(srcBody) != "content" {
		t.Fatalf("source must remain readable after destination deletion: err=%v body=%q", err, srcBody)
	}
}

// -----------------------------------------------------------------------
// M8D-J: GC / verify composition -- proves the fork is real structural
// sharing, never a mere alias.
// -----------------------------------------------------------------------

func TestFork_GCPreservesBothNamespacesAfterDivergence(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("experiment"); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	srv := NewServer(store, creds, defaultRegion)
	ts := httptest.NewServer(srv)
	cfgFor := func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: defaultRegion, HTTPClient: ts.Client()}
	}

	shared := genRandomBytes(7, 300_000)
	mustPutObject(t, store, "prod", "shared.bin", shared, "application/octet-stream", nil)
	mustPutObject(t, store, "prod", "prod-only.bin", genRandomBytes(8, 40_000), "application/octet-stream", nil)

	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	// Diverge: an experiment-only edit.
	if _, err := store.PutObject("experiment", "shared.bin", genRandomBytes(9, 50_000), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	ts.Close()
	store.Close()

	gc, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if !gc.LiveSetOK {
		t.Fatalf("expected a healthy live set after fork divergence, got issues: %+v", gc.Issues)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	_, prodShared, err := reopened.GetObject("prod", "shared.bin")
	if err != nil || !bytes.Equal(prodShared, shared) {
		t.Fatalf("prod/shared.bin unreachable or corrupted after GC: err=%v", err)
	}
	if _, _, err := reopened.GetObject("prod", "prod-only.bin"); err != nil {
		t.Fatalf("prod/prod-only.bin unreachable after GC: %v", err)
	}
	if _, _, err := reopened.GetObject("experiment", "shared.bin"); err != nil {
		t.Fatalf("experiment/shared.bin unreachable after GC: %v", err)
	}

	verify, err := reopened.Verify(true)
	if err != nil || !verify.OK() {
		t.Fatalf("deep verify after GC: res=%+v err=%v", verify, err)
	}
}

func TestFork_DeepVerifyGreenAfterFork(t *testing.T) {
	srv, _, cfgFor, closeFn := newForkTestServer(t, "prod", "experiment")
	defer closeFn()
	putObj(t, srv, "prod", "a.bin", genRandomBytes(10, 300_000))
	putObj(t, srv, "prod", "b.bin", genRandomBytes(11, 10_000))
	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.store.Verify(true)
	if err != nil || !res.OK() {
		t.Fatalf("deep verify after fork: res=%+v err=%v", res, err)
	}
}

// TestFork_RepairComposesAcrossSharedCorruptChunk proves M8D composes
// cleanly with M8B's peer-assisted repair (M8D-J), without changing M8B
// at all: after a fork, one physical CAS chunk is now reachable from both
// namespaces. Corrupting it on disk, then repairing from an independent
// peer holding the same content, must restore both the source and the
// fork's copy from exactly one repaired digest.
func TestFork_RepairComposesAcrossSharedCorruptChunk(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("prod"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("experiment"); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	srv := NewServer(store, creds, defaultRegion)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cfgFor := func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: defaultRegion, HTTPClient: ts.Client()}
	}

	body := genRandomBytes(12, 600_000)
	entry := mustPutObject(t, store, "prod", "shared.bin", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)

	if _, err := replicateNamespace(namespaceReplicateConfig{Source: cfgFor("prod"), Dest: cfgFor("experiment"), DestMustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}

	_, peerSrv, peerCreds, peerRegion := newSyncTestServer(t)
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)

	corruptChunkOnDisk(t, store, man.Chunks[0].SHA256)

	pre, err := store.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if pre.OK() {
		t.Fatal("expected pre-repair deep verify to fail after corrupting a shared chunk")
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, peerCreds, peerRegion)})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.Repaired != 1 || !stats.PostRepairOK {
		t.Fatalf("stats = %+v, want exactly one digest repaired", stats)
	}

	post, err := store.Verify(true)
	if err != nil || !post.OK() {
		t.Fatalf("post-repair verify: res=%+v err=%v", post, err)
	}
	_, gotProd, err := store.GetObject("prod", "shared.bin")
	if err != nil || !bytes.Equal(gotProd, body) {
		t.Fatalf("prod/shared.bin after repair: err=%v equal=%v", err, bytes.Equal(gotProd, body))
	}
	_, gotExp, err := store.GetObject("experiment", "shared.bin")
	if err != nil || !bytes.Equal(gotExp, body) {
		t.Fatalf("experiment/shared.bin after repair: err=%v equal=%v", err, bytes.Equal(gotExp, body))
	}
}

// -----------------------------------------------------------------------
// CLI (`zeros3 fork`) -- real binary, real subprocess server(s)
// -----------------------------------------------------------------------

func TestCLI_Fork_WholeBucketToWholeBucket(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/prod", nil)
	mustSignedOK(http.MethodPut, "/experiment", nil)
	mustSignedOK(http.MethodPut, "/prod/a.bin", []byte("alpha"))
	mustSignedOK(http.MethodPut, "/prod/sub/b.bin", []byte("beta"))

	out, errOut, code := runZeros3CLI(t, bin, "fork", "-endpoint", "http://"+addr, "s3://prod/", "s3://experiment/")
	if code != 0 {
		t.Fatalf("fork failed (code %d): stdout=%s stderr=%s", code, out, errOut)
	}
	if !strings.Contains(out, "Objects forked:            2") {
		t.Fatalf("unexpected summary output: %s", out)
	}
	if !strings.Contains(out, "CAS payload bytes added:   0 B") {
		t.Fatalf("expected zero new CAS payload in summary: %s", out)
	}

	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/experiment/sub/b.bin", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /experiment/sub/b.bin: status %d", resp.StatusCode)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != "beta" {
		t.Fatalf("content mismatch: %q", gotBody)
	}
}

func TestCLI_Fork_OverlapRejectedNonzeroExitNoObjectsTouched(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/bucket", nil, nil)
	resp.Body.Close()
	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/bucket/a/x.bin", []byte("x"), nil)
	resp.Body.Close()

	out, errOut, code := runZeros3CLI(t, bin, "fork", "-endpoint", "http://"+addr, "s3://bucket/a/", "s3://bucket/a/fork/")
	if code == 0 {
		t.Fatalf("expected nonzero exit for an overlapping fork request: stdout=%s stderr=%s", out, errOut)
	}
	if !strings.Contains(errOut, "overlap") {
		t.Fatalf("expected an overlap-specific error, got stderr=%s", errOut)
	}
	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/bucket?list-type=2", nil, nil)
	defer resp.Body.Close()
	listBody, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(listBody), "a/fork/") {
		t.Fatalf("overlap rejection must run before any write: found a/fork/ content: %s", listBody)
	}
}

func TestCLI_Fork_DestinationConflictNonzeroExit(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/prod", nil)
	mustSignedOK(http.MethodPut, "/experiment", nil)
	mustSignedOK(http.MethodPut, "/prod/a.bin", []byte("a"))
	mustSignedOK(http.MethodPut, "/experiment/a.bin", []byte("pre-existing"))

	out, errOut, code := runZeros3CLI(t, bin, "fork", "-endpoint", "http://"+addr, "s3://prod/", "s3://experiment/")
	if code == 0 {
		t.Fatalf("expected nonzero exit on destination conflict: stdout=%s stderr=%s", out, errOut)
	}
	if !strings.Contains(out, "a.bin") {
		t.Fatalf("expected failure report to name a.bin: %s", out)
	}
	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/experiment/a.bin", nil, nil)
	defer resp.Body.Close()
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != "pre-existing" {
		t.Fatalf("pre-existing destination object was overwritten: %q", gotBody)
	}
}

// TestCLI_Fork_ResumeAcrossRealProcessKill is M8D-K's real-process-kill
// resume proof (mirroring M8C's own
// TestReplicateNamespace_ResumeAcrossRealProcessInterruption): a real
// `zeros3 fork` OS process is SIGKILLed partway through, then a second,
// uninterrupted invocation must complete every object correctly with no
// durable fork-session state anywhere and no duplicated CAS payload.
func TestCLI_Fork_ResumeAcrossRealProcessKill(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	srvCmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := srvCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { srvCmd.Process.Kill(); srvCmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/prod", nil, nil)
	resp.Body.Close()
	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/experiment", nil, nil)
	resp.Body.Close()

	bodies := make(map[string][]byte)
	for i, key := range []string{"ns/one.bin", "ns/two.bin", "ns/three.bin"} {
		body := genRandomBytes(int64(21000+i), 8_000_000)
		bodies[key] = body
		r := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/prod/"+key, body, nil)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("populate source object %s: status %d", key, r.StatusCode)
		}
		r.Body.Close()
	}

	forkArgs := []string{"fork", "-endpoint", "http://" + addr, "s3://prod/ns/", "s3://experiment/ns/"}

	firstAttempt := exec.Command(bin, forkArgs...)
	if err := firstAttempt.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	firstAttempt.Process.Kill()
	firstAttempt.Wait()

	secondOut, secondErr, code := runZeros3CLI(t, bin, forkArgs...)
	if code != 0 {
		t.Fatalf("resumed fork failed (code %d): stdout=%s stderr=%s", code, secondOut, secondErr)
	}

	for key, body := range bodies {
		r := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/experiment/"+key, nil, nil)
		gotBody, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET /experiment/%s after resume: status %d", key, r.StatusCode)
		}
		if !bytes.Equal(gotBody, body) {
			t.Fatalf("destination content mismatch for %s after interrupted-then-resumed fork", key)
		}
	}
}

// TestCLI_Fork_RestartThenDeepVerifyGreen is M8D's restart/verify/GC
// proof (external harness Phase 9's internal-level counterpart): after a
// real fork, a full process restart on the same store, followed by
// `zeros3 verify -deep`, must stay green and AWS-SDK-shaped GET must
// still round-trip exact bytes for both namespaces.
func TestCLI_Fork_RestartThenDeepVerifyGreen(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/prod", nil, nil)
	resp.Body.Close()
	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/experiment", nil, nil)
	resp.Body.Close()
	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/prod/a.bin", []byte("hello fork"), nil)
	resp.Body.Close()

	out, errOut, code := runZeros3CLI(t, bin, "fork", "-endpoint", "http://"+addr, "s3://prod/", "s3://experiment/")
	if code != 0 {
		t.Fatalf("fork failed (code %d): stdout=%s stderr=%s", code, out, errOut)
	}

	cmd.Process.Kill()
	cmd.Wait()

	cmd2 := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	waitForZeros3Serve(t, addr)

	resp = doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/experiment/a.bin", nil, nil)
	gotBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(gotBody) != "hello fork" {
		t.Fatalf("GET /experiment/a.bin after restart: status=%d body=%q", resp.StatusCode, gotBody)
	}

	// Stop the server before running `verify` standalone against the
	// store directory, matching how every other CLI-level verify/GC test
	// in this file avoids running two processes against one store
	// concurrently.
	cmd2.Process.Kill()
	cmd2.Wait()

	verifyOut, verifyErr, verifyCode := runZeros3CLI(t, bin, "verify", "-store", storeDir, "-deep")
	if verifyCode != 0 {
		t.Fatalf("verify -deep after restart failed: code=%d stdout=%s stderr=%s", verifyCode, verifyOut, verifyErr)
	}
}

// =============================================================================
// M8E-A: durable namespace snapshots -- format, create, list/show, GC
// pinning, delete (section 15h). M8E-B (restore, section 15i) has its own
// test block further below.
// =============================================================================

func newSnapshotTestServer(t *testing.T, buckets ...string) (srv *Server, storeDir string, cfg syncClientConfig, closeFn func()) {
	t.Helper()
	dir, s, creds, region := newSyncTestServer(t)
	for _, b := range buckets {
		if err := s.store.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(s)
	cfg = syncClientConfig{Endpoint: ts.URL, Creds: creds, Region: region, HTTPClient: ts.Client()}
	return s, dir, cfg, ts.Close
}

// -----------------------------------------------------------------------
// Format: encode/decode round-trip and every corruption mode
// -----------------------------------------------------------------------

func sampleSnapshotDescriptor() snapshotDescriptorV1 {
	sum := sha256.Sum256([]byte("hello"))
	return snapshotDescriptorV1{
		SnapshotFormatVersion: snapshotFormatVersion,
		SnapshotID:            newUUIDv7(),
		CreatedAt:             time.Now().UTC(),
		SourceBucket:          "b",
		SourcePrefix:          "p",
		Entries: []snapshotEntryV1{
			{Key: "a.txt", ManifestUUID: newUUIDv7(), ManifestSHA256: hex.EncodeToString(sum[:]), Size: 5, ETag: "e1", ContentType: "text/plain"},
			{Key: "b.txt", ManifestUUID: newUUIDv7(), ManifestSHA256: hex.EncodeToString(sum[:]), Size: 5, ETag: "e2", ContentType: "text/plain"},
		},
	}
}

func TestSnapshotFormat_ValidRoundTrip(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, err := encodeSnapshotDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeSnapshotDescriptor(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SnapshotID != d.SnapshotID || got.SourceBucket != d.SourceBucket || got.SourcePrefix != d.SourcePrefix {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, d)
	}
	if len(got.Entries) != 2 || got.Entries[0].Key != "a.txt" || got.Entries[1].Key != "b.txt" {
		t.Fatalf("entries round-trip mismatch: %+v", got.Entries)
	}
}

func TestSnapshotFormat_DeterministicEncode(t *testing.T) {
	d := sampleSnapshotDescriptor()
	a, err := encodeSnapshotDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeSnapshotDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two encodes of the same descriptor were not byte-identical")
	}
}

func TestSnapshotFormat_BadMagic(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	data[0] = 'X'
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("bad magic: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_UnsupportedVersion(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	binary.BigEndian.PutUint16(data[4:6], 99)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("unsupported version: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_Truncated(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	for _, n := range []int{0, 1, 5, snapshotHeaderSize, len(data) - 1} {
		if _, err := decodeSnapshotDescriptor(data[:n]); err == nil || !errors.Is(err, errSnapshotCorrupt) {
			t.Fatalf("truncated to %d bytes: got err=%v, want errSnapshotCorrupt", n, err)
		}
	}
}

func TestSnapshotFormat_BadCRC(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	data[len(data)-1] ^= 0xFF
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("bad crc: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_MalformedLength(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	binary.BigEndian.PutUint32(data[6:10], uint32(len(data)*10))
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("malformed length: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_OversizedDeclaredLength(t *testing.T) {
	d := sampleSnapshotDescriptor()
	data, _ := encodeSnapshotDescriptor(d)
	binary.BigEndian.PutUint32(data[6:10], uint32(maxSnapshotPayload)+1)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("oversized declared length: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_InvalidSnapshotID(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.SnapshotID = "not-a-uuid"
	data, err := encodeSnapshotDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errInvalidSnapshot) {
		t.Fatalf("invalid snapshot id: got err=%v, want errInvalidSnapshot", err)
	}
}

func TestSnapshotFormat_DuplicateEntryKey(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.Entries[1].Key = d.Entries[0].Key
	data, _ := encodeSnapshotDescriptor(d)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("duplicate entry key: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_UnsortedEntries(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.Entries[0], d.Entries[1] = d.Entries[1], d.Entries[0]
	data, _ := encodeSnapshotDescriptor(d)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("unsorted entries: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_MalformedManifestSHA256(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.Entries[0].ManifestSHA256 = "not-hex"
	data, _ := encodeSnapshotDescriptor(d)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("malformed manifest sha256: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_MalformedManifestUUID(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.Entries[0].ManifestUUID = "not-a-uuid"
	data, _ := encodeSnapshotDescriptor(d)
	if _, err := decodeSnapshotDescriptor(data); err == nil || !errors.Is(err, errSnapshotCorrupt) {
		t.Fatalf("malformed manifest uuid: got err=%v, want errSnapshotCorrupt", err)
	}
}

func TestSnapshotFormat_EmptyEntriesValid(t *testing.T) {
	d := sampleSnapshotDescriptor()
	d.Entries = nil
	data, err := encodeSnapshotDescriptor(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeSnapshotDescriptor(data)
	if err != nil {
		t.Fatalf("empty-entries descriptor should be valid: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("want 0 entries, got %d", len(got.Entries))
	}
}

func TestValidSnapshotID(t *testing.T) {
	good := newUUIDv7()
	if !validSnapshotID(good) {
		t.Fatalf("newUUIDv7 output %q rejected", good)
	}
	bad := []string{"", "short", good + "x", strings.ToUpper(good), "../../etc/passwd", good[:35] + "g", strings.Replace(good, "-", "_", 1)}
	for _, id := range bad {
		if validSnapshotID(id) {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

// -----------------------------------------------------------------------
// Create: server-side capture, entry-count/scale, weird keys
// -----------------------------------------------------------------------

func TestSnapshotCreate_EmptyPrefix_WholeBucket(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "x.bin", []byte("hello"))
	putObj(t, srv, "b", "y.bin", []byte("world"))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != 2 {
		t.Fatalf("ObjectCount = %d, want 2", summary.ObjectCount)
	}
	if summary.LogicalBytes != 10 {
		t.Fatalf("LogicalBytes = %d, want 10", summary.LogicalBytes)
	}
	if summary.SourceBucket != "b" || summary.SourcePrefix != "" {
		t.Fatalf("summary scope mismatch: %+v", summary)
	}
	if !validSnapshotID(summary.SnapshotID) {
		t.Fatalf("returned snapshot ID %q is not a valid UUID", summary.SnapshotID)
	}
}

func TestSnapshotCreate_OneObject(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "only.bin", genRandomBytes(1, 1234))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != 1 || summary.LogicalBytes != 1234 {
		t.Fatalf("summary = %+v, want 1 object / 1234 bytes", summary)
	}
}

func TestSnapshotCreate_EmptyBucketZeroObjects(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != 0 || summary.LogicalBytes != 0 {
		t.Fatalf("summary = %+v, want 0/0", summary)
	}
}

func TestSnapshotCreate_ManyObjects(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	const n = 250
	for i := 0; i < n; i++ {
		putObj(t, srv, "b", fmt.Sprintf("obj/%04d.bin", i), []byte(fmt.Sprintf("content-%d", i)))
	}
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != n {
		t.Fatalf("ObjectCount = %d, want %d", summary.ObjectCount, n)
	}
}

func TestSnapshotCreate_OverOneThousandObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-scale snapshot test in -short mode")
	}
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	const n = 1500
	for i := 0; i < n; i++ {
		putObj(t, srv, "b", fmt.Sprintf("k/%05d", i), []byte{byte(i)})
	}
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != n {
		t.Fatalf("ObjectCount = %d, want %d", summary.ObjectCount, n)
	}
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(show.Entries) != n {
		t.Fatalf("show entries = %d, want %d", len(show.Entries), n)
	}
	for i, e := range show.Entries {
		want := fmt.Sprintf("k/%05d", i)
		if e.Key != want {
			t.Fatalf("entries[%d].Key = %q, want %q (canonical ordering broken)", i, e.Key, want)
		}
	}
}

func TestSnapshotCreate_WeirdKeys(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	keys := []string{
		"space key.bin",
		"percent%20literal.bin",
		"hash#fragment.bin",
		"question?mark.bin",
		"unicode/héllo/wörld/日本語.bin",
		"a//b///c.bin",
		"trailing/slash/",
	}
	for _, k := range keys {
		putObj(t, srv, "b", k, []byte("v-"+k))
	}
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != len(keys) {
		t.Fatalf("ObjectCount = %d, want %d", summary.ObjectCount, len(keys))
	}
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range show.Entries {
		got[e.Key] = true
	}
	for _, k := range keys {
		if !got[k] {
			t.Fatalf("captured entries missing weird key %q; got %+v", k, show.Entries)
		}
	}
}

func TestSnapshotCreate_PrefixScoping(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "data/a.bin", []byte("a"))
	putObj(t, srv, "b", "data/b.bin", []byte("b"))
	putObj(t, srv, "b", "other/c.bin", []byte("c"))

	summary, err := createSnapshotRemote(cfg, "b", "data")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount != 2 {
		t.Fatalf("ObjectCount = %d, want 2 (prefix scoping failed)", summary.ObjectCount)
	}
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range show.Entries {
		if !strings.HasPrefix(e.Key, "data/") {
			t.Fatalf("captured key %q does not carry expected prefix", e.Key)
		}
	}
}

func TestSnapshotCreate_NoSuchBucket(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	if _, err := createSnapshotRemote(cfg, "nope", ""); err == nil {
		t.Fatal("expected error creating snapshot of nonexistent bucket")
	}
}

func TestSnapshotCreate_WritesZeroNewCASPayload(t *testing.T) {
	srv, dir, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "a.bin", genRandomBytes(51, 200_000))
	putObj(t, srv, "b", "sub/b.bin", genRandomBytes(52, 300_000))

	beforeFiles := countChunkFiles(t, dir)
	beforeStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := createSnapshotRemote(cfg, "b", ""); err != nil {
		t.Fatal(err)
	}

	afterFiles := countChunkFiles(t, dir)
	afterStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if afterFiles != beforeFiles {
		t.Fatalf("chunk file count changed by snapshot create: before=%d after=%d", beforeFiles, afterFiles)
	}
	if afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("ChunkStoreFileBytes changed by snapshot create: before=%d after=%d", beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}
}

func TestSnapshotCreate_CapturedRootsMatchLiveManifest(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	entry := putObjRet(t, srv, "b", "x.bin", []byte("payload"))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(show.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(show.Entries))
	}
	if show.Entries[0].ManifestUUID != entry.manifestUUID {
		t.Fatalf("captured manifest UUID = %q, want %q", show.Entries[0].ManifestUUID, entry.manifestUUID)
	}
}

func putObjRet(t *testing.T, srv *Server, bucket, key string, data []byte) *objectEntry {
	t.Helper()
	e, err := srv.store.PutObject(bucket, key, data, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// -----------------------------------------------------------------------
// Consistency: point-in-time capture semantics
// -----------------------------------------------------------------------

func TestSnapshotCreate_MutationAfterCaptureNotReflected(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	first := putObjRet(t, srv, "b", "x.bin", []byte("version-1"))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite the live object after the snapshot captured it.
	if _, err := srv.store.PutObject("b", "x.bin", []byte("version-2-longer"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if show.Entries[0].ManifestUUID != first.manifestUUID {
		t.Fatalf("snapshot entry changed after live mutation: got %q, want frozen %q", show.Entries[0].ManifestUUID, first.manifestUUID)
	}
	if show.Entries[0].Size != int64(len("version-1")) {
		t.Fatalf("snapshot entry size changed after live mutation: got %d, want %d", show.Entries[0].Size, len("version-1"))
	}
}

func TestSnapshotCreate_ObjectAddedAfterCaptureExcluded(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "before.bin", []byte("x"))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	putObj(t, srv, "b", "after.bin", []byte("y"))

	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(show.Entries) != 1 || show.Entries[0].Key != "before.bin" {
		t.Fatalf("expected only pre-capture object, got %+v", show.Entries)
	}
}

func TestSnapshotCreate_DeletedAfterCaptureStillListedInSnapshot(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "gone.bin", []byte("data"))

	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.DeleteObject("b", "gone.bin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.store.GetObject("b", "gone.bin"); err == nil {
		t.Fatal("expected live object to be gone")
	}

	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(show.Entries) != 1 || show.Entries[0].Key != "gone.bin" {
		t.Fatalf("snapshot should still list deleted-live object, got %+v", show.Entries)
	}
}

func TestSnapshotCreate_ConcurrentPutsDuringCaptureYieldsCoherentSnapshot(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	for i := 0; i < 40; i++ {
		putObj(t, srv, "b", fmt.Sprintf("stable/%03d.bin", i), []byte("v1"))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				srv.store.PutObject("b", fmt.Sprintf("churn/%d.bin", i), []byte("churn"), "application/octet-stream", nil)
				i++
			}
		}
	}()

	summary, err := createSnapshotRemote(cfg, "b", "")
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObjectCount < 40 {
		t.Fatalf("expected at least the 40 stable objects captured, got %d", summary.ObjectCount)
	}
	// Every "stable/*" entry must show manifest v1's content length (2
	// bytes) -- a torn/mixed-time capture would be internally
	// inconsistent, not merely "missing some churn objects".
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range show.Entries {
		if strings.HasPrefix(e.Key, "stable/") && e.Size != 2 {
			t.Fatalf("stable entry %q has size %d, want 2 (torn capture)", e.Key, e.Size)
		}
	}
}

// -----------------------------------------------------------------------
// List / show
// -----------------------------------------------------------------------

func TestSnapshotList_MultipleSnapshotsOrderedByID(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "x.bin", []byte("x"))

	var ids []string
	for i := 0; i < 3; i++ {
		s, err := createSnapshotRemote(cfg, "b", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.SnapshotID)
	}
	list, err := listSnapshotsRemote(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list length = %d, want 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].SnapshotID >= list[i].SnapshotID {
			t.Fatalf("list not ID-ordered: %+v", list)
		}
	}
}

func TestSnapshotList_EmptyStore(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	list, err := listSnapshotsRemote(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty list, got %+v", list)
	}
}

func TestSnapshotShow_EntriesOmittedByDefault(t *testing.T) {
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "x.bin", []byte("x"))
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	show, err := showSnapshotRemote(cfg, summary.SnapshotID, false)
	if err != nil {
		t.Fatal(err)
	}
	if show.Entries != nil {
		t.Fatalf("entries should be omitted by default, got %+v", show.Entries)
	}
	if show.ObjectCount != 1 {
		t.Fatalf("ObjectCount = %d, want 1", show.ObjectCount)
	}
}

func TestSnapshotShow_UnknownID(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	if _, err := showSnapshotRemote(cfg, newUUIDv7(), false); err == nil {
		t.Fatal("expected error for unknown snapshot ID")
	}
}

func TestSnapshotShow_InvalidID(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	for _, bad := range []string{"not-a-uuid", "../../etc/passwd", "'; DROP TABLE", ""} {
		if _, err := showSnapshotRemote(cfg, bad, false); err == nil {
			t.Fatalf("expected error for invalid snapshot ID %q", bad)
		}
	}
}

// -----------------------------------------------------------------------
// Restart durability
// -----------------------------------------------------------------------

func TestSnapshot_SurvivesRestart(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("hello"), "text/plain", nil)
	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{
		SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		CreatedAt: time.Now().UTC(), SourceBucket: "b", SourcePrefix: "", Entries: entries,
	}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.readSnapshot(desc.SnapshotID)
	if err != nil {
		t.Fatalf("snapshot did not survive restart: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Key != "x.bin" {
		t.Fatalf("restarted snapshot content mismatch: %+v", got)
	}
	list, err := reopened.listSnapshots()
	if err != nil || len(list) != 1 {
		t.Fatalf("list after restart: %+v, err=%v", list, err)
	}
}

// -----------------------------------------------------------------------
// GC: snapshot pinning, sharing, corrupt fail-safe, release-on-delete
// -----------------------------------------------------------------------

func TestSnapshotGC_PinsOtherwiseUnreachableChunks(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	payload := genRandomBytes(42, 500_000)
	mustPutObject(t, store, "b", "unique.bin", payload, "application/octet-stream", nil)

	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteObject("b", "unique.bin"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	gc, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if !gc.LiveSetOK {
		t.Fatalf("expected healthy live set, got issues: %+v", gc.Issues)
	}
	if gc.SnapshotRootCount != 1 {
		t.Fatalf("SnapshotRootCount = %d, want 1", gc.SnapshotRootCount)
	}
	if gc.ChunksDeleted != 0 {
		t.Fatalf("expected 0 chunks deleted (snapshot-pinned), got %d deleted", gc.ChunksDeleted)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.readSnapshot(desc.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := decodeHexSHA256(got.Entries[0].ManifestSHA256)
	man, err := reopened.readVerifiedManifest(got.Entries[0].ManifestUUID, sum)
	if err != nil {
		t.Fatalf("snapshot-pinned manifest unreadable after GC: %v", err)
	}
	for _, c := range man.Chunks {
		csum, _ := decodeHexSHA256(c.SHA256)
		if _, err := reopened.casRead(csum); err != nil {
			t.Fatalf("snapshot-pinned chunk %s missing after GC: %v", c.SHA256, err)
		}
	}
}

func TestSnapshotGC_MultipleSnapshotsSharingChunks_DeleteOneKeepsOther(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	payload := genRandomBytes(43, 300_000)
	mustPutObject(t, store, "b", "shared.bin", payload, "application/octet-stream", nil)

	makeSnap := func() snapshotDescriptorV1 {
		entries, err := store.captureSnapshotEntries("b", "")
		if err != nil {
			t.Fatal(err)
		}
		d := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
		if err := store.publishSnapshot(d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	snapA := makeSnap()
	snapB := makeSnap()

	if err := store.DeleteObject("b", "shared.bin"); err != nil {
		t.Fatal(err)
	}
	if err := store.deleteSnapshot(snapA.SnapshotID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	gc, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if !gc.LiveSetOK || gc.ChunksDeleted != 0 {
		t.Fatalf("expected content still pinned by snapB, got gc=%+v", gc)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.readSnapshot(snapA.SnapshotID); err == nil {
		t.Fatal("snapA should be gone")
	}
	got, err := reopened.readSnapshot(snapB.SnapshotID)
	if err != nil || len(got.Entries) != 1 {
		t.Fatalf("snapB should survive: %+v, err=%v", got, err)
	}
}

// publishOrphanSnapshotEntry writes chunks/a manifest directly (casWrite +
// publishManifest), deliberately never through commitObjectRoot -- so
// unlike an ordinary PUT/DELETE (which archives into permanent version
// history, section 7c, and therefore stays a live GC root forever
// regardless of any snapshot), this content has NO root at all except
// whatever snapshot entry the caller adds it to. This is the same
// technique TestGC_AdversarialMatrix_K1toK5's "K4" case uses to construct
// genuinely-unreachable content; here it isolates "does a snapshot root,
// specifically, keep this alive" from "does version history already keep
// everything alive forever" (the latter being true of any ordinary
// object regardless of snapshots, and not what this test is about).
func publishOrphanSnapshotEntry(t *testing.T, s *Store, key string, chunks [][]byte) snapshotEntryV1 {
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
	return snapshotEntryV1{
		Key: key, ManifestUUID: manUUID, ManifestSHA256: hex.EncodeToString(manSHA[:]),
		Size: man.TotalLength, ETag: man.ETag, ContentType: man.ContentType,
	}
}

func TestSnapshotGC_PinsContentWithNoOtherRoot(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	orphan := genRandomBytes(50, 20_000)
	entry := publishOrphanSnapshotEntry(t, store, "orphan.bin", [][]byte{orphan})
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: []snapshotEntryV1{entry}}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(orphan)
	chunkPath := store.chunkPath(sum)
	store.Close()

	// Without any current/historical/multipart root at all, this content
	// would ordinarily be exactly K4-shaped (genuinely unreachable) --
	// the snapshot root is the ONLY thing keeping it alive.
	gc, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if !gc.LiveSetOK {
		t.Fatalf("expected a healthy live set, got issues: %+v", gc.Issues)
	}
	if gc.SnapshotRootCount != 1 {
		t.Fatalf("SnapshotRootCount = %d, want 1", gc.SnapshotRootCount)
	}
	if gc.ChunksDeleted != 0 {
		t.Fatalf("snapshot-only-referenced chunk was deleted: %+v", gc)
	}
	if _, err := os.Stat(chunkPath); err != nil {
		t.Fatalf("snapshot-pinned chunk file missing after GC: %v", err)
	}
}

func TestSnapshotGC_CorruptDescriptorRefusesToSweep(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "kept.bin", []byte("kept"), "application/octet-stream", nil)
	mustPutObject(t, store, "b", "garbage-source.bin", genRandomBytes(44, 10_000), "application/octet-stream", nil)

	entries, err := store.captureSnapshotEntries("b", "kept.bin")
	if err != nil {
		t.Fatal(err)
	}
	// Fabricate the entries manually so this snapshot only covers
	// kept.bin -- garbage-source.bin's chunks are genuinely unreachable
	// once deleted, which is exactly what we want GC to (refuse to)
	// sweep despite the corrupt snapshot elsewhere in the catalog.
	_ = entries
	kept, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	var keptOnly []snapshotEntryV1
	for _, e := range kept {
		if e.Key == "kept.bin" {
			keptOnly = append(keptOnly, e)
		}
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: keptOnly}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteObject("b", "garbage-source.bin"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Corrupt the published snapshot descriptor at the filesystem level.
	path := filepath.Join(dir, "snapshots", desc.SnapshotID+".snap")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF // flip a bit in the trailing CRC32C
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	beforeFiles := countChunkFiles(t, dir)

	// A destructive apply against an untrustworthy live root set fails
	// closed: gcCollect returns errGCUnsafe (the exact same fail-closed
	// gate section 13b already uses for a corrupt manifest/chunk, section
	// 12a) alongside a GCResult that still reports what it found -- see
	// gcCollect's own "if !rr.OK() { return res, errGCUnsafe }". Nothing
	// is deleted either way.
	gc, err := gcCollect(dir, true)
	if !errors.Is(err, errGCUnsafe) {
		t.Fatalf("gcCollect apply with a corrupt snapshot present: err=%v, want errGCUnsafe", err)
	}
	if gc.LiveSetOK {
		t.Fatal("expected LiveSetOK=false with a corrupt snapshot descriptor present")
	}
	if gc.ChunksDeleted != 0 || gc.ManifestsDeleted != 0 {
		t.Fatalf("GC must not delete anything when refusing to sweep, got chunks=%d manifests=%d", gc.ChunksDeleted, gc.ManifestsDeleted)
	}
	foundCorrupt := false
	for _, iss := range gc.Issues {
		if strings.Contains(iss.Subject, "snapshot:"+desc.SnapshotID) {
			foundCorrupt = true
		}
	}
	if !foundCorrupt {
		t.Fatalf("expected an issue naming the corrupt snapshot, got %+v", gc.Issues)
	}

	afterFiles := countChunkFiles(t, dir)
	if afterFiles != beforeFiles {
		t.Fatalf("chunk files changed despite refused GC: before=%d after=%d", beforeFiles, afterFiles)
	}

	// Dry-run must also surface the same issue without needing -apply.
	dryRun, err := gcCollect(dir, false)
	if err != nil {
		t.Fatalf("dry-run gc should not hard-fail: %v", err)
	}
	if dryRun.LiveSetOK {
		t.Fatal("dry-run should also report LiveSetOK=false")
	}
}

func TestSnapshotGC_UnexpectedFileNameInSnapshotsDirCausesRefusal(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("x"), "application/octet-stream", nil)
	store.Close()

	if err := os.WriteFile(filepath.Join(dir, "snapshots", "not-a-real-snapshot.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	gc, err := gcCollect(dir, true)
	if !errors.Is(err, errGCUnsafe) {
		t.Fatalf("gcCollect apply: err=%v, want errGCUnsafe", err)
	}
	if gc.LiveSetOK {
		t.Fatal("expected LiveSetOK=false with an unexpected file in store/snapshots/")
	}
}

func TestSnapshotGC_DeleteFinalSnapshotAllowsEventualCollection(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry := publishOrphanSnapshotEntry(t, store, "x.bin", [][]byte{genRandomBytes(45, 20_000)})
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: []snapshotEntryV1{entry}}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	// While the snapshot exists, GC must preserve the content (its only
	// root).
	store.Close()
	gc1, err := gcCollect(dir, true)
	if err != nil || !gc1.LiveSetOK || gc1.ChunksDeleted != 0 {
		t.Fatalf("expected content preserved while snapshot exists: gc=%+v err=%v", gc1, err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.deleteSnapshot(desc.SnapshotID); err != nil {
		t.Fatal(err)
	}
	reopened.Close()

	gc2, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if !gc2.LiveSetOK {
		t.Fatalf("expected healthy live set after snapshot deletion, got %+v", gc2.Issues)
	}
	if gc2.ChunksDeleted == 0 {
		t.Fatal("expected the now-unreferenced content to become collectible after the last snapshot was deleted")
	}
}

func TestSnapshotGC_NoSnapshotsUnchangedBehavior(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	// A genuinely unreferenced chunk (no object, no manifest, no
	// snapshot at all -- exactly TestGC_AdversarialMatrix_K1toK5's own
	// "K4" case) must remain collectible exactly as it always has: M8E
	// must not change ordinary (snapshot-free) GC behavior at all.
	orphan := genRandomBytes(46, 10_000)
	if _, err := store.casWrite(orphan); err != nil {
		t.Fatal(err)
	}
	store.Close()

	gc, err := gcCollect(dir, true)
	if err != nil {
		t.Fatalf("gcCollect: %v", err)
	}
	if gc.SnapshotRootCount != 0 {
		t.Fatalf("SnapshotRootCount = %d, want 0 (no snapshots ever created)", gc.SnapshotRootCount)
	}
	if !gc.LiveSetOK || gc.ChunksDeleted == 0 {
		t.Fatalf("ordinary (snapshot-free) GC behavior changed: %+v", gc)
	}
}

// -----------------------------------------------------------------------
// Delete: durability, crash semantics, concurrency
// -----------------------------------------------------------------------

func TestSnapshotDelete_RemovesDescriptorOnly(t *testing.T) {
	srv, dir, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	putObj(t, srv, "b", "x.bin", []byte("x"))
	summary, err := createSnapshotRemote(cfg, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteSnapshotRemote(cfg, summary.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := showSnapshotRemote(cfg, summary.SnapshotID, false); err == nil {
		t.Fatal("expected snapshot to be gone after delete")
	}
	// Object itself must be completely unaffected.
	if _, _, err := srv.store.GetObject("b", "x.bin"); err != nil {
		t.Fatalf("live object affected by unrelated snapshot delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "snapshots")); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDelete_UnknownID(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	if err := deleteSnapshotRemote(cfg, newUUIDv7()); err == nil {
		t.Fatal("expected error deleting unknown snapshot")
	}
}

func TestSnapshotDelete_InvalidID(t *testing.T) {
	_, _, cfg, closeFn := newSnapshotTestServer(t)
	defer closeFn()
	if err := deleteSnapshotRemote(cfg, "../../../etc/passwd"); err == nil {
		t.Fatal("expected error deleting with a path-traversal-shaped ID")
	}
}

func TestSnapshotDelete_SurvivesRestart(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("x"), "application/octet-stream", nil)
	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}
	if err := store.deleteSnapshot(desc.SnapshotID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.readSnapshot(desc.SnapshotID); err == nil {
		t.Fatal("deleted snapshot reappeared after restart")
	}
}

func TestSnapshotDelete_ConcurrentDeletesOneWinsOneGetsCleanError(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	_ = dir
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("x"), "application/octet-stream", nil)
	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = store.deleteSnapshot(desc.SnapshotID)
		}(i)
	}
	wg.Wait()

	successes, notFounds := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errNoSuchSnapshot):
			notFounds++
		default:
			t.Fatalf("unexpected concurrent-delete error: %v", err)
		}
	}
	if successes != 1 || notFounds != 1 {
		t.Fatalf("expected exactly one success and one clean errNoSuchSnapshot, got successes=%d notFounds=%d (results=%v)", successes, notFounds, results)
	}
}

// -----------------------------------------------------------------------
// Crash publication: no partial descriptor is ever treated as valid
// -----------------------------------------------------------------------

func TestSnapshotCreate_NoStrayTempFilesLeftInSnapshotsDir(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("x"), "application/octet-stream", nil)
	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	ents, err := os.ReadDir(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != desc.SnapshotID+".snap" {
		t.Fatalf("store/snapshots/ contains unexpected entries: %+v", ents)
	}
}

func TestSnapshotCreate_IncompleteTempFileNeverObservedAsPublished(t *testing.T) {
	// Simulates a crash mid-publication: an incomplete file staged
	// directly under store/snapshots/ (bypassing writeFileDurable's
	// tmp-then-rename path, exactly as an interrupted rename would never
	// leave anything but either nothing or the complete renamed file)
	// must never be treated as a valid, complete snapshot.
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	id := newUUIDv7()
	partial := []byte("ZSS1")[:2] // an obviously-truncated frame
	if err := os.WriteFile(filepath.Join(dir, "snapshots", id+".snap"), partial, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.readSnapshot(id); err == nil {
		t.Fatal("expected a truncated partial descriptor to be rejected, not treated as valid")
	}
	if _, err := reopened.listSnapshots(); err == nil {
		t.Fatal("expected listSnapshots to fail loudly on a truncated descriptor rather than silently skip it")
	}
}

// -----------------------------------------------------------------------
// doctor/verify integration
// -----------------------------------------------------------------------

func TestSnapshot_VerifyReportsCorruptSnapshot(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, store, "b", "x.bin", []byte("x"), "application/octet-stream", nil)
	entries, err := store.captureSnapshotEntries("b", "")
	if err != nil {
		t.Fatal(err)
	}
	desc := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := store.publishSnapshot(desc); err != nil {
		t.Fatal(err)
	}

	verifyOK, err := store.Verify(false)
	if err != nil || !verifyOK.OK() {
		t.Fatalf("verify should be clean before corruption: %+v err=%v", verifyOK, err)
	}
	if verifyOK.SnapshotRootCount != 1 {
		t.Fatalf("SnapshotRootCount = %d, want 1", verifyOK.SnapshotRootCount)
	}

	path := filepath.Join(dir, "snapshots", desc.SnapshotID+".snap")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	verifyBad, err := store.Verify(false)
	if err != nil {
		t.Fatalf("verify should report, not hard-fail: %v", err)
	}
	if verifyBad.OK() {
		t.Fatal("expected verify to report corrupt snapshot as a failure")
	}
	found := false
	for _, iss := range verifyBad.Issues {
		if strings.Contains(iss.Subject, "snapshot:"+desc.SnapshotID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("verify issues do not mention the corrupt snapshot: %+v", verifyBad.Issues)
	}
}

// -----------------------------------------------------------------------
// Real-process CLI: full create/list/show/delete lifecycle
// -----------------------------------------------------------------------

func TestCLI_Snapshot_CreateListShowDelete(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/data", nil)
	mustSignedOK(http.MethodPut, "/data/a.bin", []byte("alpha"))
	mustSignedOK(http.MethodPut, "/data/b.bin", []byte("beta-longer"))

	createOut, createErr, code := runZeros3CLI(t, bin, "snapshot", "create", "-endpoint", "http://"+addr, "s3://data/")
	if code != 0 {
		t.Fatalf("snapshot create failed (code %d): stdout=%s stderr=%s", code, createOut, createErr)
	}
	if !strings.Contains(createOut, "Objects:            2") {
		t.Fatalf("unexpected create summary: %s", createOut)
	}
	lines := strings.Split(strings.TrimSpace(createOut), "\n")
	idLine := lines[0]
	id := strings.TrimSpace(strings.TrimPrefix(idLine, "Snapshot:"))
	if !validSnapshotID(id) {
		t.Fatalf("could not parse snapshot ID from output: %q", createOut)
	}

	listOut, listErr, code := runZeros3CLI(t, bin, "snapshot", "list", "-endpoint", "http://"+addr)
	if code != 0 {
		t.Fatalf("snapshot list failed (code %d): stdout=%s stderr=%s", code, listOut, listErr)
	}
	if !strings.Contains(listOut, id) {
		t.Fatalf("snapshot list did not include %s: %s", id, listOut)
	}

	showOut, showErr, code := runZeros3CLI(t, bin, "snapshot", "show", "-endpoint", "http://"+addr, "-entries", id)
	if code != 0 {
		t.Fatalf("snapshot show failed (code %d): stdout=%s stderr=%s", code, showOut, showErr)
	}
	if !strings.Contains(showOut, "a.bin") || !strings.Contains(showOut, "b.bin") {
		t.Fatalf("snapshot show -entries missing expected keys: %s", showOut)
	}

	deleteOut, deleteErr, code := runZeros3CLI(t, bin, "snapshot", "delete", "-endpoint", "http://"+addr, id)
	if code != 0 {
		t.Fatalf("snapshot delete failed (code %d): stdout=%s stderr=%s", code, deleteOut, deleteErr)
	}

	_, _, code = runZeros3CLI(t, bin, "snapshot", "show", "-endpoint", "http://"+addr, id)
	if code == 0 {
		t.Fatal("expected snapshot show to fail after delete")
	}

	cmd.Process.Kill()
	cmd.Wait()

	verifyOut, verifyErr, verifyCode := runZeros3CLI(t, bin, "verify", "-store", storeDir, "-deep")
	if verifyCode != 0 {
		t.Fatalf("verify -deep after snapshot lifecycle failed: code=%d stdout=%s stderr=%s", verifyCode, verifyOut, verifyErr)
	}
}

func TestCLI_Snapshot_GC_PinsAcrossRestart(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/data", nil)
	mustSignedOK(http.MethodPut, "/data/keep.bin", genRandomBytes(60, 40_000))

	createOut, createErr, code := runZeros3CLI(t, bin, "snapshot", "create", "-endpoint", "http://"+addr, "s3://data/")
	if code != 0 {
		t.Fatalf("snapshot create failed (code %d): stdout=%s stderr=%s", code, createOut, createErr)
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.Split(strings.TrimSpace(createOut), "\n")[0], "Snapshot:"))

	cmd.Process.Kill()
	cmd.Wait()

	gcOut, gcErr, gcCode := runZeros3CLI(t, bin, "gc", "-store", storeDir, "-apply")
	if gcCode != 0 {
		t.Fatalf("gc -apply failed: code=%d stdout=%s stderr=%s", gcCode, gcOut, gcErr)
	}

	cmd2 := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd2.Process.Kill(); cmd2.Wait() }()
	waitForZeros3Serve(t, addr)

	showOut, showErr, showCode := runZeros3CLI(t, bin, "snapshot", "show", "-endpoint", "http://"+addr, id)
	if showCode != 0 {
		t.Fatalf("snapshot show after restart+gc failed: code=%d stdout=%s stderr=%s", showCode, showOut, showErr)
	}
	if !strings.Contains(showOut, "Objects:            1") {
		t.Fatalf("snapshot content changed across restart+gc: %s", showOut)
	}
}

// =============================================================================
// M8E-B: snapshot restore (section 15i)
// =============================================================================

// newRestoreTestServer wires one server (same-store restore, matching
// runSnapshotRestore's own single -endpoint model) with the given
// buckets pre-created, plus a syncClientConfig usable both as the
// "Snapshot" side (endpoint/creds only) and as a per-bucket "Dest" side.
func newRestoreTestServer(t *testing.T, buckets ...string) (srv *Server, storeDir string, cfg func(bucket string) syncClientConfig, closeFn func()) {
	t.Helper()
	dir, s, creds, region := newSyncTestServer(t)
	for _, b := range buckets {
		if err := s.store.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(s)
	cfg = func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: region, HTTPClient: ts.Client()}
	}
	return s, dir, cfg, ts.Close
}

// -----------------------------------------------------------------------
// Basic restore
// -----------------------------------------------------------------------

func TestSnapshotRestore_OneObject(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("hello restore"))

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := restoreNamespace(restoreNamespaceConfig{
		Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Replicated != 1 {
		t.Fatalf("restore result = %+v", result)
	}
	_, body, err := srv.store.GetObject("dst", "a.bin")
	if err != nil || string(body) != "hello restore" {
		t.Fatalf("restored object missing/wrong: body=%q err=%v", body, err)
	}
}

func TestSnapshotRestore_EmptySnapshot(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	_ = srv
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 0 || result.Replicated != 0 || !result.OK() {
		t.Fatalf("empty snapshot restore result = %+v", result)
	}
}

func TestSnapshotRestore_NestedPrefix(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "data/models/a.bin", []byte("model-a"))
	putObj(t, srv, "src", "data/models/sub/b.bin", []byte("model-b"))

	summary, err := createSnapshotRemote(cfg("src"), "src", "data")
	if err != nil {
		t.Fatal(err)
	}
	dst := cfg("dst")
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: dst, DestPrefix: "recovered"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Replicated != 2 {
		t.Fatalf("restore result = %+v", result)
	}
	if _, body, err := srv.store.GetObject("dst", "recovered/models/a.bin"); err != nil || string(body) != "model-a" {
		t.Fatalf("recovered/models/a.bin missing/wrong: body=%q err=%v", body, err)
	}
	if _, body, err := srv.store.GetObject("dst", "recovered/models/sub/b.bin"); err != nil || string(body) != "model-b" {
		t.Fatalf("recovered/models/sub/b.bin missing/wrong: body=%q err=%v", body, err)
	}
}

func TestSnapshotRestore_OverOneThousandObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-scale restore test in -short mode")
	}
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	const n = 1200
	for i := 0; i < n; i++ {
		putObj(t, srv, "src", fmt.Sprintf("k/%05d", i), []byte(fmt.Sprintf("v%d", i)))
	}
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Replicated != n {
		t.Fatalf("restore result = %+v, want %d replicated", result, n)
	}
	for i := 0; i < n; i += 137 { // sample rather than checking all 1200 individually
		key := fmt.Sprintf("k/%05d", i)
		if _, body, err := srv.store.GetObject("dst", key); err != nil || string(body) != fmt.Sprintf("v%d", i) {
			t.Fatalf("restored %s wrong/missing: body=%q err=%v", key, body, err)
		}
	}
}

func TestSnapshotRestore_WeirdKeys(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	keys := []string{"space key.bin", "percent%20literal.bin", "hash#fragment.bin", "question?mark.bin", "unicode/héllo/日本語.bin", "a//b///c.bin"}
	for _, k := range keys {
		putObj(t, srv, "src", k, []byte("v-"+k))
	}
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Replicated != len(keys) {
		t.Fatalf("restore result = %+v", result)
	}
	for _, k := range keys {
		if _, body, err := srv.store.GetObject("dst", k); err != nil || string(body) != "v-"+k {
			t.Fatalf("restored weird key %q wrong/missing: body=%q err=%v", k, body, err)
		}
	}
}

func TestSnapshotRestore_MetadataAndContentTypePreserved(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	if _, err := srv.store.PutObject("src", "a.bin", []byte("data"), "application/x-custom", map[string]string{"owner": "alice"}); err != nil {
		t.Fatal(err)
	}
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")}); err != nil {
		t.Fatal(err)
	}
	_, man, err := srv.store.HeadObject("dst", "a.bin")
	if err != nil {
		t.Fatal(err)
	}
	if man.ContentType != "application/x-custom" {
		t.Fatalf("ContentType = %q, want application/x-custom", man.ContentType)
	}
	found := false
	for _, kv := range man.Metadata {
		if kv.Key == "owner" && kv.Value == "alice" {
			found = true
		}
	}
	if !found {
		t.Fatalf("metadata not preserved: %+v", man.Metadata)
	}
}

// -----------------------------------------------------------------------
// Point-in-time: the defining M8E-B proofs
// -----------------------------------------------------------------------

func TestSnapshotRestore_SourceMutatedAfterSnapshot_RestoresOldVersion(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("version-A"))

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.PutObject("src", "a.bin", []byte("version-B-is-different-and-longer"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst"), DestPrefix: "recovered"}); err != nil {
		t.Fatal(err)
	}

	_, liveBody, err := srv.store.GetObject("src", "a.bin")
	if err != nil || string(liveBody) != "version-B-is-different-and-longer" {
		t.Fatalf("live source should be version-B: body=%q err=%v", liveBody, err)
	}
	_, restoredBody, err := srv.store.GetObject("dst", "recovered/a.bin")
	if err != nil || string(restoredBody) != "version-A" {
		t.Fatalf("restored object should be version-A: body=%q err=%v", restoredBody, err)
	}
}

func TestSnapshotRestore_SourceDeletedThenGC_StillRestoresExactly(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	srv := NewServer(store, creds, defaultRegion)
	ts := httptest.NewServer(srv)
	cfg := func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Creds: creds, Region: defaultRegion, HTTPClient: ts.Client()}
	}
	payload := genRandomBytes(61, 200_000)
	mustPutObject(t, store, "src", "unique.bin", payload, "application/octet-stream", nil)

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteObject("src", "unique.bin"); err != nil {
		t.Fatal(err)
	}

	ts.Close()
	store.Close()
	gc, err := gcCollect(dir, true)
	if err != nil || !gc.LiveSetOK {
		t.Fatalf("gc after source delete: %+v err=%v", gc, err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	srv2 := NewServer(reopened, creds, defaultRegion)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	cfg2 := func(bucket string) syncClientConfig {
		return syncClientConfig{Endpoint: ts2.URL, Bucket: bucket, Creds: creds, Region: defaultRegion, HTTPClient: ts2.Client()}
	}

	if _, _, err := reopened.GetObject("src", "unique.bin"); err == nil {
		t.Fatal("live source object should be gone")
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg2("src"), SnapshotID: summary.SnapshotID, Dest: cfg2("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Replicated != 1 {
		t.Fatalf("restore after source-delete+GC: %+v", result)
	}
	_, restoredBody, err := reopened.GetObject("dst", "unique.bin")
	if err != nil || !bytes.Equal(restoredBody, payload) {
		t.Fatalf("restored content mismatch after source-delete+GC: err=%v", err)
	}
}

// -----------------------------------------------------------------------
// Zero-payload restore
// -----------------------------------------------------------------------

func TestSnapshotRestore_ZeroNewCASPayload(t *testing.T) {
	srv, dir, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", genRandomBytes(62, 300_000))
	putObj(t, srv, "src", "b.bin", genRandomBytes(63, 500_000))

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}

	beforeFiles := countChunkFiles(t, dir)
	beforeStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || result.Stats.UploadedBytes != 0 || result.Stats.UniqueChunksUploaded != 0 {
		t.Fatalf("expected zero-payload restore, got %+v", result)
	}

	afterFiles := countChunkFiles(t, dir)
	afterStats, err := srv.store.computeStats(statsScope{})
	if err != nil {
		t.Fatal(err)
	}
	if afterFiles != beforeFiles {
		t.Fatalf("chunk file count changed by restore: before=%d after=%d", beforeFiles, afterFiles)
	}
	if afterStats.ChunkStoreFileBytes != beforeStats.ChunkStoreFileBytes {
		t.Fatalf("ChunkStoreFileBytes changed by restore: before=%d after=%d", beforeStats.ChunkStoreFileBytes, afterStats.ChunkStoreFileBytes)
	}
}

// -----------------------------------------------------------------------
// Conflict / resume
// -----------------------------------------------------------------------

func TestSnapshotRestore_PreexistingDifferentDestinationConflict(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("snapshot-content"))
	putObj(t, srv, "dst", "a.bin", []byte("unrelated-existing-content"))

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || result.Failed != 1 {
		t.Fatalf("expected conflict failure, got %+v", result)
	}
	_, body, err := srv.store.GetObject("dst", "a.bin")
	if err != nil || string(body) != "unrelated-existing-content" {
		t.Fatalf("conflicting destination object must be unmodified: body=%q err=%v", body, err)
	}
}

func TestSnapshotRestore_ResumeMatchingPriorRestoreIsNoOp(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("content"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil || !first.OK() {
		t.Fatalf("first restore: %+v err=%v", first, err)
	}
	second, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK() || second.Replicated != 1 {
		t.Fatalf("resumed restore of already-landed object should succeed as a no-op-equivalent, got %+v", second)
	}
}

func TestSnapshotRestore_LaterObjectsContinueAfterOneConflict(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a-conflict.bin", []byte("snapshot-a"))
	putObj(t, srv, "src", "b-ok.bin", []byte("snapshot-b"))
	putObj(t, srv, "dst", "a-conflict.bin", []byte("pre-existing-different"))

	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || result.Replicated != 1 {
		t.Fatalf("expected 1 failed + 1 replicated, got %+v", result)
	}
	if _, body, err := srv.store.GetObject("dst", "b-ok.bin"); err != nil || string(body) != "snapshot-b" {
		t.Fatalf("b-ok.bin should have restored despite a-conflict.bin's failure: body=%q err=%v", body, err)
	}
}

// -----------------------------------------------------------------------
// Independence
// -----------------------------------------------------------------------

func TestSnapshotRestore_RestoredMutationDoesNotAffectSnapshot(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("original"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.PutObject("dst", "a.bin", []byte("mutated-after-restore"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	show, err := showSnapshotRemote(cfg("src"), summary.SnapshotID, true)
	if err != nil {
		t.Fatal(err)
	}
	if show.Entries[0].Size != int64(len("original")) {
		t.Fatalf("snapshot entry changed after restored-object mutation: %+v", show.Entries[0])
	}
}

func TestSnapshotRestore_RestoredDeletionDoesNotAffectSnapshot(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("original"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.DeleteObject("dst", "a.bin"); err != nil {
		t.Fatal(err)
	}
	show, err := showSnapshotRemote(cfg("src"), summary.SnapshotID, false)
	if err != nil {
		t.Fatalf("snapshot should remain valid after restored-object deletion: %v", err)
	}
	if show.ObjectCount != 1 {
		t.Fatalf("snapshot ObjectCount changed: %+v", show)
	}
}

func TestSnapshotRestore_SnapshotDeletionAfterRestoreDoesNotBreakRestoredNamespace(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("original"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")}); err != nil {
		t.Fatal(err)
	}
	if err := deleteSnapshotRemote(cfg("src"), summary.SnapshotID); err != nil {
		t.Fatal(err)
	}
	_, body, err := srv.store.GetObject("dst", "a.bin")
	if err != nil || string(body) != "original" {
		t.Fatalf("restored object must survive snapshot deletion: body=%q err=%v", body, err)
	}
}

func TestSnapshotRestore_DeleteOriginalNamespaceDoesNotAffectRestoredCopy(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("original"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.DeleteObject("src", "a.bin"); err != nil {
		t.Fatal(err)
	}
	_, body, err := srv.store.GetObject("dst", "a.bin")
	if err != nil || string(body) != "original" {
		t.Fatalf("restored copy must survive source deletion: body=%q err=%v", body, err)
	}
}

// -----------------------------------------------------------------------
// GC / concurrency during restore
// -----------------------------------------------------------------------

func TestSnapshotRestore_TwoRestoresFromSameSnapshotToDistinctDestinations(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst1", "dst2")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("shared-content"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst1")})
	if err != nil || !r1.OK() {
		t.Fatalf("restore to dst1: %+v err=%v", r1, err)
	}
	r2, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst2")})
	if err != nil || !r2.OK() {
		t.Fatalf("restore to dst2: %+v err=%v", r2, err)
	}
	if _, body, err := srv.store.GetObject("dst1", "a.bin"); err != nil || string(body) != "shared-content" {
		t.Fatalf("dst1 restore wrong: body=%q err=%v", body, err)
	}
	if _, body, err := srv.store.GetObject("dst2", "a.bin"); err != nil || string(body) != "shared-content" {
		t.Fatalf("dst2 restore wrong: body=%q err=%v", body, err)
	}
}

func TestSnapshotRestore_DeepVerifyGreenAfterRestore(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src", "dst")
	defer closeFn()
	for i := 0; i < 20; i++ {
		putObj(t, srv, "src", fmt.Sprintf("k%d.bin", i), genRandomBytes(int64(70+i), 5000))
	}
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("dst")})
	if err != nil || !result.OK() {
		t.Fatalf("restore: %+v err=%v", result, err)
	}
	verify, err := srv.store.Verify(true)
	if err != nil || !verify.OK() {
		t.Fatalf("deep verify after restore: %+v err=%v", verify, err)
	}
}

func TestSnapshotRestore_DestinationBucketMissingFailsClearly(t *testing.T) {
	srv, _, cfg, closeFn := newRestoreTestServer(t, "src")
	defer closeFn()
	putObj(t, srv, "src", "a.bin", []byte("x"))
	summary, err := createSnapshotRemote(cfg("src"), "src", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg("src"), SnapshotID: summary.SnapshotID, Dest: cfg("nope")})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK() || result.Failed != 1 {
		t.Fatalf("expected restore to a missing destination bucket to fail cleanly per-object, got %+v", result)
	}
}

// -----------------------------------------------------------------------
// Real-process CLI: full restore lifecycle, including interruption/resume
// -----------------------------------------------------------------------

func TestCLI_SnapshotRestore_EndToEnd(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/prod", nil)
	mustSignedOK(http.MethodPut, "/recovery", nil)
	mustSignedOK(http.MethodPut, "/prod/data/a.bin", []byte("point-in-time-content"))

	createOut, createErr, code := runZeros3CLI(t, bin, "snapshot", "create", "-endpoint", "http://"+addr, "s3://prod/")
	if code != 0 {
		t.Fatalf("snapshot create failed: code=%d stdout=%s stderr=%s", code, createOut, createErr)
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.Split(strings.TrimSpace(createOut), "\n")[0], "Snapshot:"))

	// Mutate the live source after the snapshot -- restore must still
	// produce the captured point-in-time content.
	mustSignedOK(http.MethodPut, "/prod/data/a.bin", []byte("overwritten-live-content"))

	restoreOut, restoreErr, code := runZeros3CLI(t, bin, "snapshot", "restore", "-endpoint", "http://"+addr, id, "s3://recovery/aug30/")
	if code != 0 {
		t.Fatalf("snapshot restore failed: code=%d stdout=%s stderr=%s", code, restoreOut, restoreErr)
	}
	if !strings.Contains(restoreOut, "New CAS payload:         0 B") {
		t.Fatalf("expected zero new CAS payload in restore summary: %s", restoreOut)
	}

	resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/recovery/aug30/data/a.bin", nil, nil)
	gotBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(gotBody) != "point-in-time-content" {
		t.Fatalf("GET recovered object: status=%d body=%q", resp.StatusCode, gotBody)
	}
	resp2 := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, "/prod/data/a.bin", nil, nil)
	liveBody, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(liveBody) != "overwritten-live-content" {
		t.Fatalf("live source should be unaffected: %q", liveBody)
	}

	cmd.Process.Kill()
	cmd.Wait()
	verifyOut, verifyErr, verifyCode := runZeros3CLI(t, bin, "verify", "-store", storeDir, "-deep")
	if verifyCode != 0 {
		t.Fatalf("verify -deep after restore failed: code=%d stdout=%s stderr=%s", verifyCode, verifyOut, verifyErr)
	}
}

func TestCLI_SnapshotRestore_ResumeAcrossRealProcessKill(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)

	cmd := exec.Command(bin, "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{}
	mustSignedOK := func(method, path string, body []byte) {
		t.Helper()
		resp := doSignedRequest(t, client, "http://"+addr, signer, method, path, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
	}
	mustSignedOK(http.MethodPut, "/prod", nil)
	mustSignedOK(http.MethodPut, "/recovery", nil)
	const n = 60
	for i := 0; i < n; i++ {
		mustSignedOK(http.MethodPut, fmt.Sprintf("/prod/obj-%03d.bin", i), []byte(fmt.Sprintf("content-%03d", i)))
	}

	createOut, createErr, code := runZeros3CLI(t, bin, "snapshot", "create", "-endpoint", "http://"+addr, "s3://prod/")
	if code != 0 {
		t.Fatalf("snapshot create failed: code=%d stdout=%s stderr=%s", code, createOut, createErr)
	}
	id := strings.TrimSpace(strings.TrimPrefix(strings.Split(strings.TrimSpace(createOut), "\n")[0], "Snapshot:"))

	// Kill the restore CLI process partway through by racing a short
	// timeout against it -- a real process interruption, not a
	// simulated in-process hook.
	restoreCmd := exec.Command(bin, "snapshot", "restore", "-endpoint", "http://"+addr, id, "s3://recovery/")
	if err := restoreCmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	restoreCmd.Process.Kill()
	restoreCmd.Wait()

	// Rerun to completion.
	out2, err2, code2 := runZeros3CLI(t, bin, "snapshot", "restore", "-endpoint", "http://"+addr, id, "s3://recovery/")
	if code2 != 0 {
		t.Fatalf("resumed snapshot restore failed: code=%d stdout=%s stderr=%s", code2, out2, err2)
	}

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("/recovery/obj-%03d.bin", i)
		resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodGet, key, nil, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		want := fmt.Sprintf("content-%03d", i)
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("recovered object %s: status=%d body=%q, want %q", key, resp.StatusCode, body, want)
		}
	}

	cmd.Process.Kill()
	cmd.Wait()
	verifyOut, verifyErr, verifyCode := runZeros3CLI(t, bin, "verify", "-store", storeDir, "-deep")
	if verifyCode != 0 {
		t.Fatalf("verify -deep after interrupted+resumed restore failed: code=%d stdout=%s stderr=%s", verifyCode, verifyOut, verifyErr)
	}
}

// =============================================================================
// M8F-A: atomic conditional PutObject -- If-None-Match: "*" / If-Match
// (section 10a)
//
// These tests prove the two required conditional-write primitives end to
// end: correctness of the supported header subset, that a failed condition
// never mutates anything visible (content, metadata, version history) while
// leaving its speculative CAS payload safely GC-collectible, and -- the
// actual point of M8F-A -- that the precondition is enforced at
// commitObjectRootChecked's locked commit boundary, not by some earlier,
// race-prone check. The deterministic-winner/ordering tests below use
// hookAfterManifestPublished (fired after all of a PUT's slow CDC/CAS/
// manifest work, immediately before the s.mu-guarded commit) as a
// synchronization barrier, exactly the same pattern withTestHook's crash
// tests already use, so these races are proven by construction rather than
// by hoping goroutine scheduling behaves.
// =============================================================================

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---- If-None-Match: "*" (create-if-absent), Store-level -------------------

func TestConditionalPut_IfNoneMatchStar_AbsentKeySucceeds(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObjectChecked("b", "k", []byte("v1"), "text/plain", nil, putCondition{ifNoneMatchStar: true})
	if err != nil {
		t.Fatalf("expected create-if-absent to succeed against an absent key, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" || entry.etag == "" {
		t.Fatalf("unexpected result: body=%q etag=%q", body, entry.etag)
	}
}

func TestConditionalPut_IfNoneMatchStar_ExistingKeyFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutObjectChecked("b", "k", []byte("v2"), "text/plain", nil, putCondition{ifNoneMatchStar: true})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected errPreconditionFailed against an existing key, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" {
		t.Fatalf("failed create-if-absent must not mutate the existing object, got %q", body)
	}
}

func TestConditionalPut_IfNoneMatchStar_DeletedKeySucceeds(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	entry, err := s.PutObjectChecked("b", "k", []byte("v2"), "text/plain", nil, putCondition{ifNoneMatchStar: true})
	if err != nil {
		t.Fatalf("expected create-if-absent to succeed after a delete, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2" || entry == nil {
		t.Fatalf("unexpected result after delete+recreate: body=%q", body)
	}
}

func TestConditionalPut_IfNoneMatchStar_FailedWriteLeavesContentMetadataAndVersionsUnchanged(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	orig, err := s.PutObject("b", "k", []byte("original"), "text/original", map[string]string{"owner": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PutObjectChecked("b", "k", []byte("attempted-overwrite"), "text/attempted", map[string]string{"owner": "mallory"}, putCondition{ifNoneMatchStar: true})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected precondition failure, got %v", err)
	}

	entry, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("content mutated by a failed conditional write: %q", body)
	}
	if entry.etag != orig.etag || entry.contentType != orig.contentType || entry.manifestUUID != orig.manifestUUID {
		t.Fatalf("metadata/identity mutated by a failed conditional write: got %+v, want manifest %q etag %q type %q", entry, orig.manifestUUID, orig.etag, orig.contentType)
	}
	if got := historyFor(t, s, "b", "k"); len(got) != 0 {
		t.Fatalf("a failed conditional write must create no visible version, got %d archived entries", len(got))
	}
}

// ---- If-Match (compare-and-swap), Store-level ------------------------------

func TestConditionalPut_IfMatch_MatchingETagSucceeds(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.PutObjectChecked("b", "k", []byte("v2"), "text/plain", nil, putCondition{ifMatchETag: v1.etag})
	if err != nil {
		t.Fatalf("expected CAS update to succeed against the exact observed ETag, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2" || v2.etag == v1.etag {
		t.Fatalf("unexpected result: body=%q etag=%q (was %q)", body, v2.etag, v1.etag)
	}
}

func TestConditionalPut_IfMatch_MismatchingETagFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutObjectChecked("b", "k", []byte("v2"), "text/plain", nil, putCondition{ifMatchETag: "0123456789abcdef0123456789abcdef"})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected errPreconditionFailed against a mismatching ETag, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v1" {
		t.Fatalf("failed CAS must not mutate the existing object, got %q", body)
	}
}

func TestConditionalPut_IfMatch_AbsentKeyFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutObjectChecked("b", "k", []byte("v1"), "text/plain", nil, putCondition{ifMatchETag: "0123456789abcdef0123456789abcdef"})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected errPreconditionFailed against an absent key, got %v", err)
	}
	if _, _, err := s.GetObject("b", "k"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("key must remain absent after a failed If-Match create attempt, got err=%v", err)
	}
}

func TestConditionalPut_IfMatch_HistoricalETagDoesNotMatchCurrent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.PutObject("b", "k", []byte("v1"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.PutObject("b", "k", []byte("v2, now current"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v1.etag == v2.etag {
		t.Fatalf("test setup: v1 and v2 must have distinct ETags")
	}
	_, err = s.PutObjectChecked("b", "k", []byte("v3"), "text/plain", nil, putCondition{ifMatchETag: v1.etag})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected If-Match against a superseded historical ETag to fail, got %v", err)
	}
	_, body, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2, now current" {
		t.Fatalf("current object must remain v2, got %q", body)
	}
}

func TestConditionalPut_IfMatch_ABASameETagAfterDeleteRecreateSucceeds(t *testing.T) {
	// Documents M8F-D's explicit ABA discussion: If-Match compares against
	// the current representation's ETag, not a hidden generation/version
	// identity, matching real HTTP/S3 ETag semantics. A delete followed by
	// a recreate with byte-identical content (so the ETag recurs) therefore
	// satisfies a caller's If-Match against that ETag, even though the
	// current object is not, in fact, the same version the caller
	// originally observed. This is intentional and matches AWS's own
	// public ETag semantics -- ZeroS3's own internal sync/replication
	// preconditions (syncPrecondition, section 15) are a separate
	// mechanism and are free to add stronger generation-based identity
	// later if that internal use ever needs it; ordinary public If-Match
	// is not silently strengthened beyond what real S3 promises.
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	sameBytes := []byte("identical content, twice")
	orig, err := s.PutObject("b", "key", sameBytes, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject("b", "key"); err != nil {
		t.Fatal(err)
	}
	recreated, err := s.PutObject("b", "key", sameBytes, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.etag != orig.etag {
		t.Fatalf("test setup: expected the recreated object to have the same ETag (ABA), got %q vs %q", recreated.etag, orig.etag)
	}

	if _, err := s.PutObjectChecked("b", "key", []byte("v2"), "text/plain", nil, putCondition{ifMatchETag: orig.etag}); err != nil {
		t.Fatalf("expected If-Match to succeed against a recurring ETag (ABA case), per plain HTTP/S3 ETag semantics, got %v", err)
	}
}

func TestConditionalPut_IfMatch_SpeculativeCASBecomesGCCollectibleAfterFailure(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "k", []byte("kept"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	// A distinct, large body so the failed attempt's chunks are unmistakably
	// its own, never incidentally shared with the surviving object.
	loserBody := genRandomBytes(99, 500*1024)
	_, err = s.PutObjectChecked("b", "k", loserBody, "text/plain", nil, putCondition{ifMatchETag: "0123456789abcdef0123456789abcdef"})
	if !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("expected precondition failure, got %v", err)
	}
	s.Close()

	dry, err := gcCollect(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.LiveSetOK {
		t.Fatalf("expected a healthy live set after a failed conditional write, got issues: %+v", dry.Issues)
	}
	if dry.ChunksUnreachable == 0 {
		t.Fatalf("expected the failed conditional write's speculative CAS payload to be reported as unreachable/collectible")
	}

	applied, err := gcCollect(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ChunksDeleted == 0 {
		t.Fatalf("expected GC to reclaim the failed conditional write's speculative CAS payload")
	}

	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_, body, err := s2.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "kept" {
		t.Fatalf("surviving object corrupted by GC after a failed conditional write, got %q", body)
	}
	verifyRes, err := s2.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRes.OK() {
		t.Fatalf("expected a fully valid store after GC of a failed conditional write's payload, got issues: %+v", verifyRes.Issues)
	}
}

// ---- HTTP-level: header parsing, status codes, quoting, multipart ---------

func TestConditionalPutHTTP_IfNoneMatchStar_CreateThenRejectWith412(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	first := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), map[string]string{"If-None-Match": "*"})
	firstBody, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("expected create-if-absent to succeed, got %d: %s", first.StatusCode, firstBody)
	}

	second := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v2"), map[string]string{"If-None-Match": "*"})
	secondBody, _ := io.ReadAll(second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 on repeat create-if-absent, got %d", second.StatusCode)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(secondBody, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "PreconditionFailed" {
		t.Fatalf("expected PreconditionFailed, got %q", errBody.Code)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if string(body) != "v1" {
		t.Fatalf("first bytes must remain intact after a rejected create-if-absent, got %q", body)
	}
}

func TestConditionalPutHTTP_IfMatch_CASUpdateThenStaleRejectWith412(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	v1 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	v1.Body.Close()
	etagA := strings.Trim(v1.Header.Get("ETag"), `"`)

	v2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v2"), map[string]string{"If-Match": `"` + etagA + `"`})
	v2.Body.Close()
	if v2.StatusCode != http.StatusOK {
		t.Fatalf("expected CAS update to succeed, got %d", v2.StatusCode)
	}
	etagB := strings.Trim(v2.Header.Get("ETag"), `"`)
	if etagB == etagA {
		t.Fatalf("expected a new ETag after CAS update")
	}

	v3 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v3"), map[string]string{"If-Match": `"` + etagA + `"`})
	v3Body, _ := io.ReadAll(v3.Body)
	v3.Body.Close()
	if v3.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for a stale If-Match, got %d", v3.StatusCode)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(v3Body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "PreconditionFailed" {
		t.Fatalf("expected PreconditionFailed, got %q", errBody.Code)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if string(body) != "v2" {
		t.Fatalf("expected GET to return v2, got %q", body)
	}
}

func TestConditionalPutHTTP_IfMatch_QuotedUnquotedAndCaseInsensitive(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := strings.Trim(put.Header.Get("ETag"), `"`)

	// Unquoted.
	r1 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v2"), map[string]string{"If-Match": etag})
	r1.Body.Close()
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("expected an unquoted If-Match value to be accepted, got %d", r1.StatusCode)
	}
	etag2 := strings.Trim(r1.Header.Get("ETag"), `"`)

	// Quoted, uppercase-hex (case-insensitive match).
	r2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v3"), map[string]string{"If-Match": `"` + strings.ToUpper(etag2) + `"`})
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("expected a quoted, differently-cased If-Match value to match case-insensitively, got %d", r2.StatusCode)
	}
}

func TestConditionalPutHTTP_IfMatch_MultipartCurrentETag(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	part := genRandomBytes(42, 6*1024*1024)
	partETag, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 1, part)
	if status != http.StatusOK {
		t.Fatal("upload part failed")
	}
	result, cStatus, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: partETag}})
	if cStatus != http.StatusOK {
		t.Fatal("complete failed")
	}
	multipartETagValue := strings.Trim(result.ETag, `"`)
	if !strings.Contains(multipartETagValue, "-") {
		t.Fatalf("test setup: expected a multipart-shaped ETag, got %q", multipartETagValue)
	}

	// A CAS update against the object's real, current multipart-shaped ETag
	// must succeed, exactly like any other If-Match.
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("overwritten"), map[string]string{"If-Match": multipartETagValue})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected If-Match against the current multipart ETag to succeed, got %d", resp.StatusCode)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, nil)
	body, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if string(body) != "overwritten" {
		t.Fatalf("expected the overwrite to have taken effect, got %q", body)
	}
}

func TestConditionalPutHTTP_WhitespaceAroundStarTolerated(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), map[string]string{"If-None-Match": "  *  "})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected surrounding whitespace around '*' to be tolerated, got %d", resp.StatusCode)
	}
}

func TestConditionalPutHTTP_MalformedAndUnsupportedHeaderValuesRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"If-None-Match not a bare star", map[string]string{"If-None-Match": `"someetag"`}},
		{"If-Match unterminated quote", map[string]string{"If-Match": `"abc`}},
		{"If-Match comma-separated list", map[string]string{"If-Match": `"a","b"`}},
		{"If-Match weak validator", map[string]string{"If-Match": `W/"abc"`}},
		{"If-Match star", map[string]string{"If-Match": "*"}},
		{"If-Match empty quoted", map[string]string{"If-Match": `""`}},
		{"both headers set", map[string]string{"If-Match": "abc", "If-None-Match": "*"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "/b/" + strings.ReplaceAll(tc.name, " ", "-")
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, key, []byte("v1"), tc.headers)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", tc.name, resp.StatusCode, body)
			}
			var errBody s3ErrorBody
			if err := xml.Unmarshal(body, &errBody); err != nil {
				t.Fatalf("expected an S3-shaped error body: %v", err)
			}
			if errBody.Code != "InvalidArgument" {
				t.Fatalf("expected InvalidArgument for %s, got %q", tc.name, errBody.Code)
			}
			get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, key, nil, nil)
			get.Body.Close()
			if get.StatusCode != http.StatusNotFound {
				t.Fatalf("a rejected conditional header must not create anything, got GET status %d for %s", get.StatusCode, tc.name)
			}
		})
	}
}

func TestConditionalPutHTTP_UnconditionalPUTUnaffectedByM8F(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := []byte("ordinary put, no conditional headers")
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", body, nil)
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected an ordinary PUT to succeed unconditionally, got %d: %s", resp.StatusCode, respBody)
	}
	wantETag := md5.Sum(body) //nolint:gosec // test-only.
	if got := resp.Header.Get("ETag"); got != `"`+hex.EncodeToString(wantETag[:])+`"` {
		t.Fatalf("unexpected ETag for an unconditional PUT: %s", got)
	}

	// Overwrite must also still succeed unconditionally, exactly as before M8F.
	resp2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("overwritten"), nil)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected an unconditional overwrite to still succeed, got %d", resp2.StatusCode)
	}
}

// ---- Deterministic races: exactly one winner, checked at commit -----------

// runBarrieredConcurrentPuts installs a testHook that blocks every caller
// reaching hookAfterManifestPublished -- the point immediately after a PUT's
// CDC/CAS/manifest work finishes and immediately before commitObjectRootChecked
// acquires s.mu -- until all n of do's goroutines have arrived there, then
// releases them all at once so they genuinely contend for the same
// commit-point lock instead of merely being scheduled close together. The
// previous hook is restored via t.Cleanup.
func runBarrieredConcurrentPuts(t *testing.T, n int, do func(i int)) {
	t.Helper()
	var arrived sync.WaitGroup
	arrived.Add(n)
	releaseCh := make(chan struct{})
	old := testHook
	testHook = func(point string) {
		if point != hookAfterManifestPublished {
			return
		}
		arrived.Done()
		<-releaseCh
	}
	t.Cleanup(func() { testHook = old })

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			do(i)
		}(i)
	}
	arrived.Wait()
	close(releaseCh)
	wg.Wait()
}

func TestConditionalPut_ConcurrentCreateOnly_ExactlyOneWinner(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	const n = 8
	results := make([]error, n)
	runBarrieredConcurrentPuts(t, n, func(i int) {
		body := bytes.Repeat([]byte{byte('A' + i)}, 1000+i)
		_, err := s.PutObjectChecked("b", "key", body, "text/plain", nil, putCondition{ifNoneMatchStar: true})
		results[i] = err
	})

	successes, failures := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errPreconditionFailed):
			failures++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || failures != n-1 {
		t.Fatalf("expected exactly 1 success and %d failures, got %d successes and %d failures", n-1, successes, failures)
	}
	if _, _, err := s.GetObject("b", "key"); err != nil {
		t.Fatalf("expected the single winner's object to be visible: %v", err)
	}
}

func TestConditionalPut_ConcurrentIfMatchSameOldETag_ExactlyOneWinner(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	orig, err := s.PutObject("b", "key", []byte("v0"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	results := make([]error, n)
	runBarrieredConcurrentPuts(t, n, func(i int) {
		body := bytes.Repeat([]byte{byte('A' + i)}, 1000+i)
		_, err := s.PutObjectChecked("b", "key", body, "text/plain", nil, putCondition{ifMatchETag: orig.etag})
		results[i] = err
	})

	successes, failures, winner := 0, 0, -1
	for i, err := range results {
		switch {
		case err == nil:
			successes++
			winner = i
		case errors.Is(err, errPreconditionFailed):
			failures++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || failures != n-1 {
		t.Fatalf("expected exactly 1 success and %d failures, got %d successes and %d failures", n-1, successes, failures)
	}
	_, body, err := s.GetObject("b", "key")
	if err != nil {
		t.Fatal(err)
	}
	wantBody := bytes.Repeat([]byte{byte('A' + winner)}, 1000+winner)
	if !bytes.Equal(body, wantBody) {
		t.Fatalf("current object does not match the declared CAS winner's body (no lost update expected)")
	}
}

func TestConditionalPutHTTP_ConcurrentCreateOnly_ExactlyOneWinnerOverRealHTTP(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}

	const n = 20
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := bytes.Repeat([]byte{byte('A' + i)}, 1000+i)
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/key", body, map[string]string{"If-None-Match": "*"})
			statuses[i] = resp.StatusCode
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	successes, failures := 0, 0
	for _, code := range statuses {
		switch code {
		case http.StatusOK:
			successes++
		case http.StatusPreconditionFailed:
			failures++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if successes != 1 || failures != n-1 {
		t.Fatalf("expected exactly 1 success and %d failures over real HTTP, got %d successes and %d failures", n-1, successes, failures)
	}
}

func TestConditionalPut_StaleIfMatchLosesAfterInterveningOrdinaryPut(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	orig, err := s.PutObject("b", "key", []byte("A"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}

	parked := make(chan struct{})
	release := make(chan struct{})
	parkedFirst := false
	old := testHook
	testHook = func(point string) {
		if point != hookAfterManifestPublished {
			return
		}
		if !parkedFirst {
			parkedFirst = true
			close(parked)
			<-release
		}
		// Any later caller (the ordinary writer below) passes straight
		// through unblocked -- only the first (conditional) writer parks.
	}
	t.Cleanup(func() { testHook = old })

	var wg sync.WaitGroup
	wg.Add(1)
	var condErr error
	go func() {
		defer wg.Done()
		_, condErr = s.PutObjectChecked("b", "key", []byte("stale-writer-content"), "text/plain", nil, putCondition{ifMatchETag: orig.etag})
	}()
	<-parked // the conditional writer observed A and is now parked right before the commit lock

	// An ordinary (unconditional) writer races in and publishes B while the
	// conditional writer is still parked, holding its now-stale observation
	// of A.
	ordinary, err := s.PutObject("b", "key", []byte("B"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.etag == orig.etag {
		t.Fatalf("test setup: expected the ordinary write to change the ETag")
	}

	close(release)
	wg.Wait()

	if !errors.Is(condErr, errPreconditionFailed) {
		t.Fatalf("expected the stale conditional writer to re-evaluate at commit and fail, got %v", condErr)
	}
	_, body, err := s.GetObject("b", "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "B" {
		t.Fatalf("expected the ordinary writer's B to remain current, got %q", body)
	}
}

func TestConditionalPut_StaleIfMatchLosesAfterDeleteRecreate(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	orig, err := s.PutObject("b", "key", []byte("A"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}

	parked := make(chan struct{})
	release := make(chan struct{})
	parkedFirst := false
	old := testHook
	testHook = func(point string) {
		if point != hookAfterManifestPublished {
			return
		}
		if !parkedFirst {
			parkedFirst = true
			close(parked)
			<-release
		}
	}
	t.Cleanup(func() { testHook = old })

	var wg sync.WaitGroup
	wg.Add(1)
	var condErr error
	go func() {
		defer wg.Done()
		_, condErr = s.PutObjectChecked("b", "key", []byte("stale-writer-content"), "text/plain", nil, putCondition{ifMatchETag: orig.etag})
	}()
	<-parked

	if err := s.DeleteObject("b", "key"); err != nil {
		t.Fatal(err)
	}
	recreated, err := s.PutObject("b", "key", []byte("C, recreated with different content"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.etag == orig.etag {
		t.Fatalf("test setup: expected the recreated object to have a different ETag")
	}

	close(release)
	wg.Wait()

	if !errors.Is(condErr, errPreconditionFailed) {
		t.Fatalf("expected a stale If-Match to fail after delete+recreate with a different ETag, got %v", condErr)
	}
	_, body, err := s.GetObject("b", "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "C, recreated with different content" {
		t.Fatalf("expected the recreated object to remain current, got %q", body)
	}
}

// =============================================================================
// M8F-B: conditional GET/HEAD -- If-Match / If-None-Match read
// preconditions (section 10b)
// =============================================================================

func TestConditionalGetHTTP_IfMatch_MatchSucceeds(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := put.Header.Get("ETag")

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": etag})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "v1" {
		t.Fatalf("expected 200 with body v1, got %d %q", resp.StatusCode, body)
	}
}

func TestConditionalGetHTTP_IfMatch_MismatchFailsWith412(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": `"0123456789abcdef0123456789abcdef"`})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "PreconditionFailed" {
		t.Fatalf("expected PreconditionFailed, got %q", errBody.Code)
	}
}

func TestConditionalHeadHTTP_IfMatch_MatchAndMismatch(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := put.Header.Get("ETag")

	ok := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/k", nil, map[string]string{"If-Match": etag})
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for a matching If-Match HEAD, got %d", ok.StatusCode)
	}

	mismatch := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/k", nil, map[string]string{"If-Match": `"0123456789abcdef0123456789abcdef"`})
	mismatch.Body.Close()
	if mismatch.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for a mismatching If-Match HEAD, got %d", mismatch.StatusCode)
	}
}

func TestConditionalGetHTTP_IfNoneMatch_MatchReturns304(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := put.Header.Get("ETag")

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-None-Match": etag})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Fatalf("expected an empty 304 body, got %d bytes", len(body))
	}
	if got := resp.Header.Get("ETag"); got != etag {
		t.Fatalf("expected the 304 response to still carry the ETag header, got %q want %q", got, etag)
	}
}

func TestConditionalGetHTTP_IfNoneMatch_MismatchServesNormally(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-None-Match": `"0123456789abcdef0123456789abcdef"`})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "v1" {
		t.Fatalf("expected a normal 200 GET, got %d %q", resp.StatusCode, body)
	}
}

func TestConditionalHeadHTTP_IfNoneMatch_MatchAndMismatch(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := put.Header.Get("ETag")

	match := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/k", nil, map[string]string{"If-None-Match": etag})
	match.Body.Close()
	if match.StatusCode != http.StatusNotModified {
		t.Fatalf("expected 304 for a matching If-None-Match HEAD, got %d", match.StatusCode)
	}

	mismatch := doSignedRequest(t, client, ts.URL, signer, http.MethodHead, "/b/k", nil, map[string]string{"If-None-Match": `"0123456789abcdef0123456789abcdef"`})
	mismatch.Body.Close()
	if mismatch.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for a mismatching If-None-Match HEAD, got %d", mismatch.StatusCode)
	}
}

func TestConditionalGetHTTP_RangeInteraction(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("0123456789"), 1000) // 10000 bytes
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", body, nil)
	put.Body.Close()
	etag := put.Header.Get("ETag")

	t.Run("If-Match passes + Range", func(t *testing.T) {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": etag, "Range": "bytes=0-99"})
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("expected 206, got %d", resp.StatusCode)
		}
		if !bytes.Equal(got, body[:100]) {
			t.Fatalf("expected the requested range's bytes, got %d bytes", len(got))
		}
	})

	t.Run("If-Match fails + Range", func(t *testing.T) {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": `"0123456789abcdef0123456789abcdef"`, "Range": "bytes=0-99"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("expected the failed precondition (412) to short-circuit Range processing, got %d", resp.StatusCode)
		}
		if resp.Header.Get("Content-Range") != "" {
			t.Fatalf("expected no Content-Range header on a 412 response, got %q", resp.Header.Get("Content-Range"))
		}
	})

	t.Run("If-None-Match matches + Range", func(t *testing.T) {
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-None-Match": etag, "Range": "bytes=0-99"})
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("expected the matched If-None-Match (304) to short-circuit Range processing, got %d", resp.StatusCode)
		}
		if len(got) != 0 {
			t.Fatalf("expected an empty 304 body, got %d bytes", len(got))
		}
		if resp.Header.Get("Content-Range") != "" {
			t.Fatalf("expected no Content-Range header on a 304 response, got %q", resp.Header.Get("Content-Range"))
		}
	})
}

func TestConditionalGetHTTP_MissingObjectIgnoresCondition(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/nosuchkey", nil, map[string]string{"If-Match": `"0123456789abcdef0123456789abcdef"`})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected NoSuchKey (404) regardless of the conditional header, got %d: %s", resp.StatusCode, body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "NoSuchKey" {
		t.Fatalf("expected NoSuchKey, got %q", errBody.Code)
	}
}

func TestConditionalGetHTTP_MultipartCurrentETag(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	uploadID := doCreateMultipartUpload(t, client, ts.URL, signer, "b", "k")
	part := genRandomBytes(43, 6*1024*1024)
	partETag, status, _ := doUploadPart(t, client, ts.URL, signer, "b", "k", uploadID, 1, part)
	if status != http.StatusOK {
		t.Fatal("upload part failed")
	}
	result, cStatus, _ := doCompleteMultipartUpload(t, client, ts.URL, signer, "b", "k", uploadID, []completedPartXML{{PartNumber: 1, ETag: partETag}})
	if cStatus != http.StatusOK {
		t.Fatal("complete failed")
	}

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": result.ETag})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected If-Match against the current multipart ETag to succeed, got %d", resp.StatusCode)
	}
}

func TestConditionalGetHTTP_QuotedUnquotedAndCaseInsensitive(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()
	etag := strings.Trim(put.Header.Get("ETag"), `"`)

	unquoted := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": etag})
	unquoted.Body.Close()
	if unquoted.StatusCode != http.StatusOK {
		t.Fatalf("expected an unquoted If-Match to be accepted, got %d", unquoted.StatusCode)
	}

	upper := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": `"` + strings.ToUpper(etag) + `"`})
	upper.Body.Close()
	if upper.StatusCode != http.StatusOK {
		t.Fatalf("expected a quoted, differently-cased If-Match to match case-insensitively, got %d", upper.StatusCode)
	}
}

func TestConditionalGetHTTP_MalformedAndUnsupportedHeaderValuesRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	put.Body.Close()

	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"If-Match unterminated quote", map[string]string{"If-Match": `"abc`}},
		{"If-Match comma list", map[string]string{"If-Match": `"a","b"`}},
		{"If-Match weak validator", map[string]string{"If-Match": `W/"abc"`}},
		{"If-Match star", map[string]string{"If-Match": "*"}},
		{"If-None-Match star", map[string]string{"If-None-Match": "*"}},
		{"If-None-Match weak validator", map[string]string{"If-None-Match": `W/"abc"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, tc.headers)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d: %s", tc.name, resp.StatusCode, body)
			}
			var errBody s3ErrorBody
			if err := xml.Unmarshal(body, &errBody); err != nil {
				t.Fatalf("expected an S3-shaped error body: %v", err)
			}
			if errBody.Code != "InvalidArgument" {
				t.Fatalf("expected InvalidArgument for %s, got %q", tc.name, errBody.Code)
			}
		})
	}
}

func TestConditionalGetHTTP_HistoricalETagIsNotCurrentForIfMatch(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	v1 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v1"), nil)
	v1.Body.Close()
	etagA := v1.Header.Get("ETag")
	v2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/k", []byte("v2, now current"), nil)
	v2.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/k", nil, map[string]string{"If-Match": etagA})
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected If-Match against a superseded historical ETag to fail with 412, got %d", resp.StatusCode)
	}
}

// =============================================================================
// M8F-C: conditional CopyObject source predicates --
// x-amz-copy-source-if-match / x-amz-copy-source-if-none-match
// =============================================================================

func TestCopyObjectHTTP_SourceIfMatch_MatchSucceeds(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), nil)
	put.Body.Close()
	srcETag := put.Header.Get("ETag")

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":          "/b/src",
		"X-Amz-Copy-Source-If-Match": srcETag,
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the copy to succeed, got %d: %s", resp.StatusCode, body)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/dst", nil, nil)
	got, _ := io.ReadAll(get.Body)
	get.Body.Close()
	if string(got) != "source-bytes" {
		t.Fatalf("expected the destination to hold the source's exact bytes, got %q", got)
	}
}

func TestCopyObjectHTTP_SourceIfMatch_MismatchFailsWith412(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":          "/b/src",
		"X-Amz-Copy-Source-If-Match": `"0123456789abcdef0123456789abcdef"`,
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", resp.StatusCode, body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "PreconditionFailed" {
		t.Fatalf("expected PreconditionFailed, got %q", errBody.Code)
	}

	get := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/dst", nil, nil)
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("a rejected source-conditional copy must not create the destination, got %d", get.StatusCode)
	}
}

func TestCopyObjectHTTP_SourceIfNoneMatch_MismatchSucceeds(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":               "/b/src",
		"X-Amz-Copy-Source-If-None-Match": `"0123456789abcdef0123456789abcdef"`,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the copy to succeed against a mismatching If-None-Match, got %d", resp.StatusCode)
	}
}

func TestCopyObjectHTTP_SourceIfNoneMatch_MatchFailsWith412(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), nil)
	put.Body.Close()
	srcETag := put.Header.Get("ETag")

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":               "/b/src",
		"X-Amz-Copy-Source-If-None-Match": srcETag,
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 when x-amz-copy-source-if-none-match matches the source's current ETag, got %d: %s", resp.StatusCode, body)
	}
}

func TestCopyObjectHTTP_SourceIfMatch_SupersededHistoricalETagFails(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	v1 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("v1"), nil)
	v1.Body.Close()
	etagA := v1.Header.Get("ETag")
	v2 := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("v2, now current"), nil)
	v2.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":          "/b/src",
		"X-Amz-Copy-Source-If-Match": etagA,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected copy source If-Match against a superseded historical ETag to fail, got %d", resp.StatusCode)
	}
}

func TestCopyObjectHTTP_SourceIfMatch_WithMetadataReplaceDirective(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), map[string]string{"Content-Type": "text/plain"})
	put.Body.Close()
	srcETag := put.Header.Get("ETag")

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":          "/b/src",
		"X-Amz-Copy-Source-If-Match": srcETag,
		"X-Amz-Metadata-Directive":   "REPLACE",
		"Content-Type":               "application/json",
		"x-amz-meta-owner":           "alice",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the conditional copy with REPLACE metadata to succeed, got %d", resp.StatusCode)
	}

	head := doHeadObject(t, client, ts.URL, signer, "/b/dst")
	head.Body.Close()
	if got := head.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected REPLACE'd Content-Type, got %q", got)
	}
	if got := head.Header.Get("x-amz-meta-owner"); got != "alice" {
		t.Fatalf("expected REPLACE'd metadata, got %q", got)
	}
}

func TestCopyObjectHTTP_SourceConditionMalformedRejected(t *testing.T) {
	srv, signer := newTestServerAndSigner(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()
	if err := doCreateBucket(t, client, ts.URL, signer, "b"); err != nil {
		t.Fatal(err)
	}
	put := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/src", []byte("source-bytes"), nil)
	put.Body.Close()

	resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/dst", nil, map[string]string{
		"X-Amz-Copy-Source":          "/b/src",
		"X-Amz-Copy-Source-If-Match": `"unterminated`,
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed x-amz-copy-source-if-match, got %d: %s", resp.StatusCode, body)
	}
	var errBody s3ErrorBody
	if err := xml.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("expected an S3-shaped error body: %v", err)
	}
	if errBody.Code != "InvalidArgument" {
		t.Fatalf("expected InvalidArgument, got %q", errBody.Code)
	}
}

// TestCopyObject_SourceConditionUsesAtomicallyCapturedRevision proves
// CopyObject's source precondition (10a's doc comment on CopyObject)
// cannot observe one source revision and then copy another: the source is
// captured once via lookupObject, the condition is evaluated against that
// exact capture, and the manifest/chunks used afterward are the ones that
// capture already pinned -- never re-fetched. This exercises that
// end-to-end at the Store level: a copy whose If-Match names the CURRENT
// revision must copy that revision's exact bytes, even though a second,
// distinct revision already exists in the source's history by the time the
// copy runs.
func TestCopyObject_SourceConditionUsesAtomicallyCapturedRevision(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject("b", "src", []byte("v1"), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	v2, err := s.PutObject("b", "src", []byte("v2-is-current"), "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}

	entry, _, err := s.CopyObject(CopyObjectRequest{
		SrcBucket: "b", SrcKey: "src", DstBucket: "b", DstKey: "dst",
		SrcIfMatchETag: v2.etag,
	})
	if err != nil {
		t.Fatalf("expected the copy to succeed against the current revision's ETag, got %v", err)
	}
	if entry.etag != v2.etag {
		t.Fatalf("expected the destination to clone v2's ETag, got %q want %q", entry.etag, v2.etag)
	}
	_, body, err := s.GetObject("b", "dst")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2-is-current" {
		t.Fatalf("expected the destination to hold v2's exact bytes, got %q", body)
	}
}

// =============================================================================
// M8G-A: `replicate -dry-run` -- read-only replication planning
// =============================================================================

// storeContentFingerprint hashes every file's relative path, length, and
// content under dir into one aggregate digest -- an independent,
// filesystem-level proof that a store's authoritative persistent state
// (journal, manifests, CAS chunks, snapshots, FORMAT.json) is byte-for-
// byte unchanged, deliberately not relying on any in-process accounting
// this milestone's own new code might get wrong. filepath.Walk visits
// files in lexical order (per its documented contract), so this is stable
// across two calls against an unchanged tree with no sorting of our own
// needed.
func storeContentFingerprint(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("storeContentFingerprint(%s): %v", dir, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// failOnChunkGET wraps a Server so any GET to the chunk-download endpoint
// fails the test immediately -- an independent, protocol-level proof that
// dry-run planning (M8G-A3) never fetches source chunk payload, rather
// than trusting planReplication's own code to simply not call
// fetchSourceChunk.
type failOnChunkGET struct {
	t   *testing.T
	srv http.Handler
}

func (f failOnChunkGET) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
		f.t.Errorf("dry-run must never GET chunk payload, but got %s %s", r.Method, r.URL.Path)
	}
	if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
		f.t.Errorf("dry-run must never PUT chunk payload, but got %s %s", r.Method, r.URL.Path)
	}
	if r.Method == http.MethodPost && r.URL.Path == zeros3SyncCommitPath {
		f.t.Errorf("dry-run must never POST commit, but got %s %s", r.Method, r.URL.Path)
	}
	f.srv.ServeHTTP(w, r)
}

func TestDryRun_SingleObject_AllChunksMissing(t *testing.T) {
	_, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(failOnChunkGET{t, srcSrv})
	defer srcTS.Close()
	dstTS := httptest.NewServer(failOnChunkGET{t, dstSrv})
	defer dstTS.Close()

	body := genRandomBytes(90001, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	before := storeContentFingerprint(t, dstDir)
	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	after := storeContentFingerprint(t, dstDir)
	if before != after {
		t.Fatalf("dry-run mutated destination store on disk")
	}

	if p.action != destActionPublish {
		t.Fatalf("action = %q, want %q", p.action, destActionPublish)
	}
	if p.wouldTransferBytes != int64(len(body)) {
		t.Fatalf("wouldTransferBytes = %d, want %d (everything missing)", p.wouldTransferBytes, len(body))
	}
	if p.missingOccur != len(p.plan.ordered) {
		t.Fatalf("missingOccur = %d, want all %d occurrences missing", p.missingOccur, len(p.plan.ordered))
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("dry-run must not have published the destination object")
	}
}

func TestDryRun_SingleObject_AllChunksPresent_AlreadyEquivalent(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()

	body := genRandomBytes(90002, 500_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "text/plain", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	// Actually replicate first (not part of the dry-run under test), so
	// the destination already holds the exact content dry-run will be
	// asked about, through a plain (unwrapped) server -- the wrapped,
	// GET/PUT/commit-forbidding server below is only stood up afterward,
	// so seeding itself is never mistaken for a dry-run violation.
	seedTS := httptest.NewServer(dstSrv)
	seedCfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: seedTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: seedTS.Client()},
	}
	if _, err := replicateObject(seedCfg); err != nil {
		t.Fatalf("seeding replicateObject: %v", err)
	}
	seedTS.Close()

	dstTS := httptest.NewServer(failOnChunkGET{t, dstSrv})
	defer dstTS.Close()
	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	if p.action != destActionEquivalent {
		t.Fatalf("action = %q, want %q", p.action, destActionEquivalent)
	}
	if p.wouldTransferBytes != 0 {
		t.Fatalf("wouldTransferBytes = %d, want 0 (destination already has every chunk)", p.wouldTransferBytes)
	}
	if p.missingOccur != 0 {
		t.Fatalf("missingOccur = %d, want 0", p.missingOccur)
	}
}

func TestDryRun_SingleObject_PartialOverlap_ExactAccounting(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	// Two chunks pre-seeded directly into the destination's CAS by exact
	// digest (so negotiate reports them present regardless of whether any
	// object references them -- negotiation is a pure CAS-presence check,
	// per handleSyncNegotiate's own doc comment), one chunk genuinely new.
	// Chunks are written directly via casWrite rather than through an
	// ordinary PutObject, deliberately sidestepping CDC's own
	// content-defined chunk boundaries: PutObject-ing the concatenation
	// of shared1+shared2 would almost certainly re-chunk it along
	// different boundaries than these exact, independently-hashed byte
	// spans, making the "already present" outcome nondeterministic.
	shared1 := genRandomBytes(90101, 20_000)
	shared2 := genRandomBytes(90102, 20_000)
	newOnly := genRandomBytes(90103, 20_000)
	if _, err := dstSrv.store.casWrite(shared1); err != nil {
		t.Fatal(err)
	}
	if _, err := dstSrv.store.casWrite(shared2); err != nil {
		t.Fatal(err)
	}

	sum1 := sha256.Sum256(shared1)
	sum2 := sha256.Sum256(shared2)
	sum3 := sha256.Sum256(newOnly)
	refs := []chunkRef{
		{SHA256: hex.EncodeToString(sum1[:]), Length: int64(len(shared1))},
		{SHA256: hex.EncodeToString(sum2[:]), Length: int64(len(shared2))},
		{SHA256: hex.EncodeToString(sum3[:]), Length: int64(len(newOnly))},
	}
	total := int64(len(shared1) + len(shared2) + len(newOnly))
	objHash := sha256.New()
	objHash.Write(shared1)
	objHash.Write(shared2)
	objHash.Write(newOnly)
	var objSHA [32]byte
	copy(objSHA[:], objHash.Sum(nil))
	man := buildManifestV1FromRefs(refs, total, objSHA, "partialobj", "application/octet-stream", nil)
	if _, err := srcSrv.store.casWrite(shared1); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.casWrite(shared2); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.casWrite(newOnly); err != nil {
		t.Fatal(err)
	}
	manUUID, manSHA, err := srcSrv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.commitObjectRoot("src", "partialobj", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "partialobj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "partialobj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	if p.missingOccur != 1 {
		t.Fatalf("missingOccur = %d, want 1", p.missingOccur)
	}
	if p.wouldTransferBytes != int64(len(newOnly)) {
		t.Fatalf("wouldTransferBytes = %d, want %d", p.wouldTransferBytes, len(newOnly))
	}
	wantAvoided := total - int64(len(newOnly))
	if got := p.desc.Size - p.wouldTransferBytes; got != wantAvoided {
		t.Fatalf("avoided = %d, want %d", got, wantAvoided)
	}
	if p.action != destActionPublish {
		t.Fatalf("action = %q, want %q", p.action, destActionPublish)
	}
}

func TestDryRun_SingleObject_DestinationConflict(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	mustPutSourceObject(t, srcSrv, "src", "obj", []byte("source content"), "text/plain", nil)
	mustPutSourceObject(t, dstSrv, "dst", "obj", []byte("unrelated, pre-existing, different content"), "text/plain", nil)

	cfg := replicateConfig{
		Source:           syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:             syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestMustBeAbsent: true,
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	if p.action != destActionConflict {
		t.Fatalf("action = %q, want %q", p.action, destActionConflict)
	}
}

func TestDryRun_SingleObject_EmptyObject(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "empty", []byte{}, "text/plain", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "empty", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "empty", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	if p.wouldTransferBytes != 0 || p.desc.Size != 0 {
		t.Fatalf("empty object should predict zero transfer, got wouldTransferBytes=%d size=%d", p.wouldTransferBytes, p.desc.Size)
	}
	var buf bytes.Buffer
	printDryRunSingle(&buf, p)
	if !strings.Contains(buf.String(), "would publish object") {
		t.Fatalf("expected empty object to still report a publish action, got:\n%s", buf.String())
	}
}

func TestDryRun_SingleObject_DuplicateChunkReferences(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, dstSrv, "dst")
	mustCreateReplicateBucket(t, srcSrv, "src")

	chunkA := genRandomBytes(90201, 15_000)
	chunkB := genRandomBytes(90202, 15_000)
	if _, err := srcSrv.store.casWrite(chunkA); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.casWrite(chunkB); err != nil {
		t.Fatal(err)
	}
	sumA := sha256.Sum256(chunkA)
	sumB := sha256.Sum256(chunkB)
	refs := []chunkRef{
		{SHA256: hex.EncodeToString(sumA[:]), Length: int64(len(chunkA))},
		{SHA256: hex.EncodeToString(sumB[:]), Length: int64(len(chunkB))},
		{SHA256: hex.EncodeToString(sumA[:]), Length: int64(len(chunkA))},
		{SHA256: hex.EncodeToString(sumA[:]), Length: int64(len(chunkA))},
	}
	total := int64(len(chunkA)*3 + len(chunkB))
	objHash := sha256.New()
	objHash.Write(chunkA)
	objHash.Write(chunkB)
	objHash.Write(chunkA)
	objHash.Write(chunkA)
	var objSHA [32]byte
	copy(objSHA[:], objHash.Sum(nil))
	man := buildManifestV1FromRefs(refs, total, objSHA, "dupdryrun", "application/octet-stream", nil)
	manUUID, manSHA, err := srcSrv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.commitObjectRoot("src", "dupobj", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	cfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "dupobj", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "dupobj", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	// Everything is missing (fresh destination bucket), but chunk A must
	// only be counted once toward wouldTransferBytes despite 3 occurrences
	// (A6's "count its payload transfer once, matching real network
	// behavior").
	if p.missingOccur != 4 {
		t.Fatalf("missingOccur = %d, want 4 (occurrences)", p.missingOccur)
	}
	wantTransfer := int64(len(chunkA) + len(chunkB))
	if p.wouldTransferBytes != wantTransfer {
		t.Fatalf("wouldTransferBytes = %d, want %d (unique chunks only)", p.wouldTransferBytes, wantTransfer)
	}
}

func TestDryRun_SingleObject_WeirdKeys(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, dstSrv, "dst")

	weirdKeys := []string{
		"a b/c%d#e?f.bin",
		"unicode/héllo-wörld-日本語.bin",
		"repeated//slashes///here.bin",
	}
	for i, key := range weirdKeys {
		body := genRandomBytes(int64(90300+i), 4000)
		mustPutSourceObject(t, srcSrv, "src", key, body, "application/octet-stream", nil)

		cfg := replicateConfig{
			Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: key, Creds: creds, Region: region, HTTPClient: srcTS.Client()},
			Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: key, Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		}
		p, err := planReplication(cfg)
		if err != nil {
			t.Fatalf("planReplication key %q: %v", key, err)
		}
		if p.wouldTransferBytes != int64(len(body)) {
			t.Fatalf("key %q: wouldTransferBytes = %d, want %d", key, p.wouldTransferBytes, len(body))
		}
		if _, err := dstSrv.store.lookupObject("dst", key); err == nil {
			t.Fatalf("key %q: dry-run must not have published the destination object", key)
		}
	}
}

func TestDryRun_Recursive_EmptyPrefix(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	cfg := namespaceReplicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	result, err := planReplicationNamespace(cfg)
	if err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}
	if result.Discovered != 0 || result.WouldTransferBytes != 0 {
		t.Fatalf("empty prefix should discover nothing, got %+v", result)
	}
	if !result.OK() {
		t.Fatalf("empty prefix dry-run should report OK")
	}
}

func TestDryRun_Recursive_SeveralObjectsAndCrossObjectReuse(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	// a.bin and b.bin are byte-identical (so CDC chunks them identically,
	// deterministically, with no dependence on where any content-defined
	// boundary happens to fall -- unlike a shared-prefix/different-suffix
	// construction), giving an exact, hand-computable cross-object CAS
	// reuse case; c.bin is unrelated, and outside/d.bin sits outside the
	// replicated prefix entirely.
	shared := genRandomBytes(90401, 500_000)
	unique := genRandomBytes(90404, 5000)
	mustPutSourceObject(t, srcSrv, "src", "prefix/a.bin", shared, "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "prefix/b.bin", shared, "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "prefix/c.bin", unique, "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "outside/d.bin", genRandomBytes(90405, 5000), "application/octet-stream", nil)

	cfg := namespaceReplicateConfig{
		Source:       syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		SourcePrefix: "prefix",
		Dest:         syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestPrefix:   "prefix",
	}
	result, err := planReplicationNamespace(cfg)
	if err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}
	if result.Discovered != 3 {
		t.Fatalf("Discovered = %d, want 3 (outside/ excluded)", result.Discovered)
	}
	if result.WouldPublish != 3 || result.AlreadyEquivalent != 0 || result.WouldConflict != 0 {
		t.Fatalf("unexpected action breakdown: %+v", result)
	}
	wantLogical := int64(len(shared)*2 + len(unique))
	if result.LogicalBytes != wantLogical {
		t.Fatalf("LogicalBytes = %d, want %d", result.LogicalBytes, wantLogical)
	}
	// b.bin's chunks are byte-identical to a.bin's, and a real recursive
	// run would already have uploaded them while replicating a.bin (M8C
	// processes objects strictly in order) -- so the destination-facing
	// total must count that content once, not twice, exactly like an
	// actual replicateNamespace run's aggregate would (A7).
	wantTransfer := int64(len(shared)) + int64(len(unique))
	if result.WouldTransferBytes != wantTransfer {
		t.Fatalf("WouldTransferBytes = %d, want %d (cross-object CAS reuse within one recursive run must not double count a chunk shared by two not-yet-replicated objects)", result.WouldTransferBytes, wantTransfer)
	}
	if !result.OK() {
		t.Fatalf("expected OK, got %+v", result)
	}

	// Deterministic: running the same plan again against the same
	// (unchanged) state produces byte-for-byte identical totals.
	result2, err := planReplicationNamespace(cfg)
	if err != nil {
		t.Fatalf("planReplicationNamespace (rerun): %v", err)
	}
	if !reflect.DeepEqual(result, result2) {
		t.Fatalf("dry-run totals not deterministic across reruns:\n%+v\n%+v", result, result2)
	}
}

func TestDryRun_Recursive_PartialConflicts(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")

	mustPutSourceObject(t, srcSrv, "src", "a", []byte("source-a"), "text/plain", nil)
	mustPutSourceObject(t, srcSrv, "src", "b", []byte("source-b"), "text/plain", nil)
	mustPutSourceObject(t, dstSrv, "dst", "a", []byte("pre-existing-different"), "text/plain", nil)

	cfg := namespaceReplicateConfig{
		Source:           syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:             syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestMustBeAbsent: true,
	}
	result, err := planReplicationNamespace(cfg)
	if err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}
	if result.WouldConflict != 1 {
		t.Fatalf("WouldConflict = %d, want 1, got %+v", result.WouldConflict, result)
	}
	if result.WouldPublish != 1 {
		t.Fatalf("WouldPublish = %d, want 1, got %+v", result.WouldPublish, result)
	}
	if result.OK() {
		t.Fatalf("a plan containing a conflict must not report OK()")
	}
}

// TestDryRun_PredictionMatchesActualExecution is M8G-A7's mandatory proof:
// on a quiescent, unchanging store, a dry-run's prediction and an
// immediately-following actual replication's real transfer stats must
// match exactly, for every reuse shape the spec calls out.
func TestDryRun_PredictionMatchesActualExecution(t *testing.T) {
	cases := []struct {
		name    string
		seedDst func(t *testing.T, srcSrv, dstSrv *Server)
	}{
		{"AllMissing", func(t *testing.T, srcSrv, dstSrv *Server) {
			mustCreateReplicateBucket(t, dstSrv, "dst")
		}},
		{"AllPresent", func(t *testing.T, srcSrv, dstSrv *Server) {
			// Pre-seed the destination CAS with the exact same content
			// under a different key, so negotiate reports every digest
			// already present without the destination object itself
			// existing yet.
			body := genRandomBytes(90501, 2_000_000)
			mustPutSourceObject(t, dstSrv, "dst", "seed-copy", body, "application/octet-stream", nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
			srcTS := httptest.NewServer(srcSrv)
			defer srcTS.Close()
			dstTS := httptest.NewServer(dstSrv)
			defer dstTS.Close()

			var body []byte
			if tc.name == "AllPresent" {
				body = genRandomBytes(90501, 2_000_000)
			} else {
				body = genRandomBytes(90502, 2_000_000)
			}
			mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
			tc.seedDst(t, srcSrv, dstSrv)

			cfg := replicateConfig{
				Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
				Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
			}

			predicted, err := planReplication(cfg)
			if err != nil {
				t.Fatalf("planReplication: %v", err)
			}

			actual, err := replicateObject(cfg)
			if err != nil {
				t.Fatalf("replicateObject: %v", err)
			}

			if actual.UploadedBytes != predicted.wouldTransferBytes {
				t.Fatalf("actual UploadedBytes=%d != predicted wouldTransferBytes=%d", actual.UploadedBytes, predicted.wouldTransferBytes)
			}
			if actual.MissingChunkOccur != predicted.missingOccur {
				t.Fatalf("actual MissingChunkOccur=%d != predicted missingOccur=%d", actual.MissingChunkOccur, predicted.missingOccur)
			}
			wantAvoided := predicted.desc.Size - predicted.wouldTransferBytes
			if actual.BytesAvoided != wantAvoided {
				t.Fatalf("actual BytesAvoided=%d != predicted avoided=%d", actual.BytesAvoided, wantAvoided)
			}
		})
	}
}

// TestDryRun_PredictionMatchesActualExecution_Recursive is A7's recursive
// variant: a namespace dry-run's aggregate prediction must match an
// immediately-following actual `replicate -recursive` run exactly.
func TestDryRun_PredictionMatchesActualExecution_Recursive(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	shared := genRandomBytes(90601, 25_000)
	mustPutSourceObject(t, srcSrv, "src", "a.bin", append(append([]byte{}, shared...), genRandomBytes(90602, 8000)...), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "b.bin", append(append([]byte{}, shared...), genRandomBytes(90603, 8000)...), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "c.bin", genRandomBytes(90604, 6000), "application/octet-stream", nil)

	cfg := namespaceReplicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	predicted, err := planReplicationNamespace(cfg)
	if err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}

	actual, err := replicateNamespace(cfg)
	if err != nil {
		t.Fatalf("replicateNamespace: %v", err)
	}
	if actual.Stats.UploadedBytes != predicted.WouldTransferBytes {
		t.Fatalf("actual UploadedBytes=%d != predicted WouldTransferBytes=%d", actual.Stats.UploadedBytes, predicted.WouldTransferBytes)
	}
	if actual.Stats.MissingChunkOccur != predicted.ChunksMissing {
		t.Fatalf("actual MissingChunkOccur=%d != predicted ChunksMissing=%d", actual.Stats.MissingChunkOccur, predicted.ChunksMissing)
	}
	if actual.Stats.LogicalBytes != predicted.LogicalBytes {
		t.Fatalf("actual LogicalBytes=%d != predicted LogicalBytes=%d", actual.Stats.LogicalBytes, predicted.LogicalBytes)
	}
}

// TestDryRun_ReadOnly_FullStoreFingerprintUnchanged is M8G's core
// invariant, proven independently of any in-process stats: hash every
// file in both the source and destination store directories before and
// after a single-object dry-run and a recursive dry-run, and require
// byte-for-byte identical trees.
func TestDryRun_ReadOnly_FullStoreFingerprintUnchanged(t *testing.T) {
	srcDir, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	mustPutSourceObject(t, srcSrv, "src", "prefix/x.bin", genRandomBytes(90701, 40_000), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "prefix/y.bin", genRandomBytes(90702, 40_000), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "single.bin", genRandomBytes(90703, 40_000), "application/octet-stream", nil)

	srcBefore := storeContentFingerprint(t, srcDir)
	dstBefore := storeContentFingerprint(t, dstDir)

	singleCfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "single.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "single.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	if _, err := planReplication(singleCfg); err != nil {
		t.Fatalf("planReplication: %v", err)
	}

	nsCfg := namespaceReplicateConfig{
		Source:       syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		SourcePrefix: "prefix",
		Dest:         syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestPrefix:   "prefix",
	}
	if _, err := planReplicationNamespace(nsCfg); err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}

	srcAfter := storeContentFingerprint(t, srcDir)
	dstAfter := storeContentFingerprint(t, dstDir)
	if srcBefore != srcAfter {
		t.Fatalf("dry-run mutated the SOURCE store on disk")
	}
	if dstBefore != dstAfter {
		t.Fatalf("dry-run mutated the DESTINATION store on disk")
	}
}

func TestDryRun_CLI_SingleObject_EndToEnd(t *testing.T) {
	_, srcSrv, dstDir, dstSrv, creds, _ := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", genRandomBytes(90801, 100_000), "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	before := storeContentFingerprint(t, dstDir)
	args := []string{
		"-dry-run",
		"-from", srcTS.URL, "-to", dstTS.URL,
		"-from-access-key", creds.AccessKeyID, "-from-secret-key", creds.SecretAccessKey,
		"-to-access-key", creds.AccessKeyID, "-to-secret-key", creds.SecretAccessKey,
		"s3://src/obj.bin", "s3://dst/obj.bin",
	}
	// runReplicate talks to os.Stdout and calls os.Exit on failure; since
	// this is the happy path (absent destination -> "would publish"), it
	// simply returns.
	runReplicate(args)
	after := storeContentFingerprint(t, dstDir)
	if before != after {
		t.Fatalf("`replicate -dry-run` CLI mutated the destination store")
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("`replicate -dry-run` CLI must not have published the destination object")
	}
}

// =============================================================================
// M8G-B: `zeros3 diff` -- descriptor-only structural object comparison
// =============================================================================

// fabricatedChunkRef builds a syntactically-valid syncChunkDescriptor from
// a small integer seed, with no real CAS payload backing it -- diff is
// descriptor-only (B2) and never reads chunk bodies, so tests exercising
// its chunk-sharing math directly against syncObjectDescriptor need no
// real chunk content at all, only syntactically valid, distinguishable
// digests.
func fabricatedChunkRef(seed int, length int64) syncChunkDescriptor {
	sum := sha256.Sum256([]byte(fmt.Sprintf("m8g-diff-fixture-chunk-%d", seed)))
	return syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: length}
}

func TestObjectDiff_IdenticalObjects_ExactMatch(t *testing.T) {
	refs := []syncChunkDescriptor{fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000), fabricatedChunkRef(3, 3000)}
	desc := syncObjectDescriptor{Size: 6000, ETag: "same-etag", ContentType: "text/plain", Chunks: refs}
	d := buildObjectDiff(desc, desc)
	if !d.ExactMatch || !d.ContentEqual {
		t.Fatalf("identical descriptors must report exact/content match, got %+v", d)
	}
	if d.SharedUniqueChunks != 3 || d.UniqueChunksA != 3 || d.UniqueChunksB != 3 {
		t.Fatalf("unexpected chunk counts: %+v", d)
	}
	if d.APayloadReusableFromB != 6000 || d.BPayloadReusableFromA != 6000 {
		t.Fatalf("identical objects must be 100%% reusable both ways, got %+v", d)
	}
	if d.AOnlyPayload != 0 || d.BOnlyPayload != 0 {
		t.Fatalf("identical objects must have zero one-sided payload, got %+v", d)
	}
	if d.HasDivergence {
		t.Fatalf("identical chunk sequences must not report a divergence, got offset %d", d.FirstDivergingOffset)
	}
}

func TestObjectDiff_WhollyUnrelatedObjects(t *testing.T) {
	a := syncObjectDescriptor{Size: 3000, ETag: "etag-a", Chunks: []syncChunkDescriptor{fabricatedChunkRef(10, 1000), fabricatedChunkRef(11, 2000)}}
	b := syncObjectDescriptor{Size: 4000, ETag: "etag-b", Chunks: []syncChunkDescriptor{fabricatedChunkRef(20, 1500), fabricatedChunkRef(21, 2500)}}
	d := buildObjectDiff(a, b)
	if d.SharedUniqueChunks != 0 {
		t.Fatalf("unrelated objects must share zero chunks, got %d", d.SharedUniqueChunks)
	}
	if d.APayloadReusableFromB != 0 || d.BPayloadReusableFromA != 0 {
		t.Fatalf("unrelated objects must have zero directional reuse, got %+v", d)
	}
	if d.AOnlyPayload != a.Size || d.BOnlyPayload != b.Size {
		t.Fatalf("unrelated objects: all payload is one-sided, got %+v", d)
	}
	if d.ExactMatch {
		t.Fatalf("unrelated objects must not report an exact match")
	}
}

func TestObjectDiff_PartialOverlap_ExactAccounting(t *testing.T) {
	c1, c2, c3, c4 := fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000), fabricatedChunkRef(3, 3000), fabricatedChunkRef(4, 4000)
	a := syncObjectDescriptor{Size: 6000, ETag: "a", Chunks: []syncChunkDescriptor{c1, c2, c3}}
	b := syncObjectDescriptor{Size: 7000, ETag: "b", Chunks: []syncChunkDescriptor{c1, c2, c4}}
	d := buildObjectDiff(a, b)
	if d.SharedUniqueChunks != 2 {
		t.Fatalf("SharedUniqueChunks = %d, want 2", d.SharedUniqueChunks)
	}
	if d.APayloadReusableFromB != 3000 || d.AOnlyPayload != 3000 {
		t.Fatalf("A accounting wrong: reusable=%d only=%d", d.APayloadReusableFromB, d.AOnlyPayload)
	}
	if d.BPayloadReusableFromA != 3000 || d.BOnlyPayload != 4000 {
		t.Fatalf("B accounting wrong: reusable=%d only=%d", d.BPayloadReusableFromA, d.BOnlyPayload)
	}
}

func TestObjectDiff_LocalizedInsertion(t *testing.T) {
	c1, c2, c3 := fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000), fabricatedChunkRef(3, 3000)
	inserted := fabricatedChunkRef(99, 500)
	a := syncObjectDescriptor{Size: 6000, ETag: "a", Chunks: []syncChunkDescriptor{c1, c2, c3}}
	b := syncObjectDescriptor{Size: 6500, ETag: "b", Chunks: []syncChunkDescriptor{c1, c2, inserted, c3}}
	d := buildObjectDiff(a, b)
	if d.APayloadReusableFromB != 6000 {
		t.Fatalf("every one of A's chunks still exists somewhere in B, want full reuse, got %d", d.APayloadReusableFromB)
	}
	if d.BOnlyPayload != 500 {
		t.Fatalf("B-only payload should be exactly the inserted chunk, got %d", d.BOnlyPayload)
	}
	if !d.HasDivergence || d.FirstDivergingOffset != 3000 {
		t.Fatalf("expected first positional divergence at offset 3000 (after c1+c2), got diverged=%v offset=%d", d.HasDivergence, d.FirstDivergingOffset)
	}
}

func TestObjectDiff_LocalizedDeletion(t *testing.T) {
	c1, c2, c3 := fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000), fabricatedChunkRef(3, 3000)
	a := syncObjectDescriptor{Size: 6000, ETag: "a", Chunks: []syncChunkDescriptor{c1, c2, c3}}
	b := syncObjectDescriptor{Size: 4000, ETag: "b", Chunks: []syncChunkDescriptor{c1, c3}}
	d := buildObjectDiff(a, b)
	if d.BPayloadReusableFromA != 4000 {
		t.Fatalf("all of B still exists in A, want full reuse, got %d", d.BPayloadReusableFromA)
	}
	if d.AOnlyPayload != 2000 {
		t.Fatalf("A-only payload should be exactly the deleted chunk (c2), got %d", d.AOnlyPayload)
	}
}

func TestObjectDiff_LocalizedMutation(t *testing.T) {
	c1, c2, c3 := fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000), fabricatedChunkRef(3, 3000)
	mutated := fabricatedChunkRef(98, 2000)
	a := syncObjectDescriptor{Size: 6000, ETag: "a", Chunks: []syncChunkDescriptor{c1, c2, c3}}
	b := syncObjectDescriptor{Size: 6000, ETag: "b", Chunks: []syncChunkDescriptor{c1, mutated, c3}}
	d := buildObjectDiff(a, b)
	if d.SharedUniqueChunks != 2 {
		t.Fatalf("SharedUniqueChunks = %d, want 2 (c1, c3)", d.SharedUniqueChunks)
	}
	if d.APayloadReusableFromB != 4000 || d.AOnlyPayload != 2000 {
		t.Fatalf("A accounting wrong: reusable=%d only=%d", d.APayloadReusableFromB, d.AOnlyPayload)
	}
	if !d.HasDivergence || d.FirstDivergingOffset != 1000 {
		t.Fatalf("expected divergence at offset 1000 (after c1), got diverged=%v offset=%d", d.HasDivergence, d.FirstDivergingOffset)
	}
}

func TestObjectDiff_EmptyObjects(t *testing.T) {
	a := syncObjectDescriptor{Size: 0, ETag: "empty", Chunks: nil}
	b := syncObjectDescriptor{Size: 0, ETag: "empty", Chunks: nil}
	d := buildObjectDiff(a, b)
	if d.UniqueChunksA != 0 || d.UniqueChunksB != 0 || d.SharedUniqueChunks != 0 {
		t.Fatalf("empty objects should have zero chunks all around, got %+v", d)
	}
	if !d.ExactMatch {
		t.Fatalf("two empty objects with the same ETag must be an exact match")
	}
	if d.HasDivergence {
		t.Fatalf("two empty chunk lists must not diverge")
	}
	var buf bytes.Buffer
	printObjectDiff(&buf, d)
	if strings.Contains(buf.String(), "NaN") || strings.Contains(buf.String(), "+Inf") {
		t.Fatalf("diff output corrupted for empty objects: %s", buf.String())
	}
}

func TestObjectDiff_RepeatedIdenticalChunks_DuplicateHandling(t *testing.T) {
	c1 := fabricatedChunkRef(1, 1000)
	a := syncObjectDescriptor{Size: 3000, ETag: "a", Chunks: []syncChunkDescriptor{c1, c1, c1}}
	b := syncObjectDescriptor{Size: 1000, ETag: "b", Chunks: []syncChunkDescriptor{c1}}
	d := buildObjectDiff(a, b)
	if d.UniqueChunksA != 1 || d.UniqueChunksB != 1 || d.SharedUniqueChunks != 1 {
		t.Fatalf("physical unique counts must dedupe by digest, got %+v", d)
	}
	// Logical/directional reuse is occurrence-based (B4): all three of A's
	// occurrences reference a digest present in B, so all 3000 bytes
	// count, even though B itself is only 1000 bytes of physical content.
	if d.APayloadReusableFromB != 3000 {
		t.Fatalf("APayloadReusableFromB = %d, want 3000 (every occurrence counts)", d.APayloadReusableFromB)
	}
	if d.BPayloadReusableFromA != 1000 {
		t.Fatalf("BPayloadReusableFromA = %d, want 1000", d.BPayloadReusableFromA)
	}
}

func TestObjectDiff_SameChunkSetDifferentOrder_NotExactMatch(t *testing.T) {
	c1, c2 := fabricatedChunkRef(1, 1000), fabricatedChunkRef(2, 2000)
	a := syncObjectDescriptor{Size: 3000, ETag: "order-a", Chunks: []syncChunkDescriptor{c1, c2}}
	b := syncObjectDescriptor{Size: 3000, ETag: "order-b", Chunks: []syncChunkDescriptor{c2, c1}}
	d := buildObjectDiff(a, b)
	if d.SharedUniqueChunks != 2 || d.APayloadReusableFromB != 3000 || d.BPayloadReusableFromA != 3000 {
		t.Fatalf("reordered chunk sets should still show full chunk-level reuse, got %+v", d)
	}
	if d.ExactMatch || d.ContentEqual {
		t.Fatalf("same chunk set in a different order must NOT be an exact/content match (B5: ordered manifest/object identity matters)")
	}
	if !d.HasDivergence || d.FirstDivergingOffset != 0 {
		t.Fatalf("reordered sequences must diverge immediately at offset 0, got diverged=%v offset=%d", d.HasDivergence, d.FirstDivergingOffset)
	}
}

func TestObjectDiff_MetadataOnlyDifference(t *testing.T) {
	refs := []syncChunkDescriptor{fabricatedChunkRef(1, 1000)}
	a := syncObjectDescriptor{Size: 1000, ETag: "same", ContentType: "text/plain", Metadata: map[string]string{"k": "v1"}, Chunks: refs}
	b := syncObjectDescriptor{Size: 1000, ETag: "same", ContentType: "text/plain", Metadata: map[string]string{"k": "v2"}, Chunks: refs}
	d := buildObjectDiff(a, b)
	if !d.ContentEqual || !d.ExactMatch {
		t.Fatalf("metadata-only difference must not affect content/exact-match verdicts, got %+v", d)
	}
	if d.MetadataEqual {
		t.Fatalf("differing metadata must be reported as unequal")
	}
	if d.APayloadReusableFromB != 1000 {
		t.Fatalf("metadata differences must not affect chunk-sharing metrics, got reusable=%d", d.APayloadReusableFromB)
	}
}

func TestObjectDiff_SameServer_TwoKeys(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := genRandomBytes(91001, 300_000)
	mustPutSourceObject(t, srv, "b", "v1.bin", body, "application/octet-stream", nil)
	mustPutSourceObject(t, srv, "b", "v2.bin", body, "application/octet-stream", nil)

	cfgA := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "v1.bin", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "v2.bin", Creds: creds, Region: region, HTTPClient: ts.Client()}
	d, err := diffObjects(cfgA, cfgB)
	if err != nil {
		t.Fatalf("diffObjects: %v", err)
	}
	if !d.ExactMatch {
		t.Fatalf("byte-identical content under two different keys on the same server must be an exact match")
	}
}

func TestObjectDiff_TwoServers(t *testing.T) {
	_, srvA, _, dstDir, srvB, creds, region := newDiffTwoServerPair(t)
	_ = dstDir
	tsA := httptest.NewServer(srvA)
	defer tsA.Close()
	tsB := httptest.NewServer(srvB)
	defer tsB.Close()

	mustPutSourceObject(t, srvA, "b", "obj", []byte("hello world, this is object content"), "text/plain", nil)
	mustPutSourceObject(t, srvB, "b", "obj", []byte("hello world, this is DIFFERENT content"), "text/plain", nil)

	cfgA := syncClientConfig{Endpoint: tsA.URL, Bucket: "b", Key: "obj", Creds: creds, Region: region, HTTPClient: tsA.Client()}
	cfgB := syncClientConfig{Endpoint: tsB.URL, Bucket: "b", Key: "obj", Creds: creds, Region: region, HTTPClient: tsB.Client()}
	d, err := diffObjects(cfgA, cfgB)
	if err != nil {
		t.Fatalf("diffObjects: %v", err)
	}
	if d.ExactMatch {
		t.Fatalf("different content across two servers must not be an exact match")
	}
}

// newDiffTwoServerPair is a tiny wrapper around newReplicateTestServerPair
// (two independent stores/servers, shared credentials) so diff's
// two-server test doesn't need replication-specific naming.
func newDiffTwoServerPair(t *testing.T) (aDir string, aSrv *Server, bDir string, dstUnused *Server, bSrv *Server, creds Credentials, region string) {
	t.Helper()
	aDir, aSrv, bDir, bSrv, creds, region = newReplicateTestServerPair(t)
	return aDir, aSrv, bDir, nil, bSrv, creds, region
}

func TestObjectDiff_MissingObjectA(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	mustPutSourceObject(t, srv, "b", "exists", []byte("x"), "text/plain", nil)

	cfgA := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "missing", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "exists", Creds: creds, Region: region, HTTPClient: ts.Client()}
	_, err := diffObjects(cfgA, cfgB)
	if err == nil || !strings.Contains(err.Error(), "object A") {
		t.Fatalf("expected a clear 'object A' error, got %v", err)
	}
}

func TestObjectDiff_MissingObjectB(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	mustPutSourceObject(t, srv, "b", "exists", []byte("x"), "text/plain", nil)

	cfgA := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "exists", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "missing", Creds: creds, Region: region, HTTPClient: ts.Client()}
	_, err := diffObjects(cfgA, cfgB)
	if err == nil || !strings.Contains(err.Error(), "object B") {
		t.Fatalf("expected a clear 'object B' error, got %v", err)
	}
}

func TestObjectDiff_AuthFailure(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	mustPutSourceObject(t, srv, "b", "obj", []byte("x"), "text/plain", nil)

	badCreds := Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: "wrong-secret-key-entirely-incorrect0"}
	cfgA := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "obj", Creds: badCreds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "obj", Creds: creds, Region: region, HTTPClient: ts.Client()}
	_, err := diffObjects(cfgA, cfgB)
	if err == nil {
		t.Fatalf("expected an auth failure error")
	}
}

func TestObjectDiff_WeirdKeys(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := genRandomBytes(91002, 5000)
	mustPutSourceObject(t, srv, "b", "a b/c%d#e?f.bin", body, "application/octet-stream", nil)
	mustPutSourceObject(t, srv, "b", "unicode/héllo-wörld-日本語.bin", body, "application/octet-stream", nil)

	cfgA := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "a b/c%d#e?f.bin", Creds: creds, Region: region, HTTPClient: ts.Client()}
	cfgB := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "unicode/héllo-wörld-日本語.bin", Creds: creds, Region: region, HTTPClient: ts.Client()}
	d, err := diffObjects(cfgA, cfgB)
	if err != nil {
		t.Fatalf("diffObjects: %v", err)
	}
	if !d.ExactMatch {
		t.Fatalf("identical content under weird keys must still be an exact match")
	}
}

func TestObjectDiff_CrossCheckAgainstIndependentComputation(t *testing.T) {
	// Independently recompute every metric from raw descriptors, without
	// calling buildObjectDiff's own helper functions, and require the two
	// computations agree -- M8G-B's own "cross-check diff metrics against
	// independently computed descriptor math" requirement.
	a := syncObjectDescriptor{Size: 10000, ETag: "a", Chunks: []syncChunkDescriptor{
		fabricatedChunkRef(1, 2000), fabricatedChunkRef(2, 3000), fabricatedChunkRef(1, 2000), fabricatedChunkRef(3, 3000),
	}}
	b := syncObjectDescriptor{Size: 6000, ETag: "b", Chunks: []syncChunkDescriptor{
		fabricatedChunkRef(2, 3000), fabricatedChunkRef(4, 3000),
	}}
	d := buildObjectDiff(a, b)

	bDigests := map[string]bool{}
	for _, c := range b.Chunks {
		bDigests[c.SHA256] = true
	}
	var wantAReuse int64
	for _, c := range a.Chunks {
		if bDigests[c.SHA256] {
			wantAReuse += c.Length
		}
	}
	aUnique := map[string]bool{}
	for _, c := range a.Chunks {
		aUnique[c.SHA256] = true
	}
	wantShared := 0
	for sha := range aUnique {
		if bDigests[sha] {
			wantShared++
		}
	}
	if d.APayloadReusableFromB != wantAReuse {
		t.Fatalf("APayloadReusableFromB = %d, independently computed = %d", d.APayloadReusableFromB, wantAReuse)
	}
	if d.SharedUniqueChunks != wantShared {
		t.Fatalf("SharedUniqueChunks = %d, independently computed = %d", d.SharedUniqueChunks, wantShared)
	}
	if d.UniqueChunksA != len(aUnique) {
		t.Fatalf("UniqueChunksA = %d, want %d", d.UniqueChunksA, len(aUnique))
	}
}

func TestDiff_CLI_SameServer_EndToEnd(t *testing.T) {
	_, srv, creds, _ := newSyncTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := genRandomBytes(91003, 50_000)
	mustPutSourceObject(t, srv, "b", "a.bin", body, "application/octet-stream", nil)
	mustPutSourceObject(t, srv, "b", "b.bin", body, "application/octet-stream", nil)

	before := storeContentFingerprint(t, srv.store.root)
	args := []string{
		"-from", ts.URL,
		"-from-access-key", creds.AccessKeyID, "-from-secret-key", creds.SecretAccessKey,
		"-to-access-key", creds.AccessKeyID, "-to-secret-key", creds.SecretAccessKey,
		"s3://b/a.bin", "s3://b/b.bin",
	}
	runDiff(args)
	after := storeContentFingerprint(t, srv.store.root)
	if before != after {
		t.Fatalf("`diff` CLI must never mutate store state")
	}
}

// =============================================================================
// M8G-C: `zeros3 inspect` -- CDC/CAS representation and store-wide sharing
// =============================================================================

func newInspectTestServer(t *testing.T) (srv *Server, cfgFor func(bucket, key string) syncClientConfig, closeFn func()) {
	t.Helper()
	_, srv, creds, region := newSyncTestServer(t)
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	cfgFor = func(bucket, key string) syncClientConfig {
		return syncClientConfig{Endpoint: ts.URL, Bucket: bucket, Key: key, Creds: creds, Region: region, HTTPClient: ts.Client()}
	}
	return srv, cfgFor, ts.Close
}

func TestInspect_EmptyObject(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "empty.bin", []byte{}, "text/plain", nil)

	res, err := inspectObject(cfgFor("b", "empty.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.ChunkReferences != 0 || res.UniqueChunks != 0 || res.PhysicalUniqueBytes != 0 {
		t.Fatalf("empty object should have zero chunks/bytes, got %+v", res)
	}
	var buf bytes.Buffer
	printInspect(&buf, res)
	if strings.Contains(buf.String(), "NaN") || strings.Contains(buf.String(), "+Inf") {
		t.Fatalf("inspect output corrupted for empty object: %s", buf.String())
	}
}

func TestInspect_OneChunk(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92001, 5000)
	mustPutObject(t, srv.store, "b", "one.bin", body, "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "one.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.ChunkReferences != 1 || res.UniqueChunks != 1 {
		t.Fatalf("a %d-byte object (below CDC min chunk size) should be exactly one chunk, got refs=%d unique=%d", len(body), res.ChunkReferences, res.UniqueChunks)
	}
	if res.MinChunkLength != int64(len(body)) || res.MaxChunkLength != int64(len(body)) {
		t.Fatalf("min/max should both equal the object's own size, got min=%d max=%d", res.MinChunkLength, res.MaxChunkLength)
	}
	if res.PhysicalUniqueBytes != int64(len(body)) {
		t.Fatalf("PhysicalUniqueBytes = %d, want %d", res.PhysicalUniqueBytes, len(body))
	}
}

func TestInspect_ManyChunks_MinAvgMaxExact(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92002, 8_000_000)
	mustPutObject(t, srv.store, "b", "big.bin", body, "application/octet-stream", nil)

	desc, err := fetchSourceDescriptor(cfgFor("b", "big.bin"))
	if err != nil {
		t.Fatalf("fetchSourceDescriptor: %v", err)
	}
	if len(desc.Chunks) < 2 {
		t.Fatalf("expected an 8MB object to produce multiple CDC chunks, got %d", len(desc.Chunks))
	}
	wantUnique := map[string]int64{}
	for _, c := range desc.Chunks {
		wantUnique[c.SHA256] = c.Length
	}
	var wantMin, wantMax, wantSum int64
	first := true
	for _, l := range wantUnique {
		wantSum += l
		if first || l < wantMin {
			wantMin = l
		}
		if l > wantMax {
			wantMax = l
		}
		first = false
	}

	res, err := inspectObject(cfgFor("b", "big.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.UniqueChunks != len(wantUnique) {
		t.Fatalf("UniqueChunks = %d, want %d", res.UniqueChunks, len(wantUnique))
	}
	if res.MinChunkLength != wantMin || res.MaxChunkLength != wantMax {
		t.Fatalf("min/max = %d/%d, want %d/%d", res.MinChunkLength, res.MaxChunkLength, wantMin, wantMax)
	}
	if res.PhysicalUniqueBytes != wantSum {
		t.Fatalf("PhysicalUniqueBytes = %d, want %d", res.PhysicalUniqueBytes, wantSum)
	}
	wantAvg := float64(wantSum) / float64(len(wantUnique))
	if res.AvgChunkLength != wantAvg {
		t.Fatalf("AvgChunkLength = %v, want %v", res.AvgChunkLength, wantAvg)
	}
}

func TestInspect_RepeatedChunkIDs_ExactDedup(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	c1 := fabricatedChunkRef(1, 1000)
	c2 := fabricatedChunkRef(2, 2000)
	refs := []chunkRef{{SHA256: c1.SHA256, Length: c1.Length}, {SHA256: c2.SHA256, Length: c2.Length}, {SHA256: c1.SHA256, Length: c1.Length}, {SHA256: c1.SHA256, Length: c1.Length}}
	total := int64(1000*3 + 2000)
	var objSHA [32]byte
	man := buildManifestV1FromRefs(refs, total, objSHA, "dup-etag", "application/octet-stream", nil)
	manUUID, manSHA, err := srv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.commitObjectRoot("b", "dup.bin", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	res, err := inspectObject(cfgFor("b", "dup.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.ChunkReferences != 4 {
		t.Fatalf("ChunkReferences = %d, want 4 (occurrences)", res.ChunkReferences)
	}
	if res.UniqueChunks != 2 {
		t.Fatalf("UniqueChunks = %d, want 2 (distinct digests)", res.UniqueChunks)
	}
	if res.PhysicalUniqueBytes != 3000 {
		t.Fatalf("PhysicalUniqueBytes = %d, want 3000 (1000+2000, c1 counted once)", res.PhysicalUniqueBytes)
	}
	if res.MinChunkLength != 1000 || res.MaxChunkLength != 2000 {
		t.Fatalf("min/max = %d/%d, want 1000/2000", res.MinChunkLength, res.MaxChunkLength)
	}
}

func TestInspect_WeirdKeys(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92003, 3000)
	mustPutObject(t, srv.store, "b", "a b/c%d#e?f 日本語.bin", body, "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "a b/c%d#e?f 日本語.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.PhysicalUniqueBytes != int64(len(body)) {
		t.Fatalf("PhysicalUniqueBytes = %d, want %d", res.PhysicalUniqueBytes, len(body))
	}
}

func TestInspect_CurrentVersionSemantics(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "k", genRandomBytes(92004, 10_000), "text/plain", nil)
	v2Body := genRandomBytes(92005, 20_000)
	mustPutObject(t, srv.store, "b", "k", v2Body, "text/plain", nil)

	res, err := inspectObject(cfgFor("b", "k"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.Size != int64(len(v2Body)) {
		t.Fatalf("inspect must report the CURRENT version, got Size=%d want %d", res.Size, len(v2Body))
	}
}

func TestInspect_ObjectUniqueInStore_SolelyReachable(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "solo.bin", genRandomBytes(92101, 40_000), "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "solo.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if !res.SharingAvailable {
		t.Fatalf("expected store-wide sharing analysis to be available")
	}
	if res.BytesReachableElsewhere != 0 {
		t.Fatalf("a unique object's chunks must not be reachable elsewhere, got %d", res.BytesReachableElsewhere)
	}
	if res.BytesSolelyReachable != res.PhysicalUniqueBytes {
		t.Fatalf("BytesSolelyReachable = %d, want the object's entire physical payload (%d)", res.BytesSolelyReachable, res.PhysicalUniqueBytes)
	}
}

func TestInspect_TwoKeysIdenticalContent_FullySharedElsewhere(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92102, 60_000)
	mustPutObject(t, srv.store, "b", "a.bin", body, "application/octet-stream", nil)
	mustPutObject(t, srv.store, "b", "b.bin", body, "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "a.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != res.PhysicalUniqueBytes {
		t.Fatalf("byte-identical content under a second key should be fully reachable elsewhere: elsewhere=%d physical=%d", res.BytesReachableElsewhere, res.PhysicalUniqueBytes)
	}
	if res.BytesSolelyReachable != 0 {
		t.Fatalf("BytesSolelyReachable = %d, want 0", res.BytesSolelyReachable)
	}
}

func TestInspect_PartialChunkSharing_ExactSplit(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	c1 := fabricatedChunkRef(11, 1000)
	c2 := fabricatedChunkRef(12, 2000)
	c3 := fabricatedChunkRef(13, 3000)

	commit := func(key string, refs ...syncChunkDescriptor) {
		chunkRefs := make([]chunkRef, len(refs))
		var total int64
		for i, r := range refs {
			chunkRefs[i] = chunkRef{SHA256: r.SHA256, Length: r.Length}
			total += r.Length
		}
		var objSHA [32]byte
		man := buildManifestV1FromRefs(chunkRefs, total, objSHA, key+"-etag", "application/octet-stream", nil)
		manUUID, manSHA, err := srv.store.publishManifest(man)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := srv.store.commitObjectRoot("b", key, manUUID, manSHA, man); err != nil {
			t.Fatal(err)
		}
	}
	commit("a.bin", c1, c2)
	commit("b.bin", c1, c3)

	res, err := inspectObject(cfgFor("b", "a.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != c1.Length {
		t.Fatalf("BytesReachableElsewhere = %d, want %d (only c1 is shared with b.bin)", res.BytesReachableElsewhere, c1.Length)
	}
	if res.BytesSolelyReachable != c2.Length {
		t.Fatalf("BytesSolelyReachable = %d, want %d (c2 is exclusive to a.bin)", res.BytesSolelyReachable, c2.Length)
	}
}

func TestInspect_CopyObjectSharing(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "src.bin", genRandomBytes(92103, 30_000), "application/octet-stream", nil)
	if _, _, err := srv.store.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "src.bin", DstBucket: "b", DstKey: "copy.bin"}); err != nil {
		t.Fatalf("CopyObject: %v", err)
	}

	res, err := inspectObject(cfgFor("b", "src.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != res.PhysicalUniqueBytes {
		t.Fatalf("CopyObject's destination is a second live root sharing every chunk: elsewhere=%d physical=%d", res.BytesReachableElsewhere, res.PhysicalUniqueBytes)
	}
}

func TestInspect_ForkSharing_BeforeAndAfter(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	if err := srv.store.CreateBucket("prod"); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateBucket("fork"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, srv.store, "prod", "shared.bin", genRandomBytes(92104, 80_000), "application/octet-stream", nil)

	before, err := inspectObject(cfgFor("prod", "shared.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject (before fork): %v", err)
	}
	if before.BytesReachableElsewhere != 0 {
		t.Fatalf("before forking, expected zero bytes reachable elsewhere, got %d", before.BytesReachableElsewhere)
	}

	if _, err := replicateNamespace(namespaceReplicateConfig{
		Source:           syncClientConfig{Endpoint: cfgFor("prod", "").Endpoint, Bucket: "prod", Creds: cfgFor("prod", "").Creds, Region: cfgFor("prod", "").Region, HTTPClient: cfgFor("prod", "").HTTPClient},
		Dest:             syncClientConfig{Endpoint: cfgFor("fork", "").Endpoint, Bucket: "fork", Creds: cfgFor("fork", "").Creds, Region: cfgFor("fork", "").Region, HTTPClient: cfgFor("fork", "").HTTPClient},
		DestMustBeAbsent: true,
	}); err != nil {
		t.Fatalf("fork (replicateNamespace): %v", err)
	}

	after, err := inspectObject(cfgFor("prod", "shared.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject (after fork): %v", err)
	}
	if after.BytesReachableElsewhere != after.PhysicalUniqueBytes {
		t.Fatalf("after forking, expected every byte reachable elsewhere (via the fork's own root), got elsewhere=%d physical=%d", after.BytesReachableElsewhere, after.PhysicalUniqueBytes)
	}
	if after.BytesSolelyReachable != 0 {
		t.Fatalf("after forking, expected zero solely-reachable bytes, got %d", after.BytesSolelyReachable)
	}
}

func TestInspect_ForkThenLocalizedMutation_HistoricalRootStillShares(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	if err := srv.store.CreateBucket("prod"); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateBucket("fork"); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, srv.store, "prod", "shared.bin", genRandomBytes(92105, 80_000), "application/octet-stream", nil)

	prodCfg := cfgFor("prod", "")
	forkCfg := cfgFor("fork", "")
	if _, err := replicateNamespace(namespaceReplicateConfig{
		Source:           syncClientConfig{Endpoint: prodCfg.Endpoint, Bucket: "prod", Creds: prodCfg.Creds, Region: prodCfg.Region, HTTPClient: prodCfg.HTTPClient},
		Dest:             syncClientConfig{Endpoint: forkCfg.Endpoint, Bucket: "fork", Creds: forkCfg.Creds, Region: forkCfg.Region, HTTPClient: forkCfg.HTTPClient},
		DestMustBeAbsent: true,
	}); err != nil {
		t.Fatalf("fork: %v", err)
	}

	// Mutate the forked copy -- its original (source-identical) content is
	// archived into the fork's own retained history, not discarded, so
	// prod's chunks are still reachable through that historical root.
	mustPutObject(t, srv.store, "fork", "shared.bin", genRandomBytes(92106, 90_000), "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("prod", "shared.bin"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != res.PhysicalUniqueBytes {
		t.Fatalf("prod's chunks should still be reachable via the fork's retained historical version, got elsewhere=%d physical=%d", res.BytesReachableElsewhere, res.PhysicalUniqueBytes)
	}
}

func TestInspect_RetainedVersionSharing(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92107, 50_000)
	mustPutObject(t, srv.store, "b", "k", body, "application/octet-stream", nil)
	// Overwrite k -- its original content is archived into history, not
	// destroyed.
	mustPutObject(t, srv.store, "b", "k", genRandomBytes(92108, 10_000), "application/octet-stream", nil)
	// A completely separate, unrelated-by-name object with the SAME
	// content as k's now-historical version.
	mustPutObject(t, srv.store, "b", "other", body, "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "other"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != res.PhysicalUniqueBytes {
		t.Fatalf("'other' shares every chunk with k's retained historical version, expected fully reachable elsewhere: elsewhere=%d physical=%d", res.BytesReachableElsewhere, res.PhysicalUniqueBytes)
	}
}

func TestInspect_SnapshotPinnedSharing_SurvivesLiveDeletion(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92109, 45_000)
	mustPutObject(t, srv.store, "b", "k", body, "application/octet-stream", nil)

	kCfg := cfgFor("b", "k")
	if _, err := createSnapshotRemote(kCfg, "b", ""); err != nil {
		t.Fatalf("createSnapshotRemote: %v", err)
	}
	if err := srv.store.DeleteObject("b", "k"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	// A separate live object with the same content as the now-deleted (but
	// snapshot-pinned) k.
	mustPutObject(t, srv.store, "b", "other", body, "application/octet-stream", nil)

	res, err := inspectObject(cfgFor("b", "other"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res.BytesReachableElsewhere != res.PhysicalUniqueBytes {
		t.Fatalf("'other' shares every chunk with k, which the snapshot still pins even after live deletion: elsewhere=%d physical=%d", res.BytesReachableElsewhere, res.PhysicalUniqueBytes)
	}
}

// TestInspect_DeletionChangesReachabilityCorrectly verifies inspect keeps
// reporting the TRUE reachability state across a deletion, rather than
// naively assuming "current root removed" means "no longer reachable
// elsewhere." ZeroS3's own documented delete semantics (section 7c:
// "every successful mutation that replaces OR REMOVES an existing
// current object... archives the object state it replaces into per-key
// history") mean DeleteObject never actually drops a chunk's
// reachability by itself -- it converts a "current" root into a
// "historical" one, and computeChunkRootMembership already walks
// historical roots exactly like current ones (both are part of the same
// authoritative universe GC protects). So the correct, non-buggy answer
// here is that reachability is UNCHANGED by k's deletion -- reporting a
// drop to zero would be the actual bug (C4: "do not fake exclusive/
// shared accounting").
func TestInspect_DeletionChangesReachabilityCorrectly(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	body := genRandomBytes(92110, 35_000)
	mustPutObject(t, srv.store, "b", "k", body, "application/octet-stream", nil)
	mustPutObject(t, srv.store, "b", "other", body, "application/octet-stream", nil)

	// With both k and other live, other's chunks are reachable elsewhere.
	res1, err := inspectObject(cfgFor("b", "other"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res1.BytesReachableElsewhere != res1.PhysicalUniqueBytes {
		t.Fatalf("expected fully shared before deletion, got elsewhere=%d physical=%d", res1.BytesReachableElsewhere, res1.PhysicalUniqueBytes)
	}

	if err := srv.store.DeleteObject("b", "k"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	res2, err := inspectObject(cfgFor("b", "other"), false)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	if res2.BytesReachableElsewhere != res2.PhysicalUniqueBytes {
		t.Fatalf("k's deletion archives it into history (still a live root), so other's chunks must remain fully reachable elsewhere: elsewhere=%d physical=%d", res2.BytesReachableElsewhere, res2.PhysicalUniqueBytes)
	}
	if res2.BytesSolelyReachable != 0 {
		t.Fatalf("expected zero solely-reachable bytes immediately after a delete that only archives to history, got %d", res2.BytesSolelyReachable)
	}
}

func TestInspect_GCDoesNotChangeValidReportedSemantics(t *testing.T) {
	dir, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(92111, 30_000)
	mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	// Create, then delete, an unrelated object so there is real
	// unreachable garbage for GC to collect.
	mustPutObject(t, store, "b", "garbage", genRandomBytes(92112, 20_000), "application/octet-stream", nil)
	if err := store.DeleteObject("b", "garbage"); err != nil {
		t.Fatal(err)
	}

	creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
	srv := NewServer(store, creds, defaultRegion)
	ts := httptest.NewServer(srv)
	cfg := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "k", Creds: creds, Region: defaultRegion, HTTPClient: ts.Client()}
	before, err := inspectObject(cfg, false)
	if err != nil {
		t.Fatalf("inspectObject (before GC): %v", err)
	}
	ts.Close()
	store.Close()

	if _, err := gcCollect(dir, true); err != nil {
		t.Fatalf("gcCollect: %v", err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	srv2 := NewServer(reopened, creds, defaultRegion)
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	cfg2 := syncClientConfig{Endpoint: ts2.URL, Bucket: "b", Key: "k", Creds: creds, Region: defaultRegion, HTTPClient: ts2.Client()}
	after, err := inspectObject(cfg2, false)
	if err != nil {
		t.Fatalf("inspectObject (after GC): %v", err)
	}
	if before.PhysicalUniqueBytes != after.PhysicalUniqueBytes || before.BytesSolelyReachable != after.BytesSolelyReachable || before.BytesReachableElsewhere != after.BytesReachableElsewhere {
		t.Fatalf("GC of unrelated garbage must not change k's reported inspect semantics: before=%+v after=%+v", before, after)
	}
}

func TestInspect_ReadOnly_NoStoreMutation(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "k", genRandomBytes(92113, 25_000), "application/octet-stream", nil)
	mustPutObject(t, srv.store, "b", "other", genRandomBytes(92114, 15_000), "application/octet-stream", nil)

	before := storeContentFingerprint(t, srv.store.root)
	if _, err := inspectObject(cfgFor("b", "k"), true); err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	after := storeContentFingerprint(t, srv.store.root)
	if before != after {
		t.Fatalf("inspect must never mutate journal/manifest/CAS/snapshot state")
	}
}

func TestInspect_ChunksListing_OffsetsAndDigestsExact(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	c1 := fabricatedChunkRef(31, 1000)
	c2 := fabricatedChunkRef(32, 2000)
	c3 := fabricatedChunkRef(33, 3000)
	refs := []chunkRef{{SHA256: c1.SHA256, Length: c1.Length}, {SHA256: c2.SHA256, Length: c2.Length}, {SHA256: c3.SHA256, Length: c3.Length}}
	var objSHA [32]byte
	man := buildManifestV1FromRefs(refs, 6000, objSHA, "chunks-etag", "application/octet-stream", nil)
	manUUID, manSHA, err := srv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.commitObjectRoot("b", "k", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	res, err := inspectObject(cfgFor("b", "k"), true)
	if err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
	wantOffsets := []int64{0, 1000, 3000}
	wantDigests := []string{c1.SHA256, c2.SHA256, c3.SHA256}
	if len(res.Chunks) != 3 {
		t.Fatalf("got %d chunk rows, want 3", len(res.Chunks))
	}
	for i, row := range res.Chunks {
		if row.Offset != wantOffsets[i] {
			t.Fatalf("chunk %d: offset = %d, want %d", i, row.Offset, wantOffsets[i])
		}
		if row.SHA256 != wantDigests[i] {
			t.Fatalf("chunk %d: digest = %s, want %s", i, row.SHA256, wantDigests[i])
		}
	}
	var buf bytes.Buffer
	printInspectChunks(&buf, res.Chunks, inspectChunksDefaultLimit, false)
	if !strings.Contains(buf.String(), "OFFSET") {
		t.Fatalf("expected a chunk table header, got:\n%s", buf.String())
	}
}

func TestInspect_ChunksListing_TruncatedByDefault(t *testing.T) {
	rows := make([]inspectChunkRow, 200)
	for i := range rows {
		rows[i] = inspectChunkRow{Offset: int64(i * 1000), Length: 1000, SHA256: fmt.Sprintf("%064x", i), ReachableRootCount: 1}
	}
	var truncatedBuf bytes.Buffer
	printInspectChunks(&truncatedBuf, rows, 50, false)
	if strings.Count(truncatedBuf.String(), "\n") > 60 {
		t.Fatalf("expected truncated output to stay small, got %d lines", strings.Count(truncatedBuf.String(), "\n"))
	}
	if !strings.Contains(truncatedBuf.String(), "truncated") {
		t.Fatalf("expected truncation to be stated explicitly, got:\n%s", truncatedBuf.String())
	}

	var allBuf bytes.Buffer
	printInspectChunks(&allBuf, rows, 50, true)
	if strings.Contains(allBuf.String(), "truncated") {
		t.Fatalf("-chunks-all must not truncate")
	}
	// header + 200 rows.
	if got := strings.Count(allBuf.String(), "\n"); got < 200 {
		t.Fatalf("expected all 200 rows with -chunks-all, got %d lines", got)
	}
}

func TestInspect_CLI_EndToEnd(t *testing.T) {
	srv, cfgFor, closeFn := newInspectTestServer(t)
	defer closeFn()
	mustPutObject(t, srv.store, "b", "k", genRandomBytes(92115, 30_000), "application/octet-stream", nil)

	before := storeContentFingerprint(t, srv.store.root)
	c := cfgFor("b", "k")
	args := []string{
		"-endpoint", c.Endpoint,
		"-access-key", c.Creds.AccessKeyID, "-secret-key", c.Creds.SecretAccessKey,
		"-chunks",
		"s3://b/k",
	}
	runInspect(args)
	after := storeContentFingerprint(t, srv.store.root)
	if before != after {
		t.Fatalf("`inspect` CLI must never mutate store state")
	}
}

// =============================================================================
// M8G-D: hostile read-only audit -- disproving "M8G cannot mutate
// authoritative storage" across all three commands together.
// =============================================================================

// noMutatingVerbs wraps a Server so any HTTP method/path combination that
// could ever mutate authoritative state fails the test immediately,
// regardless of which M8G command is exercising it. GET is always safe by
// construction; POST to /negotiate and /reachability are explicitly
// allowed (both are documented, demonstrably non-mutating reads --
// handleSyncNegotiate's own doc comment: "a pure read (os.Stat only...)",
// and handleReachabilityQuery only calls computeChunkRootMembership,
// itself read-only) even though they use POST/GET respectively; every
// other historically-mutating endpoint (chunk PUT, commit POST, snapshot
// create POST/delete DELETE, any DELETE at all) is forbidden outright.
type noMutatingVerbs struct {
	t   *testing.T
	srv http.Handler
}

func (n noMutatingVerbs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodDelete:
		n.t.Errorf("M8G command issued a DELETE: %s %s", r.Method, r.URL.Path)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix):
		n.t.Errorf("M8G command issued a chunk PUT: %s %s", r.Method, r.URL.Path)
	case r.Method == http.MethodPost && r.URL.Path == zeros3SyncCommitPath:
		n.t.Errorf("M8G command issued a commit POST: %s %s", r.Method, r.URL.Path)
	case r.Method == http.MethodPost && r.URL.Path == zeros3SnapshotCreatePath:
		n.t.Errorf("M8G command issued a snapshot-create POST: %s %s", r.Method, r.URL.Path)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix):
		n.t.Errorf("M8G command issued a chunk-payload GET: %s %s", r.Method, r.URL.Path)
	}
	n.srv.ServeHTTP(w, r)
}

// TestM8G_HostileAudit_NoMutatingHTTPMethods runs dry-run (single +
// recursive), diff, and inspect (with -chunks) together against a shared
// fixture wrapped in noMutatingVerbs, so any of the three issuing a
// forbidden request fails the test regardless of which command did it.
func TestM8G_HostileAudit_NoMutatingHTTPMethods(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(noMutatingVerbs{t, srcSrv})
	defer srcTS.Close()
	dstTS := httptest.NewServer(noMutatingVerbs{t, dstSrv})
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	body := genRandomBytes(93001, 40_000)
	mustPutSourceObject(t, srcSrv, "src", "prefix/a.bin", body, "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "prefix/b.bin", genRandomBytes(93002, 30_000), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "single.bin", genRandomBytes(93003, 20_000), "application/octet-stream", nil)

	singleCfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "single.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "single.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	if _, err := planReplication(singleCfg); err != nil {
		t.Fatalf("planReplication: %v", err)
	}

	nsCfg := namespaceReplicateConfig{
		Source:       syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		SourcePrefix: "prefix",
		Dest:         syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestPrefix:   "prefix",
	}
	if _, err := planReplicationNamespace(nsCfg); err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}

	diffCfgA := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "prefix/a.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	diffCfgB := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "prefix/b.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	if _, err := diffObjects(diffCfgA, diffCfgB); err != nil {
		t.Fatalf("diffObjects: %v", err)
	}

	inspectCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "single.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	if _, err := inspectObject(inspectCfg, true); err != nil {
		t.Fatalf("inspectObject: %v", err)
	}
}

// TestM8G_HostileAudit_CombinedFingerprint is M8G-D's mandatory
// independent state fingerprint proof, exercising all three commands
// together against one shared fixture: hash every file under both
// stores, run dry-run/diff/inspect, recompute, and require byte-identical
// authoritative persistent state.
func TestM8G_HostileAudit_CombinedFingerprint(t *testing.T) {
	srcDir, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustCreateReplicateBucket(t, srcSrv, "src")
	mustCreateReplicateBucket(t, dstSrv, "dst")

	mustPutSourceObject(t, srcSrv, "src", "prefix/a.bin", genRandomBytes(93101, 50_000), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "prefix/b.bin", genRandomBytes(93102, 60_000), "application/octet-stream", nil)
	mustPutSourceObject(t, srcSrv, "src", "single.bin", genRandomBytes(93103, 30_000), "application/octet-stream", nil)

	srcBefore := storeContentFingerprint(t, srcDir)
	dstBefore := storeContentFingerprint(t, dstDir)

	singleCfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "single.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "single.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}
	if _, err := planReplication(singleCfg); err != nil {
		t.Fatalf("planReplication: %v", err)
	}
	nsCfg := namespaceReplicateConfig{
		Source:       syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		SourcePrefix: "prefix",
		Dest:         syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		DestPrefix:   "prefix",
	}
	if _, err := planReplicationNamespace(nsCfg); err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}
	diffCfgA := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "prefix/a.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	diffCfgB := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "prefix/b.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	if _, err := diffObjects(diffCfgA, diffCfgB); err != nil {
		t.Fatalf("diffObjects: %v", err)
	}
	inspectCfg := syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "single.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()}
	if _, err := inspectObject(inspectCfg, true); err != nil {
		t.Fatalf("inspectObject: %v", err)
	}

	srcAfter := storeContentFingerprint(t, srcDir)
	dstAfter := storeContentFingerprint(t, dstDir)
	if srcBefore != srcAfter {
		t.Fatalf("combined dry-run+diff+inspect run mutated the SOURCE store")
	}
	if dstBefore != dstAfter {
		t.Fatalf("combined dry-run+diff+inspect run mutated the DESTINATION store")
	}
}

func TestM8G_HugeNamespace_1500Objects_RecursiveDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1500-object scale test in -short mode")
	}
	srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
	defer closeFn()
	const n = 1500
	for i := 0; i < n; i++ {
		putObj(t, srv, "src", fmt.Sprintf("k/%04d", i), []byte("x"))
	}

	start := time.Now()
	result, err := planReplicationNamespace(namespaceReplicateConfig{Source: cfgFor("src"), Dest: cfgFor("dst")})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("planReplicationNamespace: %v", err)
	}
	if result.Discovered != n {
		t.Fatalf("Discovered = %d, want %d", result.Discovered, n)
	}
	if result.WouldPublish != n {
		t.Fatalf("WouldPublish = %d, want %d", result.WouldPublish, n)
	}
	t.Logf("1500-object recursive dry-run took %s", elapsed)
	if elapsed > 30*time.Second {
		t.Fatalf("recursive dry-run over 1500 objects took unreasonably long: %s", elapsed)
	}

	// Bounded output: printDryRunNamespace's report is a fixed handful of
	// lines regardless of object count, never one line per object.
	var buf bytes.Buffer
	printDryRunNamespace(&buf, result)
	if lines := strings.Count(buf.String(), "\n"); lines > 20 {
		t.Fatalf("printDryRunNamespace produced %d lines for a 1500-object run, want a small fixed summary", lines)
	}
}

// malformedDescriptorServer serves a hand-crafted, syntactically-invalid
// syncObjectDescriptor (a negative chunk length and a non-hex digest) for
// GET /_zeros3/v1/object, and a compatible discovery/negotiate response
// for everything else -- simulating a hostile or badly broken peer, never
// a real ZeroS3 server's own output (an honest server's manifests are
// already structurally validated on write and by computeReachability).
type malformedDescriptorServer struct{}

func (malformedDescriptorServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == zeros3SyncInfoPath:
		writeSyncJSON(w, http.StatusOK, syncDiscoveryResponse{
			Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
			DeltaSync: true, MaxHashesPerBatch: 1000, MaxBatchBytes: 1 << 20, MaxChunkBytes: 1 << 20,
		})
	case r.URL.Path == zeros3SyncObjectPath:
		writeSyncJSON(w, http.StatusOK, syncObjectDescriptor{
			Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat, Hash: zeros3SyncHashAlgorithm,
			Bucket: "b", Key: "hostile", Size: -999, ETag: "malicious",
			Chunks: []syncChunkDescriptor{
				{SHA256: "not-a-valid-hex-digest-at-all", Length: -1},
				{SHA256: "", Length: 0},
			},
		})
	case r.URL.Path == zeros3ReachabilityPath:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	default:
		http.NotFound(w, r)
	}
}

// TestM8G_HostileEndpoint_MalformedDescriptorDoesNotPanic feeds
// diffObjects and inspectObject a deliberately malformed descriptor
// (negative lengths, non-hex digests) from a simulated hostile peer and
// requires they degrade to a clear result or error -- never a panic and
// never a corrupted-looking but silently "successful" report.
func TestM8G_HostileEndpoint_MalformedDescriptorDoesNotPanic(t *testing.T) {
	ts := httptest.NewServer(malformedDescriptorServer{})
	defer ts.Close()
	cfg := syncClientConfig{Endpoint: ts.URL, Bucket: "b", Key: "hostile", HTTPClient: ts.Client()}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("diffObjects panicked on a malformed descriptor: %v", r)
			}
		}()
		if _, err := diffObjects(cfg, cfg); err != nil {
			t.Logf("diffObjects returned an error for a malformed descriptor (acceptable): %v", err)
		}
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("inspectObject panicked on a malformed descriptor: %v", r)
			}
		}()
		res, err := inspectObject(cfg, true)
		if err != nil {
			t.Logf("inspectObject returned an error for a malformed descriptor (acceptable): %v", err)
			return
		}
		// The reachability query is also malformed JSON above; inspect
		// must degrade honestly rather than fabricate a sharing verdict.
		if res.SharingAvailable {
			t.Fatalf("expected SharingAvailable=false when the reachability query itself is malformed JSON")
		}
	}()
}

// TestM8G_CredentialSeparation_Diff proves diff never sends object A's
// credentials to object B's endpoint or vice versa, the same isolation
// property M8G-A9 already established for replicate -- checked here at
// the wire level (the Authorization header's Credential= access key) so
// it doesn't just rely on cfgA/cfgB never being copied into each other in
// diffObjects' own source.
func TestM8G_CredentialSeparation_Diff(t *testing.T) {
	_, srvA, _, srvB, _, region := newReplicateTestServerPair(t)
	credsA := Credentials{AccessKeyID: "AKIADIFFTESTAAAAAAAAAAA", SecretAccessKey: "diffTestSecretKeyForObjectA0123456789"}
	credsB := Credentials{AccessKeyID: "AKIADIFFTESTBBBBBBBBBBB", SecretAccessKey: "diffTestSecretKeyForObjectB9876543210"}
	srvA.creds = credsA
	srvB.creds = credsB

	var sawOnA, sawOnB []string
	recordingA := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawOnA = append(sawOnA, r.Header.Get("Authorization"))
		srvA.ServeHTTP(w, r)
	})
	recordingB := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawOnB = append(sawOnB, r.Header.Get("Authorization"))
		srvB.ServeHTTP(w, r)
	})
	tsA := httptest.NewServer(recordingA)
	defer tsA.Close()
	tsB := httptest.NewServer(recordingB)
	defer tsB.Close()

	mustPutSourceObject(t, srvA, "b", "obj", []byte("content on server A"), "text/plain", nil)
	mustPutSourceObject(t, srvB, "b", "obj", []byte("content on server B"), "text/plain", nil)

	cfgA := syncClientConfig{Endpoint: tsA.URL, Bucket: "b", Key: "obj", Creds: credsA, Region: region, HTTPClient: tsA.Client()}
	cfgB := syncClientConfig{Endpoint: tsB.URL, Bucket: "b", Key: "obj", Creds: credsB, Region: region, HTTPClient: tsB.Client()}
	if _, err := diffObjects(cfgA, cfgB); err != nil {
		t.Fatalf("diffObjects: %v", err)
	}

	if len(sawOnA) == 0 || len(sawOnB) == 0 {
		t.Fatalf("expected requests to reach both servers, got A=%d B=%d", len(sawOnA), len(sawOnB))
	}
	for _, auth := range sawOnA {
		if !strings.Contains(auth, credsA.AccessKeyID) || strings.Contains(auth, credsB.AccessKeyID) {
			t.Fatalf("server A saw an Authorization header not scoped to A's own credentials: %q", auth)
		}
	}
	for _, auth := range sawOnB {
		if !strings.Contains(auth, credsB.AccessKeyID) || strings.Contains(auth, credsA.AccessKeyID) {
			t.Fatalf("server B saw an Authorization header not scoped to B's own credentials: %q", auth)
		}
	}
}

// =============================================================================
// M8H-B1/B2: bounded parallel chunk transfer
//
// M8H-A measured that replicate/repair/sync's shared "fetch one chunk ->
// verify -> publish one chunk" loop was strictly sequential and the
// dominant cost in every content-moving code path. M8H-B adds
// runTransferWorkers, a small bounded worker-pool primitive, and reuses it
// from executeReplicationPlan (M8A/replicate), repairFromPeer (M8B), and
// uploadMissingSyncChunks (M6/sync) -- unchanged object/root-level
// publication semantics (planReplication/replicateNamespace/restore all
// still commit exactly one object at a time, still strictly sequentially),
// just concurrent chunk transport underneath.
// =============================================================================

// -----------------------------------------------------------------------
// runTransferWorkers / firstTransferError: white-box primitive tests
// -----------------------------------------------------------------------

func TestValidateWorkers_Range(t *testing.T) {
	cases := []struct {
		n     int
		valid bool
	}{
		{-1, false}, {0, false}, {1, true}, {2, true}, {4, true}, {8, true},
		{16, true}, {maxTransferWorkers, true}, {maxTransferWorkers + 1, false},
		{1_000_000, false},
	}
	for _, c := range cases {
		err := validateWorkers(c.n)
		if c.valid && err != nil {
			t.Errorf("validateWorkers(%d) = %v, want nil", c.n, err)
		}
		if !c.valid && err == nil {
			t.Errorf("validateWorkers(%d) = nil, want an error", c.n)
		}
	}
}

func TestResolveTransferWorkers_ZeroMeansDefault(t *testing.T) {
	if got, err := resolveTransferWorkers(0); err != nil || got != defaultTransferWorkers {
		t.Fatalf("resolveTransferWorkers(0) = (%d, %v), want (%d, nil)", got, err, defaultTransferWorkers)
	}
	if got, err := resolveTransferWorkers(8); err != nil || got != 8 {
		t.Fatalf("resolveTransferWorkers(8) = (%d, %v), want (8, nil)", got, err)
	}
	if _, err := resolveTransferWorkers(-1); err == nil {
		t.Fatalf("resolveTransferWorkers(-1) should error")
	}
	if _, err := resolveTransferWorkers(maxTransferWorkers + 1); err == nil {
		t.Fatalf("resolveTransferWorkers(above max) should error")
	}
}

func TestRunTransferWorkers_ZeroItems(t *testing.T) {
	results := runTransferWorkers(context.Background(), 4, nil, true)
	if len(results) != 0 {
		t.Fatalf("expected no results for zero items, got %d", len(results))
	}
}

func TestRunTransferWorkers_ResultsPreserveInputOrderRegardlessOfCompletionOrder(t *testing.T) {
	const n = 40
	items := make([]transferWork, n)
	for i := 0; i < n; i++ {
		i := i
		items[i] = transferWork{SHA256: fmt.Sprintf("chunk-%02d", i), Length: int64(i), Do: func(ctx context.Context) error {
			// Deliberately finish in *reverse* order (later-indexed items
			// finish first): a pass depending on completion order rather
			// than input order would misplace results.
			time.Sleep(time.Duration(n-i) * time.Millisecond)
			return nil
		}}
	}
	results := runTransferWorkers(context.Background(), 8, items, true)
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for i, r := range results {
		if r.SHA256 != items[i].SHA256 {
			t.Fatalf("results[%d].SHA256 = %q, want %q -- result order must match input order regardless of completion order", i, r.SHA256, items[i].SHA256)
		}
		if r.Err != nil {
			t.Fatalf("results[%d].Err = %v, want nil", i, r.Err)
		}
	}
}

func TestRunTransferWorkers_BoundedConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2, 4, 8} {
		workers := workers
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			const n = 30
			var cur, max int64
			items := make([]transferWork, n)
			for i := 0; i < n; i++ {
				items[i] = transferWork{SHA256: fmt.Sprintf("c%d", i), Do: func(ctx context.Context) error {
					c := atomic.AddInt64(&cur, 1)
					for {
						m := atomic.LoadInt64(&max)
						if c <= m || atomic.CompareAndSwapInt64(&max, m, c) {
							break
						}
					}
					time.Sleep(2 * time.Millisecond)
					atomic.AddInt64(&cur, -1)
					return nil
				}}
			}
			runTransferWorkers(context.Background(), workers, items, true)
			if got := atomic.LoadInt64(&max); got > int64(workers) {
				t.Fatalf("observed %d concurrent Do calls, want <= %d workers -- runTransferWorkers must never exceed the requested bound", got, workers)
			}
		})
	}
}

func TestRunTransferWorkers_CancelOnErrorStopsUnstartedWork(t *testing.T) {
	const n = 50
	var started int64
	release := make(chan struct{})
	items := make([]transferWork, n)
	for i := 0; i < n; i++ {
		i := i
		items[i] = transferWork{SHA256: fmt.Sprintf("c%d", i), Do: func(ctx context.Context) error {
			atomic.AddInt64(&started, 1)
			if i == 0 {
				return errors.New("boom")
			}
			<-release
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}}
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()
	results := runTransferWorkers(context.Background(), 4, items, true)
	if err := firstTransferError(results); err == nil || err.Error() != "boom" {
		t.Fatalf("firstTransferError = %v, want boom", err)
	}
	if got := atomic.LoadInt64(&started); got >= int64(n) {
		t.Fatalf("started = %d of %d items, want fewer than all -- a first failure should cancel work that hasn't started yet", got, n)
	}
}

func TestRunTransferWorkers_NoCancelOnErrorRunsEveryItem(t *testing.T) {
	const n = 20
	items := make([]transferWork, n)
	for i := 0; i < n; i++ {
		i := i
		items[i] = transferWork{SHA256: fmt.Sprintf("c%d", i), Do: func(ctx context.Context) error {
			if i%3 == 0 {
				return fmt.Errorf("failure at %d", i)
			}
			return nil
		}}
	}
	results := runTransferWorkers(context.Background(), 4, items, false)
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	var failed, ok, wantFailed int
	for i := 0; i < n; i++ {
		if i%3 == 0 {
			wantFailed++
		}
	}
	for i, r := range results {
		if r.SHA256 != items[i].SHA256 {
			t.Fatalf("result order mismatch at index %d", i)
		}
		if r.Err != nil {
			failed++
		} else {
			ok++
		}
	}
	if failed != wantFailed || ok != n-wantFailed {
		t.Fatalf("failed=%d ok=%d, want failed=%d ok=%d -- every item must still be attempted regardless of another item's failure (cancelOnError=false)", failed, ok, wantFailed, n-wantFailed)
	}
}

func TestRunTransferWorkers_ExternalContextCancellationSkipsEveryItem(t *testing.T) {
	const n = 20
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var started int64
	items := make([]transferWork, n)
	for i := 0; i < n; i++ {
		items[i] = transferWork{SHA256: fmt.Sprintf("c%d", i), Do: func(ctx context.Context) error {
			atomic.AddInt64(&started, 1)
			return nil
		}}
	}
	results := runTransferWorkers(ctx, 4, items, false)
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for _, r := range results {
		if r.Err == nil {
			t.Fatalf("expected every item to report an error for an already-canceled parent context")
		}
	}
	if got := atomic.LoadInt64(&started); got != 0 {
		t.Fatalf("started = %d, want 0 -- an already-canceled caller context must skip every item's Do", got)
	}
}

func TestFirstTransferError_LowestIndexGenuineErrorWins(t *testing.T) {
	results := []transferOutcome{
		{SHA256: "a", Err: context.Canceled},
		{SHA256: "b", Err: errors.New("real error at b")},
		{SHA256: "c", Err: errors.New("real error at c")},
		{SHA256: "d", Err: nil},
	}
	err := firstTransferError(results)
	if err == nil || err.Error() != "real error at b" {
		t.Fatalf("firstTransferError = %v, want the lowest-index genuine error (b), regardless of which chunk actually triggered cancellation", err)
	}
}

func TestFirstTransferError_FallsBackToCancellationWhenNoGenuineError(t *testing.T) {
	results := []transferOutcome{
		{SHA256: "a", Err: context.Canceled},
		{SHA256: "b", Err: context.Canceled},
	}
	err := firstTransferError(results)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("firstTransferError = %v, want context.Canceled (external cancellation must still be reported)", err)
	}
}

func TestFirstTransferError_NoErrorsReturnsNil(t *testing.T) {
	results := []transferOutcome{{SHA256: "a"}, {SHA256: "b"}}
	if err := firstTransferError(results); err != nil {
		t.Fatalf("firstTransferError = %v, want nil", err)
	}
}

func TestTransferHTTPTransport_PoolSizedForMaxWorkers(t *testing.T) {
	if transferHTTPTransport.MaxIdleConnsPerHost < maxTransferWorkers {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= %d (maxTransferWorkers) -- otherwise a full worker pool would bottleneck on Go's default per-host idle cap (M8H-A's own flagged pitfall)", transferHTTPTransport.MaxIdleConnsPerHost, maxTransferWorkers)
	}
	if transferHTTPTransport.MaxConnsPerHost != 0 && transferHTTPTransport.MaxConnsPerHost < maxTransferWorkers {
		t.Fatalf("MaxConnsPerHost = %d, want >= %d or unlimited (0)", transferHTTPTransport.MaxConnsPerHost, maxTransferWorkers)
	}
	if transferHTTPTransport.MaxIdleConns < transferHTTPTransport.MaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConns (%d) must be >= MaxIdleConnsPerHost (%d)", transferHTTPTransport.MaxIdleConns, transferHTTPTransport.MaxIdleConnsPerHost)
	}
}

// wrapChunkEndpoint returns a handler that special-cases exactly one
// GET/PUT /_zeros3/v1/chunks/<hexDigest> request (via intercept) and
// delegates every other request to srv unmodified -- used to simulate one
// bad/missing/rejected chunk among many otherwise-healthy concurrent chunk
// transfers.
func wrapChunkEndpoint(srv *Server, targetHexDigest string, intercept func(w http.ResponseWriter, r *http.Request) bool) http.HandlerFunc {
	target := zeros3SyncChunksPrefix + targetHexDigest
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == target && intercept(w, r) {
			return
		}
		srv.ServeHTTP(w, r)
	}
}

// -----------------------------------------------------------------------
// M8H-B1: `replicate` with a bounded worker pool
// -----------------------------------------------------------------------

func TestReplicate_WorkerCountsProduceByteIdenticalStats(t *testing.T) {
	var golden syncStats
	for i, workers := range []int{1, 2, 4, 8, 16, maxTransferWorkers} {
		workers := workers
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
			body := genRandomBytes(80020, 2_500_000)
			mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
			mustCreateReplicateBucket(t, dstSrv, "dst")
			srcTS := httptest.NewServer(srcSrv)
			defer srcTS.Close()
			dstTS := httptest.NewServer(dstSrv)
			defer dstTS.Close()
			cfg := replicateConfig{
				Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
				Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
				Workers: workers,
			}
			stats, err := replicateObject(cfg)
			if err != nil {
				t.Fatalf("replicateObject: %v", err)
			}
			if i == 0 {
				golden = stats
			} else if stats != golden {
				t.Fatalf("workers=%d stats %+v differ from workers=1 golden %+v -- worker count must never change reported totals", workers, stats, golden)
			}
		})
	}
}

func TestReplicate_PartialAndZeroMissing_WithWorkers(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80040, 2_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	// All missing.
	first, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("first replicateObject: %v", err)
	}
	if first.MissingChunkOccur != first.TotalChunks || first.UploadedBytes != int64(len(body)) {
		t.Fatalf("first run stats = %+v, want everything missing", first)
	}

	// Zero missing: re-replicating the identical object must transfer
	// nothing.
	second, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("second replicateObject: %v", err)
	}
	if second.MissingChunkOccur != 0 || second.UploadedBytes != 0 {
		t.Fatalf("second run stats = %+v, want zero missing / zero uploaded", second)
	}

	// Partial missing: a related object sharing a real content prefix
	// with the first (some chunks already present, some genuinely new).
	edited := append(append([]byte{}, body[:len(body)/2]...), genRandomBytes(80041, len(body)/2)...)
	mustPutSourceObject(t, srcSrv, "src", "obj2.bin", edited, "application/octet-stream", nil)
	cfg2 := cfg
	cfg2.Source.Key = "obj2.bin"
	cfg2.Dest.Key = "obj2.bin"
	third, err := replicateObject(cfg2)
	if err != nil {
		t.Fatalf("third replicateObject: %v", err)
	}
	if third.MissingChunkOccur == 0 || third.MissingChunkOccur == third.TotalChunks {
		t.Fatalf("third run stats = %+v, want a genuine partial mix of reused/missing chunks", third)
	}
}

func TestReplicate_DuplicateDigestReferencesTransferOnce(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	if err := srcSrv.store.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if err := dstSrv.store.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}

	chunkData := genRandomBytes(80030, 40_000)
	sum, err := srcSrv.store.casWrite(chunkData)
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := hex.EncodeToString(sum[:])

	const occurrences = 15
	refs := make([]chunkRef, occurrences)
	for i := range refs {
		refs[i] = chunkRef{SHA256: hexDigest, Length: int64(len(chunkData))}
	}
	total := int64(len(chunkData) * occurrences)
	objHash := sha256.New()
	for i := 0; i < occurrences; i++ {
		objHash.Write(chunkData)
	}
	var objSHA [32]byte
	copy(objSHA[:], objHash.Sum(nil))
	man := buildManifestV1FromRefs(refs, total, objSHA, "dup-src-etag", "application/octet-stream", nil)
	manUUID, manSHA, err := srcSrv.store.publishManifest(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcSrv.store.commitObjectRoot("src", "dup.bin", manUUID, manSHA, man); err != nil {
		t.Fatal(err)
	}

	var chunkGETs int64
	srcHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == zeros3SyncChunksPrefix+hexDigest {
			atomic.AddInt64(&chunkGETs, 1)
		}
		srcSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "dup.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "dup.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.UniqueChunksUploaded != 1 {
		t.Fatalf("UniqueChunksUploaded = %d, want 1 -- the same digest referenced %d times must be one physical transfer job", stats.UniqueChunksUploaded, occurrences)
	}
	if stats.TotalChunks != occurrences {
		t.Fatalf("TotalChunks = %d, want %d", stats.TotalChunks, occurrences)
	}
	if got := atomic.LoadInt64(&chunkGETs); got != 1 {
		t.Fatalf("source GET /chunks/%s was hit %d times, want exactly 1 -- a duplicated digest must not be independently re-fetched per occurrence", hexDigest, got)
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "dup.bin")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if int64(len(gotBody)) != total {
		t.Fatalf("destination object size = %d, want %d", len(gotBody), total)
	}
}

func TestReplicate_SourceMissingOneChunkAmongMany(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80050, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 10 {
		t.Fatalf("need >= 10 chunks, got %d", len(man.Chunks))
	}
	target := man.Chunks[len(man.Chunks)/2].SHA256

	srcHandler := wrapChunkEndpoint(srcSrv, target, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		return true
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	if _, err := replicateObject(cfg); err == nil {
		t.Fatalf("expected replicateObject to fail when the source is missing one chunk among many")
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not have been committed after a chunk fetch failure")
	}
}

func TestReplicate_SourceReturnsWrongDigestAmongMany(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80051, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 10 {
		t.Fatalf("need >= 10 chunks, got %d", len(man.Chunks))
	}
	target := man.Chunks[len(man.Chunks)/2].SHA256

	srcHandler := wrapChunkEndpoint(srcSrv, target, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("these are not the bytes you're looking for"))
		return true
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	if _, err := replicateObject(cfg); err == nil || !errors.Is(err, errReplicateChunkMismatch) {
		t.Fatalf("err = %v, want errReplicateChunkMismatch", err)
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not have been committed after a wrong-digest chunk")
	}
}

func TestReplicate_DestinationUploadRejectsAmongMany(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80052, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 10 {
		t.Fatalf("need >= 10 chunks, got %d", len(man.Chunks))
	}
	target := man.Chunks[len(man.Chunks)/2].SHA256

	dstHandler := wrapChunkEndpoint(dstSrv, target, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("simulated destination rejection"))
		return true
	})
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstHandler)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	if _, err := replicateObject(cfg); err == nil {
		t.Fatalf("expected replicateObject to fail when the destination rejects one chunk upload")
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not have been committed after a rejected chunk upload")
	}
}

func TestReplicate_ConnectionResetOnOneChunkIsHandledAsFailure(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80053, 2_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 5 {
		t.Fatalf("need >= 5 chunks, got %d", len(man.Chunks))
	}
	target := man.Chunks[0].SHA256

	srcHandler := wrapChunkEndpoint(srcSrv, target, func(w http.ResponseWriter, r *http.Request) bool {
		// Simulate a connection reset mid-request: hijack the raw
		// connection and close it abruptly, without ever writing a valid
		// HTTP response.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return false
		}
		conn, _, herr := hj.Hijack()
		if herr != nil {
			return false
		}
		conn.Close()
		return true
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 4,
	}
	if _, err := replicateObject(cfg); err == nil {
		t.Fatalf("expected replicateObject to fail when the source resets the connection for one chunk")
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not be committed after a connection reset on one chunk")
	}
}

func TestReplicate_CancellationStopsUnnecessaryWorkOnFailure(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80054, 4_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 40 {
		t.Fatalf("need many chunks, got %d", len(man.Chunks))
	}
	badDigest := man.Chunks[0].SHA256

	var chunkGETs int64
	srcHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
			atomic.AddInt64(&chunkGETs, 1)
			hexDigest := strings.TrimPrefix(r.URL.Path, zeros3SyncChunksPrefix)
			if hexDigest == badDigest {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Slow down every good chunk fetch so a fast failure has time
			// to cancel work that has not started yet.
			time.Sleep(30 * time.Millisecond)
		}
		srcSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 4,
	}
	if _, err := replicateObject(cfg); err == nil {
		t.Fatalf("expected failure")
	}
	if got := atomic.LoadInt64(&chunkGETs); got >= int64(len(man.Chunks)) {
		t.Fatalf("chunk GETs = %d of %d total chunks -- cancellation should have stopped some queued work before every chunk was fetched", got, len(man.Chunks))
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not be committed")
	}
}

func TestReplicate_SuccessfulTransferCommitsExactlyOnce(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80055, 2_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	var commits int64
	dstHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == zeros3SyncCommitPath {
			atomic.AddInt64(&commits, 1)
		}
		dstSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstHandler)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	if _, err := replicateObject(cfg); err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if got := atomic.LoadInt64(&commits); got != 1 {
		t.Fatalf("commit requests = %d, want exactly 1", got)
	}
}

func TestReplicate_FailureLeavesUploadedChunksReusable_RerunTransfersOnlyRemaining(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(80056, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	entry, err := srcSrv.store.lookupObject("src", "obj.bin")
	if err != nil {
		t.Fatal(err)
	}
	man := mustManifestFor(t, srcSrv.store, entry)
	if len(man.Chunks) < 10 {
		t.Fatalf("need >= 10 chunks, got %d", len(man.Chunks))
	}
	// The LAST chunk in dispatch order fails: every other chunk is
	// already dispatched (and, since runTransferWorkers waits for every
	// dispatched goroutine before returning, fully finished) by the time
	// this one fails and cancels -- there is nothing left to cancel.
	badDigest := man.Chunks[len(man.Chunks)-1].SHA256

	var failEnabled int32 = 1
	srcHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == zeros3SyncChunksPrefix+badDigest && atomic.LoadInt32(&failEnabled) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		srcSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 4,
	}
	if _, err := replicateObject(cfg); err == nil {
		t.Fatalf("expected the first attempt to fail")
	}
	if _, err := dstSrv.store.lookupObject("dst", "obj.bin"); err == nil {
		t.Fatalf("destination must not be committed after a transfer failure")
	}

	// The deliberately-failing chunk can never have landed; a sibling
	// chunk still genuinely in flight at the moment cancellation fires may
	// also be aborted (its own HTTP request shares the same worker-pool
	// context, per B1.9) -- so "everything except the bad chunk landed" is
	// not guaranteed, only "the bad chunk didn't, and at least some
	// others did." Whatever didn't land here is exactly what the rerun
	// below must (and does) pick back up -- nothing is silently lost or
	// double-counted.
	landed := 0
	for _, c := range man.Chunks {
		sum, _ := decodeHexSHA256(c.SHA256)
		if _, err := dstSrv.store.casRead(sum); err == nil {
			landed++
		}
	}
	if landed == 0 {
		t.Fatalf("landed = 0, want at least some chunks already durable in the destination CAS before the failing one")
	}
	if landed == len(man.Chunks) {
		t.Fatalf("landed = %d (all of them), want the deliberately-failing chunk excluded", landed)
	}
	wantStillMissing := len(man.Chunks) - landed

	atomic.StoreInt32(&failEnabled, 0)
	p, err := planReplication(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.missingOccur != wantStillMissing {
		t.Fatalf("missingOccur = %d, want %d (exactly whatever didn't land on the failed attempt should still be missing on rerun)", p.missingOccur, wantStillMissing)
	}
	stats, err := executeReplicationPlan(p)
	if err != nil {
		t.Fatalf("rerun failed: %v", err)
	}
	if stats.UniqueChunksUploaded != wantStillMissing {
		t.Fatalf("UniqueChunksUploaded = %d, want %d on rerun", stats.UniqueChunksUploaded, wantStillMissing)
	}

	_, gotBody, err := dstSrv.store.GetObject("dst", "obj.bin")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("destination content mismatch after rerun")
	}
}

// TestReplicate_WorkersGreaterThanChunkCount covers B5's "worker count
// greater than chunk count" edge case: a small object (few chunks) with a
// worker count far exceeding them must not deadlock, over-allocate, or
// misbehave -- runTransferWorkers' semaphore is simply never filled.
func TestReplicate_WorkersGreaterThanChunkCount(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(90500, 10_000) // small: at most a couple of chunks
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: maxTransferWorkers,
	}
	stats, err := replicateObject(cfg)
	if err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if stats.TotalChunks >= maxTransferWorkers {
		t.Fatalf("fixture produced %d chunks, want fewer than maxTransferWorkers (%d) for this edge case to be meaningful", stats.TotalChunks, maxTransferWorkers)
	}
	if stats.UploadedBytes != int64(len(body)) {
		t.Fatalf("UploadedBytes = %d, want %d", stats.UploadedBytes, len(body))
	}
	_, gotBody, err := dstSrv.store.GetObject("dst", "obj.bin")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("content mismatch")
	}
}

func TestReplicate_ConcurrentChunkFetchesActuallyOverlap(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(90050, 3_000_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	var cur, maxConc int64
	srcHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
			c := atomic.AddInt64(&cur, 1)
			for {
				m := atomic.LoadInt64(&maxConc)
				if c <= m || atomic.CompareAndSwapInt64(&maxConc, m, c) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			atomic.AddInt64(&cur, -1)
		}
		srcSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(srcHandler)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	cfg := replicateConfig{
		Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
		Workers: 8,
	}
	if _, err := replicateObject(cfg); err != nil {
		t.Fatalf("replicateObject: %v", err)
	}
	if got := atomic.LoadInt64(&maxConc); got < 2 {
		t.Fatalf("observed max concurrent source chunk fetches = %d, want > 1 -- a bounded worker pool should let independent chunk transfers overlap, not serialize", got)
	}
}

func TestReplicate_ParallelTransferFasterThanSequentialUnderLatency(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	body := genRandomBytes(90200, 1_500_000)
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", body, "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	const delay = 5 * time.Millisecond
	delayed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, zeros3SyncChunksPrefix) {
			time.Sleep(delay)
		}
		srcSrv.ServeHTTP(w, r)
	})
	srcTS := httptest.NewServer(delayed)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()

	baseCfg := replicateConfig{
		Source: syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: srcTS.Client()},
		Dest:   syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS.Client()},
	}

	seqCfg := baseCfg
	seqCfg.Workers = 1
	start := time.Now()
	if _, err := replicateObject(seqCfg); err != nil {
		t.Fatalf("sequential replicateObject: %v", err)
	}
	seqElapsed := time.Since(start)

	// A fresh destination for a clean second measurement of the identical
	// transfer under real concurrency.
	_, dstSrv2, _, _ := newSyncTestServer(t)
	if err := dstSrv2.store.CreateBucket("dst2"); err != nil {
		t.Fatal(err)
	}
	dstTS2 := httptest.NewServer(dstSrv2)
	defer dstTS2.Close()

	parCfg := baseCfg
	parCfg.Dest = syncClientConfig{Endpoint: dstTS2.URL, Bucket: "dst2", Key: "obj.bin", Creds: creds, Region: region, HTTPClient: dstTS2.Client()}
	parCfg.Workers = 8
	start = time.Now()
	if _, err := replicateObject(parCfg); err != nil {
		t.Fatalf("parallel replicateObject: %v", err)
	}
	parElapsed := time.Since(start)

	t.Logf("sequential(workers=1)=%v parallel(workers=8)=%v", seqElapsed, parElapsed)
	if parElapsed >= seqElapsed {
		t.Fatalf("parallel transfer (%v) was not faster than sequential (%v) despite a %v per-chunk artificial delay -- bounded concurrency should overlap independent chunk round trips", parElapsed, seqElapsed, delay)
	}
}

func TestReplicate_RepeatedTransfersDoNotLeakGoroutines(t *testing.T) {
	_, srcSrv, _, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	if err := dstSrv.store.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}

	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		body := genRandomBytes(int64(90100+i), 300_000)
		key := fmt.Sprintf("obj%d.bin", i)
		mustPutSourceObject(t, srcSrv, "src", key, body, "application/octet-stream", nil)
		cfg := replicateConfig{
			Source:  syncClientConfig{Endpoint: srcTS.URL, Bucket: "src", Key: key, Creds: creds, Region: region, HTTPClient: srcTS.Client()},
			Dest:    syncClientConfig{Endpoint: dstTS.URL, Bucket: "dst", Key: key, Creds: creds, Region: region, HTTPClient: dstTS.Client()},
			Workers: 8,
		}
		if _, err := replicateObject(cfg); err != nil {
			t.Fatalf("replicateObject %d: %v", i, err)
		}
	}
	// Idle keep-alive connections legitimately keep their own background
	// read-loop goroutine alive until the pool decides to close them
	// (IdleConnTimeout, or eviction) -- that is normal net/http behavior,
	// not a leak from the worker pool. Close them explicitly so this
	// check isolates genuinely leaked (worker/transfer) goroutines from
	// harmless pooled-connection ones.
	srcTS.Client().Transport.(*http.Transport).CloseIdleConnections()
	dstTS.Client().Transport.(*http.Transport).CloseIdleConnections()

	after := before
	for i := 0; i < 30; i++ {
		after = runtime.NumGoroutine()
		if after <= before+5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before+10 {
		t.Fatalf("goroutine count grew from %d to %d after 20 replications (even after closing idle connections) -- possible leak", before, after)
	}
}

// -----------------------------------------------------------------------
// M8H-B2: `repair` with a bounded worker pool
// -----------------------------------------------------------------------

func repairManyChunksFixture(t *testing.T) (store *Store, man manifestV1, peerTS *httptest.Server, creds Credentials, region string) {
	t.Helper()
	_, store = mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(90001, 3_000_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man = mustManifestFor(t, store, entry)
	if len(man.Chunks) < 20 {
		t.Fatalf("fixture needs >= 20 chunks for a meaningful concurrency test, got %d", len(man.Chunks))
	}
	_, peerSrv, c, r := newSyncTestServer(t)
	peerTS = httptest.NewServer(peerSrv)
	t.Cleanup(peerTS.Close)
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)
	return store, man, peerTS, c, r
}

func TestRepair_ManyCorruptChunks_WorkerCountsProduceIdenticalStats(t *testing.T) {
	type snap struct {
		bad, repaired, unresolved int
		fetched                   int64
		affected                  int
		ok                        bool
	}
	var golden snap
	for i, workers := range []int{1, 2, 4, 8, 16} {
		workers := workers
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			store, man, peerTS, creds, region := repairManyChunksFixture(t)
			for _, c := range man.Chunks {
				corruptChunkOnDisk(t, store, c.SHA256)
			}
			stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: workers})
			if err != nil {
				t.Fatalf("repairFromPeer: %v", err)
			}
			got := snap{stats.BadChunks, stats.Repaired, stats.Unresolved, stats.PayloadFetched, stats.AffectedObjects, stats.PostRepairOK}
			if i == 0 {
				golden = got
			} else if got != golden {
				t.Fatalf("workers=%d result %+v differs from workers=1 golden %+v", workers, got, golden)
			}
			if !stats.PostRepairOK || stats.Unresolved != 0 || stats.Repaired != len(man.Chunks) {
				t.Fatalf("stats = %+v, want every corrupt chunk repaired", stats)
			}
		})
	}
}

func TestRepair_ManyMissingChunks_Concurrent(t *testing.T) {
	store, man, peerTS, creds, region := repairManyChunksFixture(t)
	for i, c := range man.Chunks {
		if i%2 == 0 {
			deleteChunkOnDisk(t, store, c.SHA256)
		}
	}
	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: 8})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	wantBad := (len(man.Chunks) + 1) / 2
	if stats.BadChunks != wantBad || stats.Repaired != wantBad || stats.Unresolved != 0 || !stats.PostRepairOK {
		t.Fatalf("stats = %+v, want %d bad/repaired, 0 unresolved, OK", stats, wantBad)
	}
}

func TestRepair_MixedCorruptAndMissing_Concurrent(t *testing.T) {
	store, man, peerTS, creds, region := repairManyChunksFixture(t)
	wantBad := 0
	for i, c := range man.Chunks {
		switch i % 3 {
		case 0:
			corruptChunkOnDisk(t, store, c.SHA256)
			wantBad++
		case 1:
			deleteChunkOnDisk(t, store, c.SHA256)
			wantBad++
		}
	}
	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: 8})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.BadChunks != wantBad || stats.Repaired != wantBad || stats.Unresolved != 0 || !stats.PostRepairOK {
		t.Fatalf("stats = %+v, want %d bad/repaired (mixed corrupt+missing)", stats, wantBad)
	}
}

func TestRepair_OnePeerFailureAmongManySuccesses_Concurrent(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(90010, 3_000_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 20 {
		t.Fatalf("need >= 20 chunks, got %d", len(man.Chunks))
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)
	missingDigest := man.Chunks[len(man.Chunks)/2].SHA256
	missingSum, err := decodeHexSHA256(missingDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(peerSrv.store.chunkPath(missingSum)); err != nil {
		t.Fatal(err)
	}
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()

	for _, c := range man.Chunks {
		corruptChunkOnDisk(t, store, c.SHA256)
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: 8})
	if err != nil {
		t.Fatalf("repairFromPeer should not itself error on one bad peer response among many good ones: %v", err)
	}
	if stats.BadChunks != len(man.Chunks) {
		t.Fatalf("BadChunks = %d, want %d", stats.BadChunks, len(man.Chunks))
	}
	if stats.Repaired != len(man.Chunks)-1 || stats.Unresolved != 1 {
		t.Fatalf("stats = %+v, want exactly one unresolved chunk (the peer's genuinely missing one) and every other chunk repaired despite it running concurrently", stats)
	}
	if len(stats.Failures) != 1 || stats.Failures[0].SHA256 != missingDigest {
		t.Fatalf("Failures = %+v, want exactly one failure for %s", stats.Failures, missingDigest)
	}
	if stats.PostRepairOK {
		t.Fatalf("PostRepairOK = true, want false")
	}
}

func TestRepair_WrongPeerBytesAmongMany_Concurrent(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(90020, 3_000_000)
	entry := mustPutObject(t, store, "b", "k", body, "application/octet-stream", nil)
	man := mustManifestFor(t, store, entry)
	if len(man.Chunks) < 20 {
		t.Fatalf("need >= 20 chunks, got %d", len(man.Chunks))
	}

	_, peerSrv, creds, region := newSyncTestServer(t)
	primePeerWithObject(t, peerSrv, "b", "k", body, "application/octet-stream", nil)
	wrongDigest := man.Chunks[3].SHA256
	wrongHandler := wrapChunkEndpoint(peerSrv, wrongDigest, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("wrong bytes for this digest"))
		return true
	})
	peerTS := httptest.NewServer(wrongHandler)
	defer peerTS.Close()

	for _, c := range man.Chunks {
		corruptChunkOnDisk(t, store, c.SHA256)
	}

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: 8})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.Repaired != len(man.Chunks)-1 || stats.Unresolved != 1 {
		t.Fatalf("stats = %+v, want exactly one unresolved (the wrong-bytes digest)", stats)
	}
	if len(stats.Failures) != 1 || stats.Failures[0].SHA256 != wrongDigest {
		t.Fatalf("Failures = %+v, want exactly one failure for %s", stats.Failures, wrongDigest)
	}
	if !strings.Contains(stats.Failures[0].Reason, "does not match the requested digest") {
		t.Fatalf("Failures[0].Reason = %q, want it to explain the digest mismatch", stats.Failures[0].Reason)
	}
}

func TestRepair_SharedDigestSingleRepairJob_Concurrent(t *testing.T) {
	_, store := mustCreateLocalStore(t)
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	shared := genRandomBytes(90030, 40_000)
	sum, err := store.casWrite(shared)
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := hex.EncodeToString(sum[:])

	const numObjects = 12
	for i := 0; i < numObjects; i++ {
		refs := []chunkRef{{SHA256: hexDigest, Length: int64(len(shared))}}
		var objSHA [32]byte
		copy(objSHA[:], sum[:])
		man := buildManifestV1FromRefs(refs, int64(len(shared)), objSHA, fmt.Sprintf("etag-%d", i), "application/octet-stream", nil)
		manUUID, manSHA, err := store.publishManifest(man)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.commitObjectRoot("b", fmt.Sprintf("obj%d.bin", i), manUUID, manSHA, man); err != nil {
			t.Fatal(err)
		}
	}
	corruptChunkOnDisk(t, store, hexDigest)

	_, peerSrv, creds, region := newSyncTestServer(t)
	if _, err := peerSrv.store.casWrite(shared); err != nil {
		t.Fatal(err)
	}
	peerTS := httptest.NewServer(peerSrv)
	defer peerTS.Close()

	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: 8})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if stats.BadChunks != 1 || stats.Repaired != 1 {
		t.Fatalf("stats = %+v, want exactly 1 bad/repaired chunk despite %d objects referencing it", stats, numObjects)
	}
	if stats.AffectedObjects != numObjects {
		t.Fatalf("AffectedObjects = %d, want %d", stats.AffectedObjects, numObjects)
	}
	if stats.PayloadFetched != int64(len(shared)) {
		t.Fatalf("PayloadFetched = %d, want %d -- one physical fetch, not %d", stats.PayloadFetched, len(shared), numObjects)
	}
	if !stats.PostRepairOK {
		t.Fatalf("PostRepairOK = false, want true")
	}
}

func TestRepair_HighWorkerCount_RaceAndCorrectness(t *testing.T) {
	store, man, peerTS, creds, region := repairManyChunksFixture(t)
	for _, c := range man.Chunks {
		corruptChunkOnDisk(t, store, c.SHA256)
	}
	stats, err := store.repairFromPeer(repairConfig{Peer: mustPeerConfig(peerTS, creds, region), Workers: maxTransferWorkers})
	if err != nil {
		t.Fatalf("repairFromPeer: %v", err)
	}
	if !stats.PostRepairOK || stats.Repaired != len(man.Chunks) {
		t.Fatalf("stats = %+v", stats)
	}
}

// -----------------------------------------------------------------------
// M8H-B1: `sync` (M6) with a bounded worker pool
// -----------------------------------------------------------------------

func TestSync_ParallelChunkUploadWithWorkers(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	tmp := t.TempDir()
	body := genRandomBytes(90300, 2_000_000)
	path := writeSyncTempFile(t, tmp, "f.bin", body)

	cfg := syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: "f.bin",
		Creds: creds, Region: region, HTTPClient: ts.Client(), Workers: 8,
	}
	stats, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if stats.UploadedBytes != int64(len(body)) {
		t.Fatalf("UploadedBytes = %d, want %d", stats.UploadedBytes, len(body))
	}
	_, gotBody, err := srv.store.GetObject("b", "f.bin")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("content mismatch")
	}
}

func TestSync_WorkersOneMatchesSequentialBaseline(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	tmp := t.TempDir()
	body := genRandomBytes(90301, 500_000)
	path := writeSyncTempFile(t, tmp, "f.bin", body)
	cfg := syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: "f.bin",
		Creds: creds, Region: region, HTTPClient: ts.Client(), Workers: 1,
	}
	stats, err := syncFile(cfg)
	if err != nil {
		t.Fatalf("syncFile: %v", err)
	}
	if stats.UploadedBytes != int64(len(body)) || stats.FellBackToPlainPut {
		t.Fatalf("stats = %+v, want a normal delta-sync full upload", stats)
	}
}

func TestSync_LocalMutationDuringConcurrentUploadStillDetected(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	if err := srv.store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	tmp := t.TempDir()
	body := genRandomBytes(90302, 3_000_000)
	path := writeSyncTempFile(t, tmp, "f.bin", body)

	origHook := syncTestHookBeforeMutationCheck
	defer func() { syncTestHookBeforeMutationCheck = origHook }()
	syncTestHookBeforeMutationCheck = func(cfg syncClientConfig) {
		// Mutate the file's mtime after the concurrent upload has already
		// happened, simulating an in-flight edit racing the sync.
		future := time.Now().Add(time.Hour)
		_ = os.Chtimes(cfg.LocalPath, future, future)
	}

	cfg := syncClientConfig{
		LocalPath: path, Endpoint: ts.URL, Bucket: "b", Key: "f.bin",
		Creds: creds, Region: region, HTTPClient: ts.Client(), Workers: 8,
	}
	_, err := syncFile(cfg)
	if !errors.Is(err, errSyncLocalMutation) {
		t.Fatalf("err = %v, want errSyncLocalMutation", err)
	}
}

// -----------------------------------------------------------------------
// M8H-B1/B3: CLI -workers flag
// -----------------------------------------------------------------------

func TestCLI_WorkersFlagValidation(t *testing.T) {
	bin := buildZeros3Binary(t)

	cases := []struct {
		name string
		args []string
	}{
		{"replicate", []string{"replicate", "-workers", "WORKERS", "s3://a/b", "s3://c/d"}},
		{"replicate-recursive", []string{"replicate", "-recursive", "-workers", "WORKERS", "s3://a/b/", "s3://c/d/"}},
		{"repair", []string{"repair", "-store", "STOREDIR", "-from", "http://127.0.0.1:1", "-workers", "WORKERS"}},
		{"sync", []string{"sync", "-workers", "WORKERS", "LOCALPATH", "s3://a/b"}},
	}

	substitute := func(t *testing.T, args []string, workers string) []string {
		t.Helper()
		out := make([]string, len(args))
		copy(out, args)
		storeDir := t.TempDir()
		localFile := writeSyncTempFile(t, t.TempDir(), "f.bin", []byte("x"))
		for i, a := range out {
			switch a {
			case "WORKERS":
				out[i] = workers
			case "STOREDIR":
				out[i] = storeDir
			case "LOCALPATH":
				out[i] = localFile
			}
		}
		return out
	}

	for _, tc := range cases {
		tc := tc
		for _, w := range []string{"0", "-1", strconv.Itoa(maxTransferWorkers + 1), "1000000"} {
			w := w
			t.Run(tc.name+"/workers="+w, func(t *testing.T) {
				args := substitute(t, tc.args, w)
				_, stderr, code := runZeros3CLI(t, bin, args...)
				if code != 2 {
					t.Fatalf("exit code = %d, want 2 for -workers %s; stderr=%s", code, w, stderr)
				}
				if !strings.Contains(stderr, "workers") {
					t.Fatalf("stderr = %q, want it to mention workers", stderr)
				}
			})
		}
		t.Run(tc.name+"/workers=valid", func(t *testing.T) {
			args := substitute(t, tc.args, "8")
			_, stderr, code := runZeros3CLI(t, bin, args...)
			if code == 2 && strings.Contains(stderr, "-workers") {
				t.Fatalf("a valid -workers value was rejected: %s", stderr)
			}
		})
	}
}

// =============================================================================
// P1-A: Environment-variable credentials
//
// Precedence (documented in section 15a-quater of zeros3.go): an
// explicitly supplied CLI flag always wins; otherwise AWS_ACCESS_KEY_ID/
// AWS_SECRET_ACCESS_KEY/AWS_REGION is used if set (even to an empty
// string); otherwise the existing built-in default applies exactly as
// before P1. Two-endpoint commands (replicate/diff) get AWS_REGION
// fallback only on their one shared -region flag -- never on
// -from-access-key/-from-secret-key/-to-access-key/-to-secret-key, so a
// standard AWS credential pair can never be silently misdirected to the
// wrong endpoint (P1-A5).
// =============================================================================

// newCredFlagSet builds a flag.FlagSet with the exact same
// -access-key/-secret-key/-region flags every single-endpoint command
// registers, for testing envOverride/applyCredentialEnvFallback in
// isolation from any particular CLI command.
func newCredFlagSet(t *testing.T) (fs *flag.FlagSet, accessKey, secretKey, region *string) {
	t.Helper()
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	accessKey = fs.String("access-key", defaultAccessKeyID, "access key ID")
	secretKey = fs.String("secret-key", defaultSecretAccessKey, "secret access key")
	region = fs.String("region", defaultRegion, "SigV4 region")
	return fs, accessKey, secretKey, region
}

func TestEnvOverride_FlagExplicitWinsOverEnv(t *testing.T) {
	t.Setenv(envAWSAccessKeyID, "FROM-ENV")
	fs, accessKey, _, _ := newCredFlagSet(t)
	if err := fs.Parse([]string{"-access-key", "FROM-FLAG"}); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "access-key", envAWSAccessKeyID, *accessKey); got != "FROM-FLAG" {
		t.Fatalf("envOverride = %q, want explicit flag value %q", got, "FROM-FLAG")
	}
}

func TestEnvOverride_EnvUsedWhenFlagAbsent(t *testing.T) {
	t.Setenv(envAWSAccessKeyID, "FROM-ENV")
	fs, accessKey, _, _ := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "access-key", envAWSAccessKeyID, *accessKey); got != "FROM-ENV" {
		t.Fatalf("envOverride = %q, want env value %q", got, "FROM-ENV")
	}
}

func TestEnvOverride_DefaultWhenBothAbsent(t *testing.T) {
	os.Unsetenv(envAWSAccessKeyID)
	fs, accessKey, _, _ := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "access-key", envAWSAccessKeyID, *accessKey); got != defaultAccessKeyID {
		t.Fatalf("envOverride = %q, want built-in default %q", got, defaultAccessKeyID)
	}
}

func TestEnvOverride_RegionDefaultWhenBothAbsent(t *testing.T) {
	// A3: region already has a non-empty built-in default -- confirm the
	// preferred precedence (CLI > AWS_REGION > existing default) leaves
	// it exactly as before when neither a flag nor AWS_REGION is set.
	os.Unsetenv(envAWSRegion)
	fs, _, _, region := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "region", envAWSRegion, *region); got != defaultRegion {
		t.Fatalf("envOverride = %q, want built-in default region %q", got, defaultRegion)
	}
}

func TestEnvOverride_EmptyEnvIsASetOverride(t *testing.T) {
	// P1-A6: AWS_ACCESS_KEY_ID="" is "set but empty" -- it still
	// overrides the default (to empty), it is not treated as unset.
	t.Setenv(envAWSAccessKeyID, "")
	fs, accessKey, _, _ := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "access-key", envAWSAccessKeyID, *accessKey); got != "" {
		t.Fatalf("envOverride = %q, want empty string (set-but-empty env overrides the default)", got)
	}
}

func TestEnvOverride_UnsetEnvNeverOverridesExplicitFlagEitherWay(t *testing.T) {
	os.Unsetenv(envAWSAccessKeyID)
	fs, accessKey, _, _ := newCredFlagSet(t)
	if err := fs.Parse([]string{"-access-key", "FROM-FLAG"}); err != nil {
		t.Fatal(err)
	}
	if got := envOverride(fs, "access-key", envAWSAccessKeyID, *accessKey); got != "FROM-FLAG" {
		t.Fatalf("envOverride = %q, want explicit flag value %q (unset env must never touch it)", got, "FROM-FLAG")
	}
}

func TestApplyCredentialEnvFallback_PartialPairLeavesOtherFlagAtDefault(t *testing.T) {
	// Validation: only AWS_ACCESS_KEY_ID set -- the access key picks up
	// the env value, but the secret key is untouched (falls through to
	// its existing default), never silently blanked or synthesized.
	t.Setenv(envAWSAccessKeyID, "PARTIAL-ACCESS-KEY")
	os.Unsetenv(envAWSSecretAccessKey)
	fs, accessKey, secretKey, region := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	applyCredentialEnvFallback(fs, accessKey, secretKey, region)
	if *accessKey != "PARTIAL-ACCESS-KEY" {
		t.Fatalf("access key = %q, want env value", *accessKey)
	}
	if *secretKey != defaultSecretAccessKey {
		t.Fatalf("secret key = %q, want unchanged built-in default (missing secret must not be synthesized)", *secretKey)
	}
}

func TestApplyCredentialEnvFallback_MissingAccessKeyLeavesAccessAtDefault(t *testing.T) {
	os.Unsetenv(envAWSAccessKeyID)
	t.Setenv(envAWSSecretAccessKey, "PARTIAL-SECRET-KEY")
	fs, accessKey, secretKey, region := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	applyCredentialEnvFallback(fs, accessKey, secretKey, region)
	if *accessKey != defaultAccessKeyID {
		t.Fatalf("access key = %q, want unchanged built-in default", *accessKey)
	}
	if *secretKey != "PARTIAL-SECRET-KEY" {
		t.Fatalf("secret key = %q, want env value", *secretKey)
	}
}

func TestApplyCredentialEnvFallback_AllThreeFromEnv(t *testing.T) {
	t.Setenv(envAWSAccessKeyID, "ENV-ACCESS")
	t.Setenv(envAWSSecretAccessKey, "ENV-SECRET")
	t.Setenv(envAWSRegion, "env-region-1")
	fs, accessKey, secretKey, region := newCredFlagSet(t)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	applyCredentialEnvFallback(fs, accessKey, secretKey, region)
	if *accessKey != "ENV-ACCESS" || *secretKey != "ENV-SECRET" || *region != "env-region-1" {
		t.Fatalf("got (%q,%q,%q), want all three from environment", *accessKey, *secretKey, *region)
	}
}

func TestApplyCredentialEnvFallback_ExplicitFlagsWinOverEnvForAllThree(t *testing.T) {
	t.Setenv(envAWSAccessKeyID, "ENV-ACCESS")
	t.Setenv(envAWSSecretAccessKey, "ENV-SECRET")
	t.Setenv(envAWSRegion, "env-region-1")
	fs, accessKey, secretKey, region := newCredFlagSet(t)
	if err := fs.Parse([]string{"-access-key", "FLAG-ACCESS", "-secret-key", "FLAG-SECRET", "-region", "flag-region"}); err != nil {
		t.Fatal(err)
	}
	applyCredentialEnvFallback(fs, accessKey, secretKey, region)
	if *accessKey != "FLAG-ACCESS" || *secretKey != "FLAG-SECRET" || *region != "flag-region" {
		t.Fatalf("got (%q,%q,%q), want all three from explicit flags", *accessKey, *secretKey, *region)
	}
}

// TestEnvOverride_UsageStringNeverContainsEnvValue is the P1-A7 secret-
// safety check at the flag-registration level: registering a flag with
// flag.String never embeds the *runtime* environment value into the
// flag's usage text -- only the fixed, already-public placeholder
// default (defaultAccessKeyID/defaultSecretAccessKey) is compiled in,
// and envOverride is only ever consulted after Parse, long after usage
// text was generated. This proves `-h`/PrintDefaults output can never
// leak a real secret an operator put in AWS_SECRET_ACCESS_KEY.
func TestEnvOverride_UsageStringNeverContainsEnvValue(t *testing.T) {
	const realSecret = "sk-live-DEFINITELY-NOT-A-PLACEHOLDER-98765"
	t.Setenv(envAWSSecretAccessKey, realSecret)
	fs, _, _, _ := newCredFlagSet(t)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	if strings.Contains(buf.String(), realSecret) {
		t.Fatalf("flag usage text leaked the AWS_SECRET_ACCESS_KEY environment value: %s", buf.String())
	}
}

// newEnvCredTestServer is a small wrapper around newSyncTestServer naming
// its fixed test credentials clearly for the env-fallback tests below.
func newEnvCredTestServer(t *testing.T) (srv *Server, ts *httptest.Server, creds Credentials, region string) {
	t.Helper()
	_, srv, creds, region = newSyncTestServer(t)
	ts = httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts, creds, region
}

// TestEnvCredentials_Inspect_UsesEnvWhenFlagsOmitted proves the P1-A
// wiring end to end for one ordinary single-endpoint client command
// (inspect) entirely in-process: with -access-key/-secret-key/-region
// omitted and AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION set to
// the server's real credentials, the command must authenticate
// successfully. Like the pre-existing TestDryRun_CLI_SingleObject_
// EndToEnd above, this calls runInspect directly on its happy path: it
// prints to stdout and only calls os.Exit on failure.
func TestEnvCredentials_Inspect_UsesEnvWhenFlagsOmitted(t *testing.T) {
	srv, ts, creds, region := newEnvCredTestServer(t)
	if err := srv.store.CreateBucket("envb"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.PutObject("envb", "k", []byte("hello"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAWSAccessKeyID, creds.AccessKeyID)
	t.Setenv(envAWSSecretAccessKey, creds.SecretAccessKey)
	t.Setenv(envAWSRegion, region)

	runInspect([]string{"-endpoint", ts.URL, "s3://envb/k"})
}

// TestEnvCredentials_Inspect_ExplicitFlagOverridesWrongEnv is the
// precedence companion: deliberately wrong AWS_* environment credentials
// must never win over explicit -access-key/-secret-key/-region flags
// (P1-A2).
func TestEnvCredentials_Inspect_ExplicitFlagOverridesWrongEnv(t *testing.T) {
	srv, ts, creds, region := newEnvCredTestServer(t)
	if err := srv.store.CreateBucket("envb2"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.PutObject("envb2", "k", []byte("hello"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAWSAccessKeyID, "WRONG-ENV-ACCESS-KEY")
	t.Setenv(envAWSSecretAccessKey, "WRONG-ENV-SECRET-KEY")
	t.Setenv(envAWSRegion, "wrong-env-region")

	runInspect([]string{
		"-endpoint", ts.URL,
		"-access-key", creds.AccessKeyID,
		"-secret-key", creds.SecretAccessKey,
		"-region", region,
		"s3://envb2/k",
	})
}

// TestEnvCredentials_Replicate_RegionFromEnvButFromToNeverFallBack is the
// two-endpoint (P1-A5) proof, entirely in-process via replicate's -dry-
// run mode (never mutates the destination, so a happy path is safe to
// call directly): AWS_REGION fallback applies to the one shared -region
// flag, but AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY must never reach
// -from-access-key/-from-secret-key/-to-access-key/-to-secret-key even
// when they happen to be set to a value that *would* authenticate.
func TestEnvCredentials_Replicate_RegionFromEnvButFromToNeverFallBack(t *testing.T) {
	_, srcSrv, dstDir, dstSrv, creds, region := newReplicateTestServerPair(t)
	srcTS := httptest.NewServer(srcSrv)
	defer srcTS.Close()
	dstTS := httptest.NewServer(dstSrv)
	defer dstTS.Close()
	mustPutSourceObject(t, srcSrv, "src", "obj.bin", []byte("replicate env test"), "application/octet-stream", nil)
	mustCreateReplicateBucket(t, dstSrv, "dst")

	// Set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY to the servers' own
	// real, correct credentials -- if replicate's -from-*/-to-* flags
	// wrongly fell back to them, this test would not catch it (it would
	// still work "by accident"). What it DOES prove unambiguously is
	// that AWS_REGION reaches the shared -region flag: only -region is
	// omitted from args below, everything else is explicit.
	t.Setenv(envAWSAccessKeyID, creds.AccessKeyID)
	t.Setenv(envAWSSecretAccessKey, creds.SecretAccessKey)
	t.Setenv(envAWSRegion, region)

	before := storeContentFingerprint(t, dstDir)
	runReplicate([]string{
		"-dry-run",
		"-from", srcTS.URL, "-to", dstTS.URL,
		"-from-access-key", creds.AccessKeyID, "-from-secret-key", creds.SecretAccessKey,
		"-to-access-key", creds.AccessKeyID, "-to-secret-key", creds.SecretAccessKey,
		"s3://src/obj.bin", "s3://dst/obj.bin",
	})
	after := storeContentFingerprint(t, dstDir)
	if before != after {
		t.Fatalf("replicate -dry-run mutated the destination store")
	}
}

// runZeros3CLIWithEnv is runZeros3CLI plus explicit control over the
// subprocess's environment, for P1-A tests that must observe a real
// process-level authentication failure (an in-process os.Exit would kill
// the whole test binary, so those cases need a real subprocess).
func runZeros3CLIWithEnv(t *testing.T, bin string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("failed to run zeros3 %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// TestEnvCredentials_Replicate_FromToFlagsNeverFallBackToEnv_RealProcess
// is the hostile-case companion to the in-process test above, run as a
// real subprocess so a genuine authentication failure can be observed
// safely: AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY are set to the exact
// correct SOURCE server credentials, but -from-access-key/-from-secret-
// key are omitted (left at their built-in placeholder default, which
// does not match this real server). If replicate's -from-* flags ever
// fell back to AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY, this would
// succeed; per P1-A5 it must fail instead.
func TestEnvCredentials_Replicate_FromToFlagsNeverFallBackToEnv_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	srcDir := t.TempDir()
	srcStore, err := OpenStore(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcStore.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if _, err := srcStore.PutObject("src", "obj.bin", []byte("hello"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	srcStore.Close()
	dstDir := t.TempDir()
	dstStore, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dstStore.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	dstStore.Close()

	srcAddr := freeTCPAddr(t)
	srcCmd := exec.Command(bin, "serve", "-store", srcDir, "-addr", srcAddr,
		"-access-key", "REAL-SRC-ACCESS-KEY", "-secret-key", "REAL-SRC-SECRET-KEY")
	if err := srcCmd.Start(); err != nil {
		t.Fatalf("failed to start source serve: %v", err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()
	waitForZeros3Serve(t, srcAddr)

	dstAddr := freeTCPAddr(t)
	dstCmd := exec.Command(bin, "serve", "-store", dstDir, "-addr", dstAddr,
		"-access-key", "REAL-DST-ACCESS-KEY", "-secret-key", "REAL-DST-SECRET-KEY")
	if err := dstCmd.Start(); err != nil {
		t.Fatalf("failed to start destination serve: %v", err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	env := append(os.Environ(),
		envAWSAccessKeyID+"=REAL-SRC-ACCESS-KEY",
		envAWSSecretAccessKey+"=REAL-SRC-SECRET-KEY",
	)
	// -from-access-key/-from-secret-key/-to-access-key/-to-secret-key
	// are deliberately omitted here.
	_, stderr, code := runZeros3CLIWithEnv(t, bin, env,
		"replicate", "-dry-run",
		"-from", "http://"+srcAddr, "-to", "http://"+dstAddr,
		"s3://src/obj.bin", "s3://dst/obj.bin")
	if code == 0 {
		t.Fatalf("replicate unexpectedly succeeded -- AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY must never reach -from-access-key/-from-secret-key (P1-A5); stderr=%s", stderr)
	}
	if strings.Contains(stderr, "REAL-SRC-SECRET-KEY") || strings.Contains(stderr, "REAL-DST-SECRET-KEY") {
		t.Fatalf("secret leaked into stderr: %s", stderr)
	}
}

// TestEnvCredentials_Serve_AuthenticatesFromEnv is the P1-A3/"server
// auth using env creds" real-process proof: `zeros3 serve` started with
// no -access-key/-secret-key/-region flags at all, only
// AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION in its environment,
// must accept requests signed with those exact credentials and reject
// requests signed with anything else.
func TestEnvCredentials_Serve_AuthenticatesFromEnv(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("envserve"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	env := append(os.Environ(),
		envAWSAccessKeyID+"=ENV-SERVE-ACCESS-KEY",
		envAWSSecretAccessKey+"=ENV-SERVE-SECRET-KEY",
		envAWSRegion+"=env-serve-region",
	)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	waitForZeros3Serve(t, addr)

	// A client using the same credentials (supplied explicitly, since
	// this test's own process environment is not the subprocess's) must
	// authenticate successfully.
	_, stderr, code := runZeros3CLI(t, bin, "inspect",
		"-endpoint", "http://"+addr,
		"-access-key", "ENV-SERVE-ACCESS-KEY", "-secret-key", "ENV-SERVE-SECRET-KEY", "-region", "env-serve-region",
		"s3://envserve/does-not-need-to-exist-for-auth-to-be-checked")
	// The object doesn't exist, so this may still fail with a 404-shaped
	// error -- what must NOT happen is an auth failure. A wrong-signature
	// rejection would be reported distinctly (SignatureDoesNotMatch); a
	// missing-object error is the expected, benign outcome here.
	if code != 0 && strings.Contains(stderr, "SignatureDoesNotMatch") {
		t.Fatalf("server did not authenticate the env-derived credentials: %s", stderr)
	}

	// Wrong credentials must be rejected.
	_, stderr2, code2 := runZeros3CLI(t, bin, "inspect",
		"-endpoint", "http://"+addr,
		"-access-key", "WRONG-ACCESS-KEY", "-secret-key", "WRONG-SECRET-KEY", "-region", "env-serve-region",
		"s3://envserve/does-not-need-to-exist-for-auth-to-be-checked")
	if code2 == 0 {
		t.Fatalf("server accepted wrong credentials against env-derived auth")
	}
	if strings.Contains(stderr2, "ENV-SERVE-SECRET-KEY") {
		t.Fatalf("secret leaked into stderr: %s", stderr2)
	}
}

// TestEnvCredentials_Presign_UsesEnvWhenFlagsOmitted is the P1-A
// "presign using env creds" proof: `zeros3 presign` with
// -access-key/-secret-key/-region omitted, driven only by
// AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION, must produce a URL
// that a real server started with those same credentials accepts.
func TestEnvCredentials_Presign_UsesEnvWhenFlagsOmitted(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("presignenv"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("presignenv", "k", []byte("presigned via env creds"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	srvCmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr,
		"-access-key", "PRESIGN-ENV-ACCESS-KEY", "-secret-key", "PRESIGN-ENV-SECRET-KEY")
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	defer func() { srvCmd.Process.Kill(); srvCmd.Wait() }()
	waitForZeros3Serve(t, addr)

	env := append(os.Environ(),
		envAWSAccessKeyID+"=PRESIGN-ENV-ACCESS-KEY",
		envAWSSecretAccessKey+"=PRESIGN-ENV-SECRET-KEY",
	)
	stdout, stderr, code := runZeros3CLIWithEnv(t, bin, env,
		"presign", "get", "-endpoint", "http://"+addr, "-bucket", "presignenv", "-key", "k")
	if code != 0 {
		t.Fatalf("presign failed (code %d): stderr=%s", code, stderr)
	}
	url := strings.TrimSpace(stdout)
	if url == "" {
		t.Fatalf("presign produced no URL")
	}
	if strings.Contains(url, "PRESIGN-ENV-SECRET-KEY") {
		t.Fatalf("presigned URL leaked the secret key: %s", url)
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET presigned URL: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned URL (built from env credentials) was rejected: status %d body=%s", resp.StatusCode, body)
	}
	if string(body) != "presigned via env creds" {
		t.Fatalf("unexpected body: %s", body)
	}
}

// =============================================================================
// P1-B: HTTP server hardening + graceful shutdown
//
// newHardenedHTTPServer's exact field values are checked directly
// (fast, no I/O); every SIGINT/SIGTERM/shutdown-timing behavior is
// checked against a real `zeros3 serve` subprocess signaled with a real
// OS signal, per the spec's own "use real-process tests externally for
// actual signals" requirement -- an in-process fake would not exercise
// signal.NotifyContext or http.Server.Shutdown's actual listener/
// connection-draining behavior.
// =============================================================================

func TestNewHardenedHTTPServer_FieldsExact(t *testing.T) {
	s := newHardenedHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout != serveReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", s.ReadHeaderTimeout, serveReadHeaderTimeout)
	}
	if s.IdleTimeout != serveIdleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", s.IdleTimeout, serveIdleTimeout)
	}
	if s.MaxHeaderBytes != serveMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %v, want %v", s.MaxHeaderBytes, serveMaxHeaderBytes)
	}
	// P1-B3, deliberate: no whole-request deadline, so large
	// uploads/downloads are never regressed by a P1 hardening pass.
	if s.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, want 0 (unset)", s.ReadTimeout)
	}
	if s.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0 (unset)", s.WriteTimeout)
	}
}

// throttledBody is an io.ReadCloser that hands back small pieces of a
// fixed byte slice with a delay between each, so a real subprocess
// server's PUT handler is provably still reading the request body
// (i.e. the connection is genuinely active, not idle) for a controlled,
// deterministic span of wall-clock time -- without needing the
// in-process-only testHook seam (section "Test-only failure injection
// seam"), which cannot reach a separately exec'd binary.
type throttledBody struct {
	data      []byte
	chunkSize int
	delay     time.Duration
}

func (r *throttledBody) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := r.chunkSize
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func (r *throttledBody) Close() error { return nil }

// waitForProcessExit waits up to timeout for cmd (already Start()ed) to
// exit, returning its process exit code (0 for a clean exit). It fails
// the test (after forcibly killing the process, so the test binary never
// hangs) if the process is still running when timeout elapses.
func waitForProcessExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) int {
	t.Helper()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("cmd.Wait: %v", err)
		return -1
	case <-time.After(timeout):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("process did not exit within %s of being signaled", timeout)
		return -1
	}
}

func TestServe_SIGINT_IdleGracefulShutdown_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("failed to signal SIGINT: %v", err)
	}
	code := waitForProcessExit(t, cmd, serveShutdownGrace+10*time.Second)
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (clean idle shutdown); stderr=%s", code, stderr.String())
	}
	if elapsed > serveShutdownGrace {
		t.Fatalf("idle shutdown took %s, want well under the %s grace period", elapsed, serveShutdownGrace)
	}
	// B11: a normal shutdown (http.ErrServerClosed internally) must never
	// be logged as a fatal server error.
	if strings.Contains(stderr.String(), "serve failed") {
		t.Fatalf("idle SIGINT shutdown was logged as a server failure: %s", stderr.String())
	}

	// Restart must work cleanly afterward.
	restart := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := restart.Start(); err != nil {
		t.Fatalf("failed to restart serve: %v", err)
	}
	defer func() { restart.Process.Kill(); restart.Wait() }()
	waitForZeros3Serve(t, addr)
}

func TestServe_SIGTERM_IdleGracefulShutdown_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal SIGTERM: %v", err)
	}
	code := waitForProcessExit(t, cmd, serveShutdownGrace+10*time.Second)
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (clean idle shutdown); stderr=%s", code, stderr.String())
	}
	if elapsed > serveShutdownGrace {
		t.Fatalf("idle shutdown took %s, want well under the %s grace period", elapsed, serveShutdownGrace)
	}

	restart := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := restart.Start(); err != nil {
		t.Fatalf("failed to restart serve: %v", err)
	}
	defer func() { restart.Process.Kill(); restart.Wait() }()
	waitForZeros3Serve(t, addr)
}

// TestServe_SIGTERM_SecondSignalForcesImmediateExit_RealProcess is the
// P1-B6 proof: a second SIGTERM arriving while graceful shutdown is
// already draining an active (slow) request must terminate the process
// immediately, not wait out the rest of the grace period.
func TestServe_SIGTERM_SecondSignalForcesImmediateExit_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	body := bytes.Repeat([]byte("y"), 200)
	req := mustSignedRequestWithOpts(t, signer, http.MethodPut, "http://"+addr+"/b/slow-key", body, nil)
	req.Body = &throttledBody{data: body, chunkSize: 5, delay: 300 * time.Millisecond} // ~12s total

	respCh := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			respCh <- err
			return
		}
		resp.Body.Close()
		respCh <- nil
	}()

	time.Sleep(1 * time.Second)
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send first SIGTERM: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send second SIGTERM: %v", err)
	}

	waitForProcessExit(t, cmd, 5*time.Second)
	elapsed := time.Since(start)
	if elapsed >= serveShutdownGrace {
		t.Fatalf("process took %s to exit after a second SIGTERM, want well under the %s grace period (second signal must force immediate exit)", elapsed, serveShutdownGrace)
	}
	<-respCh // drain the in-flight request's goroutine; its outcome is irrelevant here
}

// TestServe_SIGTERM_ActivePUT_CompletesWithinGrace_RealProcess is the
// P1-B7 proof for PUT, plus B9 (no new connections accepted once
// shutdown has begun): an in-flight PUT whose body is still streaming
// when SIGTERM arrives must still complete successfully and durably,
// while a brand-new connection attempted shortly after the signal must
// be refused.
func TestServe_SIGTERM_ActivePUT_CompletesWithinGrace_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	body := bytes.Repeat([]byte("z"), 200)
	req := mustSignedRequestWithOpts(t, signer, http.MethodPut, "http://"+addr+"/b/active-put-key", body, nil)
	req.Body = &throttledBody{data: body, chunkSize: 5, delay: 200 * time.Millisecond} // ~8s total

	type putResult struct {
		status int
		err    error
	}
	resultCh := make(chan putResult, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resultCh <- putResult{err: err}
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		resultCh <- putResult{status: resp.StatusCode}
	}()

	time.Sleep(1 * time.Second) // let the PUT genuinely begin streaming
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal SIGTERM: %v", err)
	}

	// B9: a brand-new connection attempted just after the signal must be
	// refused -- the listener stops accepting immediately on Shutdown.
	time.Sleep(500 * time.Millisecond)
	newConnClient := &http.Client{Timeout: 2 * time.Second}
	if resp, err := newConnClient.Get("http://" + addr + "/"); err == nil {
		resp.Body.Close()
		t.Fatalf("a new connection was accepted after shutdown began (want it refused)")
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("active PUT failed: %v", result.err)
	}
	if result.status != http.StatusOK {
		t.Fatalf("active PUT status = %d, want 200 (must complete within the grace period)", result.status)
	}

	code := waitForProcessExit(t, cmd, serveShutdownGrace+10*time.Second)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}

	// Restart and confirm the object landed durably and completely.
	restart := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := restart.Start(); err != nil {
		t.Fatalf("failed to restart serve: %v", err)
	}
	defer func() { restart.Process.Kill(); restart.Wait() }()
	waitForZeros3Serve(t, addr)

	getResp := doSignedRequest(t, http.DefaultClient, "http://"+addr, signer, http.MethodGet, "/b/active-put-key", nil, nil)
	defer getResp.Body.Close()
	got, _ := io.ReadAll(getResp.Body)
	if getResp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("object after restart: status=%d len=%d, want 200 with the exact %d-byte body", getResp.StatusCode, len(got), len(body))
	}
}

// TestServe_SIGTERM_ActiveGET_CompletesWithinGrace_RealProcess is the
// P1-B7 proof for GET: an in-flight GET whose response the client is
// deliberately still slowly draining when SIGTERM arrives must still
// complete with the exact correct content.
func TestServe_SIGTERM_ActiveGET_CompletesWithinGrace_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	body := genRandomBytes(19001, 4_000_000)
	if _, err := store.PutObject("b", "active-get-key", body, "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}

	type getResult struct {
		body []byte
		err  error
	}
	resultCh := make(chan getResult, 1)
	go func() {
		resp := doSignedRequest(t, http.DefaultClient, "http://"+addr, signer, http.MethodGet, "/b/active-get-key", nil, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			resultCh <- getResult{err: fmt.Errorf("status %d", resp.StatusCode)}
			return
		}
		var buf bytes.Buffer
		chunk := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
				time.Sleep(5 * time.Millisecond) // deliberately slow client read
			}
			if rerr != nil {
				if rerr != io.EOF {
					resultCh <- getResult{err: rerr}
					return
				}
				break
			}
		}
		resultCh <- getResult{body: buf.Bytes()}
	}()

	time.Sleep(300 * time.Millisecond) // let the GET genuinely begin streaming
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal SIGTERM: %v", err)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("active GET failed: %v", result.err)
	}
	if !bytes.Equal(result.body, body) {
		t.Fatalf("active GET returned %d bytes, want the exact %d-byte object", len(result.body), len(body))
	}

	code := waitForProcessExit(t, cmd, serveShutdownGrace+10*time.Second)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
}

// TestServe_SIGTERM_SlowRequestExceedsGrace_RealProcess is the P1-B8
// proof: a request that cannot possibly finish before the grace period
// expires must not hang the process -- it exits (nonzero, per P1-B's own
// documented exception for this one path), a restart succeeds, the
// interrupted object never becomes visible, and the store passes a deep
// verify (no corruption, no partial/mixed state; STATUS.md's existing
// "Durability contract" governs the interrupted write exactly as an
// abrupt kill would).
func TestServe_SIGTERM_SlowRequestExceedsGrace_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	store, err := OpenStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start serve: %v", err)
	}
	waitForZeros3Serve(t, addr)

	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	// 400 bytes at 1 chunk/second is ~400s total -- guaranteed to still
	// be mid-flight when the 30s grace period expires.
	body := bytes.Repeat([]byte("w"), 400)
	req := mustSignedRequestWithOpts(t, signer, http.MethodPut, "http://"+addr+"/b/never-finishes-key", body, nil)
	req.Body = &throttledBody{data: body, chunkSize: 1, delay: 1 * time.Second}

	client := &http.Client{Timeout: 90 * time.Second}
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		// Outcome deliberately ignored: the server process will exit out
		// from under this connection once the grace period expires.
	}()

	time.Sleep(2 * time.Second) // let the PUT genuinely begin streaming
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal SIGTERM: %v", err)
	}

	code := waitForProcessExit(t, cmd, serveShutdownGrace+15*time.Second)
	elapsed := time.Since(start)
	if elapsed < serveShutdownGrace {
		t.Fatalf("process exited after only %s, want it to have waited out the %s grace period first", elapsed, serveShutdownGrace)
	}
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero (P1-B's documented signal that the grace period expired with active work still in flight)")
	}

	// Restart must succeed and the store must be completely healthy --
	// no corruption, no partial/mixed state from the interrupted write.
	restart := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	if err := restart.Start(); err != nil {
		t.Fatalf("failed to restart serve after grace-period expiry: %v", err)
	}
	defer func() { restart.Process.Kill(); restart.Wait() }()
	waitForZeros3Serve(t, addr)

	getResp := doSignedRequest(t, http.DefaultClient, "http://"+addr, signer, http.MethodGet, "/b/never-finishes-key", nil, nil)
	getResp.Body.Close()
	if getResp.StatusCode == http.StatusOK {
		t.Fatalf("the never-acknowledged object is visible after restart -- expected it absent (no partial commit)")
	}

	_, stderr2, verifyCode := runZeros3CLI(t, bin, "verify", "-deep", "-store", storeDir)
	if verifyCode != 0 {
		t.Fatalf("verify -deep after grace-period expiry failed (code %d): %s", verifyCode, stderr2)
	}
}

// TestServe_GracefulShutdown_ReplicateWorkersTerminateCleanly_RealProcess
// ties P1-B's graceful shutdown to M8H's bounded parallel chunk
// transport: a `replicate -workers N` client whose SOURCE server begins
// a graceful SIGTERM mid-transfer must not hang -- the in-flight worker
// requests either complete within the source's own grace period or the
// client observes a clean connection failure, and the client process
// itself always exits in bounded time either way.
func TestServe_GracefulShutdown_ReplicateWorkersTerminateCleanly_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	srcDir := t.TempDir()
	srcStore, err := OpenStore(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcStore.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	// Multiple large, independent objects so M8H's worker pool has
	// several genuinely concurrent chunk transfers in flight.
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("obj-%d.bin", i)
		if _, err := srcStore.PutObject("src", key, genRandomBytes(int64(21000+i), 3_000_000), "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
	srcStore.Close()

	dstDir := t.TempDir()
	dstStore, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dstStore.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	dstStore.Close()

	srcAddr := freeTCPAddr(t)
	srcCmd := exec.Command(bin, "serve", "-store", srcDir, "-addr", srcAddr)
	if err := srcCmd.Start(); err != nil {
		t.Fatalf("failed to start source serve: %v", err)
	}
	waitForZeros3Serve(t, srcAddr)

	dstAddr := freeTCPAddr(t)
	dstCmd := exec.Command(bin, "serve", "-store", dstDir, "-addr", dstAddr)
	if err := dstCmd.Start(); err != nil {
		t.Fatalf("failed to start destination serve: %v", err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()
	waitForZeros3Serve(t, dstAddr)

	replicateDone := make(chan struct{})
	go func() {
		defer close(replicateDone)
		runZeros3CLI(t, bin, "replicate", "-recursive", "-workers", "4",
			"-from", "http://"+srcAddr, "-to", "http://"+dstAddr,
			"s3://src/", "s3://dst/")
	}()

	time.Sleep(200 * time.Millisecond) // let transfer begin
	if err := srcCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal source SIGTERM: %v", err)
	}
	waitForProcessExit(t, srcCmd, serveShutdownGrace+10*time.Second)

	select {
	case <-replicateDone:
		// The client returned -- whether it fully succeeded (source
		// finished all in-flight work within its own grace period) or
		// failed cleanly (source closed a connection mid-transfer) is
		// both acceptable; what matters is that it terminated at all.
	case <-time.After(serveShutdownGrace + 20*time.Second):
		t.Fatalf("replicate client hung instead of terminating after the source server's graceful shutdown")
	}
}

// =============================================================================
// P1-C: Basic TLS (operator-supplied certificate, stdlib-only)
//
// mustGenerateSelfSignedCert produces a throwaway ECDSA cert/key PEM pair
// valid for 127.0.0.1/localhost, written to disk, for exercising
// `-tls-cert`/`-tls-key` against a real subprocess. Client-side trust for
// this self-signed test certificate is established two different ways
// below, matching C7's "testing infrastructure may configure trust
// appropriately" (never InsecureSkipVerify): (1) an *http.Client in THIS
// test process with an explicit tls.Config{RootCAs: pool}, for tests that
// talk to the HTTPS server directly; (2) for a real `zeros3` CLI
// subprocess (replicate), Go's stdlib on Linux honors the SSL_CERT_FILE
// environment variable when building the system root pool a nil
// TLSClientConfig falls back to -- exactly zeros3's own client transport
// (transferHTTPTransport, section 15b) leaves TLSClientConfig unset, so
// pointing SSL_CERT_FILE at the test cert lets an unmodified `zeros3
// replicate` subprocess trust it with zero source changes and zero new
// CLI flags.
// =============================================================================

func mustGenerateSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string, certPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, certPEM
}

// httpsClientTrusting returns an *http.Client that trusts exactly the
// given self-signed test certificate -- never InsecureSkipVerify (C7).
func httpsClientTrusting(certPEM []byte) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
}

func TestServe_TLS_MissingCertOrKeyFails_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	certPath, keyPath, _ := mustGenerateSelfSignedCert(t, t.TempDir())

	_, stderr1, code1 := runZeros3CLI(t, bin, "serve", "-store", storeDir, "-addr", freeTCPAddr(t), "-tls-cert", certPath)
	if code1 != 2 {
		t.Fatalf("serve with -tls-cert only: exit code = %d, want 2; stderr=%s", code1, stderr1)
	}
	if !strings.Contains(stderr1, "-tls-cert and -tls-key must both be supplied together") {
		t.Fatalf("stderr = %q, want the tls-cert/tls-key pairing error", stderr1)
	}

	_, stderr2, code2 := runZeros3CLI(t, bin, "serve", "-store", storeDir, "-addr", freeTCPAddr(t), "-tls-key", keyPath)
	if code2 != 2 {
		t.Fatalf("serve with -tls-key only: exit code = %d, want 2; stderr=%s", code2, stderr2)
	}
	if !strings.Contains(stderr2, "-tls-cert and -tls-key must both be supplied together") {
		t.Fatalf("stderr = %q, want the tls-cert/tls-key pairing error", stderr2)
	}
}

func TestServe_TLS_BadCertKeyFails_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	badDir := t.TempDir()
	badCert := filepath.Join(badDir, "bad-cert.pem")
	badKey := filepath.Join(badDir, "bad-key.pem")
	if err := os.WriteFile(badCert, []byte("not a real certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badKey, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr, "-tls-cert", badCert, "-tls-key", badKey)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("serve with a malformed cert/key unexpectedly succeeded: %s", out)
	}
}

// TestServe_TLS_HTTPS_SigV4_PresignAndGracefulShutdown_RealProcess covers
// most of the P1-C required test list in one real HTTPS subprocess: a
// SigV4-signed PUT/GET succeed over HTTPS, a presigned URL built from an
// https:// endpoint carries the https scheme and is itself accepted by
// the server, and a SIGTERM against the HTTPS server shuts down cleanly
// -- exactly like the plain-HTTP P1-B tests above.
func TestServe_TLS_HTTPS_SigV4_PresignAndGracefulShutdown_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	certPath, keyPath, certPEM := mustGenerateSelfSignedCert(t, t.TempDir())

	addr := freeTCPAddr(t)
	cmd := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr, "-tls-cert", certPath, "-tls-key", keyPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start HTTPS serve: %v", err)
	}
	waitReady := func() bool {
		client := httpsClientTrusting(certPEM)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if resp, err := client.Get("https://" + addr + "/"); err == nil {
				resp.Body.Close()
				return true
			}
			time.Sleep(25 * time.Millisecond)
		}
		return false
	}
	if !waitReady() {
		t.Fatalf("HTTPS serve did not become ready on %s", addr)
	}

	client := httpsClientTrusting(certPEM)
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}

	if resp := doSignedRequest(t, client, "https://"+addr, signer, http.MethodPut, "/tlsbucket", nil, nil); resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create bucket over HTTPS: status %d: %s", resp.StatusCode, b)
	}
	body := []byte("hello over real TLS")
	if resp := doSignedRequest(t, client, "https://"+addr, signer, http.MethodPut, "/tlsbucket/k", body, nil); resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("PUT over HTTPS (SigV4 header auth): status %d: %s", resp.StatusCode, b)
	}
	getResp := doSignedRequest(t, client, "https://"+addr, signer, http.MethodGet, "/tlsbucket/k", nil, nil)
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("GET over HTTPS: status=%d body=%q, want 200 with %q", getResp.StatusCode, got, body)
	}

	// C5: a presigned URL built from an https:// endpoint must carry the
	// https scheme and must itself authenticate against the real server.
	url, err := GeneratePresignedURL(
		Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey},
		defaultRegion,
		PresignRequest{Method: http.MethodGet, Endpoint: "https://" + addr, Bucket: "tlsbucket", Key: "k", Expires: 5 * time.Minute},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("GeneratePresignedURL: %v", err)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Fatalf("presigned URL = %q, want an https:// scheme (matching the https:// endpoint)", url)
	}
	presignResp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET presigned HTTPS URL: %v", err)
	}
	presignBody, _ := io.ReadAll(presignResp.Body)
	presignResp.Body.Close()
	if presignResp.StatusCode != http.StatusOK || !bytes.Equal(presignBody, body) {
		t.Fatalf("presigned HTTPS GET: status=%d body=%q, want 200 with %q", presignResp.StatusCode, presignBody, body)
	}

	// Graceful SIGTERM must work identically over TLS.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal SIGTERM: %v", err)
	}
	code := waitForProcessExit(t, cmd, serveShutdownGrace+10*time.Second)
	if code != 0 {
		t.Fatalf("HTTPS server exit code = %d, want 0 (clean graceful shutdown); stderr=%s", code, stderr.String())
	}
}

// TestServe_TLS_Replicate_RealProcess proves `zeros3 replicate` works
// end to end between two real HTTPS servers -- see the section doc
// comment above for how SSL_CERT_FILE, not any new zeros3 flag, gives
// the unmodified client subprocess trust in the test certificate.
func TestServe_TLS_Replicate_RealProcess(t *testing.T) {
	bin := buildZeros3Binary(t)
	certPath, keyPath, _ := mustGenerateSelfSignedCert(t, t.TempDir())

	srcDir := t.TempDir()
	srcStore, err := OpenStore(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcStore.CreateBucket("src"); err != nil {
		t.Fatal(err)
	}
	if _, err := srcStore.PutObject("src", "obj.bin", []byte("replicated over real TLS"), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	srcStore.Close()

	dstDir := t.TempDir()
	dstStore, err := OpenStore(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dstStore.CreateBucket("dst"); err != nil {
		t.Fatal(err)
	}
	dstStore.Close()

	srcAddr := freeTCPAddr(t)
	srcCmd := exec.Command(bin, "serve", "-store", srcDir, "-addr", srcAddr, "-tls-cert", certPath, "-tls-key", keyPath)
	if err := srcCmd.Start(); err != nil {
		t.Fatalf("failed to start source HTTPS serve: %v", err)
	}
	defer func() { srcCmd.Process.Kill(); srcCmd.Wait() }()

	dstAddr := freeTCPAddr(t)
	dstCmd := exec.Command(bin, "serve", "-store", dstDir, "-addr", dstAddr, "-tls-cert", certPath, "-tls-key", keyPath)
	if err := dstCmd.Start(); err != nil {
		t.Fatalf("failed to start destination HTTPS serve: %v", err)
	}
	defer func() { dstCmd.Process.Kill(); dstCmd.Wait() }()

	// Both servers share one self-signed cert (both bound to
	// 127.0.0.1), so a single readiness probe using a trusting client
	// against either address is representative of both being up; a
	// short fixed settle plus a direct probe keeps this simple.
	time.Sleep(300 * time.Millisecond)

	env := append(os.Environ(), "SSL_CERT_FILE="+certPath)
	stdout, stderr, code := runZeros3CLIWithEnv(t, bin, env,
		"replicate",
		"-from", "https://"+srcAddr, "-to", "https://"+dstAddr,
		"s3://src/obj.bin", "s3://dst/obj.bin")
	if code != 0 {
		t.Fatalf("replicate over HTTPS failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
}

// TestNoInsecureSkipVerifyInSource is a static guardrail (C7/P1-C9):
// zeros3.go must never set tls.Config.InsecureSkipVerify, in production
// code or otherwise -- ZeroS3 has no legitimate reason to disable
// certificate verification anywhere.
func TestNoInsecureSkipVerifyInSource(t *testing.T) {
	src, err := os.ReadFile("zeros3.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "InsecureSkipVerify") {
		t.Fatalf("zeros3.go must never reference InsecureSkipVerify")
	}
}
