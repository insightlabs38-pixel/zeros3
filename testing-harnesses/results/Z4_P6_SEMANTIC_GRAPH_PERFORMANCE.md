# Z4-P6: cross-format semantic CAS, graph normalization and production-portable reconstruction

Verdict: **P6_PARTIAL**. Research only: `zeros3.go`, `go.mod`, every store format, default, floor and S3 behaviour are unchanged
(`git diff ded8d04 -- zeros3.go go.mod 'zeros3*_test.go'` is empty; `results/qualify.log`). No representation authority was enabled,
no CDC retired, drafts #97/#80/#81 and B2 were not touched. Evidence: `testing-harnesses/z4/p6/` (README lists every script).

What is demonstrated: both research cores are now **standard-library-only, mechanically extractable and differentially tested**, B3 ingestion is
**streaming, ~3.4× faster and ~3.8× lower in resident memory with byte-identical stores**, input-order sensitivity is **eliminated**
(437.99 MB → 271.48 MB, equal to the forward order), range reads are **integrity-verified** and priced, nullable/narrow-integer columnar files
now share components, and the B4 identity/matching engine uses **~2.4–3.4× less memory** with results identical to the pre-P6 algorithm.
Why not `P6_RESEARCH_READY`: LLVM module-level normalization, VM/recipe compaction and indexed B4 reconstruction were **not** delivered, Q4_0 is not
exact for the real producer tested, strings-nullable/dictionary columnar forms, ORC and the container lane were not advanced, and measurements are
single runs on one 4-core machine (not interleaved repeats).

## 1. Starting state and P5 correction
Verified through GitHub: PR #111 merged with the `merge` method, merge commit `ded8d048089903a80bf4b7ad54c7882d239e1cb4`, parents `d59ff71` and
`ae0b264`, eight development commits (`rev-list --count d59ff71..ae0b264` = 8), production unchanged, verdict `P5_PARTIAL`. The P5 report had no
merge-status statement; it was appended as §19 in one standalone commit (`0da5524`), no finding or verdict changed. B2's production port was not reopened.

## 2. Baselines (current code, pinned inputs, this machine: 4 vCPU, Go 1.27, clang 18.1.3 / 19.1.1)
Pinned hashes: `z4/b3/hf_pins.txt` (all 7 downloads match), `results/b3_q4_pins.txt`, `results/b4_bitcode_pins.txt`.
* **B3 SmolLM2-135M family (5 files, 1,495,370,725 B), forward order:** 120.7 s wall, 111.6 s user, **1,953.6 MiB RSS**, stored 272,221,529 B (cost model;
  historical 272.2 MB reproduced). 88% of CPU was the candidate search; profile in the driver (`-cpuprofile`).
* **B4:** `z4b4 llvm` clang-18 O2, six SQLite revisions: prod CDC 12,474,643 B; hybrid(+delta) 6,186,098 (0.496×); +delta recipe 6,009,211 (0.482×).
  PDB (Newtonsoft 12.0.3–13.0.4): prod CDC 597,782; strong 237,218 (0.397×). `perf`: 1 M-node synthetic DAG identity **+700 MiB heap**, matching 3.3 s.

## 3. Dependency and portability audit
`cmd/p6extract -audit-only -allow …` (type-aware, go/types): the research packages are **PORTABILITY_BLOCKED** — `semcas` (aliased `thriftcompactz3` import
plus `lab`, `deflate`, `gitz3`, `tarz3`, `parquetz3`, `ir`), `graphnorm` (`lab`, `simdelta`, `corpus`, `dcore`, `ir`, … 8 harness packages, 133 own
declarations) — and the new cores are clean: `semcore` (301 declarations reachable from all exported roots; `st`, `ggufz3`, `torchz3` are stdlib-only
parsers) and `graphcore` (260). Audits: `results/portability/`. The split moved the three harness-coupled spots behind adapters: the chunker
(`Planner.Chunk`), `Backend` (blobs/records/recipes), `Ext` (DEFLATE/Parquet ops), the packed-length heuristic (stdlib flate, identical function) and the
recipe resolver (`Replay(resolve)`; the simdelta resolver lives in `graphnorm.ReplayLLVM/ReplayProg`).
Refactor equivalence: B3 stores are **byte-identical** before/after the split (`diff -r`, Pythia and SmolLM2), `z4b4 llvm`/`pdb` output identical old vs new binary.

