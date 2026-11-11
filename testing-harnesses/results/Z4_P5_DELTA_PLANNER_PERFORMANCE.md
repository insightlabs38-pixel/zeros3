# Z4-P5 — Advanced similarity deltas, scalable discovery and representation-authority planning

Verdict: **P5_PARTIAL**. Research-only: `zeros3.go`, the root `go.mod`, every store format, default and S3 behaviour are unchanged on this branch (checked in §14).
Branch `z4/p5-delta-planner-performance` from verified `origin/main` `d59ff71c0a42c1c8765789bfa8338e31939f592e`. Draft PR #97 (`94ac9c8`) and drafts #80/#81 were not touched; B2 was only checked out detached and copied into scratch worktrees outside the repository.
Mandatory first action: the P4 report said "8 development commits"; `git rev-list --count d59ff71^1..d59ff71^2` is **9** (GitHub agrees). Fixed in a standalone one-line commit (`1d24c14`); no P4 conclusion or measurement was changed.
Evidence: [`../z4/p5/`](../z4/p5/README.md) (raw JSONL, logs, scratch diff, reproduction). Everything labelled below as *measured* ran on this 4-vCPU Firecracker VM (Xeon 2.1 GHz, 15 GiB, ext4 on virtio, Go 1.27.0); corpora were fetched fresh from the pinned public sources (§2) and are not committed.

