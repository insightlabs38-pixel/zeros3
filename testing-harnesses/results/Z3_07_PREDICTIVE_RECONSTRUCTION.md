# Z3-07: predictive DEFLATE reconstruction

Laboratory evidence only (same corpora, accounting and caveats as `Z3_06_DEFLATE_IR.md`; nothing in `zeros3.go`, the root
`go.mod` or any persistent format changed). Prediction is **compression of the exact reconstruction metadata, never the
correctness model**: every predicted stream is decoded, replayed through the exact writer, checked against the CRC-32 and length
in the skeleton, and compared with the original bytes by the lab. The explicit E2 form of Z3-06 stays the universal fallback
(it is also the per-token escape inside a predictive unit), and raw CDC stays the floor.

```sh
cd testing-harnesses/z3
go run ./cmd/z3ir predict -corpus C                  # profiles, reset penalty, correction categories, speed (7A-7D, 7F)
go run ./cmd/z3ir predict -corpus C -ablate          # feature ablation (7A)
go run ./cmd/z3ir family  -corpus C -reps irp-mono,irp-tok2k,irp-tok4k,irp-tok16k,irp-tok64k -json p.json
go run ./cmd/z3ir chooser -from r.json,p.json,low.json -predictive irp-tok16k -explicit ir-tok4k-e2   # 7E
```

## Answer to the central question

**Yes.** With the predictor the exact reconstruction cost stops scaling with the stream and starts scaling with the changed
content: on a 4 MiB-base Go L6 family (12 MiB plaintext per revision, 35 B per reference, update revisions r1..r3, % of their
compressed bytes):

| 4 MiB logical-edit family (Go L6) | insert-mid | insert-begin | delete | replace | append |
|---|---:|---:|---:|---:|---:|
| CDC incremental | 52.2% | 100.1% | 50.3% | 34.4% | **2.3%** |
| Preflate + CDC incremental | 12.2% | 13.3% | 10.0% | 13.7% | 10.0% |
| Z3 explicit block-local (tok4k) | 15.0% | 26.5% | 16.8% | 3.7% | 3.0% |
| **Z3 predictive block-local (tok16k)** | **3.4%** | **4.9%** | **4.2%** | **1.5%** | **0.9%** |
| plaintext lower bound | 0.7% | 0.4% | 1.4% | 0.8% | 0.3% |

At the production 96 B reference size: insert-mid 4.6% (tok16k), insert-begin 6.2%, delete 5.4%, replace 2.7%, append 2.1%
(CDC 52.3 / 100.1 / 50.4 / 34.4 / 2.4; Preflate 12.4 / 13.6 / 10.3 / 14.0 / 10.2). Python-zlib L6 (6 MiB plaintext per
revision, coarser plaintext CDC so the lower bound is 3.0%): insert-mid CDC 54.3, Preflate 5.8, **predictive tok16k 3.9**, lower bound 3.0.

How the remaining gap to the plaintext lower bound splits (Go L6 insert-mid, tok16k, 35 B/ref: 3.4% total):
new semantic plaintext 0.6% (this is the lower bound), new reconstruction units 2.0% (producer noise, i.e. corrections), references 0.7%
(~190 plaintext chunks and ~670 units per revision), skeleton 0.1%. So **reconstruction metadata is ~2.8 of the 3.4 points; semantic
plaintext is 0.6**; the plaintext is now the smaller term.

* Predictive wins **41 of 45 family-revisions** against raw CDC and explicit IR at 35 B/ref (tok16k); explicit wins none; CDC wins the
  4 where the transform loses (gzip append 0.4% vs 1.3%, one ZIP add where the new member content dominates).
  Byte-weighted over all 15 families: CDC 38.4%, Preflate 8.2%, explicit tok4k 11.8%, **predictive tok16k 3.1%**, per-revision best-of-three
  (the chooser) **3.1%**, plaintext lower bound 1.3%.
