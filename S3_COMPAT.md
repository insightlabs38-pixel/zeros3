# S3 Compatibility

This document states exactly what ZeroS3 does and does not implement of the
S3 HTTP API, reconciled against the current `zeros3.go` implementation and
its test suite (`zeros3_test.go`) plus the external `zeros3-testing`
interoperability harnesses. Nothing below is aspirational: every
"implemented/tested" line has a corresponding unit/integration test and, for
the operations the external harnesses cover, a passing real-SDK/real-client
run recorded in `STATUS.md`.

## Compatibility posture

ZeroS3 targets a **small, explicit S3 subset that ordinary SDKs can use
without special protocol hacks**: path-style addressing, Authorization-header
SigV4, and the operation set below. It is not a goal to reach full AWS S3
parity.

## Implemented and tested

| Operation | Wire form | Notes |
|---|---|---|
| `ListBuckets` | `GET /` | S3-shaped `ListAllMyBucketsResult` XML; buckets sorted by name |
| `CreateBucket` | `PUT /bucket` | idempotent (see "Compatibility deviations" below) |
| `HeadBucket` | `HEAD /bucket` | 200 empty body if visible, 404 empty body if missing |
| `DeleteBucket` | `DELETE /bucket` | empty buckets only; `NoSuchBucket`/`BucketNotEmpty` |
| `PutObject` | `PUT /bucket/key` | arbitrary binary body, 0-byte objects, `Content-Type`, `x-amz-meta-*`, overwrite-same-key |
| `GetObject` | `GET /bucket/key` | exact byte reconstruction, ETag, Content-Type, metadata |
| `HeadObject` | `HEAD /bucket/key` | same headers as GetObject, no body |
| `DeleteObject` | `DELETE /bucket/key` | idempotent non-versioned delete, 204 |
| `ListObjectsV2` | `GET /bucket?list-type=2...` | `prefix`, `delimiter`/`CommonPrefixes`, `max-keys` (default/clamped to 1000), `continuation-token`, UTF-8 byte-lexical key order, XML escaping |
| `CopyObject` | `PUT /bucket/key` + `x-amz-copy-source` | `COPY`/`REPLACE` metadata directives, same/cross-bucket, zero new CAS payload bytes |
| single-range `GetObject` | `GET` + `Range: bytes=...` | `start-end`, `start-`, `-suffix`; 416 with `Content-Range: bytes */<size>` for an unsatisfiable range |
| ordinary request checksum | `x-amz-checksum-crc32` header | validated over the logical request payload before any chunking begins |
| `Content-MD5` | `Content-MD5` header | validated over the logical request payload; malformed digest input (bad base64, wrong decoded length) reported as `InvalidDigest`, a well-formed digest that doesn't match reported as `BadDigest` |
| SigV4 auth | `Authorization` header, `AWS4-HMAC-SHA256` | raw request-target signing (no `ServeMux` path cleaning before verification) |
| metadata round-trip | `Content-Type` + `x-amz-meta-*` | preserved on every operation that carries them |

`zeros3 stats` (human/`-json`) and `zeros3 verify` (structural, `-deep` for
full content + whole-object digest re-hashing) round out the CLI; they are
ZeroS3-only tooling, not part of the S3 wire protocol.

## Deliberately unsupported (explicit non-goals)

These are not planned for this project, at any tier:

- IAM/STS/KMS/ACL/bucket-policy engine.
- Bucket/object encryption, storage classes, object-lock/retention.
- CORS, static website hosting, event notifications.
- SigV4a (multi-region signing).
- Full AWS versioning (delete markers, version-scoped GET/HEAD/DELETE).
- Multi-writer/distributed/HA operation (single writer process per store).
- FUSE, dashboards, Kubernetes/Lambda integration.

## Optional / later-tier behavior (not started, by design)

Planned in the project's tiering (`MILESTONES.md`/`RUBRIC_STRATEGY.md`) but
not begun in this pass, and not claimed as shipped:

- Presigned GET/PUT (SigV4 query-string auth) — T2.
- Internal object versions/restore — T2.
- Garbage collection of unreachable chunks/manifests — T2.
- Virtual-host-style bucket addressing (`bucket.host/key`) — T2; path-style
  is the only supported addressing mode today.
- Multipart upload (initiate/upload-part/complete/abort) — T3.
- `aws-chunked` streaming request bodies and trailer-based checksums — T3;
  no current external harness client requires this mode, so it was never
  promoted (see "Modern SDK checksum behavior" below).
- Benchmark/doctor CLI subcommands — T3.
- Delta/directory sync — T4.

## Compatibility deviations (differs from real AWS S3)

