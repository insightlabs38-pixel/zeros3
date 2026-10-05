# S3 compatibility

This document is the exact ordinary-S3 contract ZeroS3 currently exposes.
It describes shipped behavior only. ZeroS3-native content-transfer and
maintenance extensions are documented separately in
[docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md).

ZeroS3 intentionally implements a focused S3-compatible subset rather than
attempting full AWS S3 parity.

## Compatibility posture

The primary portable contract is **ZeroS3 Core Client Profile v1**.

An application that stays within this profile can use ZeroS3 through ordinary
S3 SDKs without knowing anything about CDC, CAS, packs, tiers, snapshots, or
the ZeroS3-native protocol.

`GET /_zeros3/v1/info` advertises:

```json
{
  "implementation": "zeros3",
  "core_s3_profile": 1
}
```

and `zeros3 probe -endpoint URL` distinguishes ZeroS3 from a generic
S3-compatible endpoint.

## Core Client Profile v1

| Area | Supported contract |
|---|---|
| Buckets | `ListBuckets`, `CreateBucket`, `HeadBucket`, `DeleteBucket`, `GetBucketLocation` |
| Objects | `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`, `DeleteObjects`, `ListObjectsV2`, `CopyObject` |
| Reads | one byte range; `If-Match` / `If-None-Match` |
| Writes | `Content-MD5`, `x-amz-checksum-crc32`, conditional PUT |
| Multipart | create, upload part, list parts, list uploads, complete, abort |
| Auth | SigV4 header auth; SigV4 presigned GET/PUT; signed `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` aws-chunked bodies |
| Addressing | path style always; virtual-hosted style when configured |
| Region | one configured server region |

The profile deliberately excludes IAM/STS, policy/ACL APIs, KMS/SSE,
AWS Versioning, lifecycle, tagging, CORS, notifications, Object Lock,
website hosting, requester-pays, replication configuration, SelectObjectContent,
and other AWS services/features layered around S3.

### Validated clients

The profile is exercised by the portable black-box conformance harness in
`testing-harnesses/profile/conformance`.

Recorded validation includes:

- AWS SDK for Go v2;
- minio-go;
- rclone on its dedicated interoperability path;
- independent golden SigV4, presign, and wire vectors under
  `testing-harnesses/vectors/`.

The same client-visible reads are tested over loose, raw packed, compressed
packed, hot, warm, cold, and mixed physical layouts. Physical representation
is not part of the S3 contract.

## Implemented operations

| Operation | Wire form | Current behavior |
|---|---|---|
| `ListBuckets` | `GET /` | S3-shaped XML; bucket names sorted |
| `CreateBucket` | `PUT /bucket` | idempotent for an already-existing bucket |
| `HeadBucket` | `HEAD /bucket` | 200 if present, 404 if missing |
| `DeleteBucket` | `DELETE /bucket` | empty bucket only |
| `GetBucketLocation` | `GET /bucket?location` | returns the server's configured region; `us-east-1` uses the empty constraint |
| `PutObject` | `PUT /bucket/key` | streaming body, metadata, content type, conditional writes, request checksums |
| `GetObject` | `GET /bucket/key` | streaming verified reads, metadata, single range, read preconditions |
| `HeadObject` | `HEAD /bucket/key` | same object metadata/preconditions, no body |
| `DeleteObject` | `DELETE /bucket/key` | idempotent current-object delete |
| `DeleteObjects` | `POST /bucket?delete` | up to 1000 keys, `Quiet`; per-key semantics, not an all-or-nothing transaction |
| `ListObjectsV2` | `GET /bucket?list-type=2...` | prefix, delimiter/CommonPrefixes, max-keys, continuation token, optional `encoding-type=url` |
| `CopyObject` | `PUT /bucket/key` + `x-amz-copy-source` | COPY/REPLACE metadata, cross/same bucket, source ETag preconditions |
| `CreateMultipartUpload` | `POST /bucket/key?uploads` | persistent journal-backed upload |
| `UploadPart` | `PUT /bucket/key?partNumber=N&uploadId=ID` | replaceable part number, normal content integrity |
| `ListParts` | `GET /bucket/key?uploadId=ID` | paginated part listing |
| `CompleteMultipartUpload` | `POST /bucket/key?uploadId=ID` | validates requested part order/ETags and re-chunks final logical bytes across part seams |
| `AbortMultipartUpload` | `DELETE /bucket/key?uploadId=ID` | aborts the live upload; repeated abort reports `NoSuchUpload` |
| `ListMultipartUploads` | `GET /bucket?uploads` | markers, max-uploads, prefix, delimiter/CommonPrefixes |

Single-request PutObject follows the implementation's 5 GiB ceiling.

### DeleteObjects details

`DeleteObjects` accepts at most 1000 keys and supports `Quiet=true`.

Each key goes through ordinary `DeleteObject` semantics:

- deleting a missing key succeeds;
- deleting an existing current object archives the replaced root into ZeroS3
  internal history;