* Entry cost is no longer a problem: r0 costs **108-115%** of the original stream (77% for the fixed emitter; Preflate 118%, explicit IR 208%), so the predictive family is
  cheaper than CDC cumulatively after one update for every non-append family. Over r0 + 3 updates: Go L6 insert-mid **30.7%** vs CDC 64.2% vs
  Preflate 38.7%; insert-begin 31.9 vs 100.1 vs 39.6; replace 29.3 vs 50.8 vs 39.8; fixed emitter 20.0 vs 62.8 vs 19.1; append 28.9 vs
  26.7 (CDC) vs 37.1 (Preflate).
* Locality survives the predictor: resetting predictor state at unit boundaries costs 0.6-1.6 points of the stream at 16 KiB and 1.5-2.7
  at 4 KiB (7D), and buys the reuse above.

## What was built (`ir/predict.go`)

For each token position the decoder rebuilds the LZ77 candidates itself from the plaintext it holds (a 32 KiB hash chain over
`[unit start - 32 KiB, unit end)`, rebuilt at every unit start, so predictor state is cheap to initialise at any boundary and a unit
depends on at most 32 KiB of plaintext before it). A unit stores only corrections, bit-level coded with the Z3-06 range coder:

| correction | coded as |
|---|---|
| literal vs match | one adaptive bit; context = longest-candidate bucket (8) x lazy-lookahead hint (none / next position not longer / longer) x literal-run bucket (6) |
| which match | unary over the ranked candidates; context = repeat-distance, candidate length class vs the longest, candidate length bucket, rank; escape = no candidate explains it |
| length | one bit "equals the candidate's full length", else a gamma-coded delta |
| escape | the explicit E2 coding of the whole token (also used for the non-canonical length-258 spelling) |
| block boundaries, BFINAL/BTYPE, dynamic headers, padding | **not predicted**: exact in the skeleton (40-48 B per dynamic block, 0.0-0.1% of the stream) |

Compressor-family **profiles** are parameter sets of one engine (min candidate length, chain depth, lazy hint, candidate ordering, frontier
filter, insertion policy), carried in each unit's header byte, chosen per stream from a sample of units, never required for correctness:

| id | name | parameters | intent |
|---|---|---|---|
| 0 | generic | 3-byte hash, chain 128, lazy hint, Pareto frontier, nearest first | unknown producers |
| 1 | zlib-like | chain 256, lazy, frontier, longest first | zlib/GNU gzip/Info-ZIP levels 4-9, Go 7-9 |
| 2 | go-like | 4-byte hash, chain 32, lazy, **all** candidates, nearest first | Go levels 1-6 table matchers |
| 3 | zlib-fast-like | chain 8, no lazy, frontier, interior of matches over 4 bytes not inserted | zlib/gzip levels 1-3 |

No libdeflate-like profile: no libdeflate producer was available, and an unmeasured profile would be an unsupported claim.

Safety: candidate work is budgeted per unit (`512 x plain length + 1 MiB` compared bytes) so adversarial plaintext cannot make decoding
unbounded (`TestPredictorWorkBudget`); the encoder fails with `ErrLimit` and the object falls back to the explicit form.

## 7A. Which predictions matter (ablation)

Whole-stream predictor, best profile per stream, % of compressed bytes (bits/token):

| variant | Go L1 | Go L6 | Go L9 | fixed emitter | zlib L1 | zlib L6 | zlib `Z_FILTERED` | GNU gzip L6 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| full model | 11.2 (1.31) | 6.2 (0.93) | 2.3 (0.33) | 1.1 (0.23) | 1.6 (0.26) | 0.6 (0.10) | 1.8 (0.20) | 0.5 (0.09) |
| no lazy-lookahead hint | 11.2 | 6.4 | 2.8 | 1.2 | 1.6 | **1.8** | 2.2 | **1.7** |
| no literal-run context | 11.8 | 6.3 | 2.3 | 1.1 | 1.6 | 0.6 | 1.8 | 0.5 |
| no repeat-distance context | 11.2 | 6.2 | 2.3 | 1.1 | 1.6 | 0.6 | 1.8 | 0.5 |
| no length contexts | 11.6 | **8.1** | 2.8 | 1.1 | **4.1** | 0.6 | 1.8 | 0.5 |
| rank only (no candidate contexts) | 11.9 | **9.1** | 2.8 | 1.1 | 4.2 | 0.6 | 1.8 | 0.5 |
| no contexts at all | 12.4 | 9.3 | 3.4 | 1.2 | 4.3 | 1.8 | 2.2 | 1.8 |

