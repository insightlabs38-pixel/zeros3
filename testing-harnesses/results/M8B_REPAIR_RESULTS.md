# M8B — peer-assisted corruption repair external validation

External, real-two-server, real-AWS-SDK-v2 black-box proof of
`zeros3 repair` (M8B). Every object fixture is written through the AWS
SDK (never zeros3's internal CAS directly); direct filesystem
modification is used only to deliberately corrupt/delete an
already-validly-published CAS chunk file afterward — the one exception
the milestone spec itself permits, since corruption is the condition
under test. `zeros3 repair`/`zeros3 verify` themselves run as real
subprocesses, driving two independent, real `zeros3 serve` subprocesses
on separate stores and ports.

## Build under test

```
zeros3 commit:     9dbdf382572b07e41bf6311b47a188a2b491da37 (branch claude/zeros3-m8b-peer-repair-snoumt)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    c8099469225764357ebab159b40989d8d6274f1193f0db06fabdfb3f3a55f4f2
```

Reproducible build re-confirmed for this exact commit: two independent
source copies, byte-identical output, matching `zeros3` repo's own
`scripts/reproducible_build.sh` run recorded in `STATUS.md`'s M8B
section.

## Harness

`harness/m8b/repair/main.go`. See its own header comment for the full
black-box rationale, including why every target object body in this
harness is deliberately kept under ZeroS3's frozen 16KiB CDC v1 minimum
chunk size (so each publishes to exactly one, trivially-locatable CAS
chunk file).

### Reproduction

```sh
cd /path/to/zeros3
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-m8b-bin zeros3.go

cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-m8b-bin go run ./harness/m8b/repair
```

## Result

**133 passed, 0 failed, 1 informational.**

## Phase-by-phase

### Phase 1 — healthy baseline

The same bucket/object (a deterministic 4000-byte body, deliberately
under CDC v1's 16KiB minimum chunk size) written to both Store A and
Store B via the AWS SDK. Both `GetObject` calls return byte-identical
content; both stores independently `verify -deep` clean; both survive a
restart.

### Phase 2 — missing chunk (+ Phase 8, restart proof, folded in)

Store A's sole CAS chunk file deleted directly from disk after a valid
`PutObject`. `zeros3 verify -deep` on Store A: nonzero exit (missing
chunk detected). An ordinary AWS SDK `GetObject` against the still-running
Store A server fails while the chunk is missing. A real `zeros3 repair
-from http://<store-B-addr>` subprocess then reports:

```
Bad chunks:          1
Repaired:            1
```

`verify -deep` afterward is clean; AWS SDK `GetObject` returns the exact
original bytes. Store A is then restarted (a second time, after the
completed repair) and both `GetObject` and `verify -deep` remain green —
this is M8B's own required Phase 8 restart proof, folded into this
phase's already-established fixture rather than a ninth separate phase.

### Phase 3 — corrupt chunk (bytes flipped, filename unchanged)

Store A's sole CAS chunk file overwritten in place with garbage bytes
(same pathname, wrong content) after a valid `PutObject`. `verify -deep`
detects the digest mismatch (nonzero exit). `zeros3 repair` reports
exactly one bad/repaired chunk; a subsequent AWS SDK `GetObject` returns
the exact original bytes.

### Phase 4 — shared chunk

Five objects (`shared-1`..`shared-5`) written with byte-identical content
to Store A, so all five share the exact same sole CAS chunk by
construction (content-addressed dedup — confirmed directly: exactly one
chunk file exists for all five). That one chunk is corrupted once.
`zeros3 repair` reports:

```
Bad chunks:          1
Repaired:            1
Affected objects:    5
```

— one unique repair fetch, not five — and an AWS SDK `GetObject` against
every one of the five keys afterward returns the correct, shared content.

### Phase 5 — peer missing data too

Store A's sole chunk is deleted; Store B never receives the object at
all, so it genuinely lacks the needed chunk too. `zeros3 repair` exits
nonzero and reports honestly:

```
Bad chunks:          1
Repaired:            0
Unresolved:          1

FAILED:
sha256:<digest> -- repair: peer chunk fetch failed: status 404: {"code":"NoSuchChunk", ...}

Post-repair verify:  FAILED
```

`verify -deep` on Store A afterward still correctly reports the
corruption — repair never fabricated success.

### Phase 6 — malicious/wrong peer bytes

A fake peer, independently written in this harness using only
`net/http` (no zeros3 internals), answers ZeroS3's capability-discovery
endpoint truthfully (so the real client's `discoverZeroS3Sync` succeeds
and proceeds to the actual test) but returns deliberately wrong bytes
for every chunk-download request, ignoring auth/digest entirely. Against
Store A's own corrupted chunk, `zeros3 repair` correctly:

```
Unresolved:          1
Post-repair verify:  FAILED
```

Direct inspection of Store A's on-disk chunk file after the attempt
confirms the malicious peer's payload was never written to local CAS —
the client's own independent SHA-256 re-verification rejected it before
`casRepairPublish` was ever called.

### Phase 7 — interrupted repair (real process kill) + resume

Twelve objects, each with distinct content (so each publishes its own
CAS chunk — confirmed: exactly 12 chunk files), all seeded identically
on Store A and Store B. All 12 of Store A's chunks are corrupted
directly on disk. A real `zeros3 repair` **OS process** is started and
`SIGKILL`ed after 150ms — no live `serve` process is required for
repair, since it takes only the same shared store lock `serve` would (an
operator who has stopped the server after noticing corruption can run
repair directly against the store directory). A second, uninterrupted
`zeros3 repair` invocation against the same store then completes; every
one of the 12 damaged objects reads back correct content via the AWS
SDK afterward, and a final `verify -deep` is fully clean. (One
informational note: this run's kill happened to land after the entire
first pass had already resolved every chunk, so the resumed second
run's own reported `Bad chunks` was already 0 — a real, timing-dependent
outcome exactly like M8A's own Phase 5 resume proof; the deterministic,
guaranteed-partial version of this same guarantee is
`TestRepair_ResumeAfterPartialRunOnlyFetchesRemaining` and
`TestCLI_Repair_RealProcessKillThenResume` in `zeros3_test.go`, both of
which directly assert a strictly-smaller rediscovered bad-chunk count on
the second run.)

## Full pre-existing external harness regression

Every harness recorded at the M8A baseline (`testing-harnesses/results/
M8_RELEASE_CANDIDATE_RESULTS.md`) was re-run, **unmodified**, against
this exact M8B build:

| Harness | Result | Matches M8A baseline? |
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
| `rclone` | 20 passed, 0 failed, 1 documented known limitation | yes, identical |
| `package-killer` | 14/14 passed (ZeroS3/s3rver) | yes, identical |
| **`m8b/repair` (new)** | **133 passed, 0 failed, 1 informational** | new this pass |

**Totals: 557 passed, 0 failed, 9 informational, 1 documented known
limitation across every harness in this repository — zero regressions
from the M8A baseline.**

## Caveats

- Phase 7 (interrupted repair) is best-effort/timing-based, exactly like
  M8A's own Phases 5-7: a real external subprocess offers no
  deterministic injection point. The invariant actually checked (every
  object correct and `verify -deep` clean after the interrupted-then-
  resumed repair) holds regardless of exactly how far the killed attempt
  got — the internal suite's `TestRepair_ResumeAfterPartialRunOnlyFetchesRemaining`
  and `TestCLI_Repair_RealProcessKillThenResume` provide the
  deterministic and guaranteed-partial versions of this same guarantee.
- This harness exercises exactly the 8 phases M8B's task description
  calls out; it is a black-box supplement to, not a replacement for, the
  33 internal `TestRepair_*`/`TestFetchRepairChunk_*`/`TestCLI_Repair_*`/
  `TestCLI_VerifyRepairFrom_*` tests in `zeros3_test.go`, which cover the
  full required test matrix (detection, peer fetch, publication, result,
  partial repair, resume, concurrency, GC/version/multipart reachability
  scope) in more exhaustive and deterministic detail.
- `package-killer` and `rclone` require external tooling (`s3rver`/`rclone`
  binaries) not committed to this repository; both were installed into
  ephemeral scratch locations outside both repos for this run, per the
  existing convention, and removed afterward.
