# ZeroS3 testing harnesses

External, black-box validation for ZeroS3. Each harness starts a real
`zeros3` binary as a subprocess and drives it over plain HTTP with an
independent S3 client — the AWS SDK for Go v2, minio-go, `rclone`, or ZeroS3's own
CLI — so the wire protocol is checked against genuine client behavior
rather than hand-written doubles.

## Dependency boundary

```
testing-harnesses  --HTTP / subprocess-->  zeros3
```

This directory is a separate Go module with its own `go.mod` and pinned
third-party dependencies. The root module stays dependency-free:
`zeros3.go` never imports anything from here, the root `go.mod` has no
`require` directives, and `go test ./...` at the repository root does not
descend into this module.

## Internal tests vs. external tests

| | `zeros3_test.go` (root) | `testing-harnesses/` |
|---|---|---|
| Access | White-box: calls `Store`, handlers, and crash-injection hooks directly | Black-box: HTTP, CLI, and process lifecycle only |
| Clients | `net/http` + hand-rolled SigV4 | AWS SDK v2, minio-go, `rclone`, `s3rver` (comparison) |
| Strength | Invariants, durability, fault injection, races | Interoperability, restart/signal behavior, end-to-end workflows |
| Dependencies | stdlib only | Pinned third-party modules |

Neither replaces the other; a change to the wire protocol or storage
path should pass both.

## Layout

```
runner/                    builds zeros3 once and runs selected harness groups
harness/
  m2/                      canonical AWS SDK workflow, restart persistence
  m3/{copy,range,dedup}/   CopyObject, range GET, dedup evidence via `stats -json`
  m5a/presign/             presigned GET/PUT
  m5b/multipart/           multipart lifecycle across a process restart
  m5d/pagination/          ListParts / ListMultipartUploads paging
  m6/sync, m6c/dirsync/    delta sync of a file and of a directory tree
  m8a/remote_delta/        remote-to-remote replication
  m8b/repair/              peer-assisted repair
  m8c/namespace_replication/
  m8d/fork/, m8e/snapshot/ copy-on-write forks and snapshots
  m8f/conditional/         If-Match / If-None-Match semantics
  z2_streaming_put/        >256 MiB streamed PutObject and GET: bounded server RSS, byte-exact readback
  z2_aws_chunked/          minio-go aws-chunked SigV4 uploads via a recording/tampering proxy
  z2_packed_cas/           `zeros3 compact`: loose -> packed -> mixed byte-exact readback, file counts, throughput, open time, RSS
  m8g/introspection/       diff / inspect / stats
  m8h/parallel_transfer/   bounded parallel transfer (plus bench/)
  m8_baseline/             throughput baseline
  p1/env_and_shutdown/     env credentials, SIGINT/SIGTERM shutdown
  p2/list_multipart_uploads/
  rclone/                  unpatched rclone lifecycle and 1 GiB multipart
  package-killer/          one frozen SDK suite, run against zeros3 and s3rver
  toc_check/               verifies the source/test map line numbers
results/                   recorded runs (historical evidence; commands and
                           commit references reflect the repository at the time)
```

## Prerequisites

- Go 1.27.x (the pinned toolchain also builds this module).
- Network access on first run to fetch the pinned modules.
- Optional: `rclone` (recorded runs used v1.75.0) and `s3rver` (3.7.1,
  installed into a scratch directory outside the repository).

## Build and run one harness

```sh
# from the repository root
go build -o /tmp/zeros3-bin zeros3.go

cd testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m2
```

Every harness starts the binary on an ephemeral port with a temporary
store, prints `PASS`/`FAIL` per assertion, and exits non-zero on any
failure. Fixtures are generated deterministically at run time. The
benchmark harnesses (`m8_baseline`, `m8h/*`) take `-bin` instead of
`ZEROS3_BIN` and print `RECORD` lines.

## Run broader validation

```sh
cd testing-harnesses
go run ./runner -list                      # groups and members
go run ./runner -group s3,sync             # build ../zeros3.go, run groups
go run ./runner -group all -bin /tmp/zeros3-bin

RCLONE_BIN=$(which rclone) go run ./runner -group clients
```

| Group | Contents |
|---|---|
| `static` | source-map / test-map checker for `zeros3.go` and `zeros3_test.go` |
| `s3` | SDK interoperability: CRUD, copy, range, presign, multipart, pagination, conditionals, env/shutdown, large streamed PUT/GET, aws-chunked uploads, packed CAS compaction |
| `sync` | sync, replication, repair, fork, snapshot, introspection |
| `clients` | `rclone` (needs `RCLONE_BIN`) and Package Killer (needs `S3RVER_BIN`); skipped when unset |
| `bench` | throughput benchmarks; not part of `all` |

`all` is `static,s3,sync,clients`. Package Killer additionally needs
`s3rver` installed in a scratch directory; see
`results/PACKAGE_KILLER_RESULTS.md` for the exact procedure.

## External dependencies

Pinned in `go.mod` / `go.sum`, used only by the harnesses:

```
github.com/aws/aws-sdk-go-v2           v1.45.1
github.com/aws/aws-sdk-go-v2/config    v1.33.1
github.com/aws/aws-sdk-go-v2/credentials v1.20.1
github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1
github.com/aws/smithy-go               v1.28.1
github.com/minio/minio-go/v7           v7.3.0
```

plus their transitive modules (minio-go is the independent aws-chunked client). Nothing here is vendored,
compiled into `zeros3`, or required at ZeroS3 runtime.