* The lazy-matching hint (will the next position match longer?) is worth 3x on classical zlib streams; candidate length contexts
  are worth 30-45% on Go and level-1 matchers; the literal-run (skip) context matters only for Go L1; a repeat-distance context
  is worth nothing here because the corpus has no repeated distances.
* Simple predictors on Go L6 (% of compressed): explicit E2 92.5 -> ranked candidates with no contexts at all 9.3 -> + match-flag
  contexts (rank only) 9.1 -> + candidate-length contexts 6.2; candidate ordering/filtering: nearest-first all candidates (go-like) 6.2 vs
  frontier longest-first (zlib-like) 15.2 vs frontier nearest-first (generic) 13.9 shows that Go picks *dominated* candidates (a farther candidate that is not longer than a nearer one): its
  tables see only part of the history, so a "Pareto" candidate filter is wrong for Go and right for zlib.

## 7B/7C. Correction cost and producer dependence

Whole-stream predictor, % of the compressed stream (bits per token); Preflate = reconstruction blob (Z3-04 oracle, same streams):

| producer | Huffman bits/token | Preflate blob | explicit E2 | generic | zlib-like | go-like | zlib-fast | **auto (best)** | profile chosen |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| Go L1 | 11.66 | 20.4 (2.38) | 77.4 | 31.4 | 32.0 | 11.2 | 33.2 | **11.2 (1.31)** | go-like |
| Go L6 | 14.89 | 12.9 (1.93) | 92.5 | 13.9 | 15.2 | 6.2 | 42.5 | **6.2 (0.93)** | go-like |
| Go L9 | 14.61 | 5.9 (0.86) | 89.9 | 2.3 | 2.8 | 2.3 | 55.9 | **2.3 (0.34)** | generic |
| fixed-Huffman emitter | 20.12 | 0.0 (0.00) | 76.4 | 1.1 | 7.0 | 19.3 | 29.1 | 1.1 (0.23) | generic |
| python zlib L1 | 16.09 | **0.0 (0.00)** | 96.0 | 31.2 | 32.2 | 24.9 | 1.6 | 1.6 (0.26) | zlib-fast |
| python zlib L6 | 16.41 | 2.8 (0.45) | 96.0 | **0.6** | 0.6 | 3.1 | 56.3 | **0.6 (0.10)** | generic/zlib |
| python zlib L9 | 16.42 | 2.7 (0.45) | 96.0 | 0.6 | 0.6 | 3.1 | 56.2 | **0.6 (0.10)** | generic/zlib |
| zlib memLevel 1 | 17.79 | **4.9 (0.87)** | 98.5 | 14.4 | 14.7 | 17.6 | 60.0 | 14.4 (2.56) | generic |
| zlib `Z_FILTERED` | 11.33 | 6.6 (0.75) | 75.2 | 1.8 | 1.8 | 1.9 | 50.9 | **1.8 (0.20)** | generic/zlib |
| zlib `Z_RLE` | 4.73 | 0.0 | 0.3 | 0.3 | 0.3 | 0.3 | 0.3 | 0.3 (0.01) | any |
| zlib `Z_FIXED` | 20.42 | 2.2 (0.45) | 77.0 | 0.4 | 0.4 | 2.4 | 45.0 | **0.4 (0.08)** | generic/zlib |
| GNU gzip L1 | 16.09 | **0.0 (0.00)** | 96.0 | 31.1 | 32.1 | 24.8 | 1.5 | 1.5 (0.24) | zlib-fast |
| GNU gzip L6 / L9 | 16.41 | 2.7 (0.45) | 96.0 | **0.5** | 0.5 | 3.1 | 56.1 | **0.5 (0.09)** | generic/zlib |
| Info-ZIP L6 | 16.40 | 2.7 (0.45) | 96.0 | **0.5** | 0.5 | 3.0 | 56.2 | **0.5 (0.09)** | generic/zlib |
| GNU gzip L1, repeat-replace | 15.37 | 0.1 (0.02) | 83.2 | 28.1 | 29.7 | 26.0 | 0.2 | 0.2 (0.03) | zlib-fast |

