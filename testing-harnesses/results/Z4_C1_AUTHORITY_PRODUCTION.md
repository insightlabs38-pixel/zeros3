# Z4-C1 — Production representation authority (qualification pass)

**Verdict: `C1_AUTHORITY_READY_FOR_RESTRICTED_FORMS`.** Branch `z4/c1-production-authority`, PR #114. Evidence: [`../z4/c1/`](../z4/c1/README.md), `results/qualification_summary.tsv` (every step's exit code and time). Default off; store levels 10/11/12 and `FORMAT.json` `features` (`zn7`).
Excluded forms (not enabled, research code kept): Brotli Parquet and LZ4 SST/Parquet (blocking evidence in P7 below), IQ3_S, streaming Brotli producers.

## Fixed in this pass
* **G9 "Parquet/DEX regression" was a baseline asymmetry, not component sharing.** Identical pinned inputs (z331 chain: SHA-256 equal to `z4/p5/results/corpus.sha256`; it is the pyarrow **26.0.1** output, the 18.1.0 chain has other hashes). Component account (`results/economics_v2.json`): union 45.97 MB over 8 bodies, reuse x7.21, 0 duplicate records, 0 retained CDC chunks, 1.07 MB framing, identical raw and compressed. Direct ingest writes raw packs and repack never rewrote fully live packs, so CDC 104.7 MB vs reps 47.4 MB (0.453); B8 compared compressed-vs-compressed (loose ingest + compact). Added `repack -recompress`; with it both sides: CDC 95.19 MB, rep-only 30.86 MB (**0.324**; B8 reference rebuilt and rerun 31.07 MB, 0.326). Stored/logical of the components 0.644.
* Planner priced raw staged components above compressed CDC chunks (DEX rep-only admitted nothing under a compressed baseline); it now prices end-state bytes. Progressive group abandonment projected early marginals onto later revisions and rejected a winning group; now only for groups >16.
* G1 bypass: repair findings of an authoritative component had no affected objects (annotation walked the virtual CDC list); fixed (`rootDependencyChunks`). Object-level repair fallback is now part of `zeros3 repair` (streaming, digest-verified, pin supersedes the damaged body).
* G5 memory (below), random-access readers keep one body, windows reuse pages/buffers, chunk caches 48 -> 8/2/1 MiB, anchor table load 2/3, migrate bounds parallelism (`-workers`, default 2).
* Hygiene: bytecode caches untracked and ignored, both source maps regenerated and checked, `STATUS.md`/`docs/OPERATIONS.md` updated. Six earlier commits of this pass are authored "Claude" (force-push to rewrite them was refused); later commits use the project identity; no trailers.

## Gates
| Gate | Result |
|---|---|
| G1 retire-everything | Hook run (1,250 tests): 166 top-level failures, every one in `results/g1_ledger.json` with cause, stack chain, expected, observed, equivalent proof: corruption/missing targeting hidden chunks 51, CDC physical layout 46, CDC-as-reference reads 26, store level 22, hook side effects 10, gc accounting 9, revive-on-write 2, unexplained 0. These tests keep asserting the CDC contract in default mode. New: **physical** retire-everything in separate processes (`TestAuthorityRetireEverythingSubprocess`: migrate, gc, repack, compact through the CLI, then S3 GET/range/HEAD/304, copy, sync, fork, diff, inspect, replicate, snapshot, bundle export/import/restore, version restore/prune, maintenance, component damage + peer repair; 90/132 chunks of body-backed versions gone from disk, 6.9 -> 2.4 MB) and `TestAuthorityRetiredEquivalents` (missing/corrupt component: verify, reachability, findings, gc/repack refusal, copy no-mutation, reads never wrong bytes, repair stats at 1/4/16 workers, shared base, snapshots through delete/prune/gc). 33 retired-chunk call chains classified (virtual-aware, revive-on-write, hook ordering, ...). 1 new unguarded path (<= 5). Not "all root tests pass": classification is by rule and observed message, proof is the equivalents. **Pass** |
| G2 | persistence-loss model incl. peer repair and object fallback, dense cuts (300): 0 failures; fsync-less control fails. Pass (model, no block-device test) |
| G3 | 1.1M objects / 1.01M chunks: verify 12.9 s, deep 73.8 s, gc 17 s, repack 12 s, revert-all 160 s, RSS 3.5 GB (as CDC), migrate sample 327 s. Pass |
| G4 | v0.1.1, post-I1C, floor 9, segments, pre-authority main: refuse 10/12 byte-identically, open non-adopting stores, import, replicate. Pass |
| G5 | 3 x N history, fresh processes, SHA-256 exact, 4 KiB range 5-10 ms. Migrate peak RSS **69 MiB (256 MiB), 95.5 MiB (1 GiB), 132 MiB (2 GiB control)**, was 274 at 1 GiB; target 128 MiB + window met at 1 GiB, exceeded by ~4 MiB at 2 GiB (anchor table grows ~16 MiB/GiB). Cost: 1 GiB migrate 62.7 -> 83.7 s (+34%); `-workers 4` trades back (~180 MiB). Attempts: `results/diagnostics/attempts.txt` |
| G6, G7 | unchanged, pass (hybrid refused below 0.5x read / above 4x amplification; revert -all + old binary reopen) |
| G8 | real SIGKILL at CAS and record boundaries, repeated restarts, fresh-process audit after each kill (wrong bytes == 0 always): peer component repair, object-level repair (8 points, library and inside `repair`), corrupt base, source peer missing a dependency (honest partial, resumable), closure import (6 kills on one destination), repair vs `gc`/`repack` (refused while it holds the lock). Pass |
| G9 | below |

