# Z4-C1 — Production representation authority

Verdict: **C1_PARTIAL_NEEDS_QUALIFICATION**. Branch `z4/c1-production-authority` (mirrored to `claude/zeros3-z4-c1-authority-9uasio`) from `origin/main` `dfbbc8b`. Draft PR only; #97/#80/#81 untouched. Evidence: [`../z4/c1/`](../z4/c1/README.md).

## What is production code now
B2's body records (pin / existing rep kinds 1-12 / exact delta) keyed by content SHA, closure-checked atomic publication, fail-closed reachability/gc/repack, virtual chunk index for sync/bulk/bundle/replication, `authority migrate|status|revert [-all]`, repair; ported onto P1-P4 (P2 served readers, P3 scan, P4 deferred builds). Default off; creation is an offline exclusive-lock CLI. Newest version stays CDC by default (`-order newest`); `-archive` is explicit. Store levels: **10** authority, **11** segments, **12** both (not cumulative); `FORMAT.json` records `required_level`; `revert -all` lowers to it only when no record remains. Non-adopting stores are byte-unchanged. P5 union-accounting planner is the only planner; ZW1 windowed single-base delta (extracted `dcore`) removes the 256 MiB limit. No P7 form is enabled.

## Gates
| Gate | Result |
|---|---|
| G1 retire-everything | Seam + hook retire every body-backed object's chunks. 167 of 1,270 root tests fail in that mode: 66 assert physical layout, 34 corrupt/delete a retired chunk and expect detection, 24 read CDC as a reference, 16 assert store level, 27 other (sampled: caches/counters/timing, rep sidecars). Recorded production chains that touch retired chunks: ingest dedup stats (revive-on-write), and the B2-rerouted bulk/sync/bundle/delta-export/base-check/replication paths. **No new distinct unguarded production path found (limit 5)**, but not "all pass" and the 27 were not each proven individually; subprocess-CLI suites are not covered by the in-process hook. Status: **partial** |
| G2 power loss | Real `strace -y` traces of migrate (49 barriers), gc, repack, revert-all replayed with 4 crash-image classes per cut (6x300 dense revert-all cuts, 40 cuts others): 0 failures; fsync-less control fails as required. Model-based, not block-level; plus 18-point/26-kill SIGKILL matrix. **Pass (model)** |
| G3 scale | 1.1M objects, 1,010,700 chunks, 50k pin records: verify 12.6 s, deep 65 s, gc 17.7 s, repack 10.3 s, revert-all 136 s; RSS 3.5-3.6 GB for scans (same as the reverted CDC store, +3%). Migration of an 80-object scope: 236 s, 2.0 GB. **Pass** |
| G4 compat | v0.1.1, b5d1762, 036ac2c, 4059cd9, dfbbc8b: all refuse levels 10/12 with a byte-identical tree, open a non-adopting store, import a retired-CDC bundle, accept replication exactly. **Pass** |
| G5 1 GiB | 3 x 1 GiB history, fresh processes, SHA-256 exact (independent regeneration), 4 KiB range 5.4 ms. Migrate peak RSS **274 MiB** (was 684 before the anchor-table/cache/GC bound), revert-all 154, deep verify 148. Target 128 MiB + window is **not strictly met** |
| G6 reads | Hybrid admission is refused unless whole-object read >= 0.5x CDC and 4 KiB amplification <= 4x (measured per object). Only SQLite admitted (0.282x); CLR/DEX/tar/Git/SST/Parquet hybrids refused. Archive forms: whole 21-330 MB/s vs CDC 1,084; 4 KiB amplification 100x (delta) to 8,644x (rep). **Pass as a gate; archive-only classes are slow** |
| G7 revert/floor | revert -all, verify, level restored, real pre-authority binary reopens and deep-verifies; floor refused while a record remains. **Pass** |
| G8 crash loops | gc/repack/revert-all/migrate killed at 1-4 successive boundaries each, exact after every kill; B2 corrupt-base/peer-repair tests pass. Interrupted repair not tested. **Partial** |
| G9 economics | below |