* **Best predictor: the auto-chosen profile of one engine.** Correction cost vs Preflate's effective bits/token: Go L6 **0.93 vs 1.93**, L1
  **1.31 vs 2.38**, L9 **0.34 vs 0.86**, zlib/GNU gzip/Info-ZIP L6-L9 **~0.09-0.10 vs 0.45** (4.5x fewer bits), `Z_FILTERED` 0.20 vs
  0.75. Preflate is better where it models the producer exactly or where blocks dominate: zlib/gzip level 1 (blob ~0 vs 1.5-1.6%), the
  custom fixed emitter (0 vs 1.1%), zlib memLevel 1 (4.9 vs 14.4%: 4048 tiny blocks, so the skeleton's block table is 11.9% of the stream;
  a header/boundary predictor, deliberately not built here, is the fix).
* Producer dependence is large but graceful: the wrong profile is 10-50x worse (e.g. go-like on zlib L6: 3.1 vs 0.6; generic on Go L6: 13.9
  vs 6.2), the generic profile never fails (worst case it escapes into explicit E2 per token), and an unknown producer degrades to between the
  explicit form (76-98%) and the generic result. Profile selection from 8 sampled units picked the best profile on every stream above.
* Where the bits go (Go L6): match flag 0.18 bit/token, candidate index 0.98 bit/match, length 0.001, escapes ~0 (0.00-0.01% of matches);
  zlib L6: 0.054 / 0.025 / 0.001. Candidate choice, not length or flags, is the Go residual.

## 7D. Predictor reset at unit boundaries

Auto profile, % of the compressed stream, units not shared (so the increase is purely the cost of independence):

| family | whole stream | tok16K | tok4K | tok2K | reset penalty 16K / 4K / 2K (points) |
|---|---:|---:|---:|---:|---|
| Go L1 | 11.2 | 12.5 | 13.9 | 15.0 | 1.2 / 2.7 / 3.7 |
| Go L6 | 6.2 | 6.8 | 7.7 | 8.3 | 0.6 / 1.5 / 2.1 |
| Go L9 | 2.3 | 3.9 | 5.0 | 5.8 | 1.6 / 2.7 / 3.5 |
| fixed emitter | 1.1 | 1.6 | 2.4 | 3.0 | 0.4 / 1.2 / 1.8 |
| ZIP of Go L6 members | 6.2 | 6.8 | 7.5 | 8.2 | 0.6 / 1.3 / 2.0 |
| python zlib L6 | 0.6 | 1.3 | 2.2 | 3.0 | 0.7 / 1.6 / 2.4 |
| python zlib `Z_FILTERED` | 1.8 | 3.7 | 5.8 | 7.2 | 1.9 / 4.0 / 5.4 |
| zlib / gzip level 1 | 1.5-1.6 | 13-14 | 26 | 31 | **12 / 24 / 30** |
| GNU gzip -1 replace | 0.2 | 4.0 | 11.7 | 17.2 | 3.8 / 11.5 / 17.0 |

