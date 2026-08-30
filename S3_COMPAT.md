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
| `CopyObject` | `PUT /bucket/key` + `x-amz-copy-source` | `COPY`/`REPLACE` metadata directives, same/cross-bucket, zero new CAS payload bytes; works identically for a completed multipart object |
| single-range `GetObject` | `GET` + `Range: bytes=...` | `start-end`, `start-`, `-suffix`; 416 with `Content-Range: bytes */<size>` for an unsatisfiable range; works across a completed multipart object's part boundaries |
| `CreateMultipartUpload` | `POST /bucket/key?uploads` | persistent upload session, journal-backed |
| `UploadPart` | `PUT /bucket/key?partNumber=N&uploadId=ID` | CDC-chunked into the ordinary CAS; replacing a part number overwrites it |
| `ListParts` | `GET /bucket/key?uploadId=ID` | paginated: `part-number-marker`/`max-parts` (default/clamped to 1000), `IsTruncated`/`NextPartNumberMarker`, stable ascending part-number order, replaced parts never duplicate |
| `CompleteMultipartUpload` | `POST /bucket/key?uploadId=ID` + XML body | validates strict ascending part order, ETags, ≥5MiB non-final parts; re-chunks the true logical concatenation via a fresh CDC pass (never treats a part boundary as a chunk boundary); publishes an ordinary object |
| `AbortMultipartUpload` | `DELETE /bucket/key?uploadId=ID` | not idempotent — a repeat abort 404s, matching real S3 |
| `ListMultipartUploads` | `GET /bucket?uploads` | paginated: `key-marker`/`upload-id-marker`/`max-uploads` (default/clamped to 1000), `IsTruncated`/`NextKeyMarker`/`NextUploadIdMarker`, ordered by key then upload ID (upload IDs are UUIDv7, so this reproduces real S3's own "same key, ascending initiation time" order); `upload-id-marker` is ignored unless `key-marker` is also given, matching real S3 |
| ordinary request checksum | `x-amz-checksum-crc32` header | validated over the logical request payload before any chunking begins |
| `Content-MD5` | `Content-MD5` header | validated over the logical request payload; malformed digest input (bad base64, wrong decoded length) reported as `InvalidDigest`, a well-formed digest that doesn't match reported as `BadDigest` |
| SigV4 auth (header) | `Authorization` header, `AWS4-HMAC-SHA256` | raw request-target signing (no `ServeMux` path cleaning before verification); `X-Amz-Content-Sha256` supports both the fixed SHA-256 digest mode (including the empty-body case) and the fixed `UNSIGNED-PAYLOAD` sentinel — see "SigV4 payload modes" below |
| SigV4 auth (query / presigned URLs) | `X-Amz-Algorithm`/`X-Amz-Credential`/`X-Amz-Date`/`X-Amz-Expires`/`X-Amz-SignedHeaders`/`X-Amz-Signature` query parameters | GET and PUT; shares the same canonicalization/signing core as header auth (`sigv4VerifyCore`); fixed `UNSIGNED-PAYLOAD` payload hash, `host` is the only signed header a generated URL uses; expiry bounded to 1..604800s |
| `zeros3 presign get\|put` CLI | stdlib `flag`-based subcommand | generates a query-auth URL using the exact same signing primitives the server verifies with; never echoes the secret key |
| virtual-hosted-style addressing | `http://bucket.<vhost-base>[:port]/key` | opt-in via `zeros3 serve -vhost-base <domain>`; bucket/key resolution happens strictly after SigV4 verification, from the unmodified `Host` header, so it never changes what was signed; path-style remains available unconditionally, on the same server, even when a vhost base is configured |
| metadata round-trip | `Content-Type` + `x-amz-meta-*` | preserved on every operation that carries them |

`zeros3 stats` (human/`-json`) and `zeros3 verify` (structural, `-deep` for
full content + whole-object digest re-hashing) round out the CLI; they are
ZeroS3-only tooling, not part of the S3 wire protocol.

**M5-C — internal object version history, restore, and safe GC** (ZeroS3-
only CLI/library surface, not part of the S3 wire protocol and not the AWS
S3 Versioning API — see the "Full AWS versioning" non-goal below): every
overwrite (ordinary `PutObject`, `CopyObject`, or a completed multipart
upload) and every `DeleteObject` of an existing object archives the state
it replaces into per-key history, retained indefinitely, with a stable
UUIDv7 version identity per archived state. `zeros3 versions -bucket B
-key K [-json]` lists a key's current root plus its history, oldest-first.
`zeros3 restore -bucket B -key K -version ID` makes that version the new
current object state, zero-copy (reuses the exact existing manifest) and
non-destructive (creates a new current state; never rewinds or removes
existing history). `zeros3 gc -store DIR [-apply] [-json]` is dry-run by
default; `-apply` requires exclusive ownership of the store (a
`syscall.Flock`-based lock `zeros3 serve` also holds, shared, for its
whole run) and refuses outright if the authoritative live root set
(current objects + retained historical versions + active multipart
uploads, one shared reachability scan — see `STORAGE_MODEL.md`) is not
fully valid. `zeros3 doctor -store DIR [-deep] [-json]` is a read-only
lifecycle diagnostic built directly on the `verify` engine, reporting live
root counts by category alongside the existing integrity/reclaimable
accounting. Ordinary S3 clients never see any of this: `ListObjectsV2`
only ever lists current objects, exactly as before this pass.

## ZeroS3 extensions (not S3 APIs)

`zeros3 sync` (M6, optional delta transfer) is a **ZeroS3-specific
extension**, not part of the S3 wire protocol and not usable by an
ordinary S3 SDK. It lives entirely under the reserved `/_zeros3/v1/...`
path namespace — never a real S3 operation name, method, or path shape —
so it can never be confused with, or accidentally shadow, an actual S3
request:

| Endpoint | Method | Purpose |
|---|---|---|
| `/_zeros3/v1/info` | `GET` | capability discovery (`protocol`/`cdc`/`hash`/`delta_sync`/batch and size limits, JSON) |
| `/_zeros3/v1/object?bucket=&key=` | `GET` | object chunk descriptor (M8A — ordered chunks/size/ETag/content-type/metadata, JSON) |
| `/_zeros3/v1/negotiate` | `POST` | bounded missing-chunk query (JSON in/out) |
| `/_zeros3/v1/chunks/<sha256-hex>` | `GET` | chunk download (M8A — raw bytes; also M8B `repair`'s only network call) |
| `/_zeros3/v1/chunks/<sha256-hex>` | `PUT` | idempotent chunk upload (raw bytes) |
| `/_zeros3/v1/commit` | `POST` | atomic ordinary object commit (JSON in/out) |

All four are authenticated by the exact same SigV4 header verification
every ordinary S3 request goes through, and all four render JSON, never
XML — a deliberate visual signal, on the wire, that these are not S3
responses. A successful `/commit` produces an ordinary object reachable
through every operation in the table above; the extension endpoints
themselves are never involved in reading it back. `zeros3 sync` against a
non-ZeroS3 endpoint (one that doesn't answer `/_zeros3/v1/info`
successfully) falls back to an ordinary `PutObject` instead of sending
any of the other three. See `STATUS.md`'s "M6" section for the full
protocol/conflict/resume semantics and `README.md` for CLI usage.

**Recursive directory sync (`zeros3 sync LOCAL_DIRECTORY s3://bucket/prefix/`,
M6C) is a client-side feature of this same ZeroS3-specific extension, not
a new AWS S3 API operation and not a new wire protocol.** It sends zero
new endpoints or request shapes: for every eligible local file it derives
a destination key and calls the exact same single-file `zeros3 sync`
client pipeline described above (capability discovery, `/negotiate`,
chunk upload, `/commit`), one file at a time. An ordinary S3 SDK talking
to a ZeroS3 server never observes anything about how a given object was
produced — a directory-synced object is, on the wire and on disk, the
same ordinary object a single-file sync or a plain `PutObject` would have
produced. Directory sync is non-destructive (it only uploads new/changed
local files; a remote object with no corresponding local file is left
untouched — there is no delete mode) and every file still goes through
M6B's own safe-mode conflict precondition, so one file's remote conflict
can never silently overwrite or corrupt another. See `STATUS.md`'s "M6C"
section for full semantics and `README.md` for CLI usage.

**Remote-to-remote delta replication (`zeros3 replicate SOURCE DESTINATION
--from SRC_ENDPOINT --to DST_ENDPOINT`, M8A) is proprietary ZeroS3
functionality layered on this same extension, not a generic AWS
S3-to-S3 replication feature and not part of the S3 wire protocol.**
It replicates one existing object from a source ZeroS3 server to a
destination ZeroS3 server, transferring only the chunks the destination
doesn't already have — the two new endpoints in the table above
(`GET /object`, `GET /chunks/<sha256-hex>`) exist solely to make a
source's chunk list and payload bytes reachable to this client; every
other step (capability discovery, negotiate, chunk upload, commit) reuses
the exact same M6 endpoints and client primitives unmodified. The
architecture is a **client-orchestrated relay**: the `zeros3` CLI process
talks independently to both servers and relays only the missing bytes
between them in memory — neither server ever makes an outbound request of
its own, stores the other's credentials, or learns the other exists.
This is a deliberate choice to avoid introducing any server-side SSRF
surface. A successful replication produces an ordinary destination
object, indistinguishable from one written by `PutObject`, `CopyObject`,
or `zeros3 sync` — no new persistent format, no "replica object" concept.
`replicate` requires both endpoints to be ZeroS3 servers that pass
capability discovery; there is no generic-S3-source or generic-S3-
destination fallback (unlike `zeros3 sync`'s plain-`PutObject` fallback
for a non-ZeroS3 destination). See `STATUS.md`'s "M8A" section for the
full protocol/consistency/conflict/resume semantics and `README.md` for
CLI usage.

**Peer-assisted corruption repair (`zeros3 repair -store DIR -from
PEER_ENDPOINT`, M8B) is a ZeroS3-specific maintenance extension, not an S3
API and not a new wire protocol.** It sends zero new endpoints: repair's
only network call is an authenticated GET against the exact same
`GET /chunks/<sha256-hex>` endpoint `replicate` (M8A) already uses,
addressed only by content digest. Detection reuses the store's existing
deep-verify reachability scan (`Store.Verify`'s own machinery) rather than
a second integrity checker, so repair can only ever act on digests that
scan already treats as live/reachable — unreachable or orphaned corruption
is never fetched over the network by this command; that remains `gc`'s
job. Every peer-supplied chunk is independently re-hashed against the
exact digest requested before it is ever written to local storage — the
peer is trusted as a source of candidate bytes, never for integrity, so a
wrong, truncated, or oversized response is rejected outright. Repair never
publishes a manifest, journal record, or namespace change: it only ever
replaces one CAS chunk file's bytes with independently-verified bytes for
a digest an already-published, already-authoritative manifest already
claims, so a repaired object is indistinguishable, to GET/HEAD/
ListObjectsV2/versions/`verify -deep`/GC, from one that was never
corrupted. Like `replicate`, this is a client-orchestrated operation with
no server-to-server protocol: the peer never learns anything beyond
answering an ordinary authenticated chunk-fetch request it would already
answer for `replicate`. See `STATUS.md`'s "M8B" section for the full
detection/fetch/publication/partial-repair/resume/concurrency semantics
and `README.md` for CLI usage.

**Prefix/bucket delta replication (`zeros3 replicate -recursive SOURCE
DEST --from SRC_ENDPOINT --to DST_ENDPOINT`, M8C) is proprietary ZeroS3
functionality layered on `replicate`, not a generic AWS S3-to-S3
replication feature and not part of the S3 wire protocol.** It sends
**zero** new endpoints beyond M8A's own two: enumeration uses ordinary,
already-existing `ListObjectsV2` (the standard S3 listing operation
itself, not a proprietary namespace-index endpoint) against the source,
and every selected object is replicated through the *exact same*
single-object `replicate` pipeline (M8A) unmodified — capability
discovery, object descriptor, chunk fetch, negotiate, chunk upload,
commit, conflict precondition, all reused verbatim, once per object. The
only new client-side logic is enumeration/pagination, source-to-
destination key mapping, and result aggregation — no second replication
protocol. `-recursive` is the sole switch between "one object" and "a
prefix/bucket" CLI forms; it is never guessed from a trailing slash
(a legal S3 object key may itself end in `/`). Namespace replication is
one-way and non-destructive: it never deletes, mirrors, or touches a
destination-only object, and it is not atomic across objects — one
object's conflict or corrupt/unavailable source chunk fails only that
object, and the command reports the exact failed set with a non-zero
exit. Each replicated object is committed through the exact same
persistent-format-unchanged path M8A's own `replicate` already uses, so
it is indistinguishable, to GET/HEAD/ListObjectsV2/versions/`verify
-deep`/GC/M8B `repair`, from one produced by ordinary `PutObject` or
single-object `replicate`. Only the current object per key is replicated
(no historical-version replication); no in-progress multipart upload
session is migrated. See `STATUS.md`'s "M8C" section for the full
mapping/enumeration/partial-failure/resume/statistics semantics and
`README.md` for CLI usage.

## Deliberately unsupported (explicit non-goals)

These are not planned for this project, at any tier:

- IAM/STS/KMS/ACL/bucket-policy engine.
- Bucket/object encryption, storage classes, object-lock/retention.
- CORS, static website hosting, event notifications.
- SigV4a (multi-region signing), including the
  `STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD[-TRAILER]` payload modes.
- `STREAMING-UNSIGNED-PAYLOAD-TRAILER` (unsigned streaming request bodies
  with a trailer-based checksum) — recognized and rejected cleanly
  (`NotImplemented`), never implemented.
- Full AWS versioning: the AWS S3 Versioning API specifically —
  `versionId=` query parameters, bucket-versioning configuration state,
  delete markers, per-version GET/HEAD/DELETE, S3 version-listing APIs.
  ZeroS3's own internal, non-AWS-API object version history/restore
  (`zeros3 versions`/`restore`, M5-C, see "Implemented and tested" above)
  is a different mechanism and is implemented.
- Multi-writer/distributed/HA operation (single writer process per store).
- FUSE, dashboards, Kubernetes/Lambda integration.

## Optional / later-tier behavior (not started, by design)

Planned in the project's tiering (`MILESTONES.md`/`RUBRIC_STRATEGY.md`) but
not begun in this pass, and not claimed as shipped:

- `STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` (`aws-chunked` streaming
  request bodies with per-chunk SigV4 signatures) — eligible but
  conditional; not implemented, since neither the pinned AWS SDK for Go v2
  nor rclone required either mode to complete a full multipart workflow
  (see `STATUS.md`'s M5-B "Phase K"). `STREAMING-UNSIGNED-PAYLOAD-TRAILER`
  and SigV4A/ECDSA streaming payload modes are permanently out of scope
  (see "Deliberately unsupported" below), not merely deferred.
- Online/background/scheduled GC and automatic version expiry/retention
  policies — internal versions/restore and offline exclusive GC shipped
  in M5-C; these specific extensions remain out of scope by design, not
  merely unstarted. (Peer-assisted physical-chunk repair beyond explicit
  `restore` shipped in M8B, see above — an *autonomous, continuously
  running* healing daemon with automatic peer discovery remains an
  explicit non-goal, not merely unstarted.)

Delta sync (M6A/M6B), recursive directory sync (M6C), remote-to-remote
delta replication (M8A), peer-assisted corruption repair (M8B), and
prefix/bucket delta replication (M8C) all shipped — see "ZeroS3
extensions (not S3 APIs)" above.

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
- **`ListMultipartUploads` has no `delimiter`/`prefix`/`CommonPrefixes`
  support.** Only the pagination parameters (`key-marker`, `upload-id-
  marker`, `max-uploads`) are implemented; `delimiter` and `prefix` (and
  the resulting `CommonPrefixes` grouping) are out of scope for this pass,
  matching `ListObjectsV2`'s own lack of `encoding-type=url` above.
- **`NextPartNumberMarker`/`NextKeyMarker`/`NextUploadIdMarker` are always
  rendered, including when not truncated** (0 / empty, matching AWS's own
  documented example response for `ListMultipartUploads`, which shows
  present-but-empty marker elements even when `IsTruncated` is `false`);
  every AWS SDK drives pagination off `IsTruncated`, not off whether a
  `Next*` field is present, so this is compatible in practice even though
  it was not independently verified for the non-truncated `ListParts` case.
- **`AbortMultipartUpload` is not idempotent.** A second abort of an
  already-aborted (or already-completed) upload ID reports `NoSuchUpload`,
  matching real S3's own behavior here — unlike `DeleteObject`/
  `CreateBucket`, which ZeroS3 does treat as idempotent.
- **Multipart part-size minimum is fixed at 5MiB** (every part except the
  last), matching AWS's own documented rule, with no configurable
  override.

## SigV4 payload modes (header-auth `X-Amz-Content-Sha256`)

One explicit interpretation layer (`classifySigV4Payload`) covers every
value this header can carry:

| Mode | Value | Behavior |
|---|---|---|
| Fixed SHA-256 | lowercase or uppercase 64-hex digest | signed; the exact digest must match the actual body received (`XAmzContentSHA256Mismatch` on tamper). Covers both an ordinary body and a zero-length body (the SHA-256 of the empty string) — the empty-body case is this same mode, not a separate one. |
| Fixed unsigned | the literal string `UNSIGNED-PAYLOAD` | signed (the literal string itself is part of the canonical request), but SigV4 places no constraint on the body — `Content-MD5`/CRC32 remain independently enforced if the client sends them. |
| Streaming HMAC (conditional) | `STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` | recognized, not implemented — rejected `NotImplemented`. Eligible for a future pass if a real client is shown to require it (see `STATUS.md`'s M5-B "Phase K"); not required by the AWS SDK for Go v2 or rclone. |
| Excluded | `STREAMING-UNSIGNED-PAYLOAD-TRAILER`, `STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD[-TRAILER]` | recognized, permanently unsupported — rejected `NotImplemented`. |
| Anything else | any other string | rejected `AccessDenied` (not a valid digest and not a recognized sentinel — including a lowercase/misspelled sentinel variant, which is never silently accepted under some other mode). |

Query-string (presigned URL) auth is unaffected by any of this: it always
uses the fixed `UNSIGNED-PAYLOAD` sentinel unconditionally, via a
completely separate code path (`authenticateQuery`) that never calls
`classifySigV4Payload`.

## Multipart upload ETag semantics

A completed multipart object's ETag is **not** the ordinary single-PUT
rule (plain MD5 of the object body). It follows the conventional S3
multipart formula: `MD5(binary_MD5(part1) || binary_MD5(part2) || ...) +
"-" + part_count`, computed over the parts exactly as named in the
`CompleteMultipartUpload` request. The two rules are kept strictly
separate in the implementation and are proven to actually differ for the
same underlying bytes (see `STATUS.md`'s M5-B section).

## Hash/checksum taxonomy (kept deliberately distinct)

| Concept | Algorithm | Purpose | Where |
|---|---|---|---|
| CAS chunk identity | SHA-256 | content-addressed dedup/integrity namespace | every chunk file name |
| object-level digest | SHA-256 | whole-object integrity, checked by `verify -deep` | manifest `object_sha256` |
| S3 ETag (single-part) | MD5 of the object body | S3 compatibility/cache-condition contract | manifest `etag`, `ETag` header |
| S3 ETag (multipart) | MD5 of concatenated per-part MD5s, `-N` suffix | S3 compatibility contract, genuinely different formula from single-part | manifest `etag` for a completed multipart object, `ETag` header |
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
later-tier behavior" above. The same SDK's `UploadPart`/
`CreateMultipartUpload`/`CompleteMultipartUpload` calls likewise send an
ordinary fixed `x-amz-content-sha256` digest, never a streaming payload
mode — confirmed directly for a real multipart workflow in M5-B (see
`zeros3-testing/results/M5B_MULTIPART_RESULTS.md`), which is why
`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` remains unimplemented (see
"SigV4 payload modes" above): a real, unmodified SDK simply never asks for
it. rclone's own multipart uploads use `UNSIGNED-PAYLOAD` for the same
reason its ordinary single-part uploads do (a non-seekable
progress-accounting body reader) — see
`zeros3-testing/results/M5B_RCLONE_LARGE_OBJECT_RESULTS.md` for a genuine
1 GiB/205-part proof.

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
