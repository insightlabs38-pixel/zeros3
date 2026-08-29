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
	"io/fs"
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

	// Journal record type numbers (frozen storage format v1). These
	// numbers are part of the on-disk format: a new record kind gets a
	// new number, and none of these four is ever repurposed.
	recordTypeCreateBucket     = byte(1)
	recordTypePutObjectRoot    = byte(2)
	recordTypeDeleteObjectRoot = byte(3)
	recordTypeDeleteBucket     = byte(4)

	maxRequestBodySize = 256 * 1024 * 1024

	// Default credentials/region. ZeroS3 has no credential-management
	// story (no IAM/STS/KMS) -- a single static keypair is enough to
	// exercise SigV4 for its self-hosted, local-development scope.
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
	errBucketNotEmpty      = errors.New("bucket not empty")
	// errNoSuchDestinationBucket is CopyObject-specific: it lets the HTTP
	// handler tell a missing *destination* bucket apart from a missing
	// *source* bucket/key (both of which use errNoSuchBucket/errNoSuchKey
	// from the ordinary lookup path), so each gets its own S3 error
	// pointing at the right resource.
	errNoSuchDestinationBucket = errors.New("no such destination bucket")
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

// sortedMetadataKV converts a metadata map into the manifest's
// deterministic sorted-by-key representation, so two builds of the same
// logical metadata always serialize identically. Shared by buildManifestV1
// and CopyObject's metadata-REPLACE path.
func sortedMetadataKV(metadata map[string]string) []metadataKV {
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	md := make([]metadataKV, 0, len(keys))
	for _, k := range keys {
		md = append(md, metadataKV{Key: k, Value: metadata[k]})
	}
	return md
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
	md := sortedMetadataKV(metadata)
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
// Durability contract:
//
//   - A mutation is committed once its frame's bytes are written AND
//     fsynced. The in-memory namespace is updated only after that sync
//     succeeds, and only then does a PUT/CreateBucket get acknowledged
//     over HTTP. So: acknowledged ⇒ durable, per this contract.
//   - The converse does NOT hold: a crash between a frame's Write and a
//     successful Sync leaves that frame's durability genuinely
//     indeterminate, not "guaranteed absent". Depending on what the OS
//     and disk actually flushed before the crash, replay on restart may
//     legally observe either the previous complete state or the new
//     complete state -- both are acceptable outcomes of an unacknowledged
//     mutation. What replay must NEVER produce, under any circumstance,
//     is a partial or mixed state: a visible manifest that references
//     incomplete/missing chunks, or object bytes that blend two versions.
//     Every frame replay accepts is validated as a complete, CRC-checked
//     unit (see replayJournal), which is what makes that guarantee hold
//     even though the write/sync timing guarantee does not.
//   - A write or sync failure is treated as terminal for that Journal:
//     see the "poisoned" field below.
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

// journalDeleteObjectPayload is the record-type-3 payload: it removes one
// key's visible root from a bucket's namespace. It does not reference (and
// therefore cannot invalidate) the manifest/chunks the deleted root used to
// point at -- those remain on disk, immutable and readable by any other
// root that still references them, until a GC pass (not implemented)
// proves them unreachable.
type journalDeleteObjectPayload struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
}

// journalDeleteBucketPayload is the record-type-4 payload: it removes a
// bucket from the visible namespace. Live code (Store.DeleteBucket) only
// ever appends this record for a bucket that was, at that instant under
// Store.mu, present and empty, so replay can safely treat it the same way
// applyRecord treats put-object-root against an unknown bucket: a
// mismatch is store corruption, not a normal condition to tolerate.
type journalDeleteBucketPayload struct {
	Bucket string `json:"bucket"`
}

// Journal owns the on-disk visibility.log file and the strictly
// sequential append cursor into it.
type Journal struct {
	f           *os.File
	mu          sync.Mutex
	writeOffset int64
	nextSeq     uint64

	// poisoned holds the first write/sync failure this Journal ever hit,
	// if any. Once set (under mu), every future appendFrame call fails
	// immediately without touching the file: a write or sync failure
	// leaves durability genuinely uncertain (the frame's bytes may or may
	// not have reached disk), and continuing to append on top of that
	// uncertainty risks writing at a stale offset or reusing a sequence
	// number that a not-quite-failed write already claimed. The only way
	// out is a fresh Journal from a fresh openJournal call (i.e. closing
	// and reopening the store), which re-derives writeOffset/nextSeq from
	// whatever is actually, durably on disk.
	poisoned error
}

