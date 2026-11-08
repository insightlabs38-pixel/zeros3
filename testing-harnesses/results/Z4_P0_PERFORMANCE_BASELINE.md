# Z4-P0 — Comprehensive performance baseline and bottleneck discovery

Verdict: **P0_BASELINE_PARTIAL** (research/benchmark infrastructure only; `zeros3.go` SHA-256 `ac29a564…f6b6f493` and root `go.mod` unchanged).
Branch `z4/p0-performance-baseline` from verified `origin/main` `a06636387f2bf1a19b815033b63a32384a84e6c9`. Evidence: [`../z4/p0/`](../z4/p0/README.md) (raw JSONL, compact profiles, generated tables `results/P0_TABLES.md`, [register](../z4/p0/BOTTLENECK_REGISTER.md)).
Why PARTIAL and not COMPLETE: every primary subsystem was measured on the real code, but (i) all storage numbers come from one virtualized ext4 disk with a *cheap* `fsync` (p50 0.26 ms), (ii) sync/replicate, SquashFS-LZ4, Pebble SSTs and models above 0.54 GB were not measured, (iii) maintenance was measured one run per ladder rung, (iv) ~half of the representation and all research/B2 runs shared the VM with one pinned background job (quantified below). See §14.

## 0. Campaign state and workstream status
Verified at start: B8 PR #103 merged; B2 PR #97 open **draft** (head `94ac9c8`); #80 (`43dfe41`) and #81 (`0a37846`) open drafts; production ingest is CDC-authoritative (`zeros3.go:28024` "CDC stays the authority"); no B1–B7 code in production (`zeros3.go` 56,197 lines, identical SHA to B8's audit). B2 was only built/run in a detached worktree (`/tmp/.../b2wt`, never pushed); the B8 accounting patch ran in a *second* scratch worktree.

| WS | Scope | Status |
|---|---|---|
| A | driver, overlay profiler, env capture, JSONL, verification | COMPLETE |
| B | production PUT/CDC matrix + profiles | COMPLETE (fsync cost caveat) |
| C | GET/range matrix + scaling + profiles | COMPLETE |
| D | maintenance ladders (objects, bytes, history) + profiles | PARTIAL_WITH_EVIDENCE: 1 run/rung; no sync/replicate; maintenance-under-load is *refused by design* (see PM-5); no tier-rebalance/restore timing |
| E | kinds 1–12 | COMPLETE for 1–12 (2–3 reps; Merkle `prefer` reads stopped early to save time; n recorded) |
| F | B1 + B2 | COMPLETE (B2: 7 families × 4–9 variants, unmodified draft; diagnostic patch variant separate) |
| G | B3 + B4 | COMPLETE for SmolLM2/Git/tar/Parquet and LLVM/PDB/DEX/CLR; DWARF/ORC not applicable |
| H | B5 + B6 | PARTIAL_WITH_EVIDENCE: B5 ≤0.54 GB checkpoints (no Qwen-scale probe); B6 SST-LZ4/Parquet LZ4_RAW/LZ4HC micro; **SquashFS-LZ4 NOT_ATTEMPTED** (`squashfs-tools` absent) |
| I | integrated workloads | COMPLETE (production, 6 workloads) + B2 research-only |
| J | roadmap | COMPLETE |

## 1. Executive performance findings (all measured on current code)
1. **Small/medium PUT is filesystem-bound, not CPU- or crypto-bound.** 1 MiB c4 runs at 68 MiB/s on disk vs **404 MiB/s on tmpfs**; 4 KiB c8 668 vs 2,735 ops/s. Each 4 KiB object performs 6.2 `fsync`, 6 `openat`, 2 `mkdir`, 1 `rename` (strace). `os.CreateTemp` is 29–53% of server CPU; all publishers serialize on one `casPubMu` (16.5 s mutex delay in a 4.6 s window). (PW-1/2)
2. **Single-stream large PUT is CPU-pipeline-bound at ~109 MiB/s (0.92 core)**: SHA-256 = 48% of CPU in **5 passes/byte** (chunk, object, pack body, and a read-back verify of the pack that re-hashes it twice), MD5 24%, CDC 11%; one goroutine does all of it. 4 streams scale to 322 MiB/s. (PW-3)
3. **GET cost grows with object size because every request re-reads and JSON-parses the whole manifest**: a 4 KiB range costs 0.42 ms on 1 MiB, 2.8 ms on 256 MiB, **9.3 ms on 1 GiB** (loose or packed); JSON decode ≈ 50% CPU; 4 KiB reads move 23–46× the bytes. (PR-1/2)
4. **Metadata tools are serial full scans**: stats/verify/doctor/gc-dry/compact-dry each cost 5–12 s at 64k objects (80–160 µs/manifest, one core); `gc -apply` 19.4 s; 328,782 inodes and +28% allocated space for 64k small objects. `gc/compact/repack/prune` refuse to run while the server is up. (PM-1..5)
5. **Optional representations are synchronous in the PUT path and multiply CAS files 67–216×**: PUT throughput falls 2.4× (Model) to **137× (gzip)**; Merkle 99→1.0 MiB/s with 156,951 files vs 729. Syscalls are 54–69% of that CPU; gzip/zstd are algorithmic (exact DEFLATE search). Storage is 1.0–5.7× CDC (additive). (PX-1..4)
6. **B1 discovery is not the cost; exact delta encoding is**: QueryMS = 0.1–0.5% of encode; `simdelta.match` 45% + per-candidate flate-L6 22%. Encode 1.8–6.2 MiB/s on structured data. (R1-1)
7. **B2 pays 4–10× time and 2–3× peak for identical bytes**: tar 42.6 s vs 8.3 s (both 0.499× CDC), Parquet 22.4 vs 2.3 s (both 0.476), SQLite 27.0 vs 6.2 s (both 0.295); only DEX/CLR need the rep path. Each source is decoded ≥3× (DEFLATE-pack decode 48% + `tarPrescan` 28% of migration CPU). (R2-1)
8. **B3's "memory-heavy, 16 MB/s" is a hash-function choice and whole-file reads**: `hash/fnv` byte-at-a-time = **29% of CPU**; live heap is `os.ReadFile` buffers (98.8%) and GOGC doubles it to 1.97 GB RSS. (R3-1)
9. **B5's family coder spends 30% re-hashing (SHA-256) and 22% in NRC parameter search**; B6's HC level-9 replay is **74× slower** than explicit replay (5.0 vs 367 MB/s). (R5-1, R6-1)
10. **Most surprising**: the `best-of-two` speculative strategy and the inline representation build both pay full cost for work whose result is discarded or deferrable, and the dominant production hot spot (per-chunk file creation) is amplified by *every* higher layer (representations 216×, B2 recovery, gc unlink). One storage-layout fix is a prerequisite for several "algorithm" batches.

