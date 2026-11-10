# Z4-P3: maintenance, reachability, verification, GC, repack and metadata scaling

Verdict: **P3_PRODUCTION_READY**
Base `origin/main` `da88b4d708942a55b31fcabe95e73fd5dd3f4df7` (P2 PR #108, `merged=true`, merge method `merge`). Branch `z4/p3-maintenance-performance`. Production diff: `zeros3.go` only (standard library only; `go.mod`, `deps-proof.txt`, every store/journal/pack/wire format unchanged; no format-level change). Evidence: [`../z4/p3/`](../z4/p3/README.md). Method: fixture stores built **through the real server by the base binary**, every command a fresh process (`p0bench cli`: wall, user+sys CPU, peak RSS), read-only commands on the fixture, mutating commands on a `cp -a` clone, base and candidate binaries run back to back (interleaved for the 1 M and churn matrices), 4 vCPU Firecracker VM, ext4 on virtio, Go 1.27. No historical figure is used as a baseline: the P0/P1C numbers appear only as context.

## 0. First action: the stale P2 line
`Z4_P2_READ_PERFORMANCE.md` section 14 said "filled in after the PR". Verified through GitHub before editing: PR #108 `merged=true`, merge method `merge`, merge SHA `da88b4d`, two parents (`4059cd9` P1C main, `7ed16d7` P2 tip), 16 commits preserved, merge tree identical to the branch tree (`git diff 7ed16d7 da88b4d` empty). Corrected in the first commit of this branch (3d2268a); P2's conclusions and evidence are untouched.

## 1. Summary
| | base (`da88b4d`) | P3 | |
|---|---:|---:|---|
| `stats`, 1 M objects | 31.9 s, 2,008 MiB | **10.0 s**, 1,644 MiB | **3.2x**, -18% RSS |
| `verify`, 1 M | 20.3 s, 2,046 MiB | **8.2 s**, 1,572 MiB | 2.5x |
| `verify -deep`, 1 M (5.0 GB store) | 53.2 s | **21.2 s** (15.6 s in the first run) | 2.5-3.2x |
| `gc -apply` after prune, 1 M | 47.1 s | **10.7 s** | 4.4x |
| `repack -apply` after prune, 1 M | 19.8 s | **6.3 s** | 3.2x |
| `stats` / `verify -deep`, 100 k objects | 2.53 s / 4.22 s | **0.71 s / 1.18 s** | 3.5x / 3.6x |
| store open, 1 M objects (in-process) | 4.4 s, 2.5 GB allocated | **2.5 s**, 1.35 GB | **1.8x**, -47% allocated |
| repack that rewrites 90% of 520 MB (10% random dead) | 7.8 s, 9.3 CPU-s | **3.8 s**, 8.1 CPU-s | 2.1x |
| 4 KiB-1 MiB PUT, GET, HEAD, range (A/B, 3-5 rounds + A/A control) | | 0.96-1.05 | within the A/A noise (0.96-1.16) |

* **A real data-loss defect was found and fixed**: a read-only opener (`stats`, `verify`, `doctor`) cut the journal back to the end of its own replay, destroying any frame a live server appended in between; the server then wrote past a hole and the store no longer opened (`journal: corrupt ... bad magic`). Deterministic reproduction `TestP3_SharedOpenerNeverTruncatesALiveJournal` failed on `da88b4d` (section 7).
* All gains are on 1-8 workers of one process; **CPU time falls too** (stats 40 -> 19 CPU-s at 1 M), because the work removed (a second manifest read, a second journal copy, hex strings, 20% collector time) is not parallelism. Peak RSS at 1 M is **lower** than base for read-only scans; it is higher for `verify -deep` (2.7 vs 2.4 GiB) and for small stores (e.g. 99 vs 38 MiB), where workers' buffers dominate.
* Negative results: loose-chunk `compact -apply` is I/O-bound and does not move (4.25 -> 4.19 s, more CPU); `gc` still reclaims nothing from partly-dead P1C packs (by design; `repack` does, section 5); the deep-verification SHA-256 passes on multi-chunk objects remain at three (section 4); startup is still dominated by journal replay (about 85% of open), so the "locator checkpoint" P1C recommended would address the smaller part (section 6).

## 2. Maintenance call graph and repeated work (on `da88b4d`, verified against the code)

```
OpenStore / OpenStoreShared            replayJournal(1 pread x2 per frame, 3 allocs/frame, ALL records retained) -> applyRecord (encoding/json per frame)
  -> reloadPacks -> loadPackState      per pack: packRecordHint + statPackFile (serial) ; buildLocIndex (one sort per bucket, serial)
  -> openSegments / loadSegmentsReadOnly (bounded by one segment)
computeReachability(deep)              replayJournal AGAIN (frames only used for a count and an error)
  per root (serial): loadManifestBytes -> casRead (open+pread+close per record, SHA-256) -> SHA-256 AGAIN -> json.Unmarshal (hex string per chunk)
  chunk pass (serial, random map order): packLookup | stat ; deep: casRead (open+pread+close, SHA-256)
computeStats                           reads EVERY current manifest (readVerifiedManifest) -> then computeReachability reads them AGAIN -> scanChunkFiles/scanManifestFiles (WalkDir+lstat) -> packUsages (hex-encode every locator digest, map lookup)
Verify(deep)                           computeReachability(deep) -> verifyPacks (pread per record header, serial) -> per manifest: readManifestSHA AGAIN (+JSON) and casRead EVERY chunk AGAIN (chunk SHA + object SHA) -> scan*Files
gcCollect                              computeReachability -> WalkDir chunks+manifests -> live() = COPY of the whole live map (2 extra hex keys per manifest) x2 -> packUsages -> serial os.Remove -> replacePacks(dead packs)
repack                                 computeReachability -> live() copy x2 -> packUsages -> replacePacksTo: per live chunk casReadExcluding (serial), stagePack (serial read+SHA+DEFLATE+write), publishPack (verifyPackFile: serial decode+SHA), final gate casReadExcluding per chunk (serial)
compact                                computeReachability -> live() -> WalkDir chunks -> localityOrder: computeReachabilityObserved AGAIN (sorted, hex chunk lists) -> stagePack (serial) per batch
versions prune                         OpenStore -> history planner -> one journal frame (no chunk I/O) ; the reclaim is the later gc/repack
tier move / rebalance                  computeReachability(Observed) -> planTierMove -> packs copied/verified (replacePacksTo for rebalance)
doctor                                 Verify
```
Cost classes: **CPU** (JSON decode, SHA-256, DEFLATE, hex encode/decode, map hashing, GC), **metadata I/O** (one `open`+`pread`+`close` per manifest and per chunk read, `lstat` per loose file), **bulk I/O** (pack bytes), **synchronisation** (none before P3 -- every scan used one core; only the journal fsync and directory fsyncs of publication synchronise).


## 3. Baseline on the P2 engine (this machine, `da88b4d`)
Selected commands, median of 3 (full table: [`TABLES.md`](../z4/p3/results/TABLES.md), raw `results/maint_base.jsonl`). The fixtures are defined in `scripts/mkfix.sh`: `s1k/s10k/s100k/s1m` small objects in P1C segments (4-64 KiB, two revisions, 30% deleted; `s1m` is 1,000,000 x 4 KiB), `loose10k` the same on the loose layout, `mix` 600 objects 4 KiB-16 MiB with duplicates and revisions, `hist` 20 keys x 256 growing revisions.

| fixture | stats | verify | verify -deep | gc dry-run | repack dry-run | gc -apply | repack -apply |
|---|---:|---:|---:|---:|---:|---:|---:|
| s10k (20 k manifests) | 0.39 | 0.31 | 1.54 | 0.35 | 0.34 | 0.34 | 0.35 |
| s100k (170 k manifests, 130 k chunks) | 2.53 | 1.55 | 4.22 | 1.74 | 1.71 | 1.76 | 2.03 |
| loose10k (42 k files) | 0.67 | 0.55 | 1.96 | 0.53 | 0.36 | 0.60 | 0.42 |
| mix (1.2 GB) | 0.06 | 0.07 | 3.50 | 0.06 | 0.06 | 0.11 | 0.12 |
| hist (1.7 GB, 5,120 versions) | 0.35 | 0.48 | 13.72 | 0.32 | 0.35 | 0.38 | 0.38 |
| s1m | 31.9 | 20.3 | 53.2 | 24.0 | 23.2 | 47.1 (after prune) | 19.8 (after prune) |

CPU utilisation of the base commands was 1.2 cores (one worker plus the collector). Profile of base `stats` on s100k ([`prof/`](../z4/p3/results/prof/)): of all CPU samples, reachability 43%, `stats`' own manifest pass (`computeStats` -> `readVerifiedManifest`) 20%, JSON decode 18%, system calls 17%, collector 15%. Base `Open` at 100 k: 0.42 s, 250 MB and 1.44 M allocations per open; at 1 M: 3.9 s, 1.29 GB, 11.8 M allocations (journal replay 76% of CPU, 62% of that JSON decoding).

## 4. What changed (all in `zeros3.go`)
**Reachability engine** (section 12a/12b; model and validity rules in [`REACHABILITY_MODEL.md`](../z4/p3/REACHABILITY_MODEL.md)). The one scan behind `stats`, `verify`, `doctor`, `gc`, `repack`, `compact` is now enumerate -> analyse (parallel) -> merge (in the original order) -> chunk pass (parallel). Each distinct manifest is read **from storage** once and decoded with the P2 canonical-chunk scanner into 32-byte digests (falling back to `encoding/json` for anything non-canonical, so the judgement is unchanged); the live set is a `digestTable` (open addressing, 44 B per digest, no pointers) and a `liveSet`, not hex-string maps; the work is done in blocks of 32,768 manifests, a verdict is freed after its last root, and workers keep their pack file open (`packHandles`) and visit records in physical order. `stats` uses one pass (it read every manifest twice). `ZEROS3_MAINT_WORKERS` (default `min(GOMAXPROCS, 8)`) sets the pool; 1 is the serial scan.

**Deep verification.** The chunk pass and the whole-object pass each run on the worker pool; the whole-object pass is skipped for single-chunk objects whose `object_sha256` is the chunk digest (the chunk pass has just hashed those exact bytes); pack record headers are checked from sequential 256 KiB windows instead of one `pread` per record, packs in parallel. Meaning unchanged: a damaged copy, header or whole object is still found (section 7). Per chunk reference the SHA-256 passes are: single-chunk object 1 (was 3), multi-chunk object 3 (unchanged: chunk pass, chunk re-verify, object hash).

**Repack / compact.** Reading, verifying and DEFLATE-compressing the live records of a replacement pack run on a bounded pipeline (two windows of <= 64 records / 8 MiB in flight, encoders pooled), appended in the original order: serial and pipelined repack write **byte-identical packs** (`TestP3_RepackPipelineWritesIdenticalPacks`). The "does another copy exist" probe and the last gate before any pack is removed (every carried chunk must read back, hashed, from outside the doomed packs) are concurrent but unchanged in meaning. Publication (`verifyPackExact` / `verifyPackFile`, fsyncs, locator swap, old-pack retirement) is untouched.

**Startup.** The journal is replayed once, streaming, with large reads (`replayJournalFn`; the old replay did two `pread`s and three allocations per frame and kept every payload), and applied while it is read; the reachability journal check keeps nothing; the dominant frames (`put-object-root-v2`, `delete-object-root-v2`) have a strict flat-JSON decoder that falls back to `encoding/json` on anything it does not recognise (`TestP3_FastJournalDecodeMatchesJSON`: 24,000 generated and mutated frames, whatever it accepts equals `encoding/json`); bucket/content-type strings are shared; the collector runs at GOGC 400 while the namespace is built; pack footers/indexes load concurrently and the locator buckets sort in parallel. Journal format, locator layout and the pack format are unchanged.

## 5. Results
### 5.1 Commands on the small and mixed fixtures (wall s, base -> P3; the `-apply` commands run on clones)
| fixture | stats | verify | verify -deep | gc dry-run | repack dry-run | compact dry-run | gc -apply | repack -apply | compact -apply |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| s10k | 0.39 -> 0.15 (2.6x) | 0.31 -> 0.14 (2.2x) | 1.54 -> 0.41 (3.8x) | 0.35 -> 0.13 (2.7x) | 0.34 -> 0.13 (2.7x) | 0.33 -> 0.12 (2.7x) | 0.34 -> 0.14 (2.4x) | 0.35 -> 0.15 (2.4x) | 0.33 -> 0.13 (2.5x) |
| s100k | 2.53 -> 0.71 (3.5x) | 1.55 -> 0.66 (2.4x) | 4.22 -> 1.18 (3.6x) | 1.74 -> 0.64 (2.7x) | 1.71 -> 0.72 (2.4x) | 1.59 -> 0.68 (2.4x) | 1.76 -> 0.77 (2.3x) | 2.03 -> 0.73 (2.8x) | 1.71 -> 0.69 (2.5x) |
| loose10k | 0.67 -> 0.38 (1.7x) | 0.55 -> 0.35 (1.6x) | 1.96 -> 0.63 (3.1x) | 0.53 -> 0.36 (1.5x) | 0.36 -> 0.17 (2.1x) | 0.75 -> 0.45 (1.7x) | 0.60 -> 0.39 (1.5x) | 0.42 -> 0.18 (2.3x) | 4.25 -> 4.19 (1.0x) |
| mix | 0.06 -> 0.05 (1.3x) | 0.07 -> 0.05 (1.5x) | 3.50 -> 1.65 (2.1x) | 0.06 -> 0.04 (1.8x) | 0.06 -> 0.03 (1.9x) | 0.06 -> 0.03 (2.0x) | 0.11 -> 0.08 (1.5x) | 0.12 -> 0.07 (1.7x) | 0.10 -> 0.04 (2.8x) |
| hist | 0.35 -> 0.14 (2.6x) | 0.48 -> 0.19 (2.5x) | 13.72 -> 4.40 (3.1x) | 0.32 -> 0.13 (2.5x) | 0.35 -> 0.12 (2.8x) | 0.45 -> 0.19 (2.4x) | 0.38 -> 0.15 (2.4x) | 0.38 -> 0.15 (2.6x) | 0.47 -> 0.20 (2.4x) |

Full per-command table with CPU and RSS: [`TABLES.md`](../z4/p3/results/TABLES.md).

### 5.2 One million objects (1,000,000 x 4 KiB written, 30% then deleted: 700,000 current, 300,000 in history; 5.0 GB, 141 packs)
| command (1,000,000 objects) | base wall s | P3 wall s | speed-up | base CPU s | P3 CPU s | base RSS MiB | P3 RSS MiB |
|---|---:|---:|---:|---:|---:|---:|---:|
| stats | 31.9 | 10.0 | 3.2x | 40 | 19 | 2008 | 1644 |
| verify | 20.3 | 8.2 | 2.5x | 26 | 17 | 2046 | 1572 |
| gc (dry-run) | 24.0 | 8.8 | 2.7x | 30 | 17 | 1966 | 1538 |
| repack (dry-run) | 23.2 | 9.6 | 2.4x | 29 | 19 | 1950 | 1550 |
| verify -deep | 53.2 | 21.2 | 2.5x | 70 | 36 | 2391 | 2745 |
| versions prune -keep-last 0 -apply | 8.6 | 4.6 | 1.9x | 10 | 6 | 1375 | 1161 |
| gc -apply (after prune) | 47.1 | 10.7 | 4.4x | 39 | 19 | 1505 | 987 |
| repack -apply (after prune and gc) | 19.8 | 6.3 | 3.2x | 24 | 12 | 1794 | 1033 |
| verify -deep (after maintenance) | 41.0 | 9.9 | 4.1x | 49 | 21 | 1775 | 2164 |

(`vdeep`: the first run measured 49.5 s -> 15.6 s; `results/maint_1m_first_run.txt`.) The wall time of `gc`/`repack` dry-runs and of `stats` is now mostly the store open (2.5 s) plus a 5-6 s scan on four cores; there is no superlinear term: 100 k -> 1 M (10x the objects and the journal) costs 14x (`stats`, P3) and 12.6x (base) -- the extra is memory and cache pressure of a 1.3 GB namespace, not a superlinear algorithm (in-process, `stats` 0.49 s -> 6.95 s is 14x).

### 5.3 Parallelism (workers 1 / 2 / 4 / 8, in-process, 4 vCPU)
| | 1 | 2 | 4 | 8 |
|---|---:|---:|---:|---:|
| reachability s100k (ms) | 850 | 567 | 390 | 420 |
| `verify -deep` s100k (ms) | 1,622 | 992 | 808 | 820 |
| `verify -deep` hist 1.7 GB (s) | 13.1 | 6.8 | 4.3 | 3.9 |
Speed-up 1 -> 4 workers: 2.2x / 2.0x / 3.0x; 8 workers oversubscribe four CPUs. The default (`min(GOMAXPROCS, 8)`) is kept; deep verification of mixed data is SHA-256-bound (`mix`: 2.1x, CPU unchanged) and does not scale beyond the cores. Foreground load: there is no throttling; a `verify` against a live server uses up to all cores unless `ZEROS3_MAINT_WORKERS` is lowered (not measured under a concurrent request load).

### 5.4 Version history (4 keys x N versions of 256 KiB, constant size, `TestP3_HistoryFixture`)
| versions per key (4 keys) | store | stats s | verify s | verify -deep s |
|---:|---:|---:|---:|---:|
| 1 | 1 MB | 0.01 -> 0.01 | 0.01 -> 0.01 | 0.01 -> 0.01 |
| 16 | 17 MB | 0.00 -> 0.01 | 0.00 -> 0.01 | 0.05 -> 0.03 |
| 256 | 270 MB | 0.03 -> 0.02 | 0.03 -> 0.02 | 0.75 -> 0.32 |
| 1024 | 1082 MB | 0.10 -> 0.05 | 0.09 -> 0.05 | 2.61 -> 1.04 |

Linear in the number of versions before and after (no superlinear term, no memo-dependent behaviour); the deep-verify gain at 1,024 versions is 2.5x. In this fixture almost no chunk is shared between versions (a 2% in-place edit touches every chunk), so the cross-version memo opportunity could not be measured; the `hist` fixture (shared chunks) gains 3.1x from parallelism alone.

### 5.5 GC and repack on fragmented P1C packs (100 k x 4 KiB, 517 MB, 14-15 packs)
Dead data is created by deleting a fraction of the objects (random, or the oldest ones = "range") and pruning all history; each arm runs on a fresh clone, base and P3 interleaved. Reclaimed bytes are **identical** between base and P3 in every cell (the same packs are written). Write amplification = bytes written by the command / bytes reclaimed.
| dead data | deleted | gc: reclaimed MB, s | repack (50% rule): reclaimed MB, write amp., s | repack -max-live-percent 100: reclaimed MB, write amp., s | gc then repack: s |
|---:|---|---|---|---|---|
| 10% | random | 0, 1.8 -> 0.5 | 0, -, 1.6 -> 0.6 | 70, 5.9, 7.8 -> 3.8 | 3.4 -> 1.0 |
| 10% | range | 34, 1.8 -> 0.7 | 34, 0, 1.8 -> 0.6 | 49, 0.4, 1.9 -> 0.7 | 3.4 -> 1.1 |
| 30% | random | 0, 1.5 -> 0.6 | 0, -, 1.5 -> 0.5 | 162, 2.0, 6.2 -> 2.9 | 2.8 -> 1.0 |
| 30% | range | 137, 1.4 -> 0.5 | 137, 0, 1.5 -> 0.5 | 146, 0.2, 1.7 -> 0.8 | 2.7 -> 1.2 |
| 50% | random | 0, 1.3 -> 0.7 | 165, 0.9, 3.2 -> 2.1 | 253, 0.9, 4.3 -> 2.5 | 4.7 -> 2.4 |
| 50% | range | 240, 1.3 -> 0.6 | 240, 0, 1.3 -> 0.6 | 243, 0.1, 1.7 -> 0.8 | 2.5 -> 1.0 |
| 75% | random | 0, 1.2 -> 0.6 | 368, 0.3, 2.7 -> 1.4 | 368, 0.3, 2.6 -> 1.7 | 3.9 -> 2.3 |
| 75% | range | 343, 1.2 -> 0.6 | 362, 0.0, 1.4 -> 0.7 | 362, 0.0, 1.4 -> 0.7 | 2.4 -> 1.4 |
| 90% | random | 0, 1.1 -> 0.6 | 436, 0.1, 1.8 -> 1.0 | 436, 0.1, 1.7 -> 0.9 | 2.8 -> 1.5 |
| 90% | range | 412, 1.1 -> 0.6 | 435, 0.0, 1.3 -> 0.7 | 435, 0.0, 1.2 -> 0.7 | 2.3 -> 1.2 |

Findings.
* **`gc` alone reclaims nothing from randomly distributed deletions at any fraction** (all 14 packs stay partly live) and reclaims 34-412 MB when deletions are concentrated (whole packs die). This is the documented P1C behaviour, not a regression; `gc` reports `pack_repack_reclaimable_bytes` and points to `repack`.
* The default `repack` rule (rewrite packs below 50% live) has write amplification **<= 1** by construction: rewriting a pack with live share *L* writes L and reclaims 1-L. It does nothing at 10% and 30% random dead (no pack is below 50% live) and recovers 165 MB at 50% and 368 MB at 75% (random). Raising `-max-live-percent` to 100 recovers more (70 MB at 10%, 162 MB at 30%) at a cost the matrix prices: write amplification 5.9 at 10%, 2.0 at 30%, 0.9 at 50%, and 5-7x the time of the dry scan. **No adaptive policy was added**: the measured trade-off is the L/(1-L) curve, the 50% default sits at amplification 1, and an operator who wants space accepts a stated price. Candidate selection by dead-byte ratio is what `selectRepackPacks` already does (it ranks nothing; it applies the threshold to every pack).
* `gc` followed by `repack` costs the sum and reclaims what `repack` alone does (repack already retires fully dead packs): **GC and repack are not coupled**; `gc -apply` keeps its semantics. Time of the full sequence at 90% dead: 2.8 -> 1.5 s (random), 2.3 -> 1.2 s (range).
* Rewrite-heavy cells (all packs rewritten): wall 7.8 -> 3.8 s (10%), 6.2 -> 2.9 s (30%), 4.3 -> 2.5 s (50%), with CPU 9.3 -> 8.1, 7.3 -> 6.0, 5.0 -> 5.0 s. Before the DEFLATE encoders were pooled these cells burned 40% *more* CPU than base (13.0 vs 9.3 s): each stager window built a fresh encoder per worker.
* DEFLATE on random bytes is wasted (about 19% of repack CPU on this incompressible fixture). A cheap entropy pre-test would skip it but would change pack bytes for borderline chunks; not done (section 8).
* `gc -apply` on 42 k loose files (90% dead) is 0.54 s base, 0.52 s P3: the unlink sweep is not a bottleneck at this size and stays serial (its crash-injection hooks fire per file).

### 5.6 Startup, memory, allocation
In-process, `OpenStoreShared` then one scan on the open store (median of 2 interleaved runs; `ZEROS3_P3_STORE`, `zeros3_p3_bench_test.go`):

| store | operation | base ms | P3 ms | speed-up | base MB allocated | P3 MB | base allocs (M) | P3 allocs (M) |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| s100k | open | 430 | 216 | 2.0x | 250 | 133 | 1.44 | 1.30 |
| s100k | reachability scan | 1120 | 461 | 2.4x | 354 | 288 | 1.66 | 1.20 |
| s100k | stats | 2123 | 489 | 4.3x | 539 | 312 | 2.93 | 1.10 |
| s100k | verify (shallow) | 1258 | 414 | 3.0x | 386 | 307 | 1.66 | 1.10 |
| s1m | open | 4405 | 2471 | 1.8x | 2520 | 1346 | 14.36 | 13.01 |
| s1m | reachability scan | 15002 | 6034 | 2.5x | 3864 | 3141 | 16.58 | 12.02 |
| s1m | stats | 25217 | 6952 | 3.6x | 5685 | 3393 | 29.34 | 11.01 |
| s1m | verify (shallow) | 14187 | 5881 | 2.4x | 4179 | 3281 | 16.59 | 11.02 |

* Peak RSS of the CLI commands (above) at 1 M: stats 2,008 -> 1,644, verify 2,046 -> 1,572, gc dry-run 1,966 -> 1,538, `gc -apply` 1,505 -> 987, `repack -apply` 1,794 -> 1,033 MiB; `verify -deep` 2,391 -> 2,745 (it keeps the decoded manifests of every live root, about 250 B each).
* Small stores pay a fixed +20-60 MiB (worker buffers, `GOGC` 400 during open) e.g. `hist` 38 -> 99 MiB, `s10k` 50 -> 59 MiB.
* Synthetic locator build (`BenchmarkP3LocatorBuild`, not a store): 1 M records 215 -> 134 ms, 10 M records 2.93 -> 1.57 s (1.9x; 657 MB of records plus index in both: the index layout is unchanged).
* **Persistent checkpoint: not implemented.** Profile of a 1 M open: journal replay and the namespace it builds are about 85% of the work, pack footers/indexes 10-15%. A locator-only checkpoint (P1C's proposal) would save at most ~0.3 s of 2.5 s. What would pay is a *namespace* checkpoint bound to the journal sequence/offset/last-frame CRC with guaranteed full-replay fallback; the design, validity rules and qualification are in [`CHECKPOINT_DESIGN.md`](../z4/p3/CHECKPOINT_DESIGN.md) for P8. It is justified as a separate project for tens of millions of objects, not at today's measured scales (1 M opens in 2.5 s).

## 6. P1C and P2 interactions
* Inline manifests and segment records: reachability reads them through `casRead` (content-verified against the journal digest); the live set carries every live manifest digest, `packUsages` classifies them as before (`packed_live_manifest_records`), a tier move or repack never sees an inline manifest as garbage (`TestP3_*` repack tests verify deep and read every survivor after each crash point; the P1C layout matrix and tier suites pass unchanged).
* Shared openers: still never truncate, seal, adopt or relocate a segment, and now never touch the journal (section 7).
* P2 caches: no serving cache is read or written by any maintenance scan (`Verify` still purges them around itself; repack/compact still purge on exit). Cache entries are keyed by manifest digest and hold no locations, so repack, relocation and tier moves cannot leave a stale handle; the only P2 code touched is `scanCanonicalChunks`, which now refuses a second `"chunks"` member (see 7).

## 7. Correctness and qualification
**Defects.** (1) *Shared opener truncates a live journal* (above): a deterministic hook (`journal-replayed`) appends a frame between the opener's replay and its truncate; on `da88b4d` the store fails to reopen. Fixed: a shared opener's journal is read-only (`errJournalReadOnly`, appends fail loudly) and is never truncated. (2) Latent: a manifest with a second `"chunks"` member whose list was empty passed the P2 fast scanner as an empty list; the scanner now declines any manifest with more than one `"chunks"` token (the slow path decides). (3) Latent: a chunk digest written in upper-case hex would have been keyed by that string, so GC could judge the lower-case loose file unreachable; digests are now keyed canonically (manifests are always written lower-case; this only matters for hand-edited ones).

**Differential tests against the serial scan** (`zeros3_p3_reference_test.go` keeps the `da88b4d` scan verbatim): identical issues (kind, subject, detail), counters, sets, lengths, valid chunks, observer calls and representation results on healthy stores with every root category (current, history, snapshot, multipart, shared chunks, 3 MiB multi-chunk objects) in both layouts, with 1/3/4/8 workers and block sizes 1/2/3/7/32768; on six damage classes (missing small chunk, missing and corrupt multi-chunk chunk, deleted manifest, garbled manifest, deleted history manifest); on 15 hand-written manifest bodies (bad versions, short and malformed digests, negative length, length mismatch, leading zero, trailing junk, duplicate `chunks`, pretty-printed); and on a representation-bearing store (SQLite, tar, nested components, history) healthy and with a corrupted root. The only intended difference is the upper-case-hex normalisation.
**Fail-closed.** `TestP3_GCFailsClosedOnCorruptManifest` (both layouts, 1 and 4 workers): `gc -apply` returns `errGCUnsafe`, removes no pack, leaves unrelated objects readable. A corrupt reachable representation root sets `RepFaults` and blocks destructive maintenance (differential test above).
**Corruption during scans.** `TestP3_DeepVerifyFindsCorruptionInjectedMidScan` (hook on the scan's chunk check corrupts the last-visited chunk after the scan began) and `...BetweenPasses` (a chunk of a multi-chunk object rots after the chunk pass): both found, both layouts.
**Crash and kill.** `TestP3_RepackCrashPointsWithPipelinedStaging`: 12 crash points (first record, inside the second window, before fsync, validated, before rename, after rename, first/second replacement published, before/between/after obsolete-pack removal, completed) x 1 and 4 workers on a multi-pack store (800 objects, 60% deleted): after every crash `verify -deep` is clean and every live object byte-identical, a rerun converges, no staging file remains, a converged store selects nothing. `TestP3_RealKillDuringMaintenance`: `kill -9` of a child `repack` / `gc` at random points (14 of 24 and 9 of 24 runs killed in flight, three consecutive interruptions per store), then `verify -deep` + byte comparison + convergence. The existing repack/GC/compact/tier/bundle/history crash matrices, the P1C crash matrix, state model and real-kill tests are part of the root suite (below).
**Not crash-tested / not changed**: publication ordering (untouched); an interrupted *scan* has no durable effect by construction.
**Compatibility** (`scripts/compat_p3.sh`, `results/compat.txt`): a store written by the P3 server is replayed byte-for-byte, verified deep and maintained by the base binary; a store pruned, GC'd, compacted and repacked by the P3 binary verifies deep with the base binary and vice versa. Every fixture (built by the base binary) is read and maintained by the P3 binary. Format level, FORMAT.json, journal, pack and segment files are bit-compatible; older binaries need no change.

## 8. Rejected or deferred designs
| design | decision | evidence |
|---|---|---|
| Trust the manifest/chunk caches inside maintenance | rejected | contract of P2 (`CACHE_MODEL.md`) |
| Skip the per-chunk digest check in the whole-object pass | rejected | it could read a different, corrupt copy than the one the chunk pass verified (false failure / hidden damage); a location-aware memo would save ~1 of 3 SHA passes on multi-chunk data and is left for a later batch |
| Bloom filter / sorted external merge for the live set | not needed | exact `digestTable` holds 2 M digests in ~90 MB; 1 M objects scan in 8 s with 1.5 GiB total RSS |
| Entropy pre-test to skip DEFLATE on random chunks | deferred | ~19% of repack CPU on incompressible data, but changes pack bytes for borderline chunks and was not qualified |
| Parallel unlink sweep | rejected | sweep is 0.5 s for 41 k files; crash hooks fire per deletion |
| Online (concurrent-writer) GC/repack | rejected | needs a publication barrier; exclusive `LOCK` stays |
| Coupling `gc -apply` with repack | rejected | measured sum of both, same reclaim as `repack` alone |
| Adaptive repack threshold | rejected for now | write amplification L/(1-L); default 50% = 1 (section 5.5) |
| Locator-only persistent checkpoint | rejected; namespace checkpoint designed | section 5.6 |
| Persisting a liveness summary | rejected | cannot be validated against a mutating store without re-reading it |
| Second reachability scan in `compact -layout locality` | left | loose-chunk compaction is I/O-bound (below) |

## 9. Limits, remaining bottlenecks, not measured
* **Remaining bottleneck**: the namespace build at open (1.3 GB of maps and strings per 1 M objects; 85% of the 2.5 s) and, for deep verification, SHA-256 (about 550 MB/s per core on this VM; three passes on multi-chunk data).
* Loose-chunk `compact -apply` (10 k objects, 4.2 s) is bound by per-batch publication fsyncs and per-file unlinks; P3 does not speed it up and spends more CPU (3.8 -> 5.2 s).
* Memory: `verify -deep` holds the decoded manifests (+350 MiB at 1 M).
* Not done for lack of disk/time: stores of 64 MiB and 1 GiB objects (`mix` has up to 16 MiB), a representation-bearing integrated measurement (correctness is covered by the differential and the existing representation suites, performance by none), the synthetic 10 M-entry metadata store beyond the locator build, and the 64,000-object point (100 k and 10 k bracket it). Foreground-request latency during maintenance was not measured. No statement is made about real block-device power loss; the crash tests are process-level.
* Read-only maintenance against a live server uses all cores by default.

## 10. Gates
| gate | result |
|---|---|
| root build, `gofmt -l .`, `go vet ./...`, `go.mod` / `deps-proof.txt` unchanged (standard library only; `runtime/debug` is the one new import) | PASS |
| `go test -count=1 .` (root suite, production-default layout, includes every `TestP3_*`, P1C crash matrix / state model / real-kill, layout matrix, P2 cache and corruption tests, GC / repack / compact / tier / history / snapshot / fork / representation / sync / bundle / repair tests) | **PASS, 376 s** |
| `go test -race -count=1 -timeout 90m .` | **PASS, 1,319 s** |
| `scripts/validate.sh` stages `format modules s3 sync repro vectors client apps cas-batch direct-pack direct-pack-life bundle bundle-life bundle-delta bundle-delta-life bundle-scale bundle-delta-scale history tier tier-policy tier-rebalance locality index index-scale crash repack bulk` | **PASS** (`client` = AWS SDK for Go + minio-go smoke against a managed server; `repro` = reproducible build; `index-scale` = 5 M-record locator; `crash` = race-enabled pack/tier crash matrices) |
| `validate.sh compact`, `compression` with `COMPACT_SIZE_MIB=256 COMPRESS_CLASS_MIB=32 COMPRESS_SIZE_MIB=256` | **PASS** (6 s, 187 s). At the *default* sizes (128 MiB objects against the 64 MiB direct-pack threshold the stage sets) both fail the harness's own "loose layout" assertion **identically on `da88b4d`** (`compact` 13 s and `compression` 352 s, both FAIL on the base worktree): not a P3 effect |
| `validate.sh history-life`, `tier-life` | **PASS** (3 s, 5 s) once the two baseline commits they build (`24ec2a6`, `33478ce`, absent from this clone) were fetched; without them the stages fail with `fatal: not a valid object name`, identically on `da88b4d` |
| `validate.sh static` | **FAIL, pre-existing and unchanged**: source map 34 of 35 entries and test map 23 of 40 entries disagree, exactly the counts reported for `origin/main` in P1C (the new section 12b and test files are not in those maps) |
| old/new binary compatibility (`scripts/compat_p3.sh`) | PASS both directions |
| PUT / GET / HEAD / range regression, interleaved A/B plus A/A | no regression (section 1, [`REGRESS_TABLE.md`](../z4/p3/results/REGRESS_TABLE.md), [`REGRESS_AA_TABLE.md`](../z4/p3/results/REGRESS_AA_TABLE.md)) |

Merge state: the PR is opened non-draft only after the gates above, and merged with the GitHub `merge` method (never squash or rebase). A commit cannot contain the SHA of the merge that includes it, so the merge SHA, its two parents and the preserved development commits are verified through GitHub after the merge and recorded in the PR and in the final batch summary, not in this file (the P2 report's stale line, section 0, was exactly that omission). B2 draft #97 and drafts #80/#81 were not touched; representation authority was not enabled.

## 11. Conclusion and next step
**What P3 delivers.** Every maintenance command shares one scan that reads each manifest once, from storage, in parallel and in physical order, keeps its live set in a compact exact table, and does the same checks as before: 2.4-4.4x on `stats`/`verify`/`gc`/`repack` and 2.5-3.6x on `verify -deep` at 100 k-1 M objects, with lower CPU and (for scans) lower memory; open is 1.8-2.0x faster and allocates 47% less; repack that rewrites a lot is 2x faster; and a data-loss race between read-only tools and a live server is closed. Correctness evidence is differential (against the verbatim serial scan), fault-injected (mid-scan corruption, 12 crash points x 2 worker counts, real `kill -9`), and the full root suite plus its race run pass.

**Strongest negative result.** Persistent-checkpoint economics: the locator checkpoint P1C recommended would save under 15% of a 1 M-object open, because journal replay and the namespace it builds are 85% of it; and `gc` still cannot reclaim randomly scattered dead data from P1C packs, which only a `repack` that pays write amplification L/(1-L) can.
**Remaining bottleneck.** Namespace construction at open (1.3 GB per million objects) and SHA-256 in deep verification (three passes on multi-chunk data).
**Is a separate persistent-checkpoint project justified?** Yes, but as a *namespace* checkpoint (design in `CHECKPOINT_DESIGN.md`), for stores of tens of millions of objects; not for today's scales, and not as a locator-only artefact.
**Recommended next batch.** P4 as already defined (synchronous representation encoding, component publication, exact-codec compute, representation reads); the P3 engine already gives it a parallel, exact reachability scan to build on. Later: a location-aware verification memo (about one of three SHA passes on multi-chunk deep verify) and the namespace checkpoint under P8.

Not started: P4 or later; B2 #97 and drafts #80/#81 untouched; representation authority not enabled.