// appendFrame durably appends one journal frame and returns its sequence
// number. It writes the frame, fires the write-before-sync test hook,
// fsyncs, fires the after-sync test hook, and only then advances the
// append cursor -- so the cursor only ever reflects frames this process
// knows, for certain, were fully written and synced.
//
// If the Journal is already poisoned (a prior write or sync failed), this
// fails immediately without touching the file at all. If the write or
// sync in THIS call fails, the Journal is poisoned before returning: a
// write/sync failure means the frame's actual on-disk state is unknown
// (WriteAt can fail after writing some, all, or none of its bytes), so
// writeOffset/nextSeq are deliberately left unmoved and no further
// appends are allowed in this process -- appending on top of an uncertain
// tail could write over live bytes, reuse a sequence number, or produce a
// journal that looks fine to replay but silently drops what came before
// the failure. The caller (Store) must be reopened (fresh openJournal,
// which replays whatever is truly durable) before mutations can resume;
// reads are unaffected, since they never touch the journal.
func (j *Journal) appendFrame(recType byte, payload []byte) (uint64, error) {
	if len(payload) > maxJournalPayload {
		return 0, fmt.Errorf("journal: payload of %d bytes exceeds max %d", len(payload), maxJournalPayload)
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.poisoned != nil {
		return 0, fmt.Errorf("journal: poisoned by a prior durability failure, refusing further mutations until the store is reopened: %w", j.poisoned)
	}

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
		wrapped := fmt.Errorf("journal: write failed: %w", err)
		j.poisoned = wrapped
		return 0, wrapped
	}
	fireTestHook(hookAfterJournalWriteBeforeSync)
	if err := j.f.Sync(); err != nil {
		wrapped := fmt.Errorf("journal: sync failed: %w", err)
		j.poisoned = wrapped
		return 0, wrapped
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
		switch recType {
		case recordTypeCreateBucket, recordTypePutObjectRoot, recordTypeDeleteObjectRoot, recordTypeDeleteBucket:
			// known record type
		default:
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
	name      string
	createdAt time.Time
	objects   map[string]*objectEntry
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
			s.buckets[p.Bucket] = &bucketEntry{name: p.Bucket, createdAt: p.CreatedAt, objects: map[string]*objectEntry{}}
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
	case recordTypeDeleteObjectRoot:
		var p journalDeleteObjectPayload
		if err := json.Unmarshal(rec.payload, &p); err != nil {
			return fmt.Errorf("seq %d: %w", rec.seq, err)
		}
		b, ok := s.buckets[p.Bucket]
		if !ok {
			return fmt.Errorf("seq %d: delete-object-root for unknown bucket %q", rec.seq, p.Bucket)
		}
		delete(b.objects, p.Key)
	case recordTypeDeleteBucket:
		var p journalDeleteBucketPayload
		if err := json.Unmarshal(rec.payload, &p); err != nil {
			return fmt.Errorf("seq %d: %w", rec.seq, err)
		}
		if _, ok := s.buckets[p.Bucket]; !ok {
			return fmt.Errorf("seq %d: delete-bucket for unknown bucket %q", rec.seq, p.Bucket)
		}
		delete(s.buckets, p.Bucket)
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
	createdAt := time.Now().UTC()
	payload, err := json.Marshal(journalCreateBucketPayload{Bucket: name, CreatedAt: createdAt})
	if err != nil {
		return err
	}
	if _, err := s.journal.appendFrame(recordTypeCreateBucket, payload); err != nil {
		return err
	}
	s.buckets[name] = &bucketEntry{name: name, createdAt: createdAt, objects: map[string]*objectEntry{}}
	return nil
}

// ListBuckets returns the names of every currently visible bucket, sorted
// for deterministic ordering, along with their creation times.
func (s *Store) ListBuckets() []bucketEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bucketEntry, 0, len(s.buckets))
	for _, b := range s.buckets {
		out = append(out, bucketEntry{name: b.name, createdAt: b.createdAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// HeadBucket reports whether name is currently a visible bucket.
func (s *Store) HeadBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.buckets[name]; !ok {
		return errNoSuchBucket
	}
	return nil
}

// DeleteBucket removes an empty, currently visible bucket from the
// namespace. It is journal-backed (record type 4) and durable: the bucket
// is only removed from the in-memory namespace after the journal frame
// recording its deletion has been appended and synced. Deletion changes
// namespace reachability only -- it never touches any chunk or manifest
// file, since those may still (or may again, after a future PutObject)
// be referenced by other roots.
func (s *Store) DeleteBucket(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[name]
	if !ok {
		return errNoSuchBucket
	}
	if len(b.objects) > 0 {
		return errBucketNotEmpty
	}
	payload, err := json.Marshal(journalDeleteBucketPayload{Bucket: name})
	if err != nil {
		return err
	}
	if _, err := s.journal.appendFrame(recordTypeDeleteBucket, payload); err != nil {
		return err
	}
	delete(s.buckets, name)
	return nil
}

// DeleteObject removes key's visible root from bucket. Deleting a key that
// does not currently exist is idempotent success (no journal record is
// appended, matching CreateBucket's existing idempotent-recreate policy),
// matching the non-versioned DELETE semantics ZeroS3 supports. As with
// DeleteBucket, this changes namespace reachability only: the manifest and
// chunks the deleted root pointed at are left on disk, immutable, for a
// later GC pass and for any other root that still references them.
func (s *Store) DeleteObject(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[bucket]
	if !ok {
		return errNoSuchBucket
	}
	if _, exists := b.objects[key]; !exists {
		return nil
	}
	payload, err := json.Marshal(journalDeleteObjectPayload{Bucket: bucket, Key: key})
	if err != nil {
		return err
	}
	if _, err := s.journal.appendFrame(recordTypeDeleteObjectRoot, payload); err != nil {
		return err
	}
	delete(b.objects, key)
	return nil
}

// PutObject runs the full commit pipeline for one object version: CDC
// chunking, durable CAS publication of every chunk, durable manifest
// publication, and finally an append+sync of the visibility journal. Only
// after the journal sync succeeds is the in-memory namespace updated;
// only after that does this function return, so a caller can safely
// acknowledge success the moment it returns. If the journal append fails
// (see Journal.appendFrame), that failure poisons the journal and this
// error propagates to the caller, who must not acknowledge success; any
// chunks/manifest already published are orphaned but harmless.
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

	return s.commitObjectRoot(bucket, key, manUUID, manSHA, man)
}

// commitObjectRoot appends the visibility-journal record that makes
// (bucket,key) point at manUUID/manSHA/man and applies it to the
// in-memory namespace. This is PutObject's original commit tail, factored
// out so CopyObject can share the exact same tested crash-safety
// discipline instead of a second, subtly different implementation of it:
// bucket existence is re-checked at the actual commit point (not just at
// the caller's entry), the journal append+sync is the sole durability
// boundary, and the in-memory map is updated only after that succeeds.
func (s *Store) commitObjectRoot(bucket, key, manUUID string, manSHA [32]byte, man manifestV1) (*objectEntry, error) {
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
	// Re-check bucket existence here, at the actual commit point, not just
	// at entry to the caller: the CDC/CAS/manifest work leading up to this
	// call runs without holding s.mu (by design -- it's slow and doesn't
	// touch the mutable namespace), which leaves a window for a concurrent
	// DeleteBucket to remove this bucket before the commit critical
	// section below runs. Journal record ordering is what decides which
	// operation "won"; committing a put-object-root against a namespace
	// that no longer has the bucket would both corrupt the in-memory map
	// (s.buckets[bucket] is nil) and produce a journal that fails replay
	// (applyRecord requires the bucket to exist for a put-object-root).
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.Unlock()
		return nil, errNoSuchBucket
	}
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
	obj, err := s.lookupObject(bucket, key)
	if err != nil {
		return nil, nil, err
	}

	man, err := s.readVerifiedManifest(obj.manifestUUID, obj.manifestSHA256)
	if err != nil {
		return nil, nil, err
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

// lookupObject resolves bucket/key against the journal-derived namespace
// without reading the manifest or any chunk data.
func (s *Store) lookupObject(bucket, key string) (*objectEntry, error) {
	s.mu.Lock()
	b, ok := s.buckets[bucket]
	if !ok {
		s.mu.Unlock()
		return nil, errNoSuchBucket
	}
	obj, ok := b.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, errNoSuchKey
	}
	return obj, nil
}

// readVerifiedManifest reads the manifest named by id and confirms its
// exact bytes hash to wantSHA (the hash the journal recorded when this
// root was committed), so a corrupted or substituted manifest file is
// detected here rather than trusted blindly.
func (s *Store) readVerifiedManifest(id string, wantSHA [32]byte) (manifestV1, error) {
	man, manBytes, err := s.readManifest(id)
	if err != nil {
		return manifestV1{}, fmt.Errorf("%w: %v", errManifestUnavailable, err)
	}
	if gotSum := sha256.Sum256(manBytes); gotSum != wantSHA {
		return manifestV1{}, fmt.Errorf("%w: manifest %s hash mismatch", errManifestUnavailable, id)
	}
	return man, nil
}

// HeadObject resolves bucket/key and returns its cached namespace entry
// (size/ETag/Content-Type, all cheap to keep in memory) plus its manifest
// (read once, for user metadata and the creation timestamp) -- but never
// touches chunk data, since a HEAD response never has a body.
func (s *Store) HeadObject(bucket, key string) (*objectEntry, manifestV1, error) {
	obj, err := s.lookupObject(bucket, key)
	if err != nil {
		return nil, manifestV1{}, err
	}
	man, err := s.readVerifiedManifest(obj.manifestUUID, obj.manifestSHA256)
	if err != nil {
		return nil, manifestV1{}, err
	}
	return obj, man, nil
}

// =============================================================================
// 7b. ListObjectsV2
//
// Concurrency policy: ListObjectsV2 takes a private, consistent snapshot
// of the bucket's current key set (a plain copy made while holding s.mu)
// at the start of each individual call, then does all filtering/sorting/
// grouping/pagination against that snapshot without holding the lock.
// This guarantees each *single* call sees one coherent, non-torn view of
// the namespace -- never a key whose objectEntry pointer was concurrently
// replaced mid-scan.
//
// Across separate calls in a paginated sequence (a ContinuationToken
// chain), ZeroS3 makes no cross-call snapshot/isolation guarantee, the
// same as real S3: if a PUT or DELETE lands between two page requests,
// the next page reflects the namespace as it exists at that later call,
// which may shift where the "resume after this key" cursor lands (a key
// added before the cursor won't retroactively appear; one added after it
// will). What pagination never does, even under concurrent mutation, is
// duplicate or corrupt a result: each page is computed fresh from
// whatever snapshot exists at that moment, using ordinary immutable Go
// values (strings, copied structs), never a pointer into a namespace
// that could change under the caller's feet.
// =============================================================================

// listedObject is one Contents entry ZeroS3 has decided to return, paired
// with the namespace entry needed to render it.
type listedObject struct {
	key   string
	entry *objectEntry
}

// listObjectsV2Page is one page of ListObjectsV2 results.
type listObjectsV2Page struct {
	contents       []listedObject
	commonPrefixes []string
	truncated      bool
	// lastConsumedKey is the last input key (from the sorted, prefix
	// filtered candidate list) that this page fully accounted for, used
	// to build NextContinuationToken. It is only meaningful when
	// truncated is true.
	lastConsumedKey string
}

// ListObjectsV2 implements the planned ESSENTIAL subset: prefix,
// delimiter/CommonPrefixes, max-keys, and continuation via startAfterKey
// (the key decoded from a client-supplied ContinuationToken). Keys are
// ordered by plain Go string comparison, which is exactly UTF-8 byte
// lexical order since Go strings are byte sequences.
func (s *Store) ListObjectsV2(bucket, prefix, delimiter, startAfterKey string, maxKeys int) (listObjectsV2Page, error) {
	s.mu.Lock()
	b, ok := s.buckets[bucket]
	if !ok {
		s.mu.Unlock()
		return listObjectsV2Page{}, errNoSuchBucket
	}
	keys := make([]string, 0, len(b.objects))
	entries := make(map[string]*objectEntry, len(b.objects))
	for k, e := range b.objects {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		keys = append(keys, k)
		entries[k] = e
	}
	s.mu.Unlock()
	sort.Strings(keys)

	var page listObjectsV2Page
	if maxKeys <= 0 {
		return page, nil
	}

	var lastGroupPrefix string
	haveGroup := false
	for _, k := range keys {
		if startAfterKey != "" && k <= startAfterKey {
			continue
		}
		remainder := k[len(prefix):]
		if delimiter != "" {
			if idx := strings.Index(remainder, delimiter); idx >= 0 {
				cp := prefix + remainder[:idx+len(delimiter)]
				if haveGroup && cp == lastGroupPrefix {
					// Another key folding into the common prefix group
					// we already emitted: it doesn't add a new result
					// unit, but it does advance how far this page reaches.
					page.lastConsumedKey = k
					continue
				}
				if len(page.contents)+len(page.commonPrefixes) >= maxKeys {
					page.truncated = true
					break
				}
				page.commonPrefixes = append(page.commonPrefixes, cp)
				lastGroupPrefix = cp
				haveGroup = true
				page.lastConsumedKey = k
				continue
			}
		}
		if len(page.contents)+len(page.commonPrefixes) >= maxKeys {
			page.truncated = true
			break
		}
		page.contents = append(page.contents, listedObject{key: k, entry: entries[k]})
		page.lastConsumedKey = k
	}
	return page, nil
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
// signs them. Presigned URLs and aws-chunked/trailer payloads are not
// implemented.
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
	case "BucketNotEmpty":
		return http.StatusConflict
	case "MethodNotAllowed":
		return http.StatusMethodNotAllowed
	case "InvalidRange":
		return http.StatusRequestedRangeNotSatisfiable
	default:
		return http.StatusBadRequest
	}
}

func writeS3Error(w http.ResponseWriter, code, message, resource string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(s3ErrorStatus(code))
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(s3ErrorBody{Code: code, Message: message, Resource: resource})
}

// writeS3ErrorStatusOnly reports an S3-style error via status code alone,
// with no XML body. HEAD responses must never carry a body -- a client
// reading Content-Length off a HEAD's headers would otherwise try to read
// a body that both violates HTTP HEAD semantics and was never intended to
// describe the (nonexistent) resource.
func writeS3ErrorStatusOnly(w http.ResponseWriter, code string) {
	w.WriteHeader(s3ErrorStatus(code))
}

// =============================================================================
// 9b. ListBuckets / ListObjectsV2 XML response types and continuation tokens
// =============================================================================

type xmlBucket struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type listAllMyBucketsResult struct {
	XMLName xml.Name    `xml:"ListAllMyBucketsResult"`
	Buckets []xmlBucket `xml:"Buckets>Bucket"`
}

type xmlContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type xmlCommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type listBucketResult struct {
	XMLName               xml.Name          `xml:"ListBucketResult"`
	Name                  string            `xml:"Name"`
	Prefix                string            `xml:"Prefix"`
	Delimiter             string            `xml:"Delimiter,omitempty"`
	MaxKeys               int               `xml:"MaxKeys"`
	KeyCount              int               `xml:"KeyCount"`
	IsTruncated           bool              `xml:"IsTruncated"`
	ContinuationToken     string            `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string            `xml:"NextContinuationToken,omitempty"`
	Contents              []xmlContent      `xml:"Contents"`
	CommonPrefixes        []xmlCommonPrefix `xml:"CommonPrefixes,omitempty"`
}

// iso8601 renders t the way S3 renders timestamps in XML response bodies:
// UTC, millisecond precision, a literal "Z" offset.
func iso8601(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

// continuationTokenVersion prefixes every ZeroS3 continuation token. It
// exists so a future token format change can be detected and rejected
// explicitly rather than silently misparsed.
const continuationTokenVersion = "zs3ct1:"

// encodeContinuationToken produces an opaque ContinuationToken/
// NextContinuationToken value for lastKey (the last input key the current
// page fully accounted for). The token is base64 of a small versioned
// string; it never contains a filesystem path, chunk hash, or manifest
// UUID -- only the caller-supplied object key that a client already knows
// exists, encoded only to keep the token opaque and to leave room for a
// version tag.
func encodeContinuationToken(lastKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(continuationTokenVersion + lastKey))
}

// decodeContinuationToken recovers the "resume after this key" cursor from
// a client-supplied ContinuationToken. Any malformed, non-base64, or
// wrong-version token is reported as an error so the caller can return an
// S3-shaped InvalidArgument response rather than silently starting over or
// panicking on a malformed index.
func decodeContinuationToken(tok string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", fmt.Errorf("invalid continuation token")
	}
	s := string(raw)
	if !strings.HasPrefix(s, continuationTokenVersion) {
		return "", fmt.Errorf("unsupported or corrupt continuation token")
	}
	return strings.TrimPrefix(s, continuationTokenVersion), nil
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

	// The bucket-less root path ("GET /") is ListBuckets: it has no
	// bucket/key to parse, so it's handled before splitBucketKey, which
	// requires (and every other operation needs) a non-empty bucket name.
	if rawPath == "/" || rawPath == "" {
		if r.Method == http.MethodGet {
			srv.handleListBuckets(w)
			return
		}
		writeS3Error(w, "MethodNotAllowed", "unsupported operation for this path", rawPath)
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
	case r.Method == http.MethodGet && key == "":
		srv.handleListObjectsV2(w, bucket, rawQuery)
	case r.Method == http.MethodGet:
		srv.handleGetObject(w, r, bucket, key)
	case r.Method == http.MethodHead && key == "":
		srv.handleHeadBucket(w, bucket)
	case r.Method == http.MethodHead:
		srv.handleHeadObject(w, bucket, key)
	case r.Method == http.MethodDelete && key == "":
		srv.handleDeleteBucket(w, bucket)
	case r.Method == http.MethodDelete:
		srv.handleDeleteObject(w, bucket, key)
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

func (srv *Server) handleListBuckets(w http.ResponseWriter) {
	buckets := srv.store.ListBuckets()
	result := listAllMyBucketsResult{Buckets: make([]xmlBucket, len(buckets))}
	for i, b := range buckets {
		result.Buckets[i] = xmlBucket{Name: b.name, CreationDate: iso8601(b.createdAt)}
	}
	writeXML(w, http.StatusOK, result)
}

func (srv *Server) handleHeadBucket(w http.ResponseWriter, bucket string) {
	if err := srv.store.HeadBucket(bucket); err != nil {
		writeS3ErrorStatusOnly(w, "NoSuchBucket")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writeBucketOrInternalError renders the S3-shaped error for a store call
// whose only expected failure besides success is a missing bucket --
// PutObject, DeleteObject, and ListObjectsV2 all share this exact two-way
// mapping (a missing bucket vs. anything else), so they share this helper
// instead of each repeating the same errors.Is/writeS3Error pair.
// DeleteBucket (which also distinguishes BucketNotEmpty) and CopyObject
// (which maps a missing bucket to a *different* S3 code depending on
// whether it's the source or destination) have their own, genuinely
// different mappings and are not forced through this one.
func writeBucketOrInternalError(w http.ResponseWriter, err error, resource string) {
	if errors.Is(err, errNoSuchBucket) {
		writeS3Error(w, "NoSuchBucket", "the specified bucket does not exist", resource)
		return
	}
	writeS3Error(w, "InternalError", err.Error(), resource)
}

func (srv *Server) handleDeleteBucket(w http.ResponseWriter, bucket string) {
	err := srv.store.DeleteBucket(bucket)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
		fireTestHook(hookAfterAck)
	case errors.Is(err, errNoSuchBucket):
		writeS3Error(w, "NoSuchBucket", "the specified bucket does not exist", "/"+bucket)
	case errors.Is(err, errBucketNotEmpty):
		writeS3Error(w, "BucketNotEmpty", "the bucket you tried to delete is not empty", "/"+bucket)
	default:
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket)
	}
}

func (srv *Server) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key string, body []byte) {
	// A PUT carrying x-amz-copy-source is CopyObject, not an ordinary body
	// upload -- same HTTP verb, different S3 operation, exactly as real S3
	// distinguishes them.
	if copySource := r.Header.Get("X-Amz-Copy-Source"); copySource != "" {
		srv.handleCopyObject(w, r, bucket, key, copySource)
		return
	}

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
		writeBucketOrInternalError(w, err, "/"+bucket+"/"+key)
		return
	}
	w.Header().Set("ETag", `"`+entry.etag+`"`)
	w.WriteHeader(http.StatusOK)
	fireTestHook(hookAfterAck)
}

// writeObjectHeaders sets every header a GET/HEAD response shares: cached
// namespace fields (Content-Type/ETag/Content-Length) plus the metadata
// and creation time carried in the object's manifest.
func writeObjectHeaders(w http.ResponseWriter, entry *objectEntry, man manifestV1) {
	if entry.contentType != "" {
		w.Header().Set("Content-Type", entry.contentType)
	}
	w.Header().Set("ETag", `"`+entry.etag+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(entry.size, 10))
	w.Header().Set("Last-Modified", man.CreatedAt.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	for _, kv := range man.Metadata {
		w.Header().Set("x-amz-meta-"+kv.Key, kv.Value)
	}
}

// writeGetObjectError renders the S3-shaped error for a GetObject/
// GetObjectRange failure, shared by both the full-object and Range paths.
func writeGetObjectError(w http.ResponseWriter, bucket, key string, err error) {
	switch {
	case errors.Is(err, errNoSuchBucket):
		writeS3Error(w, "NoSuchBucket", "the specified bucket does not exist", "/"+bucket+"/"+key)
	case errors.Is(err, errNoSuchKey):
		writeS3Error(w, "NoSuchKey", "the specified key does not exist", "/"+bucket+"/"+key)
	default:
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket+"/"+key)
	}
}

// handleGetObject dispatches to a full-object 200 response, unless the
// request carries a satisfiable single-range Range header, in which case
// it serves a manifest-driven 206 (see Section 15). A Range header this
// build doesn't understand (multi-range, malformed syntax) is ignored,
// matching RFC 7233's allowance to serve the full entity instead of
// rejecting the request; a syntactically valid but unsatisfiable range
// (e.g. starting past the object's end) is answered with 416.
func (srv *Server) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		srv.handleGetObjectFull(w, bucket, key)
		return
	}

	// Interpreting a Range header (clamping "end", resolving a suffix
	// range) needs the object's size, so resolve it once via HeadObject
	// -- no chunk I/O -- before deciding whether this is a 200, 206, or
	// 416 response.
	entry, man, err := srv.store.HeadObject(bucket, key)
	if err != nil {
		writeGetObjectError(w, bucket, key, err)
		return
	}
	rng, present, satisfiable := parseRangeSpec(rangeHeader, entry.size)
	if !present {
		srv.handleGetObjectFull(w, bucket, key)
		return
	}
	if !satisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", entry.size))
		writeS3Error(w, "InvalidRange", "the requested range is not satisfiable", "/"+bucket+"/"+key)
		return
	}

	_, _, data, err := srv.store.GetObjectRange(bucket, key, rng)
	if err != nil {
		writeGetObjectError(w, bucket, key, err)
		return
	}
	writeObjectHeaders(w, entry, man)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng.start, rng.end, entry.size))
	w.Header().Set("Content-Length", strconv.FormatInt(rng.end-rng.start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data)
}

