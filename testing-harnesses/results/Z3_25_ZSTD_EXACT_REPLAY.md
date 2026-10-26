# Z3-25 — exact Zstandard parse/replay and reusable reconstruction substrate (lab)

**Question.** Can dictionary-free Zstandard be split into reusable semantic plaintext plus small exact reconstruction metadata, and does that beat
production CDC on the Z3-23 `tar+zstd` failure cases (R3/R4/R5/R7/R10: 41–61 MiB rewritten against a 0.2–3 MiB floor)? Production untouched; starting main `31623ee`.
Costs: incremental bytes under the Z3-23 convention (pack rule on new chunks, 96 B per CDC chunk reference, 36 B per native reference), so rows A/B reproduce Z3-23.

## Decision: **PROMOTE** (keep `zstdz3` as a reusable substrate; not a production authorisation)
Exact replay is byte-identical for every producer tried and removes **87 %** of the 238 MiB R1–R10 CDC excess on the Z3-23 chain (27.3× → 4.4× floor).
The reconstruction tax is real and content dependent: 9 % of the stream on the synthetic layer, **26 % on a real binary-heavy Go release layer**, where two
revisions do *not* repay the +41 % first-sight premium. Admission must therefore be selective (see Limitations).

## What exists
`zstdz3` (stdlib only, no lab import): strict frame/block parser (`Scan`), full decoder (raw/RLE/compressed; four literal modes, 1/4 streams, direct and FSE Huffman
weights; Predefined/RLE/FSE/Repeat sequence tables; recent offsets incl. zero-literal-length codes; sliding window; XXH64), `Parse` → plaintext + canonical skeleton + unit
stream, `Replay`, `ReplayFrom` (checkpoints), canonical `Marshal/Unmarshal`. Dictionary ids, legacy and magicless frames are rejected explicitly. `repz3.KindZstd` is a thin
adapter (new enum value only; no contract change). `frontier.ScanZstd` is now a view over `zstdz3.Scan` (one frame parser).
**No payload retention:** literal Huffman streams and the FSE sequence bitstream are re-encoded from plaintext + decisions; table descriptions (≤ a few hundred B/block) are
kept verbatim and charged. Every transformed block is re-encoded and compared to the original while parsing; a block that differs becomes a counted **fallback** (original bytes
as a unit). Fallbacks seen across every stream below: **0** (a forced odd-count-form fixture proves the path, state tracking continues after it).
**FSE uniqueness (measured):** bitstream = f(symbols, extra bits, final-state rank). Rank is the only free choice; zstd 1.5.5/1.5.7 and klauspost chose rank 0 in all 4 313 blocks
checked (0.7 B/block as varints). **Sequence model (the one predictor):** match length = longest extension of the source (100 % of sequences), offset = rank among earlier
4-byte-equal positions nearest first (56 % rank 0, order-0 2.2 bits), escape to raw; literal lengths and repeat codes explicit; every record is decoded back at encode time.