## G9 physical bytes / CDC bytes (gc + repack + compact; `results/economics.json`)
| Family | additive rep | delta only | archive best | rep only | hybrid |
|---|---:|---:|---:|---:|---:|
| Parquet Z3-31 x8 Snappy | 1.45 | 0.92 | **0.453** | 0.453 | refused |
| DEX 3 releases | 1.54 | 0.585 | **0.543** | 0.543 | refused |
| tar x3 | 1.53 | 0.374 | 0.373 | 0.634 | refused |
| CLR x5 | 1.97 | 0.323 | 0.323 | 1.0 | refused |
| SQLite x4 | 1.39 | 0.297 | 0.297 | 0.393 | 0.282 |
| SST Snappy x4 | 5.73 | 0.32 | 0.32 | 1.0 | refused |
| Git packs x3 | 2.05 | 0.58 | 0.58 | 1.0 | refused |
| Parquet Zstd x4 | 2.63 | 0.936 | 0.936 | 1.0 | 0.958 |
| unrelated | 1.75 | 1.0 | 1.0 | 1.0 | 1.0 |
Targets: Parquet rep-only <= 0.40 **not met** (0.453; the planner defect is fixed: 0.92 -> 0.453, but a 8-revision representation is 47 MB vs 31 MB in B8 for the same logical chain, pointing at component sharing in the P1-P4 rep builder, not investigated); DEX <= 0.50 **not met** (0.543 on this F-Droid set, not B8's). Pilot >= 15%: met (tar/CLR/SQLite/SST save 63-70%, 58% Git). Unrelated and uneconomic data stay CDC. Modeled vs additive vs realized: additive 1.4-5.7x, realized as above. Peak during migration 1.0-1.7x. Pythia/SmolLM2/quantized histories not run (their forms are not enabled).

## Other results
`prodshadow`: the four baseline failures (also on untouched main) had one cause, the lab bridge hooking `computeReachabilityObserved` after P3 moved the scan; the bridge now wraps `reachabilityScanOpts`; all pass. Root suite 556 s PASS; Full root suite under `-race` 2,213 s PASS (no races). Fuzz 40 s each, no findings: `FuzzXDeltaEnvelope` 574k execs, `FuzzAuthRecord` 573k, `FuzzAuthWindowRecord` (new) 453k. Clean checkout builds and vets, `go.mod` unchanged and dependency-free, reproducible build identical. Source map of `zeros3.go` regenerated (`srcmap.py`, 38 entries pass `toc_check`); the older test map is stale on main as before. Fixed by tests: format level assertions (10 reserved -> 10/12), authority tests state archive order explicitly.
P7: `extract_all.sh` onto this tree: numeric 70 and hard-codec 306 differential vectors byte-identical, rehearsals pass, production digest unchanged. Not landed (about 11.5k inert lines) and not enabled; no savings claimed; extractor now aliases clashing std imports.

## Code
`zeros3.go` +5,626/-93. Mechanically transferred ~4,800 (B2 3,048, `dcore` block 1,466, P5 planner ~270); materially modified ~250 (12 conflict regions, planner legacy removal); new ~830 (levels/revert-all/lowering, windowed adapters, streamed sketch, read-ratio gate, scope, virtual-index keying, CLI, seams). Tests +3,860 (B2 authority/economics ~2,330 transferred; the rest new).

## Limitations and P8
Offline exclusive-lock maintenance only; one record file per body (50k fine); native closure transfer is a library stream, `stats` not authority-aware; hybrid is rarely admitted (delta whole reads are 0.3x CDC), so authority is an archive tier; floor lock-in until revert -all. P8: investigate rep component sharing regression vs B8, leaf-hashed ZW1 frames for range amplification, record packing for millions of bodies, an online background worker, authority-aware stats.
