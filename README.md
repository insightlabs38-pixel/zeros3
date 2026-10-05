# ZeroS3

**S3 on the outside. Content-aware storage underneath.**

ZeroS3 is a self-hosted, single-node S3-compatible object store written in Go
with **zero third-party runtime dependencies**. Ordinary S3 applications can
use it as an object store; underneath that interface, objects are represented
as content-defined chunks in a SHA-256 content-addressed store.

That substrate gives ZeroS3 capabilities that are usually separate systems:
deduplication, edit-locality reuse, delta transfer, peer repair, retained
history, copy-on-write forks, immutable snapshots, thin snapshot deltas,
locality-aware packed storage, and hot/warm/cold physical placement.

The production implementation intentionally remains one Go source file:
[`zeros3.go`](./zeros3.go).

> **Current status:** public preview / pre-1.0, Linux, single-node and
> feature-frozen while real integrations and documentation are hardened.
> See [STATUS.md](./STATUS.md).

## Why ZeroS3

ZeroS3 is not an attempt to recreate the entire AWS S3 product or compete with
distributed object stores on cluster scale.

Its narrower goal is to make a content-native storage engine directly usable
through a standard S3 surface.

```text
ordinary S3 application
        |
        | S3 / SigV4
        v
+-------------------------+
|        ZeroS3           |
|                         |
| CDC -> SHA-256 CAS      |
|       -> manifests      |
|       -> durable roots  |
|                         |
| loose / packed storage  |
| locality / compression  |
| hot / warm / cold       |
+-------------------------+
```

The logical object is independent of its physical representation. The same
manifest can keep describing the same bytes while chunks move from loose files
into packs, packs are compressed or repacked, or physical copies move between
tiers.

That separation is the basis for most of ZeroS3's feature compression: higher
level capabilities reuse the same few storage primitives instead of creating
parallel storage subsystems.

## Two ways to use it

### 1. Ordinary S3 client

Use an AWS-compatible SDK, CLI, or application normally.

