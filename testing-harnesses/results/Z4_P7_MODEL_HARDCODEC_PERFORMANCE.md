# Z4-P7: Exact Model Compression and Hard-Codec Performance Frontier

**Verdict: `P7_RESEARCH_READY`.** Everything below is implemented, byte-exact on real producer output, replayed in fresh
processes, extracted mechanically into a temporary single-file `zeros3.go` and rehearsed through the production engine.
Nothing is enabled in production: CDC stays authoritative, `zeros3.go`/`go.mod` are unchanged, no production disk saving is claimed.

**Short answer.**
* **Quantization** — the B5 Q4_0 mismatch was not a quantizer bug: the pinned bartowski files are *importance-weighted* (imatrix)
  and built with GCC FP contraction (FMA). With the producer's imatrix and an exact model of GCC's fused arithmetic, **every ported
  type reproduces 100 % of blocks** on three models and two producers (Q4_0 Q4_1 Q5_0 Q5_1 Q8_0 Q2_K Q3_K Q4_K Q5_K Q6_K IQ4_NL IQ4_XS;
  66.6 M real blocks, 0 mismatches). Only IQ3_S is unported.
* **Numeric engine** — the new windowed container (ZN7) matches B5's bytes within ±0.1 % and is **6-14x faster to encode, 1.4-1.6x
  (single worker) / 4.5-5x (4 workers) faster to replay, with 17-170x less encode memory and 3-54x less replay memory** (Qwen2.5-0.5B: 4.6 GiB → 27 MiB encode,
  7.6 GiB → 142 MiB replay). A 30-file quantized collection costs ⟦COLL_RATIO⟧x CDC.
* **LZ4** — pierrec/lz4 (Go), LZ4HC levels 10-12 and *linked* frames (library and `lz4 -BD`, fast and HC) are now predicted exactly
  with 3-4 byte recipes (B6: explicit recipes at 55-131 % of the stream). SST analysis is 6x faster.
* **Brotli** — a byte-exact Go port of the google/brotli one-shot encoder (quality 5-9) reproduces every page of the pinned Arrow
  Parquet-Brotli corpus: the chain costs 29.0 MB instead of 77.0 MB CDC (B6: oracle only). **Promote to the research kind set; reject for hot tiers** (11.7 MB/s replay per core).

## 1. Starting revision and branch
main `d2029bfb36072bcc7a46715870942f5ddf460e3e` (verified unchanged before the first write and before the PR), branch
`z4/p7-model-hardcodec-performance`. Machine: 4 vCPU, 15 GiB, Go 1.25.0, GCC 13.3.0, Linux 6.18. Producer pins: llama.cpp `b3991`,
liblz4 1.9.4 (python-lz4 4.4.5 and the `lz4` 1.9.4 CLI), pierrec/lz4 v4.1.22, google/brotli as bundled in pyarrow 26.0.1 and
python-brotli 1.2.1. Model pins: `z4/b5/pins.json`, `z4/p7/quant_pins.json`, Qwen2.5 GGUF revisions in `z4/p7/README.md`.

## 2. Baselines
All baselines are the unmodified main build (`z4b5` from a main worktree, B6 `z3sst`), same machine, same pinned inputs, fresh
processes, interleaved with the new code (`scripts/bench_model.sh`; 3 repetitions on Pythia-14M, 1 on the larger families because
one B5 run costs 5-14 min; raw rows in `results/model/*.jsonl`).

|Family (states, raw MB)|CDC MB|B5 policy|B5 MB|B5 encode s / RSS MiB|B5 replay s / RSS MiB|
|---|---|---|---|---|---|
|Pythia-14M (12, 338)|338|prev|269.63|42.7, 42.8, 57.0 / 454|7.5, 7.9, 8.0 / 407|
|Pythia-70M (11, 3099)|1697|prev|1117.78|315.6 / 1665|48.1 / 2596|
|SmolLM2-135M (8, 3229)|2916|pool3|1655.92|401.6 / 3263|65.8 / 4064|
|Qwen2.5-0.5B (5, 4940)|3132|pool3|1860.19|838.6 / 4559|103.1 / 7627|

Hard codecs (B6): RocksDB SST analysis 13 MiB/s (LZ4) and 6 MiB/s (LZ4HC); LZ4HC ≥ 10, linked blocks, Go producers and EROFS
explicit or unparsed; Parquet-Brotli an oracle estimate only.