## 2. Environment and measurement protocol
Firecracker VM: Intel Xeon 2.1 GHz, 4 vCPU (1 thread/core, AVX2/AVX-512/SHA-NI), 16 GiB RAM, no swap, Linux 6.18.44, Ubuntu 24.04, Go 1.27.0 (default flags, `-trimpath`), root ext4 on virtio `vda` (`rotational=1`, write-back cache), no cgroup limits visible, **no `perf`**, `strace` present, `drop_caches` writable (cold = sync + drop_caches + fresh server process; verified by cold/warm sequential read 2.6–3.0 vs 4.8–5.2 GB/s). `env.json`, `calib.jsonl`.
Calibration (3 runs over 4.8 h): SHA-256 1 core 996–1067 MiB/s, 4 cores 4.78–4.83 GiB/s; memcpy 6.0–7.0 GiB/s but **CV 28–33%**; `fsync` 4 KiB p50 0.20–0.28 ms, p99 0.48–1.39 ms, max 1.2–11.6 ms; seq write+fsync 182–371 MiB/s (CV up to 30%); CPU steal ≤0.43%. **Quiet-machine recheck 4.8 h later** of six key baselines: ratios 0.91–1.08 (PUT 1 MiB c4 +3%, direct pack +8%, 4 KiB c8 +4%, GET 1 MiB c8 +5%, 4 KiB range −9%, 1 GiB seq −5%). Profiling overhead (cpu+mutex+block+alloc) is indistinguishable from this drift (ratios 0.82–1.21 vs plain). Concurrent-job perturbation of research timings (pinned job on other cores): B4 llvm 25.1 s → 19.8 s quiet (−21%), B5 coder 53.2 → 46.5 s (−13%), B1 tar 87.2 → 90.3 s (+4%); numbers marked `note` in JSONL.
Method: `p0bench` drives the **real server over HTTP (SigV4) and the real CLI**; each repetition = fresh store + fresh process; stores on the same ext4 unless labelled tmpfs; content generated deterministically (random-access ChaCha8 blocks; revisions splice 4–16 KiB insertions/replacements) outside the timed window; every GET/range byte is compared inside the window; every PUT scenario is replayed in a **fresh server process** and compared (all `verified:true`; 0 mismatches). Server CPU/RSS/IO from `/proc/PID`, `wait4` rusage, `/proc/diskstats`; Go runtime metrics, CPU (500 ms segments), alloc, mutex, block profiles from an `-overlay` hook (the production file is not edited). Medians with [min–max]; **UNSTABLE** flag = CV >10% or max/min >1.25. Corpora pinned and hashed (`results/dataset_manifest.sha256`, 182 files).
Disk-backed vs tmpfs: PUT/GET tables are disk-backed; two PUT rows are tmpfs. `fsync` here is ~0.26 ms; on SATA/network volumes (1–10 ms) the fsync-bound findings (PW-1/2, PX-2) get **worse**, CPU-bound findings (PW-3, PR-1, R*) do not change.

## 3. Production PUT (reps off). `put.jsonl`; n=5 unless noted; all verified
| Case | MiB/s | ops/s | p50 / p99 ms | srv cores | RSS MiB | phys/logical |
|---|---|---|---|---|---|---|
| 4 KiB c1 / c8 | 1.1 / 2.6 | 270 / 668 | 3.5/6.8 · 11.8/20.0 | 0.68 / 1.93 | 17–18 | 1.215 |
| 64 KiB c1 / c8 | 12.3 / 28.7 | 196 / 458 | 4.9/9.1 · 17.0/30.6 | 0.67 / 1.90 | 17–18 | 1.014 |
| 1 MiB c1 / c4 / c8 / c16 / c32 | 42.7 / 67.7 / 66.6 / 73.1 / 81.6 | 43–82 | 21.7/42 · 56/118 · 112/249 · 201/424 · 374/761 | 0.75–1.76 | 17–37 | 1.002 |
| 16 MiB c1 / c4 | 51.8 / 78.6 | 3 / 5 | 303/391 · 769/1088 | 0.93 / 1.68 | 17–19 | 1.001 |
| **64 MiB direct-pack** c1 | **109.0** [105.9–115.1] | 2 | 543/573 | 0.92 | 17 | 1.003 |
| 64 MiB forced loose (`DIRECT_PACK_MIB=0`) | 50.8 [47.6–59.7] **UNSTABLE** | 1 | 1173/1301 | 1.01 | 17 | 1.001 |
| 64 MiB ×4 conc, direct pack (n=3) | 322 [267–354] **UNSTABLE** | 5 | 740/844 | 2.63 | 29–32 | 1.003 |
| 1 GiB single (n=3) | 99.4 [96.7–100.4] | – | 10,303 | 0.93 | 23 | 1.003 |
| text 1 MiB c4 / runs 1 MiB c4 | 124.5 / 154.6 | | 29/105 · 25/42 | 1.88 / 1.96 | 18–19 | 1.001 |
| dup 50% / 90% (1 MiB c4) | 84.4 / 103.7 | | 49/113 · 39/87 | 1.7–1.8 | 19 | **0.672 / 0.536** |
| similar revisions 16 MiB×24 c4, 2% edits: rev0 → rev1–4 | 78 → **342** | | | 2.2 | 19 | 1.00 → **0.234** (10% edits: 0.715) |
| multipart 64 MiB, parts 8 MiB, 1 / 4 parallel | 37.2 / 48.7 | | 1665 / 1306 | 0.89 / 1.52 | 18–19 | 1.015 |
| aws-chunked (signed chunks) 16 MiB c4 / signed payload 1 MiB c4 | 82.1 / 69.0 | | 761 · 55 | 1.7 | 20 | 1.001 |
| pack DEFLATE `auto` vs off, text 64 MiB | 53.8 vs 111.0 | | 1170 vs 560 | 1.00 | 20 | **0.214** vs 1.001 |
| mixed sizes 4k:50/64k:30/1m:15/16m:5, c4 (n=3) | 92.0 | 86 | 11.5/615 | 1.44 | 19 | 1.002 |
| **tmpfs** 1 MiB c4 / 4 KiB c8 | **403.6** / 10.7 | 404 / **2,735** | 9.2/17.5 · 2.7/7.9 | 2.64 / 2.32 | 19–21 | – |

CPU anatomy (profiles, `results/prof/prof_put_*`): direct pack 3.91 CPU-s/512 MiB = SHA-256 48% (Sum256 chunk 0.74 s, object hash 0.36 s, pack-body `multiWriter` 0.37 s, read-back `teeReader` 0.40 s), MD5 24%, `findCDCBoundary` 11%, syscalls 10%, GC 1.1%. Loose 1 MiB c1: syscalls 56% (`CreateTemp` 29%, `casEnsureShardDirs` 13%, fsync 10%), MD5 12%, SHA 10%, CDC 6.5%. strace per 4 KiB PUT: 6.2 fsync. Microbench on this ext4: create+write 64 KiB = 271 µs (tmpfs 30), fsync of that file 410 µs, `mkdir` 76 µs. Auth is not a bottleneck (signed ≈ unsigned). Server memory is bounded (≤37 MiB RSS at c32, 23 MiB for 1 GiB). Similar-revision physical growth shows **CDC edit amplification ≈11×** (2% edits → 23% new bytes at 64 KiB average chunks).

