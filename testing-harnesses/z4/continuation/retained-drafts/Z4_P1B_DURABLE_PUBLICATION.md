# Z4-P1B: durable publication and small/medium-object ingest

Verdict: **P1B_PRODUCTION_READY** for what it changes (1 MiB-16 MiB PUT 2.8-4.1x, multipart 2.4-2.9x, 4-64 KiB PUT 1.2-1.5x, no large-object regression); tiny objects (< 256 KiB) have reached the file-per-chunk ceiling and **P1C is recommended** (section 16).
Base `origin/main` `b220973cbf7ebfb518c1ebb70ea5ce9ee04e37cd` (P1A PR #105 verified `merged=true`, its optimizations present); branch `z4/p1b-durable-publication`. Production diff: `zeros3.go` only (+941/-131), standard library only, `go.mod`/`deps-proof.txt` unchanged, **no store, journal, pack, manifest or wire format change**.
Evidence: [`../z4/p1b/`](../z4/p1b/README.md) (JSONL, scripts, strace audit, profiles, `results/P1B_TABLES.md`). Method = P0 `p0bench` unchanged: real server over HTTP, fresh store+process per repetition, every PUT replayed byte-for-byte in a fresh process (all runs below `verified:true`).

## 1. Starting state and baseline
Same VM class (4 vCPU Firecracker, ext4 on virtio, Go 1.27, the load generator shares the CPUs). Absolute numbers drift between sessions (baseline 4 KiB c8 ran 664-833 ops/s), so **every claim is an interleaved A/B on this instance**, 3 rounds, median; first/second-position bias was checked by order swapping (section 6). Baseline reproduces P0/P1A mechanisms: 6.25 fsync, 6.26 openat, 2.0 mkdir, 2.0 rename per 4 KiB object; mutex delay 14.1 s in a 3.0 s window at 4 KiB c8.

## 2. Durability and visibility ordering
Full model in [`DURABILITY_MODEL.md`](../z4/p1b/DURABILITY_MODEL.md). Kept in kind: staged file `fsync` before `rename`; directory `fsync` after the rename and before a chunk is reachable through the CAS index; shard directory created and its parent `fsync`ed before use; manifest durable before the journal frame that names it; the journal `fsync` is the commit point; acknowledgement only after the `fsync` covering the request's frame. Distinguished: visible to the process (final path exists) / reachable through the CAS index (claim released, entry durable) / recoverable after process kill / recoverable after power loss (needs all barriers) / acknowledged. Nothing is deferred past the acknowledgement and no file-data `fsync` was removed; only *sharing* (directory fsyncs, journal fsync) and *overlap* (independent chains) changed.

## 3. Original bottlenecks (remeasured, 4 KiB c8 profile)
`casPubMu` held across rename + directory fsync (mutex delay 14.1 s per 3.0 s); `os.CreateTemp` 37% of server CPU, fsync 11%, mkdir/rename/dir-fsync ~20%; `Store.mu` held across the journal fsync (readers and the PUT's own `HeadBucket` wait behind every flush); 16 chunk files + 16 fsyncs + 32 mkdir-ish operations for a 1 MiB object; multipart parts always loose.

## 4. Designs compared
`pubsim` (exact filesystem operations of a small PUT, `pubsim/`; run-to-run noise +-15%, so it only ruled designs in or out): global lock -> per-digest claims +9..+28%; shared directory fsync 0..+6%; group-committed journal and chain overlap within noise (-12..+7%); `syncfs` barriers: same sequential depth (the data barrier must precede the rename), no gain beyond noise, Linux-only and flushes unrelated dirty data (rejected); all fsyncs removed: ceiling 2.6-2.9k ops/s, i.e. the file operations themselves cap the layout; shard-pre-creation ~0; sharded `tmp/` (real server, `rejected_tmp_sharding.jsonl`): 4 KiB c8 4.3 vs 4.2 MiB/s, no effect (create cost is inode allocation, not directory contention); log-structured segment prototype: 14-41k ops/s (P1C). Real-server direct-pack threshold probe (`pack_threshold_matrix.jsonl`): packing from 1 MiB doubles 1 MiB PUT, from 256 KiB also helps 256-512 KiB (+19..+64%), 4 MiB threshold leaves 1 MiB loose.

## 5. Implemented (all in `zeros3.go`)
1. **Per-digest publication claims** (`pubRegistry`, 64 shards) replace the global `casPubMu`; lookups that see a claimed file wait; duplicate publishers wait outside any claim (no hold-and-wait, tested with opposite-order batches).
2. **Shared directory fsync** (`dirSync`: join the next round, one fsync in flight per directory, error to every round member) for chunk shards, `manifests/`, `packs/`.
3. **Shard directories**: created once per directory (claims), trusted only after the parent fsync (readiness bitmaps: 256 + 65,536 bits, cleared on rename ENOENT), created concurrently with the staged-file fsyncs.
4. **Journal group commit** (`commitQueue`, `Journal.appendFrames`): one write + one fsync per queue of concurrent commits, validated in order against live state + earlier batch members (If-Match/If-None-Match races tested), applied in journal order after the fsync; no timer. **`commitMu`** serializes all journal mutators and the leader releases `Store.mu` during the fsync, so GET/HEAD/LIST/`HeadBucket` do not wait for flushes.
5. **Manifest publication overlaps the final chunk flush**; the journal commit waits for both.
6. **Medium-object direct packs**: objects (and multipart parts) from 256 KiB write their new chunks into one pack (new content < 64 KiB stays loose; >= 64 MiB unchanged; serial ingest below the 32 MiB pipeline floor; locator append amortized O(1); medium packing stops at 200,000 packs). `ZEROS3_DIRECT_PACK_MIB=0` disables direct packs.
7. Defect found by the new crash matrix and fixed: a panic between journal fsync and cursor advance let the next queued commit overwrite durable frames (`journal: corrupt ... bad magic` on reopen). The cursor now advances before the hook and an interrupted commit **poisons** the journal (fail-stop, as for a failed write/sync).

## 6. Before/after (release-candidate binary vs baseline, 3 interleaved rounds; full table `P1B_TABLES.md`)
| cell | base | final | x | p50 ms | p99 ms | srv cores | CPU-s/GiB |
|---|---:|---:|---:|---|---|---|---|
| 4 KiB c1 / c8 / c32 (ops/s) | 273 / 664 / 756 | 384 / 879 / 1086 | 1.41 / 1.33 / 1.44 | 3.4->2.5 / 11.4->8.8 / 41.9->27.3 | 6.7->4.9 / 24.6->17.7 / 66->75 | 0.69->1.12 / 1.84->2.31 | - |
| 16 KiB c8 / 64 KiB c8 / c32 (ops/s) | 735 / 510 / 520 | 869 / 633 / 802 | 1.18 / 1.24 / 1.54 | 10.5->8.9 / 14.7->12.2 / 57->38 | 18->16.5 / 30->23 / 105->88 | 2.0->2.3 | - |
| 1 MiB c1 / c4 / c8 / c16 (MiB/s) | 38 / 72 / 85 / 87 | 88 / 213 / 248 / 249 | 2.32 / 2.96 / 2.90 / 2.85 | 24->11 / 52->17 / 87->31 / 166->61 | 77->33 / 116->31 / 216->62 / 386->138 | 0.8->1.0 / 1.65->2.39 | 21->12 / 23.5->11 |
| 4 MiB c4 / 16 MiB c4 | 84.5 / 83.3 | 317.8 / 344.5 | 3.76 / 4.14 | 182->49 / 721->186 | 317->62 / 1026->196 | 1.7->2.5 | 20->8 |
| 1 MiB dup50 c4 / similar revisions 16 MiB x5 c4 | 83 / 349 | 232 / 557 | 2.81 / 1.59 | 52->17 / 175->116 | 105->33 / 218->133 | - | 20->10 / 6.2->5.5 |
| mixed sizes 4k:50 64k:30 1m:15 16m:5, c4 / c16 | 108 / 102 | 271 / 309 | 2.51 / 3.02 | 8.4->7.6 / 58->41 | 531->140 / 1937->285 | 1.6->2.5 | 15->9 |
| 64 MiB direct pack c1 / c4; 1 GiB c1 | 372 / 418; 390 | 362 / 424; 409 | 0.97 / 1.01; 1.05 | 167->169 / 588->601 | - | 2.6 / 3.0 / 2.8 | 7.0->7.2 |
Order-swapped re-checks of 64 MiB c1/c4 and 1 GiB (3 rounds each way, `big_order_*.jsonl`): 408/499/460 vs 413/500/464 and 401/490/454 vs 398/504/458 MiB/s: **no meaningful regression** (the 0.97x above is run-order bias). GOMAXPROCS 1 / 2 / 4 (`gomaxprocs.jsonl`): 1 MiB c4 54->96, 66->180, 72->226; multipart pc4 48->91, 62->147, 61->197; 64 MiB c1 137->139, 265->261, 403->400; 4 KiB c8 578->631, 756->883, 783->952. Populated vs cold store, in-process 4 KiB c8 (`ZEROS3_P1B_BENCH=1`): populated 1631/1502 -> 2391/1941 ops/s; cold 1258/869 -> 1080/1007 (within noise).

## 7. Syscalls and fsyncs per object (strace, whole server lifetime / objects)
| | fsync | openat | mkdir | rename | newfstatat |
|---|---:|---:|---:|---:|---:|
| 4 KiB c8 base -> final | 6.25 -> 5.70 | 6.26 -> 5.96 | 2.00 -> 1.25 | 2.00 -> 2.00 | 4.8 -> 4.0 |
| 1 MiB c4 base -> final | 51.4 -> 5.0 | 66.7 -> 7.1 | 32.0 -> 0.06 | 17.3 -> 2.0 | 64 -> 19 |
Disappeared: the per-chunk file, fsync, rename and shard-directory operations of >= 256 KiB objects (one pack per request); per-request `mkdir`+parent fsync in warm shards (cache). Shared: directory and journal fsyncs of concurrent requests (fsync calls per 4 KiB object 6.25 -> 5.70; journal writes 1.00 -> 0.74). Deferred: none. Not removed: the chunk file `fsync` and manifest file `fsync` of objects < 256 KiB.

## 8. Lock contention
4 KiB c8: mutex delay **14.08 s -> 1.38 s** per 3 s window; what remains is `Store.mu` at `HeadBucket` (19%) and runtime locks; the remaining cost is syscalls (61% of CPU, `openat` 37%). Attribution on the loose path (`ablation_loose.jsonl`, 4 KiB c8 / 64 KiB c8 / 1 MiB c4): baseline 777 / 469 / 69; claims only 859 / 610 / 106; all mechanisms 985 / 635 / 114; removing overlap -13%, group commit -6%, shard cache -3%, directory-fsync sharing ~0% (kept: it removes redundant fsync calls and is the right behaviour on slower devices, but its benefit is not measured here).

## 9. Scaling
4 KiB c1/4/8/16/32: 273/705/664/772/756 -> 384/819/879/1057/1086 ops/s (baseline flat from c4, final keeps scaling to c32). 1 MiB c1/4/8/16: 38/72/85/87 -> 88/213/248/249 MiB/s (baseline plateau 87 MiB/s broken; final plateaus at 2.6 of 4 cores including the load generator).

## 10. Multipart
End-to-end 64 MiB upload with 8 MiB parts incl. completion: 1 / 4 parts in flight **45 -> 108 / 60 -> 174 MiB/s (2.4x / 2.9x)**, CPU-s/GiB 22->13 / 28->12, p50 1308->588 / 1024->363 ms. Parts >= 256 KiB are written as packs (no loose publication), completion re-chunks across part boundaries exactly as before (test: the completed object's chunk list and SHA-256 equal a single PUT of the same bytes), part replacement/retry, abort + `gc` (fully dead packs removed, live data intact), restart and `verify -deep` are tested. Creation, upload and completion were **not timed separately**; completion still reads every part once with verification (the design keeps it: unchanged CDC and integrity checks).

## 11. Interaction with P1A
Pack publication shares `dirSync`; claims are only held inside `casPublish` and never across a wait; lock order `commitMu` -> `Store.mu` -> `Journal.mu`; hooks on background goroutines use atomics. 64 MiB / 1 GiB / 4x64 MiB unchanged, GOMAXPROCS 1/2/4 sweep, `go test -race ./...` passes (1206 s), and the pre-existing P1A crash tests pass (hook counters were already atomic).

## 12. Crash and recovery qualification
* **Real `kill -9`** (`TestP1B_RealKill_*`): 12 concurrent clients, mixed 4 KiB-3 MiB PUTs incl. shared keys, killed at random moments, 6 rounds on one accumulating store, default policy (449 acknowledged objects) and loose-only (243): every acknowledged object byte-identical, shared keys hold a version someone wrote, `verify -deep` OK, `gc` keeps all, tmp left over is collectable.
* **Deterministic crash matrix** (`TestP1B_CrashMatrix_*`): 160 cases = {loose, medium} x {1, 6 concurrent PUTs with a shared body, unique bodies and chunks shared with an earlier acknowledged object} x 20 boundaries (stage, stage fsync, rename, directory fsync, flush, chunks published, manifest published, journal write/fsync, after apply, pack record/fsync/rename/dir-fsync/published) x Nth firing; 99 cases injected a crash (the rest never reach an Nth firing and run clean); after the crash every goroutine dies at its next hook; checks: acknowledged data intact, nothing half-visible, `verify -deep`, `gc`, no leaked claim or queue entry, every unacknowledged PUT retryable. This found the journal-cursor defect of section 5(7).
* **Failure injection**: group-commit batch with journal failure (all members fail, none visible, acknowledged data kept), leader panic, round error propagation, panic in the fsync leader, failed publisher with a waiting duplicate (takes over), stale shard cache, vanished directory, opposite-order batches, an observer that must wait for a duplicate writer, `If-None-Match` race (exactly one winner).
* **Power-loss ordering audit** (`strace -y`, sequential and concurrent, loose and medium, 4 sizes): 0 violations of R1-R4 for baseline control and all four final modes; 6 negative controls (a barrier removed from the real trace) are each detected. A first audit run flagged 7 violations: an fd-reuse artifact of the first parser, resolved with `-y` paths and the control. **This is ordering evidence, not a power-loss or device-fault experiment**; no block-device fault harness was available.
* Compatibility: the baseline binary `verify -deep`s and reads byte-exact a P1B store (400 objects x 2 revisions: 310 packs + 591 loose chunks; and six multipart objects); production formats and `FORMAT.json` levels unchanged. The reproducible build passes.

## 13. Regressions and costs
Tail latency of tiny objects at c32 rises (4 KiB p99 66->75 ms, 16 KiB 57->73 ms) while p50 and throughput improve (queueing behind group commits). Mixed 8 readers + 4 writers of 4 KiB: reads +12% and writes +14%, read p99 5.8 -> 7.4 ms. Peak RSS for medium objects 31 -> 62 MiB at 1 MiB c16 (pack buffers), 19 -> 39-41 MiB at c4. **Medium packs change defaults**: first pack raises `FORMAT.json` to level 2 earlier (as a >= 64 MiB PUT always did); startup reads every pack index, 3,000 small packs add ~0.2 s (75 us each; capped by the 200,000-pack valve); `gc` reclaims a pack only when all its records are dead (`repack` otherwise); loose `gc` granularity is per chunk. 76 failing root tests/subtests under the new default and 7 external harness packages assert a loose layout for objects >= 256 KiB: test binaries keep the previous threshold (`init` in `zeros3_p1b_test.go`, `ZEROS3_TEST_MEDIUM_PACK=1` runs the production default; failing list in `results/`), the runner starts those 7 harnesses with `ZEROS3_DIRECT_PACK_KIB=65536`; the other harnesses and the real-kill tests run the production default.

## 14. Rejected alternatives
`syncfs` barrier; sharded `tmp/` staging; fixed-delay group commit (not needed: natural batching); lowering the global threshold below 256 KiB (64 KiB single-chunk objects gain nothing: a pack per object has the same file count); `O_TMPFILE`+`linkat` (arch-specific constants, needs `/proc`, saves one dir entry of ~6 operations; not implemented); writing manifests straight to their final path (partial files in `manifests/` would look like corrupt manifests to `verify`); removing any file `fsync`; deferring directory durability past the acknowledgement.

## 15. Remaining bottleneck
Objects below 256 KiB: two file creations + renames + fsyncs per object (`openat(O_CREAT)` 37% of CPU, ~5.7 fsync, ~6 openat); with all fsyncs removed the same operations still cap near 2.7k ops/s on this VM. 4 KiB c8 is 879 ops/s, 1.33x, short of the 2x objective, as is 64 KiB c8 (1.24x). Medium/large objects are now CPU-bound (SHA-256, MD5, CDC, page cache), like P1A.

## 16. Is P1C warranted?
Yes, for small objects: [`P1C_PROPOSAL.md`](../z4/p1b/P1C_PROPOSAL.md) (segments that seal into pack-v1, unified group-committed log, format level bump). The prototype upper bound is 7-20x the per-file layout (14-41k vs 1.5-2.2k ops/s in the same simulator), realistic estimate 3-5x end to end. Not started. P2 (read path) is independent and can start now.

## 17. Validation and merge status
| gate | result |
|---|---|
| `gofmt`, `go vet`, modules/deps (stdlib only), reproducible build | PASS |
| root suite (423 s), `go test -race ./...` (1206 s) | PASS |
| `validate.sh` s3 (14/14), sync (8/8), client (AWS SDK + minio-go), apps, cas-batch, direct-pack, direct-pack-life, vectors, crash | PASS |
| `validate.sh static` | **FAIL, pre-existing**: same as P1A (source map 34 of 35, test map 23 of 40 entries disagree); not touched |
| P1B tests: primitives, group commit, medium packs, real kill, crash matrix, ordering audit | PASS |
PR: see the PR for the head SHA and merge SHA; B2 #97 and drafts #80/#81 untouched; representation authority not enabled; no scheduled routines; P1C and P2 not started.
