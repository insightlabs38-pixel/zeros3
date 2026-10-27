# Z3-29: real model artifact frontier (GGUF, Safetensors, PyTorch ZIP)

Laboratory only: `zeros3.go`, tests, root `go.mod/go.sum`, ZS3R/ZS3T, Store format 6 and production kind numbers are untouched (`git diff origin/main` on them is empty). Code: `testing-harnesses/z3/{ggufz3,torchz3,modelz3,repz3/kinds/model.go,cmd/z3model}`; reproduce with `go run ./cmd/z3model {gguf|st|pt|reexport|deep|reads|shards|fields|entropy|meta|derive}`. Costs are **% of the target's bytes**, new components under the production pack rule (DEFLATE, kept if it saves >= 1/16) plus serialized representation objects; every row is rebuilt through `repz3` (length + SHA-256 + bytes). Classes: REAL, REAL_REEXPORT (real bytes, producer/layout rebuilt by a pinned tool), SYNTHETIC_CONTROL (unit tests only, never evidence), ORACLE.

## 1. Question and decisions
| mechanism | decision | one-line reason |
|---|---|---|
| Safetensors: structural + byte-plane XOR residual vs an ordinary base member (`KindModel`) | **PROMOTE_TO_I2** | Qwen2.5-0.5B (988 MB BF16) base>Instruct 57.6% vs CDC 97.5%; SmolLM2 star family 41.6% cumulative; ~345 MiB/s decode, no false win on unrelated pairs |
| Safetensors: PXR range-coded residual | **DEFER** | 2-8 points smaller (49.3% Qwen) but 15.6 MiB/s decode (22x slower); unlock = a >=100 MiB/s byte coder |
| GGUF: structural + same-ggml-type plane residual (same `KindModel`) | **PROMOTE_TO_I2** | real base>fine-tune pairs 58-65% (SmolLM2, Qwen2.5) and 79.5% (Qwen3 K-quants) vs CDC 98.3-98.8%; scope below |
| GGUF: native quant-block reuse, cross-quant transform, dequantise path, the Z3-02 "31.7% > 2.5%" | **KILL** | 0.0% aligned and 0.1% anywhere block identity on every real pair; the 2.5% was a low-mantissa-bit synthetic artefact |
| PyTorch ZIP/storage kind (pickle-free or pickle-aware) | **KILL** | member-structural = CDC within 0.1 point on every checkpoint pair; no pickle needed for what delta exists |
| Generic `BASE` primitive | **NOT JUSTIFIED** | Safetensors and GGUF both use kind-local Stored/Derived members; liveness proven by `Reachable` |

**Overall: MODEL_WAVE_READY** (one decoder, two format encoders; open items are I4 admission and I0 CDC/representation sharing, not new semantics).