## 4. Streaming tensor architecture (B3)
`Doc` now carries an `io.ReaderAt`; `DetectAt` / `ParseSafetensorsAt` / `ParseGGUFAt` / `ParseTorchAt` read only headers and the pickle, and a new
bounded protobuf walker (`ParseONNXAt`) reads tags and small fields through a 4 KiB window and never a tensor payload. `Ingest` reads one segment at a
time into a reused buffer and computes the file SHA-256 incrementally. Bounds: tensors above `MaxTensor` (192 MiB) are stored as 32 MiB exact `Bytes`
blocks (no semantic search; documented limit), literal regions are windowed at 64 MiB, the decoded-parent cache is a bounded FIFO (`CacheBytes`),
transient candidate buffers are bounded by `WorkBytes` through the worker count. Search is parallel across candidates with a deterministic reduction
(first minimum in candidate order), proven to give identical stores for 1/2/4 workers (`TestParallelPlannerDeterministic`, race-clean).
Algorithmic changes that keep results exact: word-wise row hashing (`maphash`), an exact early-exit residual diff (quick reject by differing-byte count,
abort at the acceptance bound), sampled-row gating of the expensive gather search (<12.5% of 64 sampled rows present ⇒ skip; counters in the ingest JSON),
per-candidate cached row tables, table-driven F16→F32 and fast BF16/F32/F16 paths, derivation proofs at **value level** (`proveDerived`: ops+patches applied to
the already-authenticated parent payload, no store re-materialization).

## 5. Memory and CPU results (B3)
| config (fresh process) | wall | user CPU | max RSS | store vs B3 |
|---|---|---|---|---|
| B3 baseline | 120.7 s | 111.6 s | 1,953.6 MiB | — |
| P6 default (256 MiB cache, 4 workers) | 38.4 s | 62.3 s | 1,295 MiB | byte-identical |
| P6 `GOMEMLIMIT=768MiB`, cache 128 | 34.5 s | 55.9 s | 777 MiB | byte-identical |
| P6 `GOMEMLIMIT=512MiB`, cache 64 | **35.7 s (41.9 MB/s)** | 58.2 s | **514.9 MiB** | byte-identical |
| same, 1 worker | 56.4 s | 53.6 s | 514.9 MiB | byte-identical |

Scale (synthetic related families: BF16 file N, exact F32 widening, F16 conversion, reordered shard, unrelated; one 320 MiB tensor at N=256; `GOMEMLIMIT=512MiB`):
logical 0.40 / 1.95 / 6.44 GB → **RSS 299.7 / 329.5 / 337.2 MiB**, ingest 10.9 / 48.9 / 199 s, stored 134 / 872 / 2,148 MB; files up to 2.15 GB (F32) and
1.07 GB (BF16); all outputs replayed in a fresh process and verified with hashlib (`results/b3/scale_*`, 16/16 exact). **Replay is not yet bounded as tightly**:
peak RSS 298.7 / 537.4 / 907.3 MiB (256 MiB decoded cache + GC slack; streaming derived replay is open). Temporary disk: none beyond the store.

## 6. Cross-format discovery
Discovery still ranks by the layout-invariant magnitude signature and gates every acceptance by exact proof; the new `PlanRoots` evaluates **all pairs** in a
family (≤12 sig-close candidates per target) so discovery no longer depends on arrival order (11,674 proven-or-rejected evaluations on SmolLM2). Recall against a
bounded exhaustive oracle was not measured separately; the planner's own regret against the exact subset solver is in §8. Tensor names are never identity.

## 7. Exact dtype / quantization results
* Real producers on the pinned SmolLM2 files: BF16→F16 (llama.cpp f16 GGUF), BF16→F32 (ONNX), F32→F16 (ONNX fp16), **Q8_0 (bartowski/llama.cpp) exact with 0 residual
  bytes** for the 151 derived tensors, plus transposes and head-interleave gathers (0 residual).
* **Q4_0 (bartowski, `SmolLM2-135M-Instruct-Q4_0.gguf`, 91.9 MB): not exact.** The reference quantizer reproduces 1 tensor; with the acceptance bound raised to 0.99 the other
  145 relatable tensors need 33.5 MiB of residual (of 85 MiB tensor bytes) and 126 stay raw — the producer's scale selection differs (imatrix-style). Default policy
  (accept < 50% of raw) rejects it, so it is stored literally. Ranked "approximately related", not admitted.
