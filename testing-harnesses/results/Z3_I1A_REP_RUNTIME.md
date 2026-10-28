# Z3-I1A — representation runtime hardening

**Status: IN PROGRESS.** Base `b050175029f8a484db1e97c6678c863b4d4984dd`.
PR #72 remains draft; Q1 is not complete and no merge eligibility is claimed.
Persistent format changed: NO. Default behavior changed: NO.

## Mechanism and portability
The graph walker and representation counters already use digest keys. The
outer maintenance scan used four hex-keyed tables: temporary/final lengths,
referenced chunks, and valid chunks. It now builds final lengths directly and
shares the two read-only chunk sets on healthy scans. Faulty scans clone the
referenced set and remove only failed chunks, preserving exact repair findings
and fail-closed maintenance. Existing map consumers were audited for mutation.
Closure slices and digest sets are sized from the validated graph instead of
repeatedly growing. No persistent reference database or new primitive exists.
Source construction already shares root descriptors; no duplicate map to remove.
Root loading now rejects files above 64 MiB before reading and also limits the
read if the file grows after stat. The ZS3T parser and canonical bytes are intact.
A sparse 256 MiB corrupt live-root test proves bounded rejection and a scan fault.
No lab algorithm was rewritten or copied; these are production runtime changes.

## Provenance and memory
All workloads here are **SYNTHETIC_FIXTURE**, not natural histories.
`BenchmarkRepReachabilitySQLite`: full scan, compacted 16 MiB SQLite-shaped
fixture, 4096 pages, seed 1401, three samples of three iterations, Go 1.27.3.
| scan | allocated bytes/op (three samples) |
|---|---|
| baseline | 10,435,336 / 10,345,234 / 10,338,872 |
| final | 7,625,794 / 7,725,765 / 7,625,810 |
Median allocation falls 26.3%. Baseline times were 12.9–13.2 ms; intermediate
candidate 10.6–11.7 ms. Final timings were CPU-contended and are not a speed claim.
Allocation profiles include fixture/PUT setup; benchmark B/op excludes that setup.

The I0 black-box 1 GiB fixture (seed 77) reaches 266,240 logical components and
266,241 physical chunks. Baseline: off/prefer GET 1.0/5.0 s; server RSS 27/167 MiB;
deep verification 8.1 s, 211 MiB peak. An intermediate candidate allocated less
but peaked at 253 MiB during deep verification. This is not a memory success;
final healthy-set sharing peaks at 215 MiB during verification, versus baseline
211 MiB: no demonstrated verification RSS reduction. Final server off/prefer RSS
24/150 MiB (125 MiB delta; baseline 140 MiB delta); GET 5.7/2.7 s and verification
10.1 s. Concurrent validation makes elapsed times unsuitable for a controlled
before/after speed comparison. Q1-A's peak-verification-memory target is unresolved.

## Whole reads and descriptors
Baseline HTTP medians (five reads): SQLite 24 MiB CDC/rep 79.40/207.29 ms;
CAR 6 MiB 22.95/67.57 ms. SQLite 4 KiB 1.50/1.34 ms, 64 KiB 0.67/1.24 ms;
CAR 4 KiB 0.50/2.05 ms, 64 KiB 0.81/2.40 ms. Reproduction uses unchanged I0 harness.
Final compacted-store medians (ms, CDC/representation), five repetitions:
| fixture | whole | 4 KiB | 64 KiB |
|---|---|---|---|
| SQLite 24 MiB | 33.77/96.46 | 0.05/0.17 | 0.07/0.24 |
| CAR 6 MiB | 6.55/30.12 | 0.05/0.41 | 0.16/0.56 |
| tar→SQLite 8 MiB | 10.16/27.39 | CDC fallback | CDC fallback |
| Merkle 20k keys | 10.67/89.66 | CDC fallback | CDC fallback |
These contended measurements confirm remaining overhead, not a controlled speed
regression estimate against I0. Whole rep/CDC physical bytes are 25.04/24.59 MB
(SQLite), 6.78/6.70 MB (CAR), 8.35/8.20 MB (tar), 1.85/0.72 MB (Merkle).
Read selection and SHA verification remain unchanged. Whole-read policy is still
unfinished; no speed improvement is claimed. Root descriptor streaming is also
unfinished; current change bounds hostile disk input rather than changing ZS3T.

## Transfer audit and remaining Q1 work
Sync and native replication share `handleSyncCommit`; add best-effort analysis
only after its successful namespace commit, never on conflict or CDC failure.
Full/delta bundles share `bundleImporter.publish`; wait until descriptor
publication succeeds, cover reused as well as newly staged manifests, and avoid
holding snapshotMu during analysis. The CLI currently has no representation-mode
option, so destination configuration and crash/compatibility coverage need an
explicit follow-up. No transfer path was changed in this checkpoint.
Verify retains ChunkLength through later pack/object decoding although it no
longer consumes lengths: releasing this local scan field before decode is a
concrete next memory experiment (not implemented or credited here).
Q1 remains active; resume whole-read policy, descriptor economics, and transfer
backfill before advancing to Q2. Do not restart the completed characterization.

## Validation
Focused root-order/shared-child/cycle/live-root/wire/oversized-root checks pass.
Final representation/corruption/reachability/repair subset passes (58.0 s).
Final expanded race passes (77.7 s), including corrupted/missing repair findings.
Final black-box: compat 54, bundles/replication 24, lifecycle 32, crash 24
checks pass after healthy-set sharing. Full read matrix passes (46.5 s).
Final vet and formatting pass. Intermediate full suite passes (361.5 s).
Final full suite FAILS (251.5 s): replication process-interruption safe-mode
conflict, and chunk-reuse test observes 3 missing chunks instead of 4.
Unchanged isolated tests, three repetitions: interruption fails 3/3 on candidate
AND unchanged origin/main (transfer finishes before the fixed 150 ms kill);
chunk-reuse passes 3/3 on both. No tests were edited, removed or skipped.
The chunk-reuse full-suite failure remains unresolved; no final full PASS claim.
Keep draft until these gates and unfinished Q1 work are resolved.
Old binaries' level-6 refusal and levels 1–5 are covered by the compat phase.
Production remains stdlib-only; root go.mod/go.sum unchanged; no wire golden edit.
Production LOC 27,504 → 27,537 (+33); new major helpers 0; duplicated lab logic 0.