func (srv *Server) handleGetObjectFull(w http.ResponseWriter, bucket, key string) {
	entry, data, err := srv.store.GetObject(bucket, key)
	if err != nil {
		writeGetObjectError(w, bucket, key, err)
		return
	}
	// GetObject already read and hash-verified this exact manifest once
	// (to get the chunk list); reading it again here to render metadata
	// headers is a small amount of duplicate I/O in exchange for keeping
	// GetObject's return signature -- and every existing caller of it --
	// unchanged.
	man, err := srv.store.readVerifiedManifest(entry.manifestUUID, entry.manifestSHA256)
	if err != nil {
		writeS3Error(w, "InternalError", err.Error(), "/"+bucket+"/"+key)
		return
	}
	writeObjectHeaders(w, entry, man)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (srv *Server) handleHeadObject(w http.ResponseWriter, bucket, key string) {
	entry, man, err := srv.store.HeadObject(bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, errNoSuchBucket):
			writeS3ErrorStatusOnly(w, "NoSuchBucket")
		case errors.Is(err, errNoSuchKey):
			writeS3ErrorStatusOnly(w, "NoSuchKey")
		default:
			writeS3ErrorStatusOnly(w, "InternalError")
		}
		return
	}
	writeObjectHeaders(w, entry, man)
	w.WriteHeader(http.StatusOK)
}