* New exact columnar integer relations: INT32 Parquet streams of INT_8/INT_16/UINT_8/UINT_16 columns are exact widenings of the canonical narrow stream (§10).
Not done: integer narrowing/widening beyond that, Q4_1/K-quants, cross-platform producer variation, ONNX/PyTorch producers beyond the pinned files.

## 8. Input order and root selection
SmolLM2 family (cost model bytes, `results/b3/order_*`):

| layout | bytes |
|---|---|
| forward online | 272,221,529 |
| reverse online | 437,985,558 |
| reverse + pre-P6 `Optimize` (greedy, largest first) | 283,123,663 (reproduces the historical 283.1 MB) |
| **reverse + P6 `PlanRoots/ApplyRoots`** | **271,484,593** |
| forward + P6 plan | 271,484,593 (−0.74 MB vs online forward) |

The planner builds connected groups (213 groups of 5 on this family), evaluates every ordered pair, and chooses roots by exact subset enumeration (≤14 members) or
greedy prune + swap beyond. The scalable greedy's regret against the exact solver is **469,829 B over 213 groups (0.17%)** (`greedy_regret_bytes`). Application proves each new derivation
from its parent's authenticated payload, materializes new roots first, swaps records, runs the whole-store closure check (depth/cycle/missing) and frees blobs only after it
passes; a failed check restores every record. Fresh-process replay of all five files after each rewrite: exact (hashlib). Two defects found and fixed on the way: an edge
evaluation that failed when a candidate parent was currently *derived from the target* (a store-level cycle that is not a value-level cycle), and the same representation
dependence in the apply-time proof. Not done: hot/cold read-cost constraints in the objective, incremental arrival of better roots, a bounded-time budget for very large groups.

## 9. Range reads
Integrity: the pre-P6 reader returned raw-blob subranges **without any verification**. Now (`Store.BlockSize`) the first range read of a raw component streams the whole blob once, requires its flat
SHA-256 to equal the recorded payload digest, and keeps a per-block table in process memory; every later block is checked against it (tamper before first touch, after first touch and truncation all fail
closed: `TestVerifiedBlocksFailClosed`, `…TruncatedBlob`). A production integration replaces first touch with the engine's existing authenticated chunks. First touch cost here: 271 MB read per SmolLM2 file per process.
Measured 4 KiB reads (`results/b3/ranges_fwd_new.jsonl`, 150 requests each; amplification = bytes read / 4096):

| component kind | unverified (B3) | verified 4 KiB | 16 KiB | 64 KiB |
|---|---|---|---|---|
| raw | 1× | 2× | 4–5× | 16× |
| elementwise convert | 0.5–1× | 1.4–2× | 4.5–5× | 16–18× |
| convert + permuted layout (tensors ≤ 2 MB) | 346–372× | 346–372× | 346–372× | 346–372× |

Permuted layouts read the whole tensor in every configuration: for a transposed view, a 4 KiB output range touches every source row. The gather-through-blocks path (reads only distinct blocks, used when
< half the tensor) helps only for large tensors; there is no setting of the block size that fixes this. Layout trade-off, same matrix, 4 KiB reads through 4 KiB verified blocks (`z4p6 tiles`):

| layout (1536×576 BF16) | stored | row-major view | transposed view |
|---|---|---|---|
| row-major root + derived transpose (B3) | 1.77 MB | 2× | **432×** |
| tile-major (32×32) root, both views derived by one exact permutation | 1.77 MB | 9.9× | **48×** |
| both views stored raw (hot) | 3.54 MB | 2× | 2× |
(576×576: 162× → 18.6× tiled.) Recommendation — **hot serving:** store hot layouts raw (the hot-hybrid row), do not serve permuted views from a derivation; **warm:** tile-major roots when both orientations are
read at medium rate (no extra bytes, ~10–50×); **cold/archive and batch whole-object:** row-major roots with derived transposes (best bytes; whole-tensor reads are cheap per byte). Elementwise conversions are
range-friendly at any tier.