* The penalty is the learning cost of the adaptive models (~100 contexts) plus a cold chain state: 0.6-1.9 points at 16 KiB for the
  well-modelled producers - a few hundred bytes per unit - and it is **repaid many times** by locality: whole-stream predictor state is
  sequentially dependent, so its units could not be reused at all (one edit changes every later byte of one monolithic blob), while reset
  units reuse like Z3-06's: Go L6 insert-mid 6.9% (irp-mono, one blob re-sent) vs 3.4% (tok16k) incremental.
* **Exception: zlib/gzip levels 1-3** (sparse insertion: a match longer than `max_insert_length` does not enter the hash chain). The decoder
  does not know the earlier token boundaries, so its rebuilt chain over the previous 32 KiB differs from the compressor's and the
  mismatch persists for ~32 KiB (escapes decay over the first 20 KiB of every unit). A greedy simulation of the window (implemented,
  `simulateWindow`) cuts the 16 KiB penalty from 13.4 to 11.2 points but does not fix it; exact emulation of zlib's `deflate_fast` decisions
  (nice length, chain limit) or carrying the chain-insertion bitmap in the unit index would. The chooser handles this correctly: for such
  streams monolithic predictive IR (1.5%) or Preflate (0%) wins and block-local units lose.
* Unit-local reuse, measured as lab cost (35 B/ref, Go L6 insert-mid): monolithic 6.9%, tok64k 3.9, tok16k 3.4, tok4k 4.4, tok2k 6.6, tok1k 9.8;
  insert-begin: 6.6 / 6.6 / 4.9 / 5.0 / 6.7 / 9.7. The optimum is 16-64 KiB, larger than the explicit codec's, because predictive units are
  small (tens to hundreds of bytes) and references dominate below 16 KiB; at 96 B/ref the optimum is 16-64 KiB as well (4.6 / 4.3).
  Units under 64 B are inlined in the recipe.

## 7E. Adaptive chooser (laboratory)

Per revision, the chooser takes the minimum physical cost of {raw CDC, block-local explicit IR (tok4k), block-local predictive IR} where
each representation's cost is measured against that representation's own family store (steady state), plus a realisable
"commit after r1" policy: stay on CDC unless the first update costs <80% of CDC under a transform, then pay that transform's r0 entry cost.
35 B/ref, predictive tok16k, 15 families x 3 revisions = 45 family-revisions:

| | CDC | Preflate+CDC | explicit tok4k | predictive tok16k | per-revision best of three | plaintext lower bound |
|---|---:|---:|---:|---:|---:|---:|
| byte-weighted incremental | 38.4% | 8.2% | 11.8% | 3.1% | **3.1%** | 1.3% |
| wins | 4 (9%) | - | 0 (0%) | 41 (91%) | | |

The commit policy over r0..r3 (including entry costs): **28.7%** (14 of 15 families transformed) vs all-CDC **53.9%**. With tok4k predictive units
the win split is CDC 22% / explicit 24% / predictive 53% (explicit wins mostly the ZIP-member families, where unit references are cheaper
than the packed tiny predictive units), and at 96 B/ref (tok16k) CDC 16% / explicit 0% / predictive 84%. Where the chooser keeps CDC: **append**
(`gzip-L6-append` 0.4% vs 1.3% for any transform; `deflate-L6-append` is the one append family where the predictive tok16k/64k beats CDC:
0.9 / 0.5 vs 2.3%), and ZIP adds/changes whose cost is the new member content. Raw CDC remains mandatory.

CPU/read cost the chooser would face (35 B/ref rows): analysis 250-360 ms per MiB of compressed input for predictive (190 explicit; Preflate 145-180),
reconstruction 10-12 MiB/s of plaintext (explicit 16-19; Preflate recreate was 70-130 ms/MiB compressed in Z3-04); a read of a predictive
object fetches the plaintext chunks (about the compressed size after packing) plus ~0.5-2% of unit bytes, versus the compressed size for CDC.

## 7F. Performance and memory (single thread, 4-core shared VM)

