# B7: next-generation similarity discovery

Linux amd64, Go 1.27.0, 4 vCPU Firecracker VM, virtio SSD; base `3c38c52` (B6). Cold reads drop the page cache
and every in-process cache before each read; every read is checked byte for byte. Families and criteria were
fixed before any comparative measurement ([PREREGISTRATION.md](PREREGISTRATION.md), first commit `95a0b0a`).
Raw rows: [results/](results/) (`tables.py results/*.jsonl` prints every table; [results/tables.md](results/tables.md)
is its output); inputs: [corpus.sha256](corpus.sha256) (B6 corpus plus `make_inputs.py`); reproduction: [run.sh](run.sh).

**Verdict: `B7_DISCOVERY_SPECIALIZED`.** Two of the twelve pre-registered families gain meaningfully from the
new discovery and are the only ones that change by more than 0.3%: the large-object Coreutils history
(**−8.3% archive, −9.2% mixed** allocated bytes, from denser COPY anchors on bases above 32 MiB) and the vendored
partial-overlap family (**−7.4% archive, −6.2% mixed**, from complementary multi-base candidates). Every other
family, including both small-file histories and all four negative controls, is within ±0.3%: on this corpus
B6's sketch shortlist already reaches 99.0-100% of the single-base benefit an exhaustive pairwise oracle finds.
The pre-registered EFFECTIVE bar (three independent families) is not met. Two engineering results carry beyond
discovery: representation candidates are now priced without being staged (planning writes on Coreutils
103 → 4.8 MB, peak allocated 146 → 48 MB), and the B6 search no longer degrades when offered more options. The
largest remaining lever on small files is not discovery but record-file allocation (an exact-bytes ablation
stores cu-files in 41% fewer modeled bytes); grouping records is a format change left for its own milestone.

## Stages and what changed
| stage | B6 | B7 |
|---|---|---|
| 1 feature extraction / index | bottom-16 MinHash of 16-byte windows per content; map feature → contents | + sampled positional 32-byte window fingerprints (nested levels), sorted posting index, per run (§16e2) |
| 2 retrieval / ranking | shared sketch features ×2, same-key bonus, top 4 (top 2 windowed) | rare-posting scoring, exact sample coverage of a 24-base shortlist, share-discounted (CDC-dedup aware), coverage floor |
| 3 verification | none (all 4 encoded) | matcher estimate of ≤8 shortlisted bases (only when more than 4 qualify); order interleaves coverage and estimate |
| 4 candidate construction | 1-base programs × locality, one refined 2-base; reps staged | + greedy complementary 2-4 base sets matched over the union index; reps built in memory; windowed bases 1 anchor / 8 B |
| 5 graph selection | B6 search | B6 search + a single-base restricted start (bounds a degradation B7's options exposed) |
| 6 publication | `authPublish`, proof per record | unchanged; a selected lazy rep is constructed, its root checked, then proven |

No persistent format, store feature or record kind changed. Envelopes are the existing xd (≤4 bases) and ZW2
formats; anchor density is an encoder parameter. The B3 persistent sketch index (ZSR1) and record sketches are
untouched (records still carry the bottom-16 sketch).

## B7-A: what B6 discovery leaves on the table
Exhaustive oracle: every ordered pair of in-memory contents matched exactly (dwDcore index, plain matcher),
the 16 bases copying most bytes per target estimated with the production estimator; best pair = union-index
match over every pair of the 6 best singles. *Benefit* = share of the oracle's best single-base saving over
standalone compression that the top-K shortlist reaches; "useful" = oracle best ≤ 0.9 × standalone.

| family | targets (useful) | B6 recall@1 / @4 | B6 benefit@1 / @4 | oracle pair over best single | B6 refine captures |
|---|---|---|---|---|---|
| cu-files | 865 (858) | 0.914 / 0.993 | 0.996 / **0.9998** | 0.30% | 0.04% |
| go-files | 529 (365) | 0.826 / 0.902 | 0.989 / **0.995** | 0.65% | 0.22% |
| vendor | 12 (12) | 0.167 / 0.500 | 0.973 / **0.990** | **8.66%** | 2.63% |
| bundle | 3 (3) | 0.333 / 1.000 | 0.854 / 1.000 | 2.08% | 1.24% |
| go, sqlite, tensors, git | 3-4 each | 0.67-1.0 / 1.0 | 0.997-1.0 / 1.0 | 0-0.25% | |
| ts, random, unrelated | 3-5 (0) | no useful base | | | |

Graph level (archive model, oracle candidate sets through the B6 optimizer; locality {0}):

| family | B6 candidates | oracle candidates | B6 + exact record bytes | oracle + exact record bytes |
|---|---:|---:|---:|---:|
| cu-files | 4.054 MB, 78 deltas | 4.055 MB, 77 | **2.411 MB, 519** | 2.403 MB, 537 |
| go-files | 3.906 MB, 61 | 3.898 MB, 61 | **3.045 MB, 194** | 3.013 MB, 196 |
| vendor | 2.824 MB, 8 | 2.762 MB, 8 (B7: **2.566 MB**) | 2.788 MB | 2.726 MB |
| go-files oracle, all other families | | −0.3% (go-files), ≤ −0.02% elsewhere | −0.7% to 0 | |

B7's final shortlist ([frontier.jsonl](results/frontier.jsonl); the first, estimate-ordered version is
[frontier-initial.jsonl](results/frontier-initial.jsonl)) reaches benefit@4 0.9993 / 0.9945 / 1.000 on
cu-files / go-files / vendor (raw recall@4 0.986 / 0.822 / 1.000): its order serves retained-storage value,
not the estimate (B7-B).