## 3. B5 bottleneck analysis
pprof of the unmodified B5 encoder on Pythia-14M (`results/profiles/b5_family_pythia14m_*.txt`):
* 18 % of CPU is SHA-256 (whole containers and tensors are re-hashed per state and per proof), 32 % is residual coding that is
  executed in full for every candidate (`nrcEncodeCfg`, `nrcEstimate`, rANS, FPE), 10 % DEFLATE, 10 % memclr/memmove.
* 31 GB allocated to store 0.27 GB; whole tensors of target and every base are materialized (`Model.Read`), residuals and
  predictions are full-size arrays, containers are built in memory; replay decodes whole base containers per dependent
  (2.6-7.6 GiB RSS).
* Range segments were coded as independent sub-containers (2x encode time).

P7 removes each cause: `io.ReaderAt` inputs, fixed windows, candidates ranked on a sample and coded once, one digest per window
computed while coding, a byte-bounded window cache shared across dependents.

## 4. Q4_0 mismatch diagnosis
B5 reported Q4_0 "not reproducible" for bartowski/SmolLM2-135M-Instruct. Findings (`z4p7 quantconf`, C oracle `oracle/quant`):
1. **Importance weighting.** The files were produced with `llama-quantize --imatrix` (the imatrix is published next to the
   files, 606,008 B). With weights, legacy types go through `quantize_row_q4_0_impl` (`make_qx_quants` with per-element
   weights), not the reference rounding B5 ported: reference path 23,871 / 3,234,816 blocks (0.74 %).
2. **FP contraction.** With the imatrix, a strict IEEE port still misses 0.16 % of blocks (3,229,653 / 3,234,816). The remaining
   difference is the producer build: GCC `-O3 -mfma` with the default `-ffp-contract=fast` fuses specific multiply-adds.
   The pattern was recovered from GCC 13 GIMPLE dumps of `ggml-quants.c` (single-use products feeding an add in the same basic
   block; `nearest_int(a*b)` fused into the magic-constant add; loop-carried reductions left unfused) and implemented as the
   `GCCFMA` arithmetic profile with an exact float32 `fma` (float64 round-to-odd). Result: **3,234,816 / 3,234,816**.
3. Block order, tensor transforms and scale selection were never the cause; QuantFactory builds without an imatrix reproduce
   with the strict reference path. The oracle built with `-ffp-contract=off` equals the strict port; built with `-mfma` it
   equals `GCCFMA` (`qcore` golden vectors for both).
4. Consequence for storage: the derivation depends on the imatrix (an *aux* dependency, stored once and digest-bound) and
   on a one-byte producer profile per tensor.

## 5. Q5_K / K-quant / IQ producer conformance
`qcore` (stdlib only) ports reference and weighted paths of all types below. Blocks byte-identical to the producer file,
best variant per producer (full matrix with the losing variants: `scripts/conf_matrix.py results/quantconf*.jsonl`):

|Producer / model|Types (all 100 % exact)|Blocks|Variant|
|---|---|---|---|
|bartowski SmolLM2-135M-Instruct (13 files)|Q4_0 Q4_1 Q5_0 Q5_1 Q3_K Q4_K Q5_K Q6_K IQ4_NL IQ4_XS; Q8_0|29,047,680 + 17,404,416|imatrix + GCCFMA; Q8_0 ref|
|QuantFactory SmolLM2-135M-Instruct (14 files)|Q4_0 Q4_1 Q5_0 Q5_1 Q8_0 Q3_K Q4_K Q5_K Q6_K IQ4_NL|all tensors of all files|ref (strict)|
|QuantFactory Qwen2.5-0.5B-Instruct Q2_K file|IQ4_NL Q3_K Q5_0 Q8_0|12,576,256|ref, from the BF16 Safetensors|
|bartowski Qwen2.5-0.5B-Instruct Q2_K file|IQ4_NL Q3_K Q5_0 Q8_0|12,576,256|F16 step + imatrix + GCCFMA|
|QuantFactory Qwen2.5-1.5B-Instruct Q2_K file|**Q2_K** Q3_K Q4_K Q6_K|3,311,616 Q2_K + 2,717,952|F16 step + ref|
|bartowski Qwen2.5-1.5B-Instruct Q2_K file|⟦Q15BW_TYPES⟧|⟦Q15BW_BLOCKS⟧|⟦Q15BW_VARIANT⟧|

