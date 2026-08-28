# ZeroS3 — M1 Status

## M1 status

**COMPLETE.**

All M1 exit-contract requirements are implemented and covered by passing
tests, including under `go test -race`. No M2/optional work was started
(see "M2 NOT STARTED" below).

## Exact toolchain

- `go version go1.27.0 linux/amd64` (installed fresh for this milestone;
  the environment originally shipped Go 1.24.7 as default, which does not
  satisfy the "Go 1.27.x" hard constraint, so Go 1.27.0 was downloaded
  from `go.dev/dl` and made the active toolchain).
- `go.mod`: `module zeros3`, `go 1.27.0`, **no `require` block**.
- Build: `CGO_ENABLED=0 go build .` — stdlib only, no cgo.
- Platform: Linux amd64 only (the only release-blocking platform for M1,
  per scope).

## Implemented

- **Store**: `store/FORMAT.json` (immutable, versioned), directory layout
  (`journal/`, `chunks/`, `manifests/`, `tmp/`), clean rejection of an
  unsupported format version.
- **CDC v1**: streaming Gear-hash content-defined chunking. Min 16KiB /
  target 64KiB / max 256KiB, FastCDC-style two-mask normalization
  (~1/65536 boundary probability below target, ~1/32768 above), forced
  cut at the max. Gear table is 256 deterministic 64-bit entries derived
  from SHA-256 of a fixed, version-tagged seed (`ZeroS3/CDCv1/GearTable`).
- **CAS**: SHA-256 content-addressed chunks at
  `chunks/<aa>/<bb>/<64-hex>`, dedup on write, hash re-verification on
  every read (corruption is detected, never trusted blindly).
- **Manifests v1**: immutable JSON, UUIDv7 identity, ordered chunk list,
  object SHA-256, MD5-based single-part ETag, sorted metadata, referenced
  from the journal by both UUID and the SHA-256 of the manifest's exact
  bytes.
- **Journal v1**: append-only binary log (`ZSJ1` magic, CRC32C-checksummed
  frames), replay-based namespace reconstruction, torn-tail tolerance
  limited strictly to a byte-count-incomplete final frame, hard failure on
  any other corruption (bad magic/version/type, CRC mismatch, sequence
  gap/duplicate).
- **HTTP layer**: a custom root `http.Handler` (no `http.ServeMux`) that
  reads `RequestURI`/raw query directly and keeps them intact through
  authentication, so S3 path-normalization traps (`//`, `%2F`, `+` vs
  `%20`, trailing slashes) can't be silently rewritten before signing is
  checked.
- **SigV4**: Authorization-header AWS4-HMAC-SHA256, built from the raw
  request-target rather than Go's parsed `r.URL`. Literal
  `X-Amz-Content-Sha256` payload hashes only (no `UNSIGNED-PAYLOAD`, no
  presigned URLs, no aws-chunked/trailer mode).
- **CRC32**: ordinary `x-amz-checksum-crc32` header validation against the
  logical request body, checked before any chunking/storage work begins.
- **S3 operations**: path-style `CreateBucket` (idempotent), `PutObject`
  (arbitrary binary body, zero-byte objects, `Content-Type`,
  `x-amz-meta-*`, overwrite-same-key), `GetObject` (exact byte
  reconstruction, ETag, Content-Type). S3-shaped XML error responses for
  the missing-bucket/key and auth-failure cases M1 needs.
- **Commit pipeline**: CDC → durable CAS chunk publication → durable
  manifest publication → journal append+sync → in-memory namespace apply
  → HTTP success. A PUT is acknowledged only after the journal frame is
  fsynced.

## Tests

`go test ./...` and `go test -race ./...` both pass (83 test cases across
top-level tests and subtests, 0 failures). Suites, matching the M1 test
plan:

- Store/format: init, `FORMAT.json` contents, unsupported-version
  rejection, stable v1 constants.
- CDC: determinism, empty/tiny input, min/max bounds, forced-max
  occurrence over a large corpus, exact boundary-size inputs, repetitive
  (all-zero) data, plausible mean chunk size (~56-72KiB range), and an
  edit-locality test showing a single inserted byte perturbs ~2 CDC
  chunks versus ~65 chunks under fixed 64KiB slicing.
- CAS: path-matches-digest, round trip, dedup (no rewrite on second
  write), corrupted on-disk chunk detected on read.
- Manifest: round trip, deterministic metadata ordering, manifest-bytes
  SHA-256 matches the journal's reference, corrupt/missing manifest
  detected.
- Journal: frame encode/decode round trip, CRC32C detection, sequence
  enforcement (gap/duplicate/non-1-start), payload length bound,
  create-bucket replay, put-object-root replay, multi-frame replay
  (including overwrite), incomplete-final-frame torn-tail tolerance,
  corruption in an earlier complete frame fails loudly, invalid frame
  version/type rejected.