Three findings directed the rest. (1) Single-base retrieval is not the bottleneck anywhere: on the two
small-file histories the oracle's candidates change the selected graph by < 0.2%. (2) Raw recall misleads:
B6 misses the best-estimate base for 17% of go-files targets and still reaches 99.5% of the benefit, while
on vendor the estimate-best bases (peer tarballs carrying the *same* vendored `lib/`, already deduplicated by
CDC) are exactly the ones that free nothing. (3) What blocks the 858 useful small-file targets is the 4 KiB
allocation of each record file: priced at exact bytes, 519 instead of 78 deltas are chosen (B7-G).

## B7-B: positional fingerprints and the posting index (section 16e2)
**Features.** A 32-byte buzhash window is kept when its sampling hash `h·K` has at least `level` leading zero
bits; level starts at 7 (one window in 128) and rises with size until a content keeps ≤ 16,384 fingerprints
(level 9 at 7 MB, 12 at 60 MB). Levels nest, so two contents compare exactly at the larger of their levels and
a 60 MB base and a 30 KB target are still comparable. Each fingerprint keeps its mixed hash, its position
(16-byte units) and its level; repeats inside a content keep their first position (a zero run is one
fingerprint). Dense adversarial sampling raises the level instead of growing memory (bounded by 2 × MaxFeat per
slice). In-memory contents are scanned in 4 MiB slices in parallel (result independent of slicing, tested
against a sequential reference); larger ones stream once, computing the B6 sketch in the same pass. Cost
2.3-3.8 ns/byte (CDC v1 0.6, FastCDC 1.7).

**Index.** Postings (hash, content) sorted by hash then content, with a directory over the top 16 hash bits:
16 B per posting plus each content's own fingerprint list (16 B each) — 3.5-4.0 MB for the 24-56 MB small-file
families (65-170 KB per logical MiB at level 7), 0.8-3.5 MB for the others; two orders of magnitude below the
per-base COPY-anchor tables the encoder builds anyway (16-32 B per base byte). Built once per run.

**Ranking.** A fingerprint with ≤ 16 holders scores every holder by the bytes it stands for (2^level); a
common one scores only its first 16 holders in content order, so shared boilerplate is represented by one
stable small set instead of every holder (B6 walks every holder of a common sketch feature, see Scale). The
24 best by rare score, the 4 best by common score and same-key versions are then measured exactly over every
target fingerprint (a bitset each). Coverage is discounted by how many contents hold the target CDC chunk the
fingerprint lies in: supplying bytes the store already shares frees nothing while their other holders keep
them. Bases covering < max(1 KiB, size/32) are dropped (this removed every false candidate on the compressed
TypeScript tarballs). When more than the encode budget (4) qualify, the 8 best are matched exactly; bases that
would not beat standalone compression are dropped, and the encode order interleaves share-discounted coverage
with the matcher's estimate. Verified plain programs are reused by the encoder at locality 0 (`Match` is
`MatchLocal(0)`, same cached index), so verification adds at most 4 matches per target.

