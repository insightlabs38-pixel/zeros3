# Standard Library Craft

ZeroS3's production core is one Go 1.27 source file with no third-party
runtime dependencies. The zero-dependency constraint is useful only because
the standard-library implementation still covers the difficult parts of the
system: SigV4, S3 wire semantics, content-defined chunking, durable metadata,
packed storage, concurrency, TLS, and outbound ZeroS3 transfers.

This document records where the standard library was sufficient, where ZeroS3
had to supply missing algorithms or storage semantics, and the complete direct
import surface.

## At a glance

- Go 1.27
- no `require` directives in the root `go.mod`
- no root `go.sum` or vendor tree
- no CGO requirement
- no subprocess dependency from `zeros3.go`
- one production implementation file
- reproducible dependency checks in
  [`deps-proof.txt`](./deps-proof.txt)

## Major substitutions

| Common dependency | ZeroS3 uses | ZeroS3 supplies |
|---|---|---|
| AWS SigV4 signer/verifier | `crypto/hmac`, `crypto/sha256`, `crypto/subtle`, `net/http`, `net/url` | canonical request handling, credential scope, signing-key derivation, presigning, verification |
| S3 server framework | `net/http`, `encoding/xml` | routing, S3 operation semantics, error mapping, pagination, multipart rules |
| CDC library | byte operations + `crypto/sha256` | deterministic Gear/FastCDC-style chunking |
| UUID package | Go 1.27 `uuid` | nothing beyond normal use |
| CAS library | `crypto/sha256`, `encoding/hex` | chunk identity, layout, verified reads, manifest integration |
| embedded metadata database | `os`, `io`, `encoding/binary`, `hash/crc32` | append-only visibility journal and replay |
| transactional durability layer | `os.File.Sync`, `os.Rename` | explicit durable-publication ordering and directory fsync |
| checksum helper | `hash/crc32`, `encoding/base64` | S3 CRC32 validation and CRC32C metadata framing |
| S3 ETag helper | `crypto/md5` | single-part and multipart ETag rules |
| file-locking package | `syscall.Flock` | shared server ownership and exclusive maintenance locking |
| worker-pool / errgroup | `context`, `sync.WaitGroup`, buffered channels | bounded transfer concurrency and cancellation |
| graceful-shutdown library | `os/signal`, `context`, `http.Server.Shutdown` | shutdown policy and second-signal behavior |
| TLS sidecar/library | `net/http` TLS serving | CLI integration and configuration checks |
| filesystem sync/walk helper | `filepath.WalkDir`, `io/fs` | directory mapping and safe file-type handling |
| HTTP client / request library | `net/http`, `net/url` | signed ZeroS3 client requests, pooling, transfer bounds |

The important distinction is that the standard library often provides the
primitive, not the storage or protocol behavior. ZeroS3 still owns the
canonicalization rules, persistent formats, failure boundaries, and S3
semantics that define the product.

## Where ZeroS3 does substantial work

### SigV4

The standard library provides the cryptographic and HTTP primitives, but not
AWS Signature Version 4.

ZeroS3 implements:

- raw request-target preservation;
- canonical URI and query encoding;
- canonical header construction;
- credential-scope validation;
- signing-key derivation;
- header-authenticated requests;
- presigned URLs;
- signed aws-chunked payload verification.

The server avoids routing that would normalize the path before signature
verification. This is necessary for keys containing encoded slashes, repeated
slashes, plus signs, percent escapes, or other path forms where normalization
would change the signed bytes.

Signature comparison uses `crypto/subtle` rather than ordinary string
comparison.

### S3 wire semantics

`net/http` and `encoding/xml` provide transport and serialization, while
ZeroS3 supplies the S3 behavior layered on top.

ZeroS3 implements the supported operation contract directly, including:

- bucket/object routing;
- S3-shaped XML errors;
- ListObjectsV2 pagination;
- CopyObject semantics;
- conditional reads/writes;
- multipart lifecycle and ETag rules;
- SigV4 request handling;
- range responses;
- encoded object-key listings.

The supported surface is documented in [S3_COMPAT.md](./S3_COMPAT.md).

### Content-defined chunking

The Go standard library has no content-defined chunker.

ZeroS3 implements deterministic Gear/FastCDC-style boundaries with:

```text
minimum: 16 KiB
target:  64 KiB
maximum: 256 KiB
```

The deterministic Gear table is derived from a fixed SHA-256 seed. Identical
input under CDC v1 therefore produces identical logical chunk boundaries.

This is the mechanism behind edit-locality reuse, global CAS deduplication,
delta transfer, and thin snapshot deltas.

### Namespace journal

Instead of embedding a key-value database, ZeroS3 keeps the current namespace
and multipart state in an append-only checksummed visibility journal.

