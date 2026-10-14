# Z3-10: tensor-pair delta representation

Laboratory evidence only. `zeros3.go`, the root `go.mod`, the store, manifests, journal and every persistent format are untouched; the codec,
container and tools live under `testing-harnesses/z3` (`tdelta/`, `cmd/z3tdelta`, a 40-line exported view of the existing range coder in `ir/rcpub.go`).
Nothing is wired into persistence.

Target architecture, as asked:

```
target tensor --+-- exact reusable rows
                +-- changed regions --+-- byte-plane XOR delta vs the selected base (native range coder)
                                      +-- raw / standalone fallback
```

```sh
cd testing-harnesses/z3
go run ./cmd/z3fetch -group pythia14m-train,pythia14m-sft-koshirowada,pythia14m-logs-honicky,bert-tiny-family,pythia14m-siblings -out "$TMPDIR/st" -max-mib 1500
go run ./cmd/z3fetch -group pythia70m-train,smollm2-135m -out "$TMPDIR/big" -max-mib 2000      # new in Z3-10F, 1.9 GB
go run ./cmd/z3tdelta pairs|family|sweep|perf -corpus "$TMPDIR/st" [-families ..] [-i N -j M]
go test ./tdelta
```

## What was built

* **PXR** (`tdelta/pxr.go`, ~190 lines): the Z3-10A native codec. The XOR of target and base is coded element by element with the Z3 adaptive range coder (`ir.RCEncoder`, no
  second entropy coder). Per element one *changed* flag (context: last three flags); for changed elements the `w` XOR bytes, most significant first, each as a zero flag and an
  8-bit tree. Contexts are small: plane index, bucket of the byte above in the same element, bucket of the same plane in the previous element, and the **base element's
  exponent-ish bits** (top byte below the sign, 32 levels) because small weights move by more ULPs than large ones. `base == nil` codes the target against zeros (standalone byte planes).
  Exact by construction: XOR of bit patterns only, no float arithmetic anywhere; NaN payloads, -0, subnormals are tested. The stream has no header; `n` and `w` come from the tensor.
  Decoder always yields exactly `n` bytes or `ErrCorrupt`; `bad` symbols (a value the encoder cannot emit) are rejected.
* **Rows** (`tdelta/tensor.go`): for tensors with a native row structure (outermost dimension of a >= 2-D tensor) a recipe of alternating *reuse / delta* run lengths
  (uvarints, reuse first) plus one PXR payload for the concatenation of the changed rows. Runs of equal rows shorter than `MinReuse` fold into the delta. The recipe is tried only if
  some row repeats (one `bytes.Equal` scan).
* **Per-tensor admission** (`EncodeTensor`): candidates `raw`, `standalone` (PXR vs zeros), `same` (bit-identical to the base tensor, 1 byte), `delta`, `rows`; the cheapest *wire* size
  wins, where wire = mode byte + length varint + payload (+ row recipe). `Options.MaxFrac` caps non-raw modes relative to raw; reconstruction CPU is measured separately (below).
* **Whole-file container** (`tdelta/file.go`, laboratory form, not a persistent format): flags + SHA-256 of the one base file + target length + header section (identical to the base header = 1 byte, else raw, else DEFLATE of the
  header XOR) + one tensor record per tensor. The base of each tensor is the **same-name, same-dtype, same-shape tensor of the file's one base file**; nothing else is searched.
  `DecodeFile` rebuilds the target through the Safetensors structure (`st.Parse` on the reconstructed header) and every row of every table here is checked by length, SHA-256 and bytes.
* Tools: `cmd/z3tdelta pairs` (adjacent and anchor pairs: CDC / standalone / DEFLATE plane-XOR "T0" / native whole-tensor "T1" / hybrid with rows), `family` (base policy and chain depth),
  `sweep` (row-reuse thresholds), `perf` (throughput and allocations).