func (srv *Server) handleDeleteObject(w http.ResponseWriter, bucket, key string) {
	if err := srv.store.DeleteObject(bucket, key); err != nil {
		writeBucketOrInternalError(w, err, "/"+bucket+"/"+key)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	fireTestHook(hookAfterAck)
}

// parseListObjectsV2Query extracts the ESSENTIAL ListObjectsV2 query
// parameters. max-keys defaults to (and is clamped to) 1000, matching
// real S3's default/maximum page size.
func parseListObjectsV2Query(rawQuery string) (listType, prefix, delimiter, continuationToken string, maxKeys int, err error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", "", "", "", 0, fmt.Errorf("malformed query string")
	}
	listType = values.Get("list-type")
	prefix = values.Get("prefix")
	delimiter = values.Get("delimiter")
	continuationToken = values.Get("continuation-token")

	maxKeys = 1000
	if raw := values.Get("max-keys"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 0 {
			return "", "", "", "", 0, fmt.Errorf("invalid max-keys %q", raw)
		}
		maxKeys = n
	}
	if maxKeys > 1000 {
		maxKeys = 1000
	}
	return listType, prefix, delimiter, continuationToken, maxKeys, nil
}

func (srv *Server) handleListObjectsV2(w http.ResponseWriter, bucket, rawQuery string) {
	listType, prefix, delimiter, continuationToken, maxKeys, err := parseListObjectsV2Query(rawQuery)
	if err != nil {
		writeS3Error(w, "InvalidArgument", err.Error(), "/"+bucket)
		return
	}
	// ZeroS3 implements only the V2 listing API; the legacy V1 GET-bucket
	// listing shape (no list-type param) is rejected explicitly rather
	// than silently misinterpreted as V2.
	if listType != "2" {
		writeS3Error(w, "InvalidArgument", "only list-type=2 (ListObjectsV2) is supported", "/"+bucket)
		return
	}

	var startAfterKey string
	if continuationToken != "" {
		startAfterKey, err = decodeContinuationToken(continuationToken)
		if err != nil {
			writeS3Error(w, "InvalidArgument", "invalid continuation token", "/"+bucket)
			return
		}
	}

	page, err := srv.store.ListObjectsV2(bucket, prefix, delimiter, startAfterKey, maxKeys)
	if err != nil {
		writeBucketOrInternalError(w, err, "/"+bucket)
		return
	}

	result := listBucketResult{
		Name:              bucket,
		Prefix:            prefix,
		Delimiter:         delimiter,
		MaxKeys:           maxKeys,
		KeyCount:          len(page.contents) + len(page.commonPrefixes),
		IsTruncated:       page.truncated,
		ContinuationToken: continuationToken,
	}
	if page.truncated {
		result.NextContinuationToken = encodeContinuationToken(page.lastConsumedKey)
	}
	for _, obj := range page.contents {
		man, merr := srv.store.readVerifiedManifest(obj.entry.manifestUUID, obj.entry.manifestSHA256)
		if merr != nil {
			writeS3Error(w, "InternalError", merr.Error(), "/"+bucket)
			return
		}
		result.Contents = append(result.Contents, xmlContent{
			Key:          obj.key,
			LastModified: iso8601(man.CreatedAt),
			ETag:         `"` + obj.entry.etag + `"`,
			Size:         obj.entry.size,
			StorageClass: "STANDARD",
		})
	}
	for _, cp := range page.commonPrefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, xmlCommonPrefix{Prefix: cp})
	}
	writeXML(w, http.StatusOK, result)
}

// =============================================================================
// 11. CopyObject
//
// CopyObject is the payoff of the manifest+CAS design: copying an object
// never re-chunks, re-reads, or re-uploads its payload, and never rewrites
// an existing CAS chunk file. Both metadata directives -- COPY and REPLACE
// -- publish a brand-new destination manifest (new UUID, new version ID,
// new CreatedAt): a copy is a genuinely new object version with its own
// Last-Modified/version identity, even though its payload is byte-for-byte
// identical to the source's. The chunk list, object SHA-256, and ETag are
// cloned verbatim from the source manifest; only metadata/Content-Type
// differ (COPY: copied from the source; REPLACE: taken from the request).
// The measurable claim is therefore "CopyObject writes zero new CAS
// payload bytes" -- not "zero bytes of any kind": both directives publish
// a small new manifest file, so manifest_file_bytes may grow even though
// chunk_store_file_bytes never does.
// =============================================================================

type metadataDirective int

const (
	metadataDirectiveCopy metadataDirective = iota
	metadataDirectiveReplace
)

// CopyObjectRequest describes one validated CopyObject call.
type CopyObjectRequest struct {
	SrcBucket, SrcKey string
	DstBucket, DstKey string
	Directive         metadataDirective
	ContentType       string            // used only when Directive == metadataDirectiveReplace
	Metadata          map[string]string // used only when Directive == metadataDirectiveReplace
}

// CopyObject publishes a new root at (DstBucket,DstKey) that reconstructs
// to exactly the source object's bytes, under a fresh manifest identity.
// Before committing, it validates that every chunk the source manifest
// references is actually present (a cheap Stat, not a re-hash -- deep
// corruption detection is verify's job, not every copy's), matching the
// crash-safety rule that a new root is only published after its
// referenced chunks are confirmed available.
func (s *Store) CopyObject(req CopyObjectRequest) (*objectEntry, manifestV1, error) {
	srcObj, err := s.lookupObject(req.SrcBucket, req.SrcKey)
	if err != nil {
		return nil, manifestV1{}, err
	}
	srcMan, err := s.readVerifiedManifest(srcObj.manifestUUID, srcObj.manifestSHA256)
	if err != nil {
		return nil, manifestV1{}, err
	}
	for _, c := range srcMan.Chunks {
		sum, herr := decodeHexSHA256(c.SHA256)
		if herr != nil {
			return nil, manifestV1{}, fmt.Errorf("copy: source manifest has a malformed chunk reference: %w", herr)
		}
		if _, serr := os.Stat(s.chunkPath(sum)); serr != nil {
			return nil, manifestV1{}, fmt.Errorf("copy: source chunk %s is not available: %w", c.SHA256, serr)
		}
	}

	s.mu.Lock()
	_, dstBucketExists := s.buckets[req.DstBucket]
	s.mu.Unlock()
	if !dstBucketExists {
		return nil, manifestV1{}, errNoSuchDestinationBucket
	}

	// Both directives clone the source manifest's payload identity
	// (Chunks -- immutable and never mutated in place, so sharing its
	// backing array is safe -- ObjectSHA256, ETag, TotalLength) byte-for-
	// byte, without reading a single chunk payload byte, then stamp a
	// fresh manifest/version identity and timestamp: the destination is a
	// new object version, not an alias of the source's. ContentType and
	// Metadata start out copied from the source (the COPY directive's
	// contract) and are overwritten below only for REPLACE.
	dstMan := srcMan
	dstMan.ManifestUUID = newUUIDv7()
	dstMan.VersionID = dstMan.ManifestUUID
	dstMan.CreatedAt = time.Now().UTC()
	if req.Directive == metadataDirectiveReplace {
		dstMan.ContentType = req.ContentType
		dstMan.Metadata = sortedMetadataKV(req.Metadata)
	}

	manUUID, manSHA, err := s.publishManifest(dstMan)
	if err != nil {
		return nil, manifestV1{}, fmt.Errorf("copy: manifest publish failed: %w", err)
	}
	fireTestHook(hookAfterManifestPublished)
	entry, err := s.commitObjectRoot(req.DstBucket, req.DstKey, manUUID, manSHA, dstMan)
	return entry, dstMan, err
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

// parseCopySource parses an x-amz-copy-source header value into a
// bucket/key pair. AWS accepts both "/bucket/key" and "bucket/key" (an
// optional leading slash); a "?versionId=..." suffix is rejected, since
// ZeroS3 does not implement versioning.
//
// This is deliberately NOT splitBucketKey (which strictly url.PathUnescape
// decodes, correct for a request *path*, which the HTTP client library
// itself guarantees is well-formed percent-encoding). x-amz-copy-source is
// an ordinary header VALUE, and inspecting the pinned AWS SDK Go v2's
// actual wire traffic shows it applies zero percent-encoding of its own:
// whatever bytes the caller puts in CopySource -- including raw spaces,
// '%', '+', '?', '#', and Unicode -- are sent completely unescaped. A
// strict decoder would reject the common case (a raw '%' not part of a
// valid escape is a parse error to url.PathUnescape) even though the
// source key is perfectly valid. So the key/bucket components here are
// decoded leniently via lenientPercentDecode: well-formed %XX escapes are
// honored (a caller MAY still choose to pre-encode, and AWS's own docs
// recommend it), but a stray '%' is kept literal instead of erroring, and
// '+' is never treated as a space (there is no application/
// x-www-form-urlencoded convention on this header, only literal bytes and
// optional RFC 3986 escapes). The bucket/key boundary is the first raw,
// undecoded '/' -- never path.Clean/filepath.Clean, so "..", "//", and
// slash-containing keys are preserved exactly as received.
func parseCopySource(raw string) (bucket, key string, err error) {
	raw = strings.TrimPrefix(raw, "/")
	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		if strings.Contains(raw[idx:], "versionId=") {
			return "", "", fmt.Errorf("versioned copy source is not supported")
		}
		// A raw '?' that isn't "?versionId=..." is still ambiguous with
		// query syntax (matching AWS's own documented CopySource
		// contract): a caller who needs a literal '?' in a source key
		// must send it pre-encoded as %3F, which never reaches this
		// branch because it contains no raw '?' byte.
		raw = raw[:idx]
	}
	if raw == "" {
		return "", "", fmt.Errorf("copy source is empty")
	}
	bucketEnc, keyEnc := raw, ""
	if idx := strings.IndexByte(raw, '/'); idx >= 0 {
		bucketEnc, keyEnc = raw[:idx], raw[idx+1:]
	}
	bucket = lenientPercentDecode(bucketEnc)
	if bucket == "" {
		return "", "", fmt.Errorf("copy source bucket name required")
	}
	key = lenientPercentDecode(keyEnc)
	if key == "" {
		return "", "", fmt.Errorf("copy source key required")
	}
	return bucket, key, nil
}

