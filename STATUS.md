# ZeroS3 — Status

Milestone-by-milestone status, newest first. M1-M6C are all complete
and accepted; M7 (release hardening/submission freeze) is the current
pass -- see its section immediately below.

## M7 — Release hardening, proof, and submission freeze

**Goal:** turn the M1-M6C feature-complete system into a submission-
ready, judge-ready, reproducible, defensible release candidate. Not a
feature milestone -- no new S3 APIs, sync flags, or capabilities were
added; every change below is either a demonstrated-bug fix, a
verification/reproducibility/documentation improvement, or a
regression test.

### Baseline (before any M7 change)

- Branch: `claude/zeros3-m7-release-o6wnzj`, based on `main` at
  `cb87c850c47be8592a7e4152c96586d715a9234a`.
- `go test ./...`: 338 top-level tests, 527 total PASS (subtests
  included) across 3 repeated runs, 0 FAIL, 0 SKIP, no flakiness found
  (the two `time.Sleep` calls in `zeros3_test.go` are a documented
  clock-advance for a timestamp-uniqueness assertion and a bounded
  subprocess-readiness poll, not undocumented timing dependence).
- `go test -race ./...`: clean (0 races, ~45s).
- `go vet ./...`: clean. `gofmt -l .`: clean.
- Implementation: 7674 lines (`zeros3.go`). Tests: 12979 lines
  (`zeros3_test.go`).
- `go.mod`: `module zeros3`, `go 1.27.0`, zero `require` directives.
- All 11 external `zeros3-testing` harnesses green at their previously
  recorded baselines (see "External validation freeze" below).

### Hostile regression review (M7A2)

A hostile-reviewer pass across storage/durability, namespace/
concurrency, protocol/S3, and M6/M6C found **one real, confirmed bug**,
fixed with a minimal patch and three new regression tests (no unrelated
refactor):

- **`zeros3 sync` client built request URLs by raw string concatenation
  of the object key** (`headSyncDestination`/`doPlainPutFallback`), with
  no percent-encoding. A key containing a literal `%` (e.g.
  `"50% off.txt"`) made `url.Parse` (inside `http.NewRequest`) reject
  the request outright -- the file could never be synced. A key
  containing `#` (e.g. `"a#b.txt"`) was silently truncated at the URL
  fragment delimiter, so `doPlainPutFallback` reported
  `FellBackToPlainPut=true` (success) while actually writing the
  content under a **different, truncated key** -- a genuine false-
  success/silent-misroute defect. A key containing `?` broke
  `headSyncDestination`'s HEAD probe the same way, causing every
  re-sync of an unchanged destination to hit a false
  `PreconditionFailed` safe-mode conflict.
  Fixed by building the path through `url.URL{Path: ...}.EscapedPath()`
  (`syncObjectPath`) instead of raw concatenation, matching how the
  server already expects/decodes percent-encoded paths. Regression
  tests: `TestSync_KeyWithPercentCharacterFallsBackCorrectly`,
  `TestSync_KeyWithHashCharacterDoesNotMisroute`,
  `TestSync_KeyWithQuestionMarkResyncsCleanly` -- each independently
  verified to fail against the pre-fix code and pass against the fix.
  No test anywhere previously exercised a sync key containing `%`/`#`/
  `?`; this was a genuine coverage gap, not an already-safe case.
- Every other category (journal replay/torn-tail handling, GC vs.
  retained versions/multipart, SigV4 raw-path/canonical-URI handling,
  CopyObject/PUT/DELETE concurrency via `commitObjectRootChecked`'s
  check-function pattern) was reviewed and found already correctly
  handled, consistent with the adversarial-review passes recorded in
  each milestone's own section above/below.

### Readability/cross-reference audit (M7E)

No broad refactor (existing code already reads as a deliberately
organized single-file program). Fixed real navigation defects:

- Section 15 ("Optional ZeroS3 Delta Sync (M6)") physically sits before
  the CLI and Lifecycle/main sections but was numbered **17**, as if it
  came last. Renumbered to match physical file order (15/15b/15c sync,
  16 CLI, 17 lifecycle/main) and fixed every internal cross-reference
  (8 places across `zeros3.go`/`STDLIB.md`/`STATUS.md`, several of
  which pointed at a "section 16a/16b/16c" that never existed -- the
  code they described is in section 13b).
- Removed one confusing self-referential comment ("per the 'future
  milestone' this comment used to point at -- this is that milestone").
- Reordered `STDLIB.md`'s substitution table (row 11 physically sat
  between rows 8 and 9).

### Documentation reconciliation (M7F-H)

Three stale claims found, each contradicted by content already shipped
and documented elsewhere in the same repository -- fixed to match
actual, current behavior:

- README's "Known limitations" claimed "no versioning, restore, or
  garbage collection" and "`ListParts`/`ListMultipartUploads` have no
  pagination" -- both false since M5-C and M5-D shipped. Replaced with
  the real current limitations (ZeroS3's internal versioning is not the
  AWS Versioning API, `gc -apply` requires exclusive offline access,
  `ListMultipartUploads` lacks `delimiter`/`prefix`, `zeros3 sync` is
  sequential with no `--delete` for directory sync).
- `DEMO.md`'s closing line claimed "no versioning, no multipart" --
  contradicted by M5-B/M5-C.
- `STDLIB.md`'s "no retry/backoff library" tradeoff justified itself
  with "ZeroS3 has no client-side sync/transfer feature in this
  milestone" -- contradicted by M6's own HTTP-client entry a few lines
  above in the same file.

`S3_COMPAT.md` was independently audited and found already accurate
(M6/M6C already correctly labeled as ZeroS3 extensions, not S3 APIs;
every compatibility deviation already itemized).

### Demo rehearsal (M7J)

`DEMO.md` was rewritten and rehearsed end-to-end, twice, from a clean
demo store, against a real build. Two real problems found during
rehearsal (not merely reviewed on paper) and fixed:

- The dedup section pointed at the whole-store `stats` view, which
  reads ~50% `dedup_reduction` for *any* two-object store regardless of
  edit size (an artifact of "roughly one duplicate's worth of savings"
  across the whole store) -- not the CDC edit-locality story the demo
  is supposed to show. Switched to `-bucket`/`-key`-scoped `stats`,
  which correctly shows ~99.9% of the edited object's own bytes as
  shared/reused for this fixture shape.
- Added a `zeros3 sync` (M6) section per this milestone's demo
  structure, using a *separate* fixture pair from the dedup section's:
  reusing the same fixtures would make even the *first* sync show 100%
  reuse (the store already holds those bytes from the dedup section's
  ordinary PUTs), demonstrating nothing about delta sync's actual
  value.

Both full rehearsals reproduced identical hashes, reuse percentages,
and reproducible-build output. Total content: ~4:30, comfortably under
5 minutes with buffer. See `DEMO.md` for the exact script.

### Reproducible build proof (M7C)

`scripts/reproducible_build.sh` (unchanged from M6C): two independent
builds, from two separately-copied source trees at two different
absolute paths, `CGO_ENABLED=0 go build -trimpath -buildvcs=false
-ldflags="-buildid=" -o zeros3 zeros3.go`, on `go1.27.0 linux/amd64`.
Run repeatedly across this pass (baseline, mid-pass, and final freeze);
copy A and copy B hashes matched byte-for-byte every time. Final-freeze
hash recorded in the M7 completion report / tag notes.

### Dependency proof (M7D)

`deps-proof.txt` independently regenerated and diffed against the
committed copy: identical. Verified directly against source, not just
by regenerating the existing artifact: `grep -c "^require" go.mod` = 0;
`zeros3.go`'s only imports are Go 1.27 standard-library packages plus
`uuid` (confirmed, by inspecting the actual `go1.27.0` `GOROOT/src/
uuid/` package doc, to be a genuine stdlib package -- `import "uuid"`,
`package uuid`, RFC 9562, `uuid.NewV7`); no `golang.org/x/...` import;
no `os/exec`/subprocess anywhere in `zeros3.go`.

### External validation freeze (M7I)

Every harness in the separate `zeros3-testing` repository run against
this exact release-candidate build (commit `381db3c0cd32108437d3951917f78d23c0d625ad`
on `claude/zeros3-m7-release-o6wnzj`): **404 passed, 0 failed, 4
informational, 1 documented known limitation** -- zero regressions from
any previously recorded baseline. Full per-harness table and
reproduction commands: `zeros3-testing/results/
M7_RELEASE_CANDIDATE_RESULTS.md`. One harness fix was needed, entirely
on the `zeros3-testing` side (not a `zeros3` regression): the T1
`harness/rclone` probe still expected rclone's default upload to be
*rejected*, a pre-M5-B assumption that M5-B's own already-shipped
`UNSIGNED-PAYLOAD` support made stale, producing 4 false failures on a
target that isn't actually broken; fixed to expect (and clean up after)
the now-correctly-succeeding upload, improving that harness's own
recorded result from 19/0/2 to 20/0/1.

### Claim/evidence matrix (M7K)