**Ablations** (estimate-level, [ablations.jsonl](results/ablations.jsonl)): sampling level 5-9 changes benefit@4
by ≤ 0.3% on go-files (index 12.0 → 1.1 MiB); posting caps 4/16/64 and shortlist 8/24 are equivalent;
skipping verification keeps benefit but doubles false positives (encodes); FastCDC or plain gear chunk hashes
as features (1 KiB average) are 4× smaller but lose 2-3% benefit on go-files (recall@4 0.75 vs 0.82); CDC v1
chunk hashes find almost nothing for small files (cu-files recall@4 0.009).

**Format-informed fingerprints: measured, not added to discovery.** Raw-byte features already see
member-level reuse inside uncompressed containers (tar members are byte-identical inside archives: vendor and
bundle recall@4 1.0). Compressed containers are the opposite case: between consecutive TypeScript `.tgz`
releases the matcher finds 1-8% of the compressed bytes reusable and the windows estimate 0.1-4.4%, while the
*decoded* tar streams share 99.5-100% (5.6.2 → 5.6.3 decodes to an identical stream; windows estimate
97.6-100%; [chunking.jsonl](results/chunking.jsonl), `ts-gunzip`). Fingerprinting decoded streams would find
these bases, but a byte delta against them cannot be encoded: the only representation that exploits it is a
codec-replay structural representation (B2/B3 gzip replay, available with `-codec-replay`), which the rep
candidate path already builds when allowed. Without such a representation decoded fingerprints yield no
usable candidate, so they belong with B8's canonical representations, not with B7's byte discovery.

## B7-C: complementary multi-base candidates
From the best single, greedy marginal coverage over the bitsets adds the base holding the most
share-discounted target bytes not yet held, while it adds ≥ max(1 KiB, size/100)/2; each prefix of the set
is matched over the *union* index (one `dwDcoreIndex` over all its bases, the locality matcher choosing per
position) and kept only if its estimate beats the previous prefix by max(64 B, 0.5%) plus a base reference.
Kept sets are encoded at every locality like singles (`o.union`; the apply-time re-encode builds the same
union index and must reproduce the planned SHA-256). The graph then prices them exactly: record files, pins a
new base forces (a 4 KiB pin for a CDC base is often what makes a small-file set lose), depth and reads.

