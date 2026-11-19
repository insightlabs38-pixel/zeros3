# Z4-P8 — cross-engine algorithmic performance convergence

Base `5d63897` (C1/T1 merge). Branch `z4/p8-cross-engine-performance`. Production stays `zeros3.go`, standard library only; no store-format, dependency or default change.
Evidence: [`../z4/p8/`](../z4/p8/README.md) (raw rows, `results/TABLES.md`, driver). Machine: 4 vCPU Firecracker VM, Go 1.27.0; baseline and P8 are test binaries built from the same tree layout, same corpus (hashes in `ECON_MANIFEST.sha256`), one fresh process per family.

## What actually dominated (profiles, not P0's historical shares)
* Current main's windowed delta (objects > 32 MiB) over a *delta* base re-decoded a whole 1 MiB frame per 64 KiB base page (single-window cache) and its cold read probes did the same: coreutils tar history **1152.6 s, 2045 CPU-s, 72.9 GiB read**.
* In-memory migration: 40–70 % of CPU was DEFLATE inflation of pack records, re-reading every base once per target (9 source decodes for 4 objects), compressing every candidate at L6 plus again at L1, and rebuilding the anchor index per candidate set.
* The matcher kernel was not the bottleneck in production (`xdMatch` 9 %); index build (cache-missing table inserts) is ~75 % of a similar-pair match.
* Exact DEFLATE: `repIRwindow.search` 58 % flat; unit encode and unit decode (the build proof) are independent given the plaintext.

## Changes
| Area | Change | Engines |
|---|---|---|
| Decoded sources/windows | `authWinCache`: byte-bounded LRU keyed (content SHA-256, record generation, window), single-flight, immutable entries, owned by one migration. Windowed bodies, in-memory sources and rep construction read through it. Proofs never use the shared cache; a proof of a windowed body keeps a private window/chunk/frame cache for that one proof (every chunk still read from disk and SHA-verified by that proof). | B2/production, ZN7 planning readers |
| Delta selection | single-base indexes cached per pass; all candidates matched concurrently; staged estimate (`dwDcoreEstimateProgram`) and only candidates whose optimistic bound can still win are compressed (5 % slack); multi-base is a *residual* match of the best program's literals against the next bases (the old joint [0,1]/[0,1,2] sets never won on these families); envelope SHA reused; pooled/len-only flate; sketch computed on slices and merged (identical values, it is persisted). | production `xdEncode`/planner |
| Matcher core | `xdMatch` replaced by the shared dcore index matcher (deterministic, 2^20 anchors); region-ordered table inserts; **deterministic** windowed index build (was scheduling-dependent); same fix in the numcore copy. | production, B1 (`simdelta`), B4, ZN7 |
| Read probes | cold 4 KiB probes drop from 8 to 2 when no read-amplification/tier gate consumes them. | production |
| Exact DEFLATE | bounded unit pool with in-order sink delivery for encode (probe units are barriers) and decode-ahead for replay/proof; whole-stream `Encode`/`Decode` (B3/lab) likewise; output byte-identical for any worker count (tests). Ported mechanically (`prodcodecport`) into `zeros3.go`. | production kind 6, B3, lab `ir` |

## Results (medians; baseline → P8; `results/TABLES.md`)
| Workload | Wall | CPU | Physical bytes after gc/repack | Exact |
|---|---|---|---|---|
| tar (3 coreutils, windowed delta) | 1152.6 s → 17.8 s (**65×**) | 2045 → 30 s | +0.50 % | yes |
| SQLite ×4 delta / default policy | 5.56 → 1.73 s (3.2×) / 10.5 → 3.3 s (3.2×) | 6.8 → 2.7 | −0.69 % | yes |
| Parquet-Snappy delta / default | 2.22 → 0.78 s (2.8×) / 10.5 → 6.1 s (1.7×) | 2.7 → 1.2 | −1.05 % | yes |
| Parquet-Zstd delta | 1.62 → 0.78 s (2.1×) | 1.8 → 1.0 | −0.06 % | yes |
| DEX delta / default | 5.20 → 3.00 s (1.7×) / 60.1 → 10.2 s (5.9×) | 82.6 → 11.5 (default) | −1.06 % | yes |
| CLR delta / default | 0.89 → 0.42 s (2.1×) / 2.44 → 1.84 s (1.3×) | 0.9 → 0.5 | −0.75 % | yes |
| gzip L6 / L9 exact-DEFLATE build (analysis + proof) | 9.9 → 3.1 s / 9.8 → 3.0 s (**3.2×**) | 10.1 → 10.6 (same work, spread over cores) | n/a | yes |
| gzip replay | 4.7 → 1.5 s (3.2×) / 4.8 → 1.35 s (3.6×) | | | yes |
Source bytes decoded per migration: SQLite 273 → 122 MiB, parquet 107 → 48, dex 106 → 52 (each source once). Allocation volume −60…−99 %. Peak RSS: −15…+0 % on clr/parquet-zstd, **+10…+50 %** on sqlite/dex/tar (caches: ≤160 MiB sources + 48 MiB indexes, bounded by budget, not by input size); tar 545 → 766 MiB.
Matcher A/B (match only): program size −30 % tar, −11 % SQLite, −9 % Parquet-Snappy, −2…−6 % DEX, ≈0 Parquet-Zstd; **speed ≈ the old matcher** (0.7–1.4×; small CLR slower). The kernel was not made faster; end-to-end wins come from the passes above.

## Targets not met / unsuccessful approaches
* Matcher kernel 2×: not met. A cyclic-polynomial rolling hash removed a 5-op dependency chain but changed neither index-build nor match time (inserts are cache-miss bound); region-ordered inserts gave 1.25× on index build only. Reusable per-base *anchor lists* were built, measured equivalent to single-base index reuse for sets of one and dropped (multi-base unions never won).
* Exact-DEFLATE CPU: unchanged (search is latency-bound; hoisting gave 3 %); the 3.2× is wall time from parallel units. Profiles other than the 13 shipped (e.g. an exact zlib `deflate_slow` emulator) were not attempted.
* First cache-only tar run: 229.6 s; proof caches + fewer probes: 43 s → 26.6 s → 17.8 s (`intermediate_*` rows).
* tar physical bytes +0.5 % (added +1.7 %): different anchor sampling in the windowed matcher; not traded for speed deliberately, not recovered.
* Cold random 4 KiB reads of nested windowed deltas still cost ~1 s each (format property, not changed).
* B1/B4 already used dcore (P5); they gain the deterministic index and cheaper inserts only. Large-object (>256 MiB) windowed paths were exercised by tests and the C1 large steps, not re-benchmarked here.
