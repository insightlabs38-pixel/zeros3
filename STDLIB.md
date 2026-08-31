# Standard Library Craft

ZeroS3 ships as one file, `zeros3.go`, built entirely on the Go 1.27
standard library. This log picks the 15 strongest cases where a normal
project would reach for a third-party package, and explains exactly what
stdlib primitive replaced it and what ZeroS3 had to build itself to fill
the gap — then closes with the full breadth of stdlib packages actually
exercised. Every line below points at real, shipped code; nothing here
is aspirational.

## At a glance

- Zero third-party runtime dependencies
- Go 1.27, zero `require` in `go.mod`
- One implementation source file (`zeros3.go`)
- Mechanical proof: [`deps-proof.txt`](./deps-proof.txt)

## 15 meaningful substitutions

### 1. AWS SigV4 signer/verifier

**Normally:** an AWS SDK's own request signer (e.g. `aws/signer/v4`), or
a third-party SigV4 library.

**Instead:** `crypto/hmac`, `crypto/sha256`, `crypto/subtle`,
`encoding/hex`, `net/http`, `net/url`.

**What ZeroS3 had to implement:** the entire canonicalization algorithm —
raw request-target preservation (deliberately *not* running through
`http.ServeMux`, which would clean `//`/`.`/`..` before the signature is
checked), query-parameter encode-then-sort, header canonicalization,
credential-scope validation, and signing-key derivation (date → region →
service → `aws4_request`) — for both Authorization-header and
query-string (presigned URL) auth, sharing one verifier core
(`sigv4VerifyCore`) so there is exactly one signing implementation used
by both the server's verification path and the `zeros3 presign` CLI's
generation path. Signature comparison uses `crypto/subtle`'s
constant-time compare rather than `==`, so a mismatching signature can't
leak timing information about how much of it matched.

**Why it matters:** the signer is the security boundary of the whole
protocol surface; authoring it directly, rather than trusting an
imported implementation, is what let ZeroS3 also correctly handle the
raw-path edge cases (`%2F`, `+` vs `%20`, encoded slashes) that a
path-normalizing router would silently break.

### 2. S3 protocol/server framework

**Normally:** an S3-compatible server framework or toolkit (what a
project like `s3rver` provides) plus an XML request/response codec.

**Instead:** a hand-rolled `http.Handler` (no `http.ServeMux`) for
method/subresource dispatch, `encoding/xml` for wire-format encoding.

**What ZeroS3 had to implement:** S3's own request/response semantics —
bucket/object routing by hand, `ListObjectsV2` pagination/continuation-
token contract, S3-shaped error XML envelopes, `CopyObject` metadata-
directive rules, and multipart's part-ordering/ETag-formula rules.
`encoding/xml`'s marshaling/escaping is a complete fit for the wire
shapes once those semantics are defined; the semantics themselves are
authored, not provided.

**Why it matters:** this is the difference between "imports an S3
server" and "implements the S3 protocol" — the latter is what makes the
zero-dependency claim mean something for the project's actual core
functionality, not just its plumbing.

### 3. Content-defined chunking library

**Normally:** a chunking library (e.g. `restic/chunker`) implementing
Gear-hash or FastCDC-style content-defined boundaries.

**Instead:** a hand-rolled streaming Gear-hash chunker over `[]byte`,
with its deterministic gear table seeded via `crypto/sha256`.

**What ZeroS3 had to implement:** the entire chunking algorithm — a
streaming rolling fingerprint, FastCDC-style min/target/max boundary
normalization (16KiB min / 64KiB target / 256KiB max), and a
deterministic gear table so the same bytes always chunk the same way.
No stdlib package does content-defined chunking; stdlib only supplies
the hash primitive used to derive the table.

**Why it matters:** this is the single algorithm the rest of the
architecture's dedup/delta-transfer story depends on — an edit anywhere
in a file perturbs only the chunks near that edit, which is what makes
localized-edit reuse and delta sync work at all.

### 4. UUID library

**Normally:** a third-party UUID generator (e.g. `google/uuid`).

**Instead:** the Go 1.27 standard library's own `uuid` package
(`uuid.NewV7`).

**What ZeroS3 had to implement:** nothing. Manifest, object-version, and
snapshot identifiers use the stdlib generator directly.

**Why it matters:** the cleanest substitution in the project, and a
direct demonstration of what a genuinely current Go toolchain removes
from the dependency list — this identifier type used to require an
import in every Go project that needed it.

### 5. Content-addressed storage / integrity library

**Normally:** a CAS library, or an object-storage SDK's own
content-integrity layer.

**Instead:** `crypto/sha256`, `encoding/hex`.

**What ZeroS3 had to implement:** the CAS layout itself — chunk/manifest
naming by content hash, re-verification on every read (`casWrite`/
`casRead`), and the layering of chunk-level SHA-256 identity, whole-
object SHA-256 (checked by `verify -deep`), and S3's own MD5-based ETag
as three deliberately distinct concepts that never stand in for one
another.