Cut deliberately: the Z3-09 `float-split`, `sparse`, `bytedelta` and `zero-rle` codecs (no measured advantage, Z3-09), a separate sparse/zero-heavy path (Z3-10A T2: PXR's changed flag costs 0.0004 bit per unchanged element; the Siblings pair
with 16 identical tensors codes at 3.8%), a global tensor index, similarity sketches, ANN.

## Real data (pinned, hash-verified, never committed; sources in `realworld/sources.json`)

| group | relationship | files | dtype | size |
|---|---|---|---|---|
| `pythia14m-train` | continued pretraining, 18 log-spaced checkpoints (steps 0..143000) | 18 | F16 | 4-14M params |
| `pythia14m-sft-koshirowada`, `-logs-honicky` | fine-tune chains (Trainer commits) | 5 + 3 | F32 | 14M |
| `bert-tiny-family` | base + continued MLM + task fine-tunes (star) | 7 | F32 | 4M |
| `pythia14m-siblings` | base F16 and two BF16 sibling fine-tunes | 3 | F16 / BF16 | 14M |
| **`pythia70m-train`** (new) | EleutherAI/pythia-70m training checkpoints steps 1000, 2000, 4000, 8000, 143000 | 5 x 269 MiB | **F32** | **70M** |
| **`smollm2-135m`** (new) | HuggingFaceTB/SmolLM2-135M **base vs Instruct** (real SFT/DPO of the base) | 2 x 257 MiB | **BF16** | **135M** |

Z3-10F obtained two materially larger families (70M and 135M parameters, 1.9 GB downloaded once, pinned by commit SHA and the HF LFS SHA-256, licence Apache-2.0, weights not committed).
Not obtained: a >= 50M F16 family and any quantised family (no real corpus material; none fabricated).

## Results

