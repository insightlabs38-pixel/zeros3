# M8A — full release-regression validation freeze

Every harness that was green at the M7 release freeze, run unmodified
against the M8A candidate build, plus the new M8A harness. This is the
M8 Phase 3 "full M7 release regression" record.

## Build under test

```
zeros3 commit:    1fb79a1e1549933634f52eaeedc6a5b66a696b1e
zeros3 branch:    claude/zeros3-m8a-baseline-inh9hk
Go toolchain:     go1.27.0 linux/amd64
Build command:    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:   efc0cb0956b39fc05fd11eb42422298f6d0aa776d5a70e41c94c87aad180e3fc
```

## Consolidated result

**438 passed, 0 failed, 8 informational, 1 documented known limitation**
across every harness in this repository (404 pre-existing + 34 new M8A).
**Zero regressions from the M7 baseline** — every pre-existing harness's
pass/fail/info count is byte-for-byte identical to `results/
M7_RELEASE_CANDIDATE_RESULTS.md`.

| Harness | M7 baseline | M8A candidate | Regression? |
|---|---|---|---|
| `harness/m2` | 41/41 | **41/41** | none |
| `harness/m3/copy` | 46/46 | **46/46** | none |
| `harness/m3/range` | 27/27 | **27/27** | none |
| `harness/m3/dedup` | 7/7 | **7/7** | none |
| `harness/m5a/presign` | 47/47 | **47/47** | none |
| `harness/m5b/multipart` | 43/43 | **43/43** | none |
| `harness/m5d/pagination` | 43/43 | **43/43** | none |
| `harness/m6/sync` | 33/0/2 | **33/0/2** | none |
| `harness/m6c/dirsync` | 69/0/2 | **69/0/2** | none |
| `harness/package-killer` | 14/14 both, GO | **14/14 both, GO** | none |
| `harness/rclone` | 20/0/1 | **20/0/1** | none |
| `harness/m8a/remote_delta` (new) | n/a | **34/0/4** | n/a (new) |

Sum of pre-existing pass counts: 41+46+27+7+47+43+43+33+69+14+14+20 =
**404**, identical to M7. Plus the new M8A harness's 34 = **438** total.

## Reproduction

```sh
# 1. build the exact M8A candidate
cd /path/to/zeros3 && git checkout 1fb79a1e1549933634f52eaeedc6a5b66a696b1e
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-m8a-bin zeros3.go

# 2. from this repository -- every harness unmodified from M7
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m2
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m3/copy
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m3/range
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m3/dedup
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m5a/presign
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m5b/multipart
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m5d/pagination
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m6/sync
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m6c/dirsync
S3RVER_BIN=/path/to/node_modules/.bin/s3rver ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/package-killer   # s3rver 3.7.1
ZEROS3_BIN=/tmp/zeros3-m8a-bin RCLONE_BIN=/path/to/rclone go run ./harness/rclone   # rclone v1.75.0

# 3. the new M8A harness
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m8a/remote_delta
```

No harness was modified to produce this result — every pre-existing
harness ran exactly as committed at the M7 freeze.

## AWS interoperability (R3)

Covered directly by the harnesses above, unmodified:
`CreateBucket`/`PutObject`/`GetObject`/`HeadObject`/`ListObjectsV2`
(`m2`), `CopyObject`/Range (`m3/copy`, `m3/range`), multipart + pagination
(`m5b`, `m5d`), presigned URLs (`m5a`), and both the local (`m6`/`m6c`)
and remote (`m8a`) delta paths — all pass identically. No M8A extension
interfered with ordinary S3 route handling: `m2`/`m3`/`m5*` (which never
touch `/_zeros3/...`) are byte-for-byte identical to M7, and
`TestReplicate_NewEndpointsRejectUnauthenticatedRequests`/
`TestReplicate_UnknownExtensionPathStillNotBucketParsed` (internal
suite) directly confirm the new routes don't shadow or misroute ordinary
S3 requests.

## M6/M6C regression (R4)

`harness/m6/sync` (33/0/2) and `harness/m6c/dirsync` (69/0/2) are
byte-for-byte identical to their M7 results — single-file local delta
sync, resume, conflict, the M7-fixed weird-key escaping regression, and
recursive directory sync/aggregate stats/non-destructive behavior are
all unaffected by M8A's client-side refactor (extracting `putSyncChunk`
out of `uploadMissingSyncChunks` for reuse by `replicateObject`; see
`zeros3.go` section 15b). Internally, `TestReplicate_
OrdinaryS3AndM6SyncUnaffected` exercises the exact refactored code path
directly as a targeted regression check on top of the full M6/M6C suite.

## Durability / verify / GC regression (R5)

`harness/m8a/remote_delta`'s Phase 4 runs `zeros3 verify -deep` against
a destination store populated entirely via real replication and gets a
clean result (0 missing/corrupt/invalid, 0 unreachable) — a replicated
object is indistinguishable from an ordinary one to verify, exactly as
M8A's persistent-format-impact claim (NONE) requires. Internally,
restart/replay is proven for both source and destination
(`TestReplicate_DestinationServerRestartBetweenAttempts`,
`TestReplicate_SourceServerRestartBetweenDescriptorAndChunkFetch`) and
GC reachability follows directly from "no new persistent format" (GC's
one reachability scan, `computeReachability`, already treats every
manifest/chunk uniformly regardless of which ingestion path produced
it — M8A adds no new root/reference type for it to special-case).

## Reproducibility (R6)

Two independent builds, from two separately-copied source trees at two
different absolute paths, `CGO_ENABLED=0 go build -trimpath
-buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go`, on
`go1.27.0 linux/amd64`:

```
SHA-256 (copy A): efc0cb0956b39fc05fd11eb42422298f6d0aa776d5a70e41c94c87aad180e3fc
SHA-256 (copy B): efc0cb0956b39fc05fd11eb42422298f6d0aa776d5a70e41c94c87aad180e3fc
```

Byte-identical. Reproducibility holds for the M8A candidate.

## Dependency proof (R7)

Re-audited directly against source (not merely by regenerating the
existing artifact): `grep -c "^require" go.mod` = 0; no `go.sum`; no
`vendor/`; `zeros3.go`'s `go list -deps .` package list is **byte-for-
byte identical** to the m7-gold proof (M8A added zero new imports — the
one new client-side use, `net/url.Values`, is the same `net/url`
package section 8/15b already import). No `golang.org/x/...` import; no
`os/exec` anywhere in `zeros3.go`. Sole implementation source file
remains `zeros3.go`; sole first-party test file remains
`zeros3_test.go`. See `deps-proof.txt` in the `zeros3` repository.

## Docs audit (R8)

`README.md` gained a concise `replicate` example and a "Known
limitations" bullet scoping M8A to one object per invocation; the
reproducible-build SHA-256 was updated to the M8A candidate's.
`S3_COMPAT.md` gained the two new endpoints in its extensions table and
an explicit "this is proprietary ZeroS3 functionality, not S3 API
compatibility" paragraph, matching the same framing M6/M6C already use.
`STDLIB.md` gained one new substitution-table-adjacent entry (`net/url.
Values` for the descriptor endpoint's query encoding) and one sentence
extending the existing "no retry/backoff library" tradeoff note to cover
`replicate`'s identical CAS-native resume story. `STATUS.md` gained a
full "M8A" section (implementation, hostile review, evidence, known
limitations) following the M7 section, without editing any prior
milestone's own section. Nothing in the core M1-M7 product story was
displaced or buried.
