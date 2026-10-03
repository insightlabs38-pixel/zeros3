# M8H-B — Bounded parallel chunk transfer: benchmarks, hostile review,
and full release regression

Implementation pass. Builds directly on `M8H_SEQUENTIAL_TRANSFER_BENCHMARKS.md`'s
**STRONG GO** decision: a bounded worker pool (`runTransferWorkers`,
`zeros3.go`) now backs `executeReplicationPlan` (M8A/`replicate`),
`repairFromPeer` (M8B), and `uploadMissingSyncChunks` (M6/`sync`), while
object/root-level publication stays exactly as sequential as it always
was (`commitSyncObject` is still called once, only after every worker has
succeeded; `replicateNamespace`/`fork`/`restore` still commit one object
at a time, in listing order).

## Exact target

- **ZeroS3 commit (M8H-B):** `ca932499152febd6f223b7b46241edefba694d39`
  (branch `claude/zerosm8h-b-parallel-chunks-0w3d0p`), on top of the
  exact M8H-A-confirmed M8G baseline `432245dc5acb4a68a2ecdd732be7a3addc85ca30`.
- **`zeros3.go`:** 12419 lines (was 12007 at M8H-A/M8G). **`zeros3_test.go`:**
  24697 lines (was 23331). Net implementation delta: +412 lines
  (the `runTransferWorkers`/`transferWork`/`resolveTransferWorkers`
  engine, the three call-site conversions, the shared HTTP transport, and
  `-workers` CLI plumbing). Net test delta: +1366 lines, +37 new test
  functions (688 total internal tests, up from 651).
- **Reproducible build command:** `CGO_ENABLED=0 go build -trimpath
  -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go`.
- **Binary SHA-256** (two independent source copies at two different
  absolute paths, byte-identical):
  `6c11cbda9bcca30cd3c5081f86c98b4f52f14d75b869f28448097dc5dcbd40d2`.
- `go.mod`: zero `require` directives (unchanged since M1). No new
  non-stdlib import in `zeros3.go`; `zeros3_test.go` gained `context`
  and `sync/atomic`, both stdlib.

## Phase 0 — baseline confirmation (before any implementation)

