# ZeroS3 native protocol

ZeroS3 exposes a small content-native protocol under the reserved
`/_zeros3/` namespace in addition to its ordinary S3 surface.

This protocol is separate from S3 and exposes ZeroS3's logical chunk model to
clients that need delta upload, ZeroS3-to-ZeroS3 replication, peer repair,
snapshot orchestration, or structural inspection. Ordinary S3 clients do not
need to implement it.

For ordinary compatibility, see [../S3_COMPAT.md](../S3_COMPAT.md).

## Protocol principles

The native protocol follows five rules:

1. **Logical content only.** Wire identities are SHA-256 plus logical length;
   packs, offsets, codecs, and tiers never appear.
2. **Ordinary object result.** A successful native commit produces the same
   kind of object root/manifests used by normal PutObject.
3. **Explicit discovery.** Clients must discover version/capability support
   before using optional behavior.
4. **Fail closed on version mismatch.** Protocol/CDC/hash identifiers must
   match the supported contract.
5. **Same authentication boundary.** Native endpoints use the same SigV4
   header authentication as the S3 server.

Current identifiers:

```text
logical protocol: 1
CDC:              gear-v1
hash:             sha256
optional bulk:    2
```

## Discovery

```text
GET /_zeros3/v1/info
```

Response JSON currently has this shape:

```json
{
  "protocol": 1,
  "cdc": "gear-v1",
  "hash": "sha256",
  "delta_sync": true,
  "max_hashes_per_batch": 1024,
  "max_batch_bytes": 262144,
  "max_chunk_bytes": 262144,
  "implementation": "zeros3",
  "core_s3_profile": 1,
  "bulk_protocol_version": 2,
  "max_bulk_chunks": 4096,
  "max_bulk_bytes": 67108864
}
```

The bulk fields are additive and optional. A client talking to an older
ZeroS3 server must treat absent bulk fields as "v1 per-chunk transport only."

A generic S3 endpoint is not expected to implement this path.

The built-in:

```sh
zeros3 probe -endpoint URL
```

uses discovery to distinguish ZeroS3 from generic S3.

## Authentication

Every native request passes through ZeroS3's normal SigV4 header verifier, so
there is no separate native-protocol credential format.

A client should sign:

- the exact HTTP method;
- raw path;
- query string;
- host;
- payload hash/sentinel as required by the request.

The current built-in clients reuse the same canonicalization/signing
implementation as the server.

Native JSON errors use:

```json
{
  "code": "ErrorCode",
  "message": "human-readable detail"
}
```

rather than S3 XML errors.

## Chunk descriptor

The common logical descriptor is:

```json
{
  "sha256": "64-lowercase-hex-digits",
  "length": 65536
}
```

Requirements:

- digest decodes to exactly 32 bytes;
- length is 1..262144;
- the digest names the **uncompressed logical bytes**;
- the same digest may not be declared with conflicting lengths.

## Object descriptor

```text
GET /_zeros3/v1/object?bucket=<B>&key=<K>
```

The key travels as a query value rather than a raw path segment.

Response:

```json
{
  "protocol": 1,
  "cdc": "gear-v1",
  "hash": "sha256",
  "bucket": "models",
  "key": "checkpoint.bin",
  "version_id": "<manifest UUID>",
  "size": 134217728,
  "etag": "<etag>",
  "content_type": "application/octet-stream",
  "metadata": {
    "example": "value"
  },
  "chunks": [
    {"sha256": "...", "length": 65536}
  ]
}
```

`chunks` is ordered and includes occurrences. If a logical chunk appears
twice in the object, it appears twice in the descriptor.

`version_id` identifies the immutable manifest revision described by the
response.

## v1 negotiation

```text
POST /_zeros3/v1/negotiate
Content-Type: application/json
```

Request:

```json
{
  "protocol": 1,
  "cdc": "gear-v1",
  "hash": "sha256",
  "chunks": [
    {"sha256": "...", "length": 65536}
  ]
}
```

Limits:

- at most 1024 descriptors per request;
- encoded JSON body bounded to 256 KiB;
- each logical chunk length at most 256 KiB.

Response:

```json
{
  "missing": [
    "<sha256-hex>"
  ]
}
```

`missing` contains normalized, de-duplicated digests not currently available
in the destination CAS, preserving first-seen request order.

Negotiation is read-only and safe to retry.

## v1 chunk fetch

```text
GET /_zeros3/v1/chunks/<sha256-hex>
```

The response body is the raw logical chunk bytes.

A correct client must verify:

```text
SHA-256(response body) == requested digest
```

before trusting or publishing the bytes.

The built-in replication/repair client does this independently of the source.

## v1 chunk upload

```text
PUT /_zeros3/v1/chunks/<sha256-hex>
```

The request body is one raw logical chunk.

The server verifies the body against the path digest before durable
publication.

Successful JSON response:

```json
{
  "sha256": "<normalized digest>",
  "length": 65536
}
```