All numbers are the *whole target file* as a percentage of the target's bytes, container overhead included (base reference, header section, tensor records, row recipes);
every file was decoded back and compared (`exact true` on every row). `CDC` is ZeroS3's new-chunk bytes against the same base. `standalone` = PXR of the target with no base. `T0` = the Z3-09 stand-in,
byte-plane XOR + `compress/flate` (best of plane-delta / standalone planes / identical per tensor). `T1` = native PXR whole-tensor delta (best of raw / standalone / identical / delta per tensor).
`hybrid` = T1 plus row-reuse candidates. Rows are *adjacent* checkpoint pairs unless marked `anchor` (= vs the family's first file).

### Continued pretraining by checkpoint distance (Pythia-14M, F16, 26.8 MiB per file)

| distance (steps) | CDC | standalone | T0 plane-XOR+DEFLATE | **T1 native** | hybrid (+rows) |
|---:|---:|---:|---:|---:|---:|
| 1 | 100.0 | 85.0 | 2.6 | **1.4** | 1.4 |
| 2 | 100.0 | 85.0 | 8.0 | **4.7** | 4.7 |
| 4 | 100.0 | 85.0 | 21.7 | **14.2** | 14.2 |
| 8 | 100.0 | 85.0 | 32.6 | **22.4** | 22.4 |
| 16 | 100.0 | 85.0 | 47.8 | **34.5** | 34.5 |
| 32 | 100.0 | 85.0 | 58.4 | **46.6** | 46.6 |
| 64 | 100.0 | 85.0 | 68.8 | **57.5** | 57.5 |
| 128 | 100.0 | 85.0 | 77.5 | **68.3** | 68.3 |
| 256 | 100.0 | 85.0 | 81.7 | **75.1** | 75.1 |
| 488 | 100.0 | 85.0 | 83.5 | 78.4 | 78.4 |
| 1 000 | 100.0 | 85.1 | 85.4 | 80.8 | 80.8 |
| 40 000 (late) | 100.0 | 85.3 | 82.6 | 77.0 | 77.0 |
| 98 000 | 100.0 | 85.2 | 96.2 | **85.2** (= standalone) | 85.2 |
| 1 000 (end of training, 141000 -> 142000) | 100.0 | 85.3 | 69.3 | **61.2** | 61.2 |

Compared with the Z3-09 numbers (best exact delta 2.6 / 8.1 / 21.4 / 32.6 / 46.5 / 68.9 / 77.6 at distance 1 / 2 / 4 / 8 / 16 / 64 / 128) the DEFLATE stand-in reproduces them and the native codec removes another **1.2-13 points**
(a quarter to a half of the remaining cost at distance 1-16). Beyond ~1 000 steps in this F16 model the delta is no better than coding the tensor on its own (standalone 85%); the admission falls back automatically (`bytes by mode` shows 100% standalone at 98 000 steps).

### Other real workload classes (byte-weighted over all pairs of the family)

| workload | dtype | CDC | standalone | T0 | **T1** | hybrid |
|---|---|---:|---:|---:|---:|---:|
| sibling fine-tunes (adjacent, incl. one dtype-changing pair) | F16/BF16 | 98.1 | 58.7 | 33.6 | **31.3** | 31.2 |
| of which BF16 -> BF16 sibling pair (b128 -> b1024) | BF16 | 96.2 | 58.7 | 5.0 | **3.8** | 3.8 |
| fine-tune chain (SFT epochs), adjacent / vs first | F32 | 100 | 68.4 | 49.3 / 59.6 | **47.0 / 56.6** | 47.0 / 56.6 |
| fine-tune chain (logs), adjacent / vs first | F32 | 100 | 71.4 | 53.5 / 59.0 | **49.0 / 54.6** | 49.0 / 54.6 |
| BERT-tiny base + 6 fine-tunes, adjacent / vs base (star) | F32 | 100 / 98.7 | 84.0 | 77.4 / 63.8 | **73.9 / 59.8** | 73.9 / 59.8 |
| **larger: Pythia-70M checkpoints 1k..143k**, adjacent / vs first | F32 | 100 | 42.9 | 46.2 / 46.5 | **41.9 / 42.7** | 41.9 / 42.7 |
| **larger: SmolLM2-135M base -> Instruct** | **BF16** | 100 | 67.3 | 59.7 | **50.7** | 50.7 |

Best individual results: sibling BF16 pair 3.8%, BERT star pair base -> cls-sst2 **32.5%** (rows), SFT epoch pair 40.2%. Whole-tensor exact identity (`same`) appears only for non-trainable buffers and the lr=0 warm-up step (Z3-09: confirmed, ~0% of bytes elsewhere).

### Larger models

* **SmolLM2-135M base -> Instruct (BF16, 257 MiB of tensors, 272 tensors)**: CDC 100%, standalone 67.3%, DEFLATE plane-XOR 59.7%, **native delta 50.7%**; every tensor is changed (no row reuse), exact. This is the real "base + fine-tune" case at 135M parameters and it survives: the
  delta halves the size where CDC saves nothing, native beats DEFLATE by 9 points.
* **Pythia-70M (F32)**: delta vs standalone is nearly a wash (41.8% vs 43.1% at distance 1 000 steps, 41.6% at 4 000, 42.2% at 135 000): in F32 the mantissa noise of a few thousand optimizer steps is already ~full entropy, and what compresses is the
  sign/exponent plane (standalone). The value of the delta in F32 is 1-5 points over the best standalone coding and ~4 over DEFLATE; the real win is against CDC (100%).
  So the F16 early-training signal (down to 1.4%) does **not** carry over to F32 mid-training - that is the main caveat on "the codec generalises": it generalises to fine-tunes and to near checkpoints, not to F32 checkpoints a thousand steps apart.

### Dtypes

| dtype | where | T0 | T1 | note |
|---|---|---:|---:|---|
| **F16** | Pythia-14M training | 2.6 .. 96 (distance dependent) | **1.4 .. 85** | best: tiny perturbations of 16-bit words, low byte noisy, exponent byte quiet |
| **BF16** | sibling pair / SmolLM2 base->Instruct | 5.0 / 59.7 | **3.8 / 50.7** | one mantissa byte holds the change; works in both a near and a far (fine-tune) regime |
| **F32** | fine-tune chains, BERT, Pythia-70M | 40-80 | **32-77** | worst: 2-3 noisy mantissa bytes per element; gains are 2-5 points over DEFLATE and 1-9 over standalone |

### Row-aligned reuse (Z3-10B/C)

Tensor-row segmentation does find exact untouched rows (embedding / output matrices): in the early Pythia pairs the `rows` mode is chosen for 46% of the bytes (distance 4-64), the SFT pairs for 46-92%, BERT base -> sst2 for 89%.
But **it adds almost nothing to the size**, because PXR already codes an unchanged element for ~0.0004 bit: net bytes saved by choosing rows over whole-tensor delta (recipe included):

| pair | delta only (whole tensor) | bytes saved by rows | row recipe | avg reuse run |
|---|---:|---:|---:|---:|
| SFT c1 -> c2 / c2 -> c3 / c3 -> c4 | 43.3 / 40.2 / 47.1 % | **7 053 / 7 035 / 6 785 B** (0.03% of the file) | 19 KB each | 3.5 rows |
| BERT base -> cls-sst2 (star) | 32.5 % | **4 057 B** | 11.4 KB | 3.3 rows |
| Siblings b128 -> b1024 | 3.8 % | 1 794 B (0.17%) | 2.8 KB | 36.5 rows |
| Pythia early pairs (rows chosen on 46% of bytes) | | < 0.1 point | 0.7-1.4 KB | |

The thresholds `MinReuse` = 1, 2, 4, 8, 32 rows give identical sizes to 0.1%. Rows therefore are a **segmentation primitive, not a size win**: what they buy is (a) no need to read or decode unchanged rows, (b) a native unit for partial updates / range reads. The measured CPU effect is in the performance table.
Reference overhead per tiny row is avoided by construction (runs, not per-row references): 19 KB of recipe for a 50 304-row embedding with ~3.5-row reuse runs is the price - a quarter of the saving it buys, which is why this is not the headline.

### Base selection and delta-chain depth (Z3-10D)

Candidates are constrained to *same name, dtype, shape in the file's one base*. Policies over whole families (physical size of all files incl. the anchor, as % of raw; "depth" = files decoded before the target, "restore" = decode wall time of the latest file through its dependency path, measured on a loaded shared machine: compare relatively):

| family | raw CDC (all earlier files) | anchor (all vs first, depth 1) | chain (each vs previous) | bounded D=2 | bounded D=3 | any earlier file (oracle) | standalone only |
|---|---:|---:|---:|---:|---:|---:|---:|
| Pythia-14M training (18 ckpts, F16) | 94.4 | 56.6 (restore 7.8 s) | **50.8** (depth <= 17, restore 41.7 s) | 54.1 (depth 2) | 52.5 (depth 3, 10.1 s) | 50.8 (= chain) | 85.1 |
| SFT chain (5, F32) | 100.0 | 60.2 | **52.4** (depth 4, 22.9 s) | 56.0 | 53.7 (10.9 s) | 52.2 | 69.6 |
| logs chain (3, F32) | 100.0 | 55.5 | 55.5 | 55.5 | 55.5 | 55.5 | 70.4 |
| BERT-tiny star (7, F32) | 99.1 | **65.6** | 75.4 (depth 6) | **65.6** | 65.6 | 65.6 | 84.0 |
| Pythia siblings (3, F16 + 2 x BF16) | 98.7 | 67.6 | **49.3** | 49.3 | 49.3 | 49.3 | 67.6 |

* **Chain family** (training checkpoints, Trainer epochs): previous-file base wins by 4-8 points over anchor, but only with unbounded depth; **bounded depth 3 (an anchor plus a previous-file hop) keeps 71-83% of the chain's gain over the anchor** (52.5 vs 50.8 vs 56.6; 53.7 vs 52.4 vs 60.2)
  at a fixed restore cost (10 s instead of 42 s for 18 checkpoints). Depth > 3 buys <= 1.7 points.
* **Star family** (BERT base + task fine-tunes): the original base beats the chain by 10 points (75.4 chain vs 65.6 anchor): a fine-tune chain between unrelated tasks is worse than going back to the base.
* The oracle ("any earlier file") never beat the better of chain/anchor by more than 0.2 points on these families, so a global base search buys nothing; the Siblings case wins from a *sibling* (the previous file), not from the original base (a dtype change), which "previous same-name tensor" already finds.
* Conclusion: **one level (anchor) is sufficient for star families; for chains, anchor + a bounded previous-file hop (depth <= 3) is within ~2 points of unbounded chaining.** No production policy is frozen here.

### Metadata and reference overhead (Z3-10E)

Per file container (any of the 7-76-tensor files): fixed part (flags, 32-byte base digest, length varint) + header section + one record per tensor. Measured (`meta B` column): **~250-300 bytes per file** when the header equals the base's (1 byte; Pythia, SmolLM2 1 067 B with a
differing header), 5 kB when the header differs (BERT: DEFLATE of the XOR header) - 0.001-0.03% of a 17-270 MiB file. Per tensor: 1 mode byte + a 1-3 byte length. Row recipes: 1.4 KB (Pythia early), 11-19 KB (SFT / BERT embeddings).
Admission therefore weighs payload bytes; the references are noise next to them, unlike in Z3-11 where references dominate. The `bytes by mode` columns of `pairs` show the admission at work: dense continued pretraining -> 100% `delta`; a far
checkpoint -> 100% `standalone` (so a "far checkpoint at 90%" does not become a base-dependent object); rows on exactly the tensors with untouched rows.