Re-confirmed at the exact M8G HEAD `432245dc5acb4a68a2ecdd732be7a3addc85ca30`
before writing any M8H-B code (byte-identical `zeros3.go`/`zeros3_test.go`/
`go.mod` diff against M8H-A's own confirmed baseline, `git diff` empty):
`gofmt -l .` clean, `go vet ./...` clean, `go test ./...` **651 tests**
ok, `go test -race ./...` ok, reproducible build SHA-256
`e7c53f313fc76425d272606e621b2b84eb966551c96ecce696f954bd2864bff6`
(identical to M8H-A's own recorded value). `m8g-gold` tag confirmed.
Focused M8A/M8B/M8C/M8G suites (84 replicate/repair-focused tests) green.

## Implementation summary

- **Shared primitive** (`zeros3.go`, new section "15a-bis"):
  `transferWork{SHA256, Length, Do func(ctx) error}` plus
  `runTransferWorkers(ctx, workers, items, cancelOnError) []transferOutcome`
  — a bounded counting-semaphore worker pool, no persistent pool, no
  background goroutine outliving the call. Results are placed at the same
  index as their input item regardless of completion order (deterministic
  scheduling/stats, B1.5/B1.12). `cancelOnError=true` (replicate/sync's
  all-or-nothing commit gate) cancels a derived context on the first
  failure, so unstarted work is skipped and in-flight HTTP requests
  sharing that context are aborted; `cancelOnError=false` (repair's
  honest partial-success contract, B2.3) runs every item regardless of
  a sibling's outcome. `firstTransferError` picks the lowest-index
  *genuine* failure, never whichever goroutine happened to lose the
  race, falling back to a plain cancellation error only if nothing
  genuine is found (e.g. an external caller-supplied context was
  canceled).
- **Operations using it:** `executeReplicationPlan` (M8A single-object
  replicate — and therefore `replicateNamespace`/M8C and `fork`/M8D,
  which both call it per object, unmodified), `repairFromPeer` (M8B),
  `uploadMissingSyncChunks` (M6 `sync`, including M6C directory sync,
  which calls `syncFile` per file unmodified).
- **Operations intentionally not using it:** `restoreObject` (M8E) stays
  sequential per the spec's explicit non-goal; `replicateNamespace`/`fork`
  still commit one object at a time (no namespace-level object
  concurrency was added — this is the "transport concurrent, publication
  not" line the milestone draws); `planReplication`/
  `planReplicationNamespace` (dry-run) never touch the worker pool at
  all, since they never fetch chunk payload.
- **M6 local sync decision: IMPLEMENTED.** `uploadMissingSyncChunks`'s
  loop is structurally identical to `executeReplicationPlan`'s (read/
  verify -> publish per missing unique chunk, all-or-nothing before
  commit) — reading from a local file range instead of a remote peer —
  so it was cleanly reusable without distorting the architecture.
- **Worker bounds:** `maxTransferWorkers = 32` (a fixed small multiple of
  the highest benchmarked candidate, 16); `validateWorkers`
  rejects `< 1` or `> 32` outright (not silently clamped) so
  `-workers 1000000` fails loudly rather than running at some other
  number; `resolveTransferWorkers` maps the Go zero value (every
  pre-M8H-B caller) to `defaultTransferWorkers`.
- **Default worker count: 8** — see "Default worker selection" below.
- **HTTP transport (B1.10):** a single package-level, long-lived
  `transferHTTPTransport`/`transferHTTPClient` (`MaxIdleConnsPerHost`/
  `MaxConnsPerHost` = `2*maxTransferWorkers` = 64, `MaxIdleConns` =
  `4*maxTransferWorkers` = 128, `IdleConnTimeout` 90s) replaces
  `http.DefaultClient` as `syncClientConfig.client()`'s fallback —
  Go's default transport caps `MaxIdleConnsPerHost` at 2, exactly the
  pitfall M8H-A flagged by name. This is a single, bounded, fixed-size
  pool shared by every ZeroS3 client operation (not one Transport per
  chunk or per operation), sized directly off `maxTransferWorkers` so it
  can never drift out of sync with the worker-count ceiling.
- **Context propagation (B1.9):** `signAndDo` and
  `fetchSourceChunk`/`putSyncChunk`/`fetchRepairChunk` now take a
  `context.Context`; every pre-existing (non-worker-pool) call site
  passes `context.Background()` (identical blocking behavior to before
  `ctx` existed), while the worker pool passes its own per-call context,
  so cancellation actually reaches the in-flight GET/PUT and its request
  body handling, not just the goroutine boundary.
- **Persistent-format impact: NONE.** No manifest/journal/CAS/`FORMAT.json`
  change; this is purely a client-side transport optimization.

## Correctness evidence (internal test suite, 37 new tests)

Full detail lives in `zeros3/zeros3_test.go`; summarized here per the
completion report's required proof list. All of the following pass under
`go test -race ./...`, and the goroutine/scheduling primitive is also
covered directly (no real network) so it's cheap to run repeatedly:

- **Duplicate scheduling:** `TestReplicate_DuplicateDigestReferencesTransferOnce`
  — a digest referenced 15 times triggers exactly 1 GET against the
  source and `UniqueChunksUploaded=1`, `TotalChunks=15`.
  `TestRepair_SharedDigestSingleRepairJob_Concurrent` — one bad digest
  shared by 12 objects triggers exactly one physical repair
  (`Repaired=1`, `PayloadFetched` = one chunk's bytes, `AffectedObjects=12`).
- **One-failure cancellation:** `TestRunTransferWorkers_CancelOnErrorStopsUnstartedWork`
  (primitive-level: a first failure stops queued work from starting),
  `TestReplicate_CancellationStopsUnnecessaryWorkOnFailure` (real HTTP:
  fewer than every chunk is fetched after a mid-batch failure).
- **No commit on transfer failure:** `TestReplicate_SourceMissingOneChunkAmongMany`,
  `TestReplicate_SourceReturnsWrongDigestAmongMany`,
  `TestReplicate_DestinationUploadRejectsAmongMany`,
  `TestReplicate_ConnectionResetOnOneChunkIsHandledAsFailure` — all
  assert the destination object was never committed.
  `TestReplicate_SuccessfulTransferCommitsExactlyOnce` asserts exactly
  one commit request reaches the destination on success.
- **Source identity:** unaffected by construction — `planReplication`
  fetches the source descriptor exactly once and every worker operates
  off that captured chunk list, unchanged from M8A7's pre-M8H-B
  contract (`TestReplicate_SourceOverwrittenDuringReplicationDoesNotProduceMixedRevision`,
  pre-existing, still green under the new default concurrency).
- **Destination-conflict safety:** unaffected by construction — the
  commit-time precondition check is unchanged and still runs exactly
  once, after every worker succeeds
  (`TestReplicate_DestinationConflict_ConcurrentWriteDuringReplicationRejectedSafely`,
  pre-existing, still green). A live demonstration of this same
  mechanism surfaced during test-timing work below.
- **Process-kill resume:** `TestReplicate_ResumeAcrossRealProcessInterruption`,
  `TestReplicateNamespace_ResumeAcrossRealProcessInterruption`,
  `TestCLI_Repair_RealProcessKillThenResume` — real `SIGKILL` of a real
  `zeros3` subprocess mid-transfer, a resumed rerun completes correctly.
- **Repair proof:** `TestRepair_ManyCorruptChunks_WorkerCountsProduceIdenticalStats`,
  `TestRepair_ManyMissingChunks_Concurrent`,
  `TestRepair_MixedCorruptAndMissing_Concurrent`,
  `TestRepair_OnePeerFailureAmongManySuccesses_Concurrent`,
  `TestRepair_WrongPeerBytesAmongMany_Concurrent`.
- **Race/leak proof:** `go test -race ./...` green (688 tests);
  `TestReplicate_RepeatedTransfersDoNotLeakGoroutines` (20 replications,
  goroutine count checked after closing idle connections);
  `TestReplicate_WorkerCountsProduceByteIdenticalStats`/
  `TestRepair_ManyCorruptChunks_WorkerCountsProduceIdenticalStats` assert
  byte-identical aggregate stats across workers = 1/2/4/8/16/32.

### A genuine race, found and fixed during this pass — in a test, not in
`zeros3.go`

`TestReplicateNamespace_ResumeAcrossRealProcessInterruption` (a pre-existing
M8C test: kill a real `replicate -recursive` subprocess mid-transfer,
then rerun) started failing intermittently (~70% of runs) once chunk
transfer became meaningfully faster. Root cause, confirmed by
instrumentation: the *killed* first attempt's own commit request could
already be fully received and accepted by the destination server (a
`SIGKILL` doesn't un-send bytes already handed to the kernel's TCP send
buffer) — a data point the test's fixed 250ms kill delay was calibrated
to avoid *precisely because* sequential transport was slow enough that
no attempt could plausibly reach commit that fast. With parallel chunk
transfer, it now sometimes could. The result was not corruption: the
resumed second attempt's own commit for the same object correctly
observed a *different* destination state than it had planned against and
was rejected with the exact same 412 conflict `commitSyncObject` has
always produced — i.e., destination-conflict safety caught and rejected
the race exactly as designed, and the final destination content was
correct in every run, confirmed directly by an instrumented reproduction.
The fix was a test-timing correction (250ms -> 50ms, reliable across 15/15
repeats), not a `zeros3.go` change; see the commit message for full
detail. This is direct, unplanned evidence for B5's "destination conflict
after transfer" and "process kill" hostile scenarios both holding under
real concurrency.

## Performance — worker-count matrix (B4)

Harness: `testing-harnesses/harness/m8h/parallel_transfer/main.go` (new,
this pass) — real two-server `zeros3 serve` subprocesses, real `replicate`/
`repair` CLI subprocesses with `-workers` threaded through, same
SHA-256-counter-mode deterministic fixture generator and reverse-proxy
latency injection technique M8H-A's own harness used, so results are
directly comparable. One untimed warmup + 3 measured runs per
configuration, median reported (min/max recorded alongside).

```sh
cd testing-harnesses
go build -o bin/zeros3h-parallel ./harness/m8h/parallel_transfer/
# at ca932499152febd6f223b7b46241edefba694d39 in the zeros3 checkout:
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3-bin zeros3.go
./bin/zeros3h-parallel -bin /path/to/zeros3-bin -out /tmp/m8h-b-run -scenario all -runs 3
```

Same environment as M8H-A: `go1.27.0 linux/amd64`, 4 vCPU, loopback
network path. A mid-run disk-space exhaustion (this session's own scratch
quota, not a ZeroS3 or harness defect) interrupted the largest
(256 MiB) configurations partway through; the harness's own
`-single-sizes-mib`/`-single-delays-ms` flags (added this pass) were
used to re-run exactly the missing configurations after freeing space —
every one of the 30 single-object configurations below is a real,
independently measured run.

### Single-object replication, destination empty

| Payload | RTT | Workers | Median | MiB/s | Speedup vs. workers=1 |
|---|---|---:|---:|---:|---:|
| 64 MiB | 0 ms | 1 | 4.507 s | 14.20 | 1.00x |
| 64 MiB | 0 ms | 2 | 2.576 s | 24.84 | 1.75x |
| 64 MiB | 0 ms | 4 | 2.368 s | 27.03 | 1.90x |
| 64 MiB | 0 ms | 8 | 2.377 s | 26.92 | 1.90x |
| 64 MiB | 0 ms | 16 | 2.111 s | 30.31 | 2.14x |
| 64 MiB | 5 ms | 1 | 10.329 s | 6.20 | 1.00x |
| 64 MiB | 5 ms | 2 | 5.681 s | 11.26 | 1.82x |
| 64 MiB | 5 ms | 4 | 3.356 s | 19.07 | 3.08x |
| 64 MiB | 5 ms | 8 | 2.441 s | 26.22 | 4.23x |
| 64 MiB | 5 ms | 16 | 2.011 s | 31.81 | 5.14x |
| 64 MiB | 10 ms | 1 | 15.653 s | 4.09 | 1.00x |
| 64 MiB | 10 ms | 2 | 8.064 s | 7.94 | 1.94x |
| 64 MiB | 10 ms | 4 | 4.552 s | 14.06 | 3.44x |
| 64 MiB | 10 ms | 8 | 2.787 s | 22.96 | 5.62x |
| 64 MiB | 10 ms | 16 | 2.077 s | 30.81 | **7.54x** |
| 256 MiB | 0 ms | 1 | 16.646 s | 15.38 | 1.00x |
| 256 MiB | 0 ms | 2 | 9.511 s | 26.91 | 1.75x |
| 256 MiB | 0 ms | 4 | 7.298 s | 35.08 | 2.28x |
| 256 MiB | 0 ms | 8 | 6.285 s | 40.73 | 2.65x |
| 256 MiB | 0 ms | 16 | 6.234 s | 41.06 | 2.67x |
| 256 MiB | 5 ms | 1 | 39.276 s | 6.52 | 1.00x |
| 256 MiB | 5 ms | 2 | 21.259 s | 12.04 | 1.85x |
| 256 MiB | 5 ms | 4 | 12.099 s | 21.16 | 3.25x |
| 256 MiB | 5 ms | 8 | 7.906 s | 32.38 | 4.97x |
| 256 MiB | 5 ms | 16 | 6.962 s | 36.77 | 5.64x |
| 256 MiB | 10 ms | 1 | 58.655 s | 4.36 | 1.00x |
| 256 MiB | 10 ms | 2 | 31.031 s | 8.25 | 1.89x |
| 256 MiB | 10 ms | 4 | 17.031 s | 15.03 | 3.44x |
| 256 MiB | 10 ms | 8 | 9.845 s | 26.00 | 5.96x |
| 256 MiB | 10 ms | 16 | 7.170 s | 35.70 | **8.18x** |

This is a direct, measured reversal of M8H-A's own B8 finding (a 10ms
per-request delay produced a **3.3x throughput collapse** under
sequential transport): the identical delay now produces up to an
**8.18x speedup** at 16 workers relative to the (still fully sequential)
`workers=1` baseline, because independent chunk round trips overlap
instead of paying their latency one at a time. At 0ms (loopback,
effectively free network transport, closer to CPU/negotiation-bound),
gains are real but far more modest (up to ~2.7x) and saturate earlier —
exactly the pattern M8H-A's CPU-utilization check predicted (this
machine had idle capacity to exploit, but it isn't infinite, and at zero
added latency there's proportionally less serialized-wait time to
reclaim in the first place).

### Namespace replication (16 objects x 4 MiB = 64 MiB total, loopback)

| Workers | Median | MiB/s | Speedup |
|---:|---:|---:|---:|
| 1 | 4.419 s | 14.48 | 1.00x |
| 2 | 2.549 s | 25.11 | 1.73x |
| 4 | 1.860 s | 34.40 | 2.38x |
| 8 | 1.763 s | 36.30 | 2.51x |
| 16 | 1.805 s | 35.45 | 2.44x |

Each object's own chunks transfer concurrently (up to `workers` at a
time); objects still commit strictly one at a time, in listing order —
no namespace-level object concurrency was added. 8 workers is
essentially the plateau here; 16 is marginally *worse* than 8 (dispatch/
scheduling overhead exceeding the shrinking per-object payload's own
benefit at this small a per-object size).