## 10. Columnar
Profiles from the pinned generator (`req`, `opt`, `hard`; 3 Arrow + 5 Parquet variants, `results`/B3 corpus): `req`/`opt` unchanged at 6.86 MB (0.150× prod CDC).
`hard` (nullable double, int16, dictionary strings): **5,096,830 → 3,825,210 B (0.332× → 0.249× CDC)**, 7/7 files exact in a fresh process. Mechanisms: nullable fixed-width Arrow columns become a dense
non-null component (the same bytes Parquet stores), per-batch validity components, and the value buffer derived by the new bounded `OpScatter` (null slots zero, any other content carried as an exact residual);
INT32-physical narrow integers are exact widenings of one canonical narrow stream. Tests: `TestScatterExactAndBounded`, `TestNullableAndNarrowColumnsShare`. Not done: nullable/dictionary **strings**,
definition-level sharing (levels stay in the Parquet recipe), nested types, column reordering beyond the existing Arrow case, schema evolution.
**ORC feasibility** (`results/orc_feasibility.json`): in uncompressed ORC, double/float columns appear **verbatim** (shareable by span typing once a footer/stripe parser exists); integer columns (RLE v2) and
strings do not. A full ORC lane needs an exact RLE v2 encoder per writer; not started — recorded as a partial prototype/negative.

## 11. Graph memory (B4)
`CGraph` (structure of arrays, 32-bit offsets, 8-byte edges) and a view-based engine shared by `Graph` and `CGraph` (iterative Tarjan with the reference traversal order, counting-sort reverse CSR,
allocation-free node hashing, sort-based classification). **Identities, ambiguity flags, occurrence indexes, SCC/search counters are identical to the pre-P6 reference** on 1,500 random graphs (DAG, cyclic, ring-symmetric,
dangling, opaque, unknown) plus large cyclic ones, for both graph types (`TestCompactIdentitiesMatchReference`, kept as `ref.go`); cycles, symmetric rings and repeated structures are covered, and renumbering stability is tested.

| synthetic LLVM-shaped graph | engine | identity time | graph B/node (live) | graph+idents B/node (live) | peak heap B/node | VmHWM B/node |
|---|---|---|---|---|---|---|
| 100 k | reference | 150 ms | 126 | 163 | 389 | 504 |
| 100 k | compact | 124 ms | **42** | **79** | **163** | 262 |
| 1 M | reference | 1.66 s | 120 | 157 | 408 | 428 |
| 1 M | shared engine on `Graph` | 1.36 s | 120 | 157 | 307 | 325 |
| 1 M | compact | 1.26 s | **42** | **79** | **161** | **204** |
| 10 M | compact | 17.2 s | 42 | 79 | 167 | 198 (1.89 GB) |

The historical "≈700 B/node" is reproduced by the old `perf` harness (**+700 MiB heap** at 1 M); the same harness now reports **+207 MiB**. My own benchmark measures the reference at 408–428 B/node, so the honest
improvement is **~2.4–3.4×** depending on the metric.

## 12. Matching
`Match`/`MatchCompact` run on interned buckets (hash addresses a chain; exact comparison once at bucket build; lists compacted in place), CSR reverse adjacency and pass-pending lists. Results are **identical to the
pre-P6 matcher** (whole `MatchResult`, 1,200 randomized pairs with insertions/deletions/renumbering, three option sets, both graph types). Original `perf` workload: 1 M renumbered nodes 3.31 s → 2.13 s; my harder
bucket-heavy edit workload: 100 k 858 → 432 ms, 1 M **21.7 → 26.7 s (slower, −35% peak heap: 1,357 → 948 MiB)**. Adversarial repetitive and symmetric cases keep the existing bounded assignment (≤24) and SCC budget.
No unbounded quadratic stage was added; large identical-local buckets are still scanned per pass (known limit).