| | predictive | explicit E2 | Preflate (oracle) |
|---|---:|---:|---:|
| analyze / encode, MiB/s of plaintext | 8-10 (Go L1-L9, zlib L4-9), 12 zlib level 1, 3 for the pathological `Z_RLE`; 3.5 for ZIP members (many small streams) | 8-10 | 5-11 MiB/s of *compressed* input (Preflate analyze), i.e. ~15-30 of plaintext |
| decode + replay + CRC, MiB/s of plaintext | 10-13 (21 zlib level 1, 54 for run-dominated streams) | 14-17 | recreate 70-130 ms/MiB comp (~8-14 MiB/s comp, ~25-40 plain) |
| allocation while encoding | 7-12 MiB per MiB of plaintext (token and run copies; 25 MiB for ZIP members) | - | - |

* **The candidate search is the dominant cost**: 77% of encode time in the profile (chain walk, byte comparison), 22-25 compared bytes
  per plaintext byte for Go matchers, 7 for zlib level 4-9 after the quick reject, 2.3 for zlib level 1, 23 for the pathological `Z_RLE`. Word-at-a-time comparison and zlib's quick reject improved
  Go-like encoding from 10.7 to 13-18 MiB/s; dropping the lazy lookahead gains another 1.4x at ~3% size. The native predictor/replayer is
  therefore **not** faster than Preflate (about 1.5-3x slower at encode, 2-3x at decode), contrary to the hope; it is slower because it prefers exhaustive
  candidate generation to cloned compressor internals. Speed was not traded for ratio.
* Retained memory is dominated by the whole-stream in-memory parse (4 B per token + the plaintext); the predictor adds 64K x 4 B + 32K x 4 B
  of hash tables per live unit window. Required state for a future streaming implementation: inflate window (32 KiB) + current block
  header; per unit, the plaintext range `[start - 32 KiB, end)` and its hash chains (rebuilt, ~0.4 MB); the rolling cut hash; the block table.

## Exactness, fuzzing, malformed input

* All predictive rows (Go, gzip, ZIP, python zlib, GNU gzip, Info-ZIP; every profile and policy) rebuilt exactly (length, SHA-256, bytes).
* Tests: round trips of every profile x {stream, 8 KiB units} over Go levels 0-9, Huffman-only, mixed stored, fixed emitter, empty,
  repeat; non-canonical length 258; corruption (400 random mutations of skeleton/unit/truncated unit/plaintext per codec never produce a
  different "successful" stream, every skeleton prefix rejected); decoders on random garbage never claim a wrong length; work-budget test;
  predictive units are 58x smaller than explicit on a Go L9 sample.
* Fuzz targets (`FuzzUnitDecode`, `FuzzSkeleton`, `FuzzReconstruct`, `FuzzEncodeDecode`) cover E1/E1+flate/E2 and the predictive codec: arbitrary bytes never
  panic, work and allocation are bounded by the supplied plaintext, valid encodings decode exactly and replay exactly, success implies a valid
  DEFLATE stream with the supplied plaintext, truncated and corrupted metadata fail. Campaign: 4 min x 2 workers per target; `FuzzUnitDecode` 4.6 M execs,
  `FuzzReconstruct` 0.6 M and `FuzzEncodeDecode` 0.22 M execs clean; `FuzzSkeleton` (5.7 M execs after the fix, clean); `FuzzSkeleton` found **one real defect** (non-zero padding bits in a dynamic header and over-long varints were
  accepted, so the same skeleton had several encodings): both are now rejected and the failing input is kept as a regression seed.

## Evidence collected on the side

* **Pythia-14M checkpoints** (steps 1000/2000/3000/4000/143000, pinned commits, 5 x 28.1 MB, SHA-256 verified by `z3fetch`): existing Z3-02 baselines
  only, `z3lab` update revisions r1..r4: CDC 100.1% (107.47 of 107.36 MiB packed), fixed-64k 100.1, struct-seg 100.0, struct-cdc 100.1, delta-replace 100.0,
  delta-xor 100.0, **delta-xorz 99.9**. Every tensor changes between these widely spaced training steps and the weights do not
  compress, so no reuse and no XOR gain exists; any Z3-09/10 tensor-delta work needs closer checkpoints or a numerically aware delta, not
  this baseline. No adapter or tensor-delta codec was written.
