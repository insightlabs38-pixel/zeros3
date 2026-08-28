// Command zeros3 is a local, S3-compatible object store built on Go's
// standard library only.
//
// Architecture: incoming object bytes are split by deterministic
// content-defined chunking (CDC), each chunk is stored once in a SHA-256
// content-addressed store (CAS), an immutable JSON manifest lists the
// chunks that make up an object, and an append-only, checksummed
// "visibility journal" is the single source of truth for which
// buckets/objects exist. A GET replays the journal (at open time) to learn
// the namespace, then follows manifest -> chunks to reconstruct bytes.
//
// See STATUS.md for the frozen on-disk format versions and a milestone
// summary.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"
)

// =============================================================================
// 1. Constants, configuration, and protocol types
// =============================================================================

const (
	// storeFormatVersion, cdcFormatVersion, and manifestFormatVersion are
	// the frozen v1 on-disk format numbers. Bumping any of these means a
	// new, explicitly versioned format; existing stores must fail to open
	// cleanly rather than being silently misread.
	storeFormatVersion    = 1
	cdcFormatVersion      = 1
	manifestFormatVersion = 1

	// CDC v1 parameters (frozen). See buildGearTable and findCDCBoundary.
	cdcMinChunkSize    = 16 * 1024
	cdcTargetChunkSize = 64 * 1024
	cdcMaxChunkSize    = 256 * 1024
	// cdcMaskS/cdcMaskL implement FastCDC-style normalized chunking: a
	// stricter (more bits required to be zero) mask is used below the
	// target size to discourage tiny chunks, and a looser mask above the
	// target size to converge toward a cut before the hard max.
	cdcMaskS    = (1 << 16) - 1 // ~1/65536 boundary probability, size < target
	cdcGearSeed = "ZeroS3/CDCv1/GearTable"
	cdcMaskL    = (1 << 15) - 1 // ~1/32768 boundary probability, size >= target

	// Journal v1 frame layout (frozen).
	journalMagic        = "ZSJ1"
	journalFrameVersion = uint16(1)
	journalHeaderSize   = 4 + 2 + 1 + 1 + 8 + 4 // magic+ver+type+flags+seq+len
	maxJournalPayload   = 8 * 1024 * 1024

	// Journal record type numbers (frozen storage format v1). Only
	// create-bucket and put-object-root are implemented in M1.
	recordTypeCreateBucket  = byte(1)
	recordTypePutObjectRoot = byte(2)
	// Reserved for future milestones; not implemented in M1.
	recordTypeDeleteObjectRoot = byte(3) //nolint:unused // reserved format v1 slot
	recordTypeDeleteBucket     = byte(4) //nolint:unused // reserved format v1 slot

	maxRequestBodySize = 256 * 1024 * 1024

	// Default M1 credentials/region. There is no credential-management
	// story in M1; a single static keypair is enough to exercise SigV4.
	defaultAccessKeyID     = "AKIAZEROS3EXAMPLE01"
	defaultSecretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	defaultRegion          = "us-east-1"
	sigv4ServiceName       = "s3"
)

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// =============================================================================
// 2. Errors and small utilities
// =============================================================================

var (
	errNoSuchBucket        = errors.New("no such bucket")
	errNoSuchKey           = errors.New("no such key")
	errManifestUnavailable = errors.New("manifest unavailable or corrupt")
)

// decodeHexSHA256 parses a lowercase-hex SHA-256 digest as stored in
// manifests/journal payloads.
func decodeHexSHA256(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("invalid sha256 hex %q: %w", s, err)
	}
	if len(b) != 32 {
		return out, fmt.Errorf("invalid sha256 hex length %q", s)
	}
	copy(out[:], b)
	return out, nil
}

// syncDir fsyncs a directory so that entries created/renamed within it
// (chunk files, manifest files, FORMAT.json) are durable even if the
// process crashes immediately afterward. Required on Linux; directory
// fsync is the only portable way to persist a rename/create in the
// directory's metadata.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeFileDurable writes data to a new immutable file at finalPath by
// staging it in tmpDir, fsyncing the staged file, then renaming it into
// place. The rename is used only as a publication mechanism for
// content-addressed/immutable files (chunks, manifests, FORMAT.json) --
// never as the authoritative visibility mechanism for the mutable
// bucket/key namespace, which is owned exclusively by the journal.
func writeFileDurable(tmpDir, finalPath string, data []byte) error {
	tmp, err := os.CreateTemp(tmpDir, "zs3-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// =============================================================================
// Test-only failure injection seam.
//
// testHook is invoked at named points inside the durability-critical PUT
// pipeline. It is nil (a no-op) in every real code path; only
// zeros3_test.go ever assigns it, to deterministically simulate a process
// crash at each commit boundary without any timing-dependent kill(1)
// tricks. Tests must restore it to nil when finished.
// =============================================================================

var testHook func(point string)

func fireTestHook(point string) {
	if testHook != nil {
		testHook(point)
	}
}

const (
	hookBeforeChunkWrite            = "before-chunk-write"
	hookAfterChunksPublished        = "after-chunks-published"
	hookAfterManifestPublished      = "after-manifest-published"
	hookAfterJournalWriteBeforeSync = "after-journal-write-before-sync"
	hookAfterJournalSync            = "after-journal-sync"
	hookAfterApplyBeforeResponse    = "after-apply-before-response"
	hookAfterAck                    = "after-ack"
)

// simulatedCrash is panicked by test hooks to unwind out of the commit
// pipeline mid-flight, standing in for an abrupt process death.
type simulatedCrash struct{ point string }

func (s simulatedCrash) String() string { return "simulated crash at " + s.point }

// =============================================================================
// 3. Content-defined chunking (CDC v1)
//
// A Gear-hash rolling checksum decides chunk boundaries so that inserting
// or removing bytes anywhere in an object only perturbs chunk boundaries
// locally, instead of reshuffling every following chunk the way fixed-size
// slicing would. This is what lets identical regions of two similar
// objects dedupe against each other in the CAS.
// =============================================================================

// gearTable holds 256 deterministic 64-bit "gear" values, one per input
// byte value, derived from SHA-256 of a fixed, version-tagged seed. This
// table is part of the frozen CDC v1 format: it must never be randomized
// or regenerated at runtime/build, or previously written chunk boundaries
// (and therefore CAS dedup behavior) would silently change.
var gearTable = buildGearTable()

func buildGearTable() [256]uint64 {
	var t [256]uint64
	for i := 0; i < 256; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", cdcGearSeed, i)))
		t[i] = binary.BigEndian.Uint64(h[:8])
	}
	return t
}

// findCDCBoundary scans data (which begins at a chunk start) for the next
// content-defined cut point and returns its length. eof indicates no more
// bytes are available from the source, so a short final chunk (even
// shorter than cdcMinChunkSize) is acceptable at the very end of an
// object.
func findCDCBoundary(data []byte, eof bool) int {
	n := len(data)
	limit := n
	if limit > cdcMaxChunkSize {
		limit = cdcMaxChunkSize
	}
	var hash uint64
	for i := 0; i < limit; i++ {
		hash = (hash << 1) + gearTable[data[i]]
		size := i + 1
		if size >= cdcMaxChunkSize {
			return size // forced boundary: never emit a chunk above the max.
		}
		if size < cdcMinChunkSize {
			continue // never cut below the min, except for a final short tail.
		}
		mask := uint64(cdcMaskL)
		if size < cdcTargetChunkSize {
			mask = cdcMaskS
		}
		if hash&mask == 0 {
			return size
		}
	}
	if eof {
		return n // final chunk: whatever bytes remain, however few.
	}
	return n // defensive fallback; fill() guarantees n==cdcMaxChunkSize here otherwise.
}

// cdcChunker turns a byte stream into a sequence of content-defined
// chunks. It buffers at most cdcMaxChunkSize bytes at a time, so memory
// use is bounded regardless of object size.
type cdcChunker struct {
	r   io.Reader
	buf []byte
	eof bool
}

func newCDCChunker(r io.Reader) *cdcChunker {
	return &cdcChunker{r: r}
}

// fill tops up c.buf to cdcMaxChunkSize bytes, or until the source is
// exhausted.
func (c *cdcChunker) fill() error {
	for !c.eof && len(c.buf) < cdcMaxChunkSize {
		need := cdcMaxChunkSize - len(c.buf)
		tmp := make([]byte, need)
		n, err := c.r.Read(tmp)
		if n > 0 {
			c.buf = append(c.buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				c.eof = true
				break
			}
			return err
		}
	}
	return nil
}

// next returns the next content-defined chunk, or io.EOF once the source
// is fully consumed and no chunk remains (this is the normal, non-error
// way a chunk stream ends; an empty object yields zero chunks and an
// immediate io.EOF).
func (c *cdcChunker) next() ([]byte, error) {
	if err := c.fill(); err != nil {
		return nil, err
	}
	if len(c.buf) == 0 {
		return nil, io.EOF
	}
	n := findCDCBoundary(c.buf, c.eof)
	chunk := make([]byte, n)
	copy(chunk, c.buf[:n])
	remaining := copy(c.buf, c.buf[n:])
	c.buf = c.buf[:remaining]
	return chunk, nil
}

// chunkPiece is one content-defined chunk plus its CAS identity.
type chunkPiece struct {
	data []byte
	sha  [32]byte
}

// chunkData splits r into content-defined chunks and computes each
// chunk's SHA-256 identity.
func chunkData(r io.Reader) ([]chunkPiece, error) {
	c := newCDCChunker(r)
	var pieces []chunkPiece
	for {
		chunk, err := c.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		pieces = append(pieces, chunkPiece{data: chunk, sha: sha256.Sum256(chunk)})
	}
	return pieces, nil
}

// =============================================================================
// 4. Content-addressed chunk storage (CAS)
//
// Chunk identity is SHA-256 of the exact chunk bytes; the digest also
// determines the chunk's storage path (chunks/aa/bb/<64-hex>), so a chunk
// can never be silently duplicated with different content, and identical
// content published from two different objects is stored only once.
// =============================================================================

// chunkPath returns the two-level sharded path for a chunk's digest. The
// digest -- not any caller-supplied name -- is the only thing that
// determines this path.
func (s *Store) chunkPath(sum [32]byte) string {
	h := hex.EncodeToString(sum[:])
	return filepath.Join(s.root, "chunks", h[0:2], h[2:4], h)
}

// casWrite durably publishes data under its own content hash. If a chunk
// with this hash already exists, publication is a no-op (immutable
// content-addressed chunks are safe to dedup this way); its content is
// re-verified on read instead of on every dedup'd write.
func (s *Store) casWrite(data []byte) ([32]byte, error) {
	sum := sha256.Sum256(data)
	path := s.chunkPath(sum)
	if _, err := os.Stat(path); err == nil {
		return sum, nil
	} else if !os.IsNotExist(err) {
		return sum, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sum, err
	}
	if err := writeFileDurable(filepath.Join(s.root, "tmp"), path, data); err != nil {
		return sum, err
	}
	if err := syncDir(dir); err != nil {
		return sum, err
	}
	return sum, nil
}

// casRead reads back a chunk and re-verifies its content against the
// digest that names it, so that on-disk corruption (bit rot, a truncated
// write that somehow left a full-length file, manual tampering) is
// reported as an error rather than trusted blindly.
func (s *Store) casRead(sum [32]byte) ([]byte, error) {
	data, err := os.ReadFile(s.chunkPath(sum))
	if err != nil {
		return nil, err
	}
	if got := sha256.Sum256(data); got != sum {
		return nil, fmt.Errorf("cas: chunk %x is corrupt (content hash mismatch)", sum)
	}
	return data, nil
}

// =============================================================================
// 5. Manifests (v1, immutable JSON)
//
// A manifest is the complete, immutable description of one object
// version: its ordered chunk list, total length, ETag, content type, and
// metadata. Manifests are named by a UUID, not by bucket/key, and are
// referenced from the journal by both that UUID and the SHA-256 of the
// manifest file's exact bytes -- so a corrupted or substituted manifest
// file is detectable independently of its filename.
// =============================================================================

type chunkRef struct {
	SHA256 string `json:"sha256"`
	Length int64  `json:"length"`
}

type metadataKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type manifestV1 struct {
	ManifestFormatVersion int          `json:"manifest_format_version"`
	CDCFormatVersion      int          `json:"cdc_format_version"`
	HashAlgorithm         string       `json:"hash_algorithm"`
	ManifestUUID          string       `json:"manifest_uuid"`
	TotalLength           int64        `json:"total_length"`
	Chunks                []chunkRef   `json:"chunks"`
	ObjectSHA256          string       `json:"object_sha256"`
	ETag                  string       `json:"etag"`
	ContentType           string       `json:"content_type"`
	Metadata              []metadataKV `json:"metadata"`
	CreatedAt             time.Time    `json:"created_at"`
	VersionID             string       `json:"version_id"`
}

// buildManifestV1 assembles an immutable manifest for one object version.
// Metadata is sorted by key so that two builds of the same logical
// metadata always serialize identically.
func buildManifestV1(pieces []chunkPiece, fullBody []byte, contentType string, metadata map[string]string) (manifestV1, error) {
	id := newUUIDv7()
	chunks := make([]chunkRef, len(pieces))
	for i, p := range pieces {
		chunks[i] = chunkRef{SHA256: hex.EncodeToString(p.sha[:]), Length: int64(len(p.data))}
	}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	md := make([]metadataKV, 0, len(keys))
	for _, k := range keys {
		md = append(md, metadataKV{Key: k, Value: metadata[k]})
	}
	objSum := sha256.Sum256(fullBody)
	etagSum := md5.Sum(fullBody) //nolint:gosec // S3-compatible single-part ETag, not a security use of MD5.
	return manifestV1{
		ManifestFormatVersion: manifestFormatVersion,
		CDCFormatVersion:      cdcFormatVersion,
		HashAlgorithm:         "sha256",
		ManifestUUID:          id,
		TotalLength:           int64(len(fullBody)),
		Chunks:                chunks,
		ObjectSHA256:          hex.EncodeToString(objSum[:]),
		ETag:                  hex.EncodeToString(etagSum[:]),
		ContentType:           contentType,
		Metadata:              md,
		CreatedAt:             time.Now().UTC(),
		VersionID:             id,
	}, nil
}

// publishManifest durably writes a manifest's canonical JSON encoding and
// returns its UUID and the SHA-256 of the exact bytes written, which the
// caller must record in the journal so that manifest corruption can be
// detected on replay/read independent of the UUID filename.
func (s *Store) publishManifest(m manifestV1) (id string, sum [32]byte, err error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", sum, err
	}
	path := filepath.Join(s.root, "manifests", m.ManifestUUID+".json")
	if err := writeFileDurable(filepath.Join(s.root, "tmp"), path, data); err != nil {
		return "", sum, err
	}
	if err := syncDir(filepath.Join(s.root, "manifests")); err != nil {
		return "", sum, err
	}
	return m.ManifestUUID, sha256.Sum256(data), nil
}

