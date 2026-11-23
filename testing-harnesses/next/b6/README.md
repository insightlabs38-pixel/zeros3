# B6: global representation-graph optimization

Linux amd64, Go 1.27.0, 4 vCPU Firecracker VM, virtio SSD. Base `f65976d` (B5). Cold reads drop the page
cache and every in-process cache before each read; "device reads" are `/sys/block/*/stat` requests (whole,
otherwise idle machine). Every read is checked byte for byte. Raw rows: [results/](results/) (`tables.py
results/{lanes,reads,concurrent,scale}*` prints the tables, [results/tables.md](results/tables.md) is its output;
`fit.py results/model-features.jsonl.gz` the model fit); inputs:
[corpus.sha256](corpus.sha256) (`make_inputs.py`); reproduction: [run.sh](run.sh).

**Verdict: `B6_GLOBAL_OPT_EFFECTIVE`.** At the existing planner's own archive policy, the global optimizer
stores Coreutils, Go and per-file Coreutils histories in 11-25% fewer bytes *and* reads them faster (paired
cold 4 KiB 0.54-0.91x, 64 KiB 0.58-0.87x, whole 0.54-0.85x over two independent sessions; 1.3-4.8x the
concurrent throughput), SQLite at -1.3% with ~0.92x reads, and finds 31-33% savings the existing planner
refuses or misses on git packs and vendored bundles (at a real read cost there: 1.1-1.7x). Two negative
controls are left untouched. All 64 materialized optimizer plans landed on the byte they predicted. The
gains come from three separable changes, measured apart below: exact closure accounting (record-file blocks,
shared chunks), the joint search, and locality-aware COPY matching, which turned out to shrink envelopes as
well as localize them.

## Starting point: what was local
`AuthorityMigrate` (section 16e) walks contents once in version order; a content may only use bases already
decided, keeps its decision, and is admitted against the bytes of chunks it alone references (`refs==1`).
Objects above 32 MiB try only the top sketch candidate. Record files are priced at their byte length,
not their allocated blocks. Read gates are per-object measurements (4 KiB amplification, whole-read ratio).
Consequences measured here: the per-file Coreutils history (865 contents, 4-280 KB) gets 425 deltas and 358
pins whose 4 KiB record files cost 3.2 MB to save 2.4 MB of chunks (store **+13% over CDC**, cold 4 KiB
reads 1.72x CDC); shared sections are never freed by the first holder; the production gates refuse every
Coreutils and Go delta (warm whole-read ratio 0.07-0.37 < 0.5) while a different graph of the same history
reads at 1.5-1.8x CDC cold.

## Architecture (`zeros3.go` section 16e1, `zeros3 authority optimize`)
**Graph.** Nodes are distinct in-scope contents, plus records only other records name (pure intermediates,
reached by closing over bases) and out-of-scope bases (fixed). Each node has options: CDC (its manifest
chunks; a pin record is charged only while something depends on it), its committed record (`keep`), deltas
(single-base per candidate, plain and locality-aware programs, a 2-base refinement; in-memory `xd` up to
32 MiB, windowed ZW2 above) and structural representations built by the existing group planner. Components
(CAS chunks, envelope pieces, representation components) are interned with the pack writer's exact price
(`physChunk`, new pieces through the same compressor) plus the record header and index entry; record files
cost their allocated filesystem blocks (statfs). Out-of-scope roots add fixed references; records outside
the model that name a node keep it required below their depth.

**Closure evaluator.** A node is required while live or named by a required node's selection; a component is
paid while any required selection (or anything outside) references it. `set(v, o)` updates requirement
counts (adding or removing whole sub-closures when a base stops being needed), component reference counts
and record-file charges incrementally, O(changed components); every quantity is a function of the selection,
so a trial move is undone by setting the old option. Read costs are memoized per node and invalidated for
the node and everything built on it. Tested against from-scratch evaluation and an independent union after
400 random moves, and against 88 materialized stores: the model of each store equals its measured retained
bytes exactly (allocated bytes add journal, manifests and directories), and each of the 64 optimizer plans
equals the model of the store it produced ([tables](results/tables.md)).