### Performance (Z3-10H)

One core, otherwise idle 4-core VM, tensor bytes only (the Safetensors header and container are noise), the same tensors through each codec; `out` is the size of the codec's output, MiB/s is of tensor bytes.
Allocation is `TotalAlloc` per MiB of tensor bytes, so 1.0 means "just the output buffer".

| pair | dtype | T0 DEFLATE plane-XOR: out % | enc / dec MiB/s | **T1 PXR delta**: out % | enc / dec MiB/s | hybrid tensor (tries standalone + delta + rows): enc / dec MiB/s | PXR alloc / MiB (enc, dec) |
|---|---|---:|---:|---:|---:|---:|---:|
| Pythia-14M step 4 -> 8 | F16 | 21.7 | 71 / 105 | 14.2 | 32 / 32 | 6 / 25 | 1.1 / 1.3 |
| Pythia-14M step 1000 -> 2000 | F16 | 85.4 | 58 / 117 | 80.9 | 8 / 8 | 4 / 8 | 4.5 / 1.4 |
| SFT epoch c1 -> c2 (rows chosen) | F32 | 45.4 | 92 / 163 | 43.3 | 13 / 15 | 4 / 14 | 2.5 / 1.2 |
| siblings b128 -> b1024 (rows chosen) | BF16 | 5.0 | 143 / 153 | 3.8 | **91 / 85** | 9 / **101** | 0.4 / 1.2 |
| BERT-tiny base -> cls-sst2 (rows chosen, 89% of bytes) | F32 | 34.5 | 87 / 100 | 32.5 | 19 / 19 | 5 / 19 | 2.1 / 1.3 |
| **SmolLM2-135M base -> Instruct** | BF16 | 59.7 | 112 / 209 | 50.7 | 13 / 13 | 6 / 13 | 2.7 / 1.1 |
| **Pythia-70M step 1000 -> 2000** | F32 | 46.4 | 73 / 154 | 41.8 | 13 / 13 | 7 / 13 | 2.4 / 1.0 |

