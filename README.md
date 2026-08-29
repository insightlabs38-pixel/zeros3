# ZeroS3

**0 Dependencies Hackathon — Track D: Data & Storage**

**S3 on the outside, content-addressed storage underneath.**

A local, self-hosted, S3-compatible object store, built with Go 1.27 and
**zero third-party runtime dependencies** — one implementation file,
`zeros3.go`, and an organizer-approved `zeros3_test.go`.

## At a glance

| | |
|---|---|
| **Track** | Track D — Data & Storage |
| **Runtime dependencies** | zero — `go.mod` has no `require` block ([`deps-proof.txt`](./deps-proof.txt)) |
| **Implementation source files** | one — `zeros3.go` (plus organizer-approved `zeros3_test.go`) |
| **Real external S3 client proof** | pinned AWS SDK for Go v2, black-box, in [`zeros3-testing`](https://github.com/insightlabs38-pixel/zeros3-testing) — see "External interoperability" below |
| **Persistence / crash model** | append-only, CRC32C-framed visibility journal; acknowledged mutation ⇒ durable (see "Durability model") |
| **Dedup** | content-defined chunking (CDC) over a SHA-256 CAS; measured, not asserted (see "Dedup and stats") |
| **Reproducible build** | two independent source copies, byte-identical SHA-256 (see "Reproducible build") |
| **Bonus claims** | Single File, Reproducible Build, STDLIB Log; Package Killer only if `STATUS.md`'s GO/NO-GO section says GO |

## Why

Standing up a local S3-compatible store for development or testing
normally means pulling in a dependency-heavy server or SDK stack. ZeroS3
demonstrates the storage and protocol layers directly: incoming objects
are split into content-defined chunks, stored once by SHA-256 content
address, described by an immutable manifest, and made visible only
through an append-only, checksummed journal. That architecture isn't
incidental — it's what lets `CopyObject` publish a new object version
without moving a single payload byte, and what lets an edited revision of
a large file reuse the vast majority of its bytes automatically. Both are
measured, not claimed — see "Dedup and stats" below.

## Quick start

Requires Go **1.27.x** (`go.mod` pins `go 1.27.0`; `GOTOOLCHAIN=auto`
will fetch it automatically, or install it from
[go.dev/dl](https://go.dev/dl/)).

```sh
# Build
go build -o zeros3 zeros3.go

# Run (defaults: store ./zeros3-data, listen 127.0.0.1:9000)
./zeros3 serve
```

Default credentials (a single static keypair — there is no IAM/STS/KMS;
see "Known limitations"):

```
Access Key ID:     AKIAZEROS3EXAMPLE01
Secret Access Key: zeros3exampleSecretKeyForM1TestingOnly01
Region:             us-east-1
```

Point any path-style S3 client at it, for example the AWS SDK for Go v2:

```go
cfg, _ := config.LoadDefaultConfig(ctx,
    config.WithRegion("us-east-1"),
    config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
        "AKIAZEROS3EXAMPLE01", "zeros3exampleSecretKeyForM1TestingOnly01", "")),
)
client := s3.NewFromConfig(cfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String("http://127.0.0.1:9000")
    o.UsePathStyle = true
})
client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("my-bucket")})
client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("my-bucket"), Key: aws.String("hello.txt"), Body: ...})
```

## What works

Supported over path-style HTTP with Authorization-header SigV4:
`CreateBucket`, `ListBuckets`, `HeadBucket`, `DeleteBucket` (empty
buckets only), `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`,
`ListObjectsV2` (`prefix`/`delimiter`/`max-keys`/continuation tokens),
`CopyObject` (`COPY`/`REPLACE` metadata directives, same/cross-bucket),
single-range `GET` (`bytes=start-end`, open-ended, suffix; 416 for an
unsatisfiable range; a multi-range header falls back to a full 200, per
RFC 7233). `Content-Type` and `x-amz-meta-*` metadata round-trip on every
operation that carries them. Ordinary `x-amz-checksum-crc32` request
integrity is validated (the exact default behavior of a current AWS SDK
Go v2 client), and `Content-MD5` is validated when a client sends it
(rclone's ordinary single-part upload path does). `zeros3 stats`
(human/`-json`) and `zeros3 verify`
(structural, and `-deep` for full content re-hashing plus a whole-object
SHA-256 check) round out the CLI.

Not implemented (see `S3_COMPAT.md`/`STATUS.md` for the full deviation
list): versioning, presigned URLs, multipart upload, object-lock/ACL/
policy/IAM, conditional-copy headers, self-copy rejection, and
`aws-chunked` streaming checksum trailers.

## Architecture

```
HTTP/SigV4  →  CDC  →  SHA-256 CAS  →  immutable manifest  →  visibility journal
```

- **HTTP/SigV4** — a custom root `http.Handler` (no `http.ServeMux`)
  keeps the raw request target intact through authentication, so S3
  path-normalization traps (`//`, `%2F`, `+` vs `%20`) can't be silently
  rewritten before the signature is checked.
- **CDC** — a streaming Gear-hash content-defined chunker (16KiB min /
  64KiB target / 256KiB max) splits object bytes at content-determined
  boundaries, so an edit anywhere in a file only perturbs the chunks
  near that edit.
- **SHA-256 CAS** — each chunk is stored once, named by its own content
  hash; a second write of identical bytes is a no-op.
- **Immutable manifest** — one JSON file per object version: its ordered
  chunk list, total length, object SHA-256, ETag, Content-Type, and
  metadata. Manifests are never mutated, only superseded.
- **Visibility journal** — an append-only, CRC32C-framed binary log is
  the *sole* authority for which buckets/keys currently exist. A GET
  replays it (at store-open time) to learn the namespace, then follows
  manifest → chunks to reconstruct bytes.

## Dedup and stats

Measured with `zeros3 stats` (or `stats -json`) against real uploads —
not asserted, not hardcoded:

- **Identical-object reuse:** uploading the same object twice adds zero
  new bytes to the chunk store (`chunk_store_file_bytes` unchanged) while
  `logical_current_bytes` counts both copies — `dedup_reduction` of
  exactly 50% for one exact duplicate.
- **Edited-object reuse:** a 4MiB object with a ~4KB insertion near the
  start reused **97.5%** of its bytes from the original upload in the
  external `zeros3-testing` black-box demo (`harness/m3/dedup`), and a
  similarly-shaped internal test measured **96.6%** reuse against **0%**
  reuse for a fixed-64KiB-chunk comparison over the identical byte
  strings (`TestDedup_EditedObjectReuseBeatsFixedSizeChunking`) — CDC's
  whole point. These are measurements of a specific corpus/edit shape,
  not a universal guarantee; different edits reuse different amounts.
- **CopyObject writes zero new CAS payload bytes** for both metadata
  directives — proven internally (`TestCopyObject_
  SameBucketZeroNewCASChunkBytes`) and externally over the real S3 wire
  protocol (`zeros3-testing`'s `harness/m3/copy`, 46/46 passed).

`stats` distinguishes *logical* bytes (what a scope refers to), *unique*
bytes (distinct content anywhere in the store), *exclusive*/*shared*
bytes (whether other objects also reference a chunk), and `*_file_bytes`
(actual on-disk measurements) — never conflating "shared" with "owned."

## Durability model

- Immutable CAS chunks and the immutable manifest are always fully
  published (written, fsynced, renamed into place) *before* the journal
  record referencing them is even built.
- The journal's `fsync` is the acknowledgment threshold: a mutation
  updates the in-memory namespace, and gets acknowledged over HTTP, only
  after its journal frame's `Sync()` call returns successfully. So:
  **acknowledged mutation ⇒ durable**, always.
- The converse is not claimed: an **unacknowledged** mutation's actual
  durability is genuinely indeterminate — replay after a crash may
  legally observe either the previous complete state or the new complete
  state, whichever the OS actually flushed.
- What replay may **never** produce, under either outcome, is a partial
  or mixed state: a manifest referencing incomplete/missing chunks, or
  object bytes blending two versions. A journal write or sync failure
  poisons the process against further mutation until the store is
  reopened, rather than risking a write at an uncertain offset.

See `STATUS.md`'s "Durability contract" for the exact, itemized
crash-point-by-crash-point recovery guarantees and their tests.

## Verification and stats

`zeros3 verify` never repairs or deletes anything — it only reports, and
exits nonzero on any integrity failure:

- **Structural:** journal replay validity; every reachable root's
  manifest file exists and its bytes' SHA-256 matches *that root's own*
  journal-recorded reference (checked independently per root, even when
  several roots share a manifest UUID); chunk references are well-formed
  and sum to the declared total length.
- **Chunks:** existence + declared length always; `-deep` additionally
  re-hashes actual chunk bytes.
- **Whole-object digest, `-deep` only:** every reachable manifest's
  chunks are streamed, in order, through one SHA-256 hasher and compared
  against the manifest's own `object_sha256` — catching a case per-chunk
  hashing alone cannot: a manifest naming the wrong object digest, or
  listing intact chunks in the wrong order.

## Zero-dependency proof

- `go.mod` has no `require` block; no `go.sum`; no `vendor/`.
- `go list -deps .` contains only Go standard-library packages (plus
  `internal/...`/`vendor/golang.org/x/...` entries that are part of the
  Go toolchain's *own* implementation of `net/http`/`crypto/tls` — not a
  ZeroS3 dependency) and the Go 1.27 standard library's own `uuid`
  package.
- `CGO_ENABLED=0 go build` succeeds — no cgo required.
- No subprocess/shell-out anywhere in `zeros3.go`.
- Full generated evidence: [`deps-proof.txt`](./deps-proof.txt).
  Substitution-by-substitution detail: [`STDLIB.md`](./STDLIB.md).
- External test clients (the pinned AWS SDK for Go v2) live entirely in
  the separate [`zeros3-testing`](https://github.com/insightlabs38-pixel/zeros3-testing)
  repository — never imported by, linked into, or required by this
  module.

## External interoperability

All interoperability proof runs a real `zeros3` binary as a black-box S3
endpoint from the pinned AWS SDK for Go v2, in the separate
[`zeros3-testing`](https://github.com/insightlabs38-pixel/zeros3-testing)
repository:

- **M2 canonical workflow** (`harness/m2`) — **41/41 passed**: buckets,
  objects, `ListObjectsV2`, metadata, restart persistence, default SDK
  CRC32 checksum behavior.
- **CopyObject** (`harness/m3/copy`) — **46/46 passed**: same/cross-
  bucket, overwrite, both metadata directives, a new (not reused)
  destination `Last-Modified`, and encoded/tricky source keys.
- **Range GET** (`harness/m3/range`) — **27/27 passed**.
- **Dedup evidence** (`harness/m3/dedup`) — **7/7 passed**.

See that repository's `results/` directory for the exact recorded runs,
pinned SDK versions, and reproduction commands.

## Reproducible build

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
```

Two independent builds — from two separately-copied source trees at two
different absolute paths, on `go1.27.0 linux/amd64` — produce
byte-identical output:

```
SHA-256 (copy A): 1e98c1d57e49855d509d84921d0c9b3c09aacb8ef7164b35549a358ea423daf9
SHA-256 (copy B): 1e98c1d57e49855d509d84921d0c9b3c09aacb8ef7164b35549a358ea423daf9
```

Reproduce this with [`scripts/reproducible_build.sh`](./scripts/reproducible_build.sh)
(builds twice from two independent source copies and compares hashes; no
arguments needed).

## Known limitations

Honest, not exhaustive — see `STATUS.md` for the full list per milestone:

- Single writer process per store; no distributed/HA operation.
- No versioning, restore, garbage collection, multipart upload, or
  presigned URLs.
- No IAM/STS/KMS/ACL/policy engine; one static credential pair.
- The entire request body is buffered in memory (bounded, 256MiB max)
  rather than fully streamed end-to-end, since SigV4 payload-hash and
  CRC32 validation both need the complete body before chunking begins.
- No power-loss (real `kill -9`/hardware) testing beyond deterministic
  in-process crash injection and direct on-disk truncation — see
  "Durability model" above for exactly what is and isn't claimed.
- `CopyObject` does not implement conditional-copy headers or reject a
  same-key `COPY`-directive copy the way real S3 does in some cases.

## Project layout

```
zeros3.go        the entire implementation (stdlib only)
zeros3_test.go   the entire test suite (stdlib testing only)
go.mod           module zeros3, go 1.27.0, no require block
STATUS.md        milestone-by-milestone status, durability contract, test inventory
STDLIB.md        every stdlib substitution, mapped to shipped code
S3_COMPAT.md     exact supported/unsupported/deviating S3 behavior
DEMO.md          deterministic demo rehearsal script
deps-proof.txt   generated zero-dependency evidence
scripts/         reproducible-build verification script
```

## License

[Apache License 2.0](./LICENSE).