## Z3-23 chain, zstd 1.5.5 `-3 -T1` (MiB; R1–R10 excludes first sight and the unrelated R11)
| representation | R0 | R1–R10 | cum | ×floor R1–R10 | ×floor cum | metadata (R0 / R1–R10) |
|---|---|---|---|---|---|---|
| FLOOR (oracle) | 75.5 | 9.1 | 146.5 | 1.0 | 1.00 | – |
| A production CDC of tar.zst | 70.9 | 247.5 | 379.0 | 27.3 | 2.59 | – |
| B native zstd blocks | 70.9 | 291.1 | 422.6 | 32.1 | 2.88 | – |
| C exact-zstd + plain-tar CDC | 83.4 | 71.0 | 225.3 | 7.8 | 1.54 | 6.7 / 28.9 |
| **D exact-zstd + structural tar** | 82.1 | **39.7** | **187.4** | **4.4** | **1.28** | 6.7 / 28.9 |
| F reconstruction metadata alone | 6.7 | 28.9 | 39.0 | 3.2 | 0.27 | skeleton 0.04/rev |
Per revision A → D: R3 41.4 → 4.1, R4 56.8 → 5.9, R5 60.7 → 9.8, R7 11.8 → 1.8, R10 56.7 → 6.9; R1 0.43 vs 0.78 and R6/R9 ties favour CDC (hindsight min(A,D) 39.3). R11 (unrelated):
D 65.5 vs A 60.6 vs floor 61.9 — no false reuse. **First-sight premium** +11.2 MiB (+15.9 %), repaid by R3 alone (cumulative A−D: −11.6 → +27.0). Real `repz3` contract (KindZstd over
plain-CDC / tar-with-CDC-bodies child, reconstruction proved at R0/R3/R10/R11): baseline CDC 378.7 cum, zstd→CDC 224 (same as C), **zstd→tar 193.4**.
**Remainder (30.6 MiB over floor in D):** 28.9 reconstruction metadata + 1.7 tar structure. Metadata is *not* shiftable by finer units (CDC ≈1–64 KiB and native blocks all give 42–44 MiB):
the producer's decisions are chaotic against small input changes (R3 re-pays 56 % of R0's units). Fundamental without encoder emulation; improvable ≈40 % by entropy-coding the
decision fields (order-0 estimate 3.6 vs 6.7 MiB at R0) and by modelling rep-code lengths (24 % of real sequences).

## Metadata breakdown (bytes; IR packed = skeleton + units through the production pack rule)
| stream | compressed | frame/tables | sequence units | rank residual | IR raw (%) | IR packed (%) |
|---|---|---|---|---|---|---|
| OCI R0, zstd 1.5.5 -3 (synthetic) | 74 252 192 | 67 207 | 18 814 301 | 2 763 | 25.4 | 9.1 |
| OCI R0, klauspost level 2 | 74 107 272 | 53 901 | 18 574 758 | 4 329 | 25.1 | 9.6 |
| Go 1.27.0 tar, zstd -3 (real content, local re-export) | 67 508 935 | 252 697 | 36 786 782 | 5 847 | 54.9 | **26.1** |
| text tar 63.7 MB, zstd 1.5.5 -3 | 13 086 579 | 36 604 | 9 855 780 | 1 458 | 75.6 | 25.8 |
| same, zstd 1.5.7 -19 | 10 417 327 | 35 241 | 11 796 822 | 1 563 | 113.6 | 73.4 |
| same, zstd:chunked (14 266 frames + skippable) | 17 122 517 | 1 463 729 | 6 402 569 | 42 069 | 46.2 | 23.6 |
Real family (go1.27.0 → 1.27.3, SHA-256 checked, **real content / synthetic local re-export**): CDC of tar.zst 67.6 → 67.3; exact + structural tar 95.6 → 55.2 (floor ≈ 37). Net loss over two
revisions (150.8 vs 134.9); gain 18 % on the second.

## Producer matrix (text tar, 63.7 MB; all single-thread unless noted; dec=ref SHA equals zstd 1.5.7 -d; fallback 0.00 % everywhere)
| producer | setting | parse / decode / exact replay | IR packed % | new IR vs new CDC after z155l3 |
|---|---|---|---|---|
| zstd 1.5.5 / 1.5.7 | -3 | ok / ok / exact | 25.8 | 3.3 vs 12.5 MiB (1.5.7 reuses 0 % of CDC and ≈0 % of units; plaintext 100 %) |
| both | --fast=3, -1 | ok / ok / exact | 13.1, 19.9 | 2.7 vs 16.1; 2.8 vs 13.5 |
| 1.5.7 / both | -9 / -19 | ok / ok / exact | 29.1 / 72.9–73.4 | 3.5 vs 11.5; 7.5 vs 10.2 |
| klauspost v1.18 | levels 1–4 | ok / ok / exact | 24–37 | 3.3–4.5 vs 11.5–13.0 |
| containers/storage zstd:chunked | default | ok / ok / exact | 23.6 | 4.0 vs 16.4 |
| zstd 1.5.7 | -3 -T4 -B1M | ok / ok / exact (one frame) | 24.1 | 3.1 vs 12.5 |
Same plaintext, different producer/settings costs 3–4.5 MiB of metadata instead of 11–16 MiB of new compressed stream; level 19 (optimal parse) is the expensive producer.
SquashFS probe (R0, `mksquashfs -comp zstd`): 467 data + 338 fragment + 44 metadata frames parse and replay exactly, IR packed ≈30 % of compressed. EROFS was **not** probed
(its tool needs an unpinned source build, refused in this sandbox).