- no chunk is synchronously removed from the CAS;
- reclaiming retired content remains a separate history-prune/GC/repack
  operation.

The request is not atomic across keys. Per-key failures are returned in the
result.

A non-empty `VersionId` other than `null` is rejected for that entry because
the AWS S3 Versioning API is not implemented.

## Object integrity and ETags

ZeroS3 deliberately keeps several integrity concepts separate.

| Concept | Algorithm | Meaning |
|---|---|---|
| CAS chunk identity | SHA-256 | immutable logical chunk identity and read verification |
| Whole-object digest | SHA-256 | manifest-level end-to-end content digest |
| Single-part ETag | MD5(body) | S3 compatibility/cache validator |
| Multipart ETag | MD5(concatenated binary part-MD5s) + `-N` | conventional multipart ETag |
| SigV4 payload hash | SHA-256 / sentinel | authentication binding |
| `Content-MD5` | MD5 + base64 | optional request-body integrity |
| `x-amz-checksum-crc32` | IEEE CRC32 + base64 | optional request-body integrity |
| Journal/snapshot frame checksum | CRC32C | torn/corrupt metadata-frame detection |

The CAS digest is not an ETag, and the ETag is not used as the storage
integrity identity.

## Conditional operations

### PutObject

Supported write preconditions:

- `If-None-Match: *`
- `If-Match: "<etag>"` (quoted or unquoted accepted by the narrow parser)

The condition is revalidated at ZeroS3's locked namespace-commit boundary,
after payload ingest. Concurrent writers therefore cannot both successfully
commit the same create-only/replace-only condition.

A failed conditional request can leave unreachable immutable CAS content, but
does not publish the object root.

### GetObject / HeadObject

Supported read preconditions:

- `If-Match`
- `If-None-Match`

The implementation intentionally does not implement the full HTTP conditional
request grammar. Comma-separated validator lists and weak validators are not
silently approximated.

### CopyObject

Supported source preconditions:

- `x-amz-copy-source-if-match`
- `x-amz-copy-source-if-none-match`

Date-based CopyObject source preconditions are not implemented.

## Range reads

One byte range is supported:

- `bytes=start-end`
- `bytes=start-`
- `bytes=-suffix`

An unsatisfiable range returns 416 with
`Content-Range: bytes */<object-size>`.

Multi-range requests are not implemented. They are treated as an unsupported
range form rather than producing `multipart/byteranges`.

## ListObjectsV2 key encoding

Without `encoding-type=url`, ListObjectsV2 emits normal XML-escaped values.

With:

```text
encoding-type=url
```

ZeroS3 additionally percent-encodes key-bearing fields bytewise and advertises
`<EncodingType>url</EncodingType>`.

Encoded fields include:

- `Key`
- `Prefix`
- `Delimiter`
- `CommonPrefixes/Prefix`

Encoding uses uppercase `%XX`; space is `%20`; slash is `%2F`; `+` is
never used for space.

Filtering, delimiter grouping, and continuation tokens operate on the original
key bytes, not the rendered encoding.

Any other `encoding-type` value is `InvalidArgument`.

This mode is the portable choice for arbitrary legal key bytes that XML 1.0
cannot represent directly.

## SigV4

ZeroS3 implements AWS Signature Version 4 with one configured static credential
pair and one server region.

Canonicalization is performed from the original request target rather than an
HTTP router-normalized path.

Covered edge cases include:

- repeated slashes;
- encoded slash;
- percent signs;
- plus versus space;
- Unicode;
- query sorting;
- metadata headers;
- virtual-hosted addressing.

### Header authentication payload modes

`X-Amz-Content-Sha256` supports:

| Value | Behavior |
|---|---|
| 64-hex SHA-256 digest | request is signed against that digest and the received logical body must match |
| `UNSIGNED-PAYLOAD` | sentinel is signed; SigV4 itself does not bind the body |
| `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` | signed aws-chunked body with chained per-chunk signatures |

The signed aws-chunked mode requires:

- `Content-Encoding: aws-chunked`;
- `x-amz-decoded-content-length`;
- valid chained signatures;
- a valid final zero-length signed chunk;
- exact decoded length;
- no bytes after the final chunk.

Individual signed chunks are bounded, verified before their bytes are released
to the ingest path, and the decoded payload then uses the same request checksum
and object-ingest machinery as an ordinary body.

Recognized but **not implemented**:

- `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER`;
- `STREAMING-UNSIGNED-PAYLOAD-TRAILER`;
- SigV4A/ECDSA streaming payload modes.

These values are rejected rather than silently interpreted as another mode.

### Presigned URLs

SigV4 query authentication supports presigned GET and PUT.

Required query fields are the usual:

- `X-Amz-Algorithm`
- `X-Amz-Credential`
- `X-Amz-Date`
- `X-Amz-Expires`
- `X-Amz-SignedHeaders`
- `X-Amz-Signature`