The client gets the standard S3 behavior in
[Core Client Profile v1](./S3_COMPAT.md#core-client-profile-v1). Server-side
content-aware behavior is transparent:

- content-defined chunking;
- SHA-256 CAS deduplication;
- immutable manifests;
- retained overwrite/delete history;
- packed/locality-aware physical storage;
- verified reads;
- physical tiering and maintenance.

A normal full `PutObject` still sends the full object over the network. ZeroS3
can avoid storing duplicate chunks, but an ordinary S3 client does not perform
client-side delta negotiation.

### 2. ZeroS3-aware client

A client can detect ZeroS3 with:

```sh
./zeros3 probe -endpoint http://127.0.0.1:9000
```

and use the ZeroS3-native chunk protocol.

That enables content-aware transfer:

```text
client CDC
   |
   +--> describe / negotiate hashes
   |
   +--> transfer only missing chunks
   |
   +--> atomic ordinary-object commit
```

The resulting object is still an ordinary S3 object.

The built-in `sync`, `replicate`, and `repair` commands use this model.
Independent clients can target the documented protocol in
[docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md).

## Good fit / not a fit

| Good fit today | Not the current target |
|---|---|
| local/self-hosted S3 development | multi-node HA object-store cluster |
| research/team artifact storage | public multi-tenant cloud storage |
| model/checkpoint revisions | complete AWS S3 parity |
| datasets and build artifacts | IAM/STS/KMS platform |
| content with repeated revisions | Windows storage server |
| applications that value dedup/snapshots | automatic distributed consensus |
| ZeroS3-aware delta clients | transparent replacement for every S3 workload |

## Quick start

ZeroS3 currently builds from source. It requires Go **1.27.x**.

```sh
git clone https://github.com/insightlabs38-pixel/zeros3.git
cd zeros3

go build -o zeros3 zeros3.go
./zeros3 serve
```

Defaults:

```text
store:   ./zeros3-data
listen:  127.0.0.1:9000
region:  us-east-1
```

Example credentials:

```text
Access Key ID:     AKIAZEROS3EXAMPLE01
Secret Access Key: zeros3exampleSecretKeyForM1TestingOnly01
```

**These credentials are public examples. Do not expose a server using them
beyond local testing.** Configure your own credentials before binding ZeroS3
to a non-loopback interface.

Credentials can be set with server flags or:

```sh
export AWS_ACCESS_KEY_ID='your-access-key'
export AWS_SECRET_ACCESS_KEY='your-secret-key'
export AWS_REGION='us-east-1'
```

### AWS CLI

```sh
export AWS_ACCESS_KEY_ID='AKIAZEROS3EXAMPLE01'
export AWS_SECRET_ACCESS_KEY='zeros3exampleSecretKeyForM1TestingOnly01'
export AWS_DEFAULT_REGION='us-east-1'

aws --endpoint-url http://127.0.0.1:9000 s3 mb s3://demo
aws --endpoint-url http://127.0.0.1:9000 s3 cp ./hello.txt s3://demo/hello.txt
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo/hello.txt -
```

### AWS SDK for Go v2

Use a normal S3 client with a custom endpoint and path-style addressing:

```go
cfg, _ := config.LoadDefaultConfig(ctx,
    config.WithRegion("us-east-1"),
    config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
        "AKIAZEROS3EXAMPLE01",
        "zeros3exampleSecretKeyForM1TestingOnly01",
        "",
    )),
)

client := s3.NewFromConfig(cfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String("http://127.0.0.1:9000")
    o.UsePathStyle = true
})
```

For the exact supported S3 surface, see [S3_COMPAT.md](./S3_COMPAT.md).

## Architecture in three layers

```text
APPLICATION SURFACE
  ordinary S3                    ZeroS3-aware tools
  PUT/GET/range/multipart        sync/replicate/repair
           \                      /
            +--------------------+
                     |
                     v
LOGICAL CONTENT LAYER
  deterministic CDC
       -> SHA-256 chunk identity
       -> immutable manifests
       -> journal/current roots
       -> history / snapshots / forks
                     |
                     v
PHYSICAL STORAGE LAYER
  loose CAS
       or immutable pack-v1
       -> locality-aware record order
       -> optional per-record DEFLATE
       -> hot / warm / cold roots
       -> GC / repack / rebalance
```

The important invariant is:

> **manifests describe logical chunks, not pack locations.**

Physical maintenance can therefore change layout without changing object
identity or the S3 representation.

Read [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) for the complete storage,
durability, read/write, and root model.

## Core capabilities

### S3-compatible application surface

Core Client Profile v1 includes:

- bucket create/list/head/delete/location;
- object put/get/head/delete/batch-delete/copy;
- ListObjectsV2;
- single byte ranges;
- ETag read/write conditions;
- Content-MD5 and CRC32 request checks;
- persistent multipart upload;
- SigV4 header auth;
- presigned GET/PUT;
- signed non-trailer aws-chunked payloads;
- path-style and optional virtual-hosted addressing.

See [S3_COMPAT.md](./S3_COMPAT.md).

### Content-defined deduplication

Object bytes are split with deterministic content-defined chunking:

```text
16 KiB minimum
64 KiB target
256 KiB maximum
```

Each chunk is identified by SHA-256. Local edits therefore tend to perturb
only chunks around the edit instead of shifting every later fixed-size block.

### Streaming ingest and reads

Large objects do not require whole-object buffering.

Known-size writes of at least 64 MiB can stream new chunks directly into
locality-ordered immutable hot packs. Smaller or size-unknown writes and some
internal transfer paths use grouped loose CAS publication.

Reads reconstruct objects from their manifests and verify logical chunk hashes
before serving bytes.

### Immutable packed storage

Loose chunks can be compacted into immutable pack-v1 files.

Packs support:

- raw or adaptive per-record DEFLATE storage;
- locality-aware record order;
- bounded coalesced reads;
- GC of fully dead packs;
- repacking of partly dead packs;
- hot/warm/cold physical placement.

Pack identity and placement never appear in object manifests.

### History and structural sharing

Overwrites and deletes retain previous roots until explicitly pruned.

The same immutable content graph supports:

- `versions` and zero-copy `restore`;
- `CopyObject` with no new payload when chunks already exist;
- copy-on-write namespace `fork`;
- immutable namespace `snapshot`;
- structural `diff` and `inspect`.

### Portable snapshots

A snapshot can become a portable artifact:

- **`.zs3b` full bundle** — self-contained descriptor, manifests, and unique
  chunk payloads;
- **`.zs3d` delta bundle** — complete target metadata plus only chunk payloads
  absent from one exact base snapshot.

See [BUNDLE_FORMAT.md](./BUNDLE_FORMAT.md).

### Delta movement and repair

ZeroS3-aware transfer operates on logical chunk identities rather than whole
object blobs.

It supports:

- local file/directory sync;
- ZeroS3-to-ZeroS3 replication;
- optional bounded bulk transport;
- peer-assisted repair of missing/corrupt live chunks.

See [docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md).

### Physical tiers

Hot, warm, and cold pack roots all sit under one logical CAS.

Tier policy is content-aware: if one chunk is referenced by several live roots,
its desired placement is the **hottest** tier requested by any of those roots.

Physical tiering is not exposed as AWS S3 StorageClass.

## Try these next

### Inspect the store

```sh
./zeros3 stats -store ./zeros3-data
./zeros3 doctor -store ./zeros3-data
./zeros3 verify -store ./zeros3-data
```

Use `-deep` when you want full content re-hashing:

```sh
./zeros3 verify -store ./zeros3-data -deep
```

### Create an immutable namespace snapshot

With the server running:

```sh
./zeros3 snapshot create   -endpoint http://127.0.0.1:9000   s3://demo

./zeros3 snapshot list   -endpoint http://127.0.0.1:9000
```

### Export a portable snapshot

After obtaining a snapshot ID:

```sh
./zeros3 bundle export   -store ./zeros3-data   -snapshot SNAPSHOT_ID   -out snapshot.zs3b
```

Bundle import/maintenance is offline. See
[docs/OPERATIONS.md](./docs/OPERATIONS.md).

### Compact loose content

Stop `zeros3 serve` before exclusive maintenance:

```sh
./zeros3 compact -store ./zeros3-data
```

Large known-size uploads may already be directly packed, so compact only needs
to handle remaining loose content.

## Measured behavior

Measurements are environment-specific, not universal performance claims.
The full methodology and historical context live in
[docs/BENCHMARKS.md](./docs/BENCHMARKS.md).

A few results that characterize the current architecture:

| Scenario | Result |
|---|---|
| localized edit | 4 MiB object + ~4 KiB insertion reused 96.6% of original bytes; fixed 64 KiB blocks reused 0% in the comparison fixture |
| direct pack ingest | 256 MiB pseudo-random PUT: 72–89 MiB/s, +7 MiB RSS, 0 loose chunks / 4 packs |
| time to locality-packed state | baseline PUT + compact 7.1 s; direct ingest 2.9 s (~59% less) |
| locality-aware packed GET | 256 MiB full GET: 4,019 -> 4 pack opens and 609 -> 852 MiB/s in the locality experiment |
| checkpoint delta bundle | 128 MiB S1->S2 delta: 3.67 MiB vs 128.26 MiB full target bundle (97.1% smaller) |

The current direct-packed 1 GiB sanity run reached 81.6 MiB/s PUT with about
+14 MiB RSS, created 16 packs and no loose chunks, and immediately read at
917 MiB/s on the recorded machine.

## Validation

ZeroS3 uses two test layers:

- **root suite:** stdlib-only white-box tests, crash injection, corruption
  cases, and race testing;
- **testing-harnesses:** separate dependency-bearing module that drives real
  ZeroS3 processes through independent S3 clients.

Validated client paths include AWS SDK for Go v2, minio-go, and rclone.

Golden SigV4/presign/wire vectors support independent client implementations.

See [testing-harnesses/README.md](./testing-harnesses/README.md).

## Zero-dependency core

The root module has:

- no third-party runtime packages;
- no `require` block;
- no root `go.sum`;
- no vendor directory;
- no CGO requirement;
- no subprocess dependency from `zeros3.go`.

[STDLIB.md](./STDLIB.md) explains the major standard-library substitutions.
[`deps-proof.txt`](./deps-proof.txt) records current source-level dependency
evidence and the exact Go 1.27 commands used to reproduce the mechanical proof.

Third-party SDKs are confined to the independent black-box harness module.

## Current boundaries

The important current limitations are deliberate and explicit:

- one writer process per store;
- no distributed/HA operation;
- one static SigV4 credential pair;
- no IAM/STS/KMS/ACL/policy engine;
- no server-side encryption;
- Linux is the validated platform;
- several destructive/physical-maintenance operations are offline and take
  exclusive store ownership;
- ZeroS3 internal version history is not the AWS S3 Versioning API;
- physical hot/warm/cold tiers are not S3 StorageClass;
- no comprehensive hardware power-loss/device-failure test campaign;
- pre-1.0 CLI and ZeroS3-native protocol surfaces may still evolve under
  explicit versioning rules.

See [STATUS.md](./STATUS.md) for the precise maturity/format posture.

## Documentation

| Document | Purpose |
|---|---|
| [STATUS.md](./STATUS.md) | maturity, supported deployment, format versions, compatibility policy |
| [S3_COMPAT.md](./S3_COMPAT.md) | exact ordinary-S3 contract |
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | logical/physical storage design and durability invariants |
| [docs/OPERATIONS.md](./docs/OPERATIONS.md) | running, backup, recovery, maintenance, upgrades |
| [docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md) | ZeroS3-aware discovery, delta, bulk, and commit protocol |
| [docs/BENCHMARKS.md](./docs/BENCHMARKS.md) | current measurements and historical performance context |
| [BUNDLE_FORMAT.md](./BUNDLE_FORMAT.md) | full and delta snapshot artifact formats |
| [STDLIB.md](./STDLIB.md) | standard-library implementation notes |
| [testing-harnesses/README.md](./testing-harnesses/README.md) | independent validation and evidence map |
| [CONTRIBUTING.md](./CONTRIBUTING.md) | architecture constraints and contribution workflow |
| [SECURITY.md](./SECURITY.md) | private vulnerability reporting and security boundary |

## Project layout

```text
zeros3.go                 complete production implementation
zeros3_test.go            stdlib-only white-box test suite
go.mod                    Go 1.27 module; no require directives

README.md                 project landing page
STATUS.md                 maturity / format contract
SECURITY.md               private vulnerability reporting / security policy
S3_COMPAT.md              ordinary S3 compatibility contract
BUNDLE_FORMAT.md          portable snapshot formats
STDLIB.md                 stdlib / zero-dependency engineering notes
deps-proof.txt            generated dependency evidence

docs/                     architecture, operations, protocol, benchmarks
scripts/                  validation and reproducible-build helpers
testing-harnesses/        external black-box validation (separate Go module)
```

## License

Apache License 2.0. See [LICENSE](./LICENSE).