Standalone PXR (no base) runs at 8-13 MiB/s both ways. Observations:

* PXR costs **8-19 MiB/s on dense changes** (every element changes, ~10-30 binary range-coder decisions per element) and **85-91 MiB/s on sparse ones** (the siblings pair: most elements unchanged -> one decision each); the DEFLATE stand-in is 58-143 MiB/s to encode and 100-209 MiB/s to decode. A 257 MiB model file takes ~20 s to decode with PXR (one core) against ~1.3 s with DEFLATE.
  The profile (`go test -bench PXR -cpuprofile`) is ~55% inside the shared binary range coder (encode/decode, probability, update), ~20% the codec loop, ~10% interface dispatch; no single hot spot, so no cheap micro-optimisation was taken. Tensors are independent (and rows within a tensor too), so decode parallelises by tensor; the numbers above are one core.
* Hybrid admission pays the encode time of every candidate (standalone + delta + rows = 3 passes: 4-9 MiB/s) but decodes at the speed of the chosen mode. Gating `rows` behind the cheap "does any row repeat?" scan (kept) removes the third pass on dense tensors; a similar gate on `standalone` was not added.
* Rows do not make decoding faster in these pairs: SFT 14 vs 15 MiB/s, BERT 19 vs 19; only the sparse sibling pair gains (101 vs 85, +19%). Skipping unchanged rows is worth little when the changed rows dominate the bytes.
* Allocation: decode ~1.0-1.4x the output (the output buffer plus ~66 KB of lazily allocated models per tensor); encode 0.4-4.5x (the growing output slice; a size hint would remove most of it). Metadata overhead per tensor: 1-3 bytes of record.