## 2. Corpus and provenance (`realworld/models.json`, `derived_models.json`; weights never committed; 8.4 GiB pinned, 8.8 GiB downloaded of the 10 GiB cap)
| family | format | relationship | files | dtype / quant | bytes | class |
|---|---|---|---:|---|---:|---|
| SmolLM2-135M | Safetensors | base, Instruct, SFT-only, Shakespeare SFT (star) | 4 | BF16 | 1.00 GiB | REAL |
| Qwen2.5-0.5B | Safetensors | base > Instruct | 2 | BF16 | 1.84 GiB | REAL |
| SmolLM v1 135M | Safetensors | unrelated control, same shapes | 1 | F32 | 0.50 GiB | REAL |
| SmolLM2-135M | GGUF | QuantFactory base/Instruct x Q4_K_M, Q5_K_M, Q6_K, Q8_0; bartowski (imatrix); SmolLM v1 control | 12 | Q4_K Q5_0 Q5_1 Q6_K Q8_0 F32 | 1.40 GiB | REAL |
| Qwen2.5-0.5B / Qwen3-0.6B | GGUF | QuantFactory base>Instruct, Dhruval, official Qwen; mradermacher base>post-trained | 4 + 2 | Q4_K_M | 1.57 + 0.82 GiB | REAL |
| Pythia-70M, steps 1k-143k | GGUF | llama.cpp `7aca0d2e` (2026-10-26), gguf-py 0.19.0, torch 2.14.1 | 15 | F16, Q4_K_M, Q8_0 | 1.3 GiB | REAL_REEXPORT |
| Pythia-70M/14M, bert-tiny (+2 fine-tunes), Pythia-70M-deduped | PyTorch ZIP | checkpoint chains, star, sibling run; Pythia-70M `model.safetensors` of the same state | 14 + 1 | F16 / F32 | 1.2 GiB | REAL |
| layout re-exports of real tensors | GGUF, Safetensors, PyTorch | extra KV / reverse / align 64 / 3 and 5 shards / one tensor swapped / re-saved by torch 2.14.1 | 15 | - | - | REAL_REEXPORT |
Producers seen: all 18 third-party GGUF are v3, `quantization_version` 2, no `quantized_by` (llama.cpp via QuantFactory/bartowski/mradermacher/Dhruval/Qwen; exact versions unrecorded); Safetensors `format=pt`; PyTorch ZIPs are three eras (`archive/` older; `pytorch_model/` + `version`; + `byteorder` + `.data/serialization_id`), all with ZIP64 end records, stored members, data descriptors, 64-byte aligned data. Licences: Apache-2.0 (38 of 40 pins), MIT (bert-tiny), SFT-only unlisted. Oracle tools (llama.cpp, torch 2.14.1, safetensors 0.8.0, transformers 4.57.6) are producers/oracles only. GGUF byte coverage: Q8_0 38.7%, Q5_0 24.6%, Q4_K 16.4%, Q6_K 15.4%, Q5_1 2.9%, Q5_K 0.5%, F32 0.1%; no IQ/Q2_K/Q3_K in the corpus (parser geometry table has them, untested on real data); Safetensors BF16/F32 (+F16 via Pythia).

## 3. GGUF characterization (REAL unless marked; cost % of target; CDC = current ZeroS3)
| relationship | CDC | tensor struct | native-block (aligned / any) | aligned XOR-plane delta | KindModel incremental / family cumulative |
|---|---:|---:|---:|---:|---|
| same quant: SmolLM2 base>Instruct Q4_K_M / Q5_K_M / Q6_K / Q8_0 | 98.4 / 98.5 / 98.8 / 98.8 | 98.7-99.1 | 0.0 / 0.1 | 58.3 / 58.9 / 64.8 / 64.6 (standalone 99.9) | 58.1 / - / - / 64.7; cum 78.4 and 81.9 |
| same quant: Qwen2.5-0.5B base>Instruct Q4_K_M; Qwen3-0.6B base>post Q4_K_M | 98.3; 98.6 | 98.9; 99.1 | 0.0 / 0.1 | 58.4; 79.6 | 58.3 (cum 78.6); 79.5 (cum 88.3) |
| other producer, same model: Q8_0 QuantFactory>bartowski | 0.1 | 0.4 | 100 / 100 | 0.2 | 0.1 (tensors bit-identical; CDC already finds it) |
| other producer, same model: Q4_K_M plain>imatrix | 69.5 | 68.6 | 31.5 | 18.3 | 18.2 (cum 58.5) |
| cross-quant, e.g. Q4_K_M>Q5_K_M / Q6_K>Q8_0 / base Q4_K_M>Instruct Q8_0 | 63.1 / 21.6 / 98.8 | 61.4 / 19.9 / 99.1 | 38 / 80 / 0.0 whole-tensor | 62.0 / 19.9 / 92.6 | only tensors both recipes store in the same type (bit-identical); no transform across types |
| metadata: extra KV / reversed tensor order / alignment 64 (REAL_REEXPORT) | 0.13 / 19.72 / 0.13 | 0.52 | - | - | header CDC'd + tensors whole: 0.06 / 0.01 / 0.06 |
| Qwen2.5-0.5B Instruct QuantFactory>Dhruval (re-quant, 87% tensors identical) | 15.0 | 13.0 | 87 | 12.9 | - |
| unrelated, same arch + quant: SmolLM2 Instruct vs SmolLM v1 Q4_K_M / Q8_0 | 98.4 / 98.9 | 98.7 | 0.0 / 0.1 | 99.9 | 98.3 (no false win) |
| training chain REAL_REEXPORT: Pythia-70M Q4_K_M, Q8_0, F16 (1k-step pairs) | 96.6-98.8 | 97.4-99.1 | 0.0 / 0.2 | 99.4-99.9; F16 85.7-88.8 | cumulative F16 93.7 (previous) vs CDC 98.9; Q4_K_M 96.5 vs 96.8 |
* Metadata is 1.2-1.7% of a GGUF (tokenizer); structural reps must CDC the header (whole-component header re-stores 1.7 MB per edit, 0.52% vs 0.06%). Reordering is the only place CDC loses badly (19.7%).
* Churn is in the payload, not the scales: scale/min fields are 4-16% of the packed residual (Q8_0 4.4%, Q4_K 15.6%); 0% of blocks are fully equal, 11-45% of individual bytes are. Order-0 plane entropy / byte-wise PXR are only 4-12 points under DEFLATE (Q8_0 64.7 / 60.7 / 59.1; Qwen3 Q4_K 60.5 / 53.5 / 54.1), so the pack rule is the right coder; no quant-specific codec was built.
* Scope of the PROMOTE: fine-tunes of <=0.6B-parameter models quantised by one producer; no >=7B, IQ, or Q2/Q3_K evidence; gains shrink with fine-tune size (Qwen3 post-training 79.5% vs SmolLM2 SFT/DPO 58%).