- SigV4: canonical URI/query unit tests (including `//`, `%2F`, `%20`,
  `+`, trailing slash, empty/repeated query params) with an independent
  cross-check implementation; a self-contained test-only signer (not
  calling into the server's own SigV4 code) exercises valid signed
  requests across those same raw-path/query shapes, and rejects wrong
  secret, wrong access key, wrong region/service/scope, a missing
  required signed header, an altered payload, and a signature computed
  over a "cleaned" path.
- CRC32: valid accepted, invalid rejected, failed checksum leaves no
  visible object (via a real HTTP round trip).
- End-to-end (real HTTP handler + server): full lifecycle
  (CreateBucket → PutObject → GetObject → restart → GetObject again),
  zero-byte object, 3MiB binary object, overwrite-then-restart keeps the
  latest version, failed PUT (missing bucket) never becomes visible.
- Crash/recovery: deterministic failure injection (a package-private test
  hook, nil in all non-test code paths) simulates a process death — via
  `panic`/`recover`, then a fresh `OpenStore` on the same directory — at
  each of the seven durability boundaries the M1 spec calls out: during
  chunk staging, after chunks published, after manifest published, during
  a torn journal write (via direct on-disk truncation of a real frame),
  after journal write but sync fails (verifies the append cursor never
  advances on a failed sync, so a retry can't skip a sequence number or
  believe an unsynced frame is durable), after journal sync (object
  correctly **is** visible after restart — the sync is the true commit
  point), and after the client has been ack'd (object remains visible
  through a subsequent, unrelated panic).

## Storage format frozen (v1)

- `store_format_version = 1`, `cdc_format_version = 1`,
  `manifest_format_version = 1`, `hash_algorithm = "sha256"`.
- CDC v1 parameters: min 16384 / target 65536 / max 262144 bytes;
  mask below target = `0xFFFF` (16 bits, ~1/65536); mask at/above target =
  `0x7FFF` (15 bits, ~1/32768); gear table = SHA-256(`"ZeroS3/CDCv1/GearTable/<i>"`)
  first 8 bytes, big-endian, for `i` in `0..255`.
- Journal v1 frame: `4B magic "ZSJ1" | 2B version BE | 1B type | 1B flags(0) | 8B seq BE | 4B payload-len BE | N bytes JSON | 4B CRC32C (Castagnoli) over everything before it`.
  Max payload 8MiB. Record types: `1 = CreateBucket`, `2 = PutObjectRoot`.
  `3` and `4` are reserved (documented as constants) for a future
  DeleteObjectRoot/DeleteBucket and are **not implemented**.
- Manifest v1: JSON with `manifest_format_version`, `cdc_format_version`,
  `hash_algorithm`, `manifest_uuid`, `total_length`, `chunks[]{sha256,length}`,
  `object_sha256`, `etag`, `content_type`, `metadata[]{key,value}` (sorted
  by key), `created_at`, `version_id`.

### Deliberate deviations from the prompt

- **UUIDs**: the prompt asks to "use the Go 1.27 standard-library UUID
  API." No such package exists in Go 1.27's standard library (verified
  against the installed `go1.27.0` source tree). `newUUIDv7` implements
  UUIDv7 directly with `crypto/rand` + `time`, using only the standard
  library, which is the closest faithful compliance available.
- Bucket names are treated as idempotent on `CreateBucket` (re-creating an
  existing bucket succeeds rather than erroring), which keeps the M1
  surface small; this wasn't specified either way.
- A single static SigV4 credential pair is hardcoded as the default
  (`defaultAccessKeyID`/`defaultSecretAccessKey`); there is no credential
  management story in M1, matching scope.

## Known issues

- The entire request body is buffered in memory (bounded by
  `maxRequestBodySize`, 256MiB) rather than truly streamed end-to-end
  through HTTP, because SigV4 payload-hash verification and CRC32
  validation both need the complete body before CDC chunking begins. The
  CDC chunker itself is a genuine streaming `io.Reader` consumer. Fine for
  M1's scope; true request streaming would be later work.
- No automatic fsync of the store root directory beyond what's needed at
  FORMAT.json creation time; this is sufficient for the invariants M1
  requires but hasn't been stress-tested against real power-loss/`kill
  -9` scenarios, only deterministic in-process failure injection and
  direct on-disk truncation (as the prompt itself prefers for this
  unattended phase).
- `TestCrash_AfterJournalWriteBeforeSync` tests the real, provable
  invariant at that boundary (a failed sync must not advance the journal
  cursor) rather than "the write vanishes if the process dies before
  sync," which cannot be demonstrated in-process: once `WriteAt` returns,
  the bytes are visible to any reader sharing the OS page cache regardless
  of whether `Sync()` has run, so that specific claim is only meaningful
  against real power loss.

## M2 NOT STARTED

Confirmed: no work was begun on ListObjectsV2, ListBuckets, HeadBucket,
HeadObject, DeleteBucket, DeleteObject, CopyObject, Range GET, presigned
URLs, multipart upload, versioning, GC, stats, a verify command, rclone
integration, an AWS SDK Go v2 test harness, s3rver/Package Killer
integration, Windows/macOS CI, sync/delta-transfer features, or
benchmarks/polish beyond what M1 needed. Journal record types 3 and 4 are
reserved as constants only, with no behavior attached.