* Strict-imatrix (no FMA) reaches only 94.6-99.9 % on bartowski files; the reference path ≤ 2 %: the producer profile matters.
* "F16 step": llama.cpp pipelines quantize from an F16 GGUF; BF16 → F16 changes a few values. Direct BF16 input misses
  14 / 3,311,616 Q2_K blocks (Qwen2.5-1.5B) and 1 / 7,827,456 IQ4_NL blocks (Qwen2.5-0.5B bartowski); with the F16 step both are 0.
* Real Q2_K blocks exist only where the column count is a multiple of 256: SmolLM2-135M (576) and Qwen2.5-0.5B (896) "Q2_K"
  files contain no Q2_K tensor (llama.cpp falls back to IQ4_NL / Q3_K); Qwen2.5-1.5B (1536) does.
* Not ported: IQ3_S (21.7 MB in the two bartowski IQ3 files; stored opaque, exact). B5 behaviour for Q8_0/Q5_0/Q5_1/Q4_K/Q6_K is
  preserved (B5 tests pass through the `mdelta2` shim).
* New gain vs packaging reuse: quantized files derived from floats are *function replay of the same checkpoint* (packaging saving,
  §8); the new part in P7 is coverage (imatrix producers, Q2_K/Q3_K/Q5_K/IQ4) and zero patched blocks.

## 6. Numeric streaming architecture (`numcore`, container ZN7)
`"ZN7\x01" | gap payload | window payloads | directory | trailer`. Per tensor: fixed windows (default 65,536 elements), each with a
mode (raw / direct / predicted / copy / quant-derived), a coder (store, DEFLATE, FPE, NRC exponent-rANS, bit planes), an optional
exact narrowing (F32→F16/BF16) and a SHA-256/128 digest of its decoded bytes.
* Inputs are `io.ReaderAt` + a tensor layout; the encoder holds `workers × window` bytes of target and base, never a whole tensor.
* Candidate bases are ranked on evenly spaced 64-element spans with the real coders' cost estimate; the winner is coded once
  (`-exhaustive` codes every candidate in full: the planner oracle). Identity, affine and low-rank predictors are tried only
  after cheap probes say the structure exists.
* Quantized tensors: reference to a float tensor + producer profile (imatrix id, arithmetic, F16 step) + sparse block patches.
  The profile is chosen per file from a deterministic probe of the first tensors (strict and fused arithmetic agree on 95-99.9 % of
  blocks, so single-tensor samples tie).
* Non-tensor bytes (headers, padding) are one "gap" stream, delta-coded against a base with P5 `dcore` windows.
* Decoder: `Resolver{Open, Aux}` supplies dependencies; a byte-bounded LRU of decoded windows is shared by all dependents; depth
  ≤ 4; every window is verified before it is written; `ReadRange` touches only the windows (and base windows) under the range.
* P6 `semcore` was not reused for the container (its op programs target whole-tensor transforms); the tensor layouts come from
  the existing `st`/`ggufz3` parsers through a 100-line adapter, header deltas reuse P5 `dcore`.

## 7. Encode / decode throughput and RSS (same policy as B5 unless noted; fresh processes)

|Family|Bytes B5 → P7|Encode s B5 → P7|Encode RSS MiB|Replay s B5 → P7 (1 worker / 4)|Replay RSS MiB|
|---|---|---|---|---|---|
|Pythia-14M prev (3 reps)|269.63 → 269.54|42.7 → 3.7-3.9 (11x)|454 → 23-29|7.5-8.0 → 4.8-5.1 / 1.6-1.8|407 → 127-143|
|Pythia-70M prev|1117.78 → 1117.09|315.6 → 25.7 (12x)|1665 → 27|48.1 → 29.9 / 9.7|2596 → 139-155|
|SmolLM2 pool3 → pool:8|1655.92 → 1655.52|401.6 → 28.7 (14x)|3263 → 46|65.8 → 41.7 / 14.0|4064 → 149-179|
|Qwen2.5-0.5B pool3 → pool:8|1860.19 → 1862.00|838.6 → 140.6 (6x; 83.5 s CPU vs 995)|4559 → 27|103.1 → 72.3 / —|7627 → 142|
|synthetic pair 2 × 1280 MiB|— → 494.2|— → 18.7|— → 31|— → 25.3|— → 138|