**Read model.** A read of node v over [a,b) touches its own units (CDC/pin chunks, or the envelope header,
frame table and the pieces of the frames/segments covering the range) and recurses through every COPY span
into the base's *current selection*. Cost = fixed + per resolved body record + per record fetched + per
inflated byte + per raw byte (+ a reconstruction term per depth level); restartable 32 KiB blocks are
modeled when the store has `packblk`. The sparse cost averages 8 probes of 4 KiB and 4 of 64 KiB per content;
whole reads have their own coefficients. Coefficients were fitted to 2,160 cold sparse and 282 cold whole
reads of the baseline stores (`fit.py`): 164 µs/read, 209 µs/body record, 128 µs/record, 6.4 ns/inflated
byte, 1.8 ns/raw byte (R² 0.72); whole 340 µs/record, 1.8 ns/byte, 6.4 ns/byte/level (R² 0.945). For
in-memory deltas the model's records and decoded bytes equal the traced ones exactly; on all 7,200 cold
reads of every lane the model's mean is 968 vs 929 µs (4 KiB) and 2,197 vs 1,961 µs (64 KiB), correlation
0.71/0.86. It overstates delta/CDC ratios ~10% (geometric mean, worst 2x on windowed 64 KiB reads): base
pieces are fetched by B5's prefetch in parallel, which no linear fit captured (own/COPY-split and
parallel-factor fits were tried and rejected: R² unchanged, coefficients ill-conditioned).

**Objective and policies.** J = retained bytes + w_s·Σ sparse µs + w_w·Σ whole µs + 0.1·bytes written, with
optional per-content budgets (sparse, whole) relative to the content's CDC read, depth ≤ `-max-depth`, the
newest version of each multi-version key kept CDC (the B2 default), and a partition rewritten only for a
gain ≥ 1% of its stored bytes (convergence, churn). Profiles: `archive` (bytes only), `mixed` (w_s 20 B/µs,
w_w 1 B/µs), `hot` (w_s 400, w_w 10, sparse ≤ 2.5x and whole ≤ 2x CDC, depth 1). `-pareto` reports a sweep.

**Search.** Union-find partitions over shared components and base relations (dependencies across partitions
cannot exist by construction). Per partition: (1) a Chu-Liu/Edmonds minimum arborescence over single-base
options with shared chunks split among holders, depth-repaired greedily (start only); (2) best-improvement
single-node sweeps with exact deltas; (3) *cascade*: an option infeasible because its base is too deep, with
the base's best shallower option; (4) *lift*: an option infeasible because of what is built on it, repairing
each broken dependent outward with its cheapest feasible option; (5) *joint moves*: the holders of a shared
component switched together, keeping the best prefix; the better of the arborescence and current starts is
kept. Bounded by 400k evaluations per partition; an exhaustive oracle enumerates partitions up to 2^20
combinations. Costs: candidates O(N·K·L) encodes (K ≤ 4 bases, 2 for windowed; L locality variants), one
sketch read per content, partitioning linear in option components, Edmonds O(V·E) per partition, search
O(rounds · Σ options) evaluations each O(changed closure + probes of the invalidated subtree).

**Transitions.** Changed nodes are published through `authPublish` (closure check, generation, atomic rename)
after `authProveContext` reconstructs each record from the durable store and compares its SHA-256. A record
may declare more depth than its bases need, so each is written at its planned depth and published once (a)
its new bases have records shallower than it and (b) every current record built on it is deeper than its
new depth; a blocking dependent moves first, and only when nothing is ready is it bridged to a pin
(materialized CDC). CDC targets materialize their chunks before their record goes; pins nothing needs are
retired at the end; everything else unreferenced is ordinary gc/B4 garbage (no new ownership system, no new
persistent format: the existing record kinds and features). Windowed envelopes are re-encoded at apply and
refused if they differ from the planned bytes. Cancellation or a crash leaves a valid published graph; a
rerun plans from it and converges (no changes on a second run in every test and lane checked).