Entropy coding is *not* fast enough for a network-bound read path (> ~100 MB/s); it is fast enough for cold, storage-bound objects where 2-11 points of the file matter more than 20 s of one core. That decision is left to the admission step; I stopped at measurement as instructed.

## Q1-Q10

**1. Did the native byte-plane codec beat the DEFLATE baseline?** Yes, on every real tensor class and dtype, by 2-11 points of the file (a quarter to a half of the remaining cost at the best pairs): F16 near pairs 1.4 vs 2.6 (distance 1), 14.2 vs 21.7 (4), 57.5 vs 68.8 (64); BF16 3.8 vs 5.0 and 50.7 vs 59.7;
F32 47.0 vs 49.3 (SFT), 73.9 vs 77.4 (BERT), 41.9 vs 46.2 (70M). Native delta is smaller than the DEFLATE delta on 100.0% of the tensor *bytes* in every measured pair (DEFLATE wins only on tiny tensors: 37 of 76 tensors in Pythia-14M step 4 -> 8 but 0.1% of the bytes; 58 of 272 in SmolLM2 and 42 of 76 in Pythia-70M, both <0.05% of the bytes), and on 76 of 76 / 39 of 39 tensors in the SFT, sibling and BERT pairs. It is also ~5-15x slower to decode on dense changes (8-19 vs 100-209 MiB/s) and 2-10x slower to encode (8-32 vs 58-143 MiB/s), so the gain is a CPU-for-bytes trade.

**2. How much does row reuse add?** Net bytes: 0.03% of the file (SFT embeddings), 0.17% (sibling pair), 0.1% of a point or less elsewhere, with a 1-19 KB recipe. It identifies the untouched rows correctly but PXR already makes them nearly free, so rows are a segmentation/skipping primitive, not a size win.

**3. Which real workload classes win?** Against CDC (100%): sibling fine-tunes from a shared base (3.8%), near training checkpoints (1.4-22% at distance 1-8), epoch-to-epoch fine-tunes (40-47%), base -> instruct SFT at 135M parameters (50.7%), star fine-tunes with some untouched layers (BERT 32-65%).
What does not win: F32 checkpoints a thousand steps apart (delta ~ standalone), far checkpoints (> ~1 000 steps in F16, falls back), model families with different dtypes (dtype change = no base).

