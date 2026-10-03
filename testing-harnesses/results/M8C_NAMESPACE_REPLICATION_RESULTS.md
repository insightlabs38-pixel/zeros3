# M8C — prefix/bucket delta replication external validation

External, real-two-server, real-AWS-SDK-v2 black-box proof of `zeros3
replicate -recursive` (M8C). Every object fixture is written through the
AWS SDK (never zeros3's internal CAS directly); the one exception the
milestone spec itself permits (Phase 6, Phase 9) directly corrupts an
already-validly-published CAS chunk file on disk afterward, since
corruption is the condition under test. `zeros3 replicate -recursive`/
`zeros3 repair`/`zeros3 verify` themselves run as real subprocesses,
driving two independent, real `zeros3 serve` subprocesses on separate
stores and ports per phase.

## Build under test

```
zeros3 commit:     7bc94d4c00e11e5f0e4ea9bf920772d8d201a729 (branch claude/zeros3-m8c-prefix-bucket-e30x9r)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    726315e7676e58bef5be70ee0127d3cceda746779a4c932d02dfdbafb5540f86
```

Reproducible build re-confirmed for this exact commit: two independent
source copies, byte-identical output, matching the `zeros3` repo's own
`scripts/reproducible_build.sh` run recorded in `STATUS.md`'s M8C
section.

## Harness

`harness/m8c/namespace_replication/main.go`. See its own header comment
for the full black-box rationale. Each of the 9 required phases gets a
fresh, independent pair of `zeros3 serve` subprocesses on freshly created
empty store directories (the same precedent `harness/m8b/repair` set),
so phases never interact and results stay fully deterministic.

### Reproduction

```sh
cd /path/to/zeros3
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-m8c-bin zeros3.go

cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-m8c-bin go run ./harness/m8c/namespace_replication
```

## Result

**111 passed, 0 failed, 2 informational.** Stable and reproducible across
three independent runs against this exact binary (no flakiness observed).

## Phase-by-phase

### Phase 1 — empty namespace

An empty source bucket, replicated with `-recursive` into an empty
destination bucket. `zeros3 replicate -recursive` succeeds, reports
`Objects discovered: 0` and zero payload transferred, and the destination
namespace remains empty per an independent AWS SDK `ListObjectsV2`.

### Phase 2 — initial namespace replication

The milestone's own worked example tree, written via the AWS SDK:

```
datasets/a.bin
datasets/b.bin
datasets/sub/c.bin
datasets/sub/d.txt
other/not-selected.bin
```

Replicating only `s3://p2-src/datasets/` into `s3://p2-dst/archive/`
reports exactly 4 objects discovered/replicated. An independent AWS SDK
`ListObjectsV2` on the destination returns exactly `archive/a.bin`,
`archive/b.bin`, `archive/sub/c.bin`, `archive/sub/d.txt` — never
`archive/not-selected.bin` or `not-selected.bin` — and every one reads
back byte-for-byte identical to its source content via AWS SDK `GetObject`.

### Phase 3 — destination prepopulation / global CAS reuse

The strongest M8C demo, per the milestone spec. Before replication, the
destination already holds a 20MB related object (`related/existing.bin`,
written via the AWS SDK) under a completely different key from anything
being replicated. The source holds an edited variant of that same content
(`edited/new.bin`, a small localized insertion into the same 20MB base)
plus one unrelated brand-new object, both under the replicated prefix.
Replicating `s3://p3-src/edited/` into `s3://p3-dst/landed/`:

```
Logical data:            19.13 MiB
Payload transferred:     87.10 KiB
Transfer avoided:        19.04 MiB
Reuse:                   99.6%
```

A strong, honest reuse figure driven entirely by destination CAS content
that was never part of this replication run's own source objects — proof
that M8C's global CAS reuse (not merely "reuse within the objects being
replicated") is real. The destination's final key set is exactly
`landed/brand-new.bin`, `landed/new.bin`, `related/existing.bin`, and
`landed/new.bin` reads back byte-for-byte identical to the source's
edited content via AWS SDK `GetObject`.

### Phase 4 — multi-page source (1500 objects)

1500 lightweight objects (`many/0000.txt` … `many/1499.txt`) written via
the AWS SDK, deliberately past ZeroS3's 1000-key `ListObjectsV2` page
size. `zeros3 replicate -recursive` reports exactly 1500 objects
discovered and replicated; an independent AWS SDK `ListObjectsV2` call
(itself internally paginated by the SDK) on the destination returns
exactly 1500 keys with zero duplicates and zero missing.

### Phase 5 — non-destructive semantics

A destination-only object (`dest-only.bin`, written directly via the AWS
SDK, never present at the source) survives a full namespace replication
run byte-for-byte untouched. Separately: after an initial replication of
two source objects, one (`b.bin`) is deleted from the source via the AWS
SDK; a rerun discovers only the one remaining source object, and an
independent AWS SDK `GetObject` confirms `b.bin`'s previously-replicated
destination copy remains exactly as it was — no implicit delete anywhere.

### Phase 6 — partial failure (deterministic)

Three source objects (`aaa.bin`, `corrupt.bin`, `zzz.bin`); `corrupt.bin`'s
body is kept under ZeroS3's frozen 16KiB CDC v1 minimum chunk size so it
always publishes to exactly one, trivially-locatable CAS chunk file,
which is then corrupted directly on disk before replication runs (the
milestone's own permitted direct-filesystem exception; a deterministic
trigger was chosen over a timing-based conflict race, which has two
equally legitimate real-world outcomes and can't be forced to fail on
every run). `zeros3 replicate -recursive` exits non-zero, its failure
report names `corrupt.bin`, and reports exactly "3 discovered, 2
replicated, 1 failed." Independent AWS SDK `GetObject` confirms `aaa.bin`
and `zzz.bin` both committed successfully and `corrupt.bin` was never
committed at the destination at all.

### Phase 7 — interruption/resume

Three 9MB source objects under a replicated prefix. A real `zeros3
replicate -recursive` subprocess is started and `SIGKILL`ed 300ms in
(well before three real HTTP-relayed 9MB transfers could plausibly all
complete). A second, uninterrupted invocation completes cleanly, reports
all 3 objects replicated with zero failures, and every object reads back
byte-for-byte identical to its source content via AWS SDK `GetObject` —
no namespace-replication session state anywhere; CAS content-addressing
plus each object's own M8A resume behavior make the resume correct on
their own:

```
Payload transferred:     21.65 MiB
Reuse:                   15.9% (chunks the killed attempt already landed reduce this further)
```

### Phase 8 — restart

Two objects replicated into a fresh destination; the destination process
is restarted (kill + fresh `zeros3 serve` on the same store directory).
An independent AWS SDK `ListObjectsV2`+`GetObject` confirms both keys and
exact byte content survive the restart, and `zeros3 verify -deep` reports
clean (0 missing, 0 corrupt, 0 invalid).

### Phase 9 — M8B compatibility

One object replicated via `-recursive`; the destination is stopped, one
of its CAS chunk files is corrupted directly on disk (the milestone's own
permitted exception), and the destination is restarted. `zeros3 verify
-deep` correctly detects the corruption. `zeros3 repair -from` the
source (a healthy M8A/M8B peer for that same content, since M8C-replicated
objects are ordinary objects with no special repair-blocking state) then
repairs it cleanly:

```
Repair source:       <source endpoint>
Bad chunks:          1
Repaired:            1
Unresolved:          0
Payload fetched:     62.10 KiB
Affected objects:    1

Post-repair verify:  OK
```

An independent AWS SDK `GetObject` after repair confirms exact original
content — proof that M8C composes cleanly with M8B's peer-assisted
repair without any special-case entanglement between the two milestones.

## Full pre-existing external harness regression

Every harness recorded at the M8B baseline
(`results/M8B_REPAIR_RESULTS.md`) was re-run, **unmodified**, against
this exact M8C build:

| Harness | Result | Matches M8B baseline? |
|---|---|---|
| `m2` | 41 passed, 0 failed | yes, identical |
| `m3/copy` | 46 passed, 0 failed | yes, identical |
| `m3/range` | 27 passed, 0 failed | yes, identical |
| `m3/dedup` | 7 passed, 0 failed | yes, identical |
| `m5a/presign` | 47 passed, 0 failed | yes, identical |
| `m5b/multipart` | 43 passed, 0 failed | yes, identical |
| `m5d/pagination` | 43 passed, 0 failed | yes, identical |
| `m6/sync` | 33 passed, 0 failed, 2 informational | yes, identical |
| `m6c/dirsync` | 69 passed, 0 failed, 2 informational | yes, identical |
| `m8a/remote_delta` | 34 passed, 0 failed, 4 informational | yes, identical |
| `m8b/repair` | 133 passed, 0 failed, 1 informational | yes, identical |
| **`m8c/namespace_replication` (new)** | **111 passed, 0 failed, 2 informational** | new this pass |

`rclone` and `package-killer` require external tooling (an `rclone`
binary and an `npm`-installed `s3rver`, respectively) not available in
this session's environment, so they were not re-run here; neither
harness exercises any code path this milestone changed (M8C added no new
server-side endpoint and no changed request-routing behavior — see
`STATUS.md`'s M8C section), and both were confirmed unmodified and green
(20 passed/0 failed/1 documented known limitation, and 14/14 passed
respectively) at the M8B freeze recorded in `results/M8B_REPAIR_RESULTS.md`.

**Totals across the 11 harnesses actually re-run this pass (the 10
pre-existing ones above plus the new `m8c` harness): 634 passed, 0
failed, 11 informational — every count byte-for-byte identical to its
own M8B-recorded baseline, zero regressions.** Including the two
harnesses not re-run this session at their last-recorded M8B counts
(`rclone` 20/0/1, `package-killer` 14/14), the full repository total
stands at 668 passed, 0 failed, 11 informational, 1 documented known
limitation.

## Benchmark

Phase 3 above is this milestone's required deterministic namespace
benchmark: a destination prepopulated with related content under
different keys, replicating an edited variant plus one unrelated new
object from the source. Recorded numbers (see Phase 3 above for full
detail):

```
Objects:             2
Logical source:      19.13 MiB
Payload relayed:     87.10 KiB
Transfer avoided:    19.04 MiB
Reuse:               99.6%
```

This demonstrates M8C's primary guaranteed claim — it transmits only
chunks absent from the destination CAS, evaluated store-wide, not merely
within the set of objects being replicated in one run — on one test
environment and one fixture shape; it is not a claim that M8C is
universally faster than ordinary replication.

## Interpretation

`zeros3 replicate -recursive` (M8C) generalizes M8A's proven
single-object primitive across a source namespace with no second
replication engine: every phase above exercises the same `replicateObject`
pipeline M8A's own external harness already validated, now driven by
ordinary `ListObjectsV2` enumeration and key mapping. Non-destructive
semantics, partial-failure isolation, resume without session state, exact
statistics, and clean composition with M8B's peer-assisted repair are all
proven end-to-end against real, independent AWS SDK for Go v2 clients and
real two-server `zeros3 serve` subprocesses — never zeros3's own internal
test harness pretending to be an external client.