## 13. LLVM normalization
Unchanged in method; evidence for where bytes go (hybrid +delta, 6 clang-18 O2 revisions, 6,186,098 B): function templates 85.5%, skeleton 7.7%, identity tables 1.5%, recipes 5.3%.
Module-level ID normalization would act on ≈9% of bytes, so it was **not implemented**; a recipe step-diff would save <1%. Second toolchain (clang 19.1.1, same flags, same sources): hybrid **0.518×** (0.505× with delta
recipes) vs 0.496×/0.482× for clang 18 — the method carries over with no code change, exact self-replay holds. Cross-compiler (same source, clang 18 then 19): second file 1.83 MB vs 1.97 MB CDC (**0.93×**): little is shared
across compilers. First-sight premium: revision 1 costs 2,567,533 B vs 2,107,620 B CDC (**+21.8%**); cumulative break-even at the second revision (3.02 MB vs 4.21 MB). Fresh-process replay of both toolchains:
12/12 exact (`results/b4_llvm_replay_full.txt`, independent hashlib check). No broad compiler-independence is claimed; debug-info, O0/O2 and real historical artifacts were not re-run.

## 14. PDB / debug symbols
Output unchanged (0.397× cumulative). New negative result: extra column predictors (second difference, XOR-with-previous) improve PDB column bytes by **0.15% / 0.21%** (97,159 → 96,957 B; `TestPDBPredictorExperiment`), not worth a VM
extension. Line-number blob normalization (existing oracle) lowers packed blob heaps 365,924 → 347,178 B (**−5.1%**) but has no reversible VM form yet; not integrated. DEX/CLR/DWARF not advanced; the specialized decoders stay.

## 15. VM and recipes
Not changed. Measured only: VM decode 22.6 → 31.3 MiB/s on the SQLite O2 fixture between baseline and new binary (same VM code; run-to-run variance, not a claim), whole-object replay only. Recipe: 33 B/step; delta-coded recipes save 177 KB (2.9%).
**Indexed reconstruction was not implemented.** Boundary identified: function pieces are bit-packed inside the module block, so independent replay needs the bit phase, the BLOCKINFO scope and the identity tables at each piece start (a checkpointed VM state),
and block length words are back-patched, so any early-stopping replay must exclude open frames' length fields. Policy until built: LLVM objects are **cold/archive whole-object** representations.

## 16. Shared infrastructure decisions
`semcore` and `graphcore` are separate cores: their reconstruction semantics differ (typed tensors and exact ops vs bit-level graph programs). What is shared is the *pattern* (bounded parser, injected storage, fail-closed replay) and
the extractor/alias tooling; no common abstraction was introduced because no duplication that would shrink was found. P5's `dcore` was not duplicated; `simdelta` stays a research resolver behind `graphnorm`.

## 17. Economics (modeled; CDC remains authoritative, no production saving claimed)
Research-store "physical bytes" are `BlobPacked` (stdlib flate pack model) + record + recipe bytes + 12 B/component index; they are not production pack bytes. SmolLM2 family: prod CDC (additive model) ≈ 1,127.5 MB vs semantic 271.5 MB
(**0.2414× → 0.2408×**: the historical ratio is preserved and root choice is now order-independent). B3 hard profile 0.249× CDC; req/opt 0.150×. B4 LLVM 0.50×/0.51× (clang 18/19), PDB 0.397×. Additive CDC+representation, authoritative, cold and hot-hybrid
rows differ only in the retained roots: hot-hybrid keeps raw hot layouts (up to 2× on those tensors, §9). Mixed unrelated data: the first-sight premium (LLVM +21.8%) must be admitted by a probe; no estimator-regret study was run.

## 18. Independent replay and integrity
Fresh-process replay verified with `hashlib` (never the Go code): B3 SmolLM2 stores (before and after root rewrite, 3 stores × 5 files), scale families (16/16), columnar `hard` (7/7), B4 LLVM clang 18/19 (12/12), extraction rehearsals.

## 19. Corruption and adversarial testing
`TestCorruptionMatrixNeverWrongBytes` (byte flips at four offsets of every blob, **every byte of every record**, every byte of every recipe), `TestPoisonedIndexesNeverCreateFalseEquivalence`, `TestHostileRecordsRejected`
(5,000 random records, one canonical wire form), verified-block tamper/truncation tests, short-reader/failing-writer/short-writer tests, hostile-input parser tests; fuzzers `FuzzParseRecord`, `FuzzParseRecipe`, `FuzzDetectAt`
(semcore) and `FuzzVM`, `FuzzBitstream`, `FuzzRecipe`, `FuzzPDBandDEX` (graphcore), 15 s each, plus `-race` on `semcore`, `graphcore`, `dcore` (all green, `results/qualify.log`). Limits: fuzz budgets are smoke-level; materialization budget for a hostile record is the existing
2 GiB `MaxOutput` bound (not tightened).