## Summary of results (details and caveats in the sections referenced)
| Area | Result | Kind |
|---|---|---|
| Matcher (§3) | program **23-34% smaller** than the best B1 variant on tar/SQLite/Parquet-Snappy (5% DEX, equal elsewhere); **4.7-5.8× faster** on DEX, Parquet/Zstd, safetensors; **mostly slower (0.1-1.7× of B1's speed) on tar/SQLite/Parquet-Snappy**; 3-6× is not a single-configuration result | measured, real corpora |
| Candidate costing (§4) | staged estimator: **0 regret, 0 wrong choices on 23 targets**, 70-90% fewer exact encodings, 26-41% less wall time | measured |
| Index (§5) | sort **3.4-7.4×** faster at 10 M chunks (13.97 → 1.88 s), serialization 15.0 → 1.2 s, file −17%, lookup 5-9× faster at ≤1 M; `RunSet` append 1.6 µs/object, compaction, tombstones, CRC | measured (synthetic postings; real-history recall inherited from B1) |
| Group accounting / planner (§6-7) | B8 defect reproduced **to the byte** and fixed in two places; Z3-31 8-revision Parquet **0.916× → 0.326×** automatically; 3-revision 0.901× → 0.775× | measured on real stores (scratch, draft base) |
| Planner overhead (§7) | plan reuse: −14 to −50% migration time, peak −5 to −31%; **default-off gate: tar 3.5×, Git 13× faster at identical bytes, peak 1.16×/1.19×**; speculation remains where reps are live alternatives | measured; heuristic |
| Large objects (§9) | **1 GiB** base+target, **257 MiB RSS**, 709 MiB/s encode, 410 MiB/s decode, exact SHA-256, fresh-process decode 51 MiB RSS; real 377 MiB multi-revision object → 1.54% record | measured |
| Reads (§8) | hot-hybrid keeps 4 KiB amplification ≤1.3× CDC; delta 2-3×; REP bodies 15 198× (tar) - 821 ms/4 KiB (DEX) | measured on the draft base |
| Format levels (§12) | composite feature-set labels (segments+authority = 12, not 10); real binaries' gates observed; model tested | observed + executable model; **nothing activated** |
| CDC-retirement audit (§10.1) | 51 chunk-touching functions, **13 post-B2 functions unaudited** | source inventory |
| Portability (§15) | **PORTABLE**: 148 declarations extracted, compiled into the production source, 61/61 vectors byte-identical, 0 adapters | executed |
| Scratch B2-on-main (§10-11) | 13 conflict hunks in 8 regions; **not completed** (P3 reachability pipeline) | attempted, stopped |


## 1. What was read and the dependency map (B1 / B2 / production)
Inspected: B1 `simdelta` (`match.go`, `index.go`, `codec.go`), B2 draft `94ac9c8` (section 16e of `zeros3.go`: `xdMatch`, `xdEncode`, `authFeatures`, `authMigrator.process`, `planRepGroups`, `authProve`, `authCtx`), the B8 accounting diff, P0/P4 reports and current main (P1C level-11 segments, P2 read caches, P3 reachability pipeline, P4 deferred construction).

| Concern | B1 (`z3/simdelta`) | B2 draft (`zeros3.go` 16e) | Relation |
|---|---|---|---|
| COPY/ADD matcher | `match`: FNV-1a **re-hashed over the whole anchor for every target position**, base sampled at fixed stride, positions packed `base<<24\|off` (hard 16 MiB), 4 slots, byte-wise extension, no backward extension | `xdMatch`: polynomial rolling hash on both sides, content-sampled (`mix(h)&mask==0`, k = pow2 ≥ total>>18), 4 slots × 16 probes, backward extension, `base<<30\|off` (≤1 GiB, enforced by the 256 MiB cap), byte-wise extension | **independently implemented**; B2's is algorithmically the better one |
| Serializer | `B1D1` envelope, whole-program DEFLATE | `ZS3D` v1, 128 KiB independently deflated frames (range-addressable) | separate formats; not interchangeable |
| Cost evaluation | `Physical(wire)`: DEFLATE-6 of the whole wire per candidate, after `Encode` already deflated it (2 compressions/candidate) | `xdEncode` deflates every frame at level 6, then `phys := min(len, len(xdDeflate(blob,1)))+96` (again) per candidate | **duplicated idea, separate code**; neither prunes before compressing |
| Discovery | bottom-16 + 4 superfeature bands + edges + 4 KiB local features, map index (newest 64), sorted-array/`DiskLookup` variant | `authFeatures(b,16)` bottom-16 sketch, in-process map (newest 64) + same-key neighbours; **no persistent index** | duplicated concept |
| Group accounting | none (models only) | `planRepGroups`/`unsharedBytes`: *exclusive* per-object CDC bytes vs *group-union* component bytes (the B8 defect) | B2-only |
| Planning flow | n/a | `best` = `AuthorityMigrate` three times (delta plan, rep plan, applying pass); every pass re-encodes/re-constructs | B2-only |
| Large objects | `MaxBytes` 16 MiB | `xdMaxObject` 256 MiB, whole target + ≤8 candidate bases resident | both bounded by whole-object memory |

Consequences: a shared **matcher + cost-estimator core** is realistic (and is what `dcore` is); a shared serializer is not (different wire formats, different range behaviour: B2's frames are the better design for ranges and are kept); B2's decoder is untouched. `dcore.EnvelopeSize` models B2's frame/deflate accounting exactly enough to rank, but the production adapter must keep `xdEncode` as the byte-producing serializer.


## 2. Corpora and baselines (three separate environments, never mixed)
Corpus (fetched this session, pinned public sources, SHA-256 in `../z4/p5/results/corpus.sha256`): coreutils 9.5/9.6/9.7 tar; Newtonsoft.Json 12.0.3, 13.0.1-13.0.4 (5 real releases; the 12-release "long" history of B2 was **not** reproduced); F-Droid `org.fdroid.fdroid` 1019000/1020050/1021000 `classes.dex` and `classes2.dex` **kept as separate families**; Parquet kept as **three producer chains** (pyarrow/Snappy 4 states and pyarrow/Zstd 4 states from `p0/make_structured.py`, and the Z3-31 pyarrow-chain, Snappy, regenerated with `z3/oracle/parquet/gen_pyarrow.py`, the workload of B8's 0.916→0.326 result); SQLite edit chain (4 states, synthetic edits); hiredis Git **pack-objects exports** (not commit-subset exports, and not this repository's history as in B2: numbers are not comparable with B2's Git row); Pythia-14m safetensors (3 checkpoints; B1 showed no similarity gain, kept as an unrelated-entropy control). Not obtained: Newtonsoft 10.x-11.x, GGUF/large model revisions, SST histories; the large-object evidence therefore uses synthetic bases and a *real concatenated* multi-revision object (§9).

| Baseline | What | Where |
|---|---|---|
| **A. B1 standalone** | unmodified `simdelta.match`/`Encode` (`SetReferenceMatcher(true)`, `MatchRef`), unmodified flat `Sort`/`WriteTable`/`DiskLookup` | §3, §5 |
| **B. B2 production draft** | `94ac9c8` detached worktree, **unmodified**, its own economics harness, real stores, packs after gc+repack+compact | §7 (rows "draft"). *Scratch integration onto current main was attempted and stopped: §11* |
| **C. production CDC** | the CDC baseline store of each family (`cdc` row, compacted into packs) = what main's P4 engine stores with authority off | §7 |
Labelled difference: B1's *modelled* envelope sizes (§3) are not B2's *actual stored bytes* (§7); the two are never divided into each other.

## 3. Optimized matcher (`dcore/match.go`) — measured
Design: rolling polynomial hash (O(1) per byte instead of B1's O(anchor) FNV per position); **content-sampled anchors on both sides** (an anchor is kept when the top `nbits` of `h·K` are zero, so base and target agree on positions after any shift); open-addressed table of 8-byte entries (`24-bit tag | 40-bit virtual offset`), ≤`Slots` positions per tag, ≤`MaxProbe` probes; every candidate is verified by an 8-byte-word compare, extended **backwards** into the pending literal and forwards; the base index is immutable and reusable across targets. Offsets are **checked**: `layout()` rejects any base set that does not fit 40 bits (`ErrTooLarge`, tested with sizes that cannot be allocated) — the B1 `base<<24|off` packing that silently caps at 16 MiB is not used. Matcher variants are *filters only*: a hash/tag collision cannot emit a wrong COPY (`FuzzMatchExact`, adversarial zeros/periodic/forced-collision tests).

Matched pairs of consecutive revisions, inputs clipped to B1's 16 MiB limit so both matchers see the same bytes; `envelope` = exact `EnvelopeSize` (deflated 128 KiB frames) of the program, identical for both matchers; MiB/s = target bytes / (index build + match), best of 3, quiet machine. Size column is relative to the *smallest* B1 variant of that family (1.000 = B1 best; lower is better).

| family (pairs) | B1 smallest variant: bytes (MiB/s) | B1 adaptive MiB/s | dcore 2^18 anchors: size×B1 / MiB/s | 2^20 (default): size× / MiB/s | 2^22: size× / MiB/s | 2^20, floor 3: size× / MiB/s |
|---|---:|---:|---:|---:|---:|---:|
| tar (2) | 725827 (131, rolling) | 147 | 0.976 / 252 | 0.771 / 131 | 0.702 / 43 | 0.771 / 124 |
| clr (4) | 715674 (18, git) | 20 | 0.995 / 46 | 0.983 / 18 | 0.983 / 19 | 1.003 / 78 |
| dex-classes (2) | 5886279 (15, rolling) | 27 | 0.986 / 137 | 0.947 / 53 | 0.929 / 18 | 0.947 / 56 |
| dex-classes2 (2) | 5979732 (15, rolling) | 28 | 0.998 / 161 | 0.981 / 70 | 0.971 / 23 | 0.981 / 70 |
| parquet-zstd (3) | 9612939 (45, adaptive) | 45 | 1.001 / 215 | 0.999 / 93 | 0.997 / 32 | 0.999 / 96 |
| parquet-snappy (3) | 836416 (207, rolling) | 291 | 0.731 / 278 | 0.659 / 133 | 0.653 / 48 | 0.659 / 139 |
| sqlite (3) | 247341 (293, rolling) | 406 | 0.753 / 221 | 0.687 / 111 | 0.686 / 38 | 0.687 / 111 |
| git-pack (2) | 510874 (40, git) | 49 | 1.000 / 111 | 0.997 / 36 | 0.997 / 25 | 1.000 / 111 |
| safetensors (2) | 33537728 (24, adaptive) | 24 | 1.000 / 133 | 1.000 / 61 | 1.000 / 19 | 1.000 / 61 |


Reading (quiet machine): on families with real similarity the new matcher's program is **smaller than every B1 variant** at the default 2^20 anchors — tar −23%, Parquet/Snappy −34%, SQLite −31%, DEX −5%/−2% — and −30% on tar at 2^22; on entropy-bound data (Parquet/Zstd, safetensors, Git packs) it is equal. Speed is a function of the anchor budget and of how much of the target matches (long matches skip the per-byte loop): **the "3–6× faster" target is met where B1 spends its time failing to match** — DEX 5.1–5.8×, Parquet/Zstd 4.7×, safetensors 5.5× at 2^18 anchors, CLR 4.0× (floor 3, 1.003× B1's best size) to 6.2× (floor 4, 1.021×) and Git 4.3× at floor 4 (1.001×) — **and is not met where B1 is fast because it finds little**: on tar, SQLite and Parquet/Snappy the new matcher is 0.1–1.7× B1's speed in exchange for the 23–34% smaller programs (at 2^18 anchors SQLite runs at 0.54× B1 adaptive's speed with a 0.71× smaller program). No single configuration gives both the best ratio and ≥3× speed; the 2^20 default is the ratio-oriented point. For bases at or below the anchor budget, indexing every position (`MinSampleBits`=0) is wasteful on CLR (floor 3: 4.0× faster than floor 0 at +2% size). B1's `Encode` now uses `dcore` (decoder and wire format unchanged; 800 randomized B1 round trips, the native-Git oracle and 120 new differential cases pass).

## 4. Candidate-cost estimator (`dcore/cost.go`, `select.go`) — measured on real families
B1 and B2 both deflate every candidate. `EstimateProgram` costs a program from exact structure (raw program length is a *hard upper bound*, tested) plus a deflate-1 ratio of ≤32 KiB of evenly sampled literal bytes (or an order-0 entropy variant). `Select` ranks by estimate and exact-encodes ("finalists") only while a candidate's heuristic lower bound can still beat the best exact size (slack 5%). The chosen program is always the exact one: a bad estimate can only *lose a better candidate* (regret), never produce an invalid delta (tested: selected programs reconstruct the target; unrelated candidates stay on the prior form).

Candidates for each target = every earlier version + the first file of every other family (deliberate negatives) + a two-base candidate; oracle = exact-encode all. Quiet machine.

| family | policy | targets | wall s | exact encodings / candidates | wrong choices | byte regret (total / worst target) |
|---|---|---:|---:|---:|---:|---:|
| clr | exhaustive | 4 | 4.3 | 49/49 | 0 | +0.0000 / 0.0000 |
| clr | staged-entropy-s5 | 4 | 3.7 | 9/49 | 0 | +0.0000 / 0.0000 |
| clr | staged-s5 | 4 | 3.8 | 30/49 | 0 | +0.0000 / 0.0000 |
| clr | staged-top1 | 4 | 3.7 | 4/49 | 3 | +0.0028 / 0.0100 |
| dex-classes | exhaustive | 2 | 8.0 | 22/22 | 0 | +0.0000 / 0.0000 |
| dex-classes | staged-entropy-s5 | 2 | 5.4 | 3/22 | 0 | +0.0000 / 0.0000 |
| dex-classes | staged-s5 | 2 | 5.7 | 5/22 | 0 | +0.0000 / 0.0000 |
| dex-classes | staged-top1 | 2 | 5.6 | 2/22 | 1 | +0.0289 / 0.0686 |
| dex-classes2 | exhaustive | 2 | 8.1 | 22/22 | 0 | +0.0000 / 0.0000 |
| dex-classes2 | staged-entropy-s5 | 2 | 5.8 | 3/22 | 0 | +0.0000 / 0.0000 |
| dex-classes2 | staged-s5 | 2 | 5.5 | 3/22 | 0 | +0.0000 / 0.0000 |
| dex-classes2 | staged-top1 | 2 | 5.4 | 2/22 | 1 | +0.0043 / 0.0089 |
| git-pack | exhaustive | 2 | 2.1 | 22/22 | 0 | +0.0000 / 0.0000 |
| git-pack | staged-entropy-s5 | 2 | 2.0 | 4/22 | 0 | +0.0000 / 0.0000 |
| git-pack | staged-s5 | 2 | 2.1 | 4/22 | 0 | +0.0000 / 0.0000 |
| git-pack | staged-top1 | 2 | 2.1 | 2/22 | 0 | +0.0000 / 0.0000 |
| parquet-snappy | exhaustive | 3 | 12.6 | 35/35 | 0 | +0.0000 / 0.0000 |
| parquet-snappy | staged-entropy-s5 | 3 | 9.0 | 6/35 | 0 | +0.0000 / 0.0000 |
| parquet-snappy | staged-s5 | 3 | 9.3 | 6/35 | 0 | +0.0000 / 0.0000 |
| parquet-snappy | staged-top1 | 3 | 9.2 | 3/35 | 0 | +0.0000 / 0.0000 |
| parquet-zstd | exhaustive | 3 | 7.8 | 35/35 | 0 | +0.0000 / 0.0000 |
| parquet-zstd | staged-entropy-s5 | 3 | 7.5 | 11/35 | 0 | +0.0000 / 0.0000 |
| parquet-zstd | staged-s5 | 3 | 7.5 | 16/35 | 0 | +0.0000 / 0.0000 |
| parquet-zstd | staged-top1 | 3 | 7.4 | 3/35 | 0 | +0.0000 / 0.0000 |
| safetensors | exhaustive | 2 | 14.4 | 22/22 | 0 | +0.0000 / 0.0000 |
| safetensors | staged-entropy-s5 | 2 | 14.2 | 22/22 | 0 | +0.0000 / 0.0000 |
| safetensors | staged-s5 | 2 | 14.7 | 22/22 | 0 | +0.0000 / 0.0000 |
| safetensors | staged-top1 | 2 | 13.5 | 2/22 | 0 | +0.0000 / 0.0000 |
| sqlite | exhaustive | 3 | 37.7 | 35/35 | 0 | +0.0000 / 0.0000 |
| sqlite | staged-entropy-s5 | 3 | 22.1 | 5/35 | 0 | +0.0000 / 0.0000 |
| sqlite | staged-s5 | 3 | 22.3 | 6/35 | 0 | +0.0000 / 0.0000 |
| sqlite | staged-top1 | 3 | 22.1 | 3/35 | 0 | +0.0000 / 0.0000 |
| tar | exhaustive | 2 | 36.9 | 22/22 | 0 | +0.0000 / 0.0000 |
| tar | staged-entropy-s5 | 2 | 22.5 | 3/22 | 0 | +0.0000 / 0.0000 |
| tar | staged-s5 | 2 | 22.5 | 3/22 | 0 | +0.0000 / 0.0000 |
| tar | staged-top1 | 2 | 22.3 | 2/22 | 1 | +0.0537 / 0.1579 |

Reading: staged-5% gave **zero regret and zero wrong choices on every family** (23 targets), with 70–90% fewer exact (deflating) encodings where candidates are plentiful (DEX 3–5/22, tar 3/22, SQLite 6/35, Parquet/Snappy 6/35); the safetensors control needs all 22 (nothing is cheaply rejectable when every candidate is equally bad). Wall time falls 26–41% (tar 36.9 → 22.5 s, SQLite 37.7 → 22.3 s, DEX 8.0 → 5.7 s, Parquet/Snappy 12.6 → 9.3 s); what remains is matching every candidate, including the deliberately unrelated negatives, which dominates now. Top-1-only (no slack) is **rejected**: wrong choices in CLR/DEX/tar, up to 15.8% byte regret on a tar target. A cheap containment pre-probe (match only a sample of the target before indexing a candidate) is the next step and is **not implemented**. Caveat: 23 targets in 9 families is a small sample; "zero regret" is a measurement here, not a bound.

## 5. Similarity index (`dcore/postings.go`, `runset.go`) — measured
Postings are (feature u64, id u32). Baseline B1: `sort.Slice` (reflection swapper) over 16-byte structs, flat 12-byte table, ~25 `ReadAt` calls per lookup. New: one scatter pass on the top 16 feature bits + cache-resident local `slices.SortFunc`, parallel across buckets; immutable run `ZSR1` = 65 537-entry directory + 10-byte entries (48-bit feature remainder, u32 id) + CRC32C, a lookup is one in-memory directory probe and **one** `ReadAt` of a bucket. Four postings per chunk, uniform random features (B1's own scale protocol), warm page cache:

| chunks | postings | B1 `sort.Slice` | `slices.SortFunc` | bucket sort 1 worker | bucket sort 4 workers | flat bytes → run bytes | lookup flat → run |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 k | 0.4 M | 0.114 s | 0.070 s | 0.019 s | 0.020 s | 4.80 → 4.26 MB | 5.9 → 0.65 µs |
| 1 M | 4 M | 1.19 s | 0.70 s | 0.26 s | 0.18 s | 48.0 → 40.3 MB | 8.6 → 1.7 µs |
| 10 M | 40 M | 13.97 s | 7.59 s | 4.11 s | **1.88 s** | 480 → 400 MB | 12.0 → 10.2 µs |
Sorting is **3.4× (1 worker) to 7.4× (4 workers)** faster at 10 M; serialization 15.0 s → 1.2 s; the file is 17% smaller. The 10 M lookup gain vanishes (10.2 vs 12.0 µs): buckets average 600 entries (6 KB) and the table no longer fits the page cache's hot set; sharding by a longer prefix is the obvious remedy and was not tried. Resident cost during the sort is `2×16 B/posting` (scatter scratch) — the sort does not lower peak memory; the run file is the on-disk reduction, and `RunSet` bounds memory by the memtable. Peak RSS of the 10 M harness (2.45 GiB) holds four copies of the input for the four variants and is a harness figure, not an engine requirement.

`RunSet` (LSM-style) — equivalence and safety, measured by tests: for hot features (>64 ids) it returns **exactly** B1's `Index.Query` rankings across flushes and a compaction; for cold features tombstoned ids disappear immediately and compaction drops them; compaction writes and fsyncs the merged run, opens and CRC-verifies it, and only then drops inputs (a failed flush keeps the memtable). A corrupted run (every sampled byte flip is caught by `Verify`; bucket-order violations are caught by `Lookup`) **lowers recall only**: it cannot invent ids, and nothing in the engine takes a deletion or identity decision from it. Generations are the run file names; fallback = ignore the run and rebuild from object features. Recall on real histories equals B1's by construction (identical retention rule: newest 64 ids per feature); B1's measured useful-candidate recall (local 68/69, bottom 65/69) is inherited, not re-measured.

`RunSet` at 1 M objects × 4 features (memtable limit 262 144 postings, real files with fsync+rename, quiet machine): append **1.6 µs/object** (16 flushes, 81 MiB heap), `Query` of 8 features **53 µs with 16 runs → 7.6 µs after compaction**, deleting 100 k objects 6 ms (tombstones), compaction 1.4 s (heap 48 MiB), reopening the compacted run 0.03 ms, CRC verification 8 ms. Not measured: multi-GiB indexes, concurrent writers, a persistent tombstone manifest with generations on disk (the encoder/decoder exist and are tested; publication ordering is the caller's).


## 6. Group accounting and plan selection
**Exact model (`dcore/acct.go`, simulation, 60 seeds × 80 random assignments checked against an independent reference implementation).** A content (object body by SHA-256, `Refs` manifests) is *live* if referenced or a base of a live content; a chunk is *needed* iff some live content in CDC or rep form lists it; a delta content needs only its record plus its bases' bodies (a CDC-form base is pinned). Totals are reported at the lifecycle stages the task lists: `Logical` (live chunks+records), `AfterGC` (dead loose chunks and fully dead packs gone; **partially live packs keep their dead bytes**), `AfterRepack(threshold)`, `Peak` (everything pre-existing plus all new bytes, before deletion), `NewBytes`. Unchosen rep components are neither live nor free; two manifests sharing a content SHA count once; a component shared by several contents counts once. Depth/fanout/cycle violations make a plan infeasible. `ExclusiveSaving` reproduces the B8 defect (chunks referenced by one content only); `GroupReclaimable` is the corrected rule (chunks whose total live references equal the references inside the migrating set) — the same predicate as the B8 diagnostic patch (`refs[sum]==inG[sum]`). Test `TestExclusiveAccountingDefectAndFix`: three versions sharing 80 of 86 chunks: the exclusive rule sees 60 000 reclaimable bytes per member and **refuses a representation that the union rule prices at < 0.7× CDC**.

**Planner quality vs an exhaustive oracle (simulation: 40 random depth-limited chain models, 5–7 contents, ≤3 options each, exact enumeration).** Regret = plan total / oracle total − 1 at the repack stage:

| planner | mean regret | worst | evaluations per model (order) |
|---|---:|---:|---|
| B2 "best of two" pure strategies (even with exact union scoring) | **68.5%** | 393% | O(n·opts) |
| hill climbing (single + neighbour-pair moves) from best-of-two | 3.8% | 96.5% | O(passes·n·opts²·window) |
| roots local search (facility-location style) | 20.8% | 86% | O(n²) per pass — **rejected** |
| **chronological beam, width 8, feasible-rollout completion** | **0.9%** | **9.0%** | O(n·width·opts·n) |
Negative result worth keeping: *pure per-kind strategies lose badly under a depth bound* — the cheapest option per content builds chains that exceed the bound, so later contents fall back to CDC (best-of-two 68% above optimum on these models), and the B7-style point is reproduced: choosing per object without pricing the set destroys sharing. The beam's cost is polynomial in group size (a group is the contents of one kind/key family, not the store), so planning complexity does not grow with history length. **These are model results**: delta sizes in them are random, not measured; they validate the *search*, not production economics. Real-data delta matrices (all-pairs `dcore.EnvelopeSize` on the corpora) feeding the beam, and a comparison of the model's `Logical` prediction with the measured packs, were not completed.

## 7. Migration: the B2 draft vs the scratch P5 planner — measured on real stores
Method: `TestB2Economics` of the draft, one fresh process per family, bytes = `dirBytes` of the store after `gc -apply` + repack(≤100% live) + compact + gc; CDC baseline = same corpus PUT with authority off. "legacy" = the draft's planner (`Planner ""`), **"p5" = the scratch change** (`z4/p5/b2_p5_planner.diff.txt`, applied to a copy of the draft worktree, never to this repository): union accounting (the B8 rule) in `planRepGroups`, the plan-only delta encodings and constructed rep roots reused by the applying pass instead of recomputed, progressive rep construction that abandons a group once its projected union cannot beat the delta plan, and an optional **default-off** heuristic gate `RepSkipIfDeltaAtMost` ("p5skip", 0.6). A first p5 version regressed hot-hybrid SQLite (0.79× vs 0.55×): cached plan-only encodings are invalid once apply-time gating (read-amplification/proof) rejects an object. It is kept in the evidence (`b2_p5_ab.jsonl`) and was fixed by a stale-cache rule.

| family | CDC MB | draft `best` (archive): x CDC / migrate s / peak x | **p5 `best`**: x CDC / s / peak x | p5 + skip gate 0.6: x CDC / s / peak x | draft delta-only: x CDC / s | rep-only draft (with union accounting) x CDC |
|---|---:|---|---|---|---|---|
| coreutils tar x3 | 45.11 | 0.499 / 40.5 / 3.24 | **0.499 / 29.7 / 2.67** | 0.499 / 11.5 / 1.16 | 0.499 / 7.6 | 1.067 |
| SQLite x4 | 94.13 | 0.295 / 20.0 / 1.47 | **0.295 / 15.8 / 1.39** | 0.295 / 8.9 / 1.02 | 0.295 / 5.4 | 0.404 |
| CLR Newtonsoft x5 | 1.40 | 0.817 / 4.4 / 4.00 | **0.817 / 2.2 / 2.77** | 0.817 / 2.2 / 2.77 | 0.817 / 0.8 | 0.901 |
| Git packs (hiredis) x3 | 2.72 | 0.582 / 5.3 / 2.13 | **0.582 / 4.4 / 1.96** | 0.582 / 0.4 / 1.19 | 0.582 / 0.3 | 1.000 |
| Parquet pyarrow/Zstd x4 | 19.27 | 0.935 / 22.1 / 3.13 | **0.935 / 18.9 / 3.13** | 0.935 / 19.1 / 3.13 | 0.935 / 1.5 | 1.000 |
| Parquet pyarrow/Snappy x4 | 21.63 | 0.476 / 17.8 / 2.38 | **0.476 / 11.4 / 2.06** | – | 0.476 / 2.0 | – |
| Parquet Z3-31 chain x3 (Snappy) | 35.99 | 0.901 / 24.3 / 2.18 | **0.775 / 70.8 / 2.18** | – | 0.901 / 1.7 | 1.000 (0.775) |
| Parquet Z3-31 chain x8 (Snappy) | 95.39 | 0.916 / 68.7 / 1.49 | **0.326 / 166.3 / 1.48** | – | 0.916 / 12.0 | 1.000 (0.326) |
| DEX classes.dex+classes2.dex x6 | 23.35 | 0.903 / 141.6 / 3.75 | **0.903 / 79.4 / 2.58** | 0.903 / 80.1 / 2.58 | 0.903 / 4.5 | 0.884 |

x CDC is the final physical size after the full reclamation sequence over the same family's CDC store; `peak` is the largest `dirBytes` seen while migrating over the CDC size; times are single runs (±15% run-to-run on this VM; the draft's own tar `best` measured 43.3 s in an earlier run and 40.5 s here). Raw rows incl. read, recovery and blast-radius records: `z4/p5/results/`. Draft rows come from the draft's own binary; "draft `best`" for tar/SQLite/CLR/Git/Parquet is the P5 binary with `Planner ""`, which executes exactly the draft code path.

**Findings (measured).**
1. **The B8 accounting defect is reproduced to the byte and fixed.** On the Z3-31 pyarrow 8-revision chain the draft's rep-only plan admits **0 of 8** objects (1.000×, "no admissible exact representation"); the union rule admits all 8 and stores **31.07 MB = 0.326× CDC (95.39 MB)** — identical to B8's diagnostic figure — and my `best` planner now *selects* it automatically (the draft's `best` stays at the delta result 0.916×). A **second** instance of the same defect was found and fixed: the `best` comparator (`total()`) also summed *exclusive* bytes for kept objects, so even with a corrected group planner the delta plan won (first p5 attempt: 0.916×). On the 3-revision chain: 0.901× → **0.775×**. Delta-only, tar, SQLite, Git, CLR, DEX results are byte-identical between planners (0 bytes of regret).
2. **Union accounting needs a safety margin.** With the comparator corrected and no margin, Parquet/Zstd flipped to a representation (0.954×) that was *worse* than delta (0.935×): the estimated union cost counts component sizes before pack compression and was 2% optimistic. p5 now requires the representation to win by 5% on the estimate; DEX (rep 0.884× vs delta 0.903×, 37× the CPU) correctly stays delta. The margin is a tuned constant from 9 families.
3. **Speculative construction is only partly removed.** Reusing the plan-only encodings and constructed roots removes the third full pass: tar 40.5 → 29.7 s, CLR 4.4 → 2.2 s, DEX 141.6 → 79.4 s, Parquet/Snappy 17.8 → 11.4 s, Git 5.3 → 4.4 s, SQLite 20.0 → 15.8 s, **bytes unchanged**, peak scratch 3.24× → 2.67× (tar), 4.00× → 2.77× (CLR), 3.75× → 2.58× (DEX). The 4-10× penalty over delta-only (tar 5.3×) remains **unless the default-off gate is used**: with `RepSkipIfDeltaAtMost=0.6` the rep plan pass is skipped when the delta plan already costs ≤ 60% of the group's chunk bytes — tar **11.5 s (3.5× faster), peak 1.16×**, SQLite 8.9 s (peak 1.02×), Git **0.4 s (13× faster)**, same bytes as delta-only. The gate is a heuristic, not a bound: it can skip a representation that would have won (none did in these families: rep-only was worse than delta wherever delta ≤ 0.6×), and it does nothing for CLR/Parquet/DEX where delta saves less than 40% and the representation is a live alternative. For those the speculative cost is the representation construction itself (§17).
4. **Migration RSS** stays 120-570 MiB (whole target + candidate bases resident; unchanged by planning); the ZW1 path (§9) is the memory fix, not the planner. **Scratch space**: peak falls with plan reuse and the gate (above) but a rep plan still stages components (1.5-3.1×).
5. A first p5 version (cached encodings without a staleness rule) regressed hot-hybrid SQLite 0.554× → 0.790×; the A/B caught it, the rule `stale` fixed it (hot-hybrid rows equal the draft in all five families where it was run — tar 0.731, SQLite 0.554, CLR 0.759, Git 0.857, Parquet/Zstd 1.000 — measured with the build before the comparator/margin changes; hot-hybrid was not re-run on the final build).

## 8. Reads and ranges (measured, draft baseline; cold device reads not measured)
Logical CAS bytes touched per 4 KiB / 64 KiB request, `RangeAmp`; whole-object MB/s warm-process. CDC vs draft forms:

| family | CDC whole MB/s | delta-only whole | hot-hybrid whole | CDC 4 KiB | delta 4 KiB | hot 4 KiB | REP-only 4 KiB |
|---|---:|---:|---:|---|---|---|---|
| coreutils tar | 150 | 78 | 126 | 0.75 ms (37×) | 2.56 ms (85×) | 1.37 ms (47×) | 3.5 ms (**15 198×**) |
| SQLite | 115 | 98 | 91 | 0.73 ms (23×) | 1.14 ms (41×) | 1.12 ms (29×) | 0.32 ms (7 814×) |
| CLR | 86 | 43 | 54 | 1.32 ms (31×) | 2.72 ms (60×) | 1.64 ms (36×) | **148 ms** (172×) |
| Parquet/Zstd | 429 | 249 | 392 | 0.18 ms (26×) | 0.73 ms (69×) | 0.29 ms (26×, = CDC) | 0.15 ms (26×) |
| Git | 350 | 164 | 185 | 0.13 ms (17×) | 0.67 ms (35×) | 0.31 ms (21×) | 0.13 ms (17×) |
| DEX | – | 53 | – | – | 3.4 ms (65×) | – | **821 ms** (2 219×) |
Windowed delta (dcore ZW1, synthetic 256 MiB base, 4 workers): frame size is the range-amplification knob, because every frame is authenticated by its own SHA-256 and so must be reconstructed whole: 256 KiB frames → 4 KiB request reads **80×**, 64 KiB **6×**, 1 MiB **1.3×**, record +14% (394 462 B vs 346 563 B at 16 MiB frames); 1 MiB → 272× / 17× / 2.0×; 4 MiB → 1 027× / 68× / 5.2× (1 GiB base); 16 MiB → 3 993× / 247× / 16.4×, 23 ms per 4 KiB request. Design consequence (not implemented): hash fixed 64 KiB leaves *inside* a frame so a range materializes only the touched leaves — the frame program is cheap to scan, only the touched ops must be expanded.

**Recommendation by tier** (evidence-based, not a deployed claim): **hot** — CDC stays authoritative for the newest versions; older versions may be delta-authoritative only under a read-amplification admission cap (`hot-hybrid` keeps 4 KiB amplification ≤ 1.3× CDC and 53–91% of CDC whole-read throughput while saving 0.55–0.76× on SQLite/CLR/tar and correctly admitting nothing on Parquet/Git); **warm** — delta-authoritative at depth ≤ 2 with 16–32 KiB frames (2–3× 4 KiB amplification, 0.5–0.9× whole-read throughput); **cold/archive** — delta at depth ≤ 4, and representation bodies only for kinds with a range decoder (REP reads of tar/SQLite/DEX/CLR cost 3–800 ms per 4 KiB and are not suitable for anything but offline archives). Blast radius grows with chain depth (draft, tar: losing one physical chunk breaks on average 2.64 of 3 contents under delta authority vs 1.04 under CDC).

## 9. Large objects (measured)
`dcore` ZW1: the base is indexed by a streaming parallel pass into a fixed-size anchor table (4 Mi anchors ≈ 64 MiB at load 0.5 regardless of base size); the target is read, matched and emitted in independent windows through a bounded per-worker 64 KiB-page cache of the base (`ReaderAt`), COPY offsets are absolute 64-bit, so a window never refers to a base region another window owns and cross-window insertions/deletions/moves/duplicates need no coordination; each frame is deflated only when smaller and carries its SHA-256; the object SHA-256 and length are proven at the end; `DecodeRange` decodes only covering frames. Resources, one process, 4 workers, files on ext4 (page cache warm; RSS = `VmHWM` after encode, which includes the generator):

| input | window | record bytes | vs ideal | encode MiB/s | index MiB/s | decode MiB/s | peak RSS MiB | verified |
|---|---:|---:|---|---:|---:|---:|---:|---|
| synthetic 64 MiB | 4 MiB | 99 813 | 1.01× literal | 633 | 86 | 395 | 249 | SHA-256 + 150 ranges |
| synthetic 256 MiB | 4 MiB | 348 945 | 1.01× | 831 | 241 | 414 | 256 | ✓ |
| synthetic **1 GiB** (1 GiB target) | 4 MiB | 1 097 593 | 1.014× | 709 | 615 | 410 | **257** | ✓ |
| **real concatenated multi-revision, 377 MiB base / 373 MiB target** (3 tar, 4 SQLite, 4 Parquet, DEX, CLR; blocks reordered across revisions) | 4 MiB | 6 027 301 (1.54% of target) | – | 628 | 432 | 359 | 230 | ✓ |
| real, 223 MiB / 222 MiB | 4 / 1 MiB | 8 832 667 / 8 879 350 | – | 428 / 463 | 294 / 303 | 308 / 291 | 286 / 233 | ✓ |
All records verified against an independently computed `sha256sum`/in-process SHA-256 of the target; the 1 GiB case is **beyond B2's 256 MiB ceiling by 4×** with a flat working set (257 MiB vs the draft's 447–593 MiB for 187 MB). Caveats: the synthetic inputs are long-copy dominated (hence 600–800 MiB/s); the real object is 377 MiB, not ≥ 1 GiB of real data; a hostile base is protected only by the per-frame hashes (the base identity is *recorded*, and a caller must supply authenticated base reads, as B2's CAS does); matches inside the target (self-reference) are not implemented; windows are encoded independently, so a match crossing a window edge is split in two COPYs (the ratio cost is visible in the 256 KiB row above).

## 10. Integration rehearsal on current main (scratch, outside the repository)
`git merge --no-commit origin/z4/representation-authority` into `d59ff71` in a scratch worktree: **13 conflict hunks in 8 regions** of `zeros3.go`, all structural: (1) the `Store` struct (P2/P4 `fgGate`, `mcache`, `ccache`, `pipeStreams` vs B2 `auth authState`); (2) the `UploadPartCopy` reader (P2 `newServedReader` vs B2 `openObjectReader`); (3) **the P3 reachability pipeline**, 4 hunks — P3 replaced the single-pass root walk (`checkRoot`/`want`) in which B2 hooks `wantMan`/`wantRep`/`actx.observe` with a staged, parallel `reachStep` pipeline, so authority liveness must be re-expressed as a new step kind with the same fail-closed contract; (4) `Verify` (P3 releases the length table before pack/whole-object decoding; B2 adds `AuthorityBodies/Orphans`); (5) `manifestReaderAt.ReadAt` (P4's probe-vs-sequential served reader vs B2's `openObjectReader`); (6) `repBuild` — P4 split it into `repBuildFor` with a liveness check, phase timers and `publishRepRootAt(floor)`, B2 split it into `repConstruct` + publish; (7) `repConstruct`'s error path (P4 build counters); (8) `openObjectReader` itself — P2's `servedManifest` fast path vs B2's `authOpenReader` hook. I judged a byte-faithful port (reachability, `pipeReader`, `servedManifest` cache, `verifyWholeObjects`, `replacePacksToLive`, segment-resident chunks) to need a dedicated, separately qualified batch and **did not force one**; no authority code was merged anywhere, and the planner measurements above therefore use the draft at its own base `94ac9c8` (pre-P1A). Consequences for the claims: planner **economics** (bytes, plan quality, peak) transfer because they depend on chunk/pack accounting that P1-P4 did not change in kind; **timings and read numbers do not** (P2 caches, P1C segments, P3 scans change them) and must be re-measured after the port.

Answers to the rehearsal questions: *Which production paths need adaptation?* — the 13 in the audit below, plus the whole P3 scan. *P1C segment assumptions that conflict?* — B2's liveness/GC assume chunks are loose files or sealed pack records; segment-resident chunks (level 11) need authority-aware seal/recovery (`replacePacksToLive` reads chunks by hash and would need to treat "chunk reachable only through a body" as live). *P2 caches reusable?* — the verified manifest/chunk caches are keyed by content hash and never authoritative, so they are safe to share; the served-manifest fast path (`newServedManifest`) must learn that a retired chunk is absent. *P3 reachability?* — needs the new step kind (above). *P4 deferred worker?* — its contract (components durable before the root, exact proof, atomic rename, abandon on close) maps one-to-one onto authority construction, so offline migration could be a deferred job; not attempted. *Optimized B1 algorithms through a production-compatible interface?* — yes: §15, extraction demonstrated. *Safe revert / old-binary refusal?* — draft evidence only (§12).

### 10.1 CDC-retirement call-site inventory (`z4/p5/cmd/casaudit`, go/ast, run on `d59ff71`)
51 functions directly call a chunk-bytes/presence primitive (`casRead*`, `casStat*`, `newServedReader`, `newManifestReaderAt`, `manifestBytes`) or iterate `manifest.Chunks`. Classification is mechanical and says **only** whether the B2 draft's *same-named function* references authority (a lower bound on awareness, not a proof): 2 do (`handleUploadPartCopy`, `manifestReaderAt.ReadAt`, both via `openObjectReader`); 36 are unchanged in the draft; **13 do not exist in the draft** because they were introduced by P1-P4 and have never been audited against retired CDC:

| function (line on main) | operation | must become authority-aware | tested? |
|---|---|---|---|
| `casPublishClaimed` (1244) | PUT publish (P1B) | presence of a retired chunk must be defined: absent → rewritten (re-materialises CDC, benign); "present via the virtual index" must never let a PUT skip a write it needs | no authority test |
| `loadManifestBytesH`, `servedManifest.manifest`, `newServedManifest` (5677/5804/5825) | GET/HEAD/range (P2 cache) | served-manifest cache must resolve through bodies | no |
| `analyzeManifest`, `checkChunk`, `verifyWholeObjects` (12703/13142/13885) | verify (P3) | deep verify must prove bodies, not chunk lists | no |
| `replacePacksToLive` (15436) | repack/compact | liveness through bodies; segment-resident chunks | no |
| `newStreamReader`, `newPipeReader`, `pipeReader.run/next` (17724-17832) | GET pipelined large responses (P2) | read via body reader | no |
| `repBuildFor` (33335) | representation construction (P4 deferred) | source read via body reader | no |
The remaining 36 include every path the task lists: `Store.RestoreObjectVersion`, `CopyObject`, `handleUploadPartCopy`, `multipartReader.Read`, `handleSync{DescribeObject,Negotiate,ChunkDownload,Commit}`, `handleBulk{Negotiate,Fetch}`, `planReplication`, `repairFindings`/`repairFromPeer`, `exportBundle`/`bundleImporter.chunk`/`exportDelta`/`checkBaseRef`, `handleSnapshotDescribeObject`, `restoreObject`, `buildObjectDiff`, `inspectObject`, `storeRepSource`. The draft rerouted *callers*, mostly via a virtual chunk index (so bulk/sync can slice retired chunks out of a body); that mechanism is unaudited on main. GC, `compact` ranking and `versions prune` are not in the table because they consume the reachability pipeline (conflict region 3), not `casRead` directly. **Conclusion: CDC retirement must not be enabled in any qualification store until each row above has an authority test; the inventory is a worklist, not a pass.** Full machine-readable list: `z4/p5/results/cas_audit.json`.

## 11. Why the scratch merge stopped (honest scope statement)
The task asked for a scratch production integration. What exists: the unmodified draft run on its own base (§7, baseline B), the scratch P5 planner on that base with the draft's authority suite subset run against it (§14), the conflict inventory above, and a gate matrix against real current/draft binaries (§12). What does **not** exist: the draft rebased onto P1-P4, the draft's full authority suite on main, CDC-retirement differential tests on main, P1C-segment × authority tests, or crash tests of the combined engine. `P5_PRODUCTION_READY` and any claim about the combined engine's timings are therefore unavailable by construction.

## 12. Store-format capability resolution (real-binary matrix + executable model)
Observed with real binaries (`z4/p5/scripts/formatmatrix.sh`, `results/formatmatrix.txt`): current main accepts levels 1-9 and 11 and refuses 10, 12, 13, 99 with `unsupported store format version`; the draft accepts 1-10 and refuses 11-13, 99; refusals leave the store tree byte-identical. (Levels ≥ 5 in the fixture fail later on missing tier roots — the version gate had already accepted — which is a fixture artefact.) The source rule is `supportedStoreFormat`: `v>=1 && v<=9 || v==11` — **not** a numeric maximum, which is why a numeric "raise to the highest supported" rule would be wrong: it would let a segments-only build open an authority store (10, 12).

Proposal (`z4/p5/fmtcap`, executable model with tests; **activates nothing, publishes no floor**): treat levels as *labels of feature sets*, not an order — `{}`→9, `{segments}`→11, `{authority}`→10, `{segments, authority}`→**12** — and gate by `label known ∧ required features ⊆ implemented features`. Tested properties: (1) no binary accepts a store requiring a feature it lacks (4 binary models × 15 levels); (2) authority is never published at a level any non-authority binary accepts; (3) adopting authority on a level-11 (segments) store publishes **12, not 10** (publishing 10 would silently downgrade segment semantics and old segment-aware binaries would refuse for the wrong reason); adopting segments on an authority store publishes 12; (4) features are only dropped (`12→11`) when no record of that feature remains (zero bodies and CDC chunks materialized+fsynced) — never automatically; (5) the numeric-max rule is demonstrated unsafe (accepts 2 unreadable levels for a segments-only build). The model reproduces the observed gates of both real binaries. A later explicit `required_features` array in `FORMAT.json` (ext4-style incompat flags) would make the label table unnecessary; it needs one more floor and is a future decision. **Not tested:** a combined binary (it does not exist).

## 13. Crash, corruption and failure qualification
*Executed:* `dcore` — bit flips over the whole windowed record (never an accepted wrong output), truncation at 300 lengths, corrupt/short-read base, short and failing writers at five cut points, hostile bounds (`ErrTooLarge`, window/frame limits, frame-table overflow), range past end, run-file byte flips caught by `Verify`, bucket-order corruption caught by `Lookup`, failed flush keeps the memtable, corrupt runs lower recall only, tombstone-list corruption; `-race` on the whole package; `FuzzMatchExact`, `FuzzWindowRecord`. *Draft-level (scratch B2 on its base), run against the P5 planner build:* `TestP5Planner*` (exact reads after migrate, deep verify, gc+repack+compact, **revert of every object**) and the draft's own authority suite subset (§14). *Not executed:* SIGKILL/ENOSPC/short-write injection against the combined engine (it does not exist), power loss, concurrent migration+read on main, snapshot/retention races on main. B2's 26-case SIGKILL matrix and injected-ENOSPC tests are the draft's own evidence, not re-run on main.

## 14. Validation actually run
All commands from the repository root unless stated; Go 1.27.0.
| Check | Result |
|---|---|
| `gofmt -l` on `z3/dcore`, `z3/simdelta`, `z3/cmd/{z4p5bench,dcoreport}`, `z4/p5` | clean |
| `go vet` on the same + `z3/mdelta2`, `z3/cmd/z4b5` | clean |
| `go -C testing-harnesses/z3 test ./dcore ./simdelta` | PASS (dcore: matcher round trips ×120 seeds, adversarial/collision/overflow, window, run/RunSet, accounting reference check over up to 4 800 random assignments (≥1 000 feasible), planners vs exhaustive oracle, estimator bounds; simdelta: existing B1 suite incl. 800 randomized round trips + native-Git oracle, plus 120 new matcher-differential cases) |
| `-race` on `./dcore ./simdelta` | PASS |
| `go test -fuzz FuzzMatchExact -fuzztime 20s` / `FuzzWindowRecord -fuzztime 20s` | 923 515 / 1 559 387 execs, no failure |
| `./gitz3 ./tdelta ./mdelta2 ./cmd/z4similarity ./cmd/z4b5` (dependants of the changed `simdelta`) | PASS |
| `go -C testing-harnesses/z4 test ./p5/...` (format-capability model) | PASS |
| `dcoreport` (type-aware extraction → compile into temp copy of `zeros3.go` → differential) | PORTABLE, 61/61 vectors, 0 fidelity failures (`results/extraction.json`) |
| Fresh-process replay: `wencode` then `wdecode` in separate processes, `sha256sum` of the output vs the target | identical (`d9495823…576e0`, 391 654 997 B), decoder peak RSS 51.5 MiB |
| Scratch B2 + P5 planner (`94ac9c8` + `b2_p5_planner.diff.txt`): `go test -run 'TestXDelta\|TestAuthority\|TestP5' .` — 27 test functions incl. the draft's SIGKILL crash matrix, injected ENOSPC/short writes, corrupt component, graph limits, old-binary refusal, replication/sync/bundle, concurrency | PASS (87.6 s) |
| Production unchanged | `git diff origin/main -- zeros3.go go.mod` empty; `zeros3.go` sha256 `0a842054…c85`, `go.mod` `9ecf9246…288`; no default, floor, format or S3 behaviour changed; Draft PR #97 and drafts #80/#81 untouched |
**Not run (and why):** the full root production suite and the external S3/sync validation groups (P4 did not run them either; nothing under `zeros3.go` changed, so there is no concrete reason); the draft's tests on current main (the port was not done, §11); cold-device reads; power-loss/fsync-fault injection; `-race` on the scratch B2 build; a soak. The 20-second fuzz runs are smoke tests, not campaigns.


## 15. Mechanical portability (`dcoreport`) — executed
`go run ./cmd/dcoreport -dcore dcore -prod ../../zeros3.go -work W -z3 .` (~12 s): type-checks `dcore` (**stdlib-only; any non-stdlib import or unresolved external identifier would print `PORTABILITY_BLOCKED`**), renames all 117 package-level names with a collision-checked `dc` prefix using `go/types` object resolution (not text search), merges 148 declarations (48 funcs, 48 methods, 37 types, 8 vars, 7 consts) and their imports into a temporary copy of the production `zeros3.go` + `go.mod` (**no imports had to be added; 0 modified adapters; 0 unresolved dependencies**), formats, compiles it, runs the same 61 differential vectors in the original package and in the extracted code — matcher op digests, `Apply` output, `EnvelopeSize`, estimator, `Program`, `Select`, the **windowed record bytes (single worker), decode and range output, authentication failure on a corrupted base**, sort agreement, run-file bytes, lookups, tombstones, `Eval` totals, every planner's totals — and requires byte-identical results; plus a token-level fidelity check that each extracted declaration equals the original after reversing the rename. Result: `PORTABLE`, 61/61 vectors identical, 0 fidelity failures, integration SHA-256 recorded in `results/extraction.json`. Nothing generated was committed. The adapters that remain for a real port are the serializer (keep B2's `xdEncode`) and the `ReaderAt` that serves authenticated base chunks; `simdelta`'s `matchOps` is the existing example of a thin adapter around the core.

## 16. Rejected / negative results
(1) **Single-configuration "3-6× faster matcher with equal or better ratio"** — only true at the fast operating points (§3). (2) **Top-1 candidate selection without slack** — up to 15.8% byte regret, wrong choice in CLR/DEX/tar (§4). (3) **Roots local search planner** — 20.8% mean regret (§6). (4) **Pure per-kind "best of two" even with exact union scoring** — 68.5% mean regret vs an exhaustive oracle on depth-limited chains. (5) **First p5 planner (plan reuse without a staleness rule)** — regressed hot-hybrid SQLite to 0.79× (vs 0.55×); fixed. (6) **Early rep abandonment alone** — saved only 15-20% of tar/Parquet migration time; the speculative cost is the *first two* constructions. (7) **Bucket-sort memory** — no memory reduction (needs a scratch copy); the gain is time and file size. (8) **Order-0 entropy estimator vs sampled deflate** — both gave zero regret on every family; the entropy variant needed fewer exact encodings on CLR (9/49 vs 30/49) and DEX/tar equal; it was not made the default because 7 targets per family is too few to separate them. (9) **Base-index sharing across candidates / pre-probe** — considered, not built.

## 17. Remaining bottlenecks
(a) rep construction dominates every `best` pass (tar 12 s/object, Parquet 4 s, DEX 28 s) and re-decodes its source several times (P0: 48% DEFLATE-pack decode + 28% `tarPrescan`): a shared bounded decoded window is the fix and was not built; (b) matching every candidate including hopeless ones (§4); (c) staged peak 2.2-3.2× CDC whenever a rep plan runs — only avoided by *not building* (p5skip) or by reclaiming after each admission (not implemented); (d) the large-object path has no persistent base index and no self-reference matches; (e) the P3-pipeline port; (f) 4 KiB range amplification under authority is 2-3× CDC even for deltas and thousands for REP bodies.



## 18. Verdict and production-integration recommendation
**P5_PARTIAL.** Why not `P5_RESEARCH_READY`: the large strong results are real but three required items are missing or only partly met — (i) the B2 scratch integration on current main was **not completed** (P3 pipeline port, §10-11), so there is no combined-engine timing, crash or CDC-retirement evidence; (ii) the "remove the 4-10× speculative penalty" target is met only through a **default-off heuristic gate** and only where delta already saves ≥40%; where a representation is a live alternative (CLR, Parquet, DEX) construction cost remains; (iii) the matcher's 3-6× speed is workload-dependent. Why not `P5_NEEDS_REDESIGN`/`NO_MEANINGFUL_GAIN`: the corrected accounting recovers the Z3-31 Parquet chain's 0.916× → **0.326×** automatically, the windowed codec lifts the object limit to ≥1 GiB with a flat ~250 MiB working set, the index and estimator gains are large and safe, and the core extracts mechanically. Why not `P5_BLOCKED_CORRECTNESS`: no integrity defect was found in anything executed; the one regression found (stale plan cache) was caught by measurement and fixed.

**Safe to merge now (this PR):** research code only — `dcore`, the `simdelta` adapter (decoder/wire unchanged, reference matcher retained), drivers, extraction tool, audit tool, format model, evidence, report. **Not merged and not proposed for main:** any B2 authority code, the scratch planner diff (it is evidence), any floor/format activation. **Ready to carry into a future, separately qualified authority port:** the matcher + estimator core (extracted and differential-tested), the union accounting rule *and its comparator* (two sites in the draft), plan reuse with a staleness rule, the windowed codec as the >256 MiB path (with leaf hashes added for range amplification), the run-based index. **Not ready:** the skip gate (needs a bound or a probe), the 5% margin (tuned on 9 families), anything about read performance on main.

**Remaining blockers to authority in production (unchanged from B2 plus P5 findings):** port B2 liveness into the P3 reachability pipeline and the P1C segment layer; audit the 13 post-B2 chunk-touching functions (§10.1) and give each an authority test; combined-engine SIGKILL/ENOSPC/power-loss matrix; a read-amplification admission policy per tier; the composite format label (§12) activated only with an old-binary refusal test on the real combined binary; operator runbook for irreversibility/revert. **Next batch per the roadmap is P6 (B3/B4 semantic CAS and graph normalization);** P5 does not add mandatory batches. If the authority line is continued, its first prerequisite is the port above, not another algorithm.

*Strongest negative result:* correct per-kind group scoring is not enough — pure "best of two" strategies are 68% above the optimum on depth-bounded chains, and *both* places where the draft priced kept CDC by exclusive bytes (the group planner and the best-of-two comparator) had to be fixed before the Parquet chain's 0.326× could be selected by the planner itself; and the union estimate needs a margin because it was 2% optimistic against the real packs.