**Locality-aware COPY matching** (`dwDcoreIndex.MatchLocal`, `dwDcoreWParams.Locality`): at every position the
matcher first verifies the base offsets that continue the previous COPY (aligned and resumed), takes a near
match unless an indexed one is `slack` bytes longer, and drops far matches shorter than `slack`; slack 0 is
the old matcher exactly (tested). Any program decodes with the unchanged decoders; nothing in any format changed.

## Storage (allocated bytes after gc + repack + compact; vs the existing planner at the same order)
| family (logical) | CDC | existing planner, archive | global, archive {0,32,256} | ratio | existing, production gates | global, mixed |
|---|---:|---:|---:|---:|---:|---:|
| Coreutils 9.5-9.7 tars (187 MB) | 45.04 MB | 22.38 MB | **19.82 MB** | 0.886 | 45.04 MB (refused) | 20.10 MB |
| Go `cmd/compile` 1.22.0-1.23.4 (60 MB) | 9.60 | 3.60 | **3.18** | 0.884 | 9.59 (refused) | 3.18 |
| Coreutils per-file, 865 contents (24 MB) | 6.07 | 6.99 | **5.27**¹ | 0.754 | 6.85 | 5.46¹ |
| SQLite 4 states (128 MB) | 94.06 | 27.51 | **27.17**¹ | 0.987 | 27.25 | 27.42¹ |
| vendored bundles a/b/c (24 MB) | 3.49 | 3.49 (refused) | **2.35** | 0.674 | 3.49 | 2.35 |
| git packs h0-h2 (4.1 MB) | 2.74 | 2.36 | **1.62** | 0.689 | 1.62 | 1.62 |
| TypeScript tgz (12.6 MB), random (50 MB) | 12.63 / 50.45 | = CDC | = CDC | 1.000 | = CDC | = CDC |

¹ lanes with locality {0,256}; the {0,32,256} confirmation ran on Coreutils, Go, bundle and git only.
Re-optimizing the existing planner's stores: per-file Coreutils 6.85 → 5.39 MB (405 transitions, removing
unjustified deltas and pins safely), Coreutils archive 22.38 → 20.27 MB, Go 3.60 → 3.22 MB, git 2.36 → 1.62 MB.

## Reads (paired geometric-mean wall ratios; n = 12 cold 4 KiB, 6 cold 64 KiB, 2 whole per content, ≤24 contents)
Session 1: all 10 lanes of all 8 families (8,610 reads); session 2: CDC, existing archive, global {0,256}
and {0,32,256} lanes of Coreutils, Go, bundle and git. Global = archive profile, same order as the existing planner.

| family | global vs existing archive: 4 KiB / 64 KiB / whole (session 1; session 2 {0,32,256}) | p95 4 KiB µs (existing → global) | global vs CDC 4 KiB / 64 KiB |
|---|---|---|---|
| Coreutils | **0.72 / 0.66 / 0.79; 0.80 / 0.64 / 0.85** | 10,294 → 3,435; 6,993 → 3,349 | 1.64 / 1.49; 1.67 / 1.68 |
| Go | 0.91 / **0.69** / 0.79; 0.88 / **0.58** / 0.84 | 8,828 → 3,781; 6,948 → 2,625 | 1.88 / 1.91; 1.76 / 1.60 |
| per-file Coreutils | **0.54** / 0.87 / **0.54** | 1,486 → 1,256 | 1.11 / 1.48 |
| SQLite | 1.01 / 0.91 / 0.92 | 2,773 → 3,022 | 1.61 / 1.41 |
| bundle | 1.45 / 1.70 / 1.15; 1.36 / 1.32 / 1.35 (existing keeps CDC) | 2,011 → 3,742; 1,897 → 3,172 | 1.42 / 1.72; 1.35 / 1.37 |
| git | 1.17 / 1.28 / 1.40; 1.58 / 1.33 / 1.10 (depth 2 vs 1) | 2,507 → 1,906; 1,294 → 2,605 | 2.08 / 2.09 |
| TypeScript / random (identical stores) | 0.98 / 0.94 / 0.89; 1.13 / 1.01 / 0.90 | | noise band ±15% sparse, ±40% whole |