* **Real ELF pair** (Go toolchain, `go build -trimpath ./cmd/z3lab`, 5.6 MiB; r1 adds one 56-byte function): CDC stores 81.4% of the second build
  (49.7% after the pack rule), fixed-64k 97.9%, fixed-4k 92.4%: code motion defeats chunking for a 56-byte source edit. Evidence for the later
  binary batch; no PE/ELF parser exists in this lab.
* **ZIP composition (stretch, laboratory)** - ZIP structural segmentation + each deflated member through the block-local IR, 35 B/ref, % of update revisions' compressed bytes
  (Go L6 members, 4 MiB base):

| ZIP family | CDC | ZIP structural + member CDC | ZIP + Preflate | ZIP + explicit tok4k | ZIP + predictive tok4k | ZIP + predictive tok16k | plaintext lower bound |
|---|---:|---:|---:|---:|---:|---:|---:|
| add member | 8.7 | 7.6 | 8.1 | 11.6 | 9.0 | 8.2 | 7.7 |
| change member | 9.5 | 8.3 | 8.3 | 9.3 | 9.4 | 8.6 | 8.3 |
| metadata only | 42.3 | **0.1** | 0.1 | 1.1 | 1.2 | 0.4 | 0.1 |
| remove member | 7.5 | **0.1** | 0.1 | 1.1 | 1.1 | 0.4 | 0.1 |
| reorder members | 15.9 | **0.1** | 0.1 | 1.1 | 1.2 | 0.4 | 0.1 |

  Composition is exact and costs 0.3-1.2 points of references, but when unchanged members keep byte-identical compressed streams, structural
  segmentation + member CDC already reaches the plaintext bound; the transform adds nothing there (and a new member's content is
  the cost either way). Its value would be members recompressed by a different tool or level. Not a Z3-08 decision.

## Limitations

* Synthetic text (random-copy matches: no repeated distances, so the repeat-distance model is untested), one seed, 3 revisions; Go 1.27.0
  `compress/flate` levels 1-6 are table matchers (klauspost-derived) and levels 7-9 chain matchers; Go output is toolchain dependent.
* Zlib levels 1-3, tiny blocks (memLevel 1) and exact emulation (Preflate's strength) are not matched; the skeleton is exact but not predicted.
* Whole-stream in-memory parse; no streaming; E2/predictor speed is research-grade (8-13 MiB/s).
* The chooser prices each representation against its own steady-state store; real policy needs entry-cost accounting (shown: commit-after-r1)
  and CPU/read cost, which are out of scope.
* Unit references (35 B binary vs 96 B manifest entry) decide the best unit size; the actual production reference structure is not designed here.

## Recommendation

1. Treat **predictive block-local reconstruction as the viable path**: exact, universal fallback (explicit E2, then raw CDC), correction cost 0.1-0.9 bits/token
   for chain compressors and Go, entry cost 108-115%, incremental 1-7% where CDC pays 35-100% and Preflate 10-20% (Go) / 3-7% (zlib family).
2. Production-shaped Z3-08 work should keep the three-level hierarchy (CDC / explicit / predictive) and a per-object chooser with entry-cost
   accounting; never apply the transform unconditionally (gzip appends, ZIP content changes, zlib level 1).
3. Next measured experiments before any format: (a) exact zlib `deflate_fast` emulation or a chain-insertion hint to remove the level-1 reset
   penalty; (b) predict dynamic headers and block boundaries from the token statistics (memLevel-1 style streams: 12% skeleton);
   (c) real corpora (container layers, wheels, Go release tarballs) in place of synthetic text; (d) a streaming decoder, whose state is listed above;
   (e) unit references sized for binary hashes (16-64 KiB predictive units).