## 20. Mechanical extraction
`cmd/p6extract` (go/types): loads the root packages and their in-module imports, rejects non-stdlib imports and (with `-allow`) any reachable declaration outside the allowed core packages, selects declarations reachable from named roots
(all methods of reachable types, whole const/var/type groups), renames with a collision-checked prefix using object resolution (qualified `pkg.Name` rewritten from selector resolution), merges them and exactly the stdlib imports they use into a **temporary copy** of `zeros3.go`, compiles, runs original-vs-extracted
differential vectors, checks token-level fidelity, and reports adapter interfaces. Results (`results/extraction/`):

| core | selected decl. | vectors | mismatches | fidelity | imports added to production | class |
|---|---|---|---|---|---|---|
| B3 `semcore`+`st`/`ggufz3`/`torchz3` | 280 → 232 names | 31 | 0 | 0 | `hash/maphash` (stdlib) | PORTABLE_WITH_THIN_ADAPTER (Backend, BlobReader, Ext) |
| B4 `graphcore` | 241 → 193 names | 90 | 0 | 0 | `hash/maphash` (stdlib) | tool says PORTABLE_WITH_THIN_ADAPTER because it counts the internal `view` interface; the only external dependency is the `Replay(resolve)` function argument, so effectively CORE_PORTABLE |

Both integrations leave `go.mod` untouched. The B4 differential **caught a real extractor defect** (`init` functions were renamed, so lookup tables stayed empty and EncodeLLVM failed); fixed, rerun green. Classification: streaming tensor
canonicalization, reconstruction, graph normalization, VM and recipe logic — `CORE_PORTABLE`/thin adapter; `semcas`, `graphnorm` — `PORTABILITY_BLOCKED` (research stores); ORC, DEFLATE/Parquet exact codecs — `RESEARCH_ONLY_NOT_SELECTED` for this batch.

## 21. Production-compatibility rehearsal
Inside the temporary integrations only: B3 — the extracted core ingests three safetensors files read **through the engine's own `manifestReaderAt`**, stores components/records/recipes as ordinary engine objects (real CDC/CAS path), the
engine is closed and reopened, the core index is rebuilt from the stored records, each file is replayed into a SHA-256 sink and 20 verified ranges per file are read from engine objects: all exact, 4 of 8 components derived, peak heap 80/88 MiB for 16 MiB logical (the engine's own buffers
dominate). B4 — extracted `EncodeLLVM`/recipe stored as engine objects, resolver verifies each object's digest, replay exact for both fixtures. Not a deployed-authority test; nothing was published to the repository store.

## 22. Negative results and rejected approaches
* Extra PDB column predictors: <0.25% (§14). * Raising block size does not help permuted ranges; only layout choice does (§9). * Compact matcher is slower than the reference on bucket-heavy synthetic edit graphs (§12).
* Q4_0 from a real quantizer is not exact (§7). * Pre-P6 greedy re-rooting leaves 11.6 MB on the table on this family and its result depended on the arrival order of the parent (§8).
* The first planner version evaluated edges through the store and silently lost reverse-order edges (cycle false positive) — found by the order experiment, fixed. * The "≈700 B/node" baseline is metric-dependent (§11).

## 23. Remaining blockers and unresolved niches
LLVM module-level normalization; reversible VM compaction and indexed/range reconstruction for B4; streaming derived replay with bounded memory; Q4_0 and K-quants; strings-nullable/dictionary/definition-level sharing and ORC; Git/tar/OCI lane and exact-DEFLATE speed (deferred to P7/P8
as specified); estimator regret/false-negative study for early admission; interleaved repeated timing; hot/cold-aware root objective; cross-platform producer variation. B2 authority integration remains a separate prerequisite.

## 24. Final PR/merge state and recommendation
Final PR and merge state are recorded in the PR and the hand-off message, not here (the merge commit does not exist when this file is committed). **Recommendation:** P7 (model/numeric hard-codec) can start from the extracted `semcore` ops and the
exactness harness (Q4_0/K-quant producer conformance is the first target). P8 should take the verified-block mechanism (replace first touch with engine chunk hashes), the streaming `Backend` contract and the extractor. The next B3/B4 work item before any authority
integration is indexed/streaming reconstruction; do not start it before the B2 port.