On vendor every r1 product shares its vendored `lib/` 9.5 with five peers (deduplicated) and its own `src/`
sixth only with its r2 release; the useful sets pair a release carrying `lib/` 9.7 with the product's other
release. **The first integration made vendor worse** (3.61 MB vs B6's 2.82 MB) although the candidates were
strictly better: the B6 search, given multi-base options, moved into a local optimum that its single-base
options alone avoid (union-single: 2.90 MB; union with multi: 3.59 MB, a strict superset). The fix is an extra
start: Edmonds plus local search restricted to ≤ 1-base options, then local search over all options, keeping
the best of the three starts. With it B7 plans 2.566 MB (−9.1% model, −7.4% allocated); B6's own candidates
plan within 0.01% under the new search on every family. The estimate-optimal oracle candidates (best singles
plus the exhaustive best pair) plan 2.762 MB through the same search: their bases are the peer tarballs whose
shared `lib/` CDC already deduplicates, so B7's share-discounted ranking beats the estimate oracle by 7%.
Without multi-base sets B7's singles plan 2.90 MB (+2.7%), so on vendor the gain is the complementary sets,
not single-base retrieval. Estimated multi-base gain over
B7's best single, as a share of the oracle's single-base saving: vendor 13.4% (the oracle's best pair adds
8.7% over *its* best single), go-files 0.75%, cu-files 0.25%, sqlite 0.1%. Selected multi-base records:
vendor 4 (archive) / 2 (mixed), cu-files 2, sqlite 1.

Windowed targets (> 32 MiB) stay single-base (ZW2 has one base). Only the Coreutils tarballs are windowed
here; for each, a second base adds less than the 1 MiB reporting threshold beyond the first
(`windowed_second_cover` 0), so this corpus justifies no ZW2 multi-base extension.

## B7-D: large-object indexing
B6 indexed bases above 16 MiB one position in 16-32 in memory and windowed bases (> 32 MiB) one anchor per
~240 bytes (`authWParams`: one per KiB, 256 Ki-1 Mi). Envelope bytes (packed) of consecutive versions vs anchor
budget ([density.jsonl](results/density.jsonl), best of locality 0/256):

| base | 256 Ki (B6) | 1 Mi | 2 Mi | 4 Mi | 8 Mi (B7) | 16 Mi | 64 Mi | build s (B6 → B7) |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| Coreutils 9.5 → 9.6 (61 MB) | 3.42 MB | 2.77 | 2.67 | 2.51 | **2.36** | 2.28 | 2.22 | 0.06 → 0.57 |
| Coreutils 9.6 → 9.7 (63 MB) | 1.49 MB | 1.25 | 1.21 | 1.19 | **1.20** | 1.18 | 1.15 | 0.06 → 0.62 |

In-memory bases (Go 15 MB, SQLite 32 MB, tensors 22 MB) are insensitive: 1 Mi → 64 Mi anchors changes their
envelopes by −2.5% to +0.2% (continuation probes already recover what sampling misses) while the table grows
16 → 512 MiB, so they keep the B6 budget. The optimizer now indexes windowed bases with one anchor per 8 bytes,
at most 8 Mi anchors (≤ 128 MiB table; `-anchor-bytes`, `-1` = B6): Coreutils −27% envelope bytes, −8.3%
allocated store. The same density is used when the selected record is re-encoded at apply (planned and
written envelopes are byte-identical, checked by SHA-256). Low-similarity and random inputs cost the same
index build and no extra matching (the table is bounded by the cap, not by the input).

## B7-E: integration with B6
`zeros3 authority optimize` defaults to `-discovery region` (`-discovery sketch -anchor-bytes -1` is B6).
* **Exact accounting is preserved**: on all 48 optimizer lanes the plan equals the model of the materialized
  store byte for byte, and every store deep-verifies after reclamation.
* **Lazy construction.** Delta candidates were already lazy (priced in memory, re-encoded at apply). Structural
  representation candidates were staged for every eligible content and selected in 0 of B6's 64 lanes. They
  are now built with an in-memory sink: the builder's source reads the overlay, so the production proof still
  runs; components the store lacks are priced with the pack writer's rule; nothing is written. A selected
  lazy rep is constructed for real at apply and its root ID must equal the planned one (else the node keeps
  its current record). Planning writes fell 103 → 4.8 MB (Coreutils), 45 → 1.4 (SQLite), 54 → 0.7
  (unrelated), 22.5 → 1.3 (vendor); peak allocated 146 → 48 MB on Coreutils. Transitions now also charge the
  components a rep must still write (they were free when pre-staged).
* **Convergence and determinism**: plans are identical across passes (tested); a re-run after apply plans
  nothing (tested with multi-base records).
* **Memory**: fingerprints are bounded per content; the index is O(Σ min(size/128, 16 Ki)); nothing pairwise
  is materialized (shortlists are per target and freed).
* **No persistent index.** Fingerprint extraction plus index build is 0.04-1.3 s per run (2-9% of planning
  wall time; 16% on the 5 s unrelated run, 0.15 s of a 0.3 s random run), dominated by the content reads
  the planner performs anyway, so a persisted index would save little offline and would add a derived
  format with its own invalidation. Features are a pure function of content bytes and `rsParams`, per
  content, so an online design can add or drop one content's postings
  (sorted runs merged lazily) without a rebuild; not implemented. Index state is never an authority: a wrong
  or missing fingerprint can only cost a candidate.

## B7-F: chunk boundaries as similarity features
Bytes of version i+1 inside chunks identical to version i's, at each granularity ([chunking.jsonl](results/chunking.jsonl)):

| pair | matcher copies | CDC v1 (64 KiB) | gear 1 KiB | FastCDC 1 KiB | B7 windows (estimate) |
|---|---:|---:|---:|---:|---:|
| Coreutils 9.5 → 9.6 / 9.6 → 9.7 | 0.968 / 0.980 | 0.015 / 0.052 | 0.431 / 0.705 | 0.441 / 0.728 | 0.902 / 0.950 |
| Go 1.22.0 → 1.22.6 / → 1.23.0 | 0.999 / 0.974 | 0.253 / 0.172 | 0.914 / 0.800 | 0.932 / 0.818 | 0.994 / 0.927 |
| SQLite (3 pairs) | 0.988 | 0.16-0.18 | 0.925-0.931 | 0.935-0.940 | 0.985-0.986 |
| git packs | 0.727 / 0.934 | 0.216 / 0.674 | 0.647 / 0.902 | 0.610 / 0.896 | 0.722 / 0.930 |
| tensors (retrain+perturb / shift) | 0.904 / 0.977 | 0.815 / 0.954 | 0.833 / 0.976 | 0.833 / 0.976 | 0.899 / 0.976 |

FastCDC normalization tightens sizes (CV 0.30-0.37 vs 0.80-0.91) and adds 1-2 points of stability over plain
gear; neither approaches the sampled windows, whose estimate stays within 1-7 points of what the matcher
actually copies, because a window needs only 32 unchanged bytes while a chunk needs its whole span and both
boundaries. Chunk features are cheaper to index (one per KiB vs one per 128 bytes) but retrieved worse
(B7-B ablations). CDC v1 storage semantics and chunk identities are unchanged; B7 uses no chunker.

## B7-G: secondary
**Small-record grouping: measured, not implemented.** Pricing record files at their exact bytes (model only,
`recordBlock=1`) changes the plan on small files dramatically — cu-files 4.05 → 2.41 MB (−41%, 78 → 519
deltas), go-files 3.91 → 3.04 MB (−22%) — and nowhere else by more than 1.3% (vendor). This is an upper
bound for grouping records into shared files. Grouping needs a new persistent record container (append,
tombstones, compaction, torn-tail recovery, an older-reader feature gate) behind all 17 record-file call
sites, including B4's online epoch reclamation of retired record files, and its own process-kill matrix; that
is disproportionate inside B7 and is the strongest candidate for the next storage milestone.
**Incremental replanning: not attempted.**

## Materialized stores (allocated bytes after optimize + gc + repack + compact)
| family | cdc | b6 archive | b7 archive | b7/b6 | b6 mixed | b7 mixed | b7/b6 | verify |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| cu-files | 6.07 | 5.27 | 5.26 | 0.998 | 5.46 | 5.46 | 1.000 | True |
| go-files | 5.94 | 5.42 | 5.42 | 1.001 | 5.56 | 5.57 | 1.003 | True |
| vendor | 4.50 | 2.77 | 2.57 | 0.926 | 2.90 | 2.72 | 0.938 | True |
| bundle | 3.49 | 2.35 | 2.35 | 1.000 | 2.35 | 2.35 | 1.000 | True |
| coreutils | 45.04 | 19.82 | 18.17 | 0.917 | 20.10 | 18.25 | 0.908 | True |
| go | 9.60 | 3.18 | 3.18 | 1.000 | 3.18 | 3.18 | 1.000 | True |
| sqlite | 94.06 | 27.17 | 27.16 | 1.000 | 27.41 | 27.41 | 1.000 | True |
| tensors | 27.23 | 24.80 | 24.80 | 1.000 | 24.80 | 24.80 | 1.000 | True |
| git | 2.74 | 1.62 | 1.62 | 1.000 | 1.62 | 1.62 | 1.000 | True |
| ts | 12.63 | 12.63 | 12.63 | 1.000 | 12.63 | 12.63 | 1.000 | True |
| random | 50.45 | 50.45 | 50.45 | 1.000 | 50.45 | 50.45 | 1.000 | True |
| unrelated | 67.51 | 67.51 | 67.51 | 1.000 | 67.51 | 67.51 | 1.000 | True |

All 48 optimizer stores deep-verify and equal their plan in the model exactly. Ratios are deterministic (bytes,
not timings). The Coreutils gain is the anchor density alone (model level: B6 discovery with the B7 encoder
parameters plans the same 18.65 MB as B7); vendor's is the complementary sets plus the restricted start.

Migration cost (wall / CPU s, planning writes, peak RSS; B6 → B7, archive): Coreutils 23.4/38.3 s, 103 MB, 635 MiB →
31.5/50.5 s, 4.8 MB, 917 MiB (dense windowed index, in-memory rep components); vendor 14.5/15.4 s → 24.5/25.6 s
(9.5 s of matcher verification of 7 MB tarballs); cu-files 11.4 → 8.2 s and go-files 12.3 → 7.6 s (6,336 and
1,926 encodes instead of 10,165 and 5,510: the coverage floor and verification drop useless bases); other
families within ±0.8 s. Negative controls: no candidate gained; the only extra cost is fingerprint extraction
(+0.1-0.3 s per family, unrelated 133 MB: 0.6 → 0.8 s).

## Reads
Paired cold reads (geometric mean of per-read wall ratios B7/B6 on the same contents and offsets; n pairs).
Session 1: Coreutils, vendor and Go (identical stores: the noise control) at 12 4 KiB / 6 64 KiB / 2 whole
reads per content; session 2, independent and alone on the machine: Coreutils at twice the samples, vendor
again ([reads.jsonl.gz](results/reads.jsonl.gz), [reads2.jsonl.gz](results/reads2.jsonl.gz)).

| family, profile | cold 4 KiB s1 / s2 | cold 64 KiB s1 / s2 | whole s1 / s2 | 64 KiB device reads (median, B6 → B7) |
|---|---|---|---|---|
| vendor, archive | 0.95 / 0.97 (144 each) | **0.87 / 0.88** (72) | **0.89 / 0.89** (12) | 8 → 7 / 7 → 7 |
| vendor, mixed | 1.01 / 0.96 | 1.08 / 1.09 | 0.99 / 1.00 | 6 → 6 |
| Coreutils, archive | 1.03 / 1.03 (36 / 72) | 0.98 / 1.02 (18 / 36) | 1.15 / 0.96 (3) | 7 → 10 / 7 → 9 |
| Coreutils, mixed | 1.03 / 1.02 | 1.31 / 1.09 | 0.84 / 1.07 | 8 → 9 / 8 → 8 |
| Go (identical stores) | 0.96 / – | 1.01 / – | 0.89 / – | 7 → 7 |

B6 measured a ±15% sparse noise band on identical stores; the Go control here is within 4% sparse and 11% whole.
Vendor's smaller graph is also faster to read under the archive profile (fewer, more local base pieces).
Coreutils' sparse reads are unchanged within noise, but its denser-anchor envelopes have more, shorter COPYs:
64 KiB reads touch more records (median device reads 7 → 9-10) and decode more (4 KiB: 184-202 → 209-228 KB).

## Concurrent readers (8 closed-loop, 75% 4 KiB / 25% 64 KiB, cold start; both lane orders)
| family | lane | order | reads/s | p50 µs | p95 µs | p99 µs | CPU s |
|---|---|---|---:|---:|---:|---:|---:|
| vendor | b6-archive | b6-archive first | 9,768 | 177 | 3933 | 9494 | 33.5 |
| vendor | b7-archive | b6-archive first | 11,645 | 177 | 3349 | 7022 | 33.3 |
| vendor | b7-archive | b7-archive first | 11,882 | 173 | 3255 | 6975 | 33.2 |
| vendor | b6-archive | b7-archive first | 10,163 | 171 | 3796 | 9199 | 33.6 |
| coreutils | b6-archive | b6-archive first | 4,842 | 632 | 9305 | 21505 | 37.4 |
| coreutils | b7-archive | b6-archive first | 4,429 | 656 | 10084 | 23823 | 37.9 |
| coreutils | b7-archive | b7-archive first | 4,380 | 663 | 10332 | 24161 | 38.0 |
| coreutils | b6-archive | b7-archive first | 4,869 | 633 | 9450 | 21560 | 37.8 |

Under 8 concurrent readers that extra work shows: Coreutils B7 serves 9% fewer reads/s (p99 21.5 → 24 ms),
consistently in both lane orders; vendor B7 serves 17% more (p99 9.3 → 7.0 ms). The mixed profile accepted the
Coreutils graph: B6's read model (fitted to B6's sparser envelopes) does not price the extra pieces enough to
prefer a sparser variant; `-anchor-bytes 1024` restores B6's envelopes where read throughput matters more.