## 4. Safetensors extension (Z3-10 `st`, `tdelta`, PXR run unchanged; SmolLM2 base>Instruct reproduces Z3-10's 50.7%)
| family (BF16) | CDC | structural | PXR container | plane residual (incremental) | winner | decode MiB/s (PXR / plane, no store inflate) |
|---|---:|---:|---:|---:|---|---|
| Qwen2.5-0.5B base>Instruct, 988 MB | 97.5 | 100.0 | **49.3** | 57.6 (cum 64.5) | PXR bytes, plane CPU | 15.6 / 345 (93 with DEFLATE inflate) |
| SmolLM2 base>Instruct / base>SFT-only | 97.9 | 99.7 | 50.7 | 58.1 | PXR bytes | 15.0 / ~340 |
| SmolLM2 base>Shakespeare (LR-small SFT) | 97.0 | 98.1 | 23.7 | 31.5 | PXR bytes | 32.9 / ~340 |
| SmolLM2 Instruct>SFT-only (sibling) | 97.8 | 99.7 | 2.9 | 5.0 | PXR bytes | 125 / ~340 |
| star of 4 files, cumulative | 97.6 | 99.3 | - | anchor 54.8, hybrid 41.6 | plane (hybrid) | - |
| unrelated Qwen>SmolLM2 (shapes differ) | 97.9 | 99.6 | none | 71.6 = standalone planes | no false win | - |
| sharded: base 3 shards > Instruct 3 or 5 shards (REAL_REEXPORT) | 97.9 / 97.8 | - | - | 58.1 / 58.1 (+22 KB index) | exact | - |
* Byte-plane + DEFLATE alone (no base) already stores BF16 at 71.4% vs 97.4%: that part is compression, not deduplication, and is the only gain on the unrelated control. Layout re-exports: reversed order CDC 7.3% > 0.0%; re-shard 3/5 files 0.11-0.16% > 0.00-0.01%; one swapped embedding (20%) is charged 20.7%, so only the changed tensor is paid.

