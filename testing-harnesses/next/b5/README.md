# B5: delta-locality-aware pack organization

Linux amd64, Go 1.27.0, 4 vCPU Firecracker VM, virtio disk (SSD-backed, ~100-200 µs per cold read request).
Base `b169eda` (B4). Cold reads drop the page cache and every in-process cache before each read
(`drop_caches`); device counters are `/sys/block/*/stat` (whole machine, otherwise idle), so "device reads"
are real storage requests, "records" are issued pack-record reads and "decoded" is the logical bytes those
records were inflated and hashed to. Every read is checked byte for byte. Raw rows: [results/](results/),
tables: `tables.py results`, inputs: [corpus.sha256](corpus.sha256), reproduction: [run.sh](run.sh).

**Verdict: `B5_SPECIALIZED`.** The effective physical organization is *inside* records, not between them:
restartable records plus dependency prefetch cut cold sparse reads 13-33% (geometric mean of paired ratios)
on every family whose chunks compress — Coreutils deltas and CDC 19-33%, SQLite 13-21% — at +0.6-3.3% storage,
and are neutral on already-compressed and random data. The P8 inflation cost was a cache defect (migration −40%). Reconstruction-
ordered *placement* is implemented and integrated but gave no latency win on this storage; it stays an
estimator-gated research mode. Neither is a default.

## B5-A: what a cold reconstruction actually reads
Trace of the production GET path (`packTrace.OnRecord`: pack, offset, stored/logical size, pread and decode
time per record; [rows](results/trace-b5a-baseline.jsonl.gz), 16 offsets). Coreutils 9.5→9.6→9.7, ZW2
windowed deltas (B1 `zw-mixed`), archive order, depth 2:

| cold 4 KiB of 9.7 (depth 2) | records | stored | decoded | device reads | wall | pread | decode |
|---|---:|---:|---:|---:|---:|---:|---:|
| all | 11.0 | 223 KB | 611 KB | 14.5 | 6.1 ms | 1.3 ms | 3.2 ms |
| of which base (pin) CDC chunks | 3.4 | 114 KB | 456 KB | | | | |
| depth-1 / depth-2 delta pieces | 5.1 / 2.1 | 55 / 29 KB | (raw) | | | | |

* **Decode, not seeks, dominates**: inflating and hashing whole base chunks is 3/4 of the decoded bytes and
  ~2.5x the pread time. A random offset lands in chunks size-biased: 138-152 KB decoded per touched chunk.
* **Base reads are scattered**: 57% (ZW2) and 68% (in-memory `xd`) of the base chunks a reconstruction reads
  lie outside its own 256 KiB base cluster, because the matcher takes any equal match (shared boilerplate);
  SQLite 5%, TypeScript 7%, git 14%.
* **Kernel readahead does not help random preads**: co-locating records reduced 128 KiB windows per read but
  not device requests (below).
* **P8 inflation reproduced and isolated**: `decodePackPayload` was 75.7% of migration CPU, but as 23,740
  single-record decodes of 599 distinct base records (2.6 GB inflated for 60 MB) plus 960 MB of streamed
  re-reads. Cause: 8 MiB FIFO piece caches in proof and planning contexts against bases read 40 times.

## B5-B/C: implemented engine changes
**Restartable records** (pack codec 2, store feature `packblk`; `-record-blocks 32` on compact/repack/serve).
A compressible record longer than one block is stored as independently decodable 32 KiB DEFLATE blocks with a
table of per-block SHA-256 digests; blocks are primed with a 32 KiB dictionary sampled from 64 compressible
records spread over the pack (codec 3, written first and again last under a distinct name). Span reads (served
ranges ≤ 1 MiB, CDC and authority) read the table, then only the covered blocks, and verify each block digest.
Whole reads still verify the chunk digest; staging, publication, maintenance reads and `verify -deep` prove
every table against the content. A damaged block or table falls back to the whole-record read (fails closed);
a damaged dictionary falls back to the second copy. A span read leaves a sighting at the chunk cache door, so a
chunk read again is read whole and admitted (warm repeats were 1.9-2.9x slower before this). Old builds refuse
the store by feature name; bundles carry logical chunks and reject codec 2; `revert -all` keeps `packblk`.