// lenientPercentDecode decodes RFC 3986 %XX escapes in s, tolerating
// literal bytes that aren't valid escapes instead of rejecting them (see
// parseCopySource for why this differs from the stdlib's strict
// url.PathUnescape). A '%' is decoded only when followed by exactly two
// valid hex digits; any other '%' is copied through unchanged. '+' is
// always copied through unchanged -- never decoded to a space.
func lenientPercentDecode(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hiOK := hexDigitValue(s[i+1])
			lo, loOK := hexDigitValue(s[i+2])
			if hiOK && loOK {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hexDigitValue reports the 4-bit value of a single ASCII hex digit.
func hexDigitValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func (srv *Server) handleCopyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
	srcBucket, srcKey, err := parseCopySource(copySource)
	if err != nil {
		writeS3Error(w, "InvalidArgument", "invalid x-amz-copy-source: "+err.Error(), "/"+dstBucket+"/"+dstKey)
		return
	}

	directive := metadataDirectiveCopy
	switch strings.ToUpper(r.Header.Get("X-Amz-Metadata-Directive")) {
	case "", "COPY":
		directive = metadataDirectiveCopy
	case "REPLACE":
		directive = metadataDirectiveReplace
	default:
		writeS3Error(w, "InvalidArgument", "unsupported x-amz-metadata-directive", "/"+dstBucket+"/"+dstKey)
		return
	}

	req := CopyObjectRequest{SrcBucket: srcBucket, SrcKey: srcKey, DstBucket: dstBucket, DstKey: dstKey, Directive: directive}
	if directive == metadataDirectiveReplace {
		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		req.ContentType = contentType
		req.Metadata = map[string]string{}
		for name, vals := range r.Header {
			lower := strings.ToLower(name)
			if strings.HasPrefix(lower, "x-amz-meta-") && len(vals) > 0 {
				req.Metadata[strings.TrimPrefix(lower, "x-amz-meta-")] = vals[0]
			}
		}
	}

	entry, man, err := srv.store.CopyObject(req)
	if err != nil {
		switch {
		case errors.Is(err, errNoSuchDestinationBucket):
			writeS3Error(w, "NoSuchBucket", "the specified destination bucket does not exist", "/"+dstBucket+"/"+dstKey)
		case errors.Is(err, errNoSuchBucket), errors.Is(err, errNoSuchKey):
			writeS3Error(w, "NoSuchKey", "the specified source key does not exist", "/"+srcBucket+"/"+srcKey)
		default:
			writeS3Error(w, "InternalError", err.Error(), "/"+dstBucket+"/"+dstKey)
		}
		return
	}
	writeXML(w, http.StatusOK, copyObjectResult{ETag: `"` + entry.etag + `"`, LastModified: iso8601(man.CreatedAt)})
}

// =============================================================================
// 12. Stats and reachability scanning
//
// Every field below has exactly the meaning STATS_SPEC.md gives it, and
// two kinds of number are never conflated:
//
//   - "logical"/"reference"/"unique"/"exclusive"/"shared" fields are
//     derived from the journal-derived namespace and the manifests it
//     reaches -- what a scope refers to, not what is physically stored.
//     A chunk shared between two buckets is never reported as "physical
//     bytes owned by" either one.
//   - "*_file_bytes" fields are exact filesystem measurements (os.Stat
//     over the store's managed directories) -- what is physically on
//     disk, independent of whether anything still references it.
//
// All of it is computed by direct scan on each call, never a persisted
// counter, per STORAGE_MODEL.md's "prefer exact scans over transactional
// counters" rule.
// =============================================================================

// statsScope selects which part of the namespace a stats/verify call
// reports on. The zero value (every field empty) means the whole store.
type statsScope struct {
	bucket string // "" = every bucket
	prefix string // key prefix filter within bucket; "" = no filter
	key    string // exact key (object scope); takes precedence over prefix
}

func (sel statsScope) matches(bucket, key string) bool {
	if sel.bucket == "" {
		return true
	}
	if bucket != sel.bucket {
		return false
	}
	if sel.key != "" {
		return key == sel.key
	}
	return strings.HasPrefix(key, sel.prefix)
}

// namespaceObject is one flattened (bucket, key, entry) triple from a
// point-in-time snapshot of the store's visible namespace.
type namespaceObject struct {
	bucket string
	key    string
	entry  *objectEntry
}

// snapshotNamespace takes a private, consistent copy of every bucket's
// current key set under Store.mu, then returns it for the caller to walk
// without holding the lock -- the same policy ListObjectsV2 already uses.
// objectEntry values are never mutated in place after construction (a
// PUT/DELETE always replaces the map entry with a fresh pointer or
// removes it), so sharing these pointers out of the lock is safe: a
// concurrent write can only ever add a new entry or swap/remove one this
// snapshot already captured, never rewrite the fields this snapshot is
// currently reading.
func (s *Store) snapshotNamespace() []namespaceObject {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []namespaceObject
	for bname, b := range s.buckets {
		for key, e := range b.objects {
			out = append(out, namespaceObject{bucket: bname, key: key, entry: e})
		}
	}
	return out
}

// bucketNames returns every currently visible bucket name.
func (s *Store) bucketNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.buckets))
	for name := range s.buckets {
		out = append(out, name)
	}
	return out
}

// chunkObservation accumulates what a stats scan learns about one
// distinct chunk digest while walking every reachable manifest.
type chunkObservation struct {
	length     int64
	inScope    bool
	outOfScope bool
}

// fileScanTotals is one directory's exact byte/file-count totals, split
// into everything present versus the subset not in a supplied reachable
// set. Shared by stats (chunk_store_file_bytes/manifest_file_bytes) and
// verify (unreachable counts/reclaimable_bytes).
type fileScanTotals struct {
	totalBytes       int64
	totalCount       int
	unreachableBytes int64
	unreachableCount int
}

func walkFileBytes(root string, isUnreachable func(name string) bool) (fileScanTotals, error) {
	var t fileScanTotals
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		t.totalBytes += info.Size()
		t.totalCount++
		if isUnreachable(d.Name()) {
			t.unreachableBytes += info.Size()
			t.unreachableCount++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return fileScanTotals{}, err
	}
	return t, nil
}

// scanChunkFiles walks store/chunks and classifies every published chunk
// file's bytes as reachable or not, using the digest that names the file
// (chunk files are never named by anything else).
func (s *Store) scanChunkFiles(reachable map[string]bool) (fileScanTotals, error) {
	return walkFileBytes(filepath.Join(s.root, "chunks"), func(name string) bool { return !reachable[name] })
}

// scanManifestFiles walks store/manifests and classifies every published
// manifest file's bytes as reachable or not, by its UUID filename.
func (s *Store) scanManifestFiles(reachable map[string]bool) (fileScanTotals, error) {
	return walkFileBytes(filepath.Join(s.root, "manifests"), func(name string) bool {
		return !reachable[strings.TrimSuffix(name, ".json")]
	})
}

func dirSizeBytes(root string) (int64, error) {
	t, err := walkFileBytes(root, func(string) bool { return false })
	return t.totalBytes, err
}