**Why it matters:** content addressing is what makes deduplication,
zero-payload `CopyObject`, zero-payload forks, and zero-payload snapshot
restore all the same mechanism instead of four separate features.

### 6. Embedded metadata database

**Normally:** an embedded key-value/document database (e.g. `bbolt`,
`badger`) for tracking what buckets/objects currently exist.

**Instead:** `os`, `io`, `encoding/binary`, `hash/crc32`.

**What ZeroS3 had to implement:** an append-only, CRC32C-framed,
replay-based "visibility journal" that is the sole source of truth for
the bucket/object namespace — every frame's binary layout, its
checksum, and the replay-on-open logic that reconstructs the in-memory
namespace from it at store-open time.

**Why it matters:** this is a genuine engineering tradeoff, not a free
substitution — no transactions, indexes, or range queries came for
free; replay-on-open and an in-memory map were hand-built to get them
back, in exchange for an exact, auditable durability contract (see
entry 7).

### 7. Transaction/durability layer

**Normally:** a transactional storage engine's own durability guarantees
(the ACID layer a database like `bbolt`/`badger`/SQLite provides).

**Instead:** `os.File.Sync`, atomic `os.Rename`, and explicit directory
fsync.

**What ZeroS3 had to implement:** the durable-publish sequence used by
every write in the system — stage bytes in a temp file, `fsync` the
file, atomically rename it into place, then `fsync` the containing
directory (the only portable way to make a rename itself durable on
Linux) — applied consistently to CAS chunks, manifests, and journal
frames, with the journal's own `fsync` as the exact acknowledgment
threshold: a mutation is acknowledged over HTTP only after its journal
frame's sync call returns.

**Why it matters:** this is the entire crash-safety argument for the
project in one mechanism — "acknowledged mutation ⇒ durable" is a claim
that only holds because this exact ordering is enforced everywhere, not
asserted after the fact.

### 8. CRC/checksum library

**Normally:** a checksum/integrity utility package.

**Instead:** `hash/crc32` (Castagnoli table), `encoding/base64`.

**What ZeroS3 had to implement:** two independent uses sharing one
primitive — ordinary `x-amz-checksum-crc32` request-integrity validation
over the logical payload, and CRC32C framing for visibility-journal
frames (recovery/torn-frame detection, not authentication) and snapshot
descriptors.

**Why it matters:** keeping this checksum concept strictly separate from
SigV4's payload hash, S3's ETag, and CAS's SHA-256 identity (six
distinct hash/checksum concepts in the codebase, none used as a stand-in
for another) is what avoids an entire class of "which hash actually
proved what" bugs.

### 9. S3-compatible ETag helper

**Normally:** an S3-compatibility shim that computes AWS-style ETags.

**Instead:** `crypto/md5`.

**What ZeroS3 had to implement:** both S3 ETag formulas correctly kept
distinct — the ordinary single-part rule (plain MD5 of the object body)
and the genuinely different multipart rule (`MD5` of the concatenated
per-part MD5s, plus a `-N` suffix) — computed from the manifest, never
conflated with the object's own SHA-256 or the CAS chunk identity.

**Why it matters:** MD5 here is explicitly not a security use (marked as
such in the source) — it exists solely to match a documented AWS
compatibility contract, a distinction worth making explicit given MD5's
reputation elsewhere.

### 10. Advisory file locking library

**Normally:** a cross-platform file-locking package (e.g.
`gofrs/flock`).

**Instead:** `syscall` (`syscall.Flock`, `LOCK_EX`/`LOCK_SH`/`LOCK_NB`/
`LOCK_UN`).

**What ZeroS3 had to implement:** the exclusive-ownership contract `gc
-apply` needs — a shared lock for an ordinary store user (`zeros3
serve`), and an exclusive, non-blocking lock GC must win before it may
delete anything, so a GC run can never race a live server's writes.

**Why it matters:** this is a correctness-critical lock, not a
convenience one — it's the only thing standing between "safe offline
garbage collection" and "GC racing a running server against live data."

### 11. Bounded worker pool / errgroup

**Normally:** a worker-pool or task-group library (e.g.
`golang.org/x/sync/errgroup` or `golang.org/x/sync/semaphore` — both
themselves out of scope for a zero-dependency build).

**Instead:** `context`, `sync.WaitGroup`, and a plain buffered channel
used as a counting semaphore.

**What ZeroS3 had to implement:** `runTransferWorkers`, the bounded-
concurrency chunk-transfer pool shared by `sync`, `replicate`, and
`repair` — parallel independent chunk transfers with one-failure
cancellation, while preserving each operation's single serialized
commit at the end, so concurrency only ever applies to the part of the
pipeline that's safe to parallelize.

**Why it matters:** `-workers` took 256 MiB of missing payload from
4.36 MiB/s to 35.70 MiB/s (8.18x) at 16 workers in one benchmark
environment — a real throughput result — while the commit path stayed
exactly as serialized and safe as the sequential version.

### 12. Graceful-shutdown / process-lifecycle library

**Normally:** a process-supervision or graceful-restart library.

**Instead:** `os/signal` (`signal.Notify`/`signal.Stop`),
`context.WithTimeout`, and `net/http`'s own `http.Server.Shutdown`.

