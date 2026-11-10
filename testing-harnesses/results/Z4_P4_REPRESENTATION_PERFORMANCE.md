# Z4-P4: production representation performance and asynchronous construction

Verdict: **P4_PRODUCTION_READY**. State: PR #110 merged with the `merge` method (section 22).

Environment: 4 vCPU Firecracker VM, 15 GiB, ext4 on virtio, Go 1.27.0, `GOMAXPROCS=4`. Corpus: the P0 corpora (coreutils 9.5-9.7 tar/gz/zst, hiredis git packs, F-Droid DEX, Newtonsoft CLR, pyarrow Parquet snappy/zstd, rocksdict SST, SQLite 31 MiB, Pythia-14m safetensors; Merkle and CAR synthesised with the production fixtures). Every object of every run is read back whole and by ranges and compared with the input bytes, and `verify -deep` must pass, or the run fails. "Before" is the starting main `68b2593` plus phase timers (no behaviour change); "after" is this branch. Raw rows: `testing-harnesses/z4/p4/results/*.jsonl`; reproduction: `testing-harnesses/z4/p4/README.md`.

## 1. Starting state

`main` = `68b259377512a8e9e27e323f5dbdd8aa1e85aeb3` (merge of P3). Branch `z4/p4-representation-performance`. Drafts #97/#80/#81 untouched, no authority code, no format level change (levels 6-9 and 11 only, 10 unused), no new dependency, no wire/journal/S3 change. Pre-existing static drift (`harness/toc_check`, 31 TOC entries) reproduces identically on the starting main (§15).

## 2. Architecture and lifecycle audit (verified against the code, not the earlier docs)

