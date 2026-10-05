# ZeroS3 testing harnesses

This directory contains ZeroS3's external black-box validation.

The production root module stays dependency-free. Third-party SDKs and
comparison tools live in this separate Go module and communicate with a real
ZeroS3 process through HTTP, CLI, filesystem-observable results, or process
lifecycle.

```text
testing-harnesses
        |
        | HTTP / CLI / process boundary
        v
     zeros3 binary
```

Nothing in this module is imported or linked by `zeros3.go`.

## Test layers

ZeroS3 uses two complementary test layers:

| Layer | Main purpose |
|---|---|
| `zeros3_test.go` | white-box storage invariants, malformed input, crash/corruption behavior, concurrency |
| `testing-harnesses/` | real SDK interoperability, process lifecycle, application workflows, benchmarks |

A change can satisfy internal invariants while still breaking an SDK, and a
wire-compatible change can still violate durability rules. The two layers test
different failure classes.

## Dependency boundary

The root module has no third-party requirements:

```text
go.mod
    go 1.27.0
    no require directives
```

The harness module intentionally contains external clients, including AWS SDK
for Go v2 and minio-go.

## Core Client Profile conformance

`profile/conformance/` implements ZeroS3 Core Client Profile v1 as a portable
endpoint test. It can run against a managed ZeroS3 fixture or a supplied
endpoint through the supported SDK adapters.

Example:

```sh
cd testing-harnesses

go run ./profile/conformance   -endpoint http://127.0.0.1:9000   -access-key K   -secret-key S   -json
```

The profile corresponds to [../S3_COMPAT.md](../S3_COMPAT.md).

## Golden vectors

`vectors/` contains fixed interoperability fixtures for:

- SigV4 canonical requests, strings-to-sign, and signatures;
- presigned URLs;
- representative S3 wire behavior.

The expected values are fixed rather than generated at test time, making the
vectors suitable for independent client implementations.

## Harness map

### S3 and client behavior

```text
m2/                         AWS SDK workflow + restart persistence
m3/{copy,range,dedup}/      CopyObject, Range, CAS reuse
m5a/presign/                presigned GET/PUT
m5b/multipart/              persistent multipart lifecycle
m5d/pagination/             multipart pagination
p1/env_and_shutdown/        env credentials + graceful shutdown
p2/list_multipart_uploads/  multipart-upload listing
rclone/                     rclone lifecycle + large multipart
package-killer/             same SDK workload against comparison targets
```

### ZeroS3-native behavior

```text
m6/sync/                    local-file delta sync
m6c/dirsync/                recursive directory sync
m8a/remote_delta/           ZeroS3-to-ZeroS3 replication
m8b/repair/                 peer-assisted repair
m8c/namespace_replication/  bucket/prefix replication
m8d/fork/                   copy-on-write namespace fork
m8e/snapshot/               snapshot and restore
m8f/conditional/            read/write precondition races
m8g/introspection/          diff / inspect / reachability
m8h/parallel_transfer/      bounded parallel transfer
```

### Storage and scale

```text
z2_streaming_put/           bounded-memory large PUT/GET
z2_aws_chunked/             signed aws-chunked interoperability
z2_packed_cas/              loose-to-pack lifecycle
z2_pack_compression/        packed-record compression
z2_repack/                  partly-dead pack reclamation
z2_bulk_transfer/           v1 per-chunk vs v2 bulk transport
z2_cas_batch/               grouped loose-CAS publication
z2_history_prune/           history pruning
z2_storage_tiers/           hot/warm/cold tier lifecycle
z2_consumer/                browser/checkpoint application scenarios
z2_locality/                locality and coalesced reads
z2_direct_pack/             direct pack ingest
```

The historical directory names are retained as source provenance; they are not
public feature version numbers.

## Running harnesses

Build ZeroS3 from the repository root:

```sh
go build -o /tmp/zeros3-bin zeros3.go
```

Run one harness:

```sh
cd testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m2
```

Harnesses generally create temporary stores, start real ZeroS3 processes, drive
them through an independent client, and exit nonzero on failure.

## Runner groups

List the available groups:

```sh
cd testing-harnesses
go run ./runner -list
```

Examples:

```sh
go run ./runner -group s3,sync
go run ./runner -group client,apps
go run ./runner -group all -bin /tmp/zeros3-bin
```

Group membership is defined by the runner, so use `-list` instead of relying on
old commands from historical result files.

Broad categories include:

| Group | Intent |
|---|---|
| `static` | source/test map checks |
| `s3` | S3 protocol and independent-client interoperability |
| `sync` | native movement, repair, and structural workflows |
| `clients` | optional external client/comparison tools |
| `client` | Core Client Profile and physical invariance |
| `apps` | browser-site and checkpoint scenarios |
| `bench` | performance measurements |

## Repository validation stages

The repository-level orchestrator is:

```sh
scripts/validate.sh list
```

It separates ordinary correctness gates from focused lifecycle, scale, and
benchmark stages. Use focused stages while developing a change and broader
validation when shared protocol/storage paths are affected.

## Third-party dependencies

The direct harness dependencies currently include:

```text
github.com/aws/aws-sdk-go-v2             v1.45.1
github.com/aws/aws-sdk-go-v2/config      v1.33.1
github.com/aws/aws-sdk-go-v2/credentials v1.20.1
github.com/aws/aws-sdk-go-v2/service/s3  v1.109.1
github.com/aws/smithy-go                 v1.28.1
github.com/minio/minio-go/v7             v7.3.0
```

See this module's `go.mod` and `go.sum` for the exact transitive graph. These
packages are not required to build or run ZeroS3 itself.

Some harnesses also use optional external programs such as rclone, the AWS CLI,
s3rver, or strace when available.

## Benchmark evidence

Historical benchmark transcripts live under `results/` and retain their
original milestone filenames for provenance.

Current public performance summaries and reporting conventions are in
[../docs/BENCHMARKS.md](../docs/BENCHMARKS.md).

## Adding a harness

Extend an existing harness when the new assertion belongs to the same external
contract. Create a new one when the behavior needs a distinct process lifecycle,
fixture, or measurement setup.

A harness should:

- drive a real ZeroS3 process;
- use deterministic fixtures where practical;
- clean up temporary state;
- fail with a nonzero exit;
- avoid private implementation hooks;
- add evidence not already covered well by a root unit test.

If the harness represents a public compatibility claim, update the relevant
public contract in the same change.