// readManifest reads and parses a manifest by UUID. Callers that need
// corruption detection against a journal-recorded hash should hash the
// returned raw bytes themselves.
func (s *Store) readManifest(id string) (manifestV1, []byte, error) {
	path := filepath.Join(s.root, "manifests", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return manifestV1{}, nil, err
	}
	var m manifestV1
	if err := json.Unmarshal(data, &m); err != nil {
		return manifestV1{}, nil, fmt.Errorf("manifest %s is corrupt: %w", id, err)
	}
	return m, data, nil
}

// newUUIDv7 returns a fresh, time-ordered UUID version 7 identifier in its
// canonical 8-4-4-4-12 lowercase hex string form (e.g.
// "018f4d2e-6b1a-7c3d-9e2f-1a2b3c4d5e6f"), used as both manifest UUIDs and
// the store identifier. This is produced by the Go standard library's
// "uuid" package (added in Go 1.27); it is exactly the string format a
// hand-rolled generator would also need to produce, so this swap does not
// change the on-disk manifest/FORMAT.json representation at all.
func newUUIDv7() string {
	return uuid.NewV7().String()
}

// =============================================================================
// 6. Visibility journal (v1, append-only binary log)
//
// The journal is the ONLY authoritative record of which buckets and
// objects exist. Every frame is:
//
//	4 bytes  magic "ZSJ1"
//	2 bytes  frame version (big-endian)
//	1 byte   record type
//	1 byte   flags/reserved (0)
//	8 bytes  monotonically increasing sequence number (big-endian, starts at 1)
//	4 bytes  payload length N (big-endian)
//	N bytes  UTF-8 JSON payload
//	4 bytes  CRC32C (Castagnoli) over every preceding byte of the frame
//
// A frame is durable only once its bytes are written AND fsynced; the
// in-memory namespace is updated only after that sync succeeds, and only
// then does a PUT/CreateBucket get acknowledged. A crash before a frame's
// sync completes must never make its object visible after restart.
// =============================================================================

type journalRecord struct {
	seq     uint64
	recType byte
	payload []byte
}

type journalCreateBucketPayload struct {
	Bucket    string    `json:"bucket"`
	CreatedAt time.Time `json:"created_at"`
}

