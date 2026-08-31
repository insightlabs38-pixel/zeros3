# ZeroS3

**S3 on the outside. Content-addressed storage underneath.**

ZeroS3 is a local, self-hosted, S3-compatible object store built with Go
1.27 and **zero third-party runtime dependencies** — one implementation
file, `zeros3.go`, plus an organizer-approved `zeros3_test.go`. Ordinary
S3 clients (the AWS SDK, `rclone`, the AWS CLI) talk to it exactly as
they would talk to real S3. Underneath that ordinary surface, every
object is split into content-defined chunks, stored once in a SHA-256
content-addressed store, described by an immutable manifest, and made
visible only through a durable, append-only journal.

That one storage substrate is what lets a single codebase support
deduplication, delta transfer, peer-assisted repair, copy-on-write
forks, and durable snapshots — not as nine unrelated features, but as
consequences of one architecture: **content-defined chunking → SHA-256
CAS → immutable manifests → visibility journal.**

- Ordinary S3 clients: AWS SDK for Go v2, `rclone`, the AWS CLI
- CDC + SHA-256 CAS deduplication, measured against real uploads
- Crash-safe immutable manifests + an append-only visibility journal
- Delta sync and remote-to-remote replication that transfer only missing bytes
- Peer-assisted chunk repair, verified byte-for-byte before publication
- Copy-on-write namespace forks and durable, restorable snapshots
- Atomic conditional writes (`If-Match` / `If-None-Match`)
- Bounded parallel chunk transfer
- Zero third-party dependencies, reproducible build

ZeroS3 is not trying to compete with MinIO or Ceph on distributed
cluster scale — no clustering, no IAM, no erasure coding, no multi-node
HA. Its differentiator is narrower and, we think, more interesting: an
ordinary S3 surface sitting directly on a content-aware storage engine,
in the spirit of what Git and Xet-like systems do for structural
sharing — applied to an S3-shaped object store.

## Why ZeroS3

Standing up a local S3-compatible store for development or testing
normally means pulling in a dependency-heavy server or SDK stack.
ZeroS3 implements the storage and protocol layers directly instead:
incoming objects are content-defined-chunked, stored once by SHA-256
content address, described by an immutable manifest, and made visible
only through an append-only, checksummed journal. That architecture
isn't incidental — it's what lets `CopyObject` publish a new object
version without moving a single payload byte, what lets an edited
revision of a large file reuse most of its bytes automatically, and what
lets replication, repair, forking, and snapshots all reuse the same
negotiate/fetch/commit machinery instead of each needing its own.

## Quick start