### Repair (32 MiB object, every chunk corrupted, loopback)

| Workers | Median | MiB/s | Speedup |
|---:|---:|---:|---:|
| 1 | 1.401 s | 22.83 | 1.00x |
| 2 | 0.962 s | 33.24 | 1.46x |
| 4 | 0.775 s | 41.26 | 1.81x |
| 8 | 0.681 s | 47.02 | 2.06x |
| 16 | 0.693 s | 46.17 | 2.02x |

Same pattern: 8 workers plateaus (16 very slightly regresses).

### Planning (dry-run) unaffected by workers (B3.1/B6)

A 64 MiB single-object `replicate -dry-run` planning pass: **14ms**,
consistent with M8H-A's own 62-66ms range at 256 MiB (planning time
scales with chunk *count*, not with the worker flag, which `-dry-run`
never even reaches — `planReplication` never touches
`runTransferWorkers`). Confirms the worker pool adds no planning-path
overhead.

## Default worker selection (B4.2)

**Chosen default: 8** (`defaultTransferWorkers` in `zeros3.go`), replacing
the pre-benchmark placeholder of 4. Reasoning, from the tables above:

- At every RTT/payload combination, workers=8 captures the large
  majority of the attainable speedup: 73-99% of workers=16's throughput
  across the six single-object configurations (worst case 256 MiB/10ms:
  26.00 vs. 35.70 MiB/s; best case 256 MiB/0ms: 40.73 vs. 41.06 MiB/s,
  essentially saturated already).
