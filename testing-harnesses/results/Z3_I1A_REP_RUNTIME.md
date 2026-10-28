# Z3-I1A — production representation runtime hardening

**Verdict: I1A_PARTIAL.** Base `b050175029f8a484db1e97c6678c863b4d4984dd`.
Persistent format changed: NO. Default mode changed: NO. CDC remains authoritative.
Deep-verification peak RSS and descriptor streaming remain measured debts.
All merge gates for the compatibility-preserving runtime changes pass.

## Mechanism
The graph walker and representation counters already use digest keys. The
outer maintenance scan formerly retained four hex-keyed tables: temporary/final
lengths, referenced chunks, valid chunks. It now builds final lengths directly
and shares read-only referenced/valid sets on healthy scans. Faulty scans clone
and remove only invalid digests, preserving repair findings and fail-closed GC.
Closure slices and digest sets are sized from the validated graph. Verify drops
its no-longer-used component-length table before pack/object decoding.
Root loading rejects >64 MiB files before allocation, and bounds reads if they
grow after stat. A sparse 256 MiB live-root regression checks bounded rejection.
The Source already shares root descriptors, so there was no second map to remove.
ZS3T v1 still materializes the descriptor map; no wire expansion was attempted.
Whole GET now chooses CDC in every mode. Prefer still chooses supported structural
ranges; direct whole decoding remains available to proof/deep verification.
The measured decoder overhead justified this policy; SHA checks were not removed.

## Transfer/compatibility matrix
| path | publication order and optional behavior |
|---|---|
| PUT/Copy/multipart | existing prove-before-root-before-namespace sequence retained |
| sync/native replicate | shared handleSyncCommit: CDC+namespace commit, then best-effort root |
| full/delta import | verified chunks/manifests, snapshot publication, unlock snapshotMu, optional roots |
| repeated bundle import | backfill reused as well as newly staged manifest UUIDs |
| import CLI | new -representations off/shadow/prefer; default off, no protocol/bundle change |
| analysis failure | transfer/import succeeds; authoritative content stays exact, no root or level raise |
| unsupported input / off | no root; old store remains below level 6 |
| crash before first root | existing I0 rule: level 6 without a root is valid if format raise already happened |
The existing fsync/rename rule requires raising format before root publication;
that crash window was preserved, not disguised as an atomic two-file update.
Mode/analysis-failure tests cover local sync, native replicate, full/delta import,
reused manifests and the CLI. Twelve injected crash cases cover sync and import
at analysis, flush, object, staged root, rename and published-root boundaries.
After restart CDC object/snapshot remains valid; roots are absent or fully valid.

## Corpus, reads, memory
Evidence is SYNTHETIC_FIXTURE, not natural history. All serialized reconstructions
retain SHA-256, length and byte equality checks. Storage remains additive: no
first-sight/history space saving is claimed by this runtime-only batch.
16 MiB SQLite-shaped full-scan benchmark (4096 pages, seed 1401; Go 1.27.3):
| scan | allocated B/op, three samples of three iterations |
|---|---|
| baseline | 10,435,336 / 10,345,234 / 10,338,872 |
| shared healthy sets | 7,625,794 / 7,725,765 / 7,625,810 |
Median allocation drops 26.3%; contended microbenchmark times are not speed claims.
Isolated sequential I0/candidate 1 GiB harness, seed 77, 266,240 components:
| metric | baseline | candidate |
|---|---|---|
| off whole GET | 1.2 s | 1.1 s |
| prefer whole GET | 2.7 s | 1.1 s |
| prefer server peak RSS | 161 MiB | 143 MiB |
| prefer/off RSS delta | 132 MiB | 118 MiB |
| deep verify | 8.6 s / 216 MiB | 8.0 s / 218 MiB |
No demonstrated deep-verification RSS win. Avoid attributing the server reduction
to a particular map alone: CDC whole-read policy also reduces serving state.
All 1 GiB whole/range/deep checks pass. This is one paired run, not a distribution.

The pre-policy direct decoder matrix (compacted; median of five; ms CDC/rep)
keeps the remaining cost explicit. Physical whole bytes are SQLite 24.59/25.04 MB,
CAR 6.70/6.78 MB, tar→SQLite 8.20/8.35 MB, Merkle 0.72/1.85 MB.
| fixture | whole | 4 KiB | 64 KiB |
|---|---|---|---|
| SQLite 24 MiB | 33.77/96.46 | 0.05/0.17 | 0.07/0.24 |
| CAR 6 MiB | 6.55/30.12 | 0.05/0.41 | 0.16/0.56 |
| tar→SQLite 8 MiB | 10.16/27.39 | CDC fallback | CDC fallback |
| Merkle 20k keys | 10.67/89.66 | CDC fallback | CDC fallback |
Final whole policy avoids those decoder costs; supported structural ranges retain
existing amplification. No claim that every structural range beats CDC.

## Test integrity and final gates
A baseline replication interruption test assumed 20 MB could not transfer within
150 ms; it failed 3/3 on unchanged main and candidate. It now gates a real source
fetch before killing the actual process. The reuse test now waits for the other
remote upload handlers before measuring durable state; client cancellation alone
does not drain remote handlers. All original state/byte/reuse assertions remain.
Whole-read counter expectations reflect the documented CDC policy. Decoder
corruption/copy-fallback/concurrency tests explicitly exercise repOpenReader;
their corrupt-input and verified-prefix assertions remain. Direct decoder
benchmarks also remain direct. No canonical test or failing case was removed.
Final race PASS (137.1 s); full repository suite PASS (see checkpoint validation),
vet and repository gofmt check PASS after the contract adaptations.
Final black-box: 54 compat, 24 bundles/replication, 32 lifecycle, 24 crash PASS.
These include levels 1–5, old-binary format-6 refusal without mutation, wire/S3
transcripts, cross-version bundles, default off, and fail-closed maintenance.
Transfer-focused modes/failure/CLI/crash tests PASS; wire goldens unchanged.
Production stdlib-only; zeros3.go remains the sole production source;
root go.mod/go.sum unchanged. Production LOC 27,504 → 27,584 (+80); two new
major helpers (optional builder and mode-aware bundle entry). No duplicated lab
logic: this batch changes production runtime boundaries only.

## Carry-forward
Peak verification RSS and descriptor materialization remain measured negatives.
Further global digest-key migration touches ordinary repair/GC/tier/pack APIs;
that broader change should have its own evidence rather than consume this queue.
No persistent refcount, planner state, wire version, kind number or defaults changed.
After gates pass, record I1A_PARTIAL and advance to Q2; do not restart characterization.