The implementation defines:

- binary frame layouts;
- CRC32C validation;
- sequence ordering;
- replay on open;
- persistent multipart records;
- fail-closed handling of malformed or unsupported records.

Current namespace state is rebuilt into memory from that journal.

### Durable publication

ZeroS3 does not receive crash semantics from a database transaction layer. It
uses a small publication pattern repeatedly:

```text
write staging bytes
fsync file
rename into final location
fsync containing directory
publish higher-level reference
```

The same pattern appears in chunk, pack, manifest, snapshot, and format
publication where applicable. The visibility journal's durable commit is the
logical acknowledgement boundary for namespace mutations.

Grouped loose-CAS publication and immutable direct-pack publication optimize
this pattern without changing its ordering guarantees.

### Concurrency

Bounded parallelism uses standard synchronization primitives rather than a
worker-pool package.

The implementation combines:

- contexts for cancellation;
- wait groups for completion;
- buffered channels as counting semaphores;
- explicit store/namespace/pack locks for shared state.

The same approach is used by sync, replication, repair, grouped CAS
publication, and other bounded parallel paths.

## Scope boundaries made easier by the standard library

Several areas need very little custom machinery because the standard library
already supplies the hard part.

### UUIDv7

Go 1.27 provides UUIDv7 directly through `uuid.NewV7`, so ZeroS3 does not
carry a UUID dependency.

### TLS

ZeroS3 uses Go's HTTP/TLS server. It adds certificate/key flags and startup
validation, but does not implement ACME, certificate renewal, mTLS, or custom
PKI.

### Graceful shutdown

`os/signal` and `http.Server.Shutdown` provide the core mechanism. ZeroS3
adds the policy: SIGINT/SIGTERM starts a bounded graceful drain, while a second
signal forces immediate termination.

### Filesystem walking

`filepath.WalkDir` provides deterministic directory traversal. ZeroS3 uses
`DirEntry.Type` so directory sync can reject symlinks and special files
without following them.

## Direct standard-library import surface

The direct production import block is part of the zero-dependency contract.

| Package | Role |
|---|---|
| `bufio` | bounded buffered parsing for aws-chunked and binary streams |
| `bytes` | bounded in-memory buffers, comparisons, readers |
| `cmp` | ordered comparisons |
| `compress/flate` | per-record DEFLATE for packs and bundles |
| `context` | cancellation and bounded shutdown/transfer work |
| `crypto/hmac` | SigV4 HMAC |
| `crypto/md5` | S3 ETags and Content-MD5 |
| `crypto/sha256` | CAS identity, object/bundle hashes, SigV4 |
| `crypto/subtle` | constant-time signature comparison |
| `encoding/base64` | request checksum/digest headers |
| `encoding/binary` | journal, pack, bulk, snapshot, and bundle framing |
| `encoding/hex` | digest/signature encoding |
| `encoding/json` | manifests, format metadata, native protocol, CLI JSON |
| `encoding/xml` | S3 XML |
| `errors` | error classification |
| `flag` | CLI parsing |
| `fmt` | formatting |
| `hash` | streaming hash interfaces |
| `hash/crc32` | CRC32 and CRC32C |
| `io` | streaming and bounded reader/writer composition |
| `io/fs` | filesystem traversal types |
| `iter` | iterator helpers |
| `log` | server diagnostics |
| `maps` | map helpers |
| `math` | bounded numeric calculations |
| `math/bits` | compact index/prefix calculations |
| `net` | address handling |
| `net/http` | S3 server and native HTTP clients |
| `net/url` | query construction and URI handling |
| `os` | files, environment, process state |
| `os/signal` | graceful shutdown |
| `path/filepath` | store paths and traversal |
| `slices` | sorting/search helpers |
| `sort` | deterministic ordering |
| `strconv` | numeric parsing |
| `strings` | keys, headers, canonicalization, CLI parsing |
| `sync` | locks, wait groups, condition variables |
| `sync/atomic` | small concurrent counters/state |
| `syscall` | Linux advisory flock |
| `time` | timestamps and retention cutoffs |
| `unicode/utf8` | UTF-8/XML-safe key handling |
| `uuid` | UUIDv7 identifiers |

The production core does not import `os/exec`, a C binding, or a third-party
module. Third-party SDKs exist only in the independent
`testing-harnesses/` module.

## Dependency proof

[`deps-proof.txt`](./deps-proof.txt) records the inspected source/module
identity and the Go 1.27 commands used to reproduce the dependency checks.

The proof covers:

- the empty root dependency graph;
- the complete direct import surface;
- a CGO-disabled build;
- the reproducible-build check.

External interoperability dependencies remain isolated in
`testing-harnesses/`.