## 4. Production GET / range. `get.jsonl`, `scale_range.jsonl`, `mixed.jsonl`
| Case (n=5) | MiB/s or ops/s | p50 / p99 ms | TTFB p50 | srv cores |
|---|---|---|---|---|
| whole 1 MiB warm c1 / c8 | 437 / 683 MiB/s | 2.2/3.5 · 10.7/27 | 0.49 / 2.45 | 1.2 / 1.9 |
| whole 1 MiB **cold** c1 / c8 | 212 / 496 | 4.5/8.3 · 15.6/25 | 0.78 / 2.59 | 0.76 / 2.07 |
| whole 16 MiB warm c1 / c4; cold c1 / c4 | 577 / 884; 259 / 558 | 27/31 · 70/89; 59/80 · 109/134 | 0.7–2.3 | 1.2–2.0 |
| whole 4 KiB warm c1 / c8; cold c1 | 2,980 / 11,317 ops/s; 1,644 | 0.30/0.66 · 0.48/3.0; 0.56/1.1 | 0.25–0.5 | 0.8 / 1.4 |
| 1 GiB sequential, pack: warm / cold (n=3) | 842 / 771 | 1220 / 1320 | 13.8 / 17.3 | 1.0 |
| 256 MiB sequential, loose: warm / cold (n=3) | 631 / 402 | 403 / 430 | 3.3 / 3.8 | 1.2 / 0.9 |
| 4 KiB range cold c1: 1 MiB loose / 64 MiB loose / 64 MiB direct-pack / compact raw / compact **DEFLATE** | 1,351 / 736 / 741 / 1,579 / **553** ops/s | 0.69 / 1.26 / 1.21 / 0.57 / **1.71** | = p50 | 0.85–1.08 |
| 64 KiB range cold c1: same five | 1,050 / 579 / 610 / 1,166 / 442 ops/s | 0.82 / 1.53 / 1.41 / 0.73 / 1.88 | | |
| 1 MiB range cold c1: same five | 270 / 224 / 245 / 270 / 104 | 2.11 / 2.91 / 2.71 / 1.93 / **6.07** | | |
| 64 KiB range warm c1 / c2 / c4 / c8 / c16 / c32 | 1,552 / 2,509 / 3,085 / 3,702 / 3,649 / 3,577 ops/s | 0.51 · 0.59 · 1.0 · 1.74 · 3.6 · 7.2 (p99 1.1 → 33) | | 0.96 → **2.0** |
| text 1 MiB whole c4 warm: loose / raw pack / DEFLATE pack | 715 / 1,272 / **496** MiB/s | 5.2 / 2.8 / 7.4 | | 1.9–2.8 |
| hot zipf 1 MiB c4 | 745 MiB/s | 5.0/11.8 | | 1.9 |
| mixed 4 readers (64 KiB) + 2 writers (1 MiB), 20 s ×3 | reads 76–81 MiB/s, writes 55–62 | read 1.9/13; write 29–33/45–53 | | 1.5–1.6 |
| mixed 2 readers (whole) + 4 writers (4 MiB) | reads 102–127, writes 87–102 | read 9.2–9.7/85–103; write 136–162/252–276 | | 1.5–1.7 |

**Range cost vs object size (4 KiB, warm, c1, n=3)** 1/4/16/64/256/1024 MiB: **0.42 / 0.46 / 0.59 / 1.01 / 2.81 / 9.28 ms** (direct-pack identical to loose); whole-GET TTFB 0.54 → 14.1 ms. Amplification (server `rchar` / bytes served): 4 KiB reads 23× (1 MiB objects), 46× (64 MiB); 64 KiB 2.4–3.8×; 1 MiB 1.0–1.2×; device amp 18–20× (4 KiB), 7× (DEFLATE pack). Profile (`prof_range_4k_pack`): `HeadObject→readVerifiedManifest→json.Unmarshal` = 50% of CPU, `os.ReadFile` of the manifest 370 MB allocated per 2000 requests, SHA verify of whole chunks 12%, GC 8%. Allocation: whole 1 MiB GET allocates 2.2× the bytes served with **GC = 18.5% of user CPU**; 4 KiB range allocates 29–74 KB per byte served. GET server CPU plateaus at ≈2.0 of 4 cores for c≥8 while mutex delay is only 0.18 s/1.95 s — **unresolved** (candidates: GC, syscalls, client co-location). Under write load read p50 rises 1.9 ms (vs 0.5 ms unloaded) and p99 13 ms.

## 5. Maintenance and scale. `maint_*.jsonl`, `mkstore.jsonl` (median wall s; cold = drop_caches; 1 run/rung for mutating ops, cold+warm pair for read-only)
Object-count ladder (2 revisions, 30% deleted, sizes 4k:60/16k:25/64k:15) — exponent = log(t₆₄ₖ/t₁ₖ)/log 64:
| op | 1k | 4k | 16k | 64k | exp. |
|---|---|---|---|---|---|
| stats warm / cold | 0.09/0.17 | 0.32/0.61 | 1.16/2.60 | **5.19/11.81** | 0.97 |
| verify | 0.07/0.17 | 0.29/0.62 | 1.06/2.21 | 5.22/10.14 | 1.0 |
| verify -deep | 0.42/0.50 | 1.43/1.78 | 5.50/6.79 | **25.6/30.5** | 1.0 |
| doctor / gc dry / compact dry (warm) | 0.07/0.08/0.12 | 0.27/0.27/0.38 | 1.05/1.0/1.6 | 5.2/5.4/7.8 | 1.0–1.05 |
| startup (open + journal replay + chunk scan) | 0.02 | 0.06 | 0.17–0.22 | **0.75–0.81** | 0.9 |
| prune keep-0 / **gc apply** / compact | 0.03/0.07/0.26 | 0.08/0.30/0.89 | 0.32/1.25/3.5 | 2.2/**19.4**/24.2 | 1.0/**1.3**/1.1 |
| repack apply / tier move / snapshot create | 0.03/0.18/0.01 | 0.11/0.62/0.02 | 0.35/2.3/0.10 | 1.6/10.7/0.32 | 0.96/0.99/0.89 |
Byte ladder (4 MiB objects, 0.25/1/4 GiB, 4k/16k/64k chunks, exponents): stats 0.07→1.04 s (1.00), verify -deep 1.5→20.5 s (0.94 = **155–185 MiB/s of stored bytes, 1 core**), tier move 0.9→13.9 s (0.99), **compact 2.0 → 47.0 s (1.14; 81 MiB/s at 4 GiB)**, restart 0.024 s flat. History ladder (16 objects × 4 MiB, 1/4/16/64 versions): stats 0.02→0.41 s (0.7), verify -deep 0.32→16.4 s (0.95; 1.5 GB store, unique chunks grow 21×). Replay after maintenance verified byte-exact in every rung (`*_replay`, 0 mismatches).
Profiles (`mp_*`): `stats` = reachability scan (49%) + a second manifest pass in `computeStats` (15%) + `OpenStore` journal replay and `scanChunkFiles` (10%), 85% of one core; 64k-object `gc -apply`: 13.5 s wall for 6.4 CPU-s, `unlink` 32% of CPU, `lstat/ReadDir` 17%. Space: 64k small objects occupy 328,782 inodes and **3.28 GB allocated vs 2.55 GB apparent (+28%)**; 4 KiB PUT stores 1.215× logical (journal+manifest+block). **`gc -apply`, `compact`, `repack -apply`, `versions prune` refuse while `serve` holds the shared lock** (verified), so concurrency-during-maintenance is not available (PM-5); `stats/verify` do run alongside.