Mixed profile (session 1, {0,256}) vs the existing production planner: per-file Coreutils 0.80x bytes at 0.59/0.70/0.54x reads,
SQLite 1.006x bytes at 0.90/0.92/0.92x, git 1.003x bytes at 0.84/0.87/0.94x; for Coreutils and Go, which the
gates leave CDC, mixed stores 0.45x/0.33x the bytes at 1.56/1.54x and 1.70/1.60x CDC sparse reads (whole
2.30x/1.77x). Physical work per cold 4 KiB read (medians, existing archive → global archive): Coreutils 9 → 6
device reads, 262 → 191-206 KB decoded; Go 7 → 5, 248 → 209-232 KB; per-file 4 → 1 device reads. Warm repeated
4 KiB reads (one per content) differ 0.3-3x between lanes in a 0.2-2.5 ms band, dominated by cache admission;
not attributed.

**Concurrent readers** (8 closed-loop, 75% 4 KiB / 25% 64 KiB, cold start, both lane orders,
[rows](results/concurrent.jsonl)): reads/s existing archive → global archive: Coreutils 2,946-3,086 →
4,834-5,005 (p99 33 → 21-22 ms; CDC 4,462-4,523); Go 13,061-13,138 → 16,667-17,396; SQLite 13,818-13,892 →
14,910-15,463; per-file Coreutils 10,192-10,442 → 45,044-49,191 (mixed: 218,732-227,651; CDC 740k, all
warm). CPU saturated (≈3.6-3.9 cores) in every run.

## Which change did it (same corpus, same reads)
| change | Coreutils | Go | per-file | bundle | SQLite |
|---|---|---|---|---|---|
| locality matcher: global {0,256} vs global {0} (mixed), bytes / 4 KiB / 64 KiB | 0.911 / 0.86 / 0.82 | 1.00 / 0.88 / 0.69 | 1.00 / 0.99 / 0.92 | 1.016 / 0.92 / 0.92 | 1.00 / 1.02 / 0.94 |
| global search vs the sequential rule on identical candidates (`seq-loc`), bytes / 4 KiB / 64 KiB | 1.014 / 0.91 / 0.99 | 0.906 / 0.86 / 0.67 | 1.027 / 0.91 / 0.74 | 1.016 / 0.94 / 0.86 | 1.006 / 0.96 / 0.97 |
| slack 32 added: {0,32,256} vs {0,256} (archive, session 2), bytes / 4 KiB / 64 KiB | 0.984 / 0.95 / 1.12 | 0.997 / 0.98 / 0.84 | | 0.993 / 0.95 / 0.79 | |

Exact accounting alone: the sequential rule itself, re-run with record-file blocks and shared closures priced
(`seq-loc`, production order), stores the per-file history in 5.32 MB instead of 6.85 MB (0.777x) and admits
the bundle the existing planner refuses (0.676x): most of the per-file gain is this correction.