func fileSizeOrZero(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// StatsResult is the exact set of fields STATS_SPEC.md defines for one
// scan, human-readable field names doubling as stable JSON field names.
type StatsResult struct {
	ScopeBucket string `json:"scope_bucket,omitempty"`
	ScopePrefix string `json:"scope_prefix,omitempty"`
	ScopeKey    string `json:"scope_key,omitempty"`

	BucketCount         int   `json:"bucket_count"`
	CurrentObjectCount  int   `json:"current_object_count"`
	VersionCount        int   `json:"version_count"`
	LogicalCurrentBytes int64 `json:"logical_current_bytes"`
	LogicalVersionBytes int64 `json:"logical_version_bytes"`

	LogicalChunkReferenceBytes int64 `json:"logical_chunk_reference_bytes"`
	LogicalChunkReferenceCount int64 `json:"logical_chunk_reference_count"`
	ScopeUniqueChunkBytes      int64 `json:"scope_unique_chunk_bytes"`
	ScopeUniqueChunkCount      int64 `json:"scope_unique_chunk_count"`
	ScopeExclusiveChunkBytes   int64 `json:"scope_exclusive_chunk_bytes"`
	ScopeSharedChunkBytes      int64 `json:"scope_shared_chunk_bytes"`

	UniqueReachableChunkBytes int64 `json:"unique_reachable_chunk_bytes"`

	ChunkStoreFileBytes  int64 `json:"chunk_store_file_bytes"`
	ManifestFileBytes    int64 `json:"manifest_file_bytes"`
	JournalFileBytes     int64 `json:"journal_file_bytes"`
	TemporaryFileBytes   int64 `json:"temporary_file_bytes"`
	ActualStoreFileBytes int64 `json:"actual_store_file_bytes"`
	ReclaimableBytes     int64 `json:"reclaimable_bytes"`

	DedupAvoidedBytes    int64   `json:"dedup_avoided_bytes"`
	DedupReduction       float64 `json:"dedup_reduction"`
	UniqueToLogicalRatio float64 `json:"unique_to_logical_ratio"`
}

// computeStats performs one exact scan/derivation pass over the store's
// current namespace and on-disk files for the given scope. It never
// consults or updates any persisted counter -- every field is derived
// fresh from the journal-reconstructed namespace and a filesystem walk,
// per STORAGE_MODEL.md's stats/index guidance.
func (s *Store) computeStats(sel statsScope) (StatsResult, error) {
	all := s.snapshotNamespace()
	bucketSet := map[string]bool{}
	for _, o := range all {
		bucketSet[o.bucket] = true
	}
	// A bucket can be visible with zero objects; make sure it still
	// counts toward bucket_count even though it contributes no
	// namespaceObject rows above.
	for _, name := range s.bucketNames() {
		bucketSet[name] = true
	}

	res := StatsResult{ScopeBucket: sel.bucket, ScopePrefix: sel.prefix, ScopeKey: sel.key}
	if sel.bucket == "" {
		res.BucketCount = len(bucketSet)
	} else if bucketSet[sel.bucket] {
		res.BucketCount = 1
	}

	manifestCache := map[string]manifestV1{}
	loadManifest := func(o namespaceObject) (manifestV1, error) {
		if m, ok := manifestCache[o.entry.manifestUUID]; ok {
			return m, nil
		}
		m, err := s.readVerifiedManifest(o.entry.manifestUUID, o.entry.manifestSHA256)
		if err != nil {
			return manifestV1{}, err
		}
		manifestCache[o.entry.manifestUUID] = m
		return m, nil
	}

	chunkObs := map[string]*chunkObservation{} // hex sha256 -> observation, store-wide
	for _, o := range all {
		inScope := sel.matches(o.bucket, o.key)
		if inScope {
			res.CurrentObjectCount++
			res.LogicalCurrentBytes += o.entry.size
		}
		man, err := loadManifest(o)
		if err != nil {
			return StatsResult{}, fmt.Errorf("stats: reading manifest for %s/%s: %w", o.bucket, o.key, err)
		}
		for _, c := range man.Chunks {
			ob, ok := chunkObs[c.SHA256]
			if !ok {
				ob = &chunkObservation{length: c.Length}
				chunkObs[c.SHA256] = ob
			}
			if inScope {
				ob.inScope = true
				res.LogicalChunkReferenceBytes += c.Length
				res.LogicalChunkReferenceCount++
			} else {
				ob.outOfScope = true
			}
		}
	}
	// No version retention exists -- every PUT replaces its key's one
	// visible root, and DELETE simply removes it -- so the only
	// "retained committed version" for any key is its current one.
	// version_count/logical_version_bytes are therefore
	// identical to current_object_count/logical_current_bytes; this
	// becomes a genuinely separate figure only if a future milestone adds
	// retained-version semantics.
	res.VersionCount = res.CurrentObjectCount
	res.LogicalVersionBytes = res.LogicalCurrentBytes

	var uniqueReachableAll int64
	for _, ob := range chunkObs {
		uniqueReachableAll += ob.length
		if ob.inScope {
			res.ScopeUniqueChunkBytes += ob.length
			res.ScopeUniqueChunkCount++
			if ob.outOfScope {
				res.ScopeSharedChunkBytes += ob.length
			} else {
				res.ScopeExclusiveChunkBytes += ob.length
			}
		}
	}
	res.UniqueReachableChunkBytes = uniqueReachableAll

	if res.LogicalChunkReferenceBytes > 0 {
		res.DedupAvoidedBytes = res.LogicalChunkReferenceBytes - res.ScopeUniqueChunkBytes
		res.DedupReduction = float64(res.DedupAvoidedBytes) / float64(res.LogicalChunkReferenceBytes)
		res.UniqueToLogicalRatio = float64(res.ScopeUniqueChunkBytes) / float64(res.LogicalChunkReferenceBytes)
	}

	reachableManifests := make(map[string]bool, len(manifestCache))
	for uuid := range manifestCache {
		reachableManifests[uuid] = true
	}
	reachableChunks := make(map[string]bool, len(chunkObs))
	for sha := range chunkObs {
		reachableChunks[sha] = true
	}

	chunkScan, err := s.scanChunkFiles(reachableChunks)
	if err != nil {
		return StatsResult{}, fmt.Errorf("stats: scanning chunks: %w", err)
	}
	manifestScan, err := s.scanManifestFiles(reachableManifests)
	if err != nil {
		return StatsResult{}, fmt.Errorf("stats: scanning manifests: %w", err)
	}
	journalBytes, err := fileSizeOrZero(filepath.Join(s.root, "journal", "visibility.log"))
	if err != nil {
		return StatsResult{}, fmt.Errorf("stats: scanning journal: %w", err)
	}
	tmpBytes, err := dirSizeBytes(filepath.Join(s.root, "tmp"))
	if err != nil {
		return StatsResult{}, fmt.Errorf("stats: scanning tmp: %w", err)
	}
	formatBytes, err := fileSizeOrZero(filepath.Join(s.root, "FORMAT.json"))
	if err != nil {
		return StatsResult{}, fmt.Errorf("stats: scanning FORMAT.json: %w", err)
	}

	res.ChunkStoreFileBytes = chunkScan.totalBytes
	res.ManifestFileBytes = manifestScan.totalBytes
	res.JournalFileBytes = journalBytes
	res.TemporaryFileBytes = tmpBytes
	res.ActualStoreFileBytes = chunkScan.totalBytes + manifestScan.totalBytes + journalBytes + tmpBytes + formatBytes
	// Every extra chunk/manifest byte here is exactly classified: it
	// belongs to a file whose digest/UUID is not in the reachable set
	// computed above, not a naive "store bytes minus unique bytes"
	// subtraction (STATS_SPEC.md's explicit warning against that
	// shortcut). tmp/ is always reclaimable: it is same-store staging
	// space only, never referenced by any committed manifest/journal
	// record (see STORAGE_MODEL.md's publication model).
	res.ReclaimableBytes = chunkScan.unreachableBytes + manifestScan.unreachableBytes + tmpBytes

	return res, nil
}

// =============================================================================
// 13. Verify
//
// verify never repairs or deletes anything -- it only reports. It runs
// against a private snapshot of the namespace (snapshotNamespace), the
// same concurrency policy already proven for ListObjectsV2 and stats, so
// a concurrent PUT/DELETE cannot make verify observe a torn view of what
// it is checking. Default (non-deep) verification checks structure,
// references, and lengths cheaply; deep verification additionally
// re-hashes every reachable chunk's actual bytes.
// =============================================================================

type VerifyIssue struct {
	Kind    string `json:"kind"` // "missing" | "corrupt" | "invalid"
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

// verifiedManifestCacheEntry caches one manifest UUID's parsed content and
// the SHA-256 of its own file bytes, so Verify reads/parses a given
// manifest file at most once even when several roots reference the same
// UUID. Caching the *content* this way is safe and cheap; caching a
// verdict is not -- see the per-root hash check next to this cache's use
// in Verify, which still runs unconditionally for every root.
type verifiedManifestCacheEntry struct {
	man            manifestV1
	sha            [32]byte
	structurallyOK bool
}

type VerifyResult struct {
	Deep bool `json:"deep"`

	JournalFramesChecked int  `json:"journal_frames_checked"`
	JournalOK            bool `json:"journal_ok"`

	ManifestsChecked int `json:"manifests_checked"`
	ChunksChecked    int `json:"chunks_checked"`

	Missing int `json:"missing"`
	Corrupt int `json:"corrupt"`
	Invalid int `json:"invalid"`

	UnreachableManifests int   `json:"unreachable_manifests"`
	UnreachableChunks    int   `json:"unreachable_chunks"`
	ReclaimableBytes     int64 `json:"reclaimable_bytes"`

	Issues []VerifyIssue `json:"issues"`
}

// OK reports whether verify found zero integrity failures. Unreachable/
// reclaimable garbage is not by itself a failure -- it is expected under
// the "deletion changes roots, not chunks" model -- so it never affects
// OK(); only Missing/Corrupt/Invalid and journal replay do.
func (r VerifyResult) OK() bool {
	return r.Missing == 0 && r.Corrupt == 0 && r.Invalid == 0 && r.JournalOK
}

func (r *VerifyResult) addIssue(kind, subject, detail string) {
	switch kind {
	case "missing":
		r.Missing++
	case "corrupt":
		r.Corrupt++
	case "invalid":
		r.Invalid++
	}
	r.Issues = append(r.Issues, VerifyIssue{Kind: kind, Subject: subject, Detail: detail})
}

// Verify runs the essential verify contract: store/journal structural
// checks, per-reachable-manifest checks, and chunk checks (basic by
// default, byte-for-byte re-hashed when deep is true). It returns a
// non-nil error only for a fatal scan failure (e.g. the journal file
// can't be opened at all); ordinary integrity problems are reported as
// Issues in the result, which the caller inspects via VerifyResult.OK().
func (s *Store) Verify(deep bool) (VerifyResult, error) {
	res := VerifyResult{Deep: deep}

	// --- Store / journal ---
	if s.format.StoreFormatVersion != storeFormatVersion ||
		s.format.CDCFormatVersion != cdcFormatVersion ||
		s.format.HashAlgorithm != "sha256" {
		res.addIssue("invalid", "FORMAT.json", "unsupported store/CDC format version or hash algorithm")
	}
	jf, err := os.Open(filepath.Join(s.root, "journal", "visibility.log"))
	if err != nil {
		return res, fmt.Errorf("verify: opening journal: %w", err)
	}
	_, _, records, jerr := replayJournal(jf)
	jf.Close()
	if jerr != nil {
		res.addIssue("corrupt", "journal/visibility.log", jerr.Error())
	} else {
		res.JournalOK = true
	}
	res.JournalFramesChecked = len(records)

	// --- Namespace snapshot: exactly what "reachable" means today (no
	// version retention yet, so the only reachable roots are current
	// ones -- see the identical note in computeStats). ---
	all := s.snapshotNamespace()
	reachableManifests := map[string]bool{}
	reachableChunks := map[string]bool{}
	manifestCache := map[string]verifiedManifestCacheEntry{}

	for _, o := range all {
		subject := o.bucket + "/" + o.key
		cached, ok := manifestCache[o.entry.manifestUUID]
		if !ok {
			path := filepath.Join(s.root, "manifests", o.entry.manifestUUID+".json")
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				if os.IsNotExist(rerr) {
					res.addIssue("missing", subject, "manifest file "+o.entry.manifestUUID+".json does not exist")
				} else {
					res.addIssue("invalid", subject, rerr.Error())
				}
				continue
			}
			// cached.sha is computed from the manifest file's own bytes,
			// independent of any specific root -- it is what every root
			// that references this UUID gets checked against below, on
			// every iteration, not just this first (cache-filling) one.
			cached.sha = sha256.Sum256(data)
			cached.structurallyOK = true
			if uerr := json.Unmarshal(data, &cached.man); uerr != nil {
				res.addIssue("invalid", subject, "manifest json does not parse: "+uerr.Error())
				cached.structurallyOK = false
			} else if cached.man.ManifestFormatVersion != manifestFormatVersion || cached.man.CDCFormatVersion != cdcFormatVersion || cached.man.HashAlgorithm != "sha256" {
				res.addIssue("invalid", subject, "manifest declares an unsupported format/CDC/hash version")
				cached.structurallyOK = false
			} else {
				var sum int64
				validRefs := true
				for _, c := range cached.man.Chunks {
					if _, herr := decodeHexSHA256(c.SHA256); herr != nil {
						res.addIssue("invalid", subject, "manifest chunk reference has malformed sha256: "+c.SHA256)
						validRefs = false
						continue
					}
					if c.Length < 0 {
						res.addIssue("invalid", subject, "manifest chunk reference has a negative length")
						validRefs = false
						continue
					}
					sum += c.Length
				}
				if !validRefs {
					cached.structurallyOK = false
				} else if sum != cached.man.TotalLength {
					res.addIssue("invalid", subject, fmt.Sprintf("manifest chunk lengths sum to %d, want total_length %d", sum, cached.man.TotalLength))
					cached.structurallyOK = false
				}
			}
			manifestCache[o.entry.manifestUUID] = cached
			res.ManifestsChecked++
		}
		// This root's own journal-recorded manifest hash is checked here
		// on every iteration -- whether this UUID was just parsed above
		// or was already cached from an earlier root -- because caching
		// the manifest's parsed content/hash is only a read/parse
		// optimization; it must never let a second root silently inherit
		// a "verified" status it hasn't earned. Two roots can legally
		// share one manifest UUID, but each still carries its own
		// journal-recorded hash claim, and each must independently prove
		// journal-recorded SHA256 == actual manifest-file SHA256.
		if cached.sha != o.entry.manifestSHA256 {
			res.addIssue("corrupt", subject, "manifest file sha256 does not match this root's journal-recorded reference")
			continue
		}
		if !cached.structurallyOK {
			continue
		}
		reachableManifests[o.entry.manifestUUID] = true
		for _, c := range cached.man.Chunks {
			reachableChunks[c.SHA256] = true
		}
	}

	// --- Chunks referenced by every reachable manifest ---
	checkedChunk := map[string]bool{}
	badChunk := map[string]bool{}
	for _, cached := range manifestCache {
		if !cached.structurallyOK {
			continue
		}
		for _, c := range cached.man.Chunks {
			if checkedChunk[c.SHA256] {
				continue
			}
			checkedChunk[c.SHA256] = true
			res.ChunksChecked++
			sum, herr := decodeHexSHA256(c.SHA256)
			if herr != nil {
				res.addIssue("invalid", "chunk "+c.SHA256, herr.Error())
				badChunk[c.SHA256] = true
				continue
			}
			path := s.chunkPath(sum)
			info, serr := os.Stat(path)
			if serr != nil {
				if os.IsNotExist(serr) {
					res.addIssue("missing", "chunk "+c.SHA256, "chunk file does not exist")
				} else {
					res.addIssue("invalid", "chunk "+c.SHA256, serr.Error())
				}
				badChunk[c.SHA256] = true
				continue
			}
			if info.Size() != c.Length {
				res.addIssue("corrupt", "chunk "+c.SHA256, fmt.Sprintf("file length %d does not match manifest length %d", info.Size(), c.Length))
				badChunk[c.SHA256] = true
				continue
			}
			if deep {
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					res.addIssue("missing", "chunk "+c.SHA256, rerr.Error())
					badChunk[c.SHA256] = true
					continue
				}
				if got := sha256.Sum256(data); got != sum {
					res.addIssue("corrupt", "chunk "+c.SHA256, "content hash does not match its content-addressed name")
					badChunk[c.SHA256] = true
				}
			}
		}
	}

	// --- Deep only: whole-object digest ---
	//
	// Per-chunk hashing above proves every individual chunk's bytes match
	// its own content-addressed name, but it cannot catch a manifest that
	// simply names the wrong object_sha256, or lists otherwise-intact
	// chunks in a corrupted order -- GetObject doesn't check object_sha256
	// either, so nothing else in ZeroS3 would ever notice. This closes
	// that gap by feeding every reachable manifest's chunks, in the
	// manifest's own logical order, into one streaming SHA-256 hasher per
	// manifest -- never buffering the reconstructed object -- and
	// comparing the result (and the streamed byte count, against
	// total_length) to what the manifest claims. Skipped for a manifest
	// whose chunks already failed the check above: hashing known-bad
	// bytes would only add a confusing, redundant issue.
	if deep {
		for uuid, cached := range manifestCache {
			if !cached.structurallyOK {
				continue
			}
			subject := "manifest " + uuid
			wantSum, herr := decodeHexSHA256(cached.man.ObjectSHA256)
			if herr != nil {
				res.addIssue("invalid", subject, "object_sha256 is malformed: "+herr.Error())
				continue
			}
			chunksOK := true
			for _, c := range cached.man.Chunks {
				if badChunk[c.SHA256] {
					chunksOK = false
					break
				}
			}
			if !chunksOK {
				continue
			}
			h := sha256.New()
			var streamed int64
			readFailed := false
			for _, c := range cached.man.Chunks {
				sum, _ := decodeHexSHA256(c.SHA256) // already validated above
				data, rerr := s.casRead(sum)
				if rerr != nil {
					res.addIssue("corrupt", subject, "chunk "+c.SHA256+" could not be re-read for whole-object verification: "+rerr.Error())
					readFailed = true
					break
				}
				h.Write(data)
				streamed += int64(len(data))
			}
			if readFailed {
				continue
			}
			if streamed != cached.man.TotalLength {
				res.addIssue("corrupt", subject, fmt.Sprintf("streamed %d chunk bytes, want total_length %d", streamed, cached.man.TotalLength))
				continue
			}
			if gotSum := [32]byte(h.Sum(nil)); gotSum != wantSum {
				res.addIssue("corrupt", subject, "whole-object sha256 does not match manifest object_sha256")
			}
		}
	}

	// --- Unreachable/reclaimable accounting (informational, not a failure) ---
	chunkScan, serr := s.scanChunkFiles(reachableChunks)
	if serr != nil {
		return res, fmt.Errorf("verify: scanning chunks: %w", serr)
	}
	manifestScan, merr := s.scanManifestFiles(reachableManifests)
	if merr != nil {
		return res, fmt.Errorf("verify: scanning manifests: %w", merr)
	}
	tmpBytes, terr := dirSizeBytes(filepath.Join(s.root, "tmp"))
	if terr != nil {
		return res, fmt.Errorf("verify: scanning tmp: %w", terr)
	}
	res.UnreachableManifests = manifestScan.unreachableCount
	res.UnreachableChunks = chunkScan.unreachableCount
	res.ReclaimableBytes = chunkScan.unreachableBytes + manifestScan.unreachableBytes + tmpBytes

	return res, nil
}