## 6. Production representations, kinds 1–12 (`reps.jsonl`, `reps_prof.jsonl`, `prof/rp_*`)
Creation is **inline in the PUT request** (`maybeBuildRep` → `repBuild`: analysis, encode, proof, reachability, root publish before namespace publication). Limits read from source: 1 GiB cumulative inspection (`repMaxInspect`), tar ≤65,536 entries, DEFLATE output cap 64 MiB / 32 Mi tokens, zstd window ≤16 MiB, managed/structured scratch 16–32 MiB, depth ≤3–4. Real inputs, 1 concurrent client, n = 3 (kinds 6, 7, 9-snappy/zstd, 10, 11, 12: n = 2; SmolLM2 n = 1). Slowdown = PUT MiB/s off ÷ shadow:
| Kind (input, logical MiB) | PUT off → shadow MiB/s | slow-down | srv CPU s | RSS MiB | phys/logical off → shadow | files off → shadow | GET whole prefer vs off | 4K / 1M range ms (off → prefer) |
|---|---|---|---|---|---|---|---|---|
| 1 tar (coreutils ×3, 178) | 114 → 14.3 | 8.0× | 1.5 → 12.8 | 16 → 21 | 0.96 → 1.46 | 1,682 → 19,487 | 558 vs 601 | 1.40→4.47 / 3.82→9.97 |
| 2 CAR (1.4) | 78 → 2.7 | 28.6× | 0.0 → 0.5 | 13 → 17 | 0.37 → 0.76 | 11 → 1,068 | 246 vs 270 | 0.59→1.41 / 0.98→1.03 |
| 3 SQLite (4 states, 122) | 95.6 → 18.0 | 5.3× | 1.2 → 6.7 | 16 → 18 | 0.88 → 1.21 | 1,424 → 12,152 | 602 vs 575 | 1.12→1.29 / 3.08→6.18 |
| 4 Git packs (3, 3.9) | 48.5 → 1.22 | 39.8× | 0.1 → 3.8 | 13 → 22 | 0.67 → 1.29 | 58 → 3,873 | 392 vs 393 | 0.47→0.49 / 2.37→2.35 |
| 5 Merkle (2 snapshots, 60.5) | 99.2 → **1.03** | **96.7×** | 0.6 → 54.4 | 15 → **192** | 0.74 → 1.43 | 729 → **156,951** | – (stopped) | – |
| 6 gzip (tar.gz ×3, 42.8) | 92.6 → **0.68** | **137×** | 0.4 → 65.2 | 16 → 36 | 1.00 → **4.71** | 673 → 30,492 | 511 vs 583 | 0.80→0.97 / 2.39→2.95 |
| 7 zstd (L3+L19, 16.5) | 57.7 → 1.01 | 56.9× | 0.2 → 15.4 | 14 → **193** | 1.00 → **5.42** | 261 → 9,433 | 531 vs 547 | 0.58→0.78 / 2.55→2.71 |
| 8 Model (Pythia-14M ×3 / SmolLM2 ×2) | 44.9 → 19.1 / 114 → 36.0 | 2.4× / 3.2× | 1.4 → 4.7 / 4.0 → 14.3 | 16 → 25 / 18 → **95** | 1.00 → **2.00** | 1,261 → 2,743 | 447 vs 466 / 773 vs 761 | 1.15→1.23 / 3.44→4.10 (1M: 3.33→3.45 / 6.55→6.89) |
| 9 SST (snappy ×4, 12.5 / zstd ×4, 5.7) | 92.0 → 1.70 / 55.1 → 38.3 | **54×** / 1.4× (zstd falls back, 1.00×) | 0.1 → 7.2 | 15 → 27 | 1.00 → **5.66** / 1.00 | 77 → 11,006 / 116 → 116 | 473 vs 507 / 405 vs 432 | snappy 0.72→**3.11** / 2.85→4.78; zstd 0.55→0.51 |
| 10 Parquet (snappy / zstd ×4, 47.8 / 33.3) | 113 → 4.07 / 99.7 → 1.66 | 27.8× / 60× | 0.4 → 16.2 / 0.3 → 26.3 | 16 → 40 / 15 → 51 | 0.49→1.07 / 0.56→1.46 | 365→7,429 / 272→7,403 | 537 vs 569 / 480 vs 546 | 0.71→0.85 / 0.59→0.78 (1M 2.45→2.50 / 2.21→3.08) |
| 11 DEX (F-Droid ×3, 26.6) | 73.1 → 3.18 | 23× | 0.3 → 8.6 | 14 → **116** | 0.92 → 1.41 | 288 → 2,604 | 437 vs 468 | 0.69→0.77 / 2.74→2.73 |
| 12 CLR (Newtonsoft ×5, 3.4) | 43.8 → 1.39 | 31.4× | 0.1 → 2.4 | 15 → 30 | 1.00 → 1.95 | 58 → 769 | 332 vs 313 | 0.56→0.63 / 1.87→1.87 |
Findings: (a) **rep cost = components × per-file CAS cost** — Merkle/SST/Git/CAR/Parquet create 60–216× more loose files, syscalls are 54–69% of CPU (Merkle 24.9 of 36.1 CPU-s); (b) gzip/zstd are algorithmic: gzip `repIRwindow.search` 38% + `insertTo/headGet` 24%; (c) additive storage 1.0–5.7× CDC, i.e. these paths cannot save space (B8); (d) deep verify of a rep store is 4–6× slower (Model 39.2 s vs 6.5 s; RSS 111 MiB); (e) `prefer` reads: tar 3.2× (4 KiB) / 2.6× (1 MiB), SST-snappy 4.3× / 1.7×, SQLite 2.0× (1 MiB only), others ≤1.4×; whole-object reads within ±15%; (f) memory is bounded but not small (zstd/Merkle 192–193 MiB, DEX 116 MiB, SmolLM2 95 MiB vs 17 MiB baseline); (g) store floors/fallback: unsupported or below-threshold inputs (SST-zstd here) pay only the probe (1.4×). One qualifying file does not generalize: SQLite is 5× while Merkle is 97×.