## Scale
Retrieval alone on synthetic histories (no store): N small files sharing a 6 KiB boilerplate header (30% of
each file), three versions each; recall = the previous version in the top 4 ([scale.jsonl](results/scale.jsonl)).

| contents | sketch build s | sketch rank s | sketch recall@4 | region build s | region rank s | region recall@4 | region index MiB |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1500 | 0.14 | 0.26 | 1.000 | 0.21 | 0.06 | 1.000 | 8.4 |
| 6000 | 0.64 | 5.37 | 1.000 | 0.79 | 0.32 | 1.000 | 33.1 |
| 24000 | 2.66 | 114.56 | 1.000 | 3.06 | 1.49 | 1.000 | 132.0 |

B6's scoring walks every holder of every sketch feature, and boilerplate features are held by every content:
quadratic (×21 per ×4 contents). B7 caps the holders a common fingerprint scores, so ranking stays near
linear (×5 per ×4) and is 77× faster at 24,000 contents with the same recall. The first B7 version also grew
superlinearly (24.8 s at 24,000): finding the end of a posting run was a linear scan, O(N) for a fingerprint
every content holds; it is a binary search now ([scale-linear-scan.jsonl](results/scale-linear-scan.jsonl)).
The index is ~5.6 KiB per 15 KB content here (level 7) and is built once per run.