Model-level, identical candidates (objective; [lanes](results/lanes.jsonl.gz) `report.local/planned/oracle/ablations`):
on the 38 global-search lanes where every partition was enumerated exhaustively (≤ 2^20 combinations) the
search's largest gap to the oracle is 0.04% (SQLite archive); the sequential rule on the same candidates is
12.1% from the oracle on Go, 0.29% on SQLite and optimal on Coreutils, git and bundle. Single-node moves alone
reach the global result on fresh Coreutils, Go, SQLite and bundle graphs; cascade/lift/joint moves add 0.05-0.2%
on per-file Coreutils and 0.25% on git archive, **10.6% when re-optimizing the existing planner's Go graph**
(its kept depth-2 records block every single move; lift republishes them), and are what the shared-section
(`TestB6JointMoveOnly`) and raised-base (`TestB6RebaseRaisesBase`) cases need. The Edmonds start alone is
0-84% worse than the search.

**Locality sweep** (every candidate pair, single base, [rows](results/locality.jsonl)): envelope bytes /
modeled 4 KiB / decoded bytes per 4 KiB / base scatter (copied bytes outside the probe's 256 KiB cluster,
B5's metric) relative to the plain matcher:

| slack | Coreutils | Go | bundle | SQLite | git |
|---:|---|---|---|---|---|
| 32 | **0.655** / 0.71 / 0.64 / 0.21→0.06 | 0.84 / 0.87 / 0.83 / 0.07→0.015 | 0.92 / 0.55 / 0.50 / 0.20→0.04 | 1.00 / 1.00 / 1.00 | 1.005 / 0.98 / 0.98 |
| 256 | 0.71 / 0.62 / 0.53 / 0.02 | 0.88 / 0.86 / 0.82 / 0.003 | 1.09 / 0.44 / 0.38 | 1.00 / 0.99 / 0.98 | 1.41 / 0.98 / 0.97 |
| 1024 | 0.89 / 0.59 / 0.49 / 0.006 | 0.94 / 0.85 / 0.81 | 1.45 / 0.37 / 0.30 | 1.01 / 0.99 / 0.98 | 3.1 |
| 4096 | 1.47 / 0.56 / 0.47 / 0.001 | 1.37 / 0.85 / 0.82 | 3.1 / 0.41 / 0.37 | 1.01 / 0.98 / 0.96 | 17 |

The B5 diagnosis holds and is fixable in the encoder: Coreutils' scatter falls from 21% to 2% of copied bytes.
The unexpected part is the size: bases above ~16 MB index one position in 16, and continuation probes recover
the matches between anchors. Large slacks drop useful short far matches (git packs, whose objects are
compressed, lose most). The optimizer chooses per pair among {0, 32, 256}.

## Migration and planning cost
Coreutils (windowed): global archive 26-29 s wall / 41 CPU-s against the existing planner's 110 s / 257 CPU-s
(its admission proof measures whole-read timing); 12 encodes, search 0.04 s; peak RSS 526-588 MiB (128 MiB
windowed sources are held in memory below `rgMemSource`; the streamed path re-inflated chunks per page and
ran 7 CPU-minutes before being stopped, against 29 CPU-s). Go/SQLite 5-13 s (the planner 4-8 s). Per-file
Coreutils: archive 22 s (6,805 encodes, search 0.27 s); mixed 42-54 s, of which the read-priced search is 31-46 s (55k evaluations of read costs
on a 783-node partition) — the most expensive configuration measured. Bytes written by a run are dominated
by staging representation candidates the group planner builds (Coreutils ~100 MB, garbage afterwards; peak
allocated 146 MB); envelopes themselves are 1-7 MB. Bridges: 0 in every lane (dependents always moved first);
the forced-bridge path is tested. Scale ([rows](results/scale.jsonl), synthetic 4-version histories of
40-90 KB with shared headers): 200 / 800 / 3,200 contents plan in 1.2 / 4.4 / 16.3 s (search 0.1 / 0.5 /
1.9 s, 220k evaluations), peak RSS 155 / 254 / 557 MiB, linear in contents; Edmonds was rewritten to
contract every cycle per level after its single-cycle version allocated 1.7 GB on a 3,195-node partition.

## Correctness evidence
`zeros3_b6_test.go`: incremental closure vs scratch and independent union;
Edmonds vs brute force (600 random graphs); the joint-only case (no single move pays, the joint one does);
the shared-section case against the existing planner on an identical store (smaller, exact, converges);
version history with random controls under archive and hot (exact after reclaim and reopen, deep verify,
rerun empty, controls untouched); a pinned base raised under its dependents (each intermediate graph checked
by reachability before every publication, no bridge); the forced-bridge path; three cancellation cuts
before the k-th publication (reopen, deep verify, exact, rerun completes and converges); no-benefit input
(no change); deterministic plans; oracle gap ≤ 0.5% at four read weights; locality programs exact in both
codecs, `MatchLocal(0)` identical to `Match`; a base that loses its last live root survives B4 online passes
and a blocked reconstruction-ordered B5 repack, then re-optimization models it as a required intermediate;
prefix-scoped runs leave out-of-scope records untouched. Campaign: all 88 lanes deep-verify after
reclamation; 8,610 + 1,638 cold/warm/whole reads byte-exact.

Executed: `go test -race -run TestB6` (138 s, pass); `-run '^(TestAuth|TestAuthority|TestRep|TestB4|TestB5|
TestB6|TestPack|TestRepack|TestCompact|TestLocality|TestXDelta|TestP8|TestCoalesc)'` (415 s, pass); the root
suite once (`go test .`, 876 s): one failure, `TestB4ConcurrentWorkloadDuringPasses` ("the passes reclaimed
nothing"), a pre-existing scheduling flake: with three busy loops it failed 9/30 at `f65976d` and 12/30 here.
Its 32 passes can all finish before any overwrite has made data dead; they are now paced behind the writers
(assertion unchanged): 0/30 under the same load, 30/30 unloaded, 2/2 under `-race`. Not run: T1
qualification, fuzzing, process-kill cuts of optimize (cancellation cuts only; publication reuses
`authPublish`, whose crash cuts B4/B5 cover), external harnesses.

## Limits and risks
* The model is calibrated on one machine class (virtio SSD, unblocked records). Its ratio bias (~10%,
  worst 2x on windowed 64 KiB reads) makes budgets conservative; `hot` keeps CDC on every large family.
* The frontier is narrow on these corpora: once whole reads are priced, read weights 0-400 select at most two
  graphs per family; depth-1 vs depth-2 is the main lever. Workload weights are operating points, not derived.
* Read-priced search grows with partition size (per-file Coreutils: 31-46 s); incremental operation should
  plan only partitions touched since the last run (the partition is the unit; not implemented).
* Offline, exclusive-lock command only (like `migrate`); B4 online passes coexist with its output, not its run.
* Representation candidates are staged even when not chosen (write amplification, then gc).
* Orphan records are treated as outside dependents until gc removes them (conservative; run gc first).

## B7 implications
With the oracle gap at ≤0.04%, candidate generation, not the choice among candidates, now bounds the result:
the search only sees the sketch top-4 (top-2 above 32 MiB) single-base programs and one 2-base refinement.
Cross-family sharing (vendored code across bundles) and the small-file regime need similarity over
*components* (which chunks or regions a base would supply) rather than whole-object sketches, and multi-base
candidates chosen for complementary coverage. The locality matcher's continuation probes show the anchor index's sampling loses real matches above
16 MB; B7 should index denser or per region where a base serves many targets. Record-file allocation makes
small-content deltas uneconomic below ~4-8 KB of savings: grouping small records (one file for a family's
records) would move that threshold and is a format change worth B7's attention.

## Reproduce
```sh
bash testing-harnesses/next/b6/run.sh WORK     # needs root (drop_caches) and ~3 GB
zeros3 authority optimize -store DIR [-apply] [-profile archive|mixed|hot] [-pareto 0,20,400] [-locality 0,32,256]
```