type journalPutPayload struct {
	Bucket         string `json:"bucket"`
	Key            string `json:"key"`
	ManifestUUID   string `json:"manifest_uuid"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Size           int64  `json:"size"`
	ETag           string `json:"etag"`
	ContentType    string `json:"content_type"`
	VersionID      string `json:"version_id"`
}

// Journal owns the on-disk visibility.log file and the strictly
// sequential append cursor into it.
type Journal struct {
	f           *os.File
	mu          sync.Mutex
	writeOffset int64
	nextSeq     uint64
}

// appendFrame durably appends one journal frame and returns its sequence
// number. It writes the frame, fires the write-before-sync test hook,
// fsyncs, fires the after-sync test hook, and only then advances the
// append cursor -- so a failure or simulated crash between write and sync
// leaves the journal's logical length unchanged from the reader's
// perspective (replay will see it as a torn tail and discard it).
func (j *Journal) appendFrame(recType byte, payload []byte) (uint64, error) {
	if len(payload) > maxJournalPayload {
		return 0, fmt.Errorf("journal: payload of %d bytes exceeds max %d", len(payload), maxJournalPayload)
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	seq := j.nextSeq
	header := make([]byte, journalHeaderSize)
	copy(header[0:4], journalMagic)
	binary.BigEndian.PutUint16(header[4:6], journalFrameVersion)
	header[6] = recType
	header[7] = 0
	binary.BigEndian.PutUint64(header[8:16], seq)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(payload)))

	frame := make([]byte, 0, journalHeaderSize+len(payload)+4)
	frame = append(frame, header...)
	frame = append(frame, payload...)
	crc := crc32.Checksum(frame, castagnoliTable)
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	frame = append(frame, crcBytes...)

	if _, err := j.f.WriteAt(frame, j.writeOffset); err != nil {
		return 0, fmt.Errorf("journal: write failed: %w", err)
	}
	fireTestHook(hookAfterJournalWriteBeforeSync)
	if err := j.f.Sync(); err != nil {
		return 0, fmt.Errorf("journal: sync failed: %w", err)
	}
	fireTestHook(hookAfterJournalSync)

	j.writeOffset += int64(len(frame))
	j.nextSeq = seq + 1
	return seq, nil
}

// replayJournal reads every complete, valid frame from f starting at
// offset 0. It returns the byte offset just past the last valid frame
// (validEnd), the last sequence number seen, and the decoded records in
// order.
//
// Only a frame that is demonstrably incomplete by byte count -- the file
// ends partway through its header, payload, or CRC -- is treated as a
// torn tail and silently discarded; this can only happen to the last
// frame in the file, since a complete frame's declared length lets replay
// skip straight past it. Any frame with a full byte count present but
// bad magic, an unknown version/type, a sequence gap/duplicate, or a CRC
// mismatch is store corruption and replay fails loudly.
func replayJournal(f *os.File) (validEnd int64, lastSeq uint64, records []journalRecord, err error) {
	var offset int64
	for {
		header := make([]byte, journalHeaderSize)
		n, rerr := f.ReadAt(header, offset)
		if rerr == io.EOF {
			if n == 0 {
				return offset, lastSeq, records, nil // clean end, right at a frame boundary
			}
			return offset, lastSeq, records, nil // torn tail: incomplete header
		}
		if rerr != nil {
			return 0, 0, nil, fmt.Errorf("journal: read failed at offset %d: %w", offset, rerr)
		}
		if string(header[0:4]) != journalMagic {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: bad magic", offset)
		}
		ver := binary.BigEndian.Uint16(header[4:6])
		if ver != journalFrameVersion {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: unsupported frame version %d", offset, ver)
		}
		recType := header[6]
		if recType != recordTypeCreateBucket && recType != recordTypePutObjectRoot {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: unknown record type %d", offset, recType)
		}
		seq := binary.BigEndian.Uint64(header[8:16])
		plen := binary.BigEndian.Uint32(header[16:20])
		if plen > maxJournalPayload {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: payload length %d exceeds max", offset, plen)
		}

		rest := make([]byte, int(plen)+4)
		_, rerr2 := f.ReadAt(rest, offset+journalHeaderSize)
		if rerr2 == io.EOF {
			return offset, lastSeq, records, nil // torn tail: header present, payload/crc incomplete
		}
		if rerr2 != nil {
			return 0, 0, nil, fmt.Errorf("journal: read failed at offset %d: %w", offset+journalHeaderSize, rerr2)
		}
		payload := rest[:plen]
		storedCRC := binary.BigEndian.Uint32(rest[plen:])

		full := make([]byte, 0, journalHeaderSize+len(payload))
		full = append(full, header...)
		full = append(full, payload...)
		if gotCRC := crc32.Checksum(full, castagnoliTable); gotCRC != storedCRC {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: crc mismatch (seq=%d)", offset, seq)
		}
		if seq != lastSeq+1 {
			return 0, 0, nil, fmt.Errorf("journal: corrupt: expected sequence %d, got %d", lastSeq+1, seq)
		}

		records = append(records, journalRecord{seq: seq, recType: recType, payload: payload})
		lastSeq = seq
		offset += int64(journalHeaderSize) + int64(plen) + 4
	}
}

// openJournal opens (creating if absent) the journal file at path,
// replays it to recover valid records, and truncates away any torn tail
// so future appends start from a clean offset.
func openJournal(path string) (*Journal, []journalRecord, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, nil, err
	}
	validEnd, lastSeq, records, err := replayJournal(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if err := f.Truncate(validEnd); err != nil {
		f.Close()
		return nil, nil, err
	}
	return &Journal{f: f, writeOffset: validEnd, nextSeq: lastSeq + 1}, records, nil
}

// =============================================================================
// 7. Store: format metadata, namespace, and bucket/object operations
//
// The in-memory bucket/object maps are a reconstructible index, rebuilt
// by replaying the journal at open time -- they are never themselves
// authoritative. Bucket/object names are used only as Go map keys; they
// are never turned into filesystem paths (chunks and manifests are named
// solely by content hash / UUID).
// =============================================================================

type storeFormat struct {
	StoreFormatVersion int    `json:"store_format_version"`
	CDCFormatVersion   int    `json:"cdc_format_version"`
	HashAlgorithm      string `json:"hash_algorithm"`
	StoreID            string `json:"store_id"`
}

type objectEntry struct {
	manifestUUID   string
	manifestSHA256 [32]byte
	size           int64
	etag           string
	contentType    string
	seq            uint64
}

type bucketEntry struct {
	name    string
	objects map[string]*objectEntry
}

type Store struct {
	root    string
	format  storeFormat
	journal *Journal

	mu      sync.Mutex
	buckets map[string]*bucketEntry
}

// OpenStore opens the store rooted at root, initializing it (writing
// FORMAT.json and creating the directory layout) if it does not already
// exist, then replays the journal to rebuild the in-memory namespace.
// Opening a store with an unsupported format version fails clearly rather
// than attempting to read it.
func OpenStore(root string) (*Store, error) {
	for _, sub := range []string{"", "journal", "chunks", "manifests", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, fmt.Errorf("store: failed to create %s: %w", sub, err)
		}
	}

	formatPath := filepath.Join(root, "FORMAT.json")
	format, err := loadOrInitFormat(root, formatPath)
	if err != nil {
		return nil, err
	}

	journalPath := filepath.Join(root, "journal", "visibility.log")
	j, records, err := openJournal(journalPath)
	if err != nil {
		return nil, fmt.Errorf("store: failed to open journal: %w", err)
	}

	s := &Store{
		root:    root,
		format:  format,
		journal: j,
		buckets: map[string]*bucketEntry{},
	}
	for _, rec := range records {
		if err := s.applyRecord(rec); err != nil {
			j.f.Close()
			return nil, fmt.Errorf("store: journal replay failed: %w", err)
		}
	}
	return s, nil
}

func loadOrInitFormat(root, formatPath string) (storeFormat, error) {
	data, err := os.ReadFile(formatPath)
	if err == nil {
		var format storeFormat
		if err := json.Unmarshal(data, &format); err != nil {
			return storeFormat{}, fmt.Errorf("store: FORMAT.json is corrupt: %w", err)
		}
		if format.StoreFormatVersion != storeFormatVersion {
			return storeFormat{}, fmt.Errorf("store: unsupported store format version %d (this build supports version %d)", format.StoreFormatVersion, storeFormatVersion)
		}
		if format.CDCFormatVersion != cdcFormatVersion {
			return storeFormat{}, fmt.Errorf("store: unsupported CDC format version %d (this build supports version %d)", format.CDCFormatVersion, cdcFormatVersion)
		}
		if format.HashAlgorithm != "sha256" {
			return storeFormat{}, fmt.Errorf("store: unsupported hash algorithm %q", format.HashAlgorithm)
		}
		return format, nil
	}
	if !os.IsNotExist(err) {
		return storeFormat{}, err
	}

	format := storeFormat{
		StoreFormatVersion: storeFormatVersion,
		CDCFormatVersion:   cdcFormatVersion,
		HashAlgorithm:      "sha256",
		StoreID:            newUUIDv7(),
	}
	data, err = json.MarshalIndent(format, "", "  ")
	if err != nil {
		return storeFormat{}, err
	}
	if err := writeFileDurable(filepath.Join(root, "tmp"), formatPath, data); err != nil {
		return storeFormat{}, err
	}
	if err := syncDir(root); err != nil {
		return storeFormat{}, err
	}
	return format, nil
}

// applyRecord folds one already-validated journal record into the
// in-memory namespace during replay.
func (s *Store) applyRecord(rec journalRecord) error {
	switch rec.recType {
	case recordTypeCreateBucket:
		var p journalCreateBucketPayload
		if err := json.Unmarshal(rec.payload, &p); err != nil {
			return fmt.Errorf("seq %d: %w", rec.seq, err)
		}
		if _, exists := s.buckets[p.Bucket]; !exists {
			s.buckets[p.Bucket] = &bucketEntry{name: p.Bucket, objects: map[string]*objectEntry{}}
		}
	case recordTypePutObjectRoot:
		var p journalPutPayload
		if err := json.Unmarshal(rec.payload, &p); err != nil {
			return fmt.Errorf("seq %d: %w", rec.seq, err)
		}
		b, ok := s.buckets[p.Bucket]
		if !ok {
			return fmt.Errorf("seq %d: put-object-root for unknown bucket %q", rec.seq, p.Bucket)
		}
		sum, err := decodeHexSHA256(p.ManifestSHA256)
		if err != nil {
			return fmt.Errorf("seq %d: %w", rec.seq, err)
		}
		b.objects[p.Key] = &objectEntry{
			manifestUUID:   p.ManifestUUID,
			manifestSHA256: sum,
			size:           p.Size,
			etag:           p.ETag,
			contentType:    p.ContentType,
			seq:            rec.seq,
		}
	default:
		return fmt.Errorf("seq %d: unknown record type %d", rec.seq, rec.recType)
	}
	return nil
}

// Close releases the store's open file handles.
func (s *Store) Close() error {
	return s.journal.f.Close()
}

// CreateBucket makes a bucket durably visible. It is idempotent: creating
// an already-existing bucket succeeds without appending a duplicate
// journal record.
func (s *Store) CreateBucket(name string) error {
	if name == "" {
		return fmt.Errorf("invalid bucket name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.buckets[name]; exists {
		return nil
	}
	payload, err := json.Marshal(journalCreateBucketPayload{Bucket: name, CreatedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	if _, err := s.journal.appendFrame(recordTypeCreateBucket, payload); err != nil {
		return err
	}
	s.buckets[name] = &bucketEntry{name: name, objects: map[string]*objectEntry{}}
	return nil
}

// PutObject runs the full commit pipeline for one object version: CDC
// chunking, durable CAS publication of every chunk, durable manifest
// publication, and finally an append+sync of the visibility journal. Only
// after the journal sync succeeds is the in-memory namespace updated;
// only after that does this function return, so a caller can safely
// acknowledge success the moment it returns.
func (s *Store) PutObject(bucket, key string, body []byte, contentType string, metadata map[string]string) (*objectEntry, error) {
	s.mu.Lock()
	_, ok := s.buckets[bucket]
	s.mu.Unlock()
	if !ok {
		return nil, errNoSuchBucket
	}

	pieces, err := chunkData(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chunking failed: %w", err)
	}
	for _, p := range pieces {
		fireTestHook(hookBeforeChunkWrite)
		if _, err := s.casWrite(p.data); err != nil {
			return nil, fmt.Errorf("cas write failed: %w", err)
		}
	}
	fireTestHook(hookAfterChunksPublished)

	man, err := buildManifestV1(pieces, body, contentType, metadata)
	if err != nil {
		return nil, fmt.Errorf("manifest build failed: %w", err)
	}
	manUUID, manSHA, err := s.publishManifest(man)
	if err != nil {
		return nil, fmt.Errorf("manifest publish failed: %w", err)
	}
	fireTestHook(hookAfterManifestPublished)

	payload, err := json.Marshal(journalPutPayload{
		Bucket:         bucket,
		Key:            key,
		ManifestUUID:   manUUID,
		ManifestSHA256: hex.EncodeToString(manSHA[:]),
		Size:           man.TotalLength,
		ETag:           man.ETag,
		ContentType:    man.ContentType,
		VersionID:      man.VersionID,
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	seq, err := s.journal.appendFrame(recordTypePutObjectRoot, payload)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("journal append failed: %w", err)
	}
	entry := &objectEntry{
		manifestUUID:   manUUID,
		manifestSHA256: manSHA,
		size:           man.TotalLength,
		etag:           man.ETag,
		contentType:    man.ContentType,
		seq:            seq,
	}
	s.buckets[bucket].objects[key] = entry
	s.mu.Unlock()
	fireTestHook(hookAfterApplyBeforeResponse)

	return entry, nil
}

// GetObject looks up the current visible version of bucket/key (from the
// journal-derived namespace), then reconstructs its exact bytes by
// reading the manifest and following its chunk list through the CAS.
// Every layer re-verifies content against its own hash, so a corrupted
// manifest or chunk is reported as an error rather than served silently.
func (s *Store) GetObject(bucket, key string) (*objectEntry, []byte, error) {
	s.mu.Lock()
	b, ok := s.buckets[bucket]
	if !ok {
		s.mu.Unlock()
		return nil, nil, errNoSuchBucket
	}
	obj, ok := b.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, nil, errNoSuchKey
	}

	man, manBytes, err := s.readManifest(obj.manifestUUID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errManifestUnavailable, err)
	}
	if gotSum := sha256.Sum256(manBytes); gotSum != obj.manifestSHA256 {
		return nil, nil, fmt.Errorf("%w: manifest %s hash mismatch", errManifestUnavailable, obj.manifestUUID)
	}

	buf := make([]byte, 0, man.TotalLength)
	for _, c := range man.Chunks {
		sum, err := decodeHexSHA256(c.SHA256)
		if err != nil {
			return nil, nil, err
		}
		data, err := s.casRead(sum)
		if err != nil {
			return nil, nil, fmt.Errorf("chunk read failed: %w", err)
		}
		if int64(len(data)) != c.Length {
			return nil, nil, fmt.Errorf("chunk %s: length mismatch", c.SHA256)
		}
		buf = append(buf, data...)
	}
	if int64(len(buf)) != man.TotalLength {
		return nil, nil, fmt.Errorf("reconstructed object length mismatch for %s/%s", bucket, key)
	}
	return obj, buf, nil
}

// =============================================================================
// 8. AWS SigV4 (Authorization header only)
//
// The canonical request is built from the ORIGINAL request-target bytes
// (request.RequestURI, split ourselves into raw path and raw query)
// rather than from Go's parsed/decoded r.URL, specifically so that S3
// path-normalization traps -- repeated slashes, "%2F" standing for a
// literal slash inside a key, "+" vs "%20" for space, trailing slashes --
// are preserved exactly as the client sent them and exactly as S3 itself
// signs them. Presigned URLs and aws-chunked/trailer payloads are out of
// scope for M1.
// =============================================================================

type authError struct {
	code string
	msg  string
}

func (e *authError) Error() string { return e.msg }

func isUnreservedByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
		b == '-' || b == '_' || b == '.' || b == '~'
}

// sigv4EncodeBytes uppercase-percent-encodes every byte that is not in
// SigV4's unreserved set. It never special-cases '/' -- callers that want
// '/' preserved as a path separator must not pass it through this
// function, which is exactly why canonical-URI construction operates on
// already-'/'-split segments rather than the whole path.
func sigv4EncodeBytes(raw []byte) string {
	var sb strings.Builder
	for _, b := range raw {
		if isUnreservedByte(b) {
			sb.WriteByte(b)
		} else {
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

// percentDecodeToBytes decodes %XX triplets to raw bytes and passes every
// other byte through unchanged (in particular, '+' is never treated as
// space -- that is a query-string-only, application/x-www-form-urlencoded
// convention that does not apply to SigV4 canonicalization).
func percentDecodeToBytes(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) {
				return nil, fmt.Errorf("invalid percent-encoding in %q", s)
			}
			hi, lo := hexVal(s[i+1]), hexVal(s[i+2])
			if hi < 0 || lo < 0 {
				return nil, fmt.Errorf("invalid percent-encoding in %q", s)
			}
			out = append(out, byte(hi<<4|lo))
			i += 2
		} else {
			out = append(out, s[i])
		}
	}
	return out, nil
}

// sigv4CanonicalURI rebuilds the canonical URI from a raw, unnormalized
// request path. Each '/'-delimited segment (including empty segments from
// repeated slashes, and the empty segments at a leading/trailing slash)
// is percent-decoded and then strictly re-encoded on its own, so a
// literal '/' that arrives already-decoded stays a separator, while an
// encoded slash ("%2F") sitting inside one segment's content survives as
// "%2F" -- decoded to a raw '/' byte and then re-escaped, because encoding
// never special-cases '/'. Path segments are never resolved ("." / "..")
// or collapsed ("//" stays "//").
func sigv4CanonicalURI(rawPath string) (string, error) {
	if rawPath == "" {
		rawPath = "/"
	}
	segs := strings.Split(rawPath, "/")
	for i, seg := range segs {
		decoded, err := percentDecodeToBytes(seg)
		if err != nil {
			return "", err
		}
		segs[i] = sigv4EncodeBytes(decoded)
	}
	return strings.Join(segs, "/"), nil
}

type queryPair struct{ k, v string }

// sigv4CanonicalQuery rebuilds the canonical query string from the raw
// query (the substring of RequestURI after '?'): each "k=v" (or bare "k")
// pair is decoded and re-encoded independently, empty values are kept
// (rendered as "k="), repeated names are preserved as separate entries,
// and pairs are sorted by encoded name then encoded value.
func sigv4CanonicalQuery(rawQuery string) (string, error) {
	if rawQuery == "" {
		return "", nil
	}
	parts := strings.Split(rawQuery, "&")
	pairs := make([]queryPair, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		rawK, rawV := p, ""
		if idx := strings.IndexByte(p, '='); idx >= 0 {
			rawK, rawV = p[:idx], p[idx+1:]
		}
		dk, err := percentDecodeToBytes(rawK)
		if err != nil {
			return "", err
		}
		dv, err := percentDecodeToBytes(rawV)
		if err != nil {
			return "", err
		}
		pairs = append(pairs, queryPair{k: sigv4EncodeBytes(dk), v: sigv4EncodeBytes(dv)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&"), nil
}

type sigv4Auth struct {
	accessKeyID   string
	date          string
	region        string
	service       string
	signedHeaders []string
	signature     string
}

// parseAuthorizationHeader parses the "AWS4-HMAC-SHA256
// Credential=.../SignedHeaders=.../Signature=..." header form. Presigned
// (query-string) SigV4 is intentionally not supported.
func parseAuthorizationHeader(h string) (*sigv4Auth, error) {
	const prefix = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(h, prefix) {
		return nil, fmt.Errorf("unsupported authorization scheme")
	}
	var credential, signedHeaders, signature string
	for _, p := range strings.Split(strings.TrimPrefix(h, prefix), ",") {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "Credential="):
			credential = strings.TrimPrefix(p, "Credential=")
		case strings.HasPrefix(p, "SignedHeaders="):
			signedHeaders = strings.TrimPrefix(p, "SignedHeaders=")
		case strings.HasPrefix(p, "Signature="):
			signature = strings.TrimPrefix(p, "Signature=")
		}
	}
	if credential == "" || signedHeaders == "" || signature == "" {
		return nil, fmt.Errorf("malformed authorization header")
	}
	cp := strings.Split(credential, "/")
	if len(cp) != 5 || cp[4] != "aws4_request" {
		return nil, fmt.Errorf("malformed credential scope %q", credential)
	}
	return &sigv4Auth{
		accessKeyID:   cp[0],
		date:          cp[1],
		region:        cp[2],
		service:       cp[3],
		signedHeaders: strings.Split(signedHeaders, ";"),
		signature:     signature,
	}, nil
}

func collapseWhitespace(s string) string {
	var sb strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				sb.WriteByte(' ')
			}
			prevSpace = true
		} else {
			sb.WriteRune(r)
			prevSpace = false
		}
	}
	return sb.String()
}

// sigv4CanonicalHeaders builds the "name:value\n" block for exactly the
// signed headers, sorted by lowercase name. "host" is special-cased
// because net/http moves it out of r.Header into r.Host.
func sigv4CanonicalHeaders(r *http.Request, signed []string) (string, error) {
	names := append([]string{}, signed...)
	sort.Strings(names)
	var sb strings.Builder
	for _, name := range names {
		var value string
		if strings.EqualFold(name, "host") {
			value = r.Host
		} else {
			vals := r.Header.Values(http.CanonicalHeaderKey(name))
			if len(vals) == 0 {
				return "", fmt.Errorf("missing signed header %q", name)
			}
			trimmed := make([]string, len(vals))
			for i, v := range vals {
				trimmed[i] = collapseWhitespace(strings.TrimSpace(v))
			}
			value = strings.Join(trimmed, ",")
		}
		sb.WriteString(strings.ToLower(name))
		sb.WriteByte(':')
		sb.WriteString(value)
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func sigv4SignedHeadersList(signed []string) string {
	names := make([]string, len(signed))
	for i, n := range signed {
		names[i] = strings.ToLower(n)
	}
	sort.Strings(names)
	return strings.Join(names, ";")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sigv4SigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

// authenticate validates a request's Authorization header against
// srv.creds/srv.region, reconstructing the canonical request from the
// original raw path/query rather than r.URL. On success it also confirms
// the signed X-Amz-Content-Sha256 value matches the actual body bytes
// received, catching tampering that changes the body but replays an
// old, still-signed content-hash header.
func (srv *Server) authenticate(r *http.Request, rawPath, rawQuery string, body []byte) error {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return &authError{code: "AccessDenied", msg: "missing Authorization header"}
	}
	auth, err := parseAuthorizationHeader(authHeader)
	if err != nil {
		return &authError{code: "AuthorizationHeaderMalformed", msg: err.Error()}
	}
	if auth.accessKeyID != srv.creds.AccessKeyID {
		return &authError{code: "InvalidAccessKeyId", msg: "unknown access key"}
	}
	if auth.region != srv.region {
		return &authError{code: "AuthorizationHeaderMalformed", msg: "region mismatch"}
	}
	if auth.service != sigv4ServiceName {
		return &authError{code: "AuthorizationHeaderMalformed", msg: "service mismatch"}
	}

	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		return &authError{code: "AccessDenied", msg: "missing X-Amz-Date header"}
	}
	t, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return &authError{code: "AccessDenied", msg: "invalid X-Amz-Date"}
	}
	if t.Format("20060102") != auth.date {
		return &authError{code: "AccessDenied", msg: "credential date does not match X-Amz-Date"}
	}
	if diff := time.Since(t); diff > 15*time.Minute || diff < -15*time.Minute {
		return &authError{code: "RequestTimeTooSkewed", msg: "request timestamp outside allowed window"}
	}

	payloadHashHeader := strings.ToLower(r.Header.Get("X-Amz-Content-Sha256"))
	if len(payloadHashHeader) != 64 {
		return &authError{code: "AccessDenied", msg: "missing or invalid X-Amz-Content-Sha256"}
	}
	var hasContentSha, hasHost bool
	for _, h := range auth.signedHeaders {
		if strings.EqualFold(h, "x-amz-content-sha256") {
			hasContentSha = true
		}
		if strings.EqualFold(h, "host") {
			hasHost = true
		}
	}
	if !hasContentSha {
		return &authError{code: "AccessDenied", msg: "x-amz-content-sha256 must be a signed header"}
	}
	if !hasHost {
		return &authError{code: "AccessDenied", msg: "host must be a signed header"}
	}

	canonicalURI, err := sigv4CanonicalURI(rawPath)
	if err != nil {
		return &authError{code: "InvalidURI", msg: err.Error()}
	}
	canonicalQuery, err := sigv4CanonicalQuery(rawQuery)
	if err != nil {
		return &authError{code: "InvalidURI", msg: err.Error()}
	}
	canonicalHeaders, err := sigv4CanonicalHeaders(r, auth.signedHeaders)
	if err != nil {
		return &authError{code: "AccessDenied", msg: err.Error()}
	}
	signedHeadersList := sigv4SignedHeadersList(auth.signedHeaders)

	canonicalRequest := strings.Join([]string{
		r.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeadersList,
		payloadHashHeader,
	}, "\n")
	crHash := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", auth.date, auth.region, auth.service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hex.EncodeToString(crHash[:]),
	}, "\n")

	signingKey := sigv4SigningKey(srv.creds.SecretAccessKey, auth.date, auth.region, auth.service)
	expectedSig := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	if subtle.ConstantTimeCompare([]byte(expectedSig), []byte(strings.ToLower(auth.signature))) != 1 {
		return &authError{code: "SignatureDoesNotMatch", msg: "signature mismatch"}
	}

	actualHash := sha256.Sum256(body)
	if hex.EncodeToString(actualHash[:]) != payloadHashHeader {
		return &authError{code: "XAmzContentSHA256Mismatch", msg: "declared payload hash does not match body received"}
	}
	return nil
}

// =============================================================================
// 9. Request checksums (CRC32) and S3-shaped XML errors
// =============================================================================

// validateCRC32Header checks the ordinary (non-chunked) x-amz-checksum-crc32
// request header, if present, against the logical request payload bytes.
// A missing header is not an error -- CRC32 validation is opt-in per
// request, matching real S3's checksum headers.
func validateCRC32Header(r *http.Request, body []byte) error {
	h := r.Header.Get("x-amz-checksum-crc32")
	if h == "" {
		return nil
	}
	declared, err := base64.StdEncoding.DecodeString(h)
	if err != nil || len(declared) != 4 {
		return &authError{code: "InvalidRequest", msg: "invalid x-amz-checksum-crc32 header"}
	}
	want := binary.BigEndian.Uint32(declared)
	if got := crc32.ChecksumIEEE(body); got != want {
		return &authError{code: "BadDigest", msg: "crc32 checksum does not match request payload"}
	}
	return nil
}

type s3ErrorBody struct {
	XMLName  xml.Name `xml:"Error"`
	Code     string   `xml:"Code"`
	Message  string   `xml:"Message"`
	Resource string   `xml:"Resource,omitempty"`
}

func s3ErrorStatus(code string) int {
	switch code {
	case "NoSuchBucket", "NoSuchKey":
		return http.StatusNotFound
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "RequestTimeTooSkewed",
		"AuthorizationHeaderMalformed", "XAmzContentSHA256Mismatch":
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func writeS3Error(w http.ResponseWriter, code, message, resource string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(s3ErrorStatus(code))
	_ = xml.NewEncoder(w).Encode(s3ErrorBody{Code: code, Message: message, Resource: resource})
}

// =============================================================================
// 10. Raw HTTP routing and S3 operation handlers
//
// Server is a plain http.Handler -- deliberately not built on
// http.ServeMux, which cleans/redirects request paths (collapsing "//",
// resolving "..") before a handler ever sees them. That cleanup would
// silently invalidate SigV4 signatures computed over the original,
// unnormalized request-target. RequestURI/RawQuery are read directly and
// kept intact through authentication; only after auth succeeds do we
// percent-decode the path into a semantic bucket/key.
// =============================================================================

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

type Server struct {
	store  *Store
	creds  Credentials
	region string
}

func NewServer(store *Store, creds Credentials, region string) *Server {
	return &Server{store: store, creds: creds, region: region}
}

// splitRawRequestURI splits the server-observed, unmodified request
// target into its raw path and raw query, without any decoding or
// normalization.
func splitRawRequestURI(requestURI string) (path, query string) {
	if idx := strings.IndexByte(requestURI, '?'); idx >= 0 {
		return requestURI[:idx], requestURI[idx+1:]
	}
	return requestURI, ""
}

// splitBucketKey performs semantic (post-authentication) parsing of a raw
// path into a bucket name and object key, path-unescaping each component.
// This is deliberately independent of sigv4CanonicalURI's byte-exact
// re-encoding: by this point the request is already authenticated, and we
// just want the literal bucket/key strings.
func splitBucketKey(rawPath string) (bucket, key string, err error) {
	if !strings.HasPrefix(rawPath, "/") {
		return "", "", fmt.Errorf("request path must be absolute")
	}
	trimmed := rawPath[1:]
	bucketEnc, keyEnc := trimmed, ""
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		bucketEnc, keyEnc = trimmed[:idx], trimmed[idx+1:]
	}
	bucket, err = url.PathUnescape(bucketEnc)
	if err != nil {
		return "", "", fmt.Errorf("invalid bucket name encoding")
	}
	if bucket == "" {
		return "", "", fmt.Errorf("bucket name required")
	}
	key, err = url.PathUnescape(keyEnc)
	if err != nil {
		return "", "", fmt.Errorf("invalid key encoding")
	}
	return bucket, key, nil
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body exceeds maximum size of %d bytes", limit)
	}
	return data, nil
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawPath, rawQuery := splitRawRequestURI(r.RequestURI)

	body, err := readAllLimited(r.Body, maxRequestBodySize)
	if err != nil {
		writeS3Error(w, "InvalidRequest", "failed to read request body", rawPath)
		return
	}

	if err := srv.authenticate(r, rawPath, rawQuery, body); err != nil {
		var ae *authError
		if errors.As(err, &ae) {
			writeS3Error(w, ae.code, ae.msg, rawPath)
		} else {
			writeS3Error(w, "AccessDenied", err.Error(), rawPath)
		}
		return
	}

	if err := validateCRC32Header(r, body); err != nil {
		var ae *authError
		if errors.As(err, &ae) {
			writeS3Error(w, ae.code, ae.msg, rawPath)
		} else {
			writeS3Error(w, "InvalidRequest", err.Error(), rawPath)
		}
		return
	}

	bucket, key, err := splitBucketKey(rawPath)
	if err != nil {
		writeS3Error(w, "InvalidURI", err.Error(), rawPath)
		return
	}

	switch {
	case r.Method == http.MethodPut && key == "":
		srv.handleCreateBucket(w, bucket)
	case r.Method == http.MethodPut:
		srv.handlePutObject(w, r, bucket, key, body)
	case r.Method == http.MethodGet && key != "":
		srv.handleGetObject(w, bucket, key)
	default:
		writeS3Error(w, "MethodNotAllowed", "unsupported operation for this path", rawPath)
	}
}

func (srv *Server) handleCreateBucket(w http.ResponseWriter, bucket string) {
	if err := srv.store.CreateBucket(bucket); err != nil {
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket)
		return
	}
	w.Header().Set("Location", "/"+bucket)
	w.WriteHeader(http.StatusOK)
	fireTestHook(hookAfterAck)
}

func (srv *Server) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key string, body []byte) {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	metadata := map[string]string{}
	for name, vals := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-meta-") && len(vals) > 0 {
			metadata[strings.TrimPrefix(lower, "x-amz-meta-")] = vals[0]
		}
	}

	entry, err := srv.store.PutObject(bucket, key, body, contentType, metadata)
	if err != nil {
		if errors.Is(err, errNoSuchBucket) {
			writeS3Error(w, "NoSuchBucket", "the specified bucket does not exist", "/"+bucket+"/"+key)
			return
		}
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket+"/"+key)
		return
	}
	w.Header().Set("ETag", `"`+entry.etag+`"`)
	w.WriteHeader(http.StatusOK)
	fireTestHook(hookAfterAck)
}

func (srv *Server) handleGetObject(w http.ResponseWriter, bucket, key string) {
	entry, data, err := srv.store.GetObject(bucket, key)
	if err != nil {
		if errors.Is(err, errNoSuchBucket) {
			writeS3Error(w, "NoSuchBucket", "the specified bucket does not exist", "/"+bucket+"/"+key)
			return
		}
		if errors.Is(err, errNoSuchKey) {
			writeS3Error(w, "NoSuchKey", "the specified key does not exist", "/"+bucket+"/"+key)
			return
		}
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket+"/"+key)
		return
	}
	if entry.contentType != "" {
		w.Header().Set("Content-Type", entry.contentType)
	}
	w.Header().Set("ETag", `"`+entry.etag+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// =============================================================================
// 11. Lifecycle / main
// =============================================================================

func main() {
	storeDir := flag.String("store", "./zeros3-data", "path to the store directory")
	addr := flag.String("addr", "127.0.0.1:9000", "listen address")
	flag.Parse()

	store, err := OpenStore(*storeDir)
	if err != nil {
		log.Fatalf("zeros3: failed to open store: %v", err)
	}
	defer store.Close()

	srv := NewServer(store, Credentials{
		AccessKeyID:     defaultAccessKeyID,
		SecretAccessKey: defaultSecretAccessKey,
	}, defaultRegion)

	httpServer := &http.Server{Addr: *addr, Handler: srv}
	log.Printf("zeros3: listening on %s (store=%s)", *addr, *storeDir)
	log.Fatal(httpServer.ListenAndServe())
}