**4. What checkpoint distance remains useful?** F16: through ~256 steps (<= 75%, i.e. >= 10 points under standalone 85%); the headline gains are at <= 16 steps (<= 35%). A late-training pair 1 000 steps apart is still 61% (F16). F32 (70M): 1 000-4 000 steps = ~42% = barely under standalone (43%).

**5. What dtypes perform best/worst?** F16 > BF16 > F32 (table above): F32 changes 2-3 mantissa bytes with near-random content; F16/BF16 change one low byte.

**6. Is one-level / base-anchored delta sufficient?** For star families yes (and better than a chain). For chains, anchor alone costs 4-6 points more than an unbounded chain; anchor + one previous-file hop (D=3) recovers ~70-80% of that for a fixed, small restore cost. Unbounded chains buy <= 1.7 more points at 4-5x the restore time.

**7. What is the actual metadata/reference overhead?** ~250-300 B per file + ~4 B per tensor + 1-19 KB per row recipe (0.001-0.1% of the file). Negligible against payload at tensor granularity.

**8. Does the result survive a larger model family?** Partly. The fine-tune result (SmolLM2-135M base -> Instruct, BF16: 100 -> 50.7%, native beats DEFLATE by 9 points) and the "native > DEFLATE" ordering do. The F16 early-training magnitude does not carry to F32 70M checkpoints a thousand steps apart (41.9% vs 42.9% standalone). We have no >= 50M F16 family and no quantised one.

**9. What should be retained for eventual production representation?** (a) Exact per-tensor representation = {raw, standalone planes, identical, delta vs one same-name/shape base}, chosen by wire size with a base digest as the only lineage reference; (b) PXR's design: per-element changed flag + MSB-first byte planes + base-exponent context on the
existing range coder; (c) the bounded policy "anchor + at most 2 hops" (depth <= 3); (d) row runs only as an optional segmentation/skip mechanism, not as a size feature; (e) whole-file reconstruction through the Safetensors structure with the header delta.

**10. What should be discarded?** Whole-tensor identity as a mechanism; sparse/zero-RLE/float-split/bytedelta codecs; row references per row; generic fixed blocks; global similarity search, sketches, ANN; unbounded chains; sign/exponent/mantissa splitting. The DEFLATE stand-in survives only as the CPU-cheap alternative if decode throughput matters more than 2-11 points.

## Limits stated plainly

* PXR is 8-19 MiB/s on dense changes on one core in this lab; a production codec needs a cheaper symbol coder or a byte-oriented fast path (the profile is ~55% in the shared binary range coder). It was **not** optimised beyond the design choices (flag for unchanged elements, row gate).
* Admission uses wire size only; a CPU-aware threshold (`Options.MaxFrac`, implemented, measured `bytes by mode` only) is not tuned.
* 270 MiB files, not multi-gigabyte shards; no sharded checkpoints, no LoRA/adapter merges, no quantised dtypes (GGUF later).
* Chain policies were evaluated on 3-18 files per family.

## Tests actually run

`go test ./tdelta` (exact roundtrip of PXR for w = 1/2/4/8, specials NaN payload / -0 / subnormal, canonical re-encode of valid payloads, truncation + 400 bit-flips never wrong-length, row recipe malformed cases, per-tensor modes, whole-file exact reconstruction with missing /
reshaped / new tensors, wrong base refused, truncated containers refused), and **one 20 s smoke fuzz** of the new container decoder (`FuzzDecodeFile`: anything it accepts is a valid Safetensors file whose re-encoding round-trips). `st` was not changed, so its parser was not
fuzzed; no DEFLATE fuzzing. Root ZeroS3 tests were not run (production code untouched).