// =============================================================================
// 14. Single-range GET
//
// A Range request is answered by walking the manifest's chunk length
// list to find exactly the CAS chunks that overlap the requested logical
// interval, and reading only those -- never reconstructing the whole
// object first and slicing it, so memory/IO for a range read is bounded
// by the range size (plus at most the two boundary chunks), not by
// object size.
// =============================================================================

// byteRange is an inclusive, 0-based logical byte interval.
type byteRange struct{ start, end int64 }

// parseRangeSpec parses a single "bytes=..." Range header value against
// an object of the given size. Multi-range requests (a comma-separated
// spec) are intentionally unsupported and are treated exactly like a
// header that doesn't parse: ok=false with satisfiable=false, which
// tells the caller to ignore Range entirely and serve the full object --
// RFC 7233 explicitly allows a server to do this for range forms it
// doesn't support, rather than rejecting the request outright. A range
// that parses fine but shares no bytes with the object (e.g. start at or
// past size, or a zero-length suffix) reports ok=true, satisfiable=false,
// which the caller must answer with 416.
func parseRangeSpec(header string, size int64) (rng byteRange, ok, satisfiable bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return byteRange{}, false, false
	}
	spec := strings.TrimPrefix(header, prefix)
	if strings.Contains(spec, ",") {
		return byteRange{}, false, false // multi-range: unsupported, ignore
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return byteRange{}, false, false
	}
	startStr, endStr := spec[:dash], spec[dash+1:]

	if startStr == "" {
		// Suffix range: "bytes=-N" means the last N bytes.
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n < 0 {
			return byteRange{}, false, false
		}
		if n == 0 || size == 0 {
			return byteRange{}, true, false
		}
		start := size - n
		if start < 0 {
			start = 0
		}
		return byteRange{start: start, end: size - 1}, true, true
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return byteRange{}, false, false
	}
	if size == 0 || start >= size {
		return byteRange{}, true, false
	}
	if endStr == "" {
		return byteRange{start: start, end: size - 1}, true, true
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return byteRange{}, false, false
	}
	if end >= size { // clamp end to the object's actual length
		end = size - 1
	}
	return byteRange{start: start, end: end}, true, true
}