## Correctness evidence
`zeros3_b7_test.go`: fingerprints equal a sequential reference across parallel slices and the streaming scan
(which also reproduces the B6 sketch exactly), deterministic, levels nested; dense sampling raises the level
instead of growing (bounded), a zero run is one fingerprint; postings and measured coverage equal brute force
over 40 contents; a mosaic target (lib part of A, shifted src part of B, 30 boilerplate distractors, 5
near-copies of A) ranks A first and greedy pairs it with B; through `optimize` only region discovery plans
the 2-base delta, it applies exactly, survives reclamation and reopen, deep-verifies and a re-run plans
nothing; random contents get no candidates or encodes; candidate sets are identical across passes; union
programs over 2-4 bases with shifted/reordered/repeated regions reconstruct exactly at every locality from
the bases their envelope names; lazy representation candidates write no file while planning, price exactly
as staged ones, and forced into a plan are constructed, proven and published (9 kinds), with one planned
root corrupted (failure injection): that node fails and keeps its record, the rest publish, reads stay exact
after reclamation, deep verify passes. Campaign: all 60 lanes deep-verify after reclamation, 48 plans equal
their materialized model, 3,072 cold/warm/whole reads byte-exact.

Executed: `go test -race -run 'TestB7|TestB6'` (265 s, pass); `-run '^(TestAuth|TestAuthority|TestRep|TestB3|
TestB4|TestB5|TestB6|TestB7|TestPack|TestRepack|TestCompact|TestLocality|TestXDelta|TestP8|TestCoalesc)'`
(462 s, pass); the root suite once (`go test .`): pass (939 s). `toc_check` passes on the regenerated source
map. Not run: T1 qualification, fuzzing, process-kill cuts (no new durable state: the lazy-rep apply path
stages through the existing builder and publishes through `authPublish`, whose crash cuts B4/B5 cover),
external harnesses. Untested: a persistent B7 index (none exists: fingerprints are rebuilt per run, so
stale, missing or corrupt index states cannot arise; the B3 ZSR1 index is unchanged).

