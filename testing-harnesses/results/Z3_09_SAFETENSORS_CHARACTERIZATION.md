# Z3-09: Safetensors structure and real checkpoint characterization

Laboratory evidence only. Nothing in `zeros3.go`, the root `go.mod`, the store, manifests or journal changed, and no tensor codec was
built: the question was *what similarity actually exists inside real model checkpoint families* before Z3-10 designs anything.
Real evidence and controlled synthetic evidence are kept apart; conclusions rest on the former.

```sh
cd testing-harnesses/z3
go run ./cmd/z3fetch -group pythia14m-train,pythia14m-sft-koshirowada,pythia14m-logs-honicky,bert-tiny-family,pythia14m-siblings -out "$TMPDIR/st" -max-mib 1500
go run ./cmd/z3st inspect|identity|bits|spacing|blocks|delta|class|header|base|cast|reshard -corpus "$TMPDIR/st"
go run ./cmd/z3ir family -corpus "$TMPDIR/st" -reps cdc,struct-seg,struct-cdc,delta-xorz,delta-plane,delta-sparse,delta-float,delta-best
go test ./st ./tdelta ./lab
```

## What was built

* `st/` - a strict, stdlib-only Safetensors parser (8-byte length, header, offsets), `io.ReaderAt`-based: **parsing reads the prefix and the
  header only** (2 reads, 8 496 B for a 26.8 MiB Pythia-14M file, 0.03%); tensor payloads are read by range on demand. It validates the header
  length (limit 100 MB, ≤ file), JSON shape (object, no duplicate keys or tensors, no unknown tensor fields, integer-only dims/offsets, ≤ 32
  dims, ≤ 2^20 tensors), dtype (unknown dtype rejected unless opted in), `dtype x shape` = byte length with overflow checks (incl. sub-byte F4/F6),
  sorted, hole-free, overlap-free offsets that end exactly at EOF (the reference rules), `__metadata__` string map. It never rewrites
  anything: `File.Header` keeps the exact header bytes (padding included) and `Rebuild` writes prefix + header + tensor payloads back byte for byte.
  Differential check against the reference `safetensors` package (optional, `Z3_PYTHON`): tensor count and metadata agree.
  Tests: 37 malformed/bounds subtests, round trip, I/O accounting; `FuzzParse` (arbitrary bytes: accepted structure tiles the file exactly,
  every tensor is readable in bounds, rebuild is exact, bounded header/tensor counts) and `FuzzHeader`.
* `tdelta/` - exact tensor-delta baselines on bit patterns only (no float arithmetic): `xor`, `xor+z`, `bytedelta+z`, `zero-rle`,
  `plane-xor+z` (XOR split into byte planes), `sparse` (changed-element bitmap + changed target elements), `float-split+z` (sign / exponent /
  mantissa XOR streams, F32/F16/BF16), and two no-base controls (`replace+z`, `plane+z alone`). All round-trip and reject garbage (tests + `FuzzDecode`).
* `cmd/z3st` - the characterisation tool; `lab` gained `delta-plane`, `delta-sparse`, `delta-float`, `delta-best` (per-tensor cheapest exact
  delta, rebuilt through the store and compared to the original file) next to `struct-seg`, `struct-cdc`, `delta-xorz`; the lab's Safetensors
  segmenter now uses the strict parser.

## Corpus (real, pinned, hash-verified, not committed)

All files are `model.safetensors`; sources, commit SHAs, SHA-256 (the Hugging Face LFS oid, checked at pin time) and licences are in
`realworld/sources.json`. 36 files, 1.1 GB, downloaded outside Git.

| group | relationship | files | dtype | note |
|---|---|---|---|---|
| `pythia14m-train` | **continued pretraining**, EleutherAI/pythia-14m training checkpoints | steps 0,1,2,4,8,16,32,64,128,256,512,1000,2000,100000,140000,141000,142000,143000 | F16 | log-spaced early steps + the end of training: distances 1 to 143 000 steps |
| `pythia14m-sft-koshirowada` | **fine-tune chain** (5 saved versions of one repo: epochs of SFT) | koshirowada/pythia_14m_sft | F32 | Trainer commits "Training in progress, epoch N" |
| `pythia14m-logs-honicky` | **fine-tune chain** (continued training on HDFS logs) | honicky/pythia-14m-hdfs-logs, 3 versions | F32 | |
| `pythia14m-siblings` | **sibling fine-tunes** from a common base + the pretrained model | pythia-14m main, domenicrosati/batch_128_wd_0, batch_1024_wd_0.1 | F16, BF16, BF16 | different batch size / weight decay |
| `bert-tiny-family` | **base + continued-MLM and task fine-tunes** | google/bert_uncased_L-2_H-128_A-2 + 6 derivatives | F32 | 40-46 tensors, different heads |