## 5. PyTorch ZIP characterization (`torchz3` raw parser; Go `archive/zip` is the differential oracle in tests)
| pair | CDC | ZIP-member structural | storage SHA reuse | aligned delta, name + width probe (no pickle) | oracle named-storage | best exact |
|---|---:|---:|---:|---:|---:|---|
| Pythia-70M 1k>2k / 4k>8k / 8k>143k | 84.9 / 84.9 / 84.8 | 84.8 | 15.2 (constant attention-mask buffers, also in an unrelated run) | 75.0 / 71.9 / 78.1 | 75.3 / 72.7 / 78.1 | delta, 10-13 points under standalone planes |
| Pythia-14M 1k>2k / 3k>4k / 4k>143k | 99.9-100 | 99.9 | 0.0 | 85.4 / 81.5 / 99.7 | 85.4 / 81.6 / 99.7 | delta only up to ~1-3k steps |
| bert-tiny base>2 fine-tunes, sibling pair (storage ids differ per producer) | 99.9 | 99.9 / 99.6 | 0.0 / 0.4 | 85.7 / 85.8 / 84.0 | 85.7 / 85.8 / 84.0 | no delta over standalone planes (85.7) |
| unrelated Pythia-70M vs -deduped | 84.7 | 84.8 | 15.2 | 77.1 | 77.2 | standalone planes |
| same state as .bin and model.safetensors (ST stored after PT) | 1.7 | **0.0** | 100 | - | 100 | structure wins cross-format |
| re-saved by torch 2.14.1 / key order reversed | 1.57 / 1.64 | 0.01 | - | - | - | framing churn only |
* Pickle gate **fails**: storage numbering is unstable only where there is no delta to lose (bert-tiny, 42 storages); name + width probing is within 0.8 point of the oracle where delta exists; framing + `data.pkl` are 0.015% of bytes. `data.pkl` stays opaque; no pickle parser built. ZIP framing/descriptors/central directory round-trip as ordinary components.

## 6-7. Strongest exact candidates and base-chain economics (`KindModel`, kind 8; cumulative = all revisions of the family, pack rule, exact on every revision)
| family | per-revision incremental (anchor) | anchor h<=2 | previous h<=3 | anchor or previous (bounded) | per-revision greedy | first-sight CDC then anchor | retained alone |
|---|---|---:|---:|---:|---|---:|---:|
| Qwen2.5-0.5B ST | 71.4 / 57.6 | 64.5 | 64.5 | 64.5 | - | 84.4 | 129% |
| SmolLM2 ST star (4) | 71.6 / 58.1 / 58.1 / 31.5 | 54.8 | 51.5 | **41.6** | 41.6 (= hindsight) | 78.1 | 103% |
| SmolLM2 GGUF Q4_K_M / Q8_0 | 98.7 / 58.1 ; 99.1 / 64.7 | 78.4 ; 81.9 | same | same | same | 98.6 ; 99.0 | 157% ; 164% |
| Qwen2.5 GGUF Q4_K_M | 98.9 / 58.3 | 78.6 | 78.6 | 78.6 | **98.6 (picks CDC twice)** | 98.6 | 157% |
| Qwen3 GGUF Q4_K_M | 98.9 / 79.5 | 88.3 | 88.3 | 88.3 | 98.7 (picks CDC) | 98.8 | 134% |
| Pythia GGUF F16 chain (5) | 99.0 / 93.6 / 98.4 / 98.7 / 91.0 | 96.1 | 93.7 | 93.7 | 93.7 | 97.2 | 91% |
* First-sight cost is paid in full (98-100% for the first quantised file; 71% for BF16 planes); gains start at revision 2. The Z3-28 lock-out reproduces: choosing CDC at first sight (98.8 vs 98.9, a 0.1-point lead) forbids every later delta (Qwen GGUF: 98.6 vs 78.6). A base stored only as CDC chunks would have to be re-stored as components.
* Incremental cost assumes the base is live; a delta alone retains 129-164% of one file (residual + the base tensors it names). Stars amortise: SmolLM2 stores base + 3 fine-tunes in 41.6% of 4 files, the anchor components shared by all targets. Depth: hop h nests h representations; contract maximum 4, an unbounded chain is refused (`ErrDepth`, test).
* Dependency is kind-local: a delta tensor lists its base as an ordinary member (`Stored` SHA, or `Derived` child that rebuilds the exact base bytes); `Reachable` keeps those bytes live (tested at chain depth 3). Nothing names a bucket, key or version; deleting the base artifact deletes nothing a descendant lists.