* Worker scaling (SmolLM2 base→Instruct): encode 28.8 / 47.1 / 71.3 MiB/s with 1 / 2 / 4 workers; replay of the dependent state
  57 / 111 / 202 MiB/s. Encode RSS grows 15 → 27 MiB.
* Heap allocated (Pythia-14M encode): 31.2 GB → 3.4 GB. Temporary disk: none (containers are streamed to the store file).
* 1 M-element windows are 25 % faster to encode and 0.05 % smaller but raise RSS to 118 MiB and range amplification 15-40x
  (`model/pythia14m_w1m.jsonl`): 64 K is the default.
* The Qwen encode wall time was measured while another job was running (CPU time is the reliable column); B5's first Pythia-14M
  repetition (57.0 s) likewise.

Planner regret (Pythia-14M, pool:3): sampled planner 269,383,755 B in 11.9 s; exhaustive oracle 269,377,938 B in 31.1 s
(4x the allocations): **regret 0.0022 %**. Content discovery (`-disc`) 269,382,821 B. Selection is causal (`prev`, `first`,
`pool:K` over earlier states, or explicit `-parents`); tensors are matched by name, by the llama GGUF↔Hugging Face name map
(`-hfmap`, with the head permutation) or by content sketch (`-disc`). Non-causal interpolation between later states was not built:
an online store never has the later state at ingest (B5's finding), and re-encoding history is an authority-layer decision.

## 8. Real model-family economics (charged = object bytes + 132 B per object, every dependency counted once)

|Family|Logical MB|CDC MB|P7 MB (ratio)|Strongest existing|First state / CDC (B5, same coder)|Retained closure of last state (B5)|
|---|---|---|---|---|---|---|
|Pythia-14M prev|338|338|269.5 (0.797)|B5 0.798; B1 PXR oracle 0.827|0.845|chain re-roots every 4th state|
|Pythia-70M prev|3099|1697|1117.1 (0.658)|B5 0.659 (lin2 0.655)|0.696|21 %|
|SmolLM2 pool:8|3229|2916|1655.5 (0.568)|B5 pool3 0.568, hybrid 0.567|0.677|78 %|
|Qwen2.5-0.5B pool:8|4940|3132|1862.0 (0.594)|B5 pool3 0.594|0.680|50 %|
|Qwen2.5-0.5B pool:8 + structured|4940|3132|1785.8 (0.570)|B5 hybrid 0.568|0.680|50 %|
|SmolLM2 + 4 synthetic controls, structured|1345|908|231.6 (0.255)|B5 hybrid 0.248|—|—|
|Quantized collection (30 files, §below)|⟦COLL_LOGICAL⟧|1728.0|⟦COLL_MB⟧ (⟦COLL_RATIO⟧)|B5 0.283 on its 11-file subset|—|100 % (one float root)|

* The compression frontier of natural fine-tunes did not move (B5's negative result stands: SGD/AdamW noise is incompressible
  beyond exponent-conditioned residuals). P7's gain on float families is CPU and memory, not bytes.
* Structured probes are gated: on Qwen they find the abliterated rank-1 tensors (84.0 → 7.9 MB) and nothing else; on the controls
  LoRA 45.8 → 26.0 MB and scaling 15.7 → 0.78 MB. Probing costs 1.7x encode CPU on Qwen and 7x on the controls; off by default.
* **Quantized collection**: SmolLM2 base and Instruct Safetensors, the bartowski F16 GGUF, 13 bartowski (imatrix) and 14
  QuantFactory quantizations. ⟦COLL_DETAIL⟧
* Overheads are inside the P7 column: window directory 0.022-0.031 % of the object (digests, modes, references), gap stream
  3-7 KB per family, 132 B record per object; predictor metadata is a few bytes per tensor.
* Costs: additive (CDC kept) = CDC + P7; authoritative = P7 column; hot-hybrid (CDC for the newest state, P7 for history)
  ≈ P7 + one CDC state. Dependency closure: a dependent needs its parents' windows (depth ≤ 4) and, for imatrix files, the
  imatrix (606 KB, charged once). First-sight premium is negative (0.68-0.85 of CDC on the first state).
* Reconstruction CPU: float states 57-200 MiB/s; **quantized files re-run the quantizer: ⟦COLL_DEC⟧** — a cold-tier form.
* Tiers: hot — none (CDC); warm — float families with 4 replay workers; cold/archive — quantized derivations, structured edits.

## 9. LZ4 algorithm and producer improvements (`lz4z3`)

|Producer class|B6|P7|Evidence|
|---|---|---|---|
|pierrec/lz4 v4.1.22 (Go, block API)|0 % predicted|4297 / 4297 blocks, 4 B recipes|`testdata/pierrec_golden.txt`, `oracle/lz4/pierrecgen`|
|LZ4HC levels 10-12 (optimal parser)|explicit, 23-30 % of the block|195 / 195 python-lz4 blocks + 198 golden cases, 3 B recipes|`testdata/hcopt_golden.txt`; SquashFS `-Xhc` image fully predicted|
|Linked frames, fast mode (LZ4F one-shot, python streaming, `lz4 -BD`)|explicit, 75-108 % of the stream|all blocks predicted|`results/hard/lz4_linked_frames.txt`|
|Linked frames, LZ4HC 3-12 (LZ4F one-shot, `lz4 -BD -9/-12`)|explicit|all blocks predicted|same|
|Unsupported producer|full sweep per block|`Analyzer` gives up after N misses: 150 MB/s, one explicit recipe per block|`TestAnalyzerMemoAndGiveUp`|

* Linked blocks: the compressor state is carried across the blocks of a frame. Four profiles are modelled — hash table carried
  with contiguous blocks (prefix mode) or through the 64 KiB saved dictionary, for the fast compressor and for LZ4HC (hash chains
  carried; `LZ4_saveDictHC` + external-dictionary search). The first block selects fast vs HC and narrows the level; the profiles
  then compete and one wins per frame. **99 / 99 real linked frames (python-lz4 one-shot and streaming, `lz4` 1.9.4 CLI `-BD` at
  `-1`, `--fast=3`, `-9`, `-12`, 64 KiB and 256 KiB blocks, with stored blocks inside the chain) replay exactly with 0 explicit
  and 0 hybrid blocks; recipes 20-111 B per frame.**
* The real CLI exposed a detail no synthetic test had: after a block overflows its budget (stored raw) the table keeps the
  entries up to the overflow point, which depends on liblz4's exact output-limit formula (regression fixture `cli_BD_fast_budget.lz4`).
* Entropy coding of explicit recipes was measured and rejected: on linked LZ4HC frames the explicit recipe is 105-131 % of the
  compressed stream and DEFLATE-9 of it still 73-96 %. Recipes pay only when a predictor exists.

## 10. LZ4HC performance
pprof before optimizing (`results/profiles/lz4hc_predict_cpu.txt`): 76 % in the wider-match search, 18 % in the reverse pattern
count. Changes: predictor runs stop at the first sequence that differs from the producer's, the profile that matched the previous
block is tried first (`Analyzer`), 8-byte match counting, bounded reverse pattern count, pooled tables.

|RocksDB corpus (fresh process)|B6|P7|
|---|---|---|
|LZ4HC history (173 tables, 610 MiB): analysis|96.5 s, 6 MiB/s|16.7 s, 37 MiB/s|
|LZ4 fast history (174 tables, 631 MiB): analysis|50.2 s, 13 MiB/s|7.7 s, 82 MiB/s|
|Replay (unchanged algorithm)|51-53 / 127 MiB/s|52 / 135 MiB/s|
|Peak RSS|33 MiB|32-34 MiB|

Fresh-process exactness with independent `sha256sum`: 174 LZ4 SSTs, 59 LZ4HC SSTs, 8 LZ4_RAW Parquet files. ⟦FINAL_RERUN⟧

## 11. Parquet-Brotli reconstruction and economics (`brotliz3`)
* Producer: Arrow (pyarrow) writes each page with one-shot `BrotliEncoderCompress(quality 8, lgwin 22)`. Decisions that must be
  preserved are all of the encoder's: hasher (H5/H6) matches, lazy matching, static-dictionary references, greedy block splitting,
  context-map choice, Huffman and code-length coding. They are deterministic given (quality, lgwin, input).
* Mechanism: an exact Go port of the one-shot encoder for quality 5-9, lgwin 17-24 (stdlib only, 122,784 B RFC 7932 dictionary
  embedded) and a bounded RFC 7932 decoder. Recipe per page: 3 bytes (codec id carrying the quality).
* Conformance: 693 + 651 differential cases against pyarrow / python-brotli across qualities, windows and sizes; **932 / 932 real
  pages of the pinned corpus; 8 / 8 files rebuilt exactly in a fresh process**. One trap: glibc `log2` and Go `math.Log2` differ
  by 1 ulp on 1.3 % of integers, which flips entropy decisions — replaced by a correctly rounded integer `log2`. python-brotli's
  `compress()` uses the streaming API and differs from one-shot when the input ends exactly on a block boundary (documented, not the
  Arrow path).
* Unsupported classes fall back to opaque exact storage after 3 misses per file: quality 10-11 (Zopfli-style parser; fixture
  `pa_brotli_q11.parquet`), other windows, streaming producers.

|pyarrow chain, incremental MB per revision|CDC|generic page delta|logical (P7)|
|---|---|---|---|
|c0 base|24.8|24.2|23.5|
|c1 append 8000 rows|0.64|0.55|0.58|
|c2 update 200 sparse values|4.02|3.24|1.58|
|c3 retention delete|23.2|24.3|0.11|
|c4 append|0.99|0.87|0.58|
|c5 compaction rewrite|18.4|18.4|0.03|
|c6 update 400 sparse values|3.78|3.27|2.23|
|c7 append + metadata|1.44|1.33|0.57|
|**total**|**77.0**|**76.0**|**29.0 (0.377)**|

CPU: Brotli replay 11.7 MB/s per core (pprof: 62 % in `findLongestMatch`), decode 77 MB/s; pack 12.7 s and unpack 6.3 s per
27 MB file single-threaded. ⟦PQ_PAR⟧ Ranges are page-granular (`DecodeRange`).
**Decision: promote** Brotli q5-9/lgwin 22 to the research codec set next to Snappy/Zstd/LZ4 (exact, 3 B/page, 2.65x smaller on
rewrite-heavy histories); **reject for hot data** (replay CPU) and for producers at quality 10-11.

## 12. Additional codec findings
* SST / Parquet LZ4 recipes are portable (extracted with the Brotli core, §15); analysis results are memoized per file.
* SquashFS LZ4 `-Xhc` (level 12) images are fully predicted after the optimal-parser port.
* Producer variation is detected, not configured: quant profile probe, LZ4 profile competition, Brotli quality probe with give-up.
* `parquetz3.DecodeParallel` spreads page codec replays over workers with identical output (race-tested).
* EROFS `destSize` LZ4: not attempted. It needs an EROFS image parser (compressed index, pclusters) before any block can be
  located — container work, not codec work; `mkfs.erofs` is available for a later batch. WOFF2, LZX, LZMA2 were not reopened.

## 13. Actual versus modeled physical bytes
* ZN7 stores: charged (modeled) vs bytes on disk — ⟦COLL_ACTUAL⟧
* Through the production engine (rehearsal, three ZN7 objects ingested as ordinary objects): modeled 3,239,232 B, engine
  physical 3,250,203 B (+0.34 %: pack framing and manifests); the same originals as plain CDC objects 8,568,254 B.
* Hard codecs: recipe sizes are measured, not modeled (LZ4 3-4 B/block, Brotli 3 B/page); the Parquet chain numbers are packed
  bytes of the research store.
* No production disk saving exists while CDC is authoritative: additive cost is CDC + representation.

## 14. Read amplification (cold, random ranges, verified windows only)

|Store|4 KiB|64 KiB|1 MiB|
|---|---|---|---|
|Pythia-14M (64 K windows)|91x|—|—|
|Pythia-70M / SmolLM2 pool:3|33x (135 KB, 1.4 ms)|2.4x|0.5x|
|SmolLM2 pool:8|90x|6.9x|1.6x|
|Qwen + structured (low-rank parents)|245x|16.2x|2.1x|
|Quantized collection (Q4_K_M)|⟦COLL_RANGE⟧|||
|B5 (64 K segments), SmolLM2|144x, whole containers read|11.5x|2.8x|

1 M-element windows: 1328x at 4 KiB. Only directory + needed windows are read (B5 read whole containers: 667 MB per request).

## 15. Mechanical extraction and temporary production rehearsal (`scripts/extract_all.sh`)

|Core|Packages|Class|Declarations|Vectors (mismatches)|Rehearsal through the engine|
|---|---|---|---|---|---|
|numeric|`numcore` + `qcore` (+ `dcore`)|PORTABLE_WITH_THIN_ADAPTER|321|70 (0)|base → fine-tune → quantized ZN7 objects stored and replayed via `ingestStream` / `manifestReaderAt`, ranges, corrupted base rejected; peak heap 55 MiB|
|hard|`brotliz3` + `lz4z3`|PORTABLE_WITH_THIN_ADAPTER|225|306 (0)|real Arrow Brotli pages and python-lz4 blocks regenerated byte-exactly from engine objects|

Both compile into a temporary copy of `zeros3.go` (2.2 MB), stdlib only, `go.mod` identical, 0 fidelity failures, 0 unresolved
dependencies; production digest unchanged before/after. Integration finding: the engine's `manifestReaderAt` is not safe for
concurrent `ReadAt`; the adapter serializes it (a production integration should open one reader per worker).

## 16. Corruption, fuzz and race testing
* ZN7: bit flips in payload/directory/trailer never yield wrong bytes (window digests); missing/altered base or imatrix fails
  closed; cycle/depth limit; hostile layouts and directories (overflowing spans, oversized counts); short reads/writes; cancellation;
  unsupported quant types stored opaque; wrong candidate bases are priced and discarded.
* Brotli: hostile streams, size limits (decoder bombs), unsupported parameters; LZ4: corrupt recipes, profile changes inside a
  frame, oversized accelerations; Parquet: missing components and short writers in the parallel decoder.
* Fuzz targets: `FuzzOpenObject`, `FuzzPatches`, brotliz3 `FuzzDecode`, `FuzzEncodeDecode`, lz4z3 `FuzzDecode`, `FuzzRecipe`,
  `FuzzFrames`. Race: `numcore`, `lz4z3`, `brotliz3` (and `parquetz3` during development).
* `scripts/qualify.sh` on the final tree (`results/qualify/summary.txt`): gofmt, vet, build; tests of all 52 harness packages
  whose dependency closure contains a changed package; race; 7 fuzz targets x 20 s; production-unchanged; production build;
  extraction + rehearsal — **all PASS**. Four `prodshadow` tests fail, identically on a clean worktree of the starting revision
  (pre-existing; the script compares the failing set with the base and records it). The root production suite was not run:
  no production file changed (checked by the script against `d2029bf`).

## 17. Negative results
* Natural fine-tune bytes did not improve over B5 (±0.1 %); the planner's exhaustive oracle is only 0.0022 % better.
* pool:3 by position is 3.2 % worse than B5's content-selected pool on SmolLM2; all earlier states as candidates restores parity.
* Quantized derivations replay at quantizer speed (single-digit MiB/s for K-quants): unusable for hot reads.
* Strict IEEE arithmetic cannot reproduce FMA builds; non-finite inputs are build-dependent across GCC flags (matches `-O2`).
* Explicit LZ4 recipes with or without entropy coding are uneconomic; Brotli quality 10-11 and streaming producers are unsupported.
* Structured probing is expensive where it triggers and finds nothing on natural histories.
* 1 M-element windows: marginally better bytes, far worse ranges and memory.
* A per-tensor sample cannot identify the producer arithmetic (first implementation patched 0.03-0.14 % of blocks).

## 18. Outstanding blockers
* IQ3_S / IQ2 / IQ1 quantizers are not ported (grid-search tables); non-GCC or non-x86 producer builds need their own profile.
* Quant derivations need the float source *and* the producer's imatrix retained; neither is discoverable from the GGUF alone.
* EROFS needs a parser; LZ4 linked frames from liblz4 ≥ 1.10 multithreaded CLI are unverified.
* Brotli q10-11; Parquet writers other than Arrow C++ (parquet-mr, Rust) unverified.
* All economics are research-store numbers; authority (B2) is not enabled and GC closure pinning is not implemented.

## 19. Final PR / merge status
⟦PR⟧

## 20. Recommendations for C1 and P8
* C1 (authority integration): take `numcore` + `qcore` and `lz4z3` + `brotliz3` through the extraction path as they are; admit
  float families to a warm tier with 4 replay workers and quantized derivations / Brotli Parquet to cold only; pin dependency
  closures (float root, imatrix) in GC; one `manifestReaderAt` per worker.
* P8: IQ3_S port; a second arithmetic profile (clang/ARM) driven by the same oracle method; EROFS parser + `destSize`; Brotli
  streaming-API producers.

Evidence: `testing-harnesses/z4/p7/` (README with pins and commands; `results/` raw JSONL, logs, profiles, extraction and
qualification output).