Flow today: `ingestStream` -> chunks durable -> `publishManifest` -> **`maybeBuildRep` (inline, before the namespace commit)** -> `commitObjectRoot`. `repBuild`: `repCandidate` sniff -> `encodeKind` (components through a `chunkSink`) -> `finishRep` -> `flush` -> `proof` (production decoder over the production Source, compared with the object's length and SHA-256) -> `reachable` -> `publishRepRoot` (floor, tmp+fsync+rename+dir fsync). A root is a sidecar file keyed by manifest UUID; components are ordinary CAS chunks (a component up to one packed chunk is stored as itself; a larger one is a *virtual* component, a flat chunk descriptor inside the root). Destructive maintenance (`gc`, `repack`, `compact`, tier moves, prune) takes the exclusive store flock, which a running server's shared lock refuses: no maintenance overlaps a build.

Where the work goes (classification, from the profiles in §3):

| Work | Class | Evidence |
|---|---|---|
| Exact-codec replay and search (gzip, zstd, parquet-zstd, DEX/CLR indexes) | necessary algorithmic work, done twice (analysis + proof) | gzip `repIRwindow.search` 55% flat; zstd `matchModel.walk/advance/lce` 50% |
| Proof (production decoder over the Source) | necessary verification | 20-45% of a build |
| Source reads during analysis | **duplicated**: every non-sequential `ReadAt` (tar header probe, footer, table) opened an open-ended reader that planned a run of up to 64 contiguous chunks, decoded and SHA-256'd all of them and dropped all but the first | tar: 1.4 s of 3.0 s CPU in `tarPrescan` |
| Small-object components | **filesystem overhead**: `looseSink` (one file, fsync, mkdir per component) although ordinary small PUTs use the P1C segment | Merkle 80 KB: 969 fsyncs, 5,486 opens, 357 renames |
| Medium/large-object components | already packed (P1B/P1C `packSink`) | 9-15 files per store, 29-38 fsyncs |
| Root publication floor | **duplicated** walk of the closure just walked by `reachable` | Merkle: 221 ms of 1,024 ms |
| `prefer` range reads | **repeated per request**: root file read+parse, representation object read+SHA-256+parse, CAR table parse+index sort | tar 4 KiB: 40% of the request |
| Deep verify | necessary, but serial over independent roots | clr: 4 roots, 731 ms |
| `FSE.prev` binary search per sequence field | algorithmic | 16% of zstd/parquet-zstd CPU |
| Speculative candidates discarded | none found beyond early `errRepUnsupported` (an unsupported SST-zstd object is rejected after ~12 ms of analysis) | `k09_sst_zstd` row |

Component publication was **not** the dominant cost after P1B/P1C for objects of 256 KiB and above (publish = 1-6% of build time), contrary to the P0 expectation; it was for small objects (§7).

## 3. Baseline on the P3 engine (kinds 1-12, shadow, serial, "before")

| kind | MB | PUT MB/s off | shadow | slowdown | components | files | store MB off -> shadow | RSS MiB off -> shadow | deep ms off -> shadow |
|---|---:|---:|---:|---:|---:|---:|---|---|---|
| k01_tar | 187.0 | 408 | 49.1 | 8x | 21,117 | 15 | 179.4 -> 275.0 | 210 -> 428 | 564 -> 1133 |
| k02_car | 19.2 | 131 | 50.6 | 3x | 8,106 | 13 | 19.0 -> 38.4 | 86 -> 103 | 50 -> 179 |
| k03_sqlite | 63.4 | 188 | 78.7 | 2x | 15,722 | 12 | 58.5 -> 94.8 | 82 -> 161 | 158 -> 471 |
| k04_git | 4.1 | 109 | 5.7 | 19x | 10,397 | 15 | 2.8 -> 5.7 | 23 -> 39 | 16 -> 114 |
| k05_merkle | 5.0 | 133 | 4.6 | 29x | 42,677 | 9 | 5.0 -> 14.0 | 42 -> 72 | 16 -> 574 |
| k06_gzip | 14.9 | 161 | 0.9 | 174x | 8,701 | 9 | 14.9 -> 77.6 | 32 -> 87 | 40 -> 7771 |
| k07_zstd | 10.6 | 136 | 1.2 | 111x | 7,812 | 10 | 10.6 -> 81.0 | 28 -> 126 | 34 -> 3428 |
| k08_model | 56.3 | 181 | 42.0 | 4x | 434 | 12 | 56.4 -> 112.9 | 75 -> 176 | 161 -> 579 |
| k09_sst_snappy | 6.6 | 120 | 5.9 | 20x | 9,020 | 12 | 6.6 -> 39.9 | 25 -> 72 | 22 -> 410 |
| k09_sst_zstd | 1.5 | 78 | 30.4 | 3x | 0 | 7 | 1.5 -> 1.5 | 18 -> 24 | 6 -> 10 |
| k10_parquet_snappy | 12.4 | 151 | 5.7 | 26x | 4,668 | 9 | 12.4 -> 31.7 | 30 -> 75 | 40 -> 739 |
| k10_parquet_zstd | 17.3 | 153 | 2.0 | 79x | 9,443 | 12 | 12.3 -> 37.2 | 36 -> 109 | 55 -> 2140 |
| k11_dex | 18.6 | 142 | 4.1 | 35x | 4,246 | 12 | 17.7 -> 30.9 | 38 -> 170 | 48 -> 1675 |
| k12_clr | 2.8 | 83 | 1.6 | 51x | 1,089 | 14 | 2.8 -> 5.5 | 21 -> 46 | 15 -> 731 |

P0 PUT throughput, off -> representation: tar 114 -> 14, SQLite 96 -> 18, CAR 78 -> 2.7, Git 49 -> 1.2, Merkle 99 -> 1.0, gzip 93 -> 0.68, zstd 58 -> 1.0 MiB/s. On the P3 engine (MiB/s, this table): tar 408 -> 49.1, SQLite 188 -> 78.7, CAR 131 -> 50.6, Git 109 -> 5.7, Merkle 133 -> 4.6, gzip 161 -> 0.9, zstd 136 -> 1.2. The per-component file cost is gone (P1B/P1C packs); the exact-codec, analysis and proof CPU remains.

Bottleneck map (CPU flat, PUT phase only): kind 1 tar SHA-256 65% (+ the duplicated probe reads); 2 CAR SHA-256 40-60%; 3 SQLite SHA-256 53%; 4 Git inflate + SHA; 5 Merkle syscalls 15% + SHA 16% + the closure walks (45% of wall in `reachable`+floor); 6 gzip `search` 55% + `matchLen`/`headGet`/`insertTo` 17%; 7 zstd `walk`/`advance`/`lce` 50% + `FSE.prev` 16%; 8 model SHA-256 52%; 9 SST snappy SHA 16% + `encodeCPPHash` 14%; 10 parquet-zstd zstd replay 60%, parquet-snappy map access 22%; 11 DEX SHA 19% + `sort.Slice`/`bytes.Compare` ~30%; 12 CLR `sort.partition`/`cmpbody` ~35%. Shared: SHA-256 (1.34 GB/s on this VM), the chunk-granular source verification, closure walks.

## 4. Construction scheduling (`-representation-build deferred`)

Compared: (a) inline (existing, kept as default); (b) deferred, in-process, bounded, ephemeral queue (**chosen**, opt-in); (c) persistent queue (rejected: needs its own journal record, replay and torn-tail rules plus a second truth about which manifests deserve a build, to protect work whose loss costs only a missed acceleration; the object is complete without it); (d) idle-only opportunistic (kept in a bounded form: workers wait up to 5 s for no request in flight, then run); (e) coalescing (implemented: one build per object SHA-256, joiners get cloned roots, last 4,096 digests remembered).

Semantics, invariants, lock order and failure boundaries: `testing-harnesses/z4/p4/DEFERRED_CONSTRUCTION.md`. In short: the PUT is acknowledged after the authoritative commit; the root is published by exactly the same rule as before (components durable, exact proof, atomic rename) and names the immutable manifest the job was created for; before publication the worker checks the key still names that manifest (current or retained history); admission is bounded (256 jobs, 8 GiB, never blocks a PUT, drops are counted); `Store.Close` abandons queued work and stops running builds at their next read/phase boundary; a panicking build is recovered. `prefer` mode reads through CDC until the root appears. CopyObject and the sync-commit handler schedule after their commit; offline bundle import stays inline.

## 5. Foreground versus background

PUT latency (§ table 1): the deferred acknowledgement is within 10 ms of the `off` latency for 12 of the 14 object sets (tar +96 ms, Git +18 ms; gzip 15,327 -> 89 ms, Merkle 1,024 -> 39, zstd 8,205 -> 70, CLR 1,656 -> 35, tar 3,635 -> 524) and the representation becomes available after the same time the inline build took (+0-5%). **Total CPU is unchanged by deferral** (table 2: deferred vs inline after, -1% to +9%): the work moved, it did not shrink.

Interference with foreground traffic (open-loop 200 and 1,500 ops/s of 64 KiB PUT + 1 MiB range GET while the 15 MB gzip object is stored; one worker): foreground p50 1.0-1.3 ms and p99 3.6-6.0 ms during the build in both policies against 2.4-3.9 ms p99 before it; max 90-130 ms in both (GC/scheduler, same for inline). The builder occupies about one core of four in both policies, so deferral does not hide contention, it only stops the heavy PUT's *client* waiting 14-15 s.

| load | policy | heavy PUT ack ms | available ms | CPU ms | fg p50/p95/p99 ms during | fg ops during |
|---|---|---:|---:|---:|---|---:|
| 200 ops/s | off | 81 | 81 | 110 | 1.33/11.6*/11.6* | 16 |
| 200 ops/s | inline | 14,187 | 14,187 | 16,636 | 1.26/2.07/3.63 | 2,834 |
| 200 ops/s | deferred | 84 | 14,753 | 17,144 | 1.24/2.00/3.92 | 2,944 |
| 1,500 ops/s | off | 92 | 92 | 184 | 1.02/9.6*/15.5* | 129 |
| 1,500 ops/s | inline | 15,563 | 15,563 | 28,940 | 1.08/2.48/6.02 | 19,529 |
| 1,500 ops/s | deferred | 87 | 15,629 | 26,539 | 0.97/1.89/5.47 | 19,227 |

(*off's window is the 80 ms of the heavy PUT itself: 16 and 129 samples.) Representative mix of all 12 kinds (362 MB), 4 concurrent PUTs: before 16.6 s wall / 33.9 s CPU; after inline 15.9 s / 31.1 s; deferred (2 workers) **0.52 s acknowledgement for the whole mix** (359 MB/s vs 482-496 MB/s with representations off), everything available after +20.5 s, 31.6 s CPU, RSS 605 MiB vs 658-694. A saturated foreground delays the background (the 5 s starvation bound then lets it run); `-representation-workers` and `-representation-queue` are the explicit resource limits.

## 6. Component publication

Findings (§2): packs are already used for components of objects >= 256 KiB (`packSink`, P1B/P1C) and were **not** a bottleneck (publication 1-6% of build; 9-15 files and 29-38 fsyncs per store). Small objects used `looseSink`. P4 routes them through the P1C segment sink exactly as an ordinary small PUT of the same size would (`segFor`): the whole representation shares one group commit (one write, one fsync); nothing is visible before `Flush`; the root is still published only after the proof. No new record format, no new floor: a store whose ordinary small PUTs use segments already runs at level 11; with segments disabled the loose path is unchanged. `syscalls.txt` (strace, store removed before each run, `off` rows identical before/after):

| object | PUT before -> after | fsync | openat | rename | mkdir |
|---|---|---:|---:|---:|---:|
| Merkle 80 KB, 352 components | 428 -> 17.6 ms | 969 -> 23 | 5,486 -> 98 | 357 -> 5 | 560 -> 16 |
| CAR 89 KB, 32 components | 45.7 -> 10.3 ms | 117 -> 23 | 508 -> 100 | 37 -> 5 | 78 -> 16 |
| tar 167 KB, 10 components | 18.0 -> 9.9 ms | 52 -> 23 | 223 -> 100 | 15 -> 5 | 35 -> 16 |
| SST snappy 2 x 3.3 MB (packs) | unchanged | 38 -> 38 | 170 -> 174 | 11 -> 11 | 17 -> 17 |
| Merkle 60k keys 5 MB (packs) | unchanged | 29 -> 29 | 213 -> 239 | 8 -> 8 | 17 -> 17 |

The 5-30x expectation for large file-heavy kinds does not apply to the current engine: those stores already hold 9-15 files for 4,000-42,000 components. The small-object gain is 2-24x wall time. A tiny Merkle shows the physical price: segment records add ~14 KB framing to a 162 KB component set (+9% apparent bytes) while removing 352 inodes.

## 7. Exact codecs

* **zstd / parquet-zstd (kind 7, 10)**: `FSE.prev` (the state inverse used by the exact sequence encoder, run in analysis and in the proof) was a per-field binary search over indirect loads, 16% of CPU. A dense inverse table is built after 128 lookups on a table (answers equal the search for every symbol/state, including out-of-range, `TestP4FSEInverseEqualsBinarySearch` on predefined, random and RLE tables). zstd 8,205 -> 6,213 ms PUT (-24%), proof 3,365 -> 2,352; parquet-zstd 8,463 -> 6,395 (-24%), deep verify 2,140 -> 1,067.
* **gzip (kind 6): no gain.** `repIRwindow.search` walks zlib-profile hash chains: over analysis, proof and the deep-verify replay of one 15 MB file: 41.3 M searches and 1.59 G chain steps (38 per search) at ~6 ns each, the same walk in all three. Hoisting profile/window fields into locals (so the compiler keeps them in registers) changed analysis time by +0.6%; reverted. The bound is the chain walk itself (data-dependent branch + dependent loads), not call overhead. Candidate lists feed the adaptive model, so they cannot be shortened or cached between analysis and proof without changing the representation bytes or making the proof depend on the analysis. Gzip stays at ~1 MB/s CPU-bound: the strongest negative result of P4 (§19).
* Exactness is unchanged everywhere: all serialised roots and decoded bytes are identical before/after (the lab-wire-fidelity tests and the extraction check, §17, pass).

## 8. Other algorithmic changes

* **Probe reads** (`manifestReaderAt`): a read that does not continue the previous one and is <= 64 KiB now fetches only the chunks it needs; the read that continues it opens the run-coalescing reader. Tar analysis 2,408 -> 912 ms, whole PUT 3,635 -> 2,021 ms (-44%), CPU 4,354 -> 2,940 (-32%); parquet-snappy -22% CPU, CLR -13%, DEX -9%, SST-snappy -10% (they probe footers/tables).
* **Floor reuse**: a freshly built root is published with the capability floor of the closure `reachable` just walked, instead of walking it again: Merkle (42,677 components) root phase 221 -> 3 ms.
* **Sorts/searches in the DEX/CLR index** (`repIX`): `sort.Slice` + reflection swapper -> `slices.SortFunc`; prefix-index `Get` binary searches open-coded. Orders are total (index tie-break) or irrelevant to results (equal identities are unreferenceable); `TestP4PrefixIndexMatchesReference` against a brute-force reference. Lab sources updated identically (§17).
* Parallel deep verification of independent roots (§12).

## 9. Memory and allocation

RSS, live heap and allocation per kind are unchanged within +-10% (table 2). The deferred pool bounds simultaneous builds by `-representation-workers`; queue memory is at most 256 manifests (chunk lists shared with the PUT). Scale: 185 MB tar (12,002 components): 1,760 ms inline / 404 ms ack deferred, RSS 627 MiB (includes the 185 MB input held by the harness), 13 files; 400,000-key Merkle (151,549 components, 28 MB): 3,469 ms inline / 144 ms ack, RSS 319 MiB, 9 files, `verify -deep` 2.5 s (`scale.txt`). Not tested: models larger than 28 MiB (no larger pinned model was fetched) and Merkle stores above 150k components per object. Cancellation, shutdown and leak behaviour: `TestP4DeferredShutdownAbandonsWorkWithoutLeaks` (goroutines return to baseline, abandoned work counted, store reopens clean).

## 10. Read path

Whole GETs always use CDC (`openServedReader`: structural whole reconstruction measured slower for every kind); `prefer` uses a representation only for ranges of SQLite, CAR and stored tar members. Per request that path re-read and re-hashed the representation object, re-read and re-parsed the root and (CAR) re-parsed the block table and re-sorted the derived index. A bounded (96 MiB) read cache of immutable, content-addressed parsed objects, parsed roots (validated by one `stat`: size, mtime, inode) and parsed CAR tables removes that. Every data chunk is still read and SHA-256 verified; deep verification never uses the cache; damaging a data component after the cache is warm falls back to CDC with the right bytes (`TestP4ReadCacheServesExactBytesAndStillVerifiesData`).

Range reads, µs/request (full table below): tar 4 KiB 2,495 -> 788 (CDC 191), 64 KiB 2,605 -> 661 (272); CAR 4 KiB 1,254 -> 184 (45), 64 KiB 1,779 -> 610 (82); SQLite 4 KiB 300 -> 215 (144). 1 MiB ranges: tar 5,055 -> 3,413 (2,962), CAR 4,925 -> 3,864 (2,514), SQLite unchanged. **The representation path remains slower than CDC for every supported kind and size in this additive configuration (1.15x-7x).** I did not switch `prefer` to route those ranges to CDC: that changes the mode's meaning and is a policy decision (§20). For kinds whose ranges are not representation-served the before/after columns differ only by cache state of the two runs; use the `off` column as the like-for-like CDC reference.

## 11. Verification and maintenance

`verify -deep` proves each live root by full reconstruction; roots are independent, so they now run on the P3 maintenance pool (`ZEROS3_MAINT_WORKERS`, 1 = serial); results are folded in sorted order and issue sets are identical to the serial run (`TestP4DeepVerifyParallelEqualsSerial`). Gain tracks the number of roots: CLR (4 roots) 731 -> 190 ms (3.8x), parquet-zstd 2.0x, DEX 1.8x, SST-snappy 1.6x; single-root kinds 0.9-1.3x (noise). Still 12-190x slower than `off` for gzip/zstd/Merkle/DEX/CLR because each root is replayed by the exact decoder; a cached proof cannot replace the required physical check, so no proof cache was added. `gc -apply` and `repack` on the shadow stores: 1-408 ms (Merkle's 42,677 components dominate), identical store bytes afterwards (no dead data), reopened `verify -deep` passes for all 14.

## 12. Storage economics (additive to CDC)

Table 4. Representation bytes are 1.5x (tar) to 7.6x (zstd) of the object; component count/size distribution: tar 17.5k unique components median 512 B; Merkle 42.7k at 75 B; parquet 3.2 KB; SST-snappy 3.7 KB. Neither gc nor repack changes the sizes (nothing is dead); P4 does not reduce additive bytes (the segment change slightly raises the footprint of tiny objects, §6). By the spec's own test, representations of 5-7.6x the object at 100x the CPU (gzip, zstd, SST-snappy 6x) are not a performance success for read or storage; their value is as a derivation capability for later authority work, not as an additive optimisation.

## 13. Crash and failure qualification

Deterministic: `TestP4DeferredCrashImagesAtEveryPhase` snapshots the store directory (what SIGKILL leaves) at 11 build phases x {pack-resident, segment-resident components} x {shadow, prefer} = 44 images; each holds the acknowledged object byte-exact, passes `verify -deep`, has no root or an exact one, and the abandoned build is redone on the image. Real process: `TestP4_RealKill_DeferredBuildsNeverHurtAcknowledgedData` (6 SIGKILL rounds of a deferred server under 6 concurrent clients, mixed SQLite/CAR/tar, `verify -deep` + `gc -apply` + re-verify after each kill, and a check that roots were really published). Concurrency: contended keys with 3 workers, put/delete races, overwrite while the older version's build waits (the root never describes another version), key removed from namespace and history while the build waits (build abandoned, no root), bounded queue under 8 distinct objects (3 built, 5 dropped, all objects intact), shutdown with running and queued builds (no goroutine growth, reopen clean), foreground yield and starvation bound. Existing suites (`TestRepCrashMatrix`, `TestRepLifecycleChain`, P1B/P1C crash matrices) pass unchanged. Not injected: power loss (SIGKILL keeps the page cache); the P1B ordering audit covers the shared publication path, and the new segment-sink use shares P1C's commit unchanged.

## 14. Root and dependency lifecycle

Small-object representations through restart, `gc`, `repack`, `compact`, hot->cold tier moves and history prune + `gc` (`TestP4SmallObjectComponentsShareTheSegmentAndSurviveMaintenance`) and the existing lifecycle chain, history, snapshot, fork, copy, sync and bundle tests (full suite) pass. Orphan roots left by a build whose manifest later disappears are the same state an inline build leaves when its commit fails; `gc` reports and reclaims them.

## 15. Validation

| gate | result |
|---|---|
| gofmt (root + testing-harnesses), go vet (root, harness module, z4 module) | pass |
| full root test suite (`go test .`, 543 s), incl. new P4 tests, kind matrix 1-12, nested, lifecycle, crash, bundle/sync | pass |
| full root race suite (`go test -race .`, 1,870 s) | pass |
| real SIGKILL rounds (deferred server), deterministic crash images (44), concurrency, shutdown/leak | pass |
| old/new binary compatibility (starting-main binary reads/verifies/gc-dry-runs a deferred store, and back) | pass |
| AWS SDK Go v2 + minio-go profile against a deferred `prefer` server | 262 passed, 0 failed (aws-cli absent: skipped) |
| reproducible build (`scripts/reproducible_build.sh`) | identical, sha256 62742838df66... |
| mechanical portability (re-extraction equals production, 3 modes) | 484/484, 586/586, 96/96 identical |
| `validate.sh static` (`harness/toc_check`) | fails with 31 TOC entries; **identical failure on the starting main** (pre-existing drift, not introduced here) |
| `validate.sh s3`, `sync` harness groups | not run separately; covered by the root suite and the conformance profile above |

## 16. Format and compatibility

No `FORMAT.json` level is introduced or raised by deferral, the segment sink, the cache or parallel verify. Root floors are computed exactly as before (`repFloorOf`, now also reused). `TestP4_PreP4BinaryUnderstandsDeferredStore` builds the starting-main binary and runs it against a store written by the new deferred server (small segment-resident and large pack-resident representations of SQLite, CAR, Merkle, tar): `verify -deep`, `doctor`, `gc` dry-run all succeed, whole and range GETs through the old server in `prefer` mode are byte-exact, and the new binary resumes it. AWS SDK for Go v2 and minio-go against a `-representations prefer -representation-build deferred` server: 262 profile cases passed, 0 failed (aws-cli not installed: skipped).

## 17. Mechanical portability

P4 promoted no new research algorithm. It edited two ported regions of `zeros3.go` (zstd `FSE.prev` + `initInv`; idxref `NewPrefixIndex`, `NewClassIndex`, `Get`). So that re-extraction still reproduces production, the identical edits were made in the lab sources (`z3/zstdz3/fse.go`, `z3/idxref/idxref.go`; lab `go test ./idxref ./zstdz3` pass) and the type-aware extractor (`z3/cmd/prodcodecport`, three modes) was re-run: `p4/cmd/portcheck` compares every extracted declaration with production after gofmt normalisation: **codec 484/484, managed 586/586, git 96/96 identical** (`results/portcheck.txt`; `scripts/portcheck.sh`). On the starting main the same check gives 1,165/1,165, so the check detects exactly the drift an unmirrored edit would create. The `declarations_sha256` recorded in `z4/testdata/a4-managed/extraction.json` is provenance of the earlier extraction and now differs by those edits. Hand-written in `zeros3.go` only (no lab counterpart): the scheduler, caches, probe reader, segment sink, parallel verify. No `PORTABILITY_BLOCKED` item.

## 18. Ablation (what each change contributes)

| change | tar | Merkle 80 KB | zstd | parquet-zstd | CLR | deep verify |
|---|---|---|---|---|---|---|
| deferral (ack latency) | 7x | 24x | 118x | 77x | 47x | - |
| segment sink for small objects | - | 24x wall, 42x fewer fsyncs | - | - | - | - |
| probe-sized reads | -44% wall | - | - | - | -13% CPU | - |
| FSE dense inverse | - | - | -24% | -24% | - | parquet-zstd 2.0x |
| floor reuse | - | -16% | - | - | - | - |
| parallel deep verify | - | - | - | - | - | CLR 3.8x |
| read cache | `prefer` 4 KiB 3.2x | - | - | - | - | - |

## 19. Rejected or negative

* Persistent build queue (cost/benefit above). * Caching analysis candidates for the proof, or skipping the chunk re-hash in the proof for chunks just written: makes the proof depend on the analysis / on unverified bytes. * A proof cache for `verify -deep`: would replace the required physical verification. * Gzip search micro-rewrite: +0.6%, reverted. * Routing `prefer` ranges to CDC: policy change, left to the owner. * Moving objects >= 256 KiB to segments: they are already in packs and are not publication-bound. * Overlapping lazy-evaluation of the lookahead search in the gzip predictor: it feeds the model context and would change the serialized representation.

## 20. Remaining bottlenecks

1. Gzip exact analysis+proof ~1 MB/s (1.6 G chain steps per 15 MB); next step is token-level parallel analysis (candidate lists are independent per known token, the model coder stays sequential) or a shared exact-DEFLATE engine (the register's XC-2), changing no bytes. 2. zstd/parquet-zstd match model: the remaining 50% is cache-missing `head`/`prev` traffic and `lce`. 3. SHA-256 passes per byte (tar: ingest 3, probe/analysis ~3, proof 2): chunk-granular verification dominates tar/SQLite/model/CAR. 4. Single-root `verify -deep` cannot use the pool. 5. `prefer` ranges are still 1.15-7x slower than CDC (tar, CAR, SQLite); needs a range-aware planner or a policy decision. 6. DEX/CLR index construction (sort + `bytes.Compare`, ~30%). 7. Additive storage 1.5-7.6x. 8. Deferred workers are not pinned to a core budget: contention is bounded by worker count only.

## 21. Recommended next batch

P5 as planned (B1/B2 delta, similarity, authority planning) can now treat representation construction as a deferred, cloneable, segment-published job; it should add a cheap estimator ahead of `repBuild` so uneconomical candidates (5-7x bytes at 100x CPU) are not built. The shared exact-DEFLATE/zstd engine belongs to P7/P8 with item 1 above as its first target. A decision on routing `prefer` ranges is needed before representations are measured against CDC as authoritative.

## 22. Final PR and merge state

PR #110 (non-draft) from `z4/p4-representation-performance` into `main`, merged with the GitHub `merge` method only (no squash, no rebase); the 9 development commits are preserved on main. The merge SHA, the two parents, the merge-tree equality check and the post-merge description re-read (no attribution footer) are recorded in the PR and in the hand-off message, not in this file, because the merge commit does not exist yet when this file is committed.

---

## Appendix: measured tables

Table numbering: 1 PUT latency, 2 CPU/RSS/alloc, 3 build phases, 4 components and storage, 5 maintenance, 6 range reads, 7-8 mix. "before" = starting main; "after" = branch; "deferred ack" is client-visible completion, "deferred available" includes the wait for the last root.

### PUT latency (ms), serial, whole object set per kind

| kind | off | inline before | inline after | deferred ack | deferred available | ack speedup vs before |
|---|---:|---:|---:|---:|---:|---:|
| k01_tar | 428 | 3,635 | 2,021 | 524 | 1,813 | 7x |
| k02_car | 108 | 362 | 360 | 117 | 342 | 3x |
| k03_sqlite | 329 | 768 | 817 | 328 | 668 | 2x |
| k04_git | 37 | 693 | 661 | 55 | 742 | 13x |
| k05_merkle | 42 | 1,024 | 979 | 39 | 1,033 | 26x |
| k06_gzip | 99 | 15,327 | 15,083 | 89 | 15,265 | 173x |
| k07_zstd | 80 | 8,205 | 6,213 | 70 | 6,588 | 118x |
| k08_model | 289 | 1,278 | 1,171 | 308 | 1,036 | 4x |
| k09_sst_snappy | 50 | 1,056 | 983 | 58 | 903 | 18x |
| k09_sst_zstd | 19 | 47 | 30 | 16 | 29 | 3x |
| k10_parquet_snappy | 80 | 2,053 | 1,644 | 82 | 1,662 | 25x |
| k10_parquet_zstd | 100 | 8,463 | 6,395 | 109 | 6,408 | 77x |
| k11_dex | 123 | 4,376 | 4,034 | 117 | 3,732 | 37x |
| k12_clr | 35 | 1,656 | 1,436 | 35 | 1,479 | 47x |

### CPU ms (user+sys, whole process incl. background), peak RSS MiB, allocation MiB

| kind | MB | off | inline before | inline after | deferred | RSS b/a/d | alloc MiB b/a |
|---|---:|---:|---:|---:|---:|---:|---:|
| k01_tar | 187.0 | 1,183 | 4,354 | 2,940 | 2,928 | 428/480/467 | 556/615 |
| k02_car | 19.2 | 130 | 414 | 409 | 420 | 103/105/115 | 130/150 |
| k03_sqlite | 63.4 | 420 | 924 | 977 | 957 | 161/163/161 | 197/198 |
| k04_git | 4.1 | 34 | 902 | 861 | 954 | 39/40/39 | 1026/1023 |
| k05_merkle | 5.0 | 39 | 1,108 | 1,046 | 1,117 | 72/75/77 | 342/363 |
| k06_gzip | 14.9 | 108 | 15,395 | 15,121 | 15,205 | 87/91/92 | 441/438 |
| k07_zstd | 10.6 | 82 | 8,432 | 6,457 | 6,771 | 126/129/129 | 710/798 |
| k08_model | 56.3 | 373 | 1,537 | 1,423 | 1,413 | 176/171/173 | 406/403 |
| k09_sst_snappy | 6.6 | 52 | 1,196 | 1,076 | 1,032 | 72/68/67 | 721/728 |
| k09_sst_zstd | 1.5 | 16 | 39 | 31 | 30 | 24/24/24 | 19/20 |
| k10_parquet_snappy | 12.4 | 92 | 2,363 | 1,849 | 1,893 | 75/80/85 | 1298/1308 |
| k10_parquet_zstd | 17.3 | 111 | 9,079 | 6,944 | 7,107 | 109/111/112 | 4906/4975 |
| k11_dex | 18.6 | 134 | 4,449 | 4,037 | 3,833 | 170/174/170 | 849/864 |
| k12_clr | 2.8 | 23 | 1,707 | 1,478 | 1,541 | 46/49/46 | 336/334 |

### Build phases (ms, summed over objects): before / after

| kind | analyze b | a | publish b | a | proof b | a | reach+root b | a |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| k01_tar | 2,408 | 912 | 92 | 83 | 604 | 583 | 31 | 29 |
| k02_car | 131 | 149 | 35 | 26 | 63 | 65 | 12 | 10 |
| k03_sqlite | 210 | 229 | 41 | 42 | 176 | 207 | 15 | 16 |
| k04_git | 566 | 537 | 14 | 13 | 55 | 55 | 12 | 10 |
| k05_merkle | 234 | 247 | 62 | 52 | 229 | 213 | 457 | 430 |
| k06_gzip | 8,022 | 7,823 | 36 | 37 | 7,158 | 7,119 | 13 | 10 |
| k07_zstd | 4,718 | 3,750 | 24 | 27 | 3,365 | 2,352 | 14 | 11 |
| k08_model | 527 | 496 | 42 | 40 | 385 | 346 | 9 | 7 |
| k09_sst_snappy | 641 | 572 | 33 | 36 | 282 | 273 | 35 | 34 |
| k09_sst_zstd | 13 | 11 | 0 | 0 | 0 | 0 | 0 | 0 |
| k10_parquet_snappy | 1,334 | 973 | 13 | 13 | 607 | 574 | 5 | 6 |
| k10_parquet_zstd | 6,266 | 4,623 | 22 | 26 | 2,042 | 1,623 | 14 | 17 |
| k11_dex | 2,420 | 2,277 | 24 | 26 | 1,791 | 1,590 | 7 | 12 |
| k12_clr | 978 | 847 | 15 | 12 | 619 | 543 | 8 | 7 |

### Component economics and storage (shadow, inline after)

| kind | roots | components | unique | median B | unique MB | files | off MB | shadow MB | amp | after gc MB | after repack MB |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| k01_tar | 3 | 21117 | 17534 | 512 | 171.6 | 15 | 179.4 | 275.0 | 1.53x | 275.0 | 275.0 |
| k02_car | 3 | 8106 | 7056 | 2,724 | 18.8 | 13 | 19.0 | 38.4 | 2.02x | 38.4 | 38.4 |
| k03_sqlite | 2 | 15722 | 8786 | 4,096 | 35.5 | 12 | 58.5 | 94.8 | 1.62x | 94.8 | 94.8 |
| k04_git | 3 | 10397 | 3810 | 163 | 2.6 | 15 | 2.8 | 5.7 | 2.05x | 5.7 | 5.7 |
| k05_merkle | 1 | 42677 | 42677 | 75 | 4.9 | 9 | 5.0 | 14.0 | 2.81x | 14.0 | 14.0 |
| k06_gzip | 1 | 8701 | 8701 | 512 | 63.4 | 9 | 14.9 | 77.6 | 5.20x | 77.6 | 77.6 |
| k07_zstd | 1 | 7812 | 7812 | 512 | 71.1 | 10 | 10.6 | 81.0 | 7.61x | 81.0 | 81.0 |
| k08_model | 2 | 434 | 290 | 256 | 56.3 | 12 | 56.4 | 112.9 | 2.00x | 112.9 | 112.9 |
| k09_sst_snappy | 2 | 9020 | 6644 | 3,683 | 32.7 | 12 | 6.6 | 39.9 | 6.06x | 39.9 | 39.9 |
| k09_sst_zstd | 0 | 0 | 0 | 0 | 0.0 | 7 | 1.5 | 1.5 | 1.00x | 1.5 | 1.5 |
| k10_parquet_snappy | 1 | 4668 | 4668 | 3,192 | 18.9 | 9 | 12.4 | 31.7 | 2.56x | 31.7 | 31.7 |
| k10_parquet_zstd | 2 | 9443 | 5497 | 3,224 | 24.4 | 12 | 12.3 | 37.2 | 3.04x | 37.2 | 37.2 |
| k11_dex | 2 | 4246 | 2234 | 3,701 | 13.1 | 12 | 17.7 | 30.9 | 1.75x | 30.9 | 30.9 |
| k12_clr | 4 | 1089 | 575 | 2,318 | 2.6 | 14 | 2.8 | 5.5 | 1.95x | 5.5 | 5.5 |

### Maintenance (ms): verify -deep and offline gc / repack on the shadow store

| kind | deep off | deep before | deep after | gain | gc -apply | repack | deep after gc+repack | ok |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| k01_tar | 509 | 1,133 | 786 | 1.4x | 87 | 84 | 827 | True |
| k02_car | 49 | 179 | 150 | 1.2x | 38 | 34 | 169 | True |
| k03_sqlite | 159 | 471 | 361 | 1.3x | 63 | 54 | 366 | True |
| k04_git | 19 | 114 | 76 | 1.5x | 46 | 42 | 88 | True |
| k05_merkle | 15 | 574 | 618 | 0.9x | 378 | 408 | 579 | True |
| k06_gzip | 37 | 7,771 | 6,912 | 1.1x | 34 | 38 | 6,768 | True |
| k07_zstd | 32 | 3,428 | 2,670 | 1.3x | 35 | 35 | 2,621 | True |
| k08_model | 162 | 579 | 436 | 1.3x | 11 | 8 | 417 | True |
| k09_sst_snappy | 22 | 410 | 260 | 1.6x | 56 | 42 | 253 | True |
| k09_sst_zstd | 6 | 10 | 7 | 1.5x | 3 | 1 | 6 | True |
| k10_parquet_snappy | 40 | 739 | 787 | 0.9x | 26 | 23 | 672 | True |
| k10_parquet_zstd | 47 | 2,140 | 1,067 | 2.0x | 38 | 38 | 1,017 | True |
| k11_dex | 48 | 1,675 | 928 | 1.8x | 14 | 12 | 1,026 | True |
| k12_clr | 16 | 731 | 190 | 3.8x | 13 | 4 | 155 | True |

### Range reads µs/request: CDC (off) / prefer before / prefer after (reads that fall back are counted)

| kind | 4K off | b | a | 64K off | b | a | 1M off | b | a | rep reads/fallbacks |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| k01_tar | 191 | 2,495 | 788 | 272 | 2,605 | 661 | 2,962 | 5,055 | 3,413 | 0/0 |
| k02_car | 45 | 1,254 | 184 | 82 | 1,779 | 610 | 2,514 | 4,925 | 3,864 | 0/0 |
| k03_sqlite | 144 | 300 | 215 | 224 | 416 | 290 | 2,901 | 2,955 | 2,892 | 0/0 |
| k04_git | 36 | 27 | 10 | 39 | 70 | 34 | 0 | 0 | 0 | 0/0 |
| k05_merkle | 117 | 82 | 20 | 144 | 119 | 47 | 1,796 | 1,588 | 1,391 | 0/0 |
| k06_gzip | 124 | 187 | 137 | 243 | 243 | 190 | 2,661 | 2,264 | 2,336 | 0/0 |
| k07_zstd | 135 | 237 | 121 | 259 | 297 | 162 | 2,242 | 2,316 | 1,664 | 0/0 |
| k08_model | 131 | 243 | 194 | 213 | 292 | 275 | 2,651 | 2,310 | 2,514 | 0/0 |
| k09_sst_snappy | 43 | 1,797 | 24 | 34 | 1,776 | 33 | 906 | 2,454 | 709 | 0/0 |
| k09_sst_zstd | 31 | 23 | 27 | 70 | 56 | 73 | 0 | 0 | 0 | 0/0 |
| k10_parquet_snappy | 115 | 155 | 92 | 207 | 184 | 109 | 2,514 | 2,430 | 1,852 | 0/0 |
| k10_parquet_zstd | 93 | 127 | 43 | 121 | 141 | 74 | 2,115 | 1,688 | 1,445 | 0/0 |
| k11_dex | 124 | 114 | 54 | 196 | 131 | 63 | 2,371 | 1,130 | 967 | 0/0 |
| k12_clr | 17 | 21 | 9 | 28 | 57 | 29 | 0 | 0 | 0 | 0/0 |

### Representative mix, 4 concurrent PUTs

| code | mode | PUT wall ms | MB/s | then wait ms | CPU ms | RSS MiB | store MB | built | verify ok |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| before | off | 385 | 481.7 | 0 | 1,266 | 252 | 193 | 0 | True |
| before | shadow | 16,618 | 11.2 | 0 | 33,909 | 658 | 387 | 12 | True |
| after | off | 374 | 496.4 | 0 | 1,203 | 253 | 193 | 0 | True |
| after | shadow | 15,875 | 11.7 | 0 | 31,105 | 694 | 387 | 12 | True |
| deferred | shadow | 516 | 359.3 | 20,485 | 31,647 | 605 | 387 | 12 | True |

### Representative mix, serial

| code | mode | PUT wall ms | MB/s | then wait ms | CPU ms | RSS MiB | store MB | built | verify ok |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| before | shadow | 32,808 | 5.7 | 0 | 33,604 | 548 | 387 | 12 | True |
| after | shadow | 30,027 | 6.2 | 0 | 30,835 | 591 | 387 | 12 | True |