`X-Amz-Expires` is limited to 1..604800 seconds.

The built-in `zeros3 presign` command signs `host` and uses
`UNSIGNED-PAYLOAD`, matching the intended portable profile.

Session-token authentication is not implemented. `X-Amz-Security-Token` is
rejected rather than ignored.

## Addressing

### Path style

Always available:

```text
http://host:port/bucket/key
```

### Virtual-hosted style

Optional with:

```sh
zeros3 serve -vhost-base example.internal
```

which allows:

```text
http://bucket.example.internal[:port]/key
```

Bucket extraction happens after successful SigV4 verification from the
unmodified Host value. Requests outside the configured suffix continue through
path-style parsing.

ZeroS3 does not automate DNS or wildcard TLS configuration.

## Multipart semantics

Multipart upload state is persistent in the same visibility journal model as
ordinary namespace state.

Completion:

- requires strict ascending part order in the completion request;
- validates part ETags;
- enforces the 5 MiB minimum for every non-final part;
- replays the selected logical parts through one fresh CDC pass;
- therefore does not preserve part boundaries as content-chunk boundaries.

The completed object's multipart ETag follows:

```text
MD5(binary_MD5(part1) || binary_MD5(part2) || ...) + "-" + part_count
```

and remains distinct from the single-part ETag rule.

## Intentional AWS deviations

ZeroS3 is S3-compatible within the stated profile, not behavior-identical to
AWS S3 in every corner case.

Current intentional deviations include:

- **CreateBucket is idempotent.** Re-creating an existing bucket succeeds.
- **One static identity.** No IAM/STS credential/session model exists.
- **One configured region.** There is no multi-region routing/signing model.
- **No AWS Versioning API.** ZeroS3's retained internal history is a different
  mechanism and is not exposed through `versionId=`, version listing, delete
  markers, or bucket-versioning configuration.
- **CopyObject date preconditions are absent.** ETag source preconditions are
  supported.
- **Same-key COPY self-copy is accepted.** ZeroS3 can publish a new current
  manifest/version rather than reproducing AWS's narrower rejection behavior.
- **Legacy ListObjects is rejected.** Only ListObjectsV2 is in the profile.
- **Multi-range GET is absent.**
- **StorageClass renders as `STANDARD`.** ZeroS3 physical hot/warm/cold tiers
  are internal physical placement and are not S3 StorageClass values.
- **Virtual-hosted style is opt-in for one configured base domain.**
- **ListMultipartUploads does not implement `encoding-type=url`.**
- **AbortMultipartUpload is not idempotent.** A repeated abort reports
  `NoSuchUpload`.
- **Multipart non-final minimum is fixed at 5 MiB.**

## Deliberately unsupported S3 areas

The current implementation does not provide:

- IAM or STS;
- ACLs or bucket policies;
- KMS/SSE or object encryption;
- S3 storage classes;
- Object Lock/legal hold;
- AWS lifecycle configuration;
- bucket/object tagging APIs;
- CORS;
- static website hosting;
- notifications/event configuration;
- AWS replication configuration;
- requester pays;
- S3 Select;
- AWS S3 Versioning;
- SigV4A/multi-region signing.

Absence from this list is not a promise that another AWS S3 operation is
implemented. The positive operation table above is authoritative.

Future enterprise or compatibility work may add capabilities where they fit the
architecture; this document intentionally states current scope rather than
permanently constraining future design.

## ZeroS3-native behavior is separate

Ordinary S3 clients do not need to understand:

- CDC or logical chunk hashes;
- loose versus packed CAS;
- pack compression/locality;
- hot/warm/cold physical placement;
- internal history;
- snapshots and forks;
- full/delta snapshot bundles;
- ZeroS3 delta synchronization;
- peer repair.

Large known-size writes may be stored directly into immutable packs, while
small/unknown-size and some internal transfer paths may initially use loose CAS
chunks. This physical choice is transparent through the S3 contract.

ZeroS3-aware clients can additionally negotiate logical chunks and transfer
only missing content. That protocol is documented in
[docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md).

## Validation evidence

The S3 surface is covered by two complementary layers:

1. `zeros3_test.go`: white-box protocol, storage, crash, corruption, and race
   tests using only the Go standard library.
2. `testing-harnesses/`: black-box real-process validation using independent
   clients, including AWS SDK for Go v2 and minio-go.

Useful focused gates include:

```sh
scripts/validate.sh s3
scripts/validate.sh client
scripts/validate.sh vectors
```

The external harness module is deliberately separate from the dependency-free
root module.

See [testing-harnesses/README.md](./testing-harnesses/README.md) for the current
evidence map.

## Related documentation

- [README.md](./README.md) — overview and quick start
- [STATUS.md](./STATUS.md) — maturity, deployment boundaries, format versions
- [docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md) — content-native protocol
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) — logical and physical storage model
- [docs/OPERATIONS.md](./docs/OPERATIONS.md) — operational procedures