Uploading an already-present correct logical chunk is idempotent.

Chunk upload alone does **not** make an object visible.

## v1 commit

```text
POST /_zeros3/v1/commit
Content-Type: application/json
```

Request:

```json
{
  "protocol": 1,
  "cdc": "gear-v1",
  "hash": "sha256",
  "bucket": "models",
  "key": "checkpoint.bin",
  "content_type": "application/octet-stream",
  "metadata": {},
  "chunks": [
    {"sha256": "...", "length": 65536}
  ],
  "expect_absent": false,
  "expected_etag": ""
}
```

The chunk list is the complete ordered occurrence list for the object, not the
unique set.

Before committing, the destination validates that required logical chunks are
available and consistent.

The commit builds/publishes an ordinary immutable manifest and current object
root. After success, GET/HEAD/ListObjectsV2/verify see an ordinary S3 object;
there is no persistent "synced object" type.

Response:

```json
{
  "bucket": "models",
  "key": "checkpoint.bin",
  "version_id": "<manifest UUID>",
  "etag": "<etag>",
  "size": 134217728
}
```

### Safe-mode preconditions

The commit request includes two mutually meaningful safety fields:

- `expect_absent`: require no current destination object;
- `expected_etag`: require the destination current ETag observed during
  planning.

Built-in sync/replication uses these to avoid silently overwriting a concurrent
destination change.

The precondition is enforced at the same locked namespace commit boundary used
by ordinary conditional writes.

## Basic client flow

For a local file:

```text
1. GET /_zeros3/v1/info
2. run CDC v1 locally
3. build ordered + unique chunk descriptor sets
4. POST /v1/negotiate in bounded batches
5. PUT only missing chunks
6. POST /v1/commit with the complete ordered chunk list
```

The unique set is an optimization for transfer. The commit always describes the
full logical object sequence.

## Generic-S3 fallback

The built-in `zeros3 sync` can fall back to ordinary PutObject when the target
does not advertise compatible ZeroS3 discovery.

That fallback transfers the whole object and does not use native chunk
negotiation.

A third-party client may adopt the same pattern:

```text
try ZeroS3 discovery
   |
   +-- compatible -> native delta path
   |
   +-- not ZeroS3 -> normal S3 PutObject
```

This is the recommended integration model for a client that should work with
both ZeroS3 and generic S3 endpoints.

## Remote replication

ZeroS3-to-ZeroS3 replication is orchestrated by the client rather than by one
server connecting directly to the other.

The relay process talks independently to source and destination:

```text
source /info + /object
          |
          v
client computes destination missing set
          |
          +--> source chunk fetch
          +--> destination chunk upload
          |
          v
destination /commit
```

Neither server is configured with the other server's credentials, and neither
server needs to make outbound requests.

After commit, the destination is an ordinary S3-visible object.

Recursive namespace replication adds client-side ListObjectsV2 enumeration and
reuses the same per-object pipeline.

## Peer repair

Repair reuses the v1 chunk fetch endpoint.

The local store determines which live logical digest is missing/corrupt and
requests exactly that digest from an explicit peer.

Candidate bytes are locally SHA-256 verified before CAS publication.

Repair changes physical content availability, not namespace roots.

## Optional bulk transport v2

Bulk v2 is an optimization for moving the **same logical chunks** with far fewer
HTTP requests.

Discovery must advertise:

```json
{
  "bulk_protocol_version": 2,
  "max_bulk_chunks": 4096,
  "max_bulk_bytes": 67108864
}
```

Current built-in client planning targets about 8 MiB logical payload per batch,
caps each batch at the advertised/server limits, caps concurrent bulk batches
at 4, and separately bounds in-flight frame memory.

These are implementation policies, not changes to logical object semantics.

### Endpoints

| Endpoint | Method | Body in | Body out |
|---|---|---|---|
| `/_zeros3/v2/negotiate` | POST | descriptor frame | missing-descriptor frame |
| `/_zeros3/v2/chunks/fetch` | POST | descriptor frame | data frame |
| `/_zeros3/v2/chunks/upload` | POST | data frame | JSON acknowledgement |

Objects still commit through:

```text
POST /_zeros3/v1/commit
```

There is no v2 object format.

## Bulk v2 frame format

All integers are big-endian.

Header:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 4 | magic `ZS3B` |
| 4 | 1 | bulk protocol version = 2 |
| 5 | 1 | kind |
| 6 | 2 | reserved = 0 |
| 8 | 4 | record count |
| 12 | 8 | total logical record bytes |

Header length: 20 bytes.

Descriptor:

| Size | Field |
|---:|---|
| 32 | raw SHA-256 digest bytes |
| 4 | logical length |

Descriptor length: 36 bytes.

### Kinds

| Kind | Value | Records |
|---|---:|---|
| negotiate request | 1 | descriptors only |
| missing response | 2 | descriptors only |
| fetch request | 3 | descriptors only |
| data | 4 | descriptor followed immediately by `length` payload bytes |