| Claim | Evidence |
|---|---|
| Zero dependencies | `go.mod`/import audit above; `deps-proof.txt` |
| Single implementation file | `zeros3.go` (7687 lines after M7) + organizer-approved `zeros3_test.go` (13122 lines); `ls *.go` |
| Reproducible build | `scripts/reproducible_build.sh`, matching SHA-256 across repeated runs this pass |
| AWS SDK interoperability | `zeros3-testing/results/M7_RELEASE_CANDIDATE_RESULTS.md` (M2: 41/41; presign: 47/47) |
| Crash/restart durability | internal crash-injection tests (this file's "Durability contract" section) + external restart-persistence checks in every `zeros3-testing` harness |
| CDC edit locality | `TestDedup_EditedObjectReuseBeatsFixedSizeChunking` (internal, 96.6%); `zeros3-testing/harness/m3/dedup` (external, 97.5%); `DEMO.md` section 3 (rehearsed, ~99.9% for its fixture shape) |
| Dedup savings | `zeros3 stats`/`stats -json`, measured not hardcoded (see "Dedup and stats" in README.md) |
| CopyObject payload reuse | `TestCopyObject_SameBucketZeroNewCASChunkBytes` (internal); `zeros3-testing/harness/m3/copy` (46/46 external) |
| Integrity detection | `zeros3 verify -deep`; `zeros3-testing` restart/persistence checks |
| Multipart/pagination | `zeros3-testing/harness/m5b/multipart` (43/43); `harness/m5d/pagination` (43/43) |
| Delta sync | `zeros3-testing/harness/m6/sync` (33/33 + 2 informational) |
| Recursive directory sync | `zeros3-testing/harness/m6c/dirsync` (69/69 + 2 informational) |
| Package Killer (vs. `s3rver`) | `zeros3-testing/harness/package-killer`, 14/14 both targets, GO |

Every major README/demo claim maps to evidence above; nothing was
softened or removed, since no claim was found stronger than the tests
prove (the two stale README/DEMO.md claims found were *understating*
shipped capability, not overstating it -- see "Documentation
reconciliation" above).

### Limitations audit (M7L)

Reviewed every limitation documented in README.md/`S3_COMPAT.md`
against this milestone's fine-to-ship / must-fix-before-release
criteria. **No must-fix limitation was found** -- every item is a
deliberate, already-documented scope boundary (single-writer-process,
no IAM/STS/KMS/ACL, sequential sync uploads, no filesystem-snapshot-
based mutation detection, non-destructive directory sync with no
`--delete`, bounded in-memory request buffering, no
`STREAMING-AWS4-HMAC-SHA256-PAYLOAD` support, `ListMultipartUploads`
without `delimiter`/`prefix`). None of these cause data corruption,
silent overwrite against documented semantics, false success, broken
restart, invalid auth behavior, inconsistent persistent state, or
misleading stats -- the one defect that would have qualified (the sync
URL-encoding bug above) was found and fixed, not merely documented
around.

### Persistent-format impact

**None.** Every M7 change was either a comment/documentation edit or a
client-side URL-construction fix (`syncObjectPath`); no journal record
type, manifest field, or on-disk layout changed. A pre-M7 store opens
identically under the M7 release candidate.

### M8/future work noted, not implemented

No M8 work was started (out of scope for M7 by explicit instruction).
No "would be a cool extra feature" ideas were implemented during this
pass; none were substantial enough to warrant a written punch list
beyond what's already in "Known limitations" (README.md) and the
"Optional / later-tier behavior" section of `S3_COMPAT.md`.

## M6 — Optional delta transfer (`zeros3 sync`)

**M6A, M6B, and M6C are all COMPLETE.** M6C (recursive directory sync)
was picked up in a later pass, once M6A/M6B were confirmed "boringly
green" on their own documented baseline (49 tests, race-clean, restart-
proven) — exactly the precondition this milestone's own spec required
before starting it. See "M6C — recursive directory sync" below for the
full writeup; M6A/M6B's own description below is otherwise unchanged from
that baseline.

M6 adds exactly one new capability: `zeros3 sync LOCAL_FILE
s3://bucket/key`, a bounded, resumable, conflict-safe way to *ingest* an
object using far less network transfer than a naive full upload when the
destination (or the store in general, via ordinary CDC/CAS dedup) already
holds most of the relevant bytes. **It is not a second storage engine.**
A synced object is produced by, and is in every way indistinguishable
from, the exact same CDC v1 → SHA-256 CAS → immutable manifest →
visibility-journal commit pipeline every ordinary `PutObject` already
uses (`buildManifestV1FromRefs`, `publishManifest`,
`commitObjectRootChecked` — zero new persistent-format types). Ordinary
S3 `GetObject`, `HeadObject`, `verify -deep`, versions/GC/multipart/
current-root logic, and a full server restart all treat a synced object
exactly like any other object, because after commit it *is* any other
object — no ZeroS3-sync-specific state exists anywhere on disk.

### Protocol (version 1, reserved `/_zeros3/` namespace)

Four endpoints, all authenticated by the *exact same* SigV4 header
verification (`srv.authenticate`) every ordinary S3 request already goes
through — there is no separate auth story for sync, and no fake/overloaded
AWS operation name or path shape:

- `GET  /_zeros3/v1/info` — capability discovery: `{protocol, cdc, hash,
  delta_sync, max_hashes_per_batch, max_batch_bytes, max_chunk_bytes}`
  as JSON. Version 1 is deliberately small, per `SYNC_PROTOCOL.md`.
- `POST /_zeros3/v1/negotiate` — bounded missing-chunk query: up to 1024
  `{sha256,length}` descriptors (`maxSyncBatchDescriptors`) and up to
  256KiB of encoded request body (`maxSyncBatchBytes`), both hard,
  independent ceilings; a pure read (`os.Stat` only, never
  `casRead`/`casWrite`), so it never mutates authoritative state and is
  always safe to retry, re-run, or run speculatively. Response: the
  normalized, de-duplicated list of digests not present in CAS.
- `PUT  /_zeros3/v1/chunks/<sha256-hex>` — idempotent chunk upload: the
  server independently computes SHA-256 of the body it actually received
  (never trusting the URL's declared digest) and rejects a mismatch;
  publication goes through the exact same `casWrite` an ordinary PUT's
  chunking loop uses, so a retried upload of an already-published chunk
  is a no-op by construction — there is no separate idempotency
  mechanism to build.
- `POST /_zeros3/v1/commit` — atomic ordinary object commit: the client's
  complete ordered chunk list (occurrences, not de-duplicated — a chunk
  that legitimately repeats within one file repeats in its manifest,
  exactly as ordinary `PutObject` chunking would produce) plus ordinary
  object metadata and an optional safe-mode conflict precondition. Only
  bounded by the same `maxRequestBodySize` (256MiB) every other request
  body already is — unlike `/negotiate`, a commit's chunk list
  legitimately grows with object size (a multi-GiB file has far more than
  1024 chunks), so it is never capped at the negotiation batch size.

`/negotiate` and chunk upload are store-wide (CAS is not per-bucket), so
neither needs a bucket/key; `/commit` carries them in its JSON body, not
the URL — none of the four endpoints is reachable via path-style or
virtual-hosted-style bucket/key resolution, and `ServeHTTP` checks for the
`/_zeros3/` prefix before any of that parsing runs.

### CDC reuse (A2)

There is exactly one CDC v1 implementation in this binary
(`newCDCChunker`/`findCDCBoundary`, section 3), used identically by
ordinary `PutObject` (`chunkData`), multipart completion
(`chunkAndStoreStream`), and the sync client's local scan
(`scanLocalFileForSync`). `TestSync_CDCEquivalenceWithOrdinaryPut` proves
directly, on the same 2.5MB random fixture, that the sync client's scan
and `chunkData`'s ordinary-PUT path produce byte-identical boundaries,
lengths, and SHA-256 identities, chunk-by-chunk.

### Missing-chunk negotiation (A4)

The sync client (`negotiateSyncMissing`) de-duplicates the local file's
chunk occurrences to unique digests, splits them into batches no larger
than the server's declared `max_hashes_per_batch` (clamped to this
build's own 1024-descriptor ceiling, so a misbehaving/compromised server
declaring an oversized batch can't induce an oversized client request),
and issues one `/negotiate` call per batch.
`TestSyncNegotiate_BatchSizeBoundary` proves exactly 1023 and 1024
descriptors succeed in one request and 1025 is rejected outright (not
silently truncated); `TestSyncNegotiate_MultiBatchViaClient` proves the
real client function correctly splits 2500 synthetic descriptors across
multiple `/negotiate` calls with no loss or duplication at the batch
seam. Zero/one/all/mixed-missing and duplicated-digest cases are each
directly tested (`TestSyncNegotiate_{ZeroMissing,OneMissingAmongMany,
AllMissing,SomeMissing,DuplicatedDigestsReportedOnce}`), as are invalid
digest encoding, invalid/out-of-bounds length, unknown protocol/CDC/hash
version (501), an oversized encoded request (400), and malformed JSON
(400). `TestSyncNegotiate_NeverMutatesStore` proves negotiation leaves
on-disk chunk/manifest byte totals unchanged.

### Chunk publication validation (A5)

`handleSyncChunkUpload` never trusts a client-declared digest merely
because it arrived via the sync protocol: it independently hashes the
body it received and rejects a mismatch (`TestSyncChunkUpload_
DigestMismatchRejected` proves a mismatched chunk is never published
under the claimed digest), bounds the body to the frozen CDC v1 envelope
maximum (`TestSyncChunkUpload_OversizedBodyRejected`), and publishes
through the exact same `casWrite` primitive ordinary PUT chunking uses —
so idempotent retry (`TestSyncChunkUpload_IdempotentRetry`) is a property
of CAS itself, not a bespoke mechanism.

### Atomic commit / manifest+journal reuse (A6)

`handleSyncCommit` builds a manifest via `buildManifestV1FromRefs` (the
same primitive `CompleteMultipartUpload`'s stream-completion path already
uses) and publishes it via `publishManifest` +
`commitObjectRootChecked` — the same primitives ordinary `PutObject`/
`CopyObject` already use (see "Manifest/journal reuse and the
precondition refactor" below). Every referenced chunk is read back via
`casRead`, which independently re-verifies content against its own digest
before anything is published — so a missing chunk
(`TestSyncCommit_MissingChunkRejected`), a declared-vs-actual length
mismatch (`TestSyncCommit_WrongLengthRejected`), or a chunk corrupted on
disk since upload (`TestSyncCommit_CorruptChunkRejected`, which flips a
byte directly in the chunk file to prove `casRead`'s own hash
re-verification — not a second, duplicated integrity check — is what
catches it) is rejected before publication, and the destination key is
proven to remain absent (404) after each rejection. The same read pass
streams each chunk's already-verified bytes through two running hashes
(whole-object SHA-256, single-part-style MD5 ETag) one chunk at a time —
bounded memory regardless of object size, matching `chunkAndStoreStream`'s
own discipline. Unknown protocol/CDC/hash version (501), malformed JSON
(400), and a missing bucket/key or unknown bucket (400/404) are each
directly tested.

**`TestSync_CriticalAcceptanceProof`** is M6A's central architectural
claim, proven directly: sync a 3MB file, `GetObject` returns exact bytes,
`HeadObject` reports the correct `Content-Length`, then the store is
closed and reopened as a brand-new `Store`/`Server` (a real restart, no
state threaded across it) and `GetObject` on the new server still returns
exact bytes, and `store.Verify(true)` (deep verify) reports no issues.

### Manifest/journal reuse and the precondition refactor

`commitObjectRoot` (used by `PutObject`/`CopyObject`) is now a thin
wrapper around a new `commitObjectRootChecked(..., check func(cur
*objectEntry, exists bool) error)`, extracted with `check == nil`
producing byte-for-byte the same behavior as the prior `commitObjectRoot`
body (no existing caller's behavior changed — the full pre-M6 regression
suite is unchanged and still green). `check`, when non-nil, runs *inside*
the exact same locked critical section as the commit itself, immediately
after re-confirming bucket existence and reading the current root, before
anything is written — there is no unlock between the precondition check
and the commit, so a concurrent writer can never slip past a
now-stale precondition (the TOCTOU an unlock-then-relock version of this
would have). This is the one new mechanism M6 needed in the pre-existing
commit path, and it is composition (one new parameter, no new commit
path), not a second implementation.

### Resume semantics (B1)

No durable server-side sync-session state exists anywhere; resume is a
direct consequence of CAS's own content-addressed durability.
`TestSync_ResumeAfterPartialPriorUpload` primes one chunk via a direct
upload (standing in for "the client died after uploading only this
much"), then runs a fresh `syncFile` end-to-end and proves it uploads
strictly less than the full logical size and still produces the correct
object. `TestSync_ServerRestartAfterPartialUploadThenResume` does the
same but closes and reopens the `Store`/`Server` in between (a real
process-equivalent restart) before the resumed run. `TestSyncChunkUpload_
IdempotentRetry` and `TestSync_RepeatedFullSyncOfIdenticalContentUploads
Nothing` cover repeated chunk upload and a fully-resumed re-sync
uploading zero bytes. `TestSync_RepeatedCommitFailsCleanlyRather
ThanDuplicating` establishes the exact commit-retry guarantee: a literal
retry of an already-succeeded `expect_absent=true` commit is rejected
with 412 (not silently accepted, and never a second object version) —
this is the documented, deliberately conservative answer to "commit
retry must not create inconsistent duplicate object versions": a retry
whose precondition has gone stale because its *own* prior attempt already
landed fails safely rather than being magically recognized as
"the same request"; the caller's correct response (matching
`TestSync_ConflictRetryAfterConflictSucceeds`) is to re-run sync, which
re-observes reality via a fresh HEAD and proceeds correctly.

### Conflict semantics (B2)

Every sync captures the destination's current identity via an *ordinary*
S3 HEAD (never a ZeroS3-specific call) before negotiating: absent, or the
current ETag. Commit carries that as `expect_absent`/`expected_etag`,
checked atomically inside `commitObjectRootChecked`'s locked section (see
above). `TestSync_Conflict{AbsentDestinationStaysAbsentUntilCommit,
UnchangedDestinationCommitsCleanly,ConcurrentPUTDuringSyncCausesCommit
Conflict,TwoConcurrentSyncsToSameKeySecondFails,RetryAfterConflict
Succeeds}` cover: destination absent until commit; an unchanged
destination (including the demonstration fixture's own "sync a modified
file back to the same key" pattern) commits cleanly; an ordinary
concurrent `PutObject` between a sync's HEAD and its commit is detected
and rejected (412); two syncs racing to the same never-before-existing
key resolve deterministically (first commit wins, second is rejected,
destination holds exactly the winner's bytes); and re-running sync after
a conflict succeeds, because it re-observes the real destination fresh.
This is real S3's own `PreconditionFailed` (412) semantics, not an
invented status code.

### Local mutation semantics (B3)

A practical, honestly-documented, stdlib-only guarantee: `syncFile`
records the local file's size and modification time (`os.Stat`) before
scanning, and compares against a fresh `os.Stat` taken immediately before
commit (plus an independent re-hash of each chunk re-read for upload,
which doubles as an earlier, cheaper signal). If either differs, the
operation aborts (`errSyncLocalMutation`) without ever sending a commit
request. **What this does not, and cannot, claim:** it is not a
filesystem snapshot — an in-place rewrite that happens to preserve both
size and modification time exactly (to the granularity the filesystem
records) is not detected. `TestSync_LocalMutationDuringOperationAborts`
proves the abort path via a test-only hook
(`syncTestHookBeforeMutationCheck`, mirroring the pre-existing
`testHook`/`fireTestHook` pattern used for crash-injection tests) that
deterministically mutates the file between upload and the check, and
proves the destination key remains absent afterward;
`TestSync_UnmodifiedFileCommitsNormally` proves the check never
false-positives on an untouched file.

### Non-ZeroS3 endpoint behavior (B5)

`syncFile` performs capability discovery first, always; a
proprietary `/negotiate`/chunk-upload/`/commit` request is never sent
without a successful, protocol-compatible discovery response. On
discovery failure (network error, non-200, unparseable body, or an
unrecognized protocol/cdc/hash), it falls back to one ordinary,
whole-file `PutObject` — the simplest, safest, least-surprising choice,
and the only one every stdlib-only client can always perform.
`TestSync_NonZeroS3Endpoint_FallsBackToPlainPut` runs a fake HTTP server
(404 on every `/_zeros3/*` path, an ordinary 200 on `PUT`, auth
unchecked — standing in for a real non-ZeroS3 S3-compatible endpoint) and
proves both that the fallback PUT carries the exact file bytes *and* that
no `/negotiate`/chunk-upload/`/commit` request was ever sent to it.

### Transfer statistics (A7)

Entirely operation-local (`syncStats`); nothing is persisted, and no
persistent journal/manifest field changed to support this. Reported:
logical bytes scanned, total chunks, chunks reused (occurrences already
present in CAS at negotiation time), missing chunk occurrences, unique
chunks actually uploaded, uploaded payload bytes, bytes avoided, and a
reuse percentage — printed in the planning doc's own example style via
`printSyncStats`/`humanBytes`. `TestSyncStats_FirstSyncAllNew` proves a
brand-new object reports zero reuse; `TestSyncStats_
ResyncIdenticalContentToNewKeyIsFullyReused` proves re-syncing identical
content to a fresh key reports 100% reuse and zero uploaded bytes.

**`TestSync_M6ADemonstrationFixture`** is the required M6A fixture: an
8MB random file is synced, then a small (4KiB) insertion is made at its
midpoint and the mutated file is synced to a new key. Because CDC v1 only
reshuffles chunk boundaries local to an edit, the second sync's own
observed reuse is asserted to be **≥80%** of logical bytes (an actual
run logs **99.0%** reuse — 80.5KiB uploaded of 7.63MiB logical — see
"External interoperability" below for an independent confirmation, via a
real AWS SDK client and a separately-sized fixture, of the same effect)
and strictly less than the
first (full) sync's upload, with an ordinary `GetObject` on the mutated
key proving exact byte correctness. Both the 80% assertion and the
"strictly less than the first sync" assertion are real, checked
conditions — not merely logged numbers — matching the "do not choose
unrealistic data solely to manufacture a misleading percentage" and
"don't weaken expected behavior just to turn a red test green" instructions.

### M6C — recursive directory sync

**COMPLETE.** `zeros3 sync LOCAL_DIRECTORY s3://bucket/prefix/` recursively
maps regular files below a local directory into S3 object keys below a
destination prefix. It is deliberately **orchestration, not a second
transfer engine**: `syncDirectory` walks the source tree once
(`discoverSyncFiles`), derives one destination key per eligible file
(`joinSyncKey`), and calls the *unmodified* M6A/M6B `syncFile` primitive
for each file in turn — the same function every existing `TestSync_*`
test above already exercises. No new CDC loop, negotiation client, upload
loop, commit path, conflict mechanism, or mutation-detection mechanism
was written; every one of those is inherited per-file, verbatim, from the
code M6A/M6B already proved. The entire new surface is: two small parsing
helpers (`parseS3DirURI`, `joinSyncKey`), one directory-walk function
(`discoverSyncFiles`), one orchestration loop (`syncDirectory`), two
result types (`dirSyncResult`, `dirSyncFailure`/`dirSyncSkip`), a summary
printer (`printDirSyncSummary`), and a small `os.Stat`-based dispatch
added to `runSync` — all in `zeros3.go`'s existing "17c" subsection, ~275
added lines. `zeros3.go` remains the sole implementation source file.

**Traversal and key mapping (C1/C2).** `discoverSyncFiles` uses
`filepath.WalkDir`, whose documented behavior — each directory's entries
are read once and sorted by name before descending — is itself the
deterministic, lexically-stable order C1 requires; no separate sort step
was needed (`TestDirSync_DeterministicOrdering` proves the same tree
produces the same order across repeated calls, checked against the exact
expected sequence, not just "consistent with itself"). A relative path is
converted to `/`-separated form (`filepath.ToSlash`) and joined to the
(already leading/trailing-`/`-trimmed) destination prefix by
`joinSyncKey`, the single place a key is ever assembled, so a bare prefix
never produces a leading or doubled `/`
(`TestDirSync_PrefixNormalization` proves `s3://bucket/`,
bare `s3://bucket`, `s3://bucket/prefix`, and `s3://bucket/prefix/` all
parse to the exact bucket/prefix C2's own examples specify, both at the
parser level and end-to-end against a real store). Nested directories,
same-basename-in-different-directories, spaces, Unicode, dot-prefixed
("hidden") files, deep nesting, an empty directory, and distinct-but-
similar-looking filenames (differing only in case or trailing whitespace)
are each directly tested
(`TestDirSync_{NestedDirectories,SameBasenameInDifferentDirectories,
SpacesInPaths,UnicodePaths,HiddenDotPrefixedFiles,EmptyDirectory,
DuplicateLookingPathsRemainDistinct}`); a source directory argument with
and without a trailing local path separator is proven to map identically
(`TestDirSync_SourcePathTrailingSeparator`). Two distinct local files can
never collide on one destination key by construction — each file's
relative path is unique within the walked tree, and `joinSyncKey` is a
pure, injective function of (fixed prefix, that relative path).

**Reuse of the single-file primitive (C3).** `syncDirectory`'s inner loop
is exactly:

```
for each discovered regular file:
    key := joinSyncKey(prefix, relSlash)
    cfg := baseCfg with LocalPath/Bucket/Key set
    stats, err := syncFile(cfg)   // <- the unmodified M6A/M6B function
    aggregate stats or record the failure; continue regardless
```

`syncFile` itself was not touched by this pass (its body is unchanged
from the M6A/M6B baseline above) — every file synced through a directory
therefore gets capability discovery, CDC v1, SHA-256 identities,
negotiation, CAS upload, the safe-mode commit precondition, local
mutation detection, and resume/reuse behavior with zero duplicated logic.
This is checked two ways, not merely asserted: every `TestDirSync_*`
conflict/mutation test below reuses the *existing* M6B test hook
(`syncTestHookBeforeMutationCheck`, defined with the original M6B tests)
rather than any new directory-specific hook — a second hook was never
needed because the exact same code path runs — and
`TestDirSync_AggregateStatsExactMatchSumOfPerFileStats` proves the
aggregate directory-level stats are byte-for-byte the sum of three
*independent* single-file `syncFile` calls against isolated stores,
confirming the directory path introduces no extra/omitted bytes anywhere.

**Non-destructive semantics (C4).** Directory sync only uploads/updates
files that are present locally; it never deletes, and there is no
`--delete`/mirror mode. `TestDirSync_LocalDeletionDoesNotDeleteRemoteObject`
syncs two files, deletes one locally, re-syncs, and proves via the
`zeros3-testing`-equivalent internal `GET` that the remote object for the
deleted file is byte-for-byte untouched while the remaining file still
updates normally.

**Symlink/special-file policy (C5).** A symlink is reported and skipped,
never followed: `fs.DirEntry.Type()` reflects the directory-entry's own
`Lstat`-derived type (never a followed `Stat`), so `filepath.WalkDir`
itself never descends through a symlinked directory and this code never
opens a symlinked file. `TestDirSync_SymlinkSkippedNotFollowed` proves
both a file symlink and a directory symlink (pointing at a directory
genuinely outside the source root) are skipped and reported, and that the
directory symlink's contents are never reached — the direct, concrete
answer to "can a symlink escape the source tree": no, because it is never
dereferenced at all. `TestDirSync_SpecialFileSkipped` creates a real named
pipe (`syscall.Mkfifo`) and proves it is skipped/reported and never
opened, honestly `t.Skip`ping only if the test environment doesn't support
FIFOs at all (it does, in this pass's environment).

**Partial failure (C6).** `dirSyncResult{Discovered, Synced, Skipped,
Failed, Failures, Skips, Stats}` and `printDirSyncSummary` implement the
milestone's own example format (file counts, then aggregate stats, then a
`SKIPPED:`/`FAILED:` block with `local -> s3://dest` plus a reason line,
then "directory sync completed with errors"), proven directly by
`TestDirSync_PrintSummaryFormat`. `dirSyncResult.OK()` (`Failed == 0`) is
the one place the "did this run fully succeed" verdict is computed; it is
exactly what `runSync`'s directory branch checks before `os.Exit(1)`, and
`TestDirSync_ResultOKReflectsFailureCount` proves that function's table
directly. Processing never stops or rolls back on one file's failure:
`TestDirSync_PartialFailureMultipleFailureModesSuccessfulSiblingsRetained`
runs five files where two fail (one remote conflict, one disappearing
sibling — injected via the reused M6B hook, never a timing race) and
proves the other three commit and remain retrievable, `Failed == 2`,
`OK() == false`, and the failed keys stay absent (404). Beyond the
in-process proof, `TestCLI_Sync_DirectoryAndSingleFile_Smoke` drives the
actual built `zeros3` binary as a real subprocess against a real `zeros3
serve` subprocess: a directory sync against a bucket that was never
created deterministically fails every file's commit and the *process*
exits nonzero with the expected `Files failed:`/"completed with errors"
summary on stdout — proving the real CLI exit code, not just the
in-memory `dirSyncResult` value.

**Aggregate statistics (C7).** `dirSyncResult.Stats` is a plain
`syncStats` accumulator: `LogicalBytes`, `TotalChunks`, `ChunksReused`,
`MissingChunkOccur`, `UniqueChunksUploaded`, `UploadedBytes`, and
`BytesAvoided` are each summed only from files that actually succeeded
(a failed file's zero-value stats, from the exact struct `syncFile`
already returns on error, are simply never added) — nothing here is a
persistent/lifetime counter, and no persistent format changed to support
it. `TestDirSync_AggregateStatsExactMatchSumOfPerFileStats` is the exact
proof: three files with disjoint random content are synced once as a
directory tree and once individually against three fresh, isolated
stores, and the directory run's aggregate is asserted `==` (full struct
equality) to the sum of the three isolated single-file results.

**Resume/restart (C8).** No durable directory-sync session/journal state
exists anywhere — resume is a direct consequence of the same
CAS content-addressed durability M6B already relies on.
`TestDirSync_ResumeAfterPartialPriorUploadOfOneFile` primes one chunk of
a 600KB file directly (standing in for "the client died mid-transfer"),
then runs a fresh `syncDirectory` across that file plus a second,
untouched one, and proves strictly less than the full logical size is
uploaded and both files land correct; a second, fully fresh rerun then
uploads zero bytes. `TestDirSync_ServerRestart` syncs a small tree, closes
and reopens the `Store`/`Server` (a real restart, nothing threaded
across it), and proves every object is still retrievable with the exact
right bytes and that `store.Verify(true)` reports no issues.

**Conflict behavior (C9).** Every file keeps M6B's exact safe-mode
conflict precondition; a conflict on one file never touches any other.
`TestDirSync_RemoteConflictForOneFileOthersSucceed` uses the existing M6B
test hook to write a genuinely concurrent change to one file's
destination between that file's own HEAD and its commit, and proves:
that one file fails with `errSyncRemoteConflict` (via `errors.Is`, through
`syncDirectory`'s unwrapped propagation — no second conflict type was
introduced), the two unrelated files commit and are correct, and the
conflicted object holds exactly the concurrent writer's content, never a
mix.

**Local mutation / directory-level races, honestly scoped (C10).**
Directory sync operates on the deterministic file set found by exactly
one recursive walk at the start of the run — not a filesystem snapshot. A
file that appears after its directory has already been walked is simply
not part of that run (picked up next run, per
`TestDirSync_NewlyAddedFileAfterInitialSync`); a file that disappears
after being discovered but before/during its own `syncFile` call
surfaces as an ordinary per-file failure through `syncFile`'s own
`os.Stat`/mutation-detection path — `TestDirSync_
FileDisappearsBeforeBeingProcessed` proves this deterministically (via
the reused hook, deleting a not-yet-reached sibling file while an earlier
file is mid-sync — a genuine directory-level race, injected without any
timing dependency) and proves the surviving file is unaffected and the
vanished file's key stays absent. This is exactly, and only, what C10
asks for: no filesystem snapshot layer was built, and none is claimed.

**Empty directory (C11).** `TestDirSync_EmptyDirectory` proves a `t.TempDir()`
with nothing in it succeeds, uploads nothing, and returns an all-zero
`dirSyncResult` (`Discovered/Synced/Skipped/Failed` all `0`,
`OK() == true`, sensible zero-value `Stats`).

**Path/key edge cases (C12).** Covered directly by the tests named above:
nested files, same basename in different directories, spaces, Unicode,
dot-prefixed files, prefix with/without a trailing `/`, source path
with/without a trailing local separator, deep (5-level) nesting, an empty
directory, and distinct-but-similar-looking filenames. No filename/path
support was invented beyond what the local filesystem and S3 keys
themselves already allow — this project targets Linux, so Windows-specific
filename restrictions do not apply and are not discussed further.

**CLI (`runSync`).** The single addition is one `os.Stat` on `LOCAL_PATH`:
a directory takes the new M6C branch (`parseS3DirURI` + `syncDirectory` +
`printDirSyncSummary`, exiting 1 on any per-file failure); anything else
(including a symlink to a regular file, which `os.Stat` — not `os.Lstat`
— transparently follows, since the user named it explicitly as the sync
source) takes the original, completely unmodified single-file branch. No
existing flag, default, or behavior of single-file `zeros3 sync` changed;
`TestCLI_Sync_DirectoryAndSingleFile_Smoke` runs both forms against the
same real server subprocess in one test to prove exactly that. Output is
judge-friendly by design: `printDirSyncSummary` prints file counts and
aggregate bytes up front and only ever adds one line per skip and one
short block per failure — never a per-file wall of successful-operation
noise, regardless of how many files were discovered.

**Adversarial review (M6C-specific).**

- *Can two local files map to the same S3 key accidentally?* No —
  `joinSyncKey` is a pure function of (one fixed prefix, one file's
  root-relative path), and root-relative paths are unique per file within
  one walked tree by construction; `TestDirSync_
  {SameBasenameInDifferentDirectories,DuplicateLookingPathsRemainDistinct}`
  confirm distinct paths never collapse to one object.
- *Can a weird prefix create `//` unexpectedly?* No — `parseS3DirURI`
  trims exactly the leading/trailing `/` around the prefix segment once,
  and `joinSyncKey` is the single place a key is assembled, using one `/`
  only when the prefix is non-empty; `TestDirSync_PrefixNormalization`
  checks this directly, including `strings.Contains(key, "//")`.
- *Can a symlink escape the source tree?* No — see C5 above; a symlink is
  never dereferenced by this code at all, so there is nothing to escape
  through.
- *Can one failed file incorrectly report global success?* No —
  `dirSyncResult.OK()` is `Failed == 0`, computed once, checked directly
  by `TestDirSync_ResultOKReflectsFailureCount`, and it is the only value
  `runSync` consults before exiting nonzero.
- *Can local deletion accidentally remove remote data?* No — directory
  sync has no delete/mirror code path at all;
  `TestDirSync_LocalDeletionDoesNotDeleteRemoteObject` proves the remote
  object for a deleted local file survives byte-for-byte.
- *Can one conflict stop already-safe unrelated files from being recorded
  correctly?* No — `syncDirectory`'s loop never returns early on a
  per-file error; `TestDirSync_RemoteConflictForOneFileOthersSucceed` and
  the multi-failure-mode test above both prove unrelated commits land
  correctly regardless.
- *Can an interrupted run corrupt a previously committed object?* No —
  each file's commit is the same atomic, precondition-checked
  `commitObjectRootChecked` M6A already proved race-safe; a directory run
  stopping between files leaves every already-committed object exactly as
  committed, never partially overwritten (there is no multi-file
  transaction to unwind).
- *Can rerunning after interruption upload all data unnecessarily?* No —
  `TestDirSync_ResumeAfterPartialPriorUploadOfOneFile`'s second rerun
  uploads zero bytes.
- *Can aggregate stats double-count bytes?* No — only files with
  `err == nil` from `syncFile` contribute to the sum, and `TestDirSync_
  AggregateStatsExactMatchSumOfPerFileStats` checks full struct equality
  against an independently-computed expectation.
- *Can a directory sync bypass existing conflict/mutation checks?* No —
  by construction, since it calls the unmodified `syncFile`, which is
  where every one of those checks lives; there is no second code path
  that could omit them.
- *Did any code duplicate M6A/B logic?* No new CDC/negotiation/upload/
  commit/conflict/mutation-detection code was written; see "Reuse of the
  single-file primitive" above.
- *Did directory sync alter persistent formats?* No — see "Persistent-
  format impact" below.
- *Did any external dependency leak into the submission?* No — see
  "Dependency / source-file audit" below.

**Internal test results (M6C's own additions).** 26 new
`TestDirSync_*`/`TestCLI_Sync_DirectoryAndSingleFile_Smoke` functions, all
passing; see "Internal test results" below for the combined M6/M6C totals
and confirmation that the pre-existing M6A/M6B suite is unmodified and
still green.

**External interoperability (`zeros3-testing`).** See "External
interoperability" below (combined M6/M6C section) for the dedicated M6C
harness results.

### Adversarial review

Explicitly attempted and found already handled by the design/tests above,
not merely asserted:

- **Replay/retry of a stale commit** — `TestSync_
  RepeatedCommitFailsCleanlyRatherThanDuplicating` (412, not a duplicate).
- **Malformed/duplicate descriptors, boundary batches** — `TestSyncNegotiate_
  {InvalidDigest,InvalidLength,DuplicatedDigestsReportedOnce,
  BatchSizeBoundary}`.
- **Unknown protocol/CDC/hash version** — rejected (501) at both
  `/negotiate` and `/commit` (`TestSyncNegotiate_
  UnsupportedProtocolCDCHash`, `TestSyncCommit_UnsupportedProtocolCDCHash`).
- **Unauthorized calls** — every one of the four endpoints individually
  proven to reject an unsigned request (`TestSync_
  AllEndpointsRejectUnauthenticatedRequests`).
- **A referenced chunk disappears / wrong digest / bad length / CAS
  already exists** — `TestSyncCommit_{MissingChunkRejected,
  CorruptChunkRejected,WrongLengthRejected}`;
  `TestSyncChunkUpload_IdempotentRetry` for "CAS already exists".
- **Commit fails after chunks are uploaded** — every `TestSyncCommit_*`
  rejection case leaves the previously-uploaded (now-unreachable-until-GC)
  chunks in place and the destination key untouched, matching the
  documented "interrupted uploads may leave unreachable valid CAS chunks;
  this is acceptable under the existing storage model" guarantee (M5-C
  GC already reclaims these — no new reclamation path was needed).
- **Server restart at awkward points** — `TestSync_
  ServerRestartAfterPartialUploadThenResume` (mid-transfer) and
  `TestSync_CriticalAcceptanceProof` (post-commit).
- **Normal PUT during sync, two syncs to the same key, destination
  changes before commit** — the full `TestSync_Conflict*` suite above.
- **Race detector** — `go test -race ./...` is clean with this pass's
  additions, including the concurrency-specific `TestSync_
  ConcurrentNormalPutAndSyncDifferentKeys`/`TestSync_
  TwoConcurrentSyncsToDifferentKeysBothSucceed`.
- **Source changes during scan/upload, source disappears, interrupted
  transfer, repeated run, non-ZeroS3 server** — `TestSync_
  LocalMutationDuringOperationAborts` (changes during operation; a
  disappeared source surfaces as an ordinary `os.Stat`/read error through
  the same `errSyncLocalMutation` path), the B1 resume tests (interrupted
  transfer/repeated run), `TestSync_NonZeroS3Endpoint_FallsBackToPlainPut`.

### Internal test results

`gofmt -l .`, `go vet ./...`: clean. `go test ./...`: **338 top-level test
functions / 527 `--- PASS`/`PASS:` lines (including subtests), 0
failing** — up from the M6A/M6B baseline of 312/501, entirely from M6C's
26 new `TestDirSync_*`/`TestCLI_Sync_DirectoryAndSingleFile_Smoke`
functions (`go test -run 'TestDirSync|TestCLI_Sync' -v` isolates exactly
these; the original 49 `TestSync*`/`TestSyncNegotiate*`/`TestSyncCommit*`/
`TestSyncStats*` M6A/M6B functions are unmodified and still green,
confirming M6C added no regression to the tier below it). `go test -race
./...`: clean. No pre-existing test was modified, weakened, or skipped.

### External interoperability (`zeros3-testing`)

New harness `zeros3-testing/harness/m6/sync` — unlike every earlier
harness in that repository, this one drives `zeros3 sync` itself as a
real external subprocess (`os/exec` against the built binary, never a Go
package call into ZeroS3 internals), while the AWS SDK for Go v2 (the
same pinned versions every other harness in that repo already uses) acts
as a fully independent S3 client. **33 passed, 0 failed, 2 informational**
— see `zeros3-testing/results/M6_SYNC_RESULTS.md` for the complete
transcript. It proves: an AWS-SDK-created bucket + `zeros3 sync` (a
brand-new 5.72MiB/83-chunk object) + AWS SDK `GetObject`/`HeadObject`
round-trip exact bytes; the same holds across a real process restart and
after `zeros3 verify -deep` (`result OK`); a small edit re-synced to a
new key measured **98.8%** real reuse (71.18KiB uploaded of 5.73MiB) in
that run, independently confirming the internal demonstration fixture's
own 99.0%; a `zeros3 sync` subprocess SIGKILLed ~150ms into a 19MB
transfer never leaves a partial/visible object, and a rerun resumes and
completes with exact bytes; the safe-mode conflict precondition
interoperates correctly with an object a real AWS SDK client wrote
(a deterministic case); and a best-effort timing race between `zeros3
sync` and a concurrent AWS SDK `PutObject` resolved, in that run, to the
AWS SDK write winning and `zeros3 sync` correctly detecting the conflict
and exiting non-zero — with the final object holding exactly one
writer's content, never a mix, which is the only invariant that run's
race section actually asserts (its outcome is expected to vary
run-to-run; see the results file's own caveat section). The pre-existing
`m2`/`m3/copy`/`m3/range`/`m3/dedup`/`m5a/presign`/`m5b/multipart`/
`m5d/pagination` harnesses were re-run unmodified against the same build
this M6 harness used: every one matched its previously recorded count
exactly (41/46/27/7/47/43/43), confirming no regression.

**M6C harness:** a dedicated `zeros3-testing/harness/m6c/dirsync` harness
(also a real `zeros3 sync` subprocess against a real AWS SDK for Go v2
client) covers directory-sync-specific interoperability against a
deterministic, nested, Unicode-including 5-file fixture tree. **69
passed, 0 failed, 2 informational** — see
`zeros3-testing/results/M6C_DIRSYNC_RESULTS.md` for the complete
transcript. It proves: an AWS-SDK-created bucket + `zeros3 sync ./tree
s3://.../prefix/` (initial directory sync) + AWS SDK `ListObjectsV2`
(exactly the 5 expected keys) + `GetObject` on each round-trips exact
bytes; a second sync after a small localized edit to one file plus one
brand-new file reports a real, CLI-output-parsed aggregate reuse of
**96.2%** while an unrelated unchanged file is proven untouched; deleting
a file locally and re-syncing leaves its remote object completely intact
— proven via the AWS SDK, not just internally; a real process restart
(kill + fresh `zeros3 serve` on the same store directory) followed by
`zeros3 verify -deep` (`result OK`, 66 chunks checked) leaves every one
of the 6 objects correct; and a partial-failure/conflict phase races a
real AWS SDK `PutObject` against one file's destination while an
unrelated file's own edit syncs in the same run — this run the AWS SDK
write won the race, and the directory sync process correctly reported
that one file `FAILED` with the exact safe-mode-conflict reason, printed
`directory sync completed with errors`, and exited non-zero, while the
unrelated file still committed correctly, proving partial-failure
isolation through a real external subprocess. Every pre-existing harness
(`m2`/`m3/copy`/`m3/range`/`m3/dedup`/`m5a/presign`/`m5b/multipart`/
`m5d/pagination`/`m6/sync`) was re-run unmodified against the same M6C
build and matched its previously recorded count exactly
(41/46/27/7/47/43/43/33), confirming M6C introduced no regression
anywhere, including to the M6A/M6B single-file sync harness it builds
directly on top of.

### Dependency / source-file audit

`zeros3.go` remains the sole implementation source file (M6 added the
"17. Optional ZeroS3 Delta Sync (M6)" / "17b. `zeros3 sync` client"
sections; M6C added one further "17c. `zeros3 sync` directory (recursive)
client (M6C)" subsection immediately after 17b, purely additive — no
existing section was restructured); `zeros3_test.go` remains the only
test file; `go.mod` still carries zero `require` directives; no
vendoring; no new subprocess/shell-out (M6C's own internal test suite
uses `os/exec` to drive the *test's own* built binary for one CLI-level
smoke test, exactly like the pre-existing M5-C `TestCLI_
VersionsRestoreGCDoctor_Smoke` pattern — this is test-only, not a runtime
shell-out from `zeros3.go` itself, which still never execs a subprocess).
M6C's only new stdlib usage is `path/filepath`'s `WalkDir` and
`io/fs`'s `DirEntry`/`FileMode`, both already-imported packages used here
in a genuinely new role (client-side recursive traversal); see
`STDLIB.md`. Single File eligibility and reproducible-build properties
are unaffected (no new build tags, no new external state read at build
time, no new import outside the Go standard library).

### Persistent-format impact

**None.** `storeFormatVersion`/`cdcFormatVersion`/`manifestFormatVersion`
are unchanged; the journal record type set is unchanged (no new record
type was added — sync produces the exact same `recordTypePutObjectRootV2`
frame an ordinary `PutObject` does); the manifest JSON shape is
unchanged. A store written partly via `zeros3 sync` (single-file or
directory) and partly via ordinary `PutObject` is byte-for-byte
indistinguishable, on disk, from one written entirely by one or the
other. M6C introduced zero new persistent state of its own — a directory
sync run's `dirSyncResult` is a purely in-memory, operation-local value,
never written to disk.

### Known limitations (honestly scoped, not silently dropped)

- **Sequential, bounded chunk upload only** — no client-side parallelism.
  Spec explicitly frames concurrency as optional ("Correct sequential/
  bounded behavior outranks maximum throughput... do not add concurrency
  merely for benchmark numbers"); this pass chose not to add it, keeping
  the client's error handling and cancellation semantics simple and
  race-free by construction rather than introducing bounded-worker-pool
  machinery this milestone doesn't require. M6C's directory orchestration
  is likewise strictly sequential, one file at a time, for the same
  reason — see "Optional bounded concurrency" in the milestone spec.
- **Local mutation detection is size+mtime-based, not a filesystem
  snapshot** — documented exactly above; an in-place rewrite preserving
  both is not detected. No stdlib-only mechanism on Linux can make a
  stronger claim without a real snapshot filesystem underneath it.
  `zeros3 sync` never claims to make one, for a single file or a
  directory.
- **A literal retry of an already-succeeded commit is a safe rejection,
  not a transparently-idempotent success** — documented exactly above
  under "Resume semantics"; the caller's correct recovery is to re-run
  sync (which re-observes reality), which is itself tested and green.
- **`zeros3 sync` has no `-force`/unsafe-overwrite flag** — every commit
  goes through the safe-mode precondition; this was a deliberate scope
  decision (not required by any M6A/M6B test), not an oversight, since
  the normal re-sync-to-the-same-key case (no concurrent third-party
  writer) already succeeds under the safe precondition on its own — see
  `TestSync_ConflictUnchangedDestinationCommitsCleanly`.
- **Directory sync has no `--delete`/mirror mode, and none is planned for
  this milestone** — deliberate, not an oversight; see C4 above and
  `SYNC_PROTOCOL.md`/`MILESTONES.md` for why deletion is treated as a
  separate, never-implicit, explicitly-designed future addition rather
  than a flag bolted onto M6C.
- **Directory sync is not a filesystem snapshot** — it operates on the
  file set found by exactly one recursive walk at the start of the run;
  a file added after its directory has already been walked is picked up
  on the next run, not the current one. Documented exactly under "Local
  mutation / directory-level races" above.
- **No `.ignore`-file/glob-exclusion language** — every regular file
  below the source root is eligible; there is no way to exclude a subtree
  short of not putting it under the source root. Not required by any
  C1-C12 requirement and explicitly out of scope ("ignore-file language"
  is listed under this milestone's own non-goals).

## M5-D — Multipart pagination micro-pass: `ListParts`/`ListMultipartUploads`

**COMPLETE.** A tightly bounded pass with exactly one purpose: close the
`ListParts`/`ListMultipartUploads` pagination gap M5-C inspected and
explicitly deferred (see the M5-C "Phase A" section below). No M6 delta
sync, directory sync, replication, new authentication modes, internal
version/GC changes, pack files, compression, compaction, indexing, Merkle
structures, or unrelated S3 features were started.

- **Starting state:** `zeros3` branch
  `claude/zeros3-m5d-multipart-pagination-gzlhq8` was confirmed
  identical-tree to `origin/main` at
  `6ef28fd042d57a56e1eeb5c914b4b274d5b36894` (the M5-C merge) before any
  edit; `zeros3-testing`'s same-named branch was confirmed identical-tree
  to its own `origin/main` at `8fb6bbd50e0fca063140f5d4efbeefa595e97ee6`
  (also the M5-C merge). Both were fetched fresh from `origin` rather than
  trusting a prior session's summary. The regression baseline (`go test`,
  `go test -race`, `go vet`, `gofmt -l`) was green before any change: **223
  top-level test functions / 452 `--- PASS` lines (including subtests), 0
  failing**, matching the M5-C end-state.
- **Resulting state:** this commit, on the same branch (`git log -1` names
  the exact SHA); `zeros3-testing`'s matching commit on its own same-named
  branch carries this pass's external-regression evidence.

### Pagination operations completed

- **`ListParts`** (`Store.ListPartsPage`, `handleListParts`,
  `parseListPartsQuery`): `part-number-marker` (parts with part number
  strictly greater than the marker), `max-parts` (default/hard cap 1000,
  matching `ListObjectsV2`'s own `max-keys` convention and AWS's
  documented "1,000 is also the default value" ceiling for this
  operation), `IsTruncated`, `NextPartNumberMarker`. Stable ascending
  part-number order, computed at response time from the existing
  `map[int]*multipartPart` (no new persistent index — a replaced part
  number simply never appears twice, since `UploadPart` already overwrites
  the map entry in place). The pre-existing `Store.ListParts` (returns
  every part, used by several crash/restart tests) is untouched; the new
  paginated path is a separate method used only by the HTTP handler, so no
  existing test call site needed to change.
- **`ListMultipartUploads`** (`Store.ListMultipartUploads` — its own
  signature gained the three pagination parameters directly, since it had
  no callers besides the HTTP handler to preserve — `handleListMultipartUploads`,
  `parseListMultipartUploadsQuery`, `afterMultipartMarker`): `key-marker`,
  `upload-id-marker`, `max-uploads` (same default/cap-1000 convention),
  `IsTruncated`, `NextKeyMarker`, `NextUploadIdMarker`. Ordering is key
  ascending, then upload ID ascending as the tie-break (unchanged from
  M5-B) — upload IDs are UUIDv7, so this reproduces AWS's own documented
  "same key, ascending initiation time" secondary order without needing a
  second sort key. `upload-id-marker` is ignored unless `key-marker` is
  also given, matching AWS's documented rule exactly (verified against
  `docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html`
  before implementing, not guessed) — this required the marker comparator
  to special-case an absent `upload-id-marker` rather than treating it as
  an ordinary empty-string tuple compare (an earlier version of this
  comparator wrongly re-admitted `key-marker`'s own uploads, caught by
  this pass's own `resume-between-keys` test before being fixed).

### XML/validation

- `listPartsResult` gained `PartNumberMarker`/`NextPartNumberMarker`
  (always rendered, including `0` when not truncated — there is no
  verified AWS-compatible omission rule for these two, unlike
  `ListObjectsV2`'s opaque `NextContinuationToken`, which this codebase
  does omit when not truncated; every AWS SDK drives pagination off
  `IsTruncated`, not field presence, so this is compatible in practice).
  `listMultipartUploadsResult` gained `KeyMarker`/`UploadIdMarker`/
  `NextKeyMarker`/`NextUploadIdMarker` (always rendered, empty when not
  applicable — AWS's own published example response shows exactly this
  present-but-empty shape for a non-truncated `ListMultipartUploads`).
- Malformed query values are rejected as `InvalidArgument`, matching
  `ListObjectsV2`'s own convention: non-integer or negative
  `part-number-marker`/`max-parts`/`max-uploads` reject; `0` is accepted
  as a valid boundary (an empty, non-truncated page — mirrors
  `ListObjectsV2`'s own verified `max-keys=0` behavior); any value above
  1000 is silently clamped to 1000 rather than rejected (matches AWS's own
  documented ceiling framing, "1,000 is also the default value", for both
  operations). `upload-id-marker` without `key-marker` is documented AWS
  behavior to *ignore*, not reject — implemented as ignore, not an error.

### Internal test result

`go test ./...`, `go test ./... -race`, `go vet ./...`, `gofmt -l .`: all
clean. **263 top-level test functions / 452 `--- PASS` lines (including
subtests), 0 failing** — up from the 223/many-fewer-subtests baseline
above, entirely from this pass's new tests (42 `--- PASS` lines under
`TestListParts_Pagination*`/`TestListMultipartUploads_Pagination*`
covering: zero/one/fewer/exactly/max+1 parts or uploads, multi-page
iteration, marker at the beginning/middle/end/beyond the highest value,
default and explicit small and clamped page sizes, invalid marker/max
syntax, restart-then-paginate determinism (`Store`-level, mirroring the
existing crash-test pattern), an overwritten part never duplicating in a
paginated listing, multiple keys, multiple uploads for the same key with
marker resume within that key, and completed/aborted uploads never
appearing). No existing test was modified; the pre-M5-D multipart
regression suite is unchanged and still green.

### External SDK result

New harness `zeros3-testing/harness/m5d/pagination` (AWS SDK for Go v2,
`s3.Client`, low-level calls only): 7 tiny parts paginated with
deliberately small `MaxParts=2`, and 6 active uploads across 5 keys (one
key with 2 concurrent uploads) paginated with deliberately small
`MaxUploads=2`, each following the real `Next*Marker` response fields into
the next request. **43/43 passed** — see
`zeros3-testing/results/M5D_PAGINATION_RESULTS.md`. The pre-existing M5-B
multipart harness (`harness/m5b/multipart`) was re-run unmodified against
the same build as a regression check: **43/43 passed**, unchanged.

### Known limitations

- `ListMultipartUploads`'s `delimiter`/`prefix`/`CommonPrefixes` grouping
  remains unimplemented (out of scope for this pagination-only pass;
  `S3_COMPAT.md` records it as a compatibility deviation).
- AWS's exact XML rendering of `NextPartNumberMarker` for a *non-truncated*
  `ListParts` response was not independently verifiable (no published AWS
  example covers that specific case); ZeroS3 always renders it (documented
  in `S3_COMPAT.md`), an evidence-informed choice consistent with the
  verified `ListMultipartUploads` behavior, not a guess made from nothing.
- `max-parts=0`/`max-uploads=0` are treated as a valid empty, non-truncated
  page (mirroring `ListObjectsV2`'s own verified `max-keys=0` behavior)
  rather than rejected, even though AWS's `max-uploads` documentation
  phrases its valid range as "1 to 1,000"; this was a deliberate,
  documented consistency choice under this pass's budget, not independently
  verified against real AWS for the `max-uploads=0` case specifically.

### Dependency/source-file audit

Zero-dependency/single-file constraints intact: `zeros3.go` remains the
only non-test source file in the `zeros3` module (`zeros3_test.go` the
only test file), `go.mod` still has no `require` directives, and no
persistent on-disk format changed — pagination is computed entirely at
response time from the existing in-memory `Store.uploads`/`up.parts`
state, with no new journal record type, no new field written to disk, and
no change to CDC/CAS/manifest/journal encoding. `zeros3-testing`'s
dependency direction is unchanged (`zeros3-testing --(HTTP/S3 API)-->
zeros3`, never the reverse); the new harness reuses the same pinned AWS
SDK for Go v2 versions already recorded above, adding no new dependency.

## M5-C — Internal versions, restore, authoritative reachability, safe GC, doctor/stats

**COMPLETE.** A bounded storage-lifecycle pass, exactly per its own scope:
ZeroS3-native (non-AWS-API) immutable object version history, zero-copy
restore, one authoritative CAS/manifest reachability model spanning
current objects, retained historical versions, and active multipart
uploads, safe offline/exclusive GC (dry-run by default, fail-closed on
corruption), and doctor/verify/stats extension built on that same
reachability source of truth. No M6 delta sync, directory sync, remote
replication, pack files, compression, compaction, Merkle structures,
advanced indexing, full AWS S3 Versioning API, lifecycle policies, or IAM
work was started. Multipart `ListParts`/`ListMultipartUploads` pagination
was inspected and explicitly deferred (see Phase A below).

- **Starting state:** `zeros3` branch `claude/zeros3-m5c-storage-lifecycle-o98lsz`
  was confirmed identical-tree to `origin/main` at
  `ecfc7c436e86f06fdf86d6e1993d36e5cb457c63` before any edit (`git
  rev-list --count origin/main..HEAD` was `0`); `zeros3-testing`'s
  same-named branch was confirmed identical-tree to its own `origin/main`
  at `cdc542f3b0e28e16ade159a39a1fb1acab9cf879`. Both were fetched fresh
  from `origin` rather than trusting a prior session's summary. The Linux
  regression baseline (`go test`, `go test -race`, `go vet`, `gofmt -l`)
  was green with **223 passing test cases** before any change, matching
  the M5-B end-state recorded above.
- **Resulting state:** this commit, on the same branch (`git log -1`
  names the exact SHA); `zeros3-testing`'s matching commit on its own
  same-named branch carries this pass's external-regression evidence.

### Phase A — multipart pagination: inspected, deferred

`ListParts`/`ListMultipartUploads` were re-inspected at the start of this
pass (both remain the single-page implementation M5-B shipped: no
`part-number-marker`/`max-parts`/`max-uploads`/`key-marker`/
`upload-id-marker`, `IsTruncated` always `false`). Per this task's own
hard-stop rule, implementing real pagination touches XML response shapes,
query-parameter parsing, and routing on both list endpoints — a real,
if modest, surface change, not a small local patch — and would have
diverted budget from the actual point of this milestone (versions/GC).
**Deferred to the start of M6 compatibility cleanup**, exactly as
`S3_COMPAT.md`/`README.md` now record. Every other M5-C requirement below
was completed without it.

### Phase B — internal immutable object version model

**Architecture: one shared "replace current object while retaining prior
state" path, not three duplicated ones.** Every mutation that can replace
or remove a current object — ordinary `PutObject` overwrite, `CopyObject`
overwrite, a completed multipart overwrite, `restore`, and `DeleteObject`
— funnels through `archivedVersionPayload` (builds the archived-version
journal payload from the current `objectEntry` being replaced, or returns
`nil` if there is none) and `archiveVersionLocked` (applies it to the new
`Store.history map[string]map[string][]*historyVersionEntry`, keyed
bucket→key→ordered history slice, guarded by the same `Store.mu` as the
bucket/object namespace). `commitObjectRoot` — already `PutObject`'s and
`CopyObject`'s shared commit tail before this pass — is the one place this
logic lives for those three callers plus `RestoreObjectVersion`;
`CompleteMultipartUpload`'s own inline commit (which must also atomically
retire the upload session) calls the exact same
`archivedVersionPayload`/`archiveVersionLocked` pair rather than
reimplementing it. **A first-time `PutObject` to a key that has never had
a current root archives nothing** — there is no meaningful "previous
state" to keep (`TestVersions_FirstPutCreatesNoHistory`).
- **Version identity:** each archived history row gets its own fresh
  `newUUIDv7()` — the exact same primitive `manifestUUID`/`storeID`
  already use, not a second ID scheme — generated once at commit time and
  persisted in the journal, so two archival events that happen to carry
  byte-identical content (e.g. restore-then-overwrite) still get two
  distinct, independently addressable history rows rather than colliding
  on a shared manifest UUID.
- **What a historical version retains:** `historyVersionEntry` carries
  `versionID`, `manifestUUID`/`manifestSHA256` (the exact immutable
  manifest — object size/ETag/SHA-256/content-type/chunk list are all
  reachable through it, never duplicated into the history record itself),
  `archivedAt`, `reason` (`"overwritten"` | `"deleted"`), and the
  archiving journal frame's own `seq` (stable total order).
- **No payload duplication:** history retains a *reference* to the
  replaced manifest, never a byte copy — this is what makes Phase D's
  zero-copy restore possible at all.

### Phase C — version history CLI

`zeros3 versions -bucket B -key K [-store DIR] [-json]`
(`runVersions`/`Store.ListVersions`, `zeros3.go` section 16): lists the
current root (if any, `status: "current"`) followed by every retained
historical version, newest-first, each row carrying version ID, logical
size, ETag, content type, an RFC3339Nano archival timestamp, and a
`deleted` flag when `reason == "deleted"`. Human output is fixed-width
columns; `-json` emits `[]versionRow`. Deterministic across restart
(`TestVersions_FullLifecycleAcrossRestart`,
`TestCrash_JournalReplay_HistoryRecordTypesDeterministic` — two
independent `OpenStore` replays of the same journal produce byte-identical
`versionID`/`manifestUUID`/`seq` ordering). Exercised over the real built
binary in `TestCLI_VersionsRestoreGCDoctor_Smoke`.

### Phase D — restore

`zeros3 restore -bucket B -key K -version ID [-store DIR]`
(`runRestore`/`Store.RestoreObjectVersion`, `zeros3.go` section 7c).
**Critical semantic, proven directly:** restore commits a brand-new
current root (through the same `commitObjectRoot` every overwrite uses,
so whatever it replaces is itself archived into history) — it never
deletes or rewrites an existing history entry
(`TestVersions_FullLifecycleAcrossRestart`,
`TestRestore_ZeroCopy_OverExistingCurrent` — restoring v1 over a current
v3 leaves v1/v2/the-replaced-v3 all present, 3 history rows afterward).
- **Zero-copy, proven, not asserted:** `RestoreObjectVersion` passes
  `found.manifestUUID`/`found.manifestSHA256` — the historical version's
  *existing* manifest identity — straight into `commitObjectRoot`; no new
  manifest is built or published, no chunk is re-read or re-written.
  `TestRestore_ZeroCopy_OverExistingCurrent` and
  `TestStorageEfficiencyProof_VersionHistoryIsCheap` both count actual
  chunk/manifest files on disk before and after a restore and assert the
  counts are identical.
- **Required cases, all proven:** restore old version over an existing
  current object (`TestRestore_ZeroCopy_OverExistingCurrent`); restore
  after current-object deletion (`TestRestore_AfterCurrentObjectDeletion`);
  restart after restore (`TestVersions_FullLifecycleAcrossRestart`);
  restore of a version created by ordinary PUT
  (`TestVersions_PutOverwriteCreatesHistory` + restore tests above), by
  `CopyObject` (`TestVersions_CopyObjectOverwriteCreatesHistory`), and by
  multipart completion (`TestVersions_MultipartOverwriteCreatesHistory`,
  `TestCrash_Restart_MultipartOverwritePreviousVersionPreserved`); invalid
  version ID (`TestRestore_InvalidVersionID`, `errNoSuchVersion`); version
  belonging to the wrong bucket/key (`TestRestore_WrongBucketOrKey` —
  deliberately not distinguished from "unknown", same
  information-leak reasoning `errNoSuchUpload` already uses); corrupted
  historical manifest (`TestRestore_CorruptedHistoricalManifest_NoPartialMutation`)
  or missing historical chunk
  (`TestRestore_MissingHistoricalChunk_NoPartialMutation`) — both fail
  restore cleanly with the current object provably untouched afterward
  (Phase D's "no partial visible mutation" requirement).

### Phase E — delete/history semantics

**Model implemented, exactly as recommended:** `DeleteObject` archives the
current root into history (`reason: "deleted"`) in the same journal frame
that removes it from the visible namespace (new record type 11, below);
ordinary `GetObject`/`HeadObject` return `errNoSuchKey`/404 immediately
afterward; `zeros3 versions` still shows full history; `restore` can
recreate a current object from any retained version, including one
archived by a delete. No AWS delete markers, no versioned-DELETE API — a
plain internal archive-then-remove, matching the existing non-versioned
`DeleteObject` contract at the S3 wire layer exactly as before.
**Required scenario, verbatim:** `TestVersions_FullLifecycleAcrossRestart`
runs PUT A → PUT B → DELETE → restart → GET=not found → versions still
contain A/B → restore B → restart → GET exact B, end to end.

### Phase F — one authoritative reachability model

`computeReachability` (`zeros3.go` section 12a) is the single root-
enumeration/mark-live path every consumer below shares — stats, GC, and
verify/doctor no longer each walk their own subtly different liveness
computation. It enumerates exactly the three required root categories —
current objects (`snapshotNamespace`), retained historical versions
(`snapshotHistory`, new), and active multipart uploads' already-published
parts (`snapshotUploads`, new — these never go through the manifest
mechanism before completion, so each part's own chunk list is a live root
directly) — resolves each to a manifest/chunk reference set, and produces
two related but distinct outputs: `ReferencedManifests`/`ReferencedChunks`
(everything any live root points to, protected from deletion regardless
of whether that specific file is itself intact) and `ValidChunks` (the
subset that also passed an existence/size(/deep-hash) check). Designed
for straightforward extension: a future M6 sync-session root is a fourth
enumeration loop, not a redesign. `TestReachability_CoversAllThreeRootCategories`
proves all three categories are actually counted and unioned into one
live set.

### Phase G — reachability integrity rules

`computeReachability` reports, rather than silently skips, every kind of
corruption Phase G lists: missing manifest, malformed manifest, referenced
chunk missing, wrong chunk hash (deep mode), a multipart part referencing
a missing payload, and a historical version referencing an invalid object
state — all via the shared `issueTracker`/`VerifyIssue`
missing/corrupt/invalid classification. **Reachable-but-broken is never
reclassified as garbage:** a chunk/manifest a live root references stays
in the protected `Referenced*` sets even when it individually fails
validation (only `OK()` — the fail-closed gate — turns false); a digest no
live root ever claimed is the only thing genuinely unreachable.
`TestReachability_DetectsCorruptionAmongLiveRoots` proves this for all
three root categories independently (current/historical/multipart).

### Phase H — safe GC

`zeros3 gc -store DIR [-apply] [-json]` (`runGC`/`gcCollect`, `zeros3.go`
section 13b). **Dry-run by default**, and dry-run genuinely deletes
nothing (`TestGC_DryRunDeletesNothing` — plants real garbage, confirms the
chunk-file count on disk is byte-for-byte unchanged after a dry-run).
Reports: chunks scanned/reachable/unreachable, reachable/reclaimable
payload bytes, reclaimable disk bytes (payload + stale `tmp/` staging
bytes), manifest scan counts, and live root counts by category.
**Destructive mode requires the explicit `-apply` flag** — no ambiguous
default, no ambient confirmation prompt.
- **Offline/exclusive requirement, genuinely enforced, not merely
  documented:** `acquireStoreLock`/`storeLock` (new — no exclusive-open
  primitive existed before this pass) wraps a non-blocking
  `syscall.Flock` on a dedicated `store/LOCK` file. `zeros3 serve` now
  holds a **shared** lock for its whole run; `gc` (both dry-run and
  apply) takes an **exclusive** lock, which flock semantics refuse to
  grant while any shared or exclusive lock is held elsewhere — including
  by a different OS process on the same directory — so GC refuses safely
  (`errGCStoreInUse`) the instant the server (or another `gc`) currently
  owns the store, and never blocks waiting.
  `TestGC_ExclusivityRefusesWhileStoreInUse` proves both dry-run and
  apply refuse while a held lock simulates an active server, and that GC
  succeeds normally once released. `stats`/`verify`/`doctor` deliberately
  do not participate in this locking (documented in section 13b): they
  are read-only point-in-time snapshots, and this milestone only requires
  protecting live data *from GC*, not protecting a read from a concurrent
  GC sweep.

### Phase I — GC safety invariant

GC's destructive delete stays deliberately simple, per this phase's own
suggestion: CAS/manifest files are immutable and content-addressed,
reachability is computed once, right after exclusive ownership is
acquired (so no writer can be racing it), and each unreachable file is
`os.Remove`d independently with no transactional deletion metadata — an
interruption mid-sweep can only ever leave some garbage still on disk,
never touch a file reachability classified live.
`TestGC_InterruptedSweep_K6` proves this directly: plants 5 unreachable
chunks plus one live object, interrupts a destructive sweep after its 2nd
deletion via the new `hookBeforeGCDelete` test seam (the same
panic/recover crash-injection pattern every other crash test in this file
already uses), reopens the store, confirms the live object and a full
`Verify(true)` are both still perfectly clean, confirms genuine partial
progress (garbage count strictly between 0 and 5), and confirms re-running
`gc -apply` finishes the cleanup to exactly 0 remaining.

### Phase J — GC corruption fail-closed behavior

`gcCollect` checks `rr.OK()` a second time immediately before the
destructive delete loop and refuses with `errGCUnsafe` if the live root
set is not fully valid — dry-run still reports the same issues, it simply
never reaches the point of deleting anything.
`TestGC_RefusesOnCorruptLiveRoot_K7` proves exactly this: a chunk
referenced by the current (live) root is deleted out from under the
store, dry-run correctly reports `LiveSetOK: false` plus the issue,
`gc -apply` returns `errGCUnsafe` and touches no file.

### Phase K — GC adversarial test matrix

`TestGC_AdversarialMatrix_K1toK5` constructs all five categories in one
store — K1 current-only, K2 historical-only (put then overwritten), K3
active-multipart-only, K4 genuinely unreachable (written straight to CAS,
referenced by nothing), K5 shared between a current object and a
historical version — and proves dry-run reports exactly 1 unreachable
chunk (K4) and apply deletes exactly that one, with K1/K2/K3/K5's chunk
files (and, after completing the K3 upload and reading K1/K5 back, their
actual object bytes) all provably intact afterward, plus a clean deep
`Verify` of the whole store. K6 (interrupted GC) is Phase I above; K7
(corrupt live root) is Phase J above.

### Phase L — version/GC interaction

`TestVersionGC_Interaction` runs PUT v1 → v2 → v3 → `gc -apply` →
restore v1 → exact bytes, proving history survives a real destructive GC
pass. Since this milestone implements no explicit version deletion,
history is retained indefinitely and GC never reclaims it — documented
explicitly here and in `S3_COMPAT.md`; no automatic retention/expiration
was invented.

### Phase M — multipart/GC interaction

The most important correctness proof in this pass, per the task's own
framing, and proven directly:
`TestGC_MultipartSurvivesGC_ThenCompletes` initiates a multipart upload,
uploads two unique 6MiB parts (no current object exists yet), plants
separate genuine garbage, runs `gc` dry-run then `-apply` (confirms
exactly the planted garbage — and only the planted garbage — is deleted),
reopens the store (restart), completes the upload, and confirms the
completed object's bytes are exactly the two parts concatenated.
`TestGC_AbortedMultipartBecomesCollectible` proves the second half:
initiate → upload a unique part → abort → the part's former chunk is
reported unreachable by dry-run and is removed by apply.

### Phase N — doctor / verification integration

`Verify` (`zeros3.go` section 13) was rebuilt on top of
`computeReachability` rather than maintaining a second, separately walked
manifest/chunk-checking pass — the exact "prefer one root-enumeration path
consumed by all of them" instruction, applied literally. `VerifyResult`
gained `CurrentRootCount`/`HistoricalRootCount`/`MultipartRootCount`
(doctor-style lifecycle visibility) and now embeds the shared
`issueTracker` (JSON-flattened, so `missing`/`corrupt`/`invalid`/`issues`
are byte-identical field names to before this pass). Deep mode's
whole-object SHA-256 re-hash now runs over every referenced-and-valid
manifest from `ReferencedManifests` — current **and** historical roots —
not just current ones. `zeros3 doctor -store DIR [-deep] [-json]`
(`runDoctor`) is a thin, explicit CLI name wired directly to this same
`Verify` engine, per this task's own "acceptable to evolve verify instead"
guidance — one coherent diagnostic interface, not two. Never mutates the
store. Exercised over the real binary in
`TestCLI_VersionsRestoreGCDoctor_Smoke`.

### Phase O — stats extension

`computeStats` (`zeros3.go` section 12) gained
`historical_version_count`/`historical_version_logical_bytes` and
`active_multipart_upload_count`/`active_multipart_logical_bytes` (scoped
exactly like the existing current-object fields — same
`sel.matches(bucket,key)` rule). `version_count`/`logical_version_bytes`
now genuinely differ from `current_object_count`/`logical_current_bytes`
for the first time — they are current-plus-historical totals, exactly the
"future milestone" the pre-M5-C code comment already predicted this would
be. `unique_reachable_chunk_bytes` and the `chunk_store_file_bytes`/
`manifest_file_bytes`/`reclaimable_bytes` file-scan classification are now
sourced from `computeReachability`'s whole-store `Referenced*` sets
instead of a current-objects-only walk — this is the concrete Phase F bug
fix: a chunk kept alive only by history or an in-progress multipart
upload is no longer misreported as reclaimable
(`TestStats_ReclaimableAfterDelete`, rewritten this pass to assert exactly
that: after `DeleteObject`, the deleted object's manifest/chunk remain
fully reachable and `reclaimable_bytes` stays `0`, not the pre-M5-C
behavior of reclassifying them as garbage). Scope-based sharing accounting
(`logical_chunk_reference_*`, `scope_unique/exclusive/shared_chunk_bytes`,
dedup ratios) is unchanged — it remains a current-objects-only concept, as
`scope` always was. Never performs any destructive action.

### Phase P — storage-efficiency proof

`TestStorageEfficiencyProof_VersionHistoryIsCheap`: uploads a 2MiB random
v1, two small (64-byte) edits producing v2/v3, and measures — not
asserts — the numbers directly: **total logical version bytes
6,291,456 (3× 2MiB) vs. 2,277,482 unique reachable CAS bytes (ratio
0.362)**, i.e. keeping full history of three 2MiB versions costs ~36% of
the naive 3-full-copies size, not because anything is compressed but
because CDC/CAS content-addressing means only the chunks actually touched
by each edit are new. Restoring v1 afterward is then proven to add zero
further chunk or manifest files (same zero-copy proof as Phase D). Numbers
are logged via `t.Logf`, not cherry-picked.

### Phase Q — crash/restart tests

`TestVersions_FullLifecycleAcrossRestart` (overwrite → restart → versions
preserved; delete → restart → history preserved; restore → restart →
restored state preserved, all in one scenario, per Phase E's exact
required test);
`TestCrash_Restart_MultipartOverwritePreviousVersionPreserved` (a
completed multipart overwrite's prior version survives restart, and
restoring it afterward reproduces the exact pre-multipart bytes);
`TestCrash_JournalReplay_HistoryRecordTypesDeterministic` (two independent
fresh `OpenStore` replays of the same journal, containing new record
types 9/10/11, produce byte-identical version IDs/manifest UUIDs/seq
ordering — deterministic replay, proven not assumed).
`TestJournal_GenuinelyUnknownRecordTypeStillFailsClosed` proves the
general "old binary fails closed on an unknown persistent record"
mechanism (`replayJournal`'s known-type switch) still works correctly
after adding types 9-11, by hand-crafting a frame of a record type (200)
no version of this codebase has ever defined and confirming `OpenStore`
refuses it. Atomic-visibility crash injection at the specific
write-vs-sync boundary was not re-derived per record type: every new
commit path (`commitObjectRoot`, multipart completion) reuses the exact
same `Journal.appendFrame`/`hookAfterJournalWriteBeforeSync`/
`hookAfterJournalSync` durability boundary M1-M5-B's own crash tests
already exercise exhaustively, and archiving a version is folded into
that same single frame — there is no new atomicity boundary a new
crash-injection scenario would be needed to prove.

### Phase R — concurrency tests

`TestConcurrency_TwoOverwritesSameKey_HistoryDeterministic` (two
concurrent overwrites of one key: exactly one wins, history ends up with
exactly 2 entries, never a torn or duplicated view);
`TestConcurrency_RestoreRacingPut`, `TestConcurrency_RestoreRacingDelete`,
`TestConcurrency_RestoreRacingCopyObject` (each races `restore` against
the other operation on the same key and confirms a deterministic,
race-free outcome plus a clean deep `Verify` afterward);
`TestGC_ExclusivityRefusesWhileStoreInUse` (GC refusing to run while the
store is actively owned, Phase H above). All run clean under
`go test -race`, including `-count=6` repetition
(`go test -race -run 'TestConcurrency_|TestCrash_|TestGC_|TestVersions_|TestRestore_' -count=6`)
with zero flakes. No online-GC synchronization complexity was added —
GC's own concurrency story is the exclusivity lock in Phase H, not a new
locking model for ordinary mutations, which continue to use the exact
`Store.mu` re-check-and-commit pattern M1-M5-B already established.

### Phase S — external/client regression

No new S3 wire-protocol surface was added this pass — internal
versions/restore/GC are ZeroS3-only CLI/library additions, invisible to
ordinary S3 clients by construction (`ListObjectsV2` only ever lists
current objects; nothing in the HTTP request/response path changed). This
was checked, not merely asserted: every external AWS SDK for Go v2
harness already proven against M5-B was rebuilt and rerun, unmodified,
against this pass's binary. **211/211 passed across 6 harnesses, 0
failed, 0 changed from their M5-B results** — M2 canonical workflow
41/41, M3 CopyObject 46/46, M3 Range GET 27/27, M3 dedup evidence 7/7,
M5-A presign 47/47, M5-B multipart 43/43. Full detail, exact
reproduction commands, and toolchain/SDK version pins:
`zeros3-testing/results/M5C_REGRESSION_RESULTS.md`. rclone and Package
Killer (`s3rver`) were **not rerun this pass** — both require installing
additional external tooling into a scratch environment purely for
comparison, and the 6 reruns above already exercise the identical
header-auth/presigned/path-style/virtual-hosted/CopyObject/Range/
multipart code paths those tools also use; their last recorded results
stand, unaffected by anything in M5-C. New M5-C CLI surface
(`versions`/`restore`/`gc`/`doctor`) is intentionally outside this
repository's black-box-S3-client charter (it is not an S3 operation) and
is instead covered by `zeros3`'s own internal test suite, including a
CLI-level smoke test that builds and execs the real binary
(`TestCLI_VersionsRestoreGCDoctor_Smoke`).

### Phase T — format/versioning discipline

**Three new additive journal record types**, none repurposing an existing
one:

| # | Name | Supersedes | Payload adds over its predecessor |
|---:|---|---|---|
| 9 | `recordTypePutObjectRootV2` | 2 (`PutObjectRoot`) | optional `previous *journalArchivedVersionPayload` |
| 10 | `recordTypeCompleteMultipartUploadV2` | 8 (`CompleteMultipartUpload`) | optional `previous *journalArchivedVersionPayload` |
| 11 | `recordTypeDeleteObjectRootV2` | 3 (`DeleteObjectRoot`) | mandatory `archived journalArchivedVersionPayload` |

Every **live** commit/delete path now unconditionally uses the V2 type —
exactly the same "no branching by case" discipline M5-B's own type 8
already established for multipart completion — so types 2/3/8 are never
appended again by this binary; they remain **only** in `replayJournal`'s
known-type switch and `applyRecord`'s replay handling, so a pre-M5-C
journal still replays byte-for-byte unchanged
(`TestCrash_JournalReplay_HistoryRecordTypesDeterministic` and every
untouched M1-M5-B crash/replay test still passing prove this). **Replay
semantics:** types 9/10 apply the new root exactly like 2/8 did, then (if
`previous` is non-nil) call the new `archiveVersionLocked` to append a
`historyVersionEntry`; type 11 removes the current root and
unconditionally archives it. **Crash invariant:** publishing the new root
and archiving the one it replaces share one journal frame — the identical
single-fsync durability boundary M5-B's own record type 8 already proved
for "publish + retire upload" — so there is no window where one effect
committed and not the other. **Old-binary behavior:** an M1-M5-B binary
opening a store whose journal contains any of types 9/10/11 fails replay
via the pre-existing "unknown record type" check
(`replayJournal`), exactly like any other genuinely unknown type — proven
generically (not merely asserted) by
`TestJournal_GenuinelyUnknownRecordTypeStillFailsClosed`, which crafts a
frame of a record type no version of this codebase has ever defined and
confirms `OpenStore` refuses it. A store that has never run the M5-C
binary is byte-for-byte unaffected and remains fully readable by an older
one.

### Phase U — dependency audit

`go.mod` still has no `require` block. This pass's one new import,
`syscall` (for `syscall.Flock`, the exclusive-GC-ownership lock), was
already present in the toolchain's own dependency graph via `net/http`'s
internal use of it — confirmed by regenerating `deps-proof.txt`: the
non-stdlib package set (toolchain-vendored `golang.org/x/...`, the stdlib
`uuid` package, and `zeros3` itself) is unchanged from before this pass,
and the explicit first-path-segment-dot check still finds nothing.
`zeros3.go` remains the sole implementation file; `zeros3_test.go` remains
test-only (its one new stdlib import, `os/exec`, builds and runs the real
`zeros3` binary for `TestCLI_VersionsRestoreGCDoctor_Smoke` — a testing
technique, not a runtime dependency; `zeros3.go` itself still never
imports `os/exec`).

### Regression validation

`gofmt -l .` clean; `go vet ./...` clean; `go test ./...` and
`go test -race ./...` both green; `go test -count=3 ./...` green (no
flakes); `go test -race -run 'TestConcurrency_|TestCrash_|TestGC_|
TestVersions_|TestRestore_' -count=6 -v ./...` green, zero flakes across 6
repeated runs of every new crash/concurrency/GC/version/restore test.
**254 passing test cases** (`go test -v` `--- PASS` lines, counting
subtests), up from 223 at this pass's starting snapshot — 31 new top-level
tests (several with subtests: `TestReachability_DetectsCorruptionAmongLiveRoots`
has 3, `TestVersions_FullLifecycleAcrossRestart` etc. are single). Every
M1-M5-B suite (SigV4 header/query auth, CRC32/Content-MD5, CDC/CAS/
manifest/journal, crash/recovery, concurrency, ListObjectsV2, CopyObject,
Range GET, presigning, virtual-host addressing, multipart) remains green,
unmodified in behavior — confirmed by rerunning the exact same suite this
pass added onto, not a fresh/rewritten one.

**Dependency proof:** see Phase U above.

**Source-file invariant:** `zeros3.go` remains the sole implementation
file; `zeros3_test.go` remains test-only. No new `.go` file was added to
the ZeroS3 module.

**Reproducible build hash:** intentionally **not** refreshed this pass,
per this task's own explicit instruction ("Do not perform final
submission polish/reproducible-build refresh yet"). `zeros3.go` did
change in this pass, so the previously recorded M5-B hash
(`9e637369284cfcbfd333b74305c3852fd151f4b266f16355d93baeba9043d31a`) is
now stale evidence of the pre-M5-C binary, not a discrepancy; refreshing
it is left for a later dedicated submission-polish pass.

### Persistent-format impact

**Journal record type space extended; every frozen v1 value unchanged.**
`store_format_version`, `cdc_format_version`, `manifest_format_version`
are all still `1`; the journal magic (`ZSJ1`), frame layout, CRC32C
checksum, and CDC/manifest parameters are byte-for-byte unchanged from M1.
What's new: three additional journal record type numbers (9-11) for
version-history-aware commits/deletes, added the same additive way the
original four and M5-B's four were defined — no existing record type
repurposed, no new manifest field, no new top-level on-disk file/directory
(history lives entirely as an in-memory index rebuilt from existing
journal frames on replay — there is no separate `history/` directory or
second metadata database). See Phase T above for exact record-type
semantics/compatibility.

### Known limitations

- `ListParts`/`ListMultipartUploads` pagination remains unimplemented,
  deliberately deferred this pass (Phase A) to the start of M6
  compatibility cleanup.
- History is retained indefinitely: this milestone implements no
  explicit version deletion, expiration, or automatic retention policy
  (Phase L) — by design, not an oversight; a store that overwrites the
  same key very many times will accumulate unbounded history, all of it a
  permanent GC root.
- GC is offline/exclusive-only: no online or scheduled/background GC was
  implemented, per this milestone's explicit scope boundary. `stats`/
  `verify`/`doctor` do not take the store lock (Phase H) — they are
  read-only snapshots and this milestone does not require protecting them
  from a concurrent GC sweep, only protecting live data *from* GC.
  `syscall.Flock` is Unix-specific; unchanged from this project's existing
  Linux-amd64-only release-blocking platform commitment (STDLIB.md).
- No repair/undelete engine beyond explicit `restore` was built, per this
  task's own exclusion list.
- External AWS SDK/rclone/Package Killer regressions were not rerun this
  pass (Phase S) — no wire-protocol surface changed, so the last recorded
  results (M5-B and earlier, in the sections below) stand unaffected.
- Reproducible-build hash refresh was deliberately skipped this pass, per
  explicit instruction (see "Regression validation" above).
- All limitations recorded in the "M5-B" and earlier sections below remain
  current and unchanged by this pass.

### Exact next milestone

M6 delta sync / directory sync, beginning with the deferred
`ListParts`/`ListMultipartUploads` pagination as a small warm-up item
(Phase A above) before the larger sync-session work. Per this task's own
stop rule, M6 itself was not started in this pass.

## M5-B — Large-object S3 interoperability: multipart, payload modes, streaming HMAC, crash recovery

**COMPLETE.** A tightly bounded pass, exactly per its own scope:
formalized SigV4 payload-mode handling, header-auth `UNSIGNED-PAYLOAD`,
persistent S3 multipart upload integrated with the existing CDC/CAS/
journal architecture, aggressive crash/restart/concurrency testing of the
multipart lifecycle, and real AWS SDK for Go v2 + rclone (1 GiB) external
proof. No internal versions/restore, GC, sync, replication, pack files,
compression, compaction, Merkle structures, advanced indexing, or
unrelated S3 API expansion was started.

- **Starting state:** `zeros3` branch `claude/zeros3-m5b-large-object-jfnvkb`
  was confirmed identical-tree to `main` at
  `ae5574957f62396a9aa8ed772501bb8e6df2f454` before any edit; `zeros3-testing`'s
  same-named branch was confirmed identical-tree to its own `main` at
  `db9451e0d502cbbe194791d9935958c266e2cfd8`. Both were fetched fresh from
  `origin` (a prior same-named branch in each repository had already been
  deleted upstream, confirming it had been squash-merged, not abandoned
  mid-work). The Linux regression baseline (`go test`, `go test -race`,
  `go vet`, `gofmt -l`) was green with 182 passing test cases before any
  change.
- **Resulting state:** this commit, on the same branch (`git log -1` names
  the exact SHA); `zeros3-testing`'s matching commit on its own same-named
  branch adds `harness/m5b/multipart/main.go` and two results files on top
  of the same starting point.

### Phase A — SigV4 payload-mode architecture

One explicit interpretation layer, `classifySigV4Payload` (`zeros3.go`
section 8, next to the rest of the SigV4/auth code — no scattered literal
comparisons through the handlers), replaces the previous bare
`len(header) != 64` check. It returns one of five `sigv4PayloadKind`
values from the literal `X-Amz-Content-Sha256` header value: fixed
SHA-256 (a lowercase-or-uppercase 64-hex digest, lowered for comparison —
this single mode covers both an ordinary body and the empty-string
SHA-256 for a zero-length body; the empty body was never a separate
protocol mode), the fixed `UNSIGNED-PAYLOAD` sentinel, the two eligible
streaming-HMAC modes (recognized but not decoded — see Phase K below), and
one `sigv4PayloadUnsupported` bucket for every permanently-excluded AWS
sentinel (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`,
`STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD[-TRAILER]`). Every sentinel is
matched case-sensitively, exactly as AWS defines it — a lowercase or
misspelled variant is not treated as that sentinel and (not also being a
valid hex digest) is rejected as `AccessDenied`, never silently accepted
under some other mode. `authenticateHeader` uses the classified kind to
pick the canonical request's `HashedPayload` value and to decide whether
the post-signature exact-body-hash cross-check runs at all (fixed SHA-256:
yes, exactly as before this pass; `UNSIGNED-PAYLOAD`: never — SigV4 places
no constraint on the body in that mode, by design). Presigned/query-string
auth (`authenticateQuery`) is completely untouched: it already used the
fixed `UNSIGNED-PAYLOAD` sentinel unconditionally and never calls
`classifySigV4Payload` at all.

- **Fixed SHA-256 mode:** preserved behavior, not rewritten, per this
  pass's own instruction to keep whatever was already correct. 16 new
  tests explicitly cover: correct digest+body; wrong digest+body; the
  SHA-256-of-empty-string digest against an empty body (accepted); that
  same empty-body digest against a non-empty body (rejected,
  `XAmzContentSHA256Mismatch`); malformed digest (non-hex characters);
  invalid length (63/65 chars); and a `classifySigV4Payload` unit-level
  table test covering all of the above plus every sentinel.
- **`UNSIGNED-PAYLOAD` (header-auth, new):** a validly-signed PUT using it
  is accepted; a tampered `Authorization` header is rejected
  (`SignatureDoesNotMatch`); a body substituted **after** signing does
  **not** invalidate the signature (proving SigV4 places no constraint on
  body content in this mode); the same substitution **does** still fail
  independent `Content-MD5`/CRC32 checks when those headers are present
  and wrong (proving those checks are genuinely independent of SigV4);
  lowercase/misspelled sentinel variants reject as `AccessDenied`; the
  three permanently-excluded sentinels reject as `NotImplemented`; M5-A's
  presigned-URL behavior is unaffected (a dedicated regression test proves
  a full presigned GET still works after this pass's payload-mode
  refactor).
- **Explicitly unsupported/excluded, by design (hard exclusions, per this
  task's own scope):** `STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD[-TRAILER]`
  (SigV4A/ECDSA — not implemented) and `STREAMING-UNSIGNED-PAYLOAD-TRAILER`
  — all three recognized cleanly and rejected with `NotImplemented`
  (HTTP 501), never misclassified as a malformed digest.
- **Conditional (eligible, not implemented this pass):**
  `STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` — see "Phase K" below for
  why.

### Phase B-F — persistent multipart upload

**Architecture: no parallel object-storage backend.** Multipart reuses
every existing primitive: `UploadPart` CDC-chunks its body with the exact
same `chunkData`/`casWrite` PutObject already uses (so an uploaded part's
bytes are durable, deduped, content-addressed CAS chunks from the moment
the request is acknowledged); `CompleteMultipartUpload` produces an
ordinary object via the exact same commit discipline `commitObjectRoot`
already uses (bucket existence re-checked at the actual commit point,
one journal append+sync as the sole durability boundary, in-memory
namespace updated only after that succeeds). The one new architectural
piece is four new journal record types (5-8:
`recordTypeCreateMultipartUpload`/`UploadPart`/`AbortMultipartUpload`/
`CompleteMultipartUpload`) added to the *existing* visibility journal —
**no new on-disk file/directory structure was introduced for multipart
state at all**. `Store.uploads map[string]*multipartUpload` is the
in-memory namespace these records replay into, guarded by the same
`Store.mu` as the bucket/object namespace, and is completely separate from
`Store.buckets[*].objects` — an incomplete upload is structurally
incapable of appearing in `ListObjectsV2` or being reached by ordinary
GET/HEAD, because nothing ever writes it into that map before completion.

`recordTypeCompleteMultipartUpload`'s payload is deliberately shaped like
an ordinary `journalPutPayload` plus an `UploadID`: **one journal frame
both publishes the finished object as an ordinary root AND retires the
upload session**, so the two effects share the exact same write+sync
durability boundary — there is no window where one happened and not the
other (see Phase G below).

**The one genuinely new algorithm: correct CDC re-chunking across part
boundaries.** `CompleteMultipartUpload` does **not** concatenate each
part's independently-computed chunk list into the final manifest — each
part was CDC-chunked starting fresh at its own first byte, so treating a
part boundary as if it were already a content-defined chunk boundary would
silently produce different, non-canonical chunk boundaries near every seam
than chunking the true logical concatenation would. Instead, completion
streams the full logical concatenation through one fresh CDC pass exactly
as if the whole object had arrived as a single `PutObject` body:
`multipartReader` presents the ordered concatenation of parts' already-
durable CAS chunk bytes as one `io.Reader` (reconstructing at most one
chunk, ≤256KiB, at a time — never the whole object), and
`chunkAndStoreStream` (a streaming generalization of PutObject's
chunk+CAS-write loop) feeds it through the ordinary CDC chunker, durably
publishing each newly-produced chunk into the same CAS an ordinary PUT
would use and accumulating only the small chunk-reference list plus a
running whole-object SHA-256 — never buffering the full reconstructed
object. `TestMultipart_CompletedObjectDedupsAgainstOrdinaryPutOfSameBytes`
proves this measurably: the same logical bytes uploaded once via ordinary
PUT and once via a two-part multipart upload (split at an arbitrary,
off-center byte offset chosen specifically to stress a chunk-boundary
reuse) show store-wide unique bytes growing by far less than the object's
own size after the multipart completion, i.e. real CAS reuse across the
two paths despite the different chunking history.

- **Multipart API implemented:** `CreateMultipartUpload` (`POST
  /bucket/key?uploads`), `UploadPart` (`PUT
  /bucket/key?partNumber=N&uploadId=ID`), `ListParts` (`GET
  /bucket/key?uploadId=ID`), `CompleteMultipartUpload` (`POST
  /bucket/key?uploadId=ID` with an XML `<CompleteMultipartUpload>` body),
  `AbortMultipartUpload` (`DELETE /bucket/key?uploadId=ID`),
  `ListMultipartUploads` (`GET /bucket?uploads`). Routing is decided purely
  by query parameters ahead of the ordinary bucket/object dispatch in
  `ServeHTTP` — the same pattern `handlePutObject` already uses to
  distinguish `x-amz-copy-source` PUTs from ordinary ones.
- **ETag semantics (Phase E), a genuinely different rule from single-PUT:**
  `multipartETag` implements `MD5(binary_MD5(part1) || binary_MD5(part2) ||
  ...) + "-" + part_count` — the conventional S3 multipart formula, applied
  only to completed multipart objects. `TestMultipart_ETag_
  DiffersFromOrdinarySinglePutETag` proves the same bytes uploaded via
  ordinary PUT and via a one-part multipart upload get **different** ETags,
  so the single-PUT MD5-of-body rule (unchanged, still applies to every
  ordinary PUT/CopyObject) is never accidentally reused for a multipart
  completion. Verified independently over the real wire in the rclone
  large-object proof (`M5B_RCLONE_LARGE_OBJECT_RESULTS.md`): a genuine
  1 GiB/205-part upload's `HeadObject` ETag is
  `"b30575cd9e0c41bd4c1df0e8294bfea2-205"`.
- **Validation (Phase F):** nonexistent upload ID, upload ID for the wrong
  bucket/key (both report `NoSuchUpload` — real S3 does not distinguish
  these either, to avoid leaking cross-key namespace information),
  invalid part number (outside 1..10000, non-numeric, negative — table
  test), empty completion part list (`MalformedXML`), a part referenced in
  completion that was never uploaded or duplicated
  (`InvalidPart`/rejected), an out-of-order completion list
  (`InvalidPartOrder`), a wrong or malformed ETag in the completion
  request (`InvalidPart`), a non-final part below the 5MiB minimum
  (`EntityTooSmall`, matching AWS's own "all but the last part" rule),
  malformed completion XML (`MalformedXML`), `UploadPart`/repeat-
  `Complete`/repeat-`Abort` after the upload is already retired (all
  `NoSuchUpload` — not idempotent, matching real S3's behavior for a
  repeat completion/abort rather than treating it as a silent no-op), and
  `DeleteBucket` refused (`BucketNotEmpty`) while a multipart upload
  targeting it is still open.

### Phase G — crash/restart durability

All five required scenarios proven, at the `Store` level (direct
crash-injection via the existing `testHook`/`simulatedCrash` seam plus a
fresh `OpenStore` on the same directory to simulate a restart — the same
pattern every M1-M4 crash test already uses) and end-to-end over real HTTP
via the AWS SDK harness:

- **G1** (initiate → upload 2 parts → restart → `ListParts` → upload a 3rd
  part → complete → restart → `GetObject` → exact SHA-256 match):
  `TestMultipart_Crash_G1_RestartMidUploadThenResumeAndComplete` (Store
  level) and the `harness/m5b/multipart` AWS SDK harness (real HTTP,
  including a real process restart).
- **G2** (initiate → upload part 1 → replace part 1 → restart → complete →
  exact expected bytes): `TestMultipart_Crash_G2_
  ReplacePartThenRestartThenComplete`.
- **G3** (initiate → upload parts → abort → restart → upload ID remains
  invalid → no ordinary object visible): `TestMultipart_Crash_G3_
  AbortThenRestartUploadIDStaysInvalid`.
- **G4** (initiate → upload → complete → restart → ordinary object
  visible → incomplete session not resurrected): `TestMultipart_Crash_G4_
  CompleteThenRestartObjectVisibleSessionGone`.
- **G5** (crash during final publication → never a partially visible
  completed object): split into two tests proving both halves of the
  invariant. `TestMultipart_Crash_G5_
  CrashBeforeJournalCommitLeavesOldStateResumable` injects a crash at
  every durability boundary **before** the completion journal frame
  (chunk staging, after chunks published, after manifest published) and
  confirms the object is never visible and the upload stays resumable;
  `TestMultipart_Crash_G5_CrashAfterJournalCommitLeavesFullyCommittedObject`
  injects a crash **after** the journal sync genuinely succeeds (and
  again after the in-memory apply, before the response is even sent) and
  confirms the object is fully, durably visible and the session is
  retired — together proving there is no window with a partially visible
  completed object. (The gap between a journal frame's `Write` and its
  `Sync` is deliberately not simulated via an in-process restart here, for
  the same documented reason `TestCrash_AfterJournalWriteBeforeSync`
  already gives: once `WriteAt` returns, the bytes sit in the same page
  cache a fresh `*os.File` in this same process would read right back, so
  an in-process restart cannot honestly simulate a true crash in that
  specific instant — only real power loss can.)

### Phase H — concurrency/adversarial multipart tests

All run under `go test -race`, stress-repeated `-count=8` with zero
flakes: two `UploadPart` calls for the same part number (deterministic:
exactly one part survives, race-free); concurrent different part numbers
(all survive); `Complete` racing `UploadPart` (race-free; the object ends
up existing **xor** the upload stays resumable, never both); `Abort`
racing `UploadPart` (race-free, one deterministic outcome); two concurrent
`Complete` calls (exactly one succeeds, confirmed via a result channel —
not merely "no crash"); two concurrent `Abort` calls (exactly one
succeeds). Every one of these mutations goes through the same commit-time
re-validation pattern `commitObjectRoot` already established (heavy work
unlocked, re-check-and-commit under `Store.mu`), so the same architecture
that makes ordinary PUT/DeleteBucket races safe makes these safe too — no
new locking model was introduced.

### Phase I — real AWS SDK for Go v2 multipart proof

New harness, `zeros3-testing/harness/m5b/multipart` (same pinned SDK
versions as every prior milestone): **43/43 passed.** Full detail:
`zeros3-testing/results/M5B_MULTIPART_RESULTS.md`. Covers: create bucket;
initiate; upload 2 parts; `ListParts`; **kill and restart the zeros3
process against the same store directory** mid-lifecycle; `ListParts`
again (parts survived); upload a 3rd part; complete; `HeadObject`;
`GetObject` with exact SHA-256 equality; Range GET straddling a part
boundary; `CopyObject` of the completed object; a **second** restart plus
re-verified SHA-256; abort scenario (upload+abort+`ListParts`
rejected+`HeadObject` rejected); negative scenarios (nonexistent upload
ID, wrong ETag, empty part list); `ListMultipartUploads`. No internal
ZeroS3 API was used — every call is an ordinary SDK S3 client call.

### Phase J — real rclone multipart proof (1 GiB)

**PASS.** rclone `v1.75.0` (downloaded directly from
`downloads.rclone.org`, same pin as the earlier rclone pass), a genuine
**1 GiB (1073741824-byte)** random file, rclone's own default upload
cutoff/chunk size (200MiB/5MiB — not overridden, so multipart triggered
exactly the way it would for any real user, not by contriving a smaller
chunk size). Full detail: `zeros3-testing/results/
M5B_RCLONE_LARGE_OBJECT_RESULTS.md`.

- **Multipart genuinely triggered, measured directly:** the completed
  object's `HeadObject` ETag is `"b30575cd9e0c41bd4c1df0e8294bfea2-205"` —
  the `-205` suffix is ZeroS3's multipart ETag formula and matches
  1GiB÷5MiB rounded up to 205 parts exactly; a single-PUT object never
  produces this ETag shape.
- **Restart:** the zeros3 process was killed and restarted (same store
  directory, fresh port) immediately after upload; `HeadObject` against
  the restarted process reported the identical ETag/size.
- **Hash equality:** `sha256sum` of the original uploaded file and of a
  fresh `rclone copy` download (into an empty directory, to force a real
  transfer rather than rclone's same-file skip heuristic) after the
  restart are **identical**
  (`207fa80173baad31239c6a7d4cfb23939f1701474c1654b93499ea9c5417c7fe`).
- **This also resolves a previously-documented interoperability
  limitation:** the post-M4 pass recorded that rclone's own ordinary
  upload command could not complete against ZeroS3 at all, because
  rclone's generic transfer path wraps every upload body in a non-seekable
  reader and therefore requires `UNSIGNED-PAYLOAD`. Phase A2's
  `UNSIGNED-PAYLOAD` support directly closes this gap — confirmed here for
  both a small object and this 1 GiB multipart object, with **zero**
  server-side errors logged across the entire run
  (`RCLONE_RESULTS.md` is annotated with a pointer to this finding rather
  than being rewritten).
- **Largest black-box object tested this pass:** 1 GiB, SHA-256 exact
  match, as above.

### Phase K — conditional HMAC streaming: not required, not implemented

Neither eligible mode (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]`) was
observed from either real client exercised in this pass. The AWS SDK for
Go v2's `UploadPart`/`CreateMultipartUpload`/`CompleteMultipartUpload`
calls all used ordinary fixed `x-amz-content-sha256` (a literal digest);
rclone's generic transfer path used `UNSIGNED-PAYLOAD`, per its own
already-documented non-seekable-body behavior. Both harnesses' full server
logs contain zero `NotImplemented` rejections
(`grep -i error` over each is empty), confirming this is not merely an
absence of the harness trying hard enough — a completely ordinary,
unmodified client workflow for both tools never needed either streaming
mode. Per this task's own completion gate, HMAC streaming is therefore
correctly left **conditional and undone**: implementing it now would be
speculative work outside this pass's evidence, not something either real
client's normal operation actually requires.

### Phase L — large-object memory behavior

`CompleteMultipartUpload` never buffers the reconstructed object:
`multipartReader` reconstructs at most one CAS chunk (≤256KiB) at a time
from the ordered part list, and `chunkAndStoreStream` streams that through
the CDC chunker and CAS writer, accumulating only small chunk references
(sha256+length, not chunk bytes) plus a running SHA-256 hash state. The
1 GiB rclone proof (Phase J) exercises this path directly: at no point
does completing that object require holding anywhere near 1 GiB in a
single buffer, unlike an ordinary ~1 GiB single-shot `PutObject` would
(which still buffers its whole body — see "Known limitations" below,
carried over unchanged from M1-M4; fixing that path was explicitly out of
this pass's bounded scope, since multipart's whole purpose is to avoid
needing it for large objects in the first place). Individual `UploadPart`
requests still use the existing per-request body buffering (bounded by
`maxRequestBodySize`, 256MiB) — unchanged, and adequate for the ordinary
part sizes both real clients used (5-6MiB) in this pass's proofs.

### Phase M — dedup/CAS verification

`TestMultipart_DeepVerifyAfterCompletion` runs `Store.Verify(true)`
(structural + whole-object-digest re-hash) against a freshly-completed
multipart object and confirms it is fully clean — a completed multipart
object is indistinguishable from an ordinary PutObject object to `verify`,
`stats`, Range GET, and CopyObject (all exercised end-to-end in
`TestMultipart_HappyPath_TwoParts_GetHeadRangeCopy` and the AWS SDK
harness). `TestMultipart_CompletedObjectDedupsAgainstOrdinaryPutOfSameBytes`
(described under Phase B-F above) measures real CAS chunk reuse between a
multipart-completed object and an ordinary-PUT object sharing the same
logical bytes. **Actual chunk layout is not claimed identical** between
the two paths — completion re-chunks the true concatenation from scratch
(Phase B-F), so its chunk boundaries need not exactly match an ordinary
single-PUT's chunking of the same bytes chunk-for-chunk (both are valid,
deterministic outputs of the same CDC algorithm over the same bytes; CDC's
locality property is what still gives them most of their content in
common, which is exactly what the dedup test measures) — this is stated
accurately here rather than overclaimed.

### Phase N — regression validation

`gofmt -l .` clean; `go vet ./...` clean; `go test ./...` and
`go test -race ./...` both green; `go test -count=3 ./...` green (no
flakes); `go test -race -run 'TestMultipart_Concurrency|TestMultipart_
Crash' -count=8 -v ./...` green, no flakes across 8 repeated runs of every
new concurrency/crash test. **223 passing test cases** (`go test -v`
`--- PASS` lines, counting subtests), up from 182 at this pass's starting
snapshot (16 new payload-mode tests bring that to 197; the rest — 26 new
top-level multipart tests, several with subtests — bring it to 223).
Every M1-M5-A suite (SigV4 header/query auth, CRC32/Content-MD5,
CDC/CAS/manifest/journal, crash/recovery, concurrency, ListObjectsV2,
CopyObject, Range GET, presigning, virtual-host addressing) remains green,
unmodified in behavior.

**External regression, same pinned AWS SDK for Go v2 versions as every
prior milestone, rerun against this pass's binary:** M2 canonical workflow
**41/41**, M3 CopyObject **46/46**, M3 Range GET **27/27**, M3 dedup
evidence **7/7**, M5-A presign **47/47** — all unchanged, confirming
multipart's new journal record types/routing/payload-mode refactor
regressed nothing in the existing header-auth/path-style/presigned
surface. **Package Killer (`s3rver`) was not rerun this pass** — it
requires installing an additional npm package into a scratch environment
purely for comparison purposes, this pass's changes are already covered
end-to-end by the M2/M3/M5-A reruns plus the two new multipart harnesses
above (all of which exercise the identical header-auth code path Package
Killer's frozen test logic also uses), and the task's own guidance is
explicit that refreshing this unrelated presentation evidence must never
be allowed to threaten completing multipart itself. Its last recorded
result (`PACKAGE_KILLER_RESULTS.md`, unaffected by anything in this pass)
stands; a full rerun remains a reasonable candidate whenever a submission
freeze wants that evidence refreshed.

**Dependency proof:** regenerated (`deps-proof.txt`); `go list -deps .`
produces the exact same non-stdlib package set as before this pass (the
toolchain's own internally-vendored `golang.org/x/...`/
`crypto/internal/entropy` packages, the stdlib `uuid` package, and
`zeros3` itself) — multipart's new code uses only already-imported stdlib
packages (`bytes`, `crypto/md5`, `crypto/sha256`, `encoding/hex`,
`encoding/json`, `encoding/xml`, `strconv`, `strings`, `sort`, `time`),
confirmed by an actual first-path-segment-dot check (none found), not
merely re-asserted. `go.mod` still has no `require` block.

**Source-file invariant:** `zeros3.go` remains the sole implementation
file; `zeros3_test.go` remains test-only. No new `.go` file was added to
the ZeroS3 module (the new harness lives entirely under
`zeros3-testing/harness/m5b/multipart`, outside the ZeroS3 module).

**Reproducible build hash (refreshed, since `zeros3.go` changed):**

```
SHA-256 (copy A): 9e637369284cfcbfd333b74305c3852fd151f4b266f16355d93baeba9043d31a
SHA-256 (copy B): 9e637369284cfcbfd333b74305c3852fd151f4b266f16355d93baeba9043d31a
```

Confirmed byte-identical across two independently-copied source trees via
`scripts/reproducible_build.sh`, on `go1.27.0 linux/amd64`. Intentionally
different from the prior pass's hash (the source changed); that earlier
hash is stale evidence of the pre-M5-B binary, not a discrepancy.

### Persistent-format impact

**Journal record type space extended; every frozen v1 value unchanged.**
`store_format_version`, `cdc_format_version`, `manifest_format_version`
are all still `1`; the journal magic (`ZSJ1`), frame layout, CRC32C
checksum, and CDC/manifest parameters are byte-for-byte unchanged from
M1. What's new: four additional journal record type numbers (5-8) for
multipart upload session state, added the same additive way the original
four were defined — no existing record type was repurposed, and no new
top-level on-disk file/directory was introduced (multipart state lives
entirely inside the existing journal; part payload bytes live entirely
inside the existing CAS chunk store). **Forward-compatibility
consequence, by design:** an M1-M5-A binary opening a store whose journal
contains any of these new record types fails replay via the existing
"unknown record type" check in `replayJournal`, exactly like any other
genuinely unknown record type — it refuses to silently misinterpret
multipart state rather than risk corrupting or misreporting it. A store
that has never used multipart upload is byte-for-byte unaffected and
remains fully readable by an older binary.

### Known limitations

- `ListParts`/`ListMultipartUploads` do not implement pagination
  (`max-parts`/`part-number-marker`, `max-uploads`/`key-marker`/
  `upload-id-marker`) — every call returns the complete result in one
  page (`IsTruncated` always `false`). Not exercised as a gap by either
  real-client proof in this pass (a client that pages by following
  `NextPartNumberMarker` etc. simply receives everything on the first
  page and pages no further); a real limitation to fix if a future
  milestone needs to prove behavior with a very large part/upload count.
- `STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` remain unimplemented,
  conditionally, per Phase K above — not required by anything exercised
  this pass, and per this task's own completion gate not required for
  M5-B to be COMPLETE.
- `STREAMING-UNSIGNED-PAYLOAD-TRAILER` and SigV4A/ECDSA streaming
  (`STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD[-TRAILER]`) are permanently
  out of scope, per this task's own hard exclusions — recognized and
  rejected cleanly (`NotImplemented`), never implemented.
- Multipart part sizing enforces AWS's own "every part but the last must
  be ≥5MiB" rule; there is no configurable override.
- An ordinary single-shot `PutObject`'s body is still fully buffered in
  memory (bounded, 256MiB) — unchanged from M1-M4, and explicitly out of
  this pass's bounded scope (Phase L): multipart exists precisely so a
  large object need not go through that path at all, and this pass proved
  a 1 GiB object completing without a correspondingly large buffer via
  multipart specifically.
- Package Killer (`s3rver`) regression was not rerun this pass (see
  "Phase N" above); its last recorded result predates this pass and is
  unaffected by it.
- All limitations recorded in the "M5-A" and earlier sections below
  remain current and unchanged by this pass, except where this pass's
  own Phase J explicitly resolves one (rclone's ordinary-upload
  `UNSIGNED-PAYLOAD` requirement — see `RCLONE_RESULTS.md`'s pointer note
  and `M5B_RCLONE_LARGE_OBJECT_RESULTS.md`).

### Exact next milestone

Remaining M5/T2 optional-tier work by score/hour priority (internal
versions/restore, safe GC) if pursued at all, per `MILESTONES.md`'s
demotion rule; M6 delta sync remains untouched and out of scope until
T0-T2 stability is otherwise secure. `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`
support remains a well-scoped, low-risk candidate to promote later if a
specific real client is found to require it, but is not itself the
recommended next milestone given neither client exercised in this pass
needed it.

## M5-A — SigV4 query authentication, presigning, virtual-hosted addressing

**COMPLETE.** A tightly bounded pass, exactly per its own scope: SigV4
query-string authentication, presigned GET/PUT, the `zeros3 presign` CLI,
virtual-hosted-style addressing, focused adversarial testing, external AWS
SDK proof, and this evidence update. No multipart, versions/restore, GC,
stats/doctor changes, conditional requests, `aws-chunked`, or sync/
replication work was started.

- **Starting state:** `zeros3` branch `claude/zeros3-m5a-sigv4-presign-ejxf9y`
  was confirmed identical-tree to `main` at `d28d181e66b44d3a28ec1d4fdd0a7cfc4ee231ed`
  before any edit (`git diff origin/main` empty); `zeros3-testing`'s same-
  named branch was identical-tree to its own `main` at
  `4d35b5baefd84585c9fd5f21a2751faac68578ff`. Both were fetched fresh from
  `origin` rather than trusting a prior session's summary. The Linux
  regression baseline (`go test`, `go test -race`, `go vet`, `gofmt -l`) was
  green with 138 passing test cases before any change.
- **Resulting state:** this commit, on the same branch (`git log -1` names
  the exact SHA); `zeros3-testing`'s matching commit on its own
  same-named branch adds `harness/m5a/presign/main.go` and
  `results/M5A_PRESIGN_RESULTS.md` on top of the same starting point.
- **Architecture — one shared signing/verifying core.** `zeros3.go` section
  8 previously had a single `authenticate` doing Authorization-header
  SigV4 end to end. It is now `authenticate` (a two-line dispatcher),
  `authenticateHeader` (the original header-auth flow, behavior-preserving),
  `authenticateQuery` (new: parses `X-Amz-Algorithm`/`Credential`/`Date`/
  `Expires`/`SignedHeaders`/`Signature` from the raw query), and
  `sigv4VerifyCore` — the one place credential-scope checking, canonical-
  URI/header/request construction, HMAC signing-key derivation, and the
  constant-time signature compare actually happen, called identically by
  both paths. `zeros3 presign`'s `GeneratePresignedURL` calls the exact
  same `sigv4CanonicalURI`/`sigv4CanonicalQueryExcluding`/
  `sigv4SigningKey` primitives the server verifies with — there is one
  signing implementation, not a CLI-side reimplementation. Canonical query
  construction gained `sigv4CanonicalQueryExcluding` (the general form of
  the existing `sigv4CanonicalQuery`, which now delegates to it), used to
  drop exactly `X-Amz-Signature` from the query-auth canonical request.
  Query auth's payload hash is the fixed `UNSIGNED-PAYLOAD` sentinel real
  S3 presigned URLs and the AWS SDK for Go v2 presigner both use; header
  auth's exact-body-hash requirement is completely untouched. A new
  `sigv4Now` var (mirroring the existing `testHook` test-injection seam)
  lets expiry/skew tests set a fixed clock instead of sleeping.
- **Presigned GET/PUT:** implemented per `SIGV4_NOTES.md`'s frozen
  contract — `host` is the only signed header a generated URL uses (matching
  the AWS SDK presigner's own default), `X-Amz-Expires` is bounded to
  `1..604800` seconds (AWS's documented maximum), a request is valid
  through and including its exact expiry instant, `X-Amz-Date` more than 15
  minutes in the future is rejected, and `X-Amz-Security-Token` is
  explicitly rejected (`AuthorizationQueryParametersError`) rather than
  silently ignored, since ZeroS3 has no session/STS credential model.
- **`zeros3 presign get|put`:** a narrow, stdlib-only CLI subcommand
  (`-bucket`, `-key`, `-expires`, `-access-key`, `-secret-key`, `-region`,
  `-endpoint`, `-vhost`), following the existing `-flag`-based convention
  (`stats`'s `-bucket`/`-prefix`/`-key`) rather than inventing an `s3://`
  URI parser. Prints exactly one line (the URL) on success; never echoes
  the secret key, including on error. No credentials/profile subsystem was
  added.
- **Virtual-hosted-style addressing:** opt-in via `zeros3 serve -vhost-base
  <domain>` (default unset — path-style only, unchanged from before this
  pass). `Server.vhostBucketFromHost` extracts the bucket from `Host`
  *after* `authenticate` has already verified the signature over the
  original, unmodified `Host` header — bucket/key resolution never runs
  before authentication and never rewrites `r.Host` or the raw path.
  A `Host` without the configured suffix (bare IP, `localhost`, an
  unrelated domain, or the bare base domain alone) falls back to ordinary
  path-style parsing, unconditionally available on the same server.
- **Adversarial/regression tests added (44 new, all in `zeros3_test.go`,
  182 total passing test cases, up from 138):** canonical-query exclusion;
  valid presigned GET/PUT accepted; tricky paths (space, `+`, `%2F`,
  repeated `/`, trailing slash, Unicode) accepted; a presigned request with
  no signature at all falling through to (and correctly failing) the
  header path; tampered signature; modified path/bucket/Host/signed-query-
  parameter after generation; an unrelated query parameter appended after
  generation invalidating the signature (every query parameter is
  canonicalized, signed-header-related or not); wrong access
  key/region/service/algorithm; `host` missing from `SignedHeaders`;
  `X-Amz-Security-Token` rejected; duplicate query parameters rejected;
  a case-altered auth parameter name never silently authenticating; every
  required parameter missing (table test); malformed credential scope;
  malformed timestamp; malformed/negative/zero/over-range `X-Amz-Expires`
  (table test); the exact expiry boundary accepted and one second past it
  rejected (via `sigv4Now` injection, no sleeps); a future-dated URL beyond
  the skew window rejected; a 7-day (604800s) URL still valid one second
  before expiry; CLI/library-generated URLs passing the server's own
  verifier for both GET and PUT; expiry/bucket/key validated at generation
  time; the secret key never appearing in a generated URL; tampered-
  signature and expired presigned PUTs proven to leave the target object
  completely invisible over real HTTP; virtual-host bucket extraction
  (ordinary host, `bucket.base[:port]`, case-insensitivity, bucket names
  with dots/hyphens, the bare base domain, a bare IP/`localhost`, an
  unrelated host, a malformed/empty host) as a pure table test; virtual-
  host disabled-by-default fallback; a full virtual-hosted create-bucket/
  put/get/list/delete lifecycle over real HTTP; path-style continuing to
  work unchanged when virtual-host is configured; `ListBuckets` on the bare
  host still meaning "list buckets," not "bucket root of an empty bucket
  name"; a presigned URL generated with virtual-host addressing.
- **Regression:** `gofmt -l .` clean; `go vet ./...` clean; `go test
  ./...`, `go test -race ./...` both green; `go test -count=3 ./...` and
  `go test -race -run 'TestPresign|TestVHost|TestSigV4' -count=5 -v ./...`
  (270 pass, 0 fail) show no flakes in the new time/HTTP-dependent tests.
  Every existing M1-M4 suite (SigV4 header-auth adversarial matrix, CRC32/
  Content-MD5, CDC/CAS/manifest/journal, crash/recovery, concurrency,
  ListObjectsV2, CopyObject, Range GET) remains green, unmodified in
  behavior.
- **External AWS SDK for Go v2 proof** (new harness,
  `zeros3-testing/harness/m5a/presign`, same pinned versions as every prior
  milestone — `v1.45.1`/`config v1.33.1`/`credentials v1.20.1`/
  `service/s3 v1.109.1`/`smithy-go v1.28.1`): **47/47 passed.** Real
  `s3.PresignClient`-generated GET and PUT URLs fetched/uploaded with an
  ordinary `net/http.Client` carrying no S3-specific signing logic;
  byte/hash/Content-Type equality proven for both; negative cases (altered
  path, altered bucket, modified signed query parameter, modified
  signature, modified Host, expired URL, wrong credential scope/region)
  all correctly rejected; every negative presigned-PUT case independently
  confirmed via `HeadObject` to leave the target key completely absent;
  the `zeros3 presign` CLI binary itself exercised for both path-style and
  `-vhost` URLs, including a full virtual-hosted-style round trip (ordinary
  signed PUT/GET plus an SDK-presigned GET) using a redirect-dialing
  `http.Client` (no real DNS needed for the test domain — the same
  technique a real deployment would use actual DNS for).
- **Regression harnesses rerun against this pass's binary** (same pinned
  SDK versions): **M2 canonical workflow 41/41**, **M3 CopyObject 46/46**,
  **M3 Range GET 27/27**, **M3 dedup evidence 7/7** — all unchanged from
  the post-M4 pass recorded below, confirming header-auth path-style
  interoperability regressed in no way.
  rclone and Package Killer (`s3rver`) were **not rerun this pass** — both
  require installing external tooling into a scratch environment, and
  since this run touches only the authentication/addressing layer (already
  covered end-to-end by the M2/M3 reruns plus the new presign harness, all
  of which exercise the identical SigV4 header-auth code path those tools
  also use), a fresh install added meaningful runtime for no new coverage
  within this pass's explicit scope. Their prior results (below) are
  unaffected by anything changed in this pass; a full rerun is a
  reasonable candidate for the next milestone's regression pass if a
  submission freeze wants that evidence refreshed.
- **Dependency proof:** `go list -deps .` after this pass's changes
  produces the exact same non-stdlib set as before (the toolchain's own
  internally-vendored `golang.org/x/...`/`crypto/internal/entropy`
  packages, part of `net/http`/`crypto/tls`'s own implementation, plus the
  stdlib `uuid` package) — the new code uses only already-imported stdlib
  packages plus `net` (for `net.SplitHostPort`, used by virtual-host Host
  parsing). `go.mod` still has no `require` block.
- **Source-file invariant:** `zeros3.go` remains the sole implementation
  file; `zeros3_test.go` remains test-only. No new `.go` file was added to
  either repository's ZeroS3 module (the new harness lives entirely under
  `zeros3-testing/harness/m5a/presign`, outside the ZeroS3 module).
- **Persistent-format confirmation:** unchanged. `store_format_version`,
  `cdc_format_version`, `manifest_format_version` are all still `1`; the
  journal magic (`ZSJ1`), frame layout, CRC32C checksum, record type
  numbers, CDC parameters, and manifest field set are byte-for-byte
  unchanged. Every change in this pass lives entirely in the HTTP-request-
  layer authentication/addressing/CLI code; nothing in this pass reads or
  writes the store, journal, manifests, or chunks differently than before.
- **Known limitations (new/changed by this pass):**
  - Presigned URLs sign only `host` — no support for signing additional
    headers (e.g. a presigned PUT that also pins `Content-Type`).
  - `X-Amz-Security-Token` is rejected outright rather than supported —
    documented, not a silent gap: ZeroS3 has no session/STS credential
    model for it to validate against.
  - Virtual-host addressing is single-domain (`-vhost-base`), opt-in, and
    request-addressing only — no wildcard-TLS or DNS automation, per this
    pass's explicit scope.
  - rclone/Package Killer regressions were not rerun this pass (see above);
    their last recorded results (below) predate this pass and are
    unaffected by it.
  - All limitations recorded in the "M4 status" and "Post-M4/M5
    verification pass" sections below remain current and unchanged by this
    pass.
- **Exact next milestone:** remaining M5/T2 optional-tier work by score/
  hour priority (internal versions/restore, safe GC) if pursued at all,
  per `MILESTONES.md`'s demotion rule; M6 delta sync remains untouched and
  out of scope until T0-T2 stability is otherwise secure.

## Post-M4/M5 verification pass (T1 completion + Package Killer)

**COMPLETE.**

A bounded post-M4 correction, interoperability, and Package Killer
verification pass — not a general feature-expansion run. No presigned
URLs, versions/restore, GC, multipart, `aws-chunked`, or sync/replication
work was started.

- **Final ZeroS3 commit/state tested:** `1042dec8c15c054cd0c1353474131c8f24b31aec`
  on branch `claude/zeros3-post-m4-package-killer-9afzph`, itself branched
  from and identical-tree to `main` at kickoff of this pass (confirmed via
  `git diff origin/main` returning empty before any edit). This
  documentation update (and the reproducible-build-hash/deps-proof
  refresh above) is layered directly on top of that commit with **no
  further changes to `zeros3.go` or `zeros3_test.go`** — the code actually
  under test for every result below is exactly `1042dec8c1`.
- **Default/main verification:** confirmed directly against the GitHub
  API (`default_branch: "main"`), not assumed — no branch change was
  needed.
- **Correction items completed (Phase A):**
  - A2 — README now leads with explicit `0 Dependencies Hackathon —
    Track D: Data & Storage` positioning and an "At a glance" scorecard.
  - A3 — `S3_COMPAT.md` added (did not previously exist in this
    repository), reconciled against the actual current implementation,
    separating implemented/tested, deliberately unsupported, optional/
    later-tier, and compatibility-deviation behavior.
  - A4 — `DEMO.md`'s fixture generation no longer uses `/dev/urandom`
    (genuinely non-deterministic); replaced with a fixed-seed
    `math/rand` generator that reproduces byte-identical fixtures across
    runs (verified: two runs of the documented command produce the same
    SHA-256). The CopyObject zero-new-bytes proof no longer needs `jq` —
    replaced with a `grep -o` extraction of the one stable
    `chunk_store_file_bytes` field name `stats -json` guarantees.
    `.gitignore` gained `/demo-store/`, `fixture-*.bin`, and `*.bin`
    entries (previously **not** actually excluded despite DEMO.md's
    "keep fixtures out of git" instruction — a real gap, now closed).
- **Content-MD5 (T1, B1):** implemented (`validateContentMD5Header`,
  `zeros3.go` section 9) — previously entirely absent from the codebase.
  Validates the standard base64 MD5 value over the logical request
  payload; a malformed digest (bad base64, or valid base64 decoding to
  something other than 16 bytes) is reported as `InvalidDigest`, a
  well-formed digest that doesn't match as `BadDigest` — matching real
  S3's error-code split and never confused with CRC32/SigV4 payload
  SHA-256/CAS SHA-256/object SHA-256/ETag. No persistent-format change.
  10 new regression tests (valid; missing header unchanged behavior;
  mismatched; malformed base64; wrong-length decoded digest; coexistence
  with valid CRC32; failed-digest leaves no visible object over real
  HTTP; valid PUT succeeds with ETag unaffected). Also independently
  exercised over a real external client: the rclone harness's AWS-SDK
  seeding path sets `Content-MD5` explicitly and the Package Killer
  harness's `PutObject` calls go through the same validated path.
- **rclone result/version:** `rclone v1.75.0` (downloaded directly from
  `downloads.rclone.org`, not the stale distro-packaged `1.60.1`).
  **19/19 passed**, plus 2 honestly-documented, root-caused known
  limitations: rclone's own ordinary upload commands cannot complete
  against ZeroS3 because rclone's generic transfer path depends on
  `UNSIGNED-PAYLOAD` (rclone wraps every upload body in a non-seekable
  progress-accounting reader), which ZeroS3 deliberately does not
  support — the same documented, frozen M1 SigV4 boundary as presigned
  URLs and `aws-chunked`, both explicitly out of this pass's scope. This
  was verified directly (captured wire headers, tried
  `use_unsigned_payload=false`, confirmed rclone then fails locally with
  "request stream is not seekable" before any request reaches ZeroS3
  across four different rclone flag combinations), not assumed. Bucket/
  object lifecycle, listing, download, byte/hash equality, overwrite, and
  a full process restart were all proven through the real `rclone`
  binary (object bytes seeded/overwritten via the already-pinned AWS SDK,
  since rclone's own upload path cannot reach ZeroS3). Full detail:
  `zeros3-testing/results/RCLONE_RESULTS.md`.
- **Package Killer GO/NO-GO:** **GO.** One frozen AWS SDK for Go v2 test
  function, called unmodified against both ZeroS3 and s3rver (only
  endpoint/credential/addressing connection settings differed): **14/14
  passed on ZeroS3, 14/14 passed on s3rver**, covering every required
  criterion (CreateBucket/ListBuckets/DeleteBucket, Put/Get/Head/
  DeleteObject, ListObjectsV2 incl. prefix filtering, Content-Type +
  user metadata, ordinary signed requests) plus two honest, non-required
  differentiators (Range GET, CopyObject) that happened to pass on both.
  Full detail, exact re-checked s3rver facts, and reproduction commands:
  `zeros3-testing/results/PACKAGE_KILLER_RESULTS.md`.
- **s3rver version tested:** `3.7.1` — re-checked live against the npm
  registry and GitHub at submission time (not reused from the planning
  snapshot): still npm's `latest` tag, last published 2022-06-26, ~1.31M
  downloads/month, GitHub repository confirmed **archived**, 11 direct
  runtime dependencies (exact match to the prior planning-time count).
  Installed only into an ephemeral scratch directory outside both
  repositories — never inside the ZeroS3 Go module or its runtime.
- **Canonical AWS SDK results (rerun against `1042dec8c1`):** M2 canonical
  workflow **41/41 passed**; M3 CopyObject **46/46 passed**; M3 Range GET
  **27/27 passed**; M3 dedup evidence **7/7 passed** (97.5% edited-object
  reuse measured externally this run). Same pinned AWS SDK for Go v2
  versions as every prior milestone (`v1.45.1`/`config v1.33.1`/
  `credentials v1.20.1`/`service/s3 v1.109.1`/`smithy-go v1.28.1`).
- **Full Linux regression result:** `gofmt -l .` clean; `go vet ./...`
  clean; `go test ./...`, `go test -race ./...` both green, **200
  passing test cases** (`go test -v` `--- PASS` lines, counting subtests
  — up from 130 at the M3-correction-pass snapshot recorded below,
  reflecting real suite growth across the rest of M4 plus this pass's 10
  new Content-MD5 tests); stress confirmation `go test -count=5 ./...`,
  `go test -race -count=3 ./...`, and `go test -race -run
  'TestConcurrency_|TestCrash_' -count=8 -v ./...` all green, no flakes.
- **Dependency proof:** regenerated (`deps-proof.txt`); `go list -deps .`
  produces the exact same package set as before this pass (Content-MD5
  uses only the already-imported `crypto/md5`/`encoding/base64`) — no new
  import, stdlib-only, confirmed by an actual first-path-segment dot
  check (`(none found)`), not merely re-asserted.
- **Reproducible build hashes (refreshed, since `zeros3.go` changed):**
  ```
  SHA-256 (copy A): 770bb0eae8a659d92a1fd38dc7916c2ccb41cc142170bf3377a323e241ae0d53
  SHA-256 (copy B): 770bb0eae8a659d92a1fd38dc7916c2ccb41cc142170bf3377a323e241ae0d53
  ```
  Confirmed byte-identical across two separate runs of
  `scripts/reproducible_build.sh` (four total builds). Superseded, and
  intentionally different from, the pre-Content-MD5 hash
  `1e98c1d57e49855d509d84921d0c9b3c09aacb8ef7164b35549a358ea423daf9`
  recorded earlier in this file — that hash is now stale evidence of the
  pre-this-pass binary, not a discrepancy.
- **Persistent-format confirmation:** unchanged. `store_format_version`,
  `cdc_format_version`, `manifest_format_version` are all still `1`; the
  journal magic (`ZSJ1`), frame layout, CRC32C checksum, record type
  numbers, CDC parameters, and manifest field set are byte-for-byte
  unchanged. Content-MD5 validation happens entirely at the HTTP-request
  layer, before any chunking/manifest/journal work begins, and introduces
  no new journal record type, manifest field, or FORMAT.json change.
- **Known limitations (new/changed by this pass):**
  - rclone's own upload commands cannot complete against ZeroS3 (see
    "rclone result" above) — a verified, root-caused, and deliberately
    not-fixed limitation, not a defect.
  - All limitations recorded in the "M4 status" section below remain
    current and unchanged by this pass.
- **Exact next milestone:** M5/T2 optional-tier work by score/hour
  priority (presigned GET/PUT, internal versions/restore, safe GC,
  virtual-host addressing) — none started in this pass, per its explicit
  scope boundary.

## M4 status

**COMPLETE** (core submission scope; no T2+ optional-tier work started —
see "Optional tiers" below).

M4 is not a feature-expansion milestone: it exists to maximize
correctness confidence, zero-dependency proof, code quality,
reproducibility, documentation, and demo readiness on top of the M3
correction pass. Nothing in this section changed CopyObject/verify
behavior itself — see "M3 correction pass" (below) for that; M4 is the
release-proof/polish layer on top of it.

### M3 correction pass results (summary; full detail in that section)

- **A1 — CopyObject destination identity:** both `COPY` and `REPLACE`
  now publish a brand-new destination manifest (new UUID/version/
  `CreatedAt`); the claim is "zero new CAS payload bytes", not "zero
  bytes of any kind". Proven internally and externally (new
  `Last-Modified`, unchanged source).
- **A2 — encoded-copy-source handling:** `x-amz-copy-source` is decoded
  leniently (`lenientPercentDecode`), matching the pinned AWS SDK Go v2's
  actual raw (unencoded) wire behavior, confirmed by direct request
  inspection. No filesystem path cleaning.
- **A3 — deep object SHA verification:** `verify -deep` streams every
  reachable manifest's chunks, in order, through one SHA-256 hasher per
  manifest and checks the result against `object_sha256`/`total_length`.
- **A4 — per-root manifest hash verification:** `Verify`'s manifest
  cache no longer lets a second root sharing a manifest UUID skip its
  own journal-recorded-hash check.

### Toolchain

- `go version go1.27.0 linux/amd64` (resolved automatically via
  `GOTOOLCHAIN=auto` from `go.mod`'s `go 1.27.0` directive; also
  available as a pinned side-by-side install for reproducibility work).
- `CGO_ENABLED=0`; Linux amd64 is the release-blocking, fully crash/
  concurrency-tested platform.

### Tests

- `go test ./...`, `go test -race ./...`, and `go vet ./...` all pass, 0
  failures, `gofmt -l .` reports nothing to format.
- **130 passing test cases** (`go test -v` `--- PASS` lines, counting
  subtests), up from 122 at the end of M3 — the M3 correction pass added
  8 new top-level tests (several with subtests) for A1/A2/A3/A4.
- Repeated-run stress confirmation for M4: `go test -count=5 ./...`,
  `go test -race -count=3 ./...`, and `go test -race -run
  'TestConcurrency_|TestCrash_' -count=8 -v ./...` all green with no
  flakes observed.

### External interoperability

Tested against **zeros3 commit `7931b6d`** (branch
`claude/zeros3-m3-m4-corrections-62o7ir`) using
**zeros3-testing commit `ce01a5f0b48edfad413d9107bf91fd09927897eb`**
(same branch name in that repository), pinned AWS SDK for Go v2
`v1.45.1` (`service/s3` `v1.109.1`, `config` `v1.33.1`, `credentials`
`v1.20.1`, `smithy-go` `v1.28.1`):

| Harness | Result |
|---|---|
| M2 canonical workflow (`harness/m2`) | **41/41 passed** |
| M3 CopyObject (`harness/m3/copy`) | **46/46 passed** |
| M3 Range GET (`harness/m3/range`) | **27/27 passed** |
| M3 dedup evidence (`harness/m3/dedup`) | **7/7 passed** |

See `zeros3-testing/results/M3_CORRECTION_RESULTS.md` for the full
per-check output, including the re-run against the pre-correction-pass
commit that confirms the two new CopyObject assertion kinds are genuine
regression tests (3 failures there, matching exactly what A1/A2 predict).

### Zero-dependency proof

- `go.mod` has no `require` block; no `go.sum`; no `vendor/`.
- `CGO_ENABLED=0 go build .` succeeds.
- `go list -deps .` contains only Go standard-library packages, the
  toolchain's own internally-vendored `golang.org/x/...` packages (part
  of `net/http`/`crypto/tls`'s own implementation, not a ZeroS3
  dependency), the Go 1.27 standard library's own `uuid` package, and
  `zeros3` itself. Full generated evidence: `deps-proof.txt`.
- No `os/exec`/subprocess shell-out anywhere in `zeros3.go`.
- **Result: zero third-party runtime dependencies, confirmed.**

### Reproducible build

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
```

Two builds from two independently-copied source trees at two different
absolute paths, on `go1.27.0 linux/amd64`, produce byte-identical output:

```
SHA-256 (copy A): 770bb0eae8a659d92a1fd38dc7916c2ccb41cc142170bf3377a323e241ae0d53
SHA-256 (copy B): 770bb0eae8a659d92a1fd38dc7916c2ccb41cc142170bf3377a323e241ae0d53
```

Reproducible via `scripts/reproducible_build.sh` (no arguments; builds
twice and compares hashes automatically). **Result: reproducible,
confirmed** on this platform/toolchain pin.

### Persistent format

**Unchanged.** `store_format_version`, `cdc_format_version`,
`manifest_format_version` are all still `1`; the journal magic (`ZSJ1`),
frame version, header layout, CRC32C checksum, sequence semantics, and
the four record type numbers are byte-for-byte unchanged from M1; CDC
parameters, gear-table derivation, CAS layout, and the manifest field set
are unchanged. The M3 correction pass changed *when* and *how many*
manifests CopyObject publishes (always a new one now, for both
directives) but not the manifest v1 *shape* itself, and introduced no new
journal record type (CopyObject still commits through the existing
`recordTypePutObjectRoot`). **No frozen v1 format value has changed at
any point in this task.**

### Code quality / readability

`zeros3.go`'s existing top-to-bottom numbered-section structure (1–16,
plus `7b`/`9b` for two additions kept near their natural neighbor) was
reviewed as a fresh read, not just diffed: the one numbering gap
(`11b` with no `11`/`11a`) was fixed, six comments that read as
milestone-diary narration ("out of scope for M2", "not part of M2", "in
current (M1-M3) semantics") were reworded to state the permanent
design/invariant instead, and three near-identical repeated S3
NoSuchBucket-vs-InternalError error-mapping blocks were consolidated into
one small helper (`writeBucketOrInternalError`) without touching the two
call sites whose mappings are genuinely different (`DeleteBucket`,
`CopyObject`). No architectural changes, no new implementation files, no
persistent-format changes, no replaced locking model — all within Phase
A/M4's explicit "small helper extraction / behavior-preserving cleanup
only" bound.

### Known limitations

Current, real, and unchanged from M3 except where the correction pass
resolved something (noted inline in each relevant section above):

- Single writer process per store; no distributed/HA operation, no
  versioning/restore, no garbage collection, no multipart upload, no
  presigned URLs, no IAM/STS/KMS/ACL/policy engine.
- `CopyObject` does not implement conditional-copy headers or reject a
  same-key `COPY`-directive copy the way real S3 does in some cases.
- Range GET does not implement multipart/multi-range responses.
- The entire request body is buffered in memory (bounded, 256MiB) rather
  than fully streamed end-to-end.
- No real power-loss (hardware) testing beyond deterministic in-process
  crash injection and direct on-disk truncation.
- `.zero-dep.toml`: not created. Neither the supplied planning bundle nor
  any official hackathon instruction available in this environment
  defines its expected schema; per this task's own instruction (document
  the gap rather than invent fields), this is recorded here instead of
  fabricated.

### Optional tiers

**Not started, by design.** No T2/T3/T4/T5 work was begun in M4:
presigned URLs, internal versions/restore, destructive GC, JSON stats
polish beyond what already shipped in M3, virtual-host addressing,
`aws-chunked`/trailer checksum modes, multipart upload, the `s3rver`
Package Killer comparison, benchmark/doctor commands, and delta/sync
transfer are all untouched. Windows/macOS/arm CI was not set up.

## M3 status

**COMPLETE.**

M3's goal was to make the content-addressed CDC/CAS/manifest
architecture visibly, measurably pay off: dedup evidence, exact stats,
`verify`, then (T1) CopyObject and single-range GET. All of it landed
without changing any frozen v1 persistent-format value and without
regressing M1/M2.

### Repository structure

- `main` was created pointing at `d46e1dece63c8368e84e0b4afe429c903e5fb4cf`
  — the merge of the completed `claude/zeros3-m2-j83lvl` branch into the
  (then-default) `claude/zeros3-m0-m1-bootstrap-xvzbbm` branch. That merge
  commit's tree is byte-identical to `claude/zeros3-m2-j83lvl`'s tip, and
  `go test`/`go test -race`/`go vet` were re-run and confirmed green on it
  before `main` was cut. No milestone history was rewritten, squashed,
  rebased, or force-updated; `main` is a new ref onto existing history.
- M3 itself was implemented on a new branch,
  `claude/zeros3-m3-implementation-liru6u`, branched from `main` — not
  by adding commits to the old M2 branch.
- **GitHub default branch:** `main` is the repository's actual default
  branch (confirmed directly against GitHub, not inferred). No further
  action is needed here.

### Stats (`STATS_SPEC.md`)

Implemented as an exact scan/derivation pass (`Store.computeStats`,
zeros3.go section 12) over the journal-reconstructed namespace and a
filesystem walk — never a persisted counter, per `STORAGE_MODEL.md`'s
"prefer exact scans" rule. Fields and formulas follow `STATS_SPEC.md`'s
exact terminology:

- `bucket_count`, `current_object_count`, `version_count`,
  `logical_current_bytes`, `logical_version_bytes`,
  `logical_chunk_reference_bytes`, `logical_chunk_reference_count`,
  `scope_unique_chunk_bytes`, `scope_unique_chunk_count`,
  `scope_exclusive_chunk_bytes`, `scope_shared_chunk_bytes`,
  `unique_reachable_chunk_bytes`, `chunk_store_file_bytes`,
  `manifest_file_bytes`, `journal_file_bytes`, `temporary_file_bytes`,
  `reclaimable_bytes`, `actual_store_file_bytes`, `dedup_avoided_bytes`,
  `dedup_reduction`, `unique_to_logical_ratio`.
- `version_count`/`logical_version_bytes` are currently identical to
  `current_object_count`/`logical_current_bytes`: no version retention
  exists yet (PUT replaces a key's one visible root; DELETE removes it),
  so the only "retained committed version" for any key is its current
  one. This becomes a genuinely separate figure only if a future
  milestone adds retained-version semantics.
- Scope selection: whole store, one bucket, a bucket+prefix, or a single
  object (`statsScope`). Exclusive/shared is computed by checking, for
  every distinct chunk referenced in-scope, whether *any* object anywhere
  else in the store also references it — never inferred from a naive
  "store bytes minus unique bytes" subtraction.
- `*_file_bytes` fields are independent `os.Stat`/directory-walk
  measurements over `chunks/`, `manifests/`, `journal/`, `tmp/`, and
  `FORMAT.json` — deliberately never conflated with the logical/
  reference/unique fields above.
- JSON output (`stats -json`) with the field names above as stable keys;
  human-readable output uses the same underlying numbers with honest
  labels (never calling a shared chunk "physical bytes owned by" a
  bucket).
- Tested by hand-constructing an exact chunk-sharing arrangement (three
  chunks of known sizes shared across two buckets and three objects) and
  asserting every field's arithmetic by hand computation
  (`TestStats_ExactSharingArrangement`), plus a delete/reclaimable
  scenario (`TestStats_ReclaimableAfterDelete`) and a JSON stable-field-
  name check (`TestStats_JSONFieldNamesMatchSpec`).

### Verify

`Store.Verify` (zeros3.go section 13) never repairs or deletes anything;
it only reports, and returns a nonzero CLI exit status on any failure.

- **Store/journal:** `FORMAT.json`'s format/CDC/hash-algorithm versions;
  a fresh `replayJournal` pass over the journal file (magic/version/type/
  sequence/CRC32C), counting frames checked.
- **Manifests**, for every currently-reachable root, independently
  (**corrected by the M3 correction pass, A4** — see below): manifest
  file exists; its exact bytes' SHA-256 matches *this root's own*
  journal-recorded reference, checked every time regardless of whether
  the manifest UUID/bytes were already parsed via another root; it
  parses as JSON; its declared format/CDC/hash-algorithm versions are
  supported; its chunk references have well-formed SHA-256 hex and
  non-negative lengths; those lengths sum to the manifest's declared
  `total_length`.
- **Chunks**, referenced by every reachable manifest: basic verification
  checks the chunk file exists and its on-disk size matches the
  manifest's declared length (no content read); `-deep` additionally
  re-hashes the actual bytes and confirms they match the digest that
  names the file.
- **Whole-object digest, `-deep` only** (**added by the M3 correction
  pass, A3**): every reachable manifest's chunks are fed, in the
  manifest's own logical order, into one streaming SHA-256 hasher per
  manifest — never buffering the reconstructed object — and the result,
  plus the streamed byte count, is compared against the manifest's own
  `object_sha256`/`total_length`. A malformed `object_sha256` is
  reported as invalid rather than silently ignored. This is the one
  thing per-chunk hashing alone cannot catch: every chunk can be
  individually intact while the manifest still names the wrong object
  digest or lists chunks in a corrupted order.
- Reports counts (manifests/chunks checked, missing/corrupt/invalid,
  unreachable manifests/chunks, reclaimable bytes) and a per-issue list.
  Unreachable/reclaimable garbage is never treated as a failure — that is
  the expected result of "deletion changes roots, not chunks."
- Runs against the same private-snapshot concurrency policy already
  proven for `ListObjectsV2`/stats (`snapshotNamespace`, taken briefly
  under `Store.mu`, then read without holding the lock), so it is safe
  to run alongside writers: manifests/chunks are immutable and only ever
  superseded, never mutated in place.
- Tested: clean-store OK (both basic and deep); missing chunk detected;
  a same-length-but-corrupted chunk detected only under `-deep` (proving
  basic verification genuinely doesn't read content); a manifest whose
  bytes no longer match its journal-recorded SHA-256; an unparsable
  manifest JSON; a chunk file whose actual size disagrees with its
  manifest-declared length; a manifest whose chunk lengths don't sum to
  its declared total; `stats`/`verify` running concurrently with a
  writer loop under `-race` (`TestConcurrency_StatsDuringWrites`,
  `TestConcurrency_VerifyDuringWrites`); deep whole-object digest OK for
  an empty and a multi-chunk object, a tampered `object_sha256` detected
  only under `-deep`, and a malformed `object_sha256` reported as
  invalid (`TestVerify_DeepWholeObjectDigest_*`); an adversarial
  white-box case with two roots sharing one manifest UUID where only one
  root's recorded hash is wrong, proving the wrong root is still caught
  regardless of which root the (randomized) map iteration visits first
  (`TestVerify_PerRootManifestHashCheckedEvenWhenUUIDCached`).

### CopyObject (T1)

**Corrected by the M3 correction pass** (A1/A2 below) — this subsection
describes the current, corrected behavior; see "M3 correction pass" for
what changed and why.

`Store.CopyObject` (zeros3.go section 11) never re-chunks, re-reads,
re-uploads, or rewrites an existing CAS chunk file:

- Both metadata directives — default `COPY` and `REPLACE` — publish a
  **brand-new destination manifest**: new UUID, new version ID, new
  `CreatedAt`. A copy is a genuinely new object version with its own
  Last-Modified/version identity, even though its payload is
  byte-for-byte identical to the source's, so neither directive reuses
  the source's manifest file or timestamp.
- The chunk list, object SHA-256, and ETag are cloned byte-for-byte from
  the source manifest in both directives, without reading a single chunk
  payload byte. `ContentType`/`Metadata` are copied from the source for
  `COPY` and taken from the request for `REPLACE`.
- The measurable claim is **CopyObject writes zero new CAS payload
  bytes** — not "zero bytes of any kind": both directives now publish a
  small new manifest file, so `manifest_file_bytes` may grow even though
  `chunk_store_file_bytes` never does.
- Both paths share `PutObject`'s exact commit discipline via
  `commitObjectRoot` (extracted from `PutObject`'s former inline tail):
  bucket existence re-checked at the actual commit point, journal
  append+sync as the sole durability boundary, in-memory apply only
  after that succeeds.
- Before committing, every source chunk is confirmed present (a cheap
  `Stat`, not a re-hash — deep corruption detection stays `verify`'s
  job) so a copy is never rooted on a chunk that has gone missing.
- HTTP: `PUT` with `x-amz-copy-source` (leading slash optional,
  `?versionId=` rejected since ZeroS3 has no versioning),
  `x-amz-metadata-directive: COPY|REPLACE`, an S3-shaped
  `CopyObjectResult` (ETag + LastModified) on success. Missing source →
  `NoSuchKey`; missing destination bucket → `NoSuchBucket` (kept
  distinct from a missing source via a dedicated `errNoSuchDestinationBucket`
  sentinel so the handler always names the right resource).
- `x-amz-copy-source` is decoded leniently (`parseCopySource`/
  `lenientPercentDecode`), not with the request path's strict
  `url.PathUnescape`: the pinned AWS SDK Go v2 sends this header
  completely raw, with none of its own percent-encoding, so a strict
  decoder would reject the common case (a literal, non-escaped `%` in a
  source key). See "M3 correction pass" (A2).
- **Zero-new-CAS-payload claim, measured, not assumed:**
  `TestCopyObject_SameBucketZeroNewCASChunkBytes` snapshots `stats`
  before/after a same-bucket copy of a 3MiB object and asserts
  `chunk_store_file_bytes` is byte-for-byte unchanged while
  `manifest_file_bytes` grows (a new destination manifest is always
  published now);
  `TestCopyObject_MetadataDirectiveReplaceUsesNewMetadataZeroNewChunkBytes`
  does the same for REPLACE. `TestCopyObject_
  DestinationGetsNewManifestIdentityAndTimestamp` directly asserts, for
  both directives, that the destination gets a new manifest UUID/version
  ID and a strictly later `CreatedAt` than the source, that the payload
  identity (chunks/object SHA-256/ETag) is still cloned exactly, and
  that the source manifest's file bytes are completely untouched. The
  external `zeros3-testing` AWS SDK Go v2 harness (`harness/m3/copy`,
  46/46 passed) independently proves the same/cross-bucket/overwrite/
  missing-source/missing-destination/both-directive/new-Last-Modified/
  encoded-source-key behavior over the real S3 wire protocol.
- Crash/recovery coverage: a simulated crash after the REPLACE
  directive's manifest publish but before the journal commit leaves the
  destination invisible and the source untouched
  (`TestCrash_CopyObjectReplaceBeforeJournalLeavesOldState`); a
  simulated crash right after the journal sync (for both directives) is
  durable on restart (`TestCrash_CopyObjectAfterJournalSyncIsDurable`).

### Single-range GET (T1)

Implemented only after stats/verify/CopyObject were green, per the
required order.

- Supports `bytes=start-end`, `bytes=start-`, and `bytes=-suffix`; end is
  clamped to the object's actual length; a syntactically valid but
  unsatisfiable range (start at/past the object's end, a zero-length
  suffix, any range on a zero-length object) returns 416 with
  `Content-Range: bytes */<size>`; a malformed or multi-range header is
  ignored and the full object is served with 200, per RFC 7233's
  allowance for range forms a server doesn't support.
- `Store.readManifestRange` (zeros3.go section 14) walks the manifest's
  chunk-length list and reads only the CAS chunks overlapping the
  requested interval — never reconstructing the whole object first. A
  satisfiable request returns 206 with an exact `Content-Range`,
  `Content-Length`, and `Accept-Ranges: bytes` (also now sent on
  ordinary 200 GET/HEAD responses, advertising range support).
- Multi-range (`bytes=0-1,3-4`) is explicitly unsupported in M3, per
  plan; a request containing one is treated as "ignore Range."
- Tested: every supported range form (single byte at start/middle/end,
  open-ended, suffix, a region spanning a chunk-boundary-dense area,
  end-clamping, and the whole object expressed as an explicit range)
  against a 500KiB multi-chunk object with exact byte comparison; 416
  for an out-of-bounds range and for any range on an empty object;
  malformed and multi-range headers falling back to a full 200; a
  white-box test proving `readManifestRange` reconstructs exactly a
  requested interval straddling a known chunk boundary
  (`TestRange_ReadsOnlyOverlappingChunks`). The external
  `zeros3-testing` harness (`harness/m3/range`, 27/27 passed)
  independently proves the same forms plus a 416 case over the real S3
  wire protocol via the AWS SDK's own `Range` field.

### CDC / dedup evidence

Frozen CDC v1 parameters (16KiB min / 64KiB target / 256KiB max,
two-region Gear masks, deterministic table derivation) are unchanged —
no evidence of a correctness defect was found, so nothing was touched.

- **Identical-object reuse**, measured via real `PutObject` calls and
  `computeStats` (not invented numbers): uploading the same 6MiB object
  to a second key doubles `logical_current_bytes`/
  `logical_chunk_reference_{bytes,count}` while leaving
  `scope_unique_chunk_{bytes,count}` and `chunk_store_file_bytes`
  completely unchanged (`TestDedup_IdenticalObjectReuseAcrossKeys`);
  uploading the same content to a second *bucket* leaves the store-wide
  unique-byte total unchanged and makes every one of that content's
  chunks fully shared (zero exclusive bytes) in both buckets'
  scoped stats (`TestDedup_IdenticalObjectReuseAcrossBuckets`).
- **Edited-object reuse**, measured the same way: a 2MiB object edited
  with a 4001-byte insertion 50000 bytes from the start (deliberately
  *not* centered, so most of the file sits downstream of the edit,
  where fixed-size chunking fails hardest) showed CDC reusing **96.6%**
  of the edited object's bytes from the original upload, versus **0%**
  reuse for an independently-computed fixed-64KiB-chunk comparison over
  the exact same two byte strings (`TestDedup_
  EditedObjectReuseBeatsFixedSizeChunking`). The external
  `zeros3-testing` dedup demo (`harness/m3/dedup`) independently
  measured **97.5%** reuse for a similarly-shaped edit via the real S3
  API plus `stats -json` — a slightly different figure than the
  in-process Go test because the two use different random/deterministic
  corpora and edit offsets, not because either result is wrong.
- CAS missing/corruption detection remains green (unchanged M1 tests
  plus the new `verify`-based detection tests above).

### CLI

`zeros3 stats` and `zeros3 verify` (zeros3.go sections 15-16), stdlib
`flag`-based, human-readable by default with `-json` for stable-field
JSON. `stdout` carries the requested data; diagnostics go to `stderr`;
`verify` exits nonzero on any integrity failure. `zeros3 -store DIR
-addr ADDR` (no subcommand) keeps working exactly as before — it's
still the `serve` command by default — so the existing external harness
invocation form (`exec.Command(binPath, "-store", storeDir, "-addr",
addr)`) needed no changes.

### Tests

`go test ./...`, `go test -race ./...`, and `go vet ./...` all pass, 0
failures — **122 passing test cases** (`go test -v` `--- PASS` lines,
counting subtests) across the full M1+M2+M3 suite, all in
`zeros3_test.go`. All M1/M2 suites listed in earlier sections remain
green unmodified. `gofmt -l` reports nothing to format.

### Persistent-format impact

**No frozen v1 format changed.** `store_format_version`,
`cdc_format_version`, `manifest_format_version` are all still `1`; the
journal magic (`ZSJ1`), frame version, header layout, CRC32C checksum,
sequence semantics, and the four existing record type numbers are
byte-for-byte unchanged; CDC parameters, gear-table derivation, CAS
layout, and the manifest field set are unchanged. CopyObject's REPLACE
directive publishes manifests using the existing, unmodified manifest
v1 shape; no new journal record type was introduced (CopyObject commits
through the existing `recordTypePutObjectRoot`, exactly like an
ordinary PUT).

### Known limitations

- `version_count`/`logical_version_bytes` are not yet a distinct figure
  from `current_object_count`/`logical_current_bytes` (see "Stats"
  above) — expected, since no version-retention feature has shipped.
- **Resolved by the M3 correction pass (A3):** `verify -deep` now
  streams every reachable manifest's chunks through SHA-256 in manifest
  order and checks the result against `object_sha256`/`total_length`;
  this bullet is kept only as a historical record of the prior gap.
- CopyObject does not implement conditional-copy headers (`x-amz-copy-
  source-if-*`) or self-copy rejection; a same-key COPY-directive copy
  now publishes a genuinely new manifest/version/timestamp for that key
  (see the M3 correction pass, A1) rather than being rejected the way
  real S3 rejects certain same-key copies. Neither conditional headers
  nor self-copy rejection were in the M3 MUST scope.
- Range GET does not implement multipart/multi-range responses (`bytes=
  0-1,3-4`); such a header is treated as unsupported and the full object
  is served, per plan.
- The M1/M2 known-issues list (full in-memory request body buffering,
  non-power-loss-tested directory fsync, the "can't prove pre-sync
  absence" durability caveat) is unchanged and still applies.

### M4 status (superseded notice)

M4 is now complete — see the "M4 status" section at the top of this
file. This note is kept only as a historical record of the M3 snapshot:
at that point, reproducible-build finalization, the final dependency
proof, README/STDLIB/demo production, and the M3 correction pass itself
had not yet been started. T2+/optional-tier work (presigned URLs,
versioning/restore, destructive GC, multipart upload, `s3rver` Package
Killer, sync/delta-transfer, Windows/macOS/arm CI) remains not started,
by design.

## M3 correction pass

A follow-up, narrowly-scoped correction pass (targeted fixes only, no
general rewrite) fixed four confirmed issues in the M3 CopyObject/verify
implementation:

1. **A1 — CopyObject destination identity.** The original default `COPY`
   directive committed the destination root pointing at the source's own
   manifest UUID/SHA-256 unchanged, which meant the destination's
   Last-Modified/version identity was actually the source's — a real
   correctness defect, since a copy is a genuinely new object version.
   Both `COPY` and `REPLACE` now always publish a brand-new destination
   manifest (new UUID, new version ID, new `CreatedAt`), cloning the
   payload identity (chunk list, object SHA-256, ETag) byte-for-byte
   from the source without reading a single chunk payload byte. The
   measurable claim changed from "CopyObject writes zero new bytes of
   any kind" (COPY only) to the correct, narrower claim that holds for
   *both* directives: **CopyObject writes zero new CAS payload bytes**.
   See "CopyObject (T1)" above for full detail; tests:
   `TestCopyObject_SameBucketZeroNewCASChunkBytes`,
   `TestCopyObject_DestinationGetsNewManifestIdentityAndTimestamp`.
   Externally: `zeros3-testing`'s `harness/m3/copy` now asserts the
   destination's `Last-Modified` is strictly after the source's (and
   that the source's own `Last-Modified` never moves), over real HTTP.
2. **A2 — `x-amz-copy-source` decoding.** Direct wire inspection of the
   pinned AWS SDK Go v2 (a real `CopyObject` call captured against a raw
   HTTP server) showed it applies **zero percent-encoding of its own**
   to `CopySource`: raw spaces, `%`, `+`, `?`, `#`, and Unicode are all
   sent completely unescaped. The original `parseCopySource` reused
   `splitBucketKey`'s strict `url.PathUnescape` (correct for a request
   *path*, which the HTTP client library itself guarantees is
   well-formed) — so a source key with a literal, non-percent-encoded
   `%` (a routine, real case, not a contrived one) was rejected outright
   with `InvalidArgument`. `parseCopySource` now decodes leniently via a
   new `lenientPercentDecode`: well-formed `%XX` escapes are still
   honored, but a `%` that isn't part of one is kept literal instead of
   erroring, `+` is never treated as a space, and the bucket/key split
   still happens on the first *raw* `/` — never `path.Clean`/
   `filepath.Clean`, so `..`, `//`, and slash-containing keys are
   preserved exactly. `versionId` stays rejected/unsupported. Tests:
   `TestParseCopySource_TrickyKeys` (unit-level table),
   `TestCopyObject_TrickySourceKeys` (end-to-end over real signed HTTP,
   RAW unencoded header values matching the SDK's actual wire form).
   Externally: `zeros3-testing`'s `harness/m3/copy` adds the same tricky
   source keys (space, literal `%`, `+`, a slash-containing key) as
   black-box `CopyObject` cases.
3. **A3 — Deep verify whole-object digest.** `verify -deep` re-hashed
   individual chunks but never independently verified the manifest's own
   `object_sha256` against the reconstructed object — a manifest could
   name the wrong object digest, or list otherwise-intact chunks in a
   corrupted order, and nothing would ever notice (`GetObject` doesn't
   check `object_sha256` either). `Store.Verify` now streams every
   reachable manifest's chunks, in the manifest's own order, through one
   `sha256.New()` hasher per manifest — never buffering the
   reconstructed object — and compares the result and the streamed byte
   count against `object_sha256`/`total_length`; a malformed
   `object_sha256` is reported `invalid` rather than silently ignored.
   Tests: `TestVerify_DeepWholeObjectDigest_EmptyObject`,
   `_MultiChunkObject`, `_MismatchDetected`,
   `_MalformedReportedInvalid`.
4. **A4 — Per-root manifest hash verification.** `Verify`'s manifest
   cache (keyed by manifest UUID, to avoid re-reading/re-parsing a
   manifest file every time a root references it) had a real bug: the
   journal-recorded-SHA-vs-actual-file-SHA check only ran on a cache
   *miss*. If manifest UUID `M` was correctly referenced by one root and
   incorrectly (wrong recorded hash) referenced by a second root, and
   Verify happened to process the correct root first, the second root's
   wrong hash was never checked at all — it silently inherited the first
   root's "verified" status. The cache now stores the manifest's parsed
   content and its own file-bytes SHA-256 only; the
   journal-recorded-SHA-vs-cached-SHA comparison runs unconditionally
   for every root, cache hit or miss. Test:
   `TestVerify_PerRootManifestHashCheckedEvenWhenUUIDCached` — a
   white-box adversarial case (two roots sharing one manifest UUID, one
   with a deliberately wrong recorded hash) confirmed, against the
   pre-fix code, to fail reliably (the wrong root's mismatch went
   undetected whenever Verify happened to process the correct root
   first) and to pass reliably against the fix, across many runs and
   both processing orders.
5. **A5 — STATUS accuracy.** The stale note claiming a human still
   needed to change GitHub's default branch was removed; `main` is
   confirmed to already be the default branch.

Also completed: a small, behavior-preserving code-quality pass extracted
`writeBucketOrInternalError` to remove three near-identical repeated
NoSuchBucket-vs-InternalError S3-error-mapping blocks (`handlePutObject`,
`handleDeleteObject`, `handleListObjectsV2`); `DeleteBucket` and
`CopyObject`, whose error mappings are genuinely different (not just the
resource string), were deliberately left as their own switches rather
than forced through one generic helper.

No frozen format value changed by this pass: store/CDC/manifest/
journal-frame versions, the `ZSJ1` magic, record type numbers, CDC
parameters, and the gear-table derivation are all exactly as before;
CopyObject still commits through the existing `recordTypePutObjectRoot`,
no new journal record type was introduced. `go test`, `go test -race`,
and `go vet` all pass with **130 passing test cases** (up from 122),
`gofmt -l` reports nothing to format, and all four preserved external
`zeros3-testing` harnesses (M2: 41/41, M3 copy: 46/46, M3 range: 27/27,
M3 dedup: 7/7) are green.

## M2 status

**COMPLETE.**

All M2 exit-contract requirements are implemented and covered by passing
tests (`go test`, `go test -race`, `go vet` all clean), plus a pinned,
external AWS SDK for Go v2 canonical workflow run outside the repository.
M3+ work was not started (see "M3 NOT STARTED" below).

### Added

S3 operations, on top of M1's CreateBucket/PutObject/GetObject:

- **ListBuckets** (`GET /`): S3-shaped `ListAllMyBucketsResult` XML,
  buckets sorted by name for deterministic ordering, `CreationDate` taken
  from the bucket's own CreateBucket journal record.
- **HeadBucket** (`HEAD /bucket`): 200 with no body for a visible bucket,
  404 with no body (not even an XML error) for a missing one.
- **DeleteBucket** (`DELETE /bucket`): succeeds only for a currently
  visible, empty bucket (204 No Content); `NoSuchBucket` for a missing
  bucket, `BucketNotEmpty` for a non-empty one. Journal-backed via the
  M1-reserved record type `4`; only removes the bucket from the
  journal-derived namespace, never touches chunk/manifest files.
- **HeadObject** (`HEAD /bucket/key`): the same headers GetObject sends
  (Content-Length, Content-Type, ETag, Last-Modified, every
  `x-amz-meta-*` key) with no body; missing bucket/key returns 404 with
  no body. Reads the cached namespace entry for size/ETag/Content-Type
  and the object's manifest (once) for metadata/timestamp -- never reads
  chunk data, since a HEAD response never has a body.
- **DeleteObject** (`DELETE /bucket/key`): removes the key's visible root
  (204 No Content); deleting an already-absent or never-existed key is
  idempotent success, matching the supported non-versioned subset.
  Journal-backed via the M1-reserved record type `3`. No delete markers,
  no versioning.
- **ListObjectsV2** (`GET /bucket?list-type=2...`): the planned ESSENTIAL
  subset -- `prefix`, `delimiter`/`CommonPrefixes`, `max-keys` (default
  and hard cap 1000, `max-keys=0` returns an empty non-truncated page),
  `continuation-token`/`NextContinuationToken`, `KeyCount`, `IsTruncated`,
  UTF-8 byte lexical key ordering (plain Go string comparison, which is
  exactly byte-lexical since Go strings are byte sequences), XML escaping
  via stdlib `encoding/xml`. Continuation tokens are opaque, versioned,
  base64 values encoding only the last consumed *key* (never a
  filesystem path, chunk hash, or manifest UUID); an unsupported version
  or unparseable token is rejected as `InvalidArgument`. The legacy V1
  listing shape (no `list-type=2`) is explicitly rejected rather than
  silently misinterpreted.
- **Object metadata/Content-Type round-trip fix**: M1's manifest already
  stored `Content-Type` and `x-amz-meta-*`, but `GetObject`'s HTTP
  handler never surfaced metadata headers back to the client. GET/HEAD
  now both read the verified manifest and emit every `x-amz-meta-*`
  header plus `Last-Modified` (from the manifest's `created_at`).

### Concurrency model

**Store.mu is the single writer lock for the visible bucket/object
namespace.** Every mutation that changes what's visible --
`CreateBucket`, `DeleteBucket`, `DeleteObject`, and the final
journal-append + namespace-apply step of `PutObject` -- holds `Store.mu`
across both its journal append and its namespace update, so the two
always happen atomically together and journal sequence order always
matches namespace-apply order.

`PutObject`'s slow, CPU/IO-heavy work (CDC chunking, CAS chunk writes,
manifest publication) deliberately runs *without* holding `Store.mu`, so
multiple concurrent PUTs can prepare their immutable data in parallel;
only the brief final commit (journal append + `s.buckets[...].objects[...]`
update) is serialized. This is inherited from M1 unchanged.

**M2 correctness fix (found while adding DeleteBucket):** `PutObject`'s
final commit now re-checks that its target bucket still exists *inside*
the same locked section that appends the journal frame, not just once at
entry. Before this fix, a `DeleteBucket` racing a slow `PutObject` to the
same (now-empty) bucket could remove the bucket between `PutObject`'s
initial existence check and its final commit, causing a nil-map write
(`s.buckets[bucket]` is nil) and a journal frame that would fail replay
(`applyRecord` requires the bucket to exist for a put-object-root). The
fix closes the race with no new locks: whichever operation's commit
critical section runs first under `Store.mu` wins, and the loser gets a
normal, coherent error (`NoSuchBucket` for the PUT, `BucketNotEmpty` for
the DeleteBucket) instead of corrupting anything. Covered by
`TestConcurrency_DeleteBucketVsPutObjectResolvesCoherently` (20 trials
under `-race`).

Readers (`GetObject`/`HeadObject`/`ListObjectsV2`) take `Store.mu` only
for their namespace lookup/snapshot, then read immutable manifest/chunk
data afterward without holding it -- safe because manifests and chunks
are never mutated in place, only ever superseded by a new root a
concurrent writer publishes under its own fresh UUID/digest. **Required
invariant, tested under `-race`:** a reader observes a complete old
object or a complete new object for a given key, never a mix of the two
(`TestConcurrency_SameKeyPutPutSerializesToOneCompleteVersion`,
`TestConcurrency_SameKeyPutVsDeleteNeverMixed`,
`TestConcurrency_GetDuringOverwriteSeesCompleteObject`).

**ListObjectsV2 concurrent-mutation policy** (documented in code next to
`Store.ListObjectsV2`): each call takes a private, consistent snapshot of
the bucket's key set under `Store.mu` at the start of that one call, then
does all filtering/sorting/grouping/pagination against that snapshot
without holding the lock -- so a single call never sees a torn view.
Across separate calls in a paginated `ContinuationToken` sequence, ZeroS3
makes no cross-call isolation guarantee, matching real S3: a PUT/DELETE
landing between two page requests is reflected in the next page as of
that later call. What pagination never does, even under concurrent
mutation, is duplicate or corrupt a result within a stable-state
sequence (no mutations between calls) -- covered by
`TestListObjectsV2_MaxKeysOnePagination` and
`TestListObjectsV2_PaginationNoDuplicateOrSkipWithDelimiter`.

### Canonical interoperability

External, ephemeral harness (`/tmp/zeros3-sdk-harness`, not part of this
repository, not a ZeroS3 runtime dependency) exercising a real
`zeros3-bin` server built from this repo as a black-box S3 endpoint:

- **Pinned module versions** (from the harness's own `go.mod`, never
  copied into this repo):
  - `github.com/aws/aws-sdk-go-v2 v1.45.1`
  - `github.com/aws/aws-sdk-go-v2/config v1.33.1`
  - `github.com/aws/aws-sdk-go-v2/credentials v1.20.1`
  - `github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1`
  - `github.com/aws/smithy-go v1.28.1`
  - (plus their own transitive `sso`/`ssooidc`/`sts`/`signin`/`imds`
    dependencies, all `// indirect` and irrelevant to the S3 wire
    protocol exercised here)
- **Endpoint/addressing:** `s3.Options.BaseEndpoint` set to the running
  `zeros3-bin`'s local `http://127.0.0.1:<port>`, `UsePathStyle: true`.
  Credentials via `credentials.NewStaticCredentialsProvider` with
  ZeroS3's default keypair; config loaded through the ordinary
  `config.LoadDefaultConfig` path (not a hand-built `aws.Config{}`
  literal), which is what actually resolves the SDK's default
  integrity-behavior knobs.
- **Actual default wire behavior, inspected before assuming anything**
  (see "investigation" below): the pinned SDK's default
  `RequestChecksumCalculation` resolves to `WhenSupported`, and for an
  ordinary `PutObject` with a seekable `bytes.Reader` body it sends a
  **plain header**, `X-Amz-Checksum-Crc32: <base64 CRC32>` -- included in
  `SignedHeaders`, computed over the literal request payload, sent
  alongside a literal (non-`UNSIGNED-PAYLOAD`) `X-Amz-Content-Sha256`.
  **No `aws-chunked` framing, no streaming trailer, no
  `x-amz-sdk-checksum-algorithm` header appeared.** This is exactly the
  ordinary header-form CRC32 path ZeroS3 M1 already implemented
  (`validateCRC32Header`), so **no new protocol/framing/checksum support
  was required or added for M2** -- no compatibility behavior was
  promoted from T3. `ResponseChecksumValidation` also defaults to
  `WhenSupported`, but the SDK only *validates* a response checksum if
  the server actually sends one; ZeroS3 doesn't (M1/M2 scope), and the
  SDK degrades to a harmless `WARN: Response has no supported checksum.
  Not validating response payload.` rather than failing the request --
  confirmed empirically against the running server, not just by reading
  SDK source.
  - Investigation note: an *older* pinned `config` module
    (`v1.28.6`, current at M1 kickoff) predates the resolver that
    defaults `RequestChecksumCalculation`/`ResponseChecksumValidation` to
    `WhenSupported` at all -- with that version, `config.LoadDefaultConfig`
    leaves both `Unset`, and the SDK sends **no checksum at all** by
    default. The harness's pinned versions above (`config v1.33.1`) are
    the ones that actually reproduce "modern default SDK behavior"; this
    was confirmed by diffing raw requests (via a header-dumping stand-in
    HTTP server) between the two `config` versions before picking the
    pin, per the "inspect exact wire behavior first" failure policy.
- **Exact canonical workflow passed** (41/41 checks,
  `/tmp/zeros3-sdk-harness` `go run .`): ListBuckets on an empty store →
  CreateBucket → HeadBucket → PutObject (default checksum behavior,
  Content-Type + 2 metadata keys) → HeadObject (Content-Length/
  Content-Type/metadata verified) → GetObject (exact byte equality +
  Content-Type + metadata verified) → 5 more PutObjects under a shared
  prefix → ListObjectsV2 (no params: KeyCount 6; `prefix`+`delimiter`:
  sees all 5 `logs/` entries; `max-keys=2` paginated across 3 pages,
  covering all 6 keys with no duplicates/skips) → DeleteObject + confirm
  `HeadObject` now errors → DeleteBucket (after clearing remaining
  objects) + confirm `HeadBucket` now errors → a separate
  restart-persistence run (CreateBucket → PutObject a 400,000-byte
  object → kill the `zeros3-bin` process → start a fresh `zeros3-bin`
  against the same store directory → GetObject returns byte-identical
  data).
- No compatibility behavior needed promotion from T3 to M2/T0 (see
  above); the SigV4 raw-path adversarial suite was re-run unmodified and
  is green (see "Tests" below).

### Tests

`go test ./...`, `go test -race ./...`, and `go vet ./...` all pass, 0
failures. All M1 suites listed in the M1 status section remain green
unmodified (crash/recovery, journal framing/corruption, SigV4
adversarial/raw-path, CRC32, CDC/CAS/manifest). M2 adds:

- **Buckets**: ListBuckets empty/non-empty/deterministic order,
  HeadBucket present/missing, DeleteBucket empty-succeeds/
  missing-is-NoSuchBucket/non-empty-fails, DeleteBucket survives restart,
  recreate-after-delete starts empty.
- **Objects**: HeadObject headers-with-no-body and missing-bucket/key,
  GetObject metadata + Content-Type round trip (including a
  header-case-insensitivity check), DeleteObject existing/idempotent-
  missing/never-existed/missing-bucket, DeleteObject survives restart,
  shared CAS chunks stay readable through a second object after deleting
  the first.
- **ListObjectsV2**: empty bucket, UTF-8 byte lexical ordering
  (including Unicode and space-containing keys), XML-special-character
  key round trip, prefix + delimiter + CommonPrefixes counting,
  `max-keys=0`, `max-keys=1` full pagination with no duplicate/skip,
  default and clamped-to-1000 `max-keys`, invalid continuation token
  rejected as `InvalidArgument`, no-duplicate/no-skip pagination across a
  mixed direct-key/common-prefix sequence.
- **Journal**: delete-object-root replay, delete-bucket replay, a mixed
  create/put/delete/delete/recreate/put sequence surviving restart with
  exactly the expected keys present/absent.
- **Concurrency** (all run under `go test -race`): same-key PUT-vs-PUT
  (20 concurrent writers, final GET matches exactly one complete written
  version), same-key PUT-vs-DELETE (interleaved, final state is always a
  complete old object, complete new object, or cleanly absent -- never
  mixed/corrupt), GET-during-overwrite (50 GETs racing a tight PUT loop,
  every GET returns one of the two complete versions), DeleteBucket-vs-
  PutObject (20 trials, always resolves to one coherent outcome with no
  panic).
- **Interoperability**: the external AWS SDK Go v2 harness described
  above; not duplicated into `zeros3_test.go` and not a repository
  dependency.

### Persistent-format impact

**No frozen v1 format changed.** `store_format_version`,
`cdc_format_version`, `manifest_format_version` are all still `1`; the
journal magic (`ZSJ1`), frame version, header layout, CRC32C checksum,
and sequence semantics are byte-for-byte unchanged; CDC parameters,
gear-table derivation, CAS layout, and manifest field set are unchanged.
The only journal-level change is *activating* the two record types M1
explicitly reserved for this: `3 = DeleteObjectRoot` and
`4 = DeleteBucket`, exactly as planned -- no new record type numbers, no
repurposed ones.

### Known limitations

- ListObjectsV2's `StorageClass` field is a hardcoded `"STANDARD"`
  literal (no real storage classes exist in ZeroS3); harmless but not a
  meaningful signal.
- `encoding-type=url` is not implemented (not required by the canonical
  client or the M2 essential subset; XML escaping alone was sufficient
  for every tested key, including XML-special characters).
- ListObjectsV2 reads one manifest file per *listed* Contents entry (to
  get `LastModified`), bounded by page size (≤1000), not by total object
  count; acceptable at M2 scope, a possible optimization target later.
- Deleted buckets/objects' chunks and manifests are never reclaimed (by
  design -- GC is explicitly out of scope for M2); a long-running store
  with many deletes will accumulate unreachable chunk/manifest files
  until a future GC milestone.
- The M1 known-issues list (full in-memory request body buffering,
  non-power-loss-tested directory fsync, the "can't prove pre-sync
  absence" durability caveat) is unchanged and still applies.

## M1 status

**COMPLETE.**

All M1 exit-contract requirements are implemented and covered by passing
tests, including under `go test -race`. No M2/optional work was started
(see "M2 NOT STARTED" below).

## M1 correction pass

A follow-up, narrowly-scoped correction pass fixed two issues found in the
original M1 implementation:

1. **UUID generation.** The original pass incorrectly claimed Go 1.27's
   standard library has no UUID package (an earlier `grep` search missed
   it past a `head -50` cutoff). Go 1.27 does ship a stdlib `uuid` package
   (`uuid.NewV7`, `uuid.Parse`, `UUID.String`). `newUUIDv7()` now wraps
   `uuid.NewV7().String()` instead of hand-rolling UUIDv7 from
   `crypto/rand` + `time`. The output format is unchanged (canonical
   lowercase 8-4-4-4-12 hex), so this does not alter the manifest or
   `FORMAT.json` on-disk representation, and existing M1 stores remain
   readable. `crypto/rand` was dropped from `zeros3.go` since nothing else
   used it. Added tests: `TestUUID_CanonicalStringForm`,
   `TestUUID_IsVersion7Variant10`, `TestUUID_RoundTripsThroughStdlibParse`,
   `TestUUID_Unique`, `TestUUID_SuitableAsManifestAndVersionID`.
2. **Pre-sync durability semantics and journal poisoning.** The original
   comments overclaimed that "a crash before sync completes must never
   make its object visible after restart" — not something an in-process
   test (or, for that matter, `fsync`) can actually prove, since a reader
   sharing the same OS page cache sees written-but-unsynced bytes
   regardless of whether `Sync()` ran; `fsync` only matters for surviving
   a real power loss. The comments and tests now state the real,
   provable contract (see "Durability contract" below), and
   `Journal.appendFrame` now poisons the journal (records the first
   write/sync failure, guarded by the existing mutex) so that a process
   which hits an uncertain journal I/O failure refuses every further
   mutation until the store is reopened, rather than risking a write at a
   stale offset or a reused sequence number. Reads are unaffected. Added
   tests: `TestJournal_PoisonedAfterWriteFailure`,
   `TestCrash_SyncFailureRecoveryIsHonest`, and extended
   `TestCrash_AfterJournalWriteBeforeSync` with poisoning assertions.

No frozen format value changed: store/CDC/manifest/journal-frame versions,
the `ZSJ1` magic, record type numbers (including the reserved 3/4), CDC
parameters, and the gear-table derivation are all exactly as before.

## Exact toolchain

- `go version go1.27.0 linux/amd64` (installed fresh for this milestone;
  the environment originally shipped Go 1.24.7 as default, which does not
  satisfy the "Go 1.27.x" hard constraint, so Go 1.27.0 was downloaded
  from `go.dev/dl` and made the active toolchain).
- `go.mod`: `module zeros3`, `go 1.27.0`, **no `require` block**.
- Build: `CGO_ENABLED=0 go build .` — stdlib only, no cgo.
- Platform: Linux amd64 only (the only release-blocking platform for M1,
  per scope).

## Implemented

- **Store**: `store/FORMAT.json` (immutable, versioned), directory layout
  (`journal/`, `chunks/`, `manifests/`, `tmp/`), clean rejection of an
  unsupported format version.
- **CDC v1**: streaming Gear-hash content-defined chunking. Min 16KiB /
  target 64KiB / max 256KiB, FastCDC-style two-mask normalization
  (~1/65536 boundary probability below target, ~1/32768 above), forced
  cut at the max. Gear table is 256 deterministic 64-bit entries derived
  from SHA-256 of a fixed, version-tagged seed (`ZeroS3/CDCv1/GearTable`).
- **CAS**: SHA-256 content-addressed chunks at
  `chunks/<aa>/<bb>/<64-hex>`, dedup on write, hash re-verification on
  every read (corruption is detected, never trusted blindly).
- **Manifests v1**: immutable JSON, UUIDv7 identity (generated by the Go
  1.27 standard library's `uuid` package), ordered chunk list, object
  SHA-256, MD5-based single-part ETag, sorted metadata, referenced from
  the journal by both UUID and the SHA-256 of the manifest's exact bytes.
- **Journal v1**: append-only binary log (`ZSJ1` magic, CRC32C-checksummed
  frames), replay-based namespace reconstruction, torn-tail tolerance
  limited strictly to a byte-count-incomplete final frame, hard failure on
  any other corruption (bad magic/version/type, CRC mismatch, sequence
  gap/duplicate). A write or sync failure poisons the journal (see
  "Durability contract" below) so the process can't keep mutating against
  an uncertain tail.
- **HTTP layer**: a custom root `http.Handler` (no `http.ServeMux`) that
  reads `RequestURI`/raw query directly and keeps them intact through
  authentication, so S3 path-normalization traps (`//`, `%2F`, `+` vs
  `%20`, trailing slashes) can't be silently rewritten before signing is
  checked.
- **SigV4**: Authorization-header AWS4-HMAC-SHA256, built from the raw
  request-target rather than Go's parsed `r.URL`. Literal
  `X-Amz-Content-Sha256` payload hashes only (no `UNSIGNED-PAYLOAD`, no
  presigned URLs, no aws-chunked/trailer mode).
- **CRC32**: ordinary `x-amz-checksum-crc32` header validation against the
  logical request body, checked before any chunking/storage work begins.
- **S3 operations**: path-style `CreateBucket` (idempotent), `PutObject`
  (arbitrary binary body, zero-byte objects, `Content-Type`,
  `x-amz-meta-*`, overwrite-same-key), `GetObject` (exact byte
  reconstruction, ETag, Content-Type). S3-shaped XML error responses for
  the missing-bucket/key and auth-failure cases M1 needs.
- **Commit pipeline**: CDC → durable CAS chunk publication → durable
  manifest publication → journal append+sync → in-memory namespace apply
  → HTTP success. A PUT is acknowledged only after the journal frame is
  fsynced.

## Tests

`go test ./...` and `go test -race ./...` both pass (90 test cases across
top-level tests and subtests, 0 failures). Suites, matching the M1 test
plan:

- UUID: canonical string form, version/variant nibbles, round trip through
  the stdlib `uuid.Parse`, uniqueness across 1000 generations, and
  end-to-end suitability as a manifest UUID / version ID / `FORMAT.json`
  store ID.
- Store/format: init, `FORMAT.json` contents, unsupported-version
  rejection, stable v1 constants.
- CDC: determinism, empty/tiny input, min/max bounds, forced-max
  occurrence over a large corpus, exact boundary-size inputs, repetitive
  (all-zero) data, plausible mean chunk size (~56-72KiB range), and an
  edit-locality test showing a single inserted byte perturbs ~2 CDC
  chunks versus ~65 chunks under fixed 64KiB slicing.
- CAS: path-matches-digest, round trip, dedup (no rewrite on second
  write), corrupted on-disk chunk detected on read.
- Manifest: round trip, deterministic metadata ordering, manifest-bytes
  SHA-256 matches the journal's reference, corrupt/missing manifest
  detected.
- Journal: frame encode/decode round trip, CRC32C detection, sequence
  enforcement (gap/duplicate/non-1-start), payload length bound,
  create-bucket replay, put-object-root replay, multi-frame replay
  (including overwrite), incomplete-final-frame torn-tail tolerance,
  corruption in an earlier complete frame fails loudly, invalid frame
  version/type rejected.
- SigV4: canonical URI/query unit tests (including `//`, `%2F`, `%20`,
  `+`, trailing slash, empty/repeated query params) with an independent
  cross-check implementation; a self-contained test-only signer (not
  calling into the server's own SigV4 code) exercises valid signed
  requests across those same raw-path/query shapes, and rejects wrong
  secret, wrong access key, wrong region/service/scope, a missing
  required signed header, an altered payload, and a signature computed
  over a "cleaned" path.
- CRC32: valid accepted, invalid rejected, failed checksum leaves no
  visible object (via a real HTTP round trip).
- End-to-end (real HTTP handler + server): full lifecycle
  (CreateBucket → PutObject → GetObject → restart → GetObject again),
  zero-byte object, 3MiB binary object, overwrite-then-restart keeps the
  latest version, failed PUT (missing bucket) never becomes visible.
- Crash/recovery: deterministic failure injection (a package-private test
  hook, nil in all non-test code paths) simulates a process death — via
  `panic`/`recover`, then a fresh `OpenStore` on the same directory — at
  each of the seven durability boundaries the M1 spec calls out: during
  chunk staging, after chunks published, after manifest published, during
  a torn journal write (via direct on-disk truncation of a real frame),
  after journal write but sync fails, after journal sync (object
  correctly **is** visible after restart — the sync is the true commit
  point), and after the client has been ack'd (object remains visible
  through a subsequent, unrelated panic).
- Journal poisoning and honest pre-sync recovery (added in the M1
  correction pass): `TestCrash_AfterJournalWriteBeforeSync` verifies a
  failed sync leaves the append cursor unmoved, poisons the journal, and
  causes a subsequent, unrelated mutation to be rejected without touching
  the file or the cursor; `TestJournal_PoisonedAfterWriteFailure` proves
  the same for a failed raw write (not just a failed sync), and that
  reopening afterward shows exactly the prior committed state with
  neither the failed nor the rejected mutation present;
  `TestCrash_SyncFailureRecoveryIsHonest` reopens the store after an
  injected sync failure and asserts only what's actually provable — the
  previously committed object always survives, the failed mutation was
  never acknowledged, further mutations in the same process were
  rejected, and whichever way the unsynced write's actual durability
  landed, `GetObject` for it is either a clean not-found or the complete,
  uncorrupted object — never a partial one.

## Storage format frozen (v1)

- `store_format_version = 1`, `cdc_format_version = 1`,
  `manifest_format_version = 1`, `hash_algorithm = "sha256"`.
- CDC v1 parameters: min 16384 / target 65536 / max 262144 bytes;
  mask below target = `0xFFFF` (16 bits, ~1/65536); mask at/above target =
  `0x7FFF` (15 bits, ~1/32768); gear table = SHA-256(`"ZeroS3/CDCv1/GearTable/<i>"`)
  first 8 bytes, big-endian, for `i` in `0..255`.
- Journal v1 frame: `4B magic "ZSJ1" | 2B version BE | 1B type | 1B flags(0) | 8B seq BE | 4B payload-len BE | N bytes JSON | 4B CRC32C (Castagnoli) over everything before it`.
  Max payload 8MiB. Record types: `1 = CreateBucket`, `2 = PutObjectRoot`.
  `3` and `4` are reserved (documented as constants) for a future
  DeleteObjectRoot/DeleteBucket and are **not implemented**.
- Manifest v1: JSON with `manifest_format_version`, `cdc_format_version`,
  `hash_algorithm`, `manifest_uuid`, `total_length`, `chunks[]{sha256,length}`,
  `object_sha256`, `etag`, `content_type`, `metadata[]{key,value}` (sorted
  by key), `created_at`, `version_id`.
- UUID generation (manifest UUIDs, version IDs, `FORMAT.json`'s
  `store_id`): `uuid.NewV7().String()` from the Go 1.27 standard library's
  `uuid` package, producing the same canonical lowercase 8-4-4-4-12 hex
  string the original hand-rolled generator did. Purely an internal
  generation-mechanism swap; nothing in the on-disk format changed.

## Durability contract

- Immutable CAS chunks and the immutable manifest are always fully
  published (written, fsynced, and renamed into place) *before* the
  journal record that references them is even built, let alone appended.
- The visibility journal's `Sync()` is the acknowledgment/commit
  threshold: `PutObject`/`CreateBucket` update the in-memory namespace
  only after `Journal.appendFrame`'s `Sync()` call returns successfully,
  and the HTTP handler only writes a success response after that function
  returns. So: **acknowledged mutation ⇒ durable**, always.
- The converse is deliberately not claimed: **an unacknowledged mutation
  is not guaranteed absent.** A crash/failure between a frame's `Write`
  and a successful `Sync` leaves that frame's actual on-disk state
  indeterminate (the OS may have already flushed it, or not). Replay on
  a subsequent open may legally produce either the previous complete
  state or the new complete state, depending on what genuinely reached
  durable storage — both are acceptable.
- What replay may **never** produce, under either outcome above, is a
  partial or mixed state: a visible manifest referencing incomplete or
  missing chunks, or object bytes blending two versions. Every frame
  replay accepts is a complete, CRC32C-validated unit (`replayJournal`
  only accepts a frame it can read in full and whose checksum matches);
  a byte-count-incomplete final frame is discarded as a torn tail, and
  corruption in any other frame fails store open outright rather than
  being silently accepted.
- A runtime journal write or sync failure poisons the `Journal`: the
  first such error is recorded (under the same mutex that guards the
  append cursor), and every subsequent `appendFrame` call in that process
  — from either `CreateBucket` or `PutObject` — fails immediately without
  touching the file or moving `writeOffset`/`nextSeq`. The only way past
  this is to reopen the store (a fresh `OpenStore` replays whatever is
  actually durable on disk and starts a fresh, unpoisoned `Journal`).
  Reads (`GetObject`) never touch the journal and are unaffected by
  poisoning.

### Deliberate deviations from the prompt

- Bucket names are treated as idempotent on `CreateBucket` (re-creating an
  existing bucket succeeds rather than erroring), which keeps the M1
  surface small; this wasn't specified either way.
- A single static SigV4 credential pair is hardcoded as the default
  (`defaultAccessKeyID`/`defaultSecretAccessKey`); there is no credential
  management story in M1, matching scope.

## Known issues

- The entire request body is buffered in memory (bounded by
  `maxRequestBodySize`, 256MiB) rather than truly streamed end-to-end
  through HTTP, because SigV4 payload-hash verification and CRC32
  validation both need the complete body before CDC chunking begins. The
  CDC chunker itself is a genuine streaming `io.Reader` consumer. Fine for
  M1's scope; true request streaming would be later work.
- No automatic fsync of the store root directory beyond what's needed at
  FORMAT.json creation time; this is sufficient for the invariants M1
  requires but hasn't been stress-tested against real power-loss/`kill
  -9` scenarios, only deterministic in-process failure injection and
  direct on-disk truncation (as the prompt itself prefers for this
  unattended phase).
- No in-process test can demonstrate "the write vanishes if the process
  dies before sync": once `WriteAt` returns, the bytes are visible to any
  reader sharing the OS page cache regardless of whether `Sync()` has run,
  so that specific claim is only meaningful against real power loss. The
  M1 correction pass replaced tests/comments that implied this guarantee
  with ones that assert what's actually provable (see "Durability
  contract" above and `TestCrash_SyncFailureRecoveryIsHonest`): the
  previously committed state always survives, an unacknowledged mutation
  never becomes visible as anything other than a complete, correct
  object (or doesn't become visible at all), and the process poisons
  itself against further mutation the moment journal I/O becomes
  uncertain.

## M2 status (superseded notice)

M2 is now complete -- see the "M2 status" section at the top of this
file. This section is kept only as a historical record of the M1
snapshot: at that point ListObjectsV2, ListBuckets, HeadBucket,
HeadObject, DeleteBucket, and DeleteObject had not been started, and
journal record types 3/4 were reserved constants with no behavior
attached.