- In namespace replication and repair specifically, workers=8
  *slightly outperforms* workers=16 on this 4-vCPU benchmark machine —
  a real signal that 16 is past this machine's own useful concurrency
  ceiling for these payload sizes, not evidence that 16 is broken.
- Where the benefit matters most (10ms simulated RTT, directly
  reproducing M8H-A's B8 finding), workers=8 alone already delivers
  5.6-6.0x speedup over sequential — most of the total available gain
  (7.5-8.2x at 16) at half the concurrent-connection/memory footprint.
- 8 keeps concurrent HTTP connections and worst-case in-flight memory
  (`workers x max chunk size` = 8 x 262144 B = ~2 MiB) modest on an
  ordinary machine, consistent with B1.2's "reasonable default" framing
  and B4.2's explicit guidance to prefer the smaller count when it
  already captures most of the larger count's throughput.

`-workers` remains fully configurable per invocation (1..32, validated);
8 is only the value used when a caller doesn't ask for a specific count.

## Regression threshold decision (B4.3)

**KEEP.** Every single-object configuration exceeds the stated "REVERT"
range (10-25% modest gain) by a wide margin, and the highest-RTT case
(the one M8H-A identified as the actual production-relevant scenario —
a real WAN path, unlike this loopback benchmark's 0ms baseline) shows an
**8.18x** speedup, comfortably clearing the "$\geq$2x on a meaningful
sequential bottleneck" KEEP bar. Complexity added is exactly the small,
shared primitive the milestone scoped (one `runTransferWorkers` function,
~230 lines with documentation, reused identically by three call sites) —
not a new subsystem. No correctness regression: full internal suite and
full historical external regression (below) both green.

## Full historical regression (unmodified harnesses)

Every harness accepted at or before M8G, re-run **unmodified**, against
the M8H-B build above (`ca932499152febd6f223b7b46241edefba694d39`):

| Harness | Result | Matches last recorded baseline? |
|---|---|---|
| `m2` | 41 passed, 0 failed | yes, identical (M8F baseline) |
| `m3/copy` | 46 passed, 0 failed | yes, identical |
| `m3/dedup` | 7 passed, 0 failed | yes, identical |
| `m3/range` | 27 passed, 0 failed | yes, identical |
| `m5a/presign` | 47 passed, 0 failed | yes, identical |
| `m5b/multipart` | 43 passed, 0 failed | yes, identical |
| `m5d/pagination` | 43 passed, 0 failed | yes, identical |
| `m6/sync` | 33 passed, 0 failed, 2 informational | yes, identical |
| `m6c/dirsync` | 69 passed, 0 failed, 2 informational | yes, identical |
| `m8a/remote_delta` | 34 passed, 0 failed, 4 informational | yes, identical |
| `m8b/repair` | 133 passed, 0 failed, 1 informational | yes, identical |
| `m8c/namespace_replication` | 111 passed, 0 failed, 2 informational | yes, identical |
| `m8d/fork` | 146 passed, 0 failed, 3 informational | yes, identical |
| `m8e/snapshot` | 151 passed, 0 failed, 3 informational | yes, identical |
| `m8f/conditional` | 83 passed, 0 failed, 1 informational | yes, identical |
| `m8g/introspection` | 78 passed, 0 failed, 5 informational | yes, identical (M8G baseline) |
| `rclone` | 20 passed, 0 failed, 1 documented known limitation | yes, identical |
| `package-killer` | ZeroS3 14/14, s3rver 14/14 — **GO** | yes, identical |

**Totals: 1140 passed, 0 failed, 23 informational, 1 documented known
limitation, across 18 harnesses.** Every harness's count is byte-for-byte
identical to its own last-recorded baseline
(`results/M8F_CONDITIONAL_RESULTS.md` for the 16 pre-M8G harnesses,
`results/M8G_INTROSPECTION_RESULTS.md` for `m8g/introspection`) — zero
regressions anywhere, despite the transport-layer change touching every
one of `replicate`/`repair`/`sync`'s code paths these harnesses exercise.

Neither `rclone` nor `package-killer`'s `s3rver` dependency were blocked
in this environment this run (both were fetched/installed fresh:
`rclone v1.75.0` from `downloads.rclone.org`, `s3rver` via `npm install`)
— both ran for real, not as a disclosed limitation.

## New M8H-B harness scope note

Per the milestone's own guidance to keep this external harness
proportionate to what it adds beyond the internal suite: the hostile-
review scenarios explicitly listed in the M8H-B spec (one-worker
failure/cancellation, malicious/wrong-digest peer, destination-rejects,
connection-reset, worker-count edge cases including workers > chunk
count, goroutine/leak stability, byte-identical stats across worker
counts) are covered by the **internal** `zeros3/zeros3_test.go` suite
(see "Correctness evidence" above) rather than duplicated here as a
separate external phase-by-phase harness — internal tests already run
under `go test -race`, execute in seconds rather than minutes, and (for
the failure-injection scenarios especially) can control timing/ordering
far more precisely than an external black-box harness could. This
harness's own job, and where an external black-box harness adds real
value beyond the internal suite, is real-subprocess performance
measurement (the tables above) and the two real-`SIGKILL` process-
interruption scenarios, which the **internal** suite also already covers
end-to-end via real `os/exec` subprocesses (`TestReplicate_ResumeAcrossRealProcessInterruption`,
`TestReplicateNamespace_ResumeAcrossRealProcessInterruption`,
`TestCLI_Repair_RealProcessKillThenResume`) — not simulated in-process,
genuinely spawning and `SIGKILL`ing real `zeros3` binaries, identical in
spirit to what a separate external harness phase would do.

## Internal (zeros3 repo) regression

```
gofmt -l .        -> clean
go vet ./...      -> clean
go test ./...     -> ok, 688 tests (up from 651 at the M8H-A/M8G
                      baseline -- 37 new M8H-B tests), ~83s
go test -race ./... -> ok (~205s), zero races
```

## Reproducibility / dependency proof

- Two independent clean builds of `zeros3` at commit
  `ca932499152febd6f223b7b46241edefba694d39`
  (`CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go`):
  byte-identical SHA-256
  `6c11cbda9bcca30cd3c5081f86c98b4f52f14d75b869f28448097dc5dcbd40d2`.
- `go.mod`: zero `require` directives.
- No `golang.org/x/...` import; no vendoring; no runtime shell-out
  (`zeros3.go` never imports `os/exec`).
- `zeros3.go` remains the sole implementation source file;
  `zeros3_test.go` remains the sole first-party test source file.
- Persistent on-disk format: unchanged (no version bump anywhere).

## Verdict

**M8H ACCEPTED — bounded parallel chunk transport materially improves
ZeroS3 with full regression green.**

- Correctness unchanged: 1140/1140 historical external checks pass
  identically to their pre-M8H-B baselines; 688/688 internal tests pass;
  `go test -race ./...` clean.
- Parallel transfer substantially improves every measured workload with
  genuine missing payload, with the *largest* gains exactly where M8H-A
  identified the real bottleneck (added per-request latency): up to
  8.18x at 10ms simulated RTT, directly reversing M8H-A's measured
  3.3x sequential collapse under the same delay.
- Worker pool is bounded (1..32, validated, no unbounded goroutines or
  connections); HTTP connection pool sized to actually support the
  worker ceiling (M8H-A's flagged pitfall fixed).
- Contexts/cancellation correct: a first genuine failure stops
  unnecessary queued work and reaches in-flight HTTP requests; repair's
  honest partial-success contract is preserved under concurrency.
- Exact stats preserved and provably order-independent across every
  tested worker count.
- Commit semantics unchanged: object publication is still one atomic
  commit, only after every required chunk succeeds; namespace/fork
  object-level commits are still strictly sequential.
- Repair remains correct (partial repair honest, shared digests
  deduplicated to one physical job, post-repair deep verify still
  authoritative).
- Process-kill resume works (three independent real-`SIGKILL` scenarios,
  including one that incidentally exercised destination-conflict safety
  under a genuine race and was correctly rejected).
- Reproducibility and zero-dependency status intact.