Not covered, said plainly: no LoRA/adapter-merge output (merging needs float arithmetic and none was found ready-made), no quantised
safetensors (the only dtype changes are F16 -> BF16 between pythia-14m and the domenicrosati siblings and F16 -> F32 in the Trainer chains), no
release-revision series with re-trained weights (all-MiniLM-L6-v2's 18 commits carry one identical file), no real multi-shard checkpoint
(re-sharding is *constructed* from real tensors), no model above 56 MB per file. Everything is small and mostly Pythia-14M-shaped (76 tensors).

## Real evidence

**Tensor identity (Z3-09D).** All 76 tensors of every Pythia-14M file share names, dtype, shapes, ordering and header; BERT-tiny
derivatives differ in heads (4 removed / 1-2 added tensors, `position_ids` I64 is the only identical tensor, 5-7 header-padding bytes
differ). **Whole-tensor exact reuse is essentially absent**: step0 -> step1 are byte-identical (the warm-up step has lr 0: 100%); step1 -> step2 keeps 13 non-trainable
buffer tensors (0.0% of bytes); every later adjacent pair, every fine-tune pair and every sibling pair reuses 0.0% of tensor bytes; no
tensor was renamed with identical bytes. Raw CDC, `struct-seg` (tensor segmentation) and `struct-cdc` (CDC inside tensors) all
cost 94.1 / 94.1 / 94.1% (continued pretraining), 89.5 / 88.5 / 89.5 (SFT chain), 88.4 / 87.3 / 88.4 (logs chain), 98.9 / 100.0 / 98.8 (BERT-tiny
family), 78.8 / 81.9 / 78.9 (siblings; the pack rule trims a little of the F16 low-entropy bytes) - **structural segmentation alone buys 0-1 points on real
checkpoints**. Header: 8.5 KB = 0.015-0.03% of a file, identical across a family and 25 B as a DEFLATEd delta; exact header storage is negligible
(`z3st header`).

**Bit-level change (Z3-09E), `z3st bits`, aligned tensors, adjacent pairs of continued pretraining (F16):**

| step pair | bit-identical elements | equal bytes | changed elems: sign / exp / mantissa changed | mean flipped bits | median log2(1+ULP dist.) | 4 KiB blocks changed | P(neighbour changed) vs overall |
|---|---:|---:|---|---:|---:|---:|---|
| 1 -> 2 | **97.76%** | 98.86% | 1.4 / 0.2 / 99.9 % | 2.18 | 1 | 99.8% | 0.05 vs 0.02 |
| 2 -> 4 | 90.58% | 95.23% | 0.0 / 0.3 / 100 | 2.29 | 1 | 99.8% | 0.11 vs 0.09 |
| 4 -> 8 | 52.06% | 75.69% | 0.0 / 0.3 / 100 | 2.16 | 1 | 99.8% | 0.56 vs 0.48 |
| 8 -> 16 | 16.76% | 57.31% | 0.0 / 0.8 / 100 | 2.34 | 2 | 99.8% | 0.86 vs 0.83 |
| 16 -> 32 | 6.12% | 49.86% | 0.1 / 2.2 / 100 | 3.04 | 3 | 100% | |
| 64 -> 128 | 0.52% | 29.18% | 1.7 / 17.2 / 100 | 4.77 | 7 | 100% | |
| 1000 -> 2000 | 0.04% | 5.04% | 18 / 66 / 100 | 6.40 | 10 | 100% | |
| 141000 -> 142000 (end of training) | 0.55% | 29.98% | 1.7 / 16.7 / 100 | 4.78 | 7 | 100% | |

Changes are almost entirely **mantissa low bits** (2-5 flipped bits, ULP distance ~2^1..2^7 early, 2^7..2^12 later), i.e. small
perturbations of every weight; sign/exponent flips appear only as the step distance grows. The changed elements are **uniformly scattered**
(neighbour-changed probability equals the base rate), so 4 KiB blocks are 99.8-100% "changed" even when 98% of the elements are identical.
Fine-tunes (F32, Trainer chains) flip ~9-11 bits per element and 0% of elements are identical in the logs chain; in the SFT chain 30%
of elements are identical and **clustered** (P(neighbour changed | changed) 1.0 vs 0.70 overall; 95% of 4 KiB blocks changed): rows of the embedding/output matrices
of tokens never seen in the data. BERT-tiny derivatives: 0% identical, 12 bits flipped (ULP 2^21). **Sibling fine-tunes
(`domenicrosati`, BF16, shared base): 91.5% of elements bit-identical, clustered (0.87 vs 0.08), 66% of 4 KiB blocks changed.**

**Checkpoint-spacing curve (Z3-09F; all 153 pairs of the 18 Pythia checkpoints, `z3st spacing`).** Distance in steps vs exact element reuse vs
best simple exact delta (per tensor, % of the changed tensors' raw bytes; standalone plane-split DEFLATE of the target alone is 96-97%) vs CDC (always 100%):

| step distance | bit-identical elements | equal bytes | XOR entropy (bits/byte) | best simple exact delta | CDC |
|---:|---:|---:|---:|---:|---:|
| 1 (step1 -> 2) | 97.8% | 98.9% | 0.12 | **2.6%** | 100% |
| 2 | 90.6% | 95.2% | 0.46 | 8.1% | 100% |
| 4 | 52.1% | 75.7% | 1.66 | 21.4% | 100% |
| 8 | 16.8% | 57.3% | 2.92 | 32.6% | 100% |
| 16 | 6.1% | 49.9% | 4.03 | 46.5% | 100% |
| 32 | 2.1% | 42.3% | 5.05 | 58.5% | 100% |
| 64 | 0.52% | 29.2% | 5.91 | 68.9% | 100% |
| 128 | 0.13% | 13.5% | 6.60 | 77.6% | 100% |
| 256 / 512 | 0.09 / 0.06% | 10.1 / 7.0% | 6.93 / 7.13 | 81.7 / 83.5% | 100% |
| 1 000 (early, step 1000 -> 2000) | 0.04% | 5.0% | 7.26 | 85.5% | 100% |
| 1 000 (late, 141000 -> 142000) | 0.55% | 30.0% | 5.95 | **69.3%** | 100% |
| 40 000 - 142 000 | 0.01% | 1.4-6.4% | 7.0-7.6 | 82.6-97.1% | 100% |

Model-aware similarity (bit-identical elements) needs a distance of **<= ~4-8 optimizer steps** and exists only in the first ~16 steps of a
run. After that the only signal left is the low-entropy upper byte plane (sign/exponent): XOR + byte-plane split compresses to 58-85% of the raw bytes, but ~15-20 of
those points are available **without any base** (standalone plane-split DEFLATE: 96-97% for F16 vs 69-85% with the base - the base's contribution is real but
modest: 12-28 points). Late-training 1 000-step neighbours (lr decayed) give 30% equal bytes and 69% best delta. CDC and tensor segmentation stay at 100% for every distance
except exact copies. This reproduces the earlier "Pythia steps 1000-4000 do not delta" result and explains it.

**Sub-tensor blocks (Z3-09G, `z3st blocks`; content-addressed reuse of the target against all blocks of the base):**

| pair | whole-file CDC | whole tensor | fixed 64 / 16 / 4 KiB | CDC inside tensor | **row blocks (2-D)** |
|---|---:|---:|---|---:|---:|
| step 1 -> 2 (97.8% identical elements) | 0.0 | 0.0 | 0 / 0 / 0 | 0.0 | **10.4** |
| step 2 -> 4 | 0.0 | 0.0 | 0 / 0 / 0 | 0.0 | 1.3 |
| SFT chain epoch 2 -> 3 | 0.0 | 0.0 | 0 / 0.1 / 5.0 | 0.0 | **30.1** |
| siblings (BF16) | 3.8 | 0.0 | 3.9 / 17.2 / 33.7 | 3.9 | **80.1** |
| all other pairs | 0.0 | 0.0 | 0 | 0.0 | 0.0 |

Whole-tensor granularity is too coarse and 4/16/64 KiB blocks too fine-grained *relative to how updates scatter*; **row-aligned blocks** are
the only cut that finds exact reuse because untouched embedding/output rows (or rows of a barely-updated matrix) are whole. The signal is sparse
and structured by the model (rows), which generic CDC cannot see.

**Simple exact deltas (Z3-09H/I; `z3st delta`, % of the changed aligned tensors' raw bytes; every codec round-trips in `tdelta` and,
via `delta-best`, through the lab store with whole-file SHA-256 equality):**

| pair | standalone DEFLATE | standalone plane+z | XOR (raw) | XOR+z | bytedelta+z | zero-RLE | **plane XOR+z** | sparse | float-split+z | best incl. standalone |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| step 1 -> 2 (F16) | 99.9 | 96.3 | 100 | 2.7 | 2.5 | 3.6 | 2.6 | 3.5 | 2.9 | **2.5** |
| step 4 -> 8 | 99.9 | 96.4 | 100 | 25.1 | 21.8 | 71.8 | 21.7 | 53.7 | 25.5 | 21.4 |
| step 64 -> 128 | 100 | 96.6 | 100 | 82.3 | 78.8 | 128 | 68.9 | 99.9 | 83.0 | 68.9 |
| step 141000 -> 142000 | 100 | 96.7 | 100 | 77.6 | 73.8 | 129 | 69.3 | 99.9 | 82.0 | 69.3 |
| SFT chain, mean of 4 (F32) | 88.4 | 70.4 | 100 | 51-79 | 51-75 | 77-119 | 43-60 | 70-88 | 50-66 | 43-60 |
| logs chain, mean of 2 (F32) | 87 | 76 | 100 | 53-71 | 46-65 | 109-124 | 49-58 | 87 | 49-61 | 43-51 |
| BERT-tiny family (F32) | 100 | 85.6 | 100 | 87-100 | 82-100 | 118 | 69-82 | 100 | 78-85 | 69-82 |
| **siblings BF16** | 80.6 | 62.3 | 100 | 5.7 | 5.4 | 12.6 | 5.0 | **8.9** | 5.2 | **4.7** |

Raw XOR is never smaller (it is the same size) - **XOR only helps with a back-end**; XOR+DEFLATE helps only when most elements are identical.
**Byte-plane separation is the useful transform** (always <= every other codec at distances where the base matters: 21.7 vs 25.1 at 8 steps, 69 vs 78 at 64-141 000), **sign/exponent/
mantissa separation adds nothing over byte planes** (float-split 82 vs plane 69). Sparse element lists only win when >90% of elements are identical (siblings 8.9 vs 5.0; steps 1-4 3.5 vs 2.6) and
lose elsewhere (100%). Zero-run coding is the worst (it has no entropy stage). Whole-file, vs adjacent base: CDC 100% always; best simple
representation: 2.6 / 8.1 / 21.4 / 32.6 / 46.5 / 58.5 / 68.9 / 77.6% at 1-128 steps, 69.3% for late-training neighbours, 42-60% for the
Trainer chains, 69-82% for BERT-tiny derivatives, 4.7% for the sibling fine-tune. Lab, exactly rebuilt files (`z3ir family`, byte-weighted
over update revisions): continued pretraining CDC 94.1 -> **delta-best 56.4**; SFT chain 89.5 -> 49.3; logs chain 88.4 -> 50.6; BERT-tiny 98.9 -> 77.3;
siblings 78.8 -> **42.6** (the 78.8 for CDC reflects the base -> sibling pair not delta-able across dtypes; the sibling pair itself is 4.7%).

**Tensor classes (Z3-09J, generic name patterns).** Embedding + output head are 91.6% of the bytes (Pythia-14M's vocabulary is large relative to
its layers), attention 2.8%, MLP 5.6%, normalization/biases ~0%. Continued pretraining: all classes alike (18-24% identical elements averaged over all pairs incl. the step0 = step1 copy, best delta
57-62%); normalization is the *least* delta-friendly class (76-88%). **Siblings: embedding and output head are 48.7-49.9% identical elements and
delta to 1.6% / 0.4% of their raw bytes; attention/MLP 6-10% identical, 42-47%.** SFT chain: the embedding holds the identical rows (49%, delta 32.6%);
other classes 0.0-0.15% identical, delta 61-80%. So the structure lives in the big embedding/head matrices, not in the transformer blocks.

**Base selection (Z3-09M, `z3st base`; simple-delta cost summed over changed target tensors):**

| family | adjacent previous | r0 | best earlier same-name (oracle) | best incl. other-name same-shape (oracle) | adjacent optimal | other same-name wins | different-name wins |
|---|---:|---:|---:|---:|---:|---:|---:|
| Pythia training (17 targets) | 59.9 | 68.8 | 59.8 | 59.8 | 85.7% | 10.9% | 3.4% |
| SFT chain | 49.3 | 59.8 | 48.4 | 48.4 | 74.0% | 24.0% | 2.0% |
| logs chain | 50.7 | 50.8 | 50.7 | 50.7 | 86.8% | 12.5% | 0.7% |
| BERT-tiny family (star: one base) | 77.4 | **66.4** | 66.4 | 66.4 | 30.4% | 69.6% | 0.0% |
| siblings | 4.8 | 100 | 4.8 | 4.8 | 96.7% | 0.0% | 3.3% |

For chains the adjacent predecessor of the same name is within 0-1 point of the oracle (74-97% of tensors pick it); for star-shaped
families (many derivatives of one base) the base is the better reference. A different tensor name wins in 0-3% of tensors. Same-name + same-shape
candidates are therefore enough; no global tensor similarity index is justified by this data. A converted base does not explain fine-tunes either:
F16 base cast to F32/BF16 (exact widening / one rounding, integer arithmetic) is 0.00-0.05% bit-identical to the Trainer chains and
to the siblings (`z3st cast`) - those models did not start from the pinned step-143000 weights.

**Cross-shard reuse (Z3-09L; constructed from real tensors, not downloaded).** Repacking the step-143000 tensors into other shard sets: raw CDC given the
first set costs 0.8% (2 -> 3 shards, same order), 0.0% (4 -> 4, name order), 6.7% (3 -> 5, reverse order) of the new bytes; tensor identity 0.0%.
Format-awareness saves at most 7 points here because tensors are megabytes long and CDC resynchronises inside them. With a single changed byte tensor identity
re-stores the 45.8% tensor while CDC pays 0.9%: whole-tensor granularity is worse than CDC for tiny edits.

**Range-friendly parsing (Z3-09B).** `st.Parse` over an `io.ReaderAt` performs 2 reads and 8.5 KB for any of the files (0.015-0.03% of the
file); one tensor range = 1 read of the requested bytes. Rebuild from the structure is byte-exact for all 36 real files (lab `struct-seg`, exact 17/17, 4/4, 2/2, 6/6, 2/2).

## Controlled synthetic evidence (not used for conclusions)

`z3corpus` `st-*` families (2 MiB bases, ground truth known). Exact rebuilds of all rows. CDC / struct-seg / struct-cdc / xorz / plane / sparse / float / best:
checkpoint-mostly 24.6 / 12.9 / 13.8 / 0.6 / 0.6 / 1.0 / 0.6 / 0.6; dense small fine-tune perturbation 97.5 / 98.3 / 98.9 / 4.3 / 3.9 / 7.0 / 4.1 / **3.9**; sparse
modification 6.8 / 2.8 / 2.9 / 0.0 ... 0.0 (sparse 0%); reordered data 47.0 / **0.0** / 0.1 / 0 (structural identity wins); rename 3.5 / 0.0 / 0.1 / 0; metadata reorder 1.5 / 0.0. These show what the tools *would*
find if real checkpoints behaved that way (dense low-bit noise, sparse edits, reordering): only the reorder/rename/metadata rows are structural wins, and no real
pair showed them. The real data differ from the controls in having ~2-5 flipped mantissa bits on *every* element instead of a few exact edits.

## Answers

1. **How often are whole tensors exactly reused?** Never in practice: after step 2 no trainable tensor is ever byte-identical to its predecessor in any pair (only step0 = step1 and 13 buffers at step 2); none in any
   fine-tune chain; one buffer tensor in BERT-tiny pairs.
2. **How close must checkpoints be?** Bit-level similarity (>50% identical elements) needs <= ~4-8 optimizer steps at the start of a run (97.8% at 1 step, 52% at 4 -> 8);
   below 1% beyond ~64 steps. The late-training 1 000-step neighbours keep 30% equal bytes (upper byte plane), no element reuse.
3. **Fine-tuning vs continued pretraining?** Different. Trainer fine-tune chains flip 9-12 bits/element in F32 (0% identical in the logs chain), except
   frozen embedding rows (30% identical, clustered in the SFT chain). Fine-tunes that share a base and are stored in BF16 are **91.5% bit-identical and cluster by rows**
   (delta 4.7%, rows 80% reusable) - the strongest real signal found. Continued pretraining is uniform noise.
4. **Easiest dtypes / classes?** BF16 (coarse mantissa swallows tiny updates) and F16 at tiny distance; F32 fine-tunes are hard (11-12 flipped bits).
   Embedding/output head carry the exact identity (unused rows) and delta to 0.4-1.6% in siblings; transformer blocks and normalization are the hard part.
5. **Is XOR useful?** Alone no (same size); XOR + a DEFLATE back-end only when most elements are identical (steps <= 4, siblings).
6. **After byte/bit-plane transforms?** Yes: byte-plane XOR is the best simple codec at every distance where the base matters (21.7% vs 25.1% plain XOR+z at 8 steps; 69% vs 78% late); part
   of the F32 gain is plane-splitting alone (standalone plane+z 62-77% for the F32 chains, 96-97% for F16), so always compare with the no-base control. Sign/exponent/mantissa splitting adds nothing over byte planes.
7. **Sparse changed-element lists?** Only for >90%-identical pairs (siblings 8.9% vs 5.0% planes; steps 1-4 3.5% vs 2.6%); otherwise 90-100%.
8. **Does subblocking help?** Fixed 4/16/64 KiB and CDC-in-tensor: no (0-5%; uniform scatter defeats them). Row blocks: yes for sparse row-structured updates (10-80%). Whole tensors: no.
9. **Does structural segmentation beat raw CDC?** No on real checkpoints (0-3 points either way); yes on synthetic reorder/rename/metadata controls and,
   mildly (<=7 points), on re-sharded layouts.
10. **What should Z3-10 implement?** (a) A **tensor-pair delta codec** with byte-plane XOR + entropy coding (the DEFLATE stand-in measured here; a range coder per plane would be the
    natural upgrade) against a *same-name, same-shape* base: the adjacent predecessor in chains, the base model in star families - no similarity index. (b) **Row-aligned exact block dedupe** for 2-D tensors before
    delta (sibling/embedding wins). (c) Admission by measured pair distance: apply only when the predecessor is within a few steps (continued pretraining) or is a BF16/F16 sibling; for
    F32 fine-tunes expect 45-60% of raw, for far checkpoints skip (CDC at 100% is the honest answer). (d) Keep exact header/padding bytes as plain data (0.03%). (e) Defer tensor-identity dedupe and
    cross-shard indexes: no real evidence. The next workload is not another weights corpus but whatever carries exact repetition: container layers (Z3-08 shows 5x on OCI), then tar-aware plaintext chunking.

## Validation and limitations

`gofmt`, `go vet ./...`, `go test ./...` (parser malformed tests, fixtures, tdelta round trips, lab exactness incl. `delta-*` on the Safetensors corpus). Fuzz: `FuzzParse` 240 s
(11.9 M execs), `FuzzHeader` 180 s (2.0 M), `tdelta.FuzzDecode` 120 s (6.0 M): clean, accepted structures tile the file exactly and rebuild identically.
Limitations: 4M-14M-parameter models only, one architecture family dominates (Pythia-14M, 91.6% of bytes in embeddings), no LoRA merges, no quantised formats,
no real sharded checkpoints, delta chains are modelled one step deep (resolution cost not modelled), DEFLATE is the only entropy coder tried, no larger-model
fine-tune series (all dense ones are 56 MB F32). Everything above must be re-measured on a larger model before a codec is frozen.