**What ZeroS3 had to implement:** the small decision logic around those
primitives — `SIGINT`/`SIGTERM` stop the listener immediately and drain
in-flight requests within a bounded grace period; a second signal during
drain forces an immediate exit instead of waiting out the rest of the
grace period; a normal shutdown is never logged as a fatal error.

**Why it matters:** `signal.Notify` and `http.Server.Shutdown` already
do most of the real work — this is a case where the stdlib primitives
were sufficient and the honest accounting is that very little needed to
be authored, which is itself worth stating plainly rather than
overselling.

### 13. TLS termination

**Normally:** a reverse-proxy TLS sidecar, or a certificate-automation
library (e.g. an ACME client).

**Instead:** `net/http`'s `http.Server.ListenAndServeTLS`, backed by
`crypto/tls` internally.

**What ZeroS3 had to implement:** the smallest possible integration
point — `-tls-cert`/`-tls-key` flags, with no ACME, no certificate
generation or renewal, no mTLS, and no custom `tls.Config`; neither flag
means plain HTTP, and exactly one of the two is a startup error, with
`InsecureSkipVerify` never set in the default path.

**Why it matters:** this is a deliberate scope boundary as much as a
substitution — `net/http` supplies a production-grade TLS server for
free, and the honest limitation (no cert automation) is stated rather
than hidden.

### 14. Recursive filesystem walking/sync helper

**Normally:** a directory-walking or file-sync utility library.

**Instead:** `path/filepath`'s `WalkDir`, `io/fs.DirEntry`.

**What ZeroS3 had to implement:** directory-sync's traversal and
prefix-mapping logic on top of `WalkDir`'s already-deterministic,
lexically-sorted-per-directory traversal order (which meant no separate
sort step was needed), plus symlink/special-file detection straight from
the `readdir`-derived `DirEntry.Type()` — never a followed `Stat` — so a
symlink is identified and skipped without ever being dereferenced.

**Why it matters:** the safety property here (never following a
symlink) depends on using the right stdlib entry point (`DirEntry`, not
`os.Stat`); the substitution and the security property are the same
decision.

### 15. Outbound HTTP client (for sync/replicate/repair)

**Normally:** an HTTP client library with retry/transport tuning (e.g.
`resty`, `req`), plus a second request-signing implementation for
client-side calls.

**Instead:** `net/http`'s own `http.Client`/`http.NewRequest`,
`net/http.Transport` for connection pooling, `net/url`'s `url.Values`
for query construction.

**What ZeroS3 had to implement:** the first genuine outbound HTTP
*client* role in the codebase (every other CLI verb operates directly on
a local store) — `signSigV4Request` reuses the exact same
canonicalization primitives the server verifies with, so there is still
only one signing implementation, now used in both directions; a tuned
`http.Transport` (bounded `MaxConnsPerHost`/`MaxIdleConns`) sized off
the same worker-count bound as entry 11, rather than an unbounded
default.

**Why it matters:** reusing the server's own SigV4 primitives for
client-side signing means there is exactly one place in the codebase
that can get request canonicalization wrong, not two.

## Additional standard-library surface

Packages used throughout the implementation that support the substitutions
above without deserving their own essay:

| Package | Role |
|---|---|
| `bytes` | in-memory request/response buffers |
| `encoding/base64` | checksum/digest header encoding |
| `encoding/binary` | journal frame and manifest binary layout |
| `encoding/hex` | chunk/manifest content-address formatting |
| `encoding/json` | manifests, `FORMAT.json`, journal payloads, `-json` CLI output, `/_zeros3/v1/...` wire bodies |
| `errors` | error classification and wrapping |
| `flag` | CLI subcommand parsing (`serve`, `stats`, `verify`, `sync`, `replicate`, `repair`, `fork`, `snapshot`, `presign`, `versions`, `restore`, `gc`, `doctor`, `diff`, `inspect`) |
| `fmt` | formatting, CLI/error output |
| `io` / `io/fs` | streaming reads/writes, buffered chunk hashing |
| `log` | request/error diagnostics to stderr |
| `net` | `net.SplitHostPort` for virtual-host `Host` parsing |
| `sort` | deterministic listing/pagination order |
| `strconv` | numeric header fields (`Content-Length`, `Content-Range`) |
| `strings` | S3 key/header/path processing |
| `time` | SigV4 timestamps, journal/manifest metadata |

`testing`, `net/http/httptest`, and (in `zeros3_test.go` only)
`crypto/tls`, `crypto/x509`, `os/exec`, and `math/rand` support the test
suite's real-process, real-socket, and adversarial-TLS coverage — no
third-party test framework is used anywhere in `zeros3_test.go`.

## Mechanical dependency proof

External interoperability validation is performed out-of-process and is
not part of the ZeroS3 module or binary. The generated, mechanical
zero-dependency evidence (`go.mod`'s empty `require` block, `go list
-deps .`'s full linked-package set, and the non-stdlib-import check)
lives in [`deps-proof.txt`](./deps-proof.txt).
