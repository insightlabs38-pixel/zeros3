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
without special protocol hacks**: path-style (and, opt-in, virtual-hosted-
style) addressing, Authorization-header and query-string (presigned URL)
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
| SigV4 auth (header) | `Authorization` header, `AWS4-HMAC-SHA256` | raw request-target signing (no `ServeMux` path cleaning before verification) |
| SigV4 auth (query / presigned URLs) | `X-Amz-Algorithm`/`X-Amz-Credential`/`X-Amz-Date`/`X-Amz-Expires`/`X-Amz-SignedHeaders`/`X-Amz-Signature` query parameters | GET and PUT; shares the same canonicalization/signing core as header auth (`sigv4VerifyCore`); fixed `UNSIGNED-PAYLOAD` payload hash, `host` is the only signed header a generated URL uses; expiry bounded to 1..604800s |
| `zeros3 presign get\|put` CLI | stdlib `flag`-based subcommand | generates a query-auth URL using the exact same signing primitives the server verifies with; never echoes the secret key |
| virtual-hosted-style addressing | `http://bucket.<vhost-base>[:port]/key` | opt-in via `zeros3 serve -vhost-base <domain>`; bucket/key resolution happens strictly after SigV4 verification, from the unmodified `Host` header, so it never changes what was signed; path-style remains available unconditionally, on the same server, even when a vhost base is configured |
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

- Internal object versions/restore — T2.
- Garbage collection of unreachable chunks/manifests — T2.
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
  session tokens are not accepted — this applies identically to header auth
  and to presigned URLs: `X-Amz-Security-Token` is explicitly rejected
  (`AuthorizationQueryParametersError`) rather than silently ignored.
- **Presigned URLs sign only the `host` header.** Real S3 presigned URLs may
  sign additional headers; ZeroS3's `zeros3 presign` CLI and verifier only
  require/use `host`, matching the AWS SDK for Go v2 presigner's own
  default and the scope of this pass.
- **`X-Amz-Expires` is bounded to 1..604800 seconds** (AWS's own documented
  maximum), with no configurable override.
- **Virtual-host addressing is opt-in and single-domain.** One configured
  base domain (`-vhost-base`) maps `bucket.<base>` to a bucket; there is no
  wildcard-TLS or multi-domain routing, and this is request-addressing
  support only (no DNS automation).

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

Path-style is always available: `http://host:port/bucket/key`. The HTTP
handler is a plain `http.Handler`, deliberately not built on
`http.ServeMux` — `ServeMux` cleans `//`, resolves `.`/`..`, and would
silently rewrite the request target that SigV4 signs. `RequestURI`/
`RawQuery` are read directly and kept intact through authentication; only
after auth succeeds is the path percent-decoded into a semantic
bucket/key. Adversarial raw-path cases (`//`, `%2F`, `%25`, `+` vs `%20`,
Unicode, trailing slash) are covered by the SigV4 test suite.

Virtual-hosted-style addressing (`http://bucket.<vhost-base>[:port]/key`)
is available when the server is started with `-vhost-base <domain>`
(default: unset, path-style only). Bucket extraction from `Host` happens
strictly *after* SigV4 verification succeeds, reading the exact, unmodified
`Host` header value that was itself part of the signed canonical request —
it is never rewritten, normalized, or consulted before authentication, so
enabling virtual-host routing cannot change what a request's signature
covers. A `Host` without the configured suffix (a bare IP, `localhost`, an
unrelated domain, or the bare base domain with no bucket label) falls back
to ordinary path-style parsing unconditionally, on the same server.

## Presigned URLs (SigV4 query-string authentication)

Header auth and query auth are two ways to *locate* a signature (an
`Authorization` header vs. `X-Amz-*` query parameters) and two different
payload/expiry policies, but they share one signature verifier
(`sigv4VerifyCore`) — there is exactly one canonicalization/HMAC
implementation in `zeros3.go`, used by both directions (server-side
verification and `zeros3 presign` generation alike).

- **Required parameters:** `X-Amz-Algorithm` (must be `AWS4-HMAC-SHA256`),
  `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires`, `X-Amz-SignedHeaders`
  (must include `host`), `X-Amz-Signature`. Each must appear exactly once;
  a duplicate is rejected, not resolved by picking one value.
- **`X-Amz-Security-Token` is explicitly rejected**, not silently ignored —
  ZeroS3 has no session/STS credential model to validate it against.
- **Payload hash is always the fixed `UNSIGNED-PAYLOAD` sentinel** for
  query auth, matching real S3 presigned URLs and the AWS SDK for Go v2
  presigner's own default; this does not change header auth's exact-body-
  hash requirement, which is untouched.
- **Expiry:** `X-Amz-Expires` must be an integer in `1..604800` seconds; a
  request is accepted through and including its exact expiry instant, and
  rejected starting the next second. `X-Amz-Date` more than 15 minutes in
  the future is rejected regardless of `X-Amz-Expires` (the same skew
  window header auth uses).
- **Canonical query construction excludes only `X-Amz-Signature` itself**
  — every other query parameter, signed-header-related or not, is part of
  the canonical query and therefore part of what's signed; adding, removing,
  or changing any query parameter after generation invalidates the
  signature.
- **Mutation safety:** every adversarial presigned-PUT case (tampered path/
  bucket/signature/Host, expired URL, wrong credential scope) is proven,
  both in `zeros3_test.go` and in the external `zeros3-testing` AWS SDK
  harness, to leave no visible object and no namespace mutation.

## Known gaps intentionally left for a later milestone

See `STATUS.md`'s "Known limitations" sections for the complete, current
list; the compatibility-relevant subset is repeated above. This document is
updated whenever a listed behavior actually ships — features move from
"optional / later-tier" to "implemented and tested" only alongside their
tests and (where applicable) external interoperability evidence, never
ahead of it.