## Streaming, memory, speed (deterministic text/JSON/record/random mix, zstd 1.5.7 -3; fresh process per phase)
| plaintext | parse heap / RSS | parse | replay heap / RSS | replay |
|---|---|---|---|---|
| 64 MiB | 59.9 / 71 MiB | 11.9 MiB/s | 56.9 / 68 MiB | 15.9 MiB/s |
| 256 MiB | 62.3 / 75 MiB | 11.8 MiB/s | 60.0 / 71 MiB | 16.5 MiB/s |
| 256 MiB, window 128 MiB (`--long=27`, model off) | 434 / 527 MiB | 26 MiB/s | – | – |
| 256 MiB, explicit sequences (no model) | 14 / 26 MiB | 36 MiB/s | – | – |
Memory is flat in object size (window + one block + model index ≈ 4 B per window position, capped at a 16 MiB window); a larger window costs window-proportional memory. The model costs ≈3× time.

## Range reads (R0 layer, 24 random offsets, replay from the nearest checkpoint; checkpoint = 80 B/block, 144 KB for 1 800 blocks; step 16 → 9 KB)
| range | step | blocks | plaintext (block + window) | units | skeleton | native decode needs |
|---|---|---|---|---|---|---|
| 4 KiB | 1 | 1.0 | 133 K + 2048 K (model) / 133 K + 0 (explicit) | 11 K / 24 K | 46 B | 35.8 M prefix |
| 64 KiB | 8 | 6.0 | 773 K + 2048 K / 773 K | 91 K / 187 K | 310 B | 43.4 M |
| 1 MiB | 16 | 26.5 | 3392 K + 1963 K / 3392 K | 485 K / 1018 K | 1.6 K | 36.3 M |
Feasible: replay needs no history, only the model's window (a ranged read of the plaintext child). Explicit sequences trade 2× unit bytes for no window fetch.

## Exactness evidence
Parser/decoder vs zstd 1.5.5/1.5.7 (5 levels × 25 inputs, bytes + SHA, every mode of the coverage gate hit), builder fixtures validated by the reference decoder (RLE literals, RLE tables, random repeat/zero-LL
offsets, treeless, 40×4 blocks), 8 committed fixtures ≤ 1.5 KB (`zstdz3/testdata/MANIFEST`), 60+ malformed cases with intended error classes, all truncations, hostile 100 TiB / 8 EiB sizes (allocation < 64 MiB), IR
canonical round trip + single-byte corruption sweep, tampering of every unit byte, `repz3` missing/tampered components, `FuzzParse` (replay of accepted input) and `FuzzIRUnmarshal`, XXH64 vectors, race run of the kind.

## Limitations, queue, reproduce
Dictionary, legacy and magicless streams unsupported; model off above a 16 MiB window; frame-relative 32-bit model positions (frames > 4 GiB use explicit units); skeleton memory ∝ blocks; speed is research grade.
Admission signals worth testing later (none coded): zstd frames recognised, decompressed tar, a related plaintext already stored, level ≥ 19 or binary-heavy layers expensive. SquashFS fragments are the first
reuse target; EROFS clusters need the magicless variant first. No Z3-26, no production work.
Reproduce: `oracle/zstd/setup.sh`; `z3zstd gen|eco|ecosum|units|unitsim|predict|matrix|range|repz|sqfs|memgen|memparse|memreplay|inspect -audit`; `go test ./zstdz3 ./repz3/kinds`; scratch ≈ 6 GB under /root/z3scratch.