For descriptor-only kinds:

```text
header
descriptor
descriptor
...
```

For data:

```text
header
descriptor + payload
descriptor + payload
...
```

The header `total` is the exact sum of all declared logical lengths, even for
descriptor-only messages.

### Bounds and validation

Current hard bounds:

```text
records per batch: <= 4096
logical payload:   <= 64 MiB
chunk length:      1..256 KiB
```

Parsers reject:

- bad magic/version/kind;
- nonzero reserved field;
- impossible totals;
- oversized count/bytes;
- invalid lengths;
- duplicate digests where duplicates are not allowed;
- one digest declared with conflicting lengths;
- truncated data;
- digest-mismatched payload;
- trailing bytes.

Negotiate requests may contain duplicate descriptors; the missing response is
de-duplicated.

Data frames are incrementally parsed and each chunk payload is SHA-256 verified
before publication.

## Bulk upload acknowledgement

Successful v2 upload returns JSON:

```json
{
  "chunks": 128,
  "bytes": 8388608
}
```

A later malformed record/checksum failure can leave earlier fully-published
chunks as unreachable/reusable CAS content, just like a failed streamed
PutObject. No object is visible until v1 commit succeeds.

## Snapshot endpoints

Snapshots are ZeroS3-native namespace roots, not S3 APIs.

Current v1 endpoints:

| Endpoint | Method | Purpose |
|---|---|---|
| `/_zeros3/v1/snapshot/create` | POST | capture current bucket/prefix root set |
| `/_zeros3/v1/snapshot/list` | GET | list snapshot summaries |
| `/_zeros3/v1/snapshot/show` | GET | inspect one snapshot |
| `/_zeros3/v1/snapshot/delete` | DELETE | remove one snapshot root |
| `/_zeros3/v1/snapshot/object` | GET | describe one snapshot-captured object for restore |

Snapshot IDs and keys travel in bounded JSON/query values rather than being
concatenated into filesystem paths.

The built-in `snapshot restore` uses the same chunk negotiation/fetch/commit
primitives as replication.

Within one store, the payload is already present, so restore normally transfers
no new CAS payload.

Operational snapshot workflows are documented in
[OPERATIONS.md](./OPERATIONS.md).

## Reachability endpoint

```text
GET /_zeros3/v1/reachability?bucket=<B>&key=<K>
```

This is a read-only ZeroS3 diagnostic endpoint used by structural inspection.

It derives reachability from the same authoritative root categories used by the
store's liveness machinery rather than from a persistent refcount index.

It is not intended as an S3 API.

## Versioning rules

Clients must use the advertised protocol identifiers rather than infer
compatibility.

For v1 JSON requests, send exactly:

```json
{
  "protocol": 1,
  "cdc": "gear-v1",
  "hash": "sha256"
}
```

where the request shape includes those fields.

A server/client should reject unknown protocol/CDC/hash combinations rather
than interpreting them approximately.

Bulk v2 is optional and independently advertised. Absence or incompatible bulk
advertisement means use the v1 transfer endpoints.

Future incompatible native-protocol evolution must use explicit versioning.

## Physical representation is never protocol state

Native clients must not rely on:

- loose chunk paths;
- pack IDs;
- pack offsets;
- record compression;
- locator internals;
- hot/warm/cold tier;
- compaction/repack state.

A logical digest can move between those representations without changing the
native protocol.

This is intentional: native protocol compatibility tracks logical content, not
one storage implementation.

## Independent-client guidance

A new client implementation should proceed in this order:

1. implement SigV4 using the vectors in `testing-harnesses/vectors/`;
2. pass Core Client Profile v1 for ordinary S3;
3. implement discovery;
4. implement CDC `gear-v1` exactly;
5. implement v1 negotiate/upload/commit;
6. verify all downloaded chunks locally;
7. add object-descriptor/fetch support for replication if needed;
8. add bulk v2 only after the v1 path is correct;
9. fall back to ordinary S3 when native discovery is unavailable.

The golden vector suite covers tricky raw-path/query/signing cases and should be
preferred over developing canonicalization only against friendly object names.

## Security considerations

- native endpoints require normal SigV4 authentication;
- source peers must never be trusted to assert chunk integrity;
- downloaded/fetched chunk payloads should be independently rehashed;
- clients should enforce advertised and local maximum sizes;
- a client-orchestrated relay avoids a server-side arbitrary outbound-fetch
  surface;
- endpoint discovery is capability detection, not authorization.

The current static-credential security boundary is described in
[../STATUS.md](../STATUS.md).

## Related documentation

- [../S3_COMPAT.md](../S3_COMPAT.md): ordinary S3 contract
- [ARCHITECTURE.md](./ARCHITECTURE.md): logical/physical storage design
- [OPERATIONS.md](./OPERATIONS.md): operator workflows
- [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md): portable snapshot artifacts
- [../testing-harnesses/README.md](../testing-harnesses/README.md): black-box validation