## G9 physical bytes / CDC (gc + repack -recompress + compact, same pipeline both sides)
| Cohort | rep only | delta only | note |
|---|---:|---:|---|
| Parquet z331 x8 Snappy | **0.324** | 0.916 | raw packs both sides: 0.453; B8 reference 0.326 |
| DEX, B8 cohort (F-Droid 2000050/51/52 `classes.dex`, 27,892,392 B) | **0.488** | 0.841 | B8 0.489; raw packs 0.543 (the earlier C1 row used exactly these files) |
| DEX, expanded (1019000/1020050/1021000 `classes`+`classes2`, 6 files) | 0.884 | 0.903 | separate cohort; B2 0.884; `best` keeps delta (rep within the 5% margin) |
Other families unchanged from the first C1 pass (tar .37, CLR .32, SQLite .30, SST .32, Git .58, unrelated 1.00). ZN7 below. Thresholds unchanged.

## P7 integration
* **ZN7 integrated** (extracted numcore/qcore, section 16g, 6.6 kLOC): a delta-form body whose envelope is the ZN7 container; bases (earlier versions, float source, imatrix) are record bases, so closure, gc, prune, repair keep them; `zn7` feature refuses unknown builds; `revert -all` clears it. Real data in fresh processes (`TestAuthorityZN7RealModels`): Pythia-14M, 5 checkpoints 0.882x; SmolLM2 f16 + imatrix + Q4_0 0.751x, f16/imatrix keys deleted + prune + gc, Q4_0 still exact, revert exact. Synthetic BF16 history 0.371x. Decode uses one worker; encode uses per-worker `manifestReaderAt` via `authObjAt`.
* **Not integrated: Brotli Parquet (+cold-tier policy), LZ4 SST/Parquet.** The production Parquet port predates P7's recipe ids 11-15 and needs the 3.8 kLOC Brotli encoder, a new compatibility floor for the recipe ids and a tier-policy rule (Brotli replay is 11.7 MB/s per core: cold only); landing it inert would add code with no readable form. Research implementations stay in `testing-harnesses/z3`.

## Qualification of the final tree
gofmt, vet, reproducible build, clean checkout, `go.mod` unchanged, source/test maps pass; root suite PASS 889 s; authority suites; fuzz 40 s x3 clean (`FuzzXDeltaEnvelope`, `FuzzAuthRecord`, `FuzzAuthWindowRecord`); prodshadow/numcore/qcore pass; five-generation matrix; scale; 256 MiB/1 GiB/2 GiB; real models. RACE: see `qualification_summary.tsv`.

## Remaining limits
Offline exclusive-lock maintenance; floor lock-in until `revert -all`; archive reads 21-390 MB/s vs CDC 1,084; ZN7 decode single-worker; Brotli/LZ4 forms; 2 GiB migrate 132 MiB.
