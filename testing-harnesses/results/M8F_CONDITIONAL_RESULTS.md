# M8F — atomic S3 conditional operations external validation

External, real-server, real-AWS-SDK-v2 black-box proof of ZeroS3's
conditional-write primitive (`PutObject` + `If-None-Match: *`/`If-Match`),
conditional reads (`GetObject`/`HeadObject` + `If-Match`/`If-None-Match`),
and `CopyObject`'s source preconditions
(`x-amz-copy-source-if-match`/`-if-none-match`). Every request in every
phase goes through the real AWS SDK for Go v2's own `PutObjectInput`/
`GetObjectInput`/`HeadObjectInput`/`CopyObjectInput` conditional fields
(present in the pinned SDK version) against one real `zeros3 serve`
subprocess per phase — never a hand-crafted header or an internal ZeroS3
API call.

## Build under test

```
zeros3 commit:     a353c29 (branch claude/s3-conditional-operations-m8f-1u7bdm,
                   on top of the exact merged M8E baseline
                   feccb186eb2cbcf91a06609f69ca0cd59cc4dba4)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    79766e9cbe4a2c8f4f56ffafd7fad182d78d4123598634cdddc3d9245c050c3b
```

Reproducible build confirmed: two independent builds from the same
working tree produced byte-identical output.

## Harness

`harness/m8f/conditional/main.go`. See its own header comment for the
full black-box rationale. 7 phases:

1. **Create-only** — `PutObject` + `If-None-Match: *` against an absent
   key succeeds; a repeat against the now-existing key fails
   `412 PreconditionFailed`; the first bytes remain intact.
2. **Compare-and-swap update** — PUT v1, HEAD ETag A; PUT v2 with
   `If-Match: A` succeeds, HEAD ETag B; PUT v3 with the now-stale
   `If-Match: A` fails `412`; GET still returns v2.
3. **Concurrent create-only** — 12 real, concurrent AWS SDK clients race
   `If-None-Match: *` with distinct bodies against one absent key: exactly
   1 success, 11 `412`s; GET returns exactly the winning body; a real
   process restart does not change the winner.
4. **Concurrent CAS update** — 12 real, concurrent clients race
   `If-Match: A` (one shared starting ETag) with distinct bodies: exactly
   1 success, 11 `412`s (no lost update); restart-stable winner.
5. **GC of a failed write's speculative CAS payload** — a 2 MiB body is
   CDC-chunked and published to CAS before a deliberately doomed
   `If-Match` rejects the commit; the surviving object is unchanged;
   `zeros3 gc` (dry run) reports the failed write's chunks as unreachable;
   `zeros3 gc -apply` reclaims them; `zeros3 verify -deep` and a restart
   both confirm full health afterward.
6. **Conditional GET/HEAD (M8F-B)** — matching `If-Match` succeeds with
   exact bytes; mismatching `If-Match` fails `412` (GET and HEAD);
   matching `If-None-Match` surfaces as the SDK's own error for a non-2xx
   response, carrying HTTP status `304` (`GetObject` only models 2xx
   responses, so a 304 cannot come back as a plain success — confirmed by
   direct inspection of the wire status via `smithy.APIError`/
   `smithyhttp.ResponseError`, not assumed); mismatching `If-None-Match`
   serves normally; a failed `If-Match` combined with `Range` still
   reports `412`, never `206`/`416`; a missing key still reports
   `404`/`NoSuchKey` regardless of `If-Match`.
7. **CopyObject source predicates (M8F-C)** — matching
   `CopySourceIfMatch` succeeds and clones exact bytes; mismatching
   `CopySourceIfMatch` fails `412` and creates no destination object; a
   `CopySourceIfNoneMatch` that matches the source's current ETag also
   fails `412`.

## Result

```
===== SUMMARY: 83 passed, 0 failed, 1 informational =====
```

The 1 informational line reports Phase 2's observed ETag A, for
readability of the CAS-update trace.

## Reproduction

```sh
# 1. build zeros3
cd /path/to/zeros3 && git checkout a353c29
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-bin .

# 2. run the harness
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m8f/conditional
```

## Full pre-existing external harness regression

Every pre-existing harness (M2 through M8E, rclone, Package Killer) was
re-run, unmodified, against this exact build:

| Harness | Result |
|---|---|
| `m2` | 41 passed, 0 failed |
| `m3/copy` | 46 passed, 0 failed |
| `m3/dedup` | 7 passed, 0 failed |
| `m3/range` | 27 passed, 0 failed |
| `m5a/presign` | 47 passed, 0 failed |
| `m5b/multipart` | 43 passed, 0 failed |
| `m5d/pagination` | 43 passed, 0 failed |
| `m6/sync` | 33 passed, 0 failed, 2 informational |
| `m6c/dirsync` | 69 passed, 0 failed, 2 informational |
| `m8a/remote_delta` | 34 passed, 0 failed, 4 informational |
| `m8b/repair` | 133 passed, 0 failed, 1 informational |
| `m8c/namespace_replication` | 111 passed, 0 failed, 2 informational |
| `m8d/fork` | 146 passed, 0 failed, 3 informational |
| `m8e/snapshot` | 151 passed, 0 failed, 3 informational |
| `m8f/conditional` (new) | 83 passed, 0 failed, 1 informational |
| `rclone` | 20 passed, 0 failed, 1 documented known limitation |
| `package-killer` | ZeroS3 14/14, s3rver 14/14 — GO |

**Totals: 1062 passed, 0 failed, 18 informational, 1 documented known
limitation, across 17 harnesses — zero regressions anywhere.**

See `zeros3`'s own `STATUS.md` "M8F" section for the complete engineering
record, hostile-review findings, and internal test evidence.