## 7. B1 and B2
**B1** (`research_b1.jsonl`, discovery vs encode from each run's own `EncodeMS/QueryMS/DecodeMS`; 3 reps, concurrent pinned job): encode MiB/s · query ms · decode ms · heap MiB: tar ×3 178 MiB (1,784 chunks) **77.5 s = 2.3 MiB/s** · 143 · 5,293 · 404 (RSS 629); SQLite ×4 122 MiB 21.9 s (5.6) · 39 · 2,494 · 283; DEX ×3 26.6 MiB 12.0 s (2.2) · 6 · 926 · 77; bitcode ×6 15.6 MiB 5.9 s (2.6) · 5 · 693; Parquet-zstd 33 MiB 2.9 s (11.6); SST-zstd 5.7 MiB 3.1 s (1.8); CLR 3.4 MiB 1.4 s; Git 3.9 MiB 0.64 s (6.2); Pythia 80.5 MiB 1.9 s (43, few deltas). **Discovery is 0.1–0.5% of encode.** Profile (tar): `simdelta.match` 44.9%, flate-L6 compression of candidates 23.7%, `anchorHash` 5.2%, SHA 5.4%, `Resolve` 5.8%, memclr 7.2%. Sorted-table index scale (4 postings/chunk, warm): 100k 0.18 s, 1M 1.72 s (70 MiB), **10M 14.9 s (621 MiB RSS, 530 MiB written)**; `hash-index` 1M 2.9 s (471 MiB); sort = reflection swapper 21%+. Fresh-process replay of every family exact. This is the single-index, 4-posting synthetic table from B1 — **not** a multi-index production system.
**B2 draft #97** (unmodified; `b2_econ.jsonl`, 1–2 processes/family, migrate = `AuthorityMigrate`; CPU profile of tar family over 9 variants: 1,006 CPU-s, 226 in migration):
| family (logical) | variant | phys/CDC | migrate s | RSS MiB | peak ×CDC | cold whole MB/s (CDC) | cold 4K µs (CDC) |
|---|---|---|---|---|---|---|---|
| tar ×3 (178 MiB) | archive-oldest (best-of-two) / delta-only / rep-only / hot-hybrid | 0.499 / 0.499 / 1.067 / 0.731 | **42.6 / 8.3** / 38.7 / 42.4 | 446 / 521 / 455 / 608 | **3.24 / 1.16** / 3.08 / 3.23 | 69 / 67 / 100 / 116 (138) | 2,447 / 2,561 / 4,544 / 1,352 (854) |
| CLR ×5 (3.4) | archive / delta / rep-only / hybrid / depth4 | 0.817 / 0.817 / 0.901 / 0.759 / 0.713 | 5.6 / **1.0** / 11.1 / 5.0 / 5.5 | 125 / 129 / 55 / 136 / 187 | 4.00 / 1.61 / 3.39 / 3.89 / 3.90 | 21 / 37 / 4 / 51 (89) | 3,091 / 3,006 / **165,942** / 1,845 (1,008) |
| DEX ×6 (54) (n=1) | archive / delta-only / rep-only | 0.489 / 0.841 / 0.489 | **158.8 / 2.4** / 85.5 | 173 / 467 / 201 | 2.17 / 1.48 / 2.17 | 8 / 52 | **1,043,479** / 2,504 |
| SQLite ×4 (122) | archive / delta / rep-only / hybrid | 0.295 / 0.295 / 0.404 / 0.554 | **27.0 / 6.2** / 24.4 / 26.2 | 267 / 286 / 211 / 396 | 1.47 / 1.02 / 1.46 / 1.46 | 86 / 91 / 66 / 80 (99) | 1,185 / 1,343 / 500 (amp 7,814×) |
| Parquet ×4 (48) | archive / delta / rep-only / hybrid | 0.476 / 0.476 / 1.000 / 0.647 | **20.9–22.4 / 2.3** / 17.3 / 21.5 | 196 / 189 / 106 / 239 | **2.38 / 1.04** / 2.34 | 107 / 106 / 140 (134) | 982 / 1,175 / 516 |
| Git ×3 (3.9) | archive / delta / rep-only / hybrid | 0.582 / 0.582 / 1.000 / 0.857 | 5.9 / **0.3** / 5.3 / 5.8 | 204 / 154 / 39 / 109 | 2.13 / 1.19 / 1.94 / 1.96 | 120 / 175 / 351 (303) | 587 / 500 / 134 (146) |
SmolLM2 (3 files, 685 MB; `b2_smollm2.jsonl`): CDC 6.3 s; additive kind 8 **1.996× (16.7 s, RSS 808 MiB)**; authority: 1.0000 — the two 269 MB files are refused ("size outside admission window", 256 MiB), the Q8_0 file admits and gives a **2.0× migration peak with 0.0% saving**; migrate RSS 381–863 MiB. Diagnostic variant (B8 shared-chunk accounting patch, separate scratch worktree, never applied): Parquet rep-only 1.000 → 0.837 but at 44.8 s, 12 MB/s whole and **929 ms per 4 KiB**; Git rep-only 1.000 → 1.049 — the defect matters only for rep-only, not for the archive policy (delta already wins). Source profile (tar, migration): `decodePackPayload`/flate 114.6 s (11% of all CPU, 48% of migration; sources live in DEFLATE-compressed packs), `tarPrescan` 64.4 s, `authProve` 33.9 s, `xdEncode` 30 s. Recovery: reachability with bodies 0.8 ms, deep verify 0.11 s (CLR). Blast radius (CLR hybrid) max 5 contents/chunk, mean 2.4.

## 8. B3 and B4
**B3** (`research_b3.jsonl`, 5 pinned SmolLM2 files, 1,495 MB; concurrent job): CDC baseline 35.1 s; ingest identity 28.4 s (RSS 1.04 GB), semantic 85.1 s (**2.55 GB**), full **104.7 s = 14.3 MB/s, RSS 1.97 GB** (B8: 87–91 s, 1.95 GB); fresh-process replay + independent SHA-256 18.1 s, all 5 exact; replay RSS 1.08 GB. Profile: `hash/fnv.(*sum64a).Write` **29.1%**, `semcas.DiffPatches` 16.2%, `convert` 16.3%, SHA-256 11.4%, `permute` 10.9%, `quantQ8` 7%. Heap at end: 767 MB of 776 MB live = `os.ReadFile` whole files (largest ONNX 540 MB held) + GC headroom (GOGC 100 ⇒ RSS ≈ 2× live). Ranges (cold, per file): safetensors 1.7 s, F16 GGUF 2.4, Q8_0 3.7, FP16 ONNX 5.4, **ONNX 14.1 s** (permuted layout, RSS 1.07 GB). Other lanes: Pythia-14M ×3 (80 MB) 2.1 s; tar ×3 (178 MiB) 8.5 s (RSS 616 MB); Git packs ×3 (3.9 MiB) 4.6 s (0.86 MiB/s); Parquet-zstd ×4 (33 MiB) 19.7 s (1.7 MiB/s); **tar.gz exact DEFLATE analysis 48 s for 29.6 MB gz (0.6 MB/s)**, profiled single file 27 s. Streaming/windowed alternatives: ReaderAt windows for ST/GGUF (header-driven), ONNX protobuf streaming; would cut live heap from whole-file to O(largest tensor) ≈ 20–100 MB — saves the 767 MB live component and the GOGC doubling; CPU unaffected.
**B4** (`research_b4.jsonl`): SQLite O2 ×6 (15.6 MiB) `llvm` **25.1 s (0.62 MiB/s)**, 297 MiB RSS (quiet recheck 19.8 s); single 2.62 MiB module: parse + exactness proof 232 ms (11.3 MiB/s), SCC identity 23 ms (10,779 nodes, 2.1 µs/node), encode with per-function proofs 607 ms (**4.3 MiB/s**), VM decode 110 ms (23.8 MiB/s), VmHWM 185 MB; synthetic DAG 100k nodes: SCC 187 ms (+66 MiB), renumber-match 300 ms; **1M nodes: SCC 2.49 s (+701 MiB = 0.7 KB/node), match 5.9 s (2× per-node degradation)**; hostile 20k-cycle 2.4 s bounded. PDB ×5 1.2 s (35 MiB); DEX ×3 28.6 s (474 MiB); CLR ×5 3.9 s (72 MiB). Profile (`llvm`): `simdelta.match` 35%, flate 13%, `BitWriter.Bits` 5%, SHA 2.8%; (`perf`): graph `Match` 31%, `nodeHash` 19%, Tarjan 9%, map ops/GC ≈ 20%. **The "2.5–7 MiB/s" is the shared delta matcher plus compression, not the VM or proofs (proofs ≈ 0.6 s of 25 s)**; fresh-process replay (`llvm-replay`) 2.2 s, independently verified.

## 9. B5 and B6
**B5** (`research_b5.jsonl`; ≤0.54 GB checkpoints; no Qwen-scale probe — extrapolation: costs are linear in bytes by construction [RSS ≈ live whole-file + container], so a 1 GB model needs ≈3.5–4 GiB RSS): `coderbench` Pythia-70M step16000 vs 4000 (2×282 MB) **53 s (10.6 MiB/s)** RSS 740 MiB; SmolLM2 instruct vs base 47.6 s (RSS 705 MiB), shakespeare vs base 23.5 s, unrelated v1 vs base 42.5 s (40.6 user-s, writes nothing: **a no-benefit pair costs as much as a hit**). `family` (pool3, 5×269 MB): **293 s = 4.6 MiB/s**, RSS 1.69 GB; replay 27.7 s RSS 2.0 GB, independently verified; ranges (60 reads) 27 s; Pythia-70M prev policy (3×282) 61.4 s RSS 1.57 GB. Profile (`coderbench`): `tdelta.pxr` (PXR comparator, bit-serial) 57%, `ir.decoder.decode` 29%; NRC-side `bprle`/`NRCDecode` ≈ 15%. Profile (`family`): **SHA-256 30%** (76 s), `nrcEstimate`+`nrcEncodeCfg` 22%, `fpeEncode` 5.6%, flate-L6 10%, `MakeResidual` 5.6%; live heap `bytes.growSlice` 513 MB + `Container.Marshal` 190 MB. Direct vs best-single-base vs multi-base economics are B5's own results (bytes saved) — P0 adds that the **encode cost is per pair, independent of benefit**, so candidate pruning before NRC search is the economic lever. Large tensors: reads are whole-file (`replay` RSS 2.0 GB).
**B6** (`research_b6.jsonl`; Go bench `lz4z3`, SST/Parquet fresh-process pack/unpack): `BenchmarkDecode` **231 MB/s (1 CPU) but 70 MB/s on 4 CPUs** (12.2 MB allocated per op ⇒ GC-bound), `ReplayPredicted` 123 vs 68 MB/s, `ReplayExplicit` 367 / 393 MB/s, **`ReplayHC9` 5.0 / 4.6 MB/s**, miss-sweep analysis 85 GB/s (nominal). RocksDB LZ4 SST ×4 (pack+unpack+sha256sum) 2.9 s vs Snappy 2.7 s; Parquet LZ4_RAW ×4 (50 MB) 10.0 s vs Snappy 11.3 s (parser/page decode ≈ comparable, recipe replay not slower than the existing path); profile (`lz4z3` bench): `Recipe.Replay` 59% = `Predict` 38% (re-running the encoder: `fastSeqs` 34%, `BuildH` hash-table build 22%) + HC `wider` match search 24%; `Decode`/`ReplayPredicted` allocate 12 MB/op. Not measured: SquashFS-LZ4 (tool absent), LZ4HC levels >9 and Brotli (separate missing capabilities).

## 10. Integrated workloads (production store, real server+CLI; `integrated.jsonl`; run on 2 vCPUs while B2 used the other 2; n=1 per workload; replay verified in every case)
| Workload | mode | ingest s (logical → store MiB) | prune+gc+compact+repack s | vdeep s | final MiB | read 64 KiB p50 ms cold → packed | whole read p50 ms cold → packed |
|---|---|---|---|---|---|---|---|
| Versioned archive (8×24 MiB × 8 revs, keep-last 2) | off | 10.8 (209 live, 1,536 written → 524) | 0.01+0.65+4.62+0.02 | 2.35 | **341** | 1.56 → 1.05 | 69 → 38 |
| Artifact registry (500 mixed objs ×2 revs, 20% dup) | off | 22.5 (1,088 → 1,475) | 0.01+0.29+18.65+0.17 | 8.51 | 1,477 | 1.27 → 0.98 | 1.6 → 1.2 |
| Artifacts (tar/DEX/CLR/Git, 212 MiB) | off / **shadow** | 3.2 / **25.4** (→ 201 / 310) | 0.04+5.6 / 0.69+17.5 | 3.27 / **10.86** | **58 / 96** | 1.08 → 2.08 / 1.13 → 1.91 | 34 → 112 / 32 → 100 |
| Database history (SQLite+Parquet+SST, 161 MiB) | off / **shadow** | 2.4 / **26.9** (→ 131 / 202) | 0.04+4.6 / 0.60+12.0 | 2.77 / 10.90 | 113 / 166 | 1.21 → 1.69 / 1.02 → 1.56 | 12.8 → 18.4 / 10.3 → 16.7 |
| Model repository (Pythia-70M ×3 + SmolLM2 ×2, 1,319 MiB) | off / **shadow** | 13.1 / **61.0** (→ 1,323 / 2,644) | 0.2 / 0.5 | 6.49 / **39.16** | 1,323 / **2,644** | 3.23 → 2.83 / 3.00 → 2.86 | 341 → 353 / 391 → 391 |
| General S3 (1,500 mixed objs, 10% deleted) | off | 22.4 (→ 1,626) | 0.37+23.2+0.12 | 9.54 | 1,628 | 0.82 → 0.69 | 1.3 → 1.15 |
Direct data-path vs maintenance vs representation analysis: in the **off** column ingest is a few seconds, `compact` (23 s on 1.6 GB, 70 MiB/s) and deep verify are the heaviest lifecycle steps; representation analysis adds 8–11× ingest time and 1.5× stored bytes before packing (artifacts, database; 1.65× after) and 2.0× (models) *with zero storage saving*, while packing alone shrank artifacts 201 → 58 MiB and the archive 524 → 341 MiB (history prune + gc). Research-only comparison for the same families: B2 physical size is 0.30–0.82× of CDC (§7) but needs the §7 time/peak; B1 authoritative/CDC bytes: tar 0.55, SQLite 0.31, DEX 0.78, Git 0.61. None of this makes any research engine production-ready.

## 11. CPU / memory / I/O bottleneck register (full table with approaches, risk, difficulty, confidence: [`BOTTLENECK_REGISTER.md`](../z4/p0/BOTTLENECK_REGISTER.md))
Ranked by (user-visible impact × breadth × leverage ÷ risk). Gains are Amdahl-style estimates [E], not promises.
| # | ID | Bottleneck (code path) | Evidence | Est. end-to-end gain | Risk | Batch |
|---|---|---|---|---|---|---|
| 1 | PW-1/2 | per-chunk file+fsync+mkdir+rename, global `casPubMu` | tmpfs 4–6×; 6.2 fsync/4 KiB PUT; mutex 16.5 s/4.6 s | 2–4× small/medium PUT; unlocks #5, #11 | durability/crash matrix | **P1B** |
| 2 | PW-3 | serial 5×SHA-256 + MD5 + CDC in one goroutine; pack read-back re-hash | 48/24/11% of 3.9 CPU-s/512 MiB; 109 MiB/s | 2–3.5× single-stream large PUT | low | **P1A** |
| 3 | PR-1 | manifest read+JSON parse+SHA per GET, O(chunks) | 0.42 → 9.3 ms for 4 KiB range; 50% CPU | ≥15× on GiB objects; 1.2–2× elsewhere; −GC | low | **P2** |
| 4 | PM-1 | serial full-manifest scans in every tool; `stats` reads twice | 5.2–11.8 s at 64k objects | 3–4× (parallel) → >50× (cached summary) | summary invalidation | P3 |
| 5 | PX-1/2 | inline rep build; component fan-out 67–216× | 2.4–137× PUT slowdown | PUT ≈ unchanged; rep creation 5–30× faster | admission/queue | P4 |
| 6 | R2-1 | B2 best-of-two + decode-3×-per-source | 4–10× time, 2–3× peak for equal bytes | 4–10× migration, peak ≤1.2× | policy | P5 |
| 7 | R1-1 | B1/B4 shared delta matcher + per-candidate compress | 45% + 23% CPU; 2–6 MiB/s | 3–6× | low | P5/P6B |
| 8 | R3-1 | B3 FNV-1a per byte, whole-file reads | 29% CPU; 1.97 GB RSS | 1.4–2× CPU, ~2.5× less RSS | low | P6A |
| 9 | PX-3/R3-2 | exact DEFLATE search (3 copies) | 0.55–0.68 MB/s | 3–10× | proof-preserving | P4/P8 |
| 10 | R5-1 | B5 re-hash 30% + NRC search 22%; no-benefit pairs cost full | 4.6 MiB/s family | 1.5–2.5× | medium | P7 |
| 11 | PM-2/3 | unlink-bound gc, serial deep verify/compact (exp. 1.14) | 19.4 s gc; 81 MiB/s compact | 2–4× | low–med | P3 |
| 12 | PR-2 | whole-chunk fetch/verify/decompress for small ranges | 23–46× bytes; DEFLATE pack 3× slower | 2–4× 4 KiB ranges | integrity model | P2 |
| 13 | R6-1 | HC9 replay 5 MB/s; Decode GC-bound (12.2 MB/op) | 74× vs explicit | 3–10× HC; 2–3× decode | low | P7 |
| 14 | PM-5 | maintenance needs exclusive store (downtime) | refusals verified | availability, not speed | design | P3 (investigate) |
| 15 | PR-4 | GET plateau at 2/4 cores, no lock cause | unresolved | unknown | – | P2 (diagnose first) |

## 12. Algorithmic and architectural redesign opportunities (≥2 credible approaches where uncertain)
- **Storage layout for ordinary writes (PW-1/2, PM-2, PX-2).** (A) *Group-commit the existing loose layout*: one fsync set per batch across concurrent PUTs, deferred/shared directory fsync, lazy two-level fan-out — no format change, bounded win (tmpfs ceiling shows the limit is syscalls, not data). (B) *Log-structured chunk segments for sub-64 MiB writes*: reuse pack-v1 records appended to an open segment with one fsync per group, publish chunk locators in the in-memory index, compact later — removes file-per-chunk (inodes 328,782 → ~6k per 64k objects), 4–6× upside, and also fixes gc unlink cost and representation fan-out. (B) needs a durability/crash model and a read path over open segments; A is the safe first step. Evidence to choose: tmpfs vs disk delta shrinks by how much after A.
- **Ingest compute (PW-3).** (A) pipeline stages on goroutines with bounded channels (CDC → {chunk SHA, object SHA, MD5, pack hash}); (B) fuse hashes in one pass over cache-resident 256 KiB windows using multi-buffer SHA/AVX-512 MD5. A is simpler; B only if A stalls on memory bandwidth. Remove or defer `verifyPackFile`-style read-back on direct pack (2 of 5 passes).
- **Read metadata (PR-1/2).** (A) immutable manifest LRU keyed by manifest UUID with verify-once; (B) side index (offset→chunk, binary) written at commit so ranges never parse the chunk list. Sub-chunk integrity (4 KiB blocks under a Merkle root) cuts amplification but changes the integrity model — investigate after A.
- **Metadata tooling (PM-1).** (A) parallel manifest scan with a shared scan result across phases of one command; (B) journal-sequence-keyed persisted reachability summary. A first (no new state).
- **Representations (PX).** Build off-request from a durable queue; coalesce components per object; share one exact-DEFLATE search with B3 and the lab.
- **B2/B1/B4.** Estimate-then-commit planning (cheap delta cost proxy before any rep staging); decode-once source windows across plan/build/prove; compress only the winning delta; reuse anchor tables across candidates.
- **Lab hygiene (cheap, large):** replace FNV-1a with a vectorizable/xxh3-class hash, reflection sorts with `slices`, size `bytes.Buffer`s, set `debug.SetMemoryLimit`.

## 13. Expected benefits and confidence
High confidence (mechanism and share both measured): PW-3 (2–3.5×), PR-1 (≥15× on large objects), PM-1 parallel scan (3–4×), PX-1 (PUT back to baseline), R1-1/R2-1/R3-1 shares. Medium: PW-1/2 group-commit magnitude (bounded by 4–6× tmpfs ceiling but real devices may differ), PM-2/3, PR-2, R5-1, R6-1, B4 matcher. Low: PR-4 (cause unknown), PM-4. Combined effects are not additive: after PW-3, PW-1 becomes the large-PUT limit; after PR-1, per-chunk SHA and GC (8–18% CPU) and read amplification dominate; after PM-1, `gc` unlink and deep-verify hashing dominate.

## 14. Correctness, regression and reproduction status
- Production `zeros3.go` and `go.mod` byte-identical to `origin/main` (SHA above); P0 files only under `testing-harnesses/` (+ one z3 helper `cmd/z4p0merkle`). Profiling overlay is `go build -overlay` only.
- `scripts/validate.sh normal` on this branch: format, modules, **full root tests (399 s)**, s3 (162 s), sync (77 s), reproducible build **PASS**; stage `static` **FAIL — pre-existing on clean `origin/main` `a066363`** (`toc_check`: 23 of 40 entries of the `zeros3_test.go` test-map disagree with the file; documentation drift, unrelated to P0). Race: `go test -race` of the CAS-batch/direct-pack/multipart/concurrent-put/snapshot/conditional-put/rep-concurrency set **PASS (110 s)**. Z3 research modules (`simdelta semcas graphnorm mdelta2 lz4z3 sstz3 parquetz3 planner2 tdelta`) **PASS**; B2 draft's own economics harness exactness checks (`Exact:true` for every read row) held.
- Every measured PUT/GET/readstore result is byte-verified; replay of maintenance rungs verified; B3/B4/B5 fresh-process replay verified with independent Python hashing. Failures preserved: first-attempt harness argument errors (`put_chunked` unsigned trailer rejected by the server — mode replaced by signed `aws-chunked`; `b3_replay_PROF` missing output dir; `b4_verify_replay` wrong originals arg; B1 `-quality/-baseline-ranges` missing `-out`) are in the JSONL as failed rows with `*_fixed` reruns; one B2 rep killed to cut time (rows duplicated, flagged); an earlier strace attempt produced empty files and was redone with a TERM-forwarding wrapper.
- Not measured / limits: real-device `fsync`; `zeros3 sync/replicate/repair/bundle` throughput (BENCHMARKS.md has 21–35 MiB/s historical bulk-transfer figures, not re-measured); SquashFS-LZ4; Pebble SST; Qwen-scale models; tier rebalance; maintenance rungs are single-run; n=2 for several representation kinds (disclosed per row); the ≈half of reps and all research/B2 runs shared the VM with a pinned job (0–21% effect, §2); client and server share the 4 vCPUs (client ≈1 core at c≥8, reported as total busy cores in JSONL).
- Reproduce: [`../z4/p0/README.md`](../z4/p0/README.md). A stray empty directory `/testing-harnesses/` was created outside the repo by a mis-sourced helper; it holds nothing and removal was left to the user.

## 15. Refined P1–P9 sequence (evidence changes the order and splits two batches)
| Batch | Scope after P0 | Why changed |
|---|---|---|
| **P1A** | PUT compute pipeline: parallel hashing stages, remove pack read-back re-hash, MD5/SHA fusion, SIMD CDC | isolated, no format change, 2–3.5× large PUT |
| **P1B** | PUT durability path: group commit, `casPubMu` sharding, shard fan-out, log-structured small-chunk segments, multipart direct pack | dominant for all objects <64 MiB; prerequisite for P3 gc and P4 |
| **P2** | read path: manifest cache/index, allocation pooling, small-range amplification, GET plateau diagnosis | O(chunks) per request; very cheap, large win |
| P3 | maintenance: parallel + cached scans, gc/unlink, deep-verify/compact parallelism, online maintenance feasibility | 80–160 µs/manifest serial; exclusive-lock downtime |
| P4 | production representations: off-request creation, component coalescing, shared exact-DEFLATE | 2.4–137× PUT penalty; depends on P1B |
| P5 | B1 delta encoder + B2 estimate-then-commit planner (split **P5A** B1 encoder, **P5B** B2 staging/decode-once) | different code, different owners; B2 stays draft until Z4-C1 gates |
| P6 | **P6A** B3 (hash, streaming), **P6B** B4 (shared matcher, graph arenas) | B3 memory/CPU fix is independent of B4 |
| P7 | B5 hash/NRC search + candidate pruning; B6 HC replay and GC-bound decode | |
| P8 | cross-engine: shared delta matcher, shared exact-DEFLATE, shared windowed ReaderAt cache | three duplicated algorithms |
| P9 | integrated qualification incl. **real-device** (NVMe/SATA/network) fsync matrix and quiet dedicated hardware | this VM's fsync is cheap |

## 16. Exact recommended next batch — **P1A + P1B-step-1 as one "Z4-P1 core write path" batch (two independently mergeable commits)**
Order: (1) P1A pipeline (no format change). Acceptance: direct-pack 64 MiB single stream ≥250 MiB/s on this VM or the sequential-write ceiling (calib 182–371 MiB/s) — whichever is lower — with CPU/byte ≥2× lower on the critical path; 1 GiB PUT RSS ≤ 40 MiB; all PUT/GET byte-exact; `go test -race` CAS/direct-pack set and crash matrix green. (2) P1B step 1 (group commit + `casPubMu` sharding + shard-dir batching, no format change). Acceptance: 1 MiB c4 ≥ 2× (≥135 MiB/s) and 4 KiB c8 ≥ 2× (≥1,300 ops/s) on disk with fsync count/object ≤ 2; tmpfs gap reported before/after; run on the §2 VM **and** on one real block device; unchanged crash-matrix results. Stop and re-plan if step 1 yields <1.3× (then the log-structured segment design, which needs its own crash model, becomes the batch). Include `P0` rerun scripts as the regression gate (`run_put.sh`, `run_prof.sh`, quiet `drift` set). Do not start P2/P3 until these gates pass; the P2 manifest cache is small enough to fold into the same release if capacity allows, but it is measured and gated separately.