## Limits and risks
* Only two families gain; on this corpus single-base retrieval was already near its oracle. Corpora with many
  partially related large archives (vendored code at scale) are where region discovery should matter more;
  they were not available beyond the synthetic-but-real `vendor` construction.
* The vendor gain depends on the search's restricted start as much as on the candidates; other multi-option
  degradations of the B6 local search may exist beyond the families measured.
* Dense windowed anchors raise optimizer RSS by up to ~280 MiB on 60 MB bases (cap 8 Mi anchors).
* Share discounting uses CDC chunk holder counts of the current store; after a plan changes many records the
  next run sees different shares (it converges: re-runs plan nothing).
* Verification costs grow with target size × shortlist (vendor: 9.5 s); a coverage-only mode exists (`Verify 0`).

## B8 implications
B8's cross-format canonical representations should be priced through the same lazy in-memory construction
(no staging) and can feed format-informed fingerprints into the same posting index (positions are generic):
the decoded TypeScript streams show 97.6-100% reuse that no byte feature sees (0.1-4.4%), the clearest
discovery opportunity this corpus offers, and it is only usable through a canonical decoded representation.
Before more discovery, the record-allocation floor should go: grouped records would let the 858 useful
small-file deltas the oracle found actually pay. Multi-base candidates showed the optimizer's local search is
sensitive to option richness; richer canonical candidates will need the restricted-start discipline (or
exhaustive search of small partitions) to keep their value.

## Reproduce
```sh
bash testing-harnesses/next/b7/run.sh WORK     # needs root (drop_caches) and ~4 GB
zeros3 authority optimize -store DIR [-apply] [-profile archive|mixed|hot] [-discovery region|sketch] [-anchor-bytes 8]
```
