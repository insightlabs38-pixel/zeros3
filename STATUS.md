# ZeroS3 — M1/M2/M3/M4 Status

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