## 8. Read and performance (single core except analysis; repz3 lab store)
| read | requested | fetched | notes |
|---|---:|---|---|
| whole GET, Qwen 988 MB | 988 MB | 2.00x (residual + base) + 35 KB rep | 345 MiB/s decode; 92-207 MiB/s end to end with DEFLATE records inflated (inflate alone 130-950 MiB/s); PXR container 15.6 MiB/s |
| one tensor (272 MB largest / 229 KB median) | same | 2.00x / 2.15x | residual and base streamed in 256 KiB chunks, SHA-256 verified |
| 4 KiB inside a tensor | 4 KiB | 2.0x (4 KiB residual + 4 KiB base) + one 35 KB rep read | `w` small plane reads; a packed store inflates the whole 0.26-0.9 MB record: 1.1-4.6 ms |
* Decoder: +1.6 MiB live heap, 0.002-0.014x output allocated; hostile recipes refused (chunk <= 16 MiB, ops, widths, sizes). Analysis encoder is *not* bounded: 10.2 GiB peak RSS on the 988 MB pair (whole-tensor buffers + in-memory store, lab debt), 48-177 MiB/s on 4 cores. Range reads verify length, not SHA (a hash needs a sequential pass).

## 9. Generic BASE verdict
No. Z3-29 gives independent evidence from two formats (Safetensors, GGUF) that exact tensor deltas need only an ordinary content-addressed member plus a kind-local recipe; the same 500-line decoder serves both. No duplication, no liveness failure, no namespace dependency, so a generic BASE would add a primitive without simplifying anything.

## 10. Production-candidate matrix and carry-forward
| candidate | decision | exact | storage win | CPU / read | portability | I2 debt |
|---|---|---|---|---|---|---|
| Safetensors structural + plane residual | PROMOTE_TO_I2 | all revisions, chains, shards | 57.6% incr, 41.6% star cum vs CDC 97.5 | ~345 MiB/s, 2x bytes | `st` CORE_PORTABLE; recipe + decoder PORT_SMALL_REFACTOR; codec CORE_PORTABLE; base lookup, admission ANALYSIS_ONLY | encode CPU/memory bound, lineage hint, SHA on range reads |
| Safetensors PXR | DEFER | yes | 49.3% | 15.6 MiB/s | NOT_READY | fast coder |
| GGUF structural + same-type residual | PROMOTE_TO_I2 | all | 58-65% (79.5% K-quant) | 300-380 MiB/s | `ggufz3` CORE_PORTABLE (fuzz + malformed suite); shared decoder | breadth >=7B, IQ; header CDC |
| PyTorch ZIP/storage | KILL | yes | <=0.1 point over CDC | - | `torchz3` CORE_PORTABLE, kept as asset | none |
Reuse audit: unchanged `st/`, `tdelta/`, `ir/`, `lab.ChunkCDC/PackedLen`, `repz3` contract + `Reachable`, `z3fetch`; modified `repz3/rep.go` (+KindModel, 2 lines), `kinds.go` (+2), realworld README; new `ggufz3` (554 lines), `torchz3` (381), `modelz3` (1.6k), `kinds/model.go` (499), `cmd/z3model` (1.5k), 5 scripts. Limits: corpus <=0.6B-parameter models, one producer per GGUF pair, no FP8/integer Safetensors, no real >1 GiB sharded family (shards are re-exports), no tail-latency or concurrent-load measurement. Tests: `ggufz3` (20 s fuzz), `torchz3` (20 s fuzz, `archive/zip` differential), `modelz3` (chains, depth bound, malformed recipes, range reads, liveness). I2 should carry: KindModel recipe + streaming decoder, header-CDC rule, bounded hops (<=3), residual chunk cap, I4 first-sight component form and lineage hints.