// readManifestRange reconstructs exactly [rng.start, rng.end] (inclusive)
// of the object man describes, reading only the CAS chunks that overlap
// that interval.
func (s *Store) readManifestRange(man manifestV1, rng byteRange) ([]byte, error) {
	out := make([]byte, 0, rng.end-rng.start+1)
	var offset int64
	for _, c := range man.Chunks {
		chunkStart := offset
		chunkEnd := offset + c.Length - 1 // inclusive
		offset += c.Length
		if chunkEnd < rng.start {
			continue
		}
		if chunkStart > rng.end {
			break
		}
		sum, err := decodeHexSHA256(c.SHA256)
		if err != nil {
			return nil, err
		}
		data, err := s.casRead(sum)
		if err != nil {
			return nil, fmt.Errorf("chunk read failed: %w", err)
		}
		if int64(len(data)) != c.Length {
			return nil, fmt.Errorf("chunk %s: length mismatch", c.SHA256)
		}
		lo := int64(0)
		if rng.start > chunkStart {
			lo = rng.start - chunkStart
		}
		hi := c.Length
		if rng.end < chunkEnd {
			hi = rng.end - chunkStart + 1
		}
		out = append(out, data[lo:hi]...)
	}
	if int64(len(out)) != rng.end-rng.start+1 {
		return nil, fmt.Errorf("range reconstruction length mismatch")
	}
	return out, nil
}

// GetObjectRange resolves bucket/key and reconstructs only the requested
// byte range, never the whole object.
func (s *Store) GetObjectRange(bucket, key string, rng byteRange) (*objectEntry, manifestV1, []byte, error) {
	obj, err := s.lookupObject(bucket, key)
	if err != nil {
		return nil, manifestV1{}, nil, err
	}
	man, err := s.readVerifiedManifest(obj.manifestUUID, obj.manifestSHA256)
	if err != nil {
		return nil, manifestV1{}, nil, err
	}
	data, err := s.readManifestRange(man, rng)
	if err != nil {
		return nil, manifestV1{}, nil, err
	}
	return obj, man, data, nil
}

// =============================================================================
// 15. CLI: stats / verify
//
// Compact verbs; stdout carries the requested result/data, stderr carries
// diagnostics, and a nonzero exit reports incomplete/failed work -- per
// CLI_SPEC.md. `serve` is both the default command (so the existing
// `zeros3 -store DIR -addr ADDR` invocation form keeps working unchanged)
// and an explicit one (`zeros3 serve -store DIR -addr ADDR`).
// =============================================================================

func printStatsHuman(w io.Writer, r StatsResult) {
	scope := "(whole store)"
	switch {
	case r.ScopeKey != "":
		scope = r.ScopeBucket + "/" + r.ScopeKey
	case r.ScopePrefix != "":
		scope = r.ScopeBucket + "/" + r.ScopePrefix + "*"
	case r.ScopeBucket != "":
		scope = r.ScopeBucket
	}
	fmt.Fprintf(w, "ZeroS3 stats\n")
	fmt.Fprintf(w, "scope            %s\n", scope)
	fmt.Fprintf(w, "buckets          %d\n", r.BucketCount)
	fmt.Fprintf(w, "objects          %d current | %d versions\n", r.CurrentObjectCount, r.VersionCount)
	fmt.Fprintf(w, "logical          %d bytes current | %d bytes versions\n", r.LogicalCurrentBytes, r.LogicalVersionBytes)
	fmt.Fprintf(w, "chunk refs       %d refs (%d bytes) | %d unique (%d bytes)\n",
		r.LogicalChunkReferenceCount, r.LogicalChunkReferenceBytes, r.ScopeUniqueChunkCount, r.ScopeUniqueChunkBytes)
	fmt.Fprintf(w, "sharing          %d bytes exclusive | %d bytes shared outside scope\n", r.ScopeExclusiveChunkBytes, r.ScopeSharedChunkBytes)
	fmt.Fprintf(w, "dedup            %d bytes avoided | %.1f%% reduction | %.3f unique/logical\n",
		r.DedupAvoidedBytes, r.DedupReduction*100, r.UniqueToLogicalRatio)
	fmt.Fprintf(w, "unique reachable %d bytes (store-global)\n", r.UniqueReachableChunkBytes)
	fmt.Fprintf(w, "store files      %d bytes chunks | %d bytes manifests | %d bytes journal | %d bytes temp\n",
		r.ChunkStoreFileBytes, r.ManifestFileBytes, r.JournalFileBytes, r.TemporaryFileBytes)
	fmt.Fprintf(w, "actual/reclaim   %d bytes actual | %d bytes reclaimable\n", r.ActualStoreFileBytes, r.ReclaimableBytes)
}

func printVerifyHuman(w io.Writer, r VerifyResult) {
	mode := "basic"
	if r.Deep {
		mode = "deep"
	}
	fmt.Fprintf(w, "ZeroS3 verify (%s)\n", mode)
	fmt.Fprintf(w, "journal          %d frames checked | ok=%v\n", r.JournalFramesChecked, r.JournalOK)
	fmt.Fprintf(w, "manifests        %d checked\n", r.ManifestsChecked)
	fmt.Fprintf(w, "chunks           %d checked\n", r.ChunksChecked)
	fmt.Fprintf(w, "integrity        %d missing | %d corrupt | %d invalid\n", r.Missing, r.Corrupt, r.Invalid)
	fmt.Fprintf(w, "reclaimable      %d unreachable manifests | %d unreachable chunks | %d bytes\n",
		r.UnreachableManifests, r.UnreachableChunks, r.ReclaimableBytes)
	for _, iss := range r.Issues {
		fmt.Fprintf(w, "  %s: %s: %s\n", iss.Kind, iss.Subject, iss.Detail)
	}
	if r.OK() {
		fmt.Fprintln(w, "result           OK")
	} else {
		fmt.Fprintln(w, "result           FAILED")
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	storeDir := fs.String("store", "./zeros3-data", "path to the store directory")
	addr := fs.String("addr", "127.0.0.1:9000", "listen address")
	fs.Parse(args)

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

func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	storeDir := fs.String("store", "./zeros3-data", "path to the store directory")
	bucket := fs.String("bucket", "", "limit to one bucket")
	prefix := fs.String("prefix", "", "limit to keys under this prefix (requires -bucket)")
	key := fs.String("key", "", "limit to one exact object key (requires -bucket)")
	asJSON := fs.Bool("json", false, "emit JSON instead of human-readable text")
	fs.Parse(args)

	if (*prefix != "" || *key != "") && *bucket == "" {
		fmt.Fprintln(os.Stderr, "zeros3: -prefix/-key require -bucket")
		os.Exit(2)
	}

	store, err := OpenStore(*storeDir)
	if err != nil {
		log.Fatalf("zeros3: failed to open store: %v", err)
	}
	defer store.Close()

	res, err := store.computeStats(statsScope{bucket: *bucket, prefix: *prefix, key: *key})
	if err != nil {
		fmt.Fprintf(os.Stderr, "zeros3: stats failed: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			log.Fatalf("zeros3: %v", err)
		}
		return
	}
	printStatsHuman(os.Stdout, res)
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	storeDir := fs.String("store", "./zeros3-data", "path to the store directory")
	deep := fs.Bool("deep", false, "re-hash every reachable chunk's actual bytes")
	asJSON := fs.Bool("json", false, "emit JSON instead of human-readable text")
	fs.Parse(args)

	store, err := OpenStore(*storeDir)
	if err != nil {
		log.Fatalf("zeros3: failed to open store: %v", err)
	}
	defer store.Close()

	res, err := store.Verify(*deep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zeros3: verify failed: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			log.Fatalf("zeros3: %v", err)
		}
	} else {
		printVerifyHuman(os.Stdout, res)
	}
	if !res.OK() {
		os.Exit(1)
	}
}

// =============================================================================
// 16. Lifecycle / main
// =============================================================================

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		runServe(args)
	case "stats":
		runStats(args)
	case "verify":
		runVerify(args)
	default:
		fmt.Fprintf(os.Stderr, "zeros3: unknown command %q (want serve, stats, or verify)\n", cmd)
		os.Exit(2)
	}
}