Requires Go **1.27.x** (`go.mod` pins `go 1.27.0`; `GOTOOLCHAIN=auto`
fetches it automatically, or install it from [go.dev/dl](https://go.dev/dl/)).

```sh
go build -o zeros3 zeros3.go
./zeros3 serve
# defaults: store ./zeros3-data, listen 127.0.0.1:9000
```

Default credentials (a single static keypair — there is no IAM/STS/KMS;
see "Known limitations"):

```
Access Key ID:     AKIAZEROS3EXAMPLE01
Secret Access Key: zeros3exampleSecretKeyForM1TestingOnly01
Region:            us-east-1
```

Override with `-access-key`/`-secret-key`/`-region`, or the standard
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION` environment
variables. Point any path-style S3 client at it:

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

Or with the AWS CLI:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 mb s3://my-bucket
aws --endpoint-url http://127.0.0.1:9000 s3 cp ./hello.txt s3://my-bucket/hello.txt
```

## Architecture

```
S3 / SigV4
    |
    v
content-defined chunking (CDC)
    |
    v
SHA-256 content-addressed store (CAS)
    |
    v
immutable manifests
    |
    v
visibility journal
```

- **S3 / SigV4** — a custom root `http.Handler` (no `http.ServeMux`)
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
  the sole authority for which buckets/keys currently exist. A read
  replays it at store-open time to learn the namespace, then follows
  manifest → chunks to reconstruct bytes.

Every higher-level capability below is orchestration over this one
substrate, not a parallel implementation:

```
CDC + CAS
  -> dedup / edit-locality reuse
  -> delta sync / remote replication
  -> peer-assisted repair
  -> copy-on-write fork
  -> snapshots / restore
  -> diff / inspect
```

## What it can do

**S3 compatibility.** `CreateBucket`, `ListBuckets`, `HeadBucket`,
`DeleteBucket`, `PutObject`, `GetObject`, `HeadObject`, `DeleteObject`,
`ListObjectsV2` (prefix/delimiter/pagination), `CopyObject`
(`COPY`/`REPLACE` directives, same/cross-bucket, source preconditions),
single-range `GetObject`, and a full persistent multipart upload
lifecycle (`CreateMultipartUpload`/`UploadPart`/`ListParts`/
`CompleteMultipartUpload`/`AbortMultipartUpload`/`ListMultipartUploads`,
survives a real process restart mid-upload) — all over path-style and
opt-in virtual-hosted-style addressing, with both Authorization-header
and presigned-URL SigV4. See [`S3_COMPAT.md`](./S3_COMPAT.md) for the
exact contract.

**Content-aware storage.** Every write goes through the same CDC → CAS →
manifest → journal pipeline. `CopyObject` publishes a new object version
without moving a payload byte; an edited revision of a large object
reuses the vast majority of its bytes automatically; internal object
version history and zero-copy restore are built on the same immutable
manifests (`zeros3 versions`/`restore`/`gc`).

**Delta movement.** `zeros3 sync` ingests a local file or directory
using far less transfer than a full upload when the store already holds
most of the bytes. `zeros3 replicate` (optionally `-recursive`, for a
whole bucket or prefix) moves objects between two ZeroS3 servers,
transferring only the chunks the destination doesn't already have, as a
client-orchestrated relay — neither server ever contacts the other
directly. `-workers N` bounds parallel chunk transfer for sync, repair,
and replication alike.

**Integrity and recovery.** `zeros3 verify` (`-deep` for full content
re-hashing) checks structural, per-chunk, and whole-object integrity and
never mutates anything. `zeros3 repair -from PEER` restores missing or
corrupt chunk bytes from an explicitly-trusted peer, verifying every
byte's SHA-256 before it's ever written to disk. `zeros3 gc` is
dry-run by default and refuses to run if the live root set isn't fully
valid, rather than risk treating broken live data as garbage.

**Structural sharing and history.** `zeros3 fork` clones a bucket or
prefix inside one store with zero new CAS payload bytes — a true
copy-on-write namespace. `zeros3 snapshot create/restore` captures
immutable, restorable point-in-time namespace state that survives
subsequent mutation or deletion of its source, pinned through garbage
collection, restored with zero new CAS payload. `zeros3 diff` and
`zeros3 inspect` are read-only tools for comparing objects and
inspecting a store's structural sharing.

**Operational hardening.** Environment-variable credentials
(`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`), conservative
HTTP server timeouts, graceful `SIGINT`/`SIGTERM` shutdown with a bounded
drain, and optional HTTPS via Go's standard-library TLS server
(`-tls-cert`/`-tls-key`). Atomic conditional writes (`If-Match`/
`If-None-Match`) resolve concurrent writers to exactly one winner, with
the condition revalidated at ZeroS3's own locked namespace-commit
boundary rather than a race-prone check before the request body is
read.

## Measured results

Two measurements that tell the architectural story; both are
environment/fixture-specific, not universal claims.

**Localized-edit CDC reuse.** A 4MiB object with a ~4KB insertion near
the start: 96.6% byte reuse from the original upload
(`TestDedup_EditedObjectReuseBeatsFixedSizeChunking`), against 0% reuse
for the same edit chunked with a fixed 64KiB window. An 8MB file synced,
then re-synced after a small 4KiB mid-file insertion, reused 99.0% of
its bytes and transferred only the touched chunks.

**Bounded parallel delta transfer.** Loopback benchmark, 4 vCPU, a 10ms
simulated per-request delay standing in for real-network RTT, 256 MiB of
missing payload:

| Workers | Throughput | Speedup vs. 1 worker |
|---:|---:|---:|
| 1 | 4.36 MiB/s | 1.00x |
| 16 | 35.70 MiB/s | 8.18x |

`-workers` is configurable (1..32, default 8); publication stays
serialized and safe regardless of worker count.

## Verification

- **Internal test suite:** 738 tests green; `go vet ./...` and
  `gofmt -l .` clean; `go test -race ./...` clean.
- **AWS SDK for Go v2 interoperability:** validated black-box against a
  real `zeros3` process using an ordinary, unmodified SDK client —
  bucket/object CRUD, `ListObjectsV2`, `CopyObject`, range GET,
  presigned GET/PUT, and a full persistent multipart lifecycle including
  a real process restart mid-upload.
- **`rclone` interoperability:** validated black-box with an unpatched
  `rclone` client, including a genuine 1 GiB / 205-part multipart
  upload, restart-persisted and downloaded with exact SHA-256 equality.
- **Package Killer comparison:** the same frozen AWS SDK test logic, run
  unmodified against both ZeroS3 and `s3rver` 3.7.1 (changing only
  endpoint/credential/addressing settings) — 14/14 passed on both
  targets.
- **Crash and restart testing:** real process restart mid-multipart-
  upload, real SIGINT/SIGTERM graceful-shutdown scenarios, and
  deterministic in-process crash injection against the journal/CAS
  recovery model.
- **Concurrency:** deterministic concurrent-conditional-writer races
  (N clients, exactly one winner), parallel transfer cancellation, and
  the race detector across the full suite.
- **Reproducible build:** two independent source copies, built
  separately, produce byte-identical output — see below.

## Zero-dependency proof

- `go.mod` has no `require` block; no `go.sum`; no `vendor/` directory.
- `go list -deps .` resolves only Go standard-library packages (plus
  toolchain-internal `internal/...`/`vendor/golang.org/x/...` entries
  that are part of the Go toolchain's own implementation of
  `net/http`/`crypto/tls` — not a ZeroS3 dependency) and the Go 1.27
  standard library's own `uuid` package.
- `CGO_ENABLED=0 go build` succeeds; no subprocess/shell-out anywhere in
  `zeros3.go`.
- Full generated evidence: [`deps-proof.txt`](./deps-proof.txt).
  Substitution-by-substitution detail: [`STDLIB.md`](./STDLIB.md).
- External interoperability validation (the AWS SDK, `rclone`) is
  performed out-of-process, against a running `zeros3` binary over plain
  HTTP — never imported by, linked into, or required by this module.

## Reproducible build

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
```

Two independent builds — from two separately-copied source trees at two
different absolute paths — produce byte-identical output. Reproduce with
[`scripts/reproducible_build.sh`](./scripts/reproducible_build.sh) (no
arguments needed).

## Platform

Linux is the supported and tested platform for the hackathon build.
Other Unix-like systems may work but were not part of the validated
target. Windows is not currently supported or tested.

## Known limitations

Honest, not exhaustive — see [`S3_COMPAT.md`](./S3_COMPAT.md) for the
exact API contract:

- Single writer process per store; no distributed/HA operation.
- Internal object version history (`zeros3 versions`/`restore`) is a
  ZeroS3-only mechanism, not the AWS S3 Versioning API.
- No IAM/STS/KMS/ACL/policy engine; a single static credential pair.
- `replicate`, `repair`, `fork`, and `snapshot` all require ZeroS3 on
  every server involved — no generic-AWS-S3 source or destination.
- The request body is buffered in memory (bounded, 256MiB max) rather
  than fully streamed end-to-end.
- No power-loss (real `kill -9`/hardware) testing beyond deterministic
  in-process crash injection and direct on-disk truncation.

## Project layout

```
zeros3.go        the entire implementation (stdlib only)
zeros3_test.go   the entire test suite (stdlib testing only)
go.mod           module zeros3, go 1.27.0, no require block
S3_COMPAT.md     exact supported/unsupported/deviating S3 behavior
STDLIB.md        standard-library substitutions, mapped to shipped code
deps-proof.txt   generated zero-dependency evidence
scripts/         reproducible-build verification script
```

The implementation intentionally remains one Go source file for the
hackathon's Single File constraint; both `zeros3.go` and
`zeros3_test.go` open with subsystem maps for navigation.

## License

[Apache License 2.0](./LICENSE).