| block encoding (Coreutils, compressed records only) | 8 KiB | 16 KiB | 32 KiB | 64 KiB |
|---|---:|---:|---:|---:|
| independent DEFLATE blocks | +24.0% | +14.7% | +7.7% | +3.4% |
| primed with a 32 KiB sampled dictionary (+36 B/block) | | +6.0% | **+1.8-2.9%** | −0.1% |
| size-biased bytes decoded per touched chunk (whole: 138-152 KB) | | 20 KB | 37 KB | 67 KB |

Dictionary size (32 KiB blocks): 8 KiB +4.4%, 16 KiB +2.9%, 32 KiB +1.8%; trained on the pack's first 4 MiB
only: +4.9%. Whole-record decode of a blocked record is 2.6% slower (150.7 vs 154.6 MB/s).

**Dependency prefetch** (served reads, default on while the CPU has headroom): the executor starts the base
pieces of each COPY batch on up to four goroutines, and sequential readers prefetch one step ahead; a
per-request single-flight keeps prefetch and reader from reading a piece twice. **P8 fix**: proof and planning
piece caches are LRU at 32 MiB ([profile before](results/migration-profile-b169eda-equivalent.txt)).

**Reconstruction-ordered layout** (`-layout recon` on compact/repack, `-maintenance-layout recon` online; section
13d1). The planner probes each authority family with sparse reads through the production resolver (fresh ranged
context per probe, ≤512 per body, ≤16,384 per plan), anchors every delta piece at the median base offset its
probes read, keeps base pins in CDC order, puts pieces most probes read (envelope headers, frame tables) at the
family head and dependents in base-anchor order. An estimator prices each probe in distinct 128 KiB pack
windows now and as planned; a family is rewritten only for ≥15% gain, through B4's carry/publish/retire path
(offline repack, or an online pass within its pack budget; a family's packs move together). After a relayout
the estimate is a fixed point, and online passes memoize (authority generation, pack set) so idle passes do not
re-probe. Variants measured with the same estimator (Coreutils ZW2, windows per probe, now 7.06):

| interleave pins and dependents per cluster | pins apart (production) | mode anchor | hypergraph coordinate descent |
|---:|---:|---:|---:|
| 5.18 (64-256 KiB clusters) | 5.69-5.74 | 5.25-5.40 | 5.14 |

## B5-D: matched results
Each (object, offset) is read on every configuration in rotating order; **ratio** is the geometric mean of the
per-read wall ratios against `base` (the existing layout, prefetch off). `base` matches the old binary:
`b169eda` on the same stores and offsets gave 0.89-1.09 of `base` (one 1.26 outlier) and blocked+prefetch is
0.53-0.96 of the old binary on all 26 cold object/size cells ([old-binary rows](results/reads-oldbinary.jsonl.gz)).
n = 32 offsets per object and size (96-128 reads per cell), 3 whole reads per object. Reads, maintenance and
migration were measured at `5058f3f`; the final commit only adds the saturation gate for prefetch, which a
single reader never reaches.

| family (store) | cold 4 KiB | cold 64 KiB | near 4 KiB | whole object | storage |
|---|---|---|---|---|---:|
| Coreutils ZW2 deltas (22.7 MB) | **0.67** (3841→2633 µs p50) | **0.72** | 0.77 | **0.78** | +1.8% |
| Coreutils `xd` deltas (20.6 MB) | **0.70** | **0.67** | 0.81 | **0.75** | +2.6% |
| Coreutils CDC (45.0 MB) | **0.74** | **0.81** | 0.73 | 0.92 | +3.3% |
| SQLite 4 states (27.5 MB) | **0.79** | 0.87 | 0.79 | 0.88 | +0.6% |
| TypeScript tgz (12.4 MB) | 1.04 | 1.01 | 1.07 | 0.77 | 0 |
| git packs (1.6 MB) | 0.96 | 0.91 | 1.27 | 0.99 | 0 |
| gzip tar, CDC (14.9 MB) | 0.99 | 1.12 | 0.98 | noise (21-85 ms in every config) | 0 |
| random, CDC (50.5 MB) | 0.97 | 1.02 | 0.90 | | 0 |

Ratios are blocked records + prefetch (`blkP`). Separately: blocked records alone 0.70-0.91 on the four
compressible families (cold); prefetch alone 0.78-0.88 cold on deltas, 0.72-0.77 on whole delta, pin and
TypeScript reads, 0.94-1.07 where no authority body exists; recon + blocks + prefetch (ZW2, the only family
the estimator relaid) 0.69 / 0.72 / 0.83 / 0.88 — no better than blocks + prefetch. Warm repeated reads:
0.69-1.42 on 40-700 µs reads (noise band; cached chunks either way). Tail: ZW2 cold 4 KiB p95 9363 → 5162 µs, 64 KiB p95 20207 → 13699 µs.

Physical work per cold read (medians, base → blkP):

| | stored | decoded | device reads | device bytes | decode µs | CPU µs |
|---|---:|---:|---:|---:|---:|---:|
| ZW2 4 KiB | 111.7 → 77.0 KB | 287 → 141 KB | 9 → 10.5 | 195 → 150 KB | 1702 → 749 | 3080 → 2299 |
| ZW2 64 KiB | 266 → 194 KB | 984 → 490 KB | 15 → 23 | 408 → 303 KB | 4605 → 2509 | 7150 → 5539 |
| SQLite 4 KiB | 124 → 65 KB | 165 → 99 KB | 7 → 8 | 203 → 111 KB | 1117 → 672 | 2414 → 1830 |
| SQLite 64 KiB | 182 → 152 KB | 233 → 193 KB | 9 → 11 | 283 → 221 KB | 1618 → 1426 | 2941 → 2944 |

Blocks trade fewer bytes for a few more requests (table, then blocks). Placement alone (recon, no blocks):
paired 0.98-1.05 on sparse reads, device requests unchanged (14 → 15), whole reads 1.03-1.27 when pins are
interleaved (base sequential readahead broken); pins apart, now the default: sparse 0.93-1.02, whole 0.89-1.07. Explicit 128 KiB window
reads around missed records (`ZEROS3_PACK_WINDOWS=1`, opt-in): device requests 14 → 11-13 but device bytes 4x
(~1 MB per 4 KiB read), latency unchanged or worse.

**Selective duplication** (copies of base chunks into the clusters of the probes that read them out of cluster,
chosen greedily by windows saved per byte, on the planned layout): 5% fewer windows costs +2.3/+2.9/+11.7% of
the family's bytes (xd/ZW2/SQLite), 10% costs +8.7/+11.4/+28%, 20% +35/+49/+77%; with windows not turning into
latency here, discarded ([rows](results/dup-economics-coreutils-zw-mixed.jsonl)).

<a id="concurrent-readers"></a>**Concurrent readers** (8 closed-loop readers, 15 s, 75% 4 KiB / 25% 64 KiB ranges over every object,
page cache dropped at start, each pair run in both orders; [rows](results/concurrent.jsonl)). The CPU is
saturated (3.8 of 4 cores) in every configuration:

| family | reads/s base → blkP | p50 / p95 / p99 µs, base | blkP |
|---|---|---|---|
| Coreutils ZW2 | 3244 → 3390 (+4%) | 900 / 12336 / 32001 | 839 / 11540 / 31672 |
| Coreutils `xd` | 3150 → 3096 (−2%) | 872 / 12982 / 32932 | 864 / 13319 / 33492 |
| Coreutils CDC | 4967 → 5524 (**+11%**) | 758 / 2227 / 23596 | 642 / 2119 / 22396 |
| SQLite | 11914 → 11653 (−2%) | 237 / 1161 / 15112 | 242 / 1212 / 15016 |
| TypeScript / git / gzip / random | −3% / +1% / +2% / −2% | | within ±8% at p50-p99 |

Prefetch only runs while at most GOMAXPROCS/2 served authority reads are open. Ungated, it traded medians for
tails at saturation — ZW2 p50 +35-40%, p99 −57-60%; SQLite p95 2.1x, p99 −60% — without adding throughput
(speculative work on a full CPU reorders, it does not add capacity), so the gate keeps saturated behaviour at
the baseline ([ungated rows](results/concurrent-ungated-prefetch.jsonl)). A single reader is always under the
gate ([check](results/gate-single-reader.jsonl.gz): ZW2 cold 0.61-0.78, pin whole 0.64).

**Maintenance.** Offline conversion (`repack -recompress -record-blocks 32`): 1.2 s / 3.2 CPU-s for the 22.7 MB
ZW2 store, 3.4 s / 8.8 CPU-s for 45 MB CDC, peak RSS ≤ 76 MiB; recon planning adds 2.5-3.6 s (1,024 probes)
per 60 MB family; bytes written ≈ the store. Online, 4 readers of 4 KiB ranges on the ZW2 store plus 24 MB of
dead data, back-to-back passes with `-maintenance-layout recon -maintenance-record-blocks 32`: pass 0 relaid and
converted the family and reclaimed the dead data (5.1 s, 22.8 MB written, 47.5 MB reclaimed); pass 1 1.3 s
(re-plan, nothing to move); 406 later passes were no-ops (memoized plan). Reader p50/p95/p99: idle 781 / 2316 /
3558 µs, during passes 798 / 3204 / 6165 (max 65 ms), after 697 / 2117 / 3214.

**Migration** (P8, Coreutils 9.5→9.7, depth 2, three alternating runs each): `b169eda` 24.9-27.0 s wall /
42.2-45.6 CPU-s → 15.3-15.5 s / 32.1-33.0 CPU-s (−40% / −26%), peak RSS 513-542 → 499-560 MiB; single-record
decodes 23,740 → 6,301; the authority records are byte-identical. The 0.9 GB of streamed source re-reads
(planning passes over whole objects) remain.

## Correctness evidence
`zeros3_b5_test.go`: codec round trip, spans at every block boundary, reopen, `verify -deep`; block, table and
header damage (never wrong bytes, deep verify reports it); loss of one and both dictionary copies; damaged
staged packs refused at publication (table, block, dictionary); encoder rule; feature gate; `revert -all`;
deterministic plan, plan-order placement, no re-plan after relayout; online relayout + blocks with concurrent
readers and writers; nine process-exit cuts of a relayout pass (mark to retirement), each reopened,
deep-verified, read exactly and converged; repeated-span cache admission on CDC and authority paths; the
prefetch gate's count returns to zero. Executed: `-race` over B5, B4, authority partial-read/cache/concurrency,
pack compression, repack, compaction and coalescing tests (241 s, pass) and the ordinary root suite once
(`go test .`, 856 s, pass), both on the code before the prefetch gate; after it, B5/B4/authority/pack/repack/
compact/locality/coalescing (242 s, pass) and B5 under `-race` (134 s, pass). Not run: T1 qualification,
fuzzing, scale fixtures, external harnesses.
Existing test changed: the "unknown codec" case of `TestPackCompression_InvalidRecordLengthsRejectedAtOpen`
moved from codec 2 (now defined) to codec 4, and blocked/dictionary length cases were added.

## Limits and B6 implications
* Not measured: HDD or network block storage, where request count matters more than bytes; there placement and
  window reads may pay. Cache eviction was controlled with `drop_caches`; device latency was not.
* Manifest records (one per cold GET) stay in their segment packs; recon covers pin/delta families only, not
  representation (rep) closures, whose reads bypass the authority piece path.
* Restartable records only help compressible chunks above 32 KiB; small and incompressible data is unchanged.
* `packblk` is never removed; converting back needs a repack without `-record-blocks` (feature stays).
* B6: the base scatter (57-68% of base reads outside the reading region) is an encoder property — a delta
  matcher that prefers nearby equal matches, and base selection that keeps a family's copy sources local,
  would bound what any layout can do; global representation-graph choices should be priced with the B5
  estimator (windows per probe) and the span-read decode model rather than logical CAS bytes.
