# B3: compact namespace generations, similarity roots, zstd reach, admission pricing

Linux amd64, Go 1.27.0, 4 vCPU, warm page cache. Old = B2 head `3aa98e5`, new = this branch. Rows: [results.jsonl](results.jsonl) (medians of 5 fresh processes); drivers: `measure.py` and `zeros3_b3_scale_test.go` (fixture and probes; they also compile against the old tree). The fixture is journal-only and synthetic: 1,000,000 puts, ~30% deleted, ~2% overwritten (1.32 M records, 501 MB journal, 700 k visible, 320 k retained roots). It has no packs, so open numbers exclude locator state (the P9 862 MB figure included it); ratios refer to the namespace alone.

## Architecture
* Visible objects and retained history are one **paged generation file** (`journal/namespace-v4.zck`): rows keyed `bucket\0key`, ~16 KiB prefix-compressed pages with restarts every 16 rows and a CRC each, a sparse in-memory directory of page first keys, a dictionary for repeated strings, canonical UUIDs and ETags as raw bytes.
* Reads resolve a mutable overlay (sorted blocks; a hash map while replaying) over a frozen overlay over the table; listings merge the three in key order and decode a row only when returned. Pages come from a 32 MiB sharded LRU and are CRC-checked on load.
* The checkpoint is a published generation bound to the journal by per-16 MiB SHA-256 blocks, verified on every core beside the file digest; republishing reuses covered blocks. Stale, truncated, foreign or damaged files fall back to full replay. A page that turns bad later fails reads (sticky) instead of answering from a gap.
* Consolidation freezes the overlay under the commit lock, merges table plus frozen overlay (untouched rows copied byte for byte) off-lock, publishes by rename and swaps; a failed build restores the overlay. `serve -namespace-checkpoint` runs it past 262,144 overlay rows (up to a quarter of the table). Journal replay spills private unlinked generations past 2 M rows. V2/V3 files are still read; V4 is derived state older binaries ignore.

## Namespace (1 M objects)
| | old V3 | new V4 |
|---|---:|---:|
| checkpoint bytes | 184,329,850 | 94,297,568 (-49%) |
| fresh open, ms | 1,436 | **181** (7.9x) |
| Go bytes allocated per open | 338.0 MB | **7.0 MB** (48x) |
| live heap / RSS after open | 285.3 MB / 347 MB | **0.6 MB** / 21 MB |
| open with a 20,000-op journal suffix: ms / alloc / live | 1,522 / 351 MB / 290 MB | 282 / 61 MB / 43 MB (32 MiB is page cache) |
| journal-only open: ms / alloc / live | 2,901 / 839 MB / 323 MB | 2,956 / 878 MB / 331 MB |
| point lookup hit p50 / p95 | 0.4 / 0.7 µs | 3.0 / 15.7 µs (cold page misses) |
| 1,000-key prefix listing page p50 | 45.9 ms | **0.25 ms** |
| next page of a whole-bucket walk p50 | 386 ms | 0.33 ms |
| versions of a key p50; PUT/DELETE p50 with fsync | 1.0 µs; 279 µs | 13 µs; 217 µs |
| write the checkpoint from the journal | 5.06 s | 5.03 s |

Open is now bounded by hashing the 501 MB journal prefix. The old listing sorted every key per page. Not done: skipping a whole common-prefix group on a seek (it is still scanned row by row) and a Bloom filter for new-key puts during replay.

## Similarity index
Roots (manifest UUID and digest) stream from table pages into a UUID arena, and content identities go through a dense digest table instead of Go maps. Roots stage at 1.02 M roots: 88 MB retained vs 49 MB, but the old figure borrows the namespace's 285 MB; total live heap 89 MB vs 334 MB (3.8x), peak 159 MB vs 334 MB. A 1 M-entry digest table costs 48 MB, 3 allocations, 46 ms against a map's 201 MB, 8,200, 197 ms. The ZSR1 runs, fences, fallbacks and bounded lookups are unchanged; a 1 M catalog build was not rerun (it needs 1 M real bodies).

## zstd model-window reach
A replay pass records, per compressed block, how far before it the model-coded ranks walk (the oldest source position visited); the table follows the seek index (2.4-4.5 KB per 60 MB tar). Restarts load only that interval; the proof replay refuses an understated entry and objects without a table keep the full window. Cold 4 KiB reads of a 60 MB Go-source tar, plaintext fetched / median ms:

| | level 3 | level 9 | level 12 | level 9, 8 MiB window |
|---|---:|---:|---:|---:|
| full window | 2,356,377 / 48.2 | 2,358,630 / 49.2 | 2,358,630 / 52.9 | 8,451,225 / 133.9 |
| reach | 1,901,358 / 43.6 | 2,101,150 / 49.4 | 2,075,467 / 50.6 | 5,525,079 / 101.4 |

Dense text keeps most blocks within 10% of the window (median reach 1.66-1.93 MB of 2.1 MB): -11 to -19% at the default window, -35% at 8 MiB. Exactness holds at every block restart of eight zstd fixtures, including 4 MiB streams with references up to 4 MiB back. The gzip table split was not done: it needs a format change and leaves first-range cost unchanged.

## Admission pricing (TypeScript 5.6.2/5.6.3/5.7.2 tarballs, 12.58 MB)
Both numbers were cohort-level; the estimate was wrong, not its scope. `physChunk` priced components not yet packed at DEFLATE level 1 while packs store at level 6 (+10.9% on synthetic text, +19.7% on this cohort's union), so the group looked 8.3% cheaper than CDC; the store measured 23.4% after gc, repack and recompression. At the pack's level the estimate is 23.38% against 23.36% measured, and the ordinary planner admits the cohort. Plans now carry `group` (members, union bytes, CDC bytes, saving, policy minimum, admitted) beside the per-object `saving`, and a refused group names its priced bytes. Thresholds, `-archive` and tier read-cost gates are unchanged: replay still costs (B2) 19 ms / 210 ms / 0.6 s for 4 KiB / 1 MiB / whole reads against CDC 0.3 ms / 5 ms / 28 ms, and only tiers that allow hard codecs admit it.

```sh
ZEROS3_B3_BUILD=fx ZEROS3_B3_N=1000000 go test -run '^TestB3BuildFixture$' .   # then measure.py WORK out.jsonl
ZEROS3_B3_ZST=file.tar.zst go test -run '^TestB3ReachProbe$' -v .
ZEROS3_B3_TGZ=a.tgz,b.tgz,c.tgz go test -run '^TestB3AdmissionProbe$' -v .
go test -race -run '^Test(NS|Namespace|Auth|Zstd)' .
```