Places where ZeroS3's behavior is intentionally narrower or different from
documented AWS S3 behavior, rather than simply "not implemented":

- **`CreateBucket` is idempotent.** Re-creating an existing bucket succeeds
  (200) instead of AWS's region/ownership-dependent
  `BucketAlreadyExists`/`BucketAlreadyOwnedByYou` errors. This keeps the M1
  surface small; it was not specified as a MUST behavior either way.
- **`CopyObject` has no conditional-copy headers.** `x-amz-copy-source-if-*`
  (match/none-match/modified-since/unmodified-since) are not read or
  enforced.
- **`CopyObject` does not reject same-key `COPY`-directive self-copies.**
  Real S3 rejects certain same-key copies where the metadata directive is
  `COPY` (no metadata change); ZeroS3 always publishes a new manifest/
  version/timestamp for the destination instead.
- **`ListObjectsV2` has no `encoding-type=url`.** XML escaping alone covers
  every tested key shape (including XML-special and Unicode characters);
  clients relying on URL-encoded key parts in the response are unsupported.
- **Legacy `ListObjects` (no `list-type=2`) is explicitly rejected**, not
  silently reinterpreted as V2.
- **Multi-range GET (`bytes=0-1,3-4`) is unsupported.** Per RFC 7233's
  allowance for range forms a server doesn't support, ZeroS3 ignores the
  header and serves the full object with 200, rather than a
  `multipart/byteranges` 206 response.
- **`StorageClass` is a hardcoded `"STANDARD"` literal** in `ListObjectsV2`
  responses — no real storage classes exist in ZeroS3.
- **A single static SigV4 credential pair** is the only supported identity;
  there is no credential/session-token management, so per-request STS
  session tokens are not accepted.
- **Path-style addressing only.** `Host`-derived (virtual-host) bucket
  routing is not implemented.

## Hash/checksum taxonomy (kept deliberately distinct)

| Concept | Algorithm | Purpose | Where |
|---|---|---|---|
| CAS chunk identity | SHA-256 | content-addressed dedup/integrity namespace | every chunk file name |
| object-level digest | SHA-256 | whole-object integrity, checked by `verify -deep` | manifest `object_sha256` |
| S3 ETag | MD5 (single-part only) | S3 compatibility/cache-condition contract | manifest `etag`, `ETag` header |
| SigV4 payload hash | SHA-256 (`x-amz-content-sha256`) | request authentication | Authorization header / signed value |
| `x-amz-checksum-crc32` | CRC32 (IEEE), base64 | client-requested transport integrity | `validateCRC32Header` |
| `Content-MD5` | MD5, base64 | client-requested transport integrity, independent of CRC32 | `validateContentMD5Header` |
| journal frame checksum | CRC32C (Castagnoli) | recovery/torn-frame detection, not authentication | journal frame trailer |

These six concepts never stand in for one another: a chunk's CAS SHA-256 is
not the object's SHA-256, the object SHA-256 is not the ETag, the ETag is
not the SigV4 payload hash, and the request-checksum headers (CRC32/
Content-MD5) are independent, opt-in, per-request checks over the logical
payload — never confused with any of the above.

## Modern SDK checksum behavior

Investigated directly (not assumed) against the pinned AWS SDK for Go v2
(`config v1.33.1`+): an ordinary `PutObject` call with a seekable body sends
a plain `x-amz-checksum-crc32` header — no `aws-chunked` framing, no
streaming trailer. ZeroS3 validates exactly that header form. `Content-MD5`
is validated the same way when a client sends it (rclone's ordinary
single-part upload path does; see `zeros3-testing/results/` for the pinned
version and recorded run). Neither request-integrity check requires or
implies `aws-chunked` support, which stays unimplemented per "Optional /
later-tier behavior" above.

## Path/addressing

Path-style only: `http://host:port/bucket/key`. The HTTP handler is a plain
`http.Handler`, deliberately not built on `http.ServeMux` — `ServeMux`
cleans `//`, resolves `.`/`..`, and would silently rewrite the request
target that SigV4 signs. `RequestURI`/`RawQuery` are read directly and kept
intact through authentication; only after auth succeeds is the path
percent-decoded into a semantic bucket/key. Adversarial raw-path cases
(`//`, `%2F`, `%25`, `+` vs `%20`, Unicode, trailing slash) are covered by
the SigV4 test suite.

## Known gaps intentionally left for a later milestone

See `STATUS.md`'s "Known limitations" sections for the complete, current
list; the compatibility-relevant subset is repeated above. This document is
updated whenever a listed behavior actually ships — features move from
"optional / later-tier" to "implemented and tested" only alongside their
tests and (where applicable) external interoperability evidence, never
ahead of it.
