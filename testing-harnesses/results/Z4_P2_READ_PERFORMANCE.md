# Z4-P2: GET / range read performance (manifest cache, range index, chunk cache, pipelined streaming)

Verdict: **P2_PRODUCTION_READY**
Base `4059cd93548313735cb6a704fc853100f5147b0a` (P1C merged). Branch `z4/p2-read-performance`. PR / merge: see section 19.
Environment: 4 vCPU Firecracker VM, 16 GiB, ext4 on virtio (cheap fsync), Go 1.27.0, Linux 6.18. Load generator = `p0bench` on the same 4 vCPUs unless a table says it was pinned. B2 draft #97 and drafts #80/#81 untouched.
Evidence: [`../z4/p2/`](../z4/p2/README.md) (raw JSONL, scripts, tables). Design and integrity contract: [`../z4/p2/CACHE_MODEL.md`](../z4/p2/CACHE_MODEL.md).

## 1. What changed (production, `zeros3.go` only; no format, journal, protocol or default-policy change)
| section | change |
|---|---|
| 5b served manifests | `servedManifest`: digests as raw 32-byte values + cumulative end offsets (40 B/chunk). Range start = binary search. Canonical-form scanner parses the chunk list without per-chunk allocation (7x faster than `encoding/json`, 10 allocations instead of 16 414 for a 1 GiB object); anything unusual falls back to `encoding/json` (differential + fuzz tested). Bounded sharded CLOCK cache keyed by the journal's manifest SHA-256, single-flight loads, generation-fenced purge. |
| 14 reader | `manifestReader` runs on the index; HEAD/GET/UploadPartCopy use `headServed`; 412 is decided from the namespace entry alone; listings peek the cache but never fill it. |
| 14a chunk cache | verified-chunk cache (copy of bytes that passed the full digest check), admission by second sighting through a door sized to the budget, hit/admit feedback throttle, requests > 4 MiB never admit, 32 MiB. |
| 14b pipelined streaming | ranges >= 4 MiB: up to 3 runs of <= 1 MiB read and verified concurrently, delivered in order, 8 MiB in flight, only when <= GOMAXPROCS/2 large streams are active. |
| other | SigV4 signing key cached per (secret, date, region, service); `ZEROS3_{MANIFEST,CHUNK}_CACHE_MIB`, `ZEROS3_PIPE_*`, `ZEROS3_READ_CACHE_STATS` are benchmark/test switches. |

Not changed: `readVerifiedManifest`, `casRead`, Verify/stats/GC/repair/bundle/restore/copy paths (all still read and hash the real bytes), pack/segment format, locator, P1C lock order.

## 2. Verified baseline (base `4059cd9`, interleaved with the candidate, 3 rounds, every byte verified)
The P0 figures reproduce on the P1C engine: 4 KiB random range p50 **0.39 ms (1 MiB), 1.16 (64 MiB), 3.01 (256 MiB), 9.78 (1 GiB)**; read amplification 22x / 46x / 114x / 387x; server CPU 3.3 ms per 4 KiB range on 256 MiB and ~13 ms on 1 GiB; 1.05 MB allocated per range request on 256 MiB (262 B per byte served, GC 8.6% of user CPU); 4 KiB range ceiling 2.6-2.8 k ops/s at c16-c32. Whole 1 MiB GET already allocates little on this engine (53.6 KB/request, 0.05 B/B): P0's 2.2 B/B predates P1A-P1C.

## 3. Bottleneck decomposition (CPU/alloc profiles in `results/prof/`)
Base, 4 KiB range: manifest read + SHA + JSON decode + GC is the request: CPU per request 1.5 ms (64 MiB), 3.3 ms (256 MiB), ~13 ms (1 GiB) against 0.19-0.4 ms afterwards. Candidate, random 4 KiB range (chunk cache cold): SHA-256 of the whole chunk ~40%, syscalls ~20%, SigV4 ~13%, malloc ~12%, GC ~1%. Whole-object GET on one goroutine: SHA-256 62%, socket write 19%, pread 14% (what 14b spreads over cores). Mutex profile at c32: 8.8 ms of delay in a 9 s window; block profile only idle waits.

## 4. Headline results (median of 3 interleaved rounds, base -> candidate; full tables `results/TABLE_main.md`)
| workload | base | candidate | x |
|---|---|---|---|
| 4 KiB range, 1 GiB object, random | 97 ops/s, p50 9.8 ms, p99 15.3 | 1 760 ops/s, p50 0.50, p99 1.13 | **18.1x** (p50 19x) |
| same, hot offset / EOF / cold process | 100 / 104 / 101 ops/s | 3 355 / 3 227 / 1 699 | **33.6x / 31.1x / 16.9x** |
| 4 KiB range, 256 MiB / 64 MiB / 1 MiB | 317 / 777 / 2 278 ops/s | 1 831 / 2 038 / 3 279 | 5.8x / 2.6x / 1.44x |
| 64 KiB range, 1 GiB / 256 MiB | 98 / 301 | 1 441 / 1 586 | 14.7x / 5.3x |
| 1 MiB range, 1 GiB / 256 MiB | 76.8 / 174 ops/s | 256 / 319 | 3.3x / 1.8x |
| HEAD, 1 GiB / 256 MiB / 64 MiB / 1 MiB | 111 / 352 / 1 117 / 4 221 ops/s | 5 492 / 5 879 / 5 589 / 5 556 | **49x / 16.7x / 5.0x / 1.3x** |
| first read of each object on a cold process, 1 GiB / 256 MiB / 64 MiB / 1 MiB | 76 / 246 / 540 / 1 282 ops/s | 129 / 369 / 659 / 1 321 | 1.7x / 1.5x / 1.2x / 1.03x |
| whole GET, full client verification (client-bound), 1 GiB / 64 MiB / 1 MiB | 797 / 805 / 415 MiB/s | 977 / 1 088 / 817 | 1.23x / 1.35x / 1.97x |
| whole GET, server-limited (client verifies 1 in 16), 1 GiB c1 / c2 / cold c1 | 831 / 1 657 / 823 MiB/s | 1 913 / 2 422 / 1 423 | **2.3x / 1.46x / 1.73x** |
| same, 64 MiB c1 / c2 / c4 / c8 | 804 / 1 576 / 2 272 / 2 411 | 1 817 / 2 426 / 2 451 / 2 484 | 2.26x / 1.54x / 1.08x / 1.03x |
| whole GET small: 0 B-64 KiB c1 / c8; 128 KiB segments c8; 512 KiB packs c8 | 68 / 259; 904; 1 456 MiB/s | 77 / 320; 1 064; 1 479 | 1.13x / 1.24x; 1.18x; 1.02x |
| 4 KiB range on loose / direct-pack / DEFLATE-pack layouts | 1 376 / 1 445 / 1 326 ops/s | 2 242 / 2 440 / 1 641 | 1.63x / 1.69x / 1.24x |
| mixed GET+PUT 4 readers + 2 writers (1 MiB objects, 64 KiB ranges): reads/s ; writes/s | 3 016 ; 114 | 3 549 ; 122 | 1.18x ; 1.08x |
| same, 16 KiB objects, 4 KiB ranges (active segments sealing) | 7 676 ; 937 | 10 389 ; 1 072 | 1.35x ; 1.14x |
"First" rows are the first read of every object after a server restart on a dropped page cache (manifest and chunk cold); `cold_r4k` is a random mix on a cold process. Warm numbers are never quoted as cold. Full-verification whole-object rows are bounded by the single-goroutine client (it hashes every byte at ~0.9 GiB/s), so the server-limited rows exist. The slowest cell anywhere in the matrix is 0.98x (1 MiB range on raw-pack text, noise); no regression otherwise.

## 5. Range index and manifest cache
Lookup is `sort`-free binary search over `ends[]`: 105 ns at 16 k chunks (1 GiB), 167 ns at 262 k chunks (16 GiB); fixture of 100 k chunks / > 4 GiB checks offsets beyond 32 bits. First load of a 1 GiB-object manifest: read + SHA (1.2 ms) + parse 1.4 ms (was 9.7 ms parse alone). Eager index at load beat a lazy one because the arrays are built in the same pass as the parse. Cost per entry 768 B + 40 B/chunk (1 GiB object 0.65 MB; small object ~1 KB). Compared: `container/list` LRU (rejected: write lock on every hit), sharded CLOCK (chosen: hits take a read lock and set a bit), sparse prefix index (rejected: 8 B/chunk is already small and exact).

## 6. Cache integrity (details in CACHE_MODEL.md; all tested)
Entries come only from digest-verified, fully parsed, validated manifests / digest-verified chunk bytes; a failed, corrupt, panicking or purge-raced load inserts nothing. A hit can serve only bytes the digest names. Cached entries are re-verified against storage after 30 s (`cacheRevalidateAfter`), so damage to a hot manifest or chunk becomes visible to GET/HEAD/range within that interval even if no diagnostic ran; Verify, stats, reachability, GC, repair, bundle export, copy and restore never use the caches, and Verify/repair/compact/repack/prune purge them. Tests: manifest corrupted before load / after warm / missing (file and inline record), chunk corrupted or removed after warm on loose, segment and sealed layouts, damaged candidate never cached, expiry forces the failure without Verify, concurrent first loads share one read, deleted/overwritten/restored versions never served from a stale entry, 412/304/HEAD headers follow the new version.

## 7. Physical reads, compression, allocation
* Read amplification (server bytes read per byte served), 4 KiB random: 1 GiB 387 -> 22.7, hot 378 -> 0.1, 64 MiB 46 -> 21; HEAD 1.49 MB -> 388 B per request. 22 is the floor of full-chunk verification (a 4 KiB range is not authenticated by hashing 4 KiB of a 64 KiB chunk); sub-chunk proofs need persistent Merkle metadata and are proposed separately, not built here.
* Compressed (DEFLATE) packs vs raw packs, 4 KiB: hot 2.39x / 2.29x, random 1.24x / 1.09x, cold process 1.18x / 1.28x; 1 MiB range 1.03x / 0.98x; whole 1.37x / 1.35x. Compressed and raw behave alike: the P0 "compressed ranges are 3x slower" gap is the shared per-chunk read + hash cost, not inflate (the chunk cache stores the decoded chunk, so hot compressed ranges skip both). No format change; block-indexed compression is recorded as future work.
* Allocation: 4 KiB range on 256 MiB **1.05 MB -> 27.8 KB per request (38x)**, GC 8.6% -> 0.8% of user CPU. Whole 1 MiB GET (64 objects, 4 clients): base 53.6 KB/request; candidate 78 KB/request when the working set is twice the 32 MiB cache (36 KB with the cache off), 26 KB (0.026 B/B) with a fitting working set, where CPU per request halves (677 vs 1 407 us). Admission copies are the only added allocation; a chunk already cached is no longer re-copied (found by this profile, fixed).
* File handles: read-family syscalls per hot range 3.5 -> 2.3; per-request `open` is not a measurable cost, so no descriptor cache (it would hold replaced packs open).

## 8. Concurrency (PR-4 resolved)
4 KiB random ranges on 64 MiB, shared CPUs: c1 857 -> 2 532, c4 2 131 -> 10 009, c8 2 512 -> 14 185, c16 2 641 -> 16 389, c32 2 833 -> 17 498 ops/s (**6.2x**; p99 at c32 47.6 -> 7.3 ms). The old "2 cores, 3.6 k/s" plateau was manifest parsing + allocation/GC, not a lock and not the machine: the base server used 2.6 of 4 cores at 1.5 ms CPU per request; the candidate needs ~0.1 ms. With the load generator on its own cores: server pinned to 1 core 1 551 -> 8 713 ops/s (c16, server at 0.99 cores, so it is server-limited and 5.6x faster per core); server on 2 cores 12 333 ops/s using 1.46 cores; server on 3 cores and the generator on the fourth: 10 671 ops/s using 1.69 cores, i.e. the single generator core saturates first. Mutex delay at c32: 8.8 ms per 9 s, no hot lock (read-locked shards, per-shard counters, nothing global added). Whole-object GET at c8 sits at the 4-core SHA-256 ceiling (~2.5 GiB/s) in both builds; the adaptive pipeline neither helps nor hurts there (1.00-1.03x).

## 9. Cache sizing, thrash and memory (`results/TABLE_scale.md`, `chunkcache_sizing.jsonl`)
* Stores of 1 k / 10 k / 100 k / 1 M objects x 4 KiB, uniform random reads (a cache's worst case), manifest budgets 0-256 MiB: throughput within noise, CPU per request 155 -> 110 us at 1 k objects, RSS +10-22 MiB at 1 k-10 k, no change at 100 k (140 MiB) and 1 M (1.06-1.31 GiB, the P1C open footprint, unrelated to cache size); entries stop at the budget (19 k entries / 19 MiB at the 32 MiB default). The small-object gain is modest because an inline manifest is already one cheap record.
* Chunk cache sizing (default 32 MiB, 2 rounds): hot 4 KiB ranges 1.38x / spread 1.47x already at 16 MiB; Zipf 1 MiB objects 1.26x; medium/segment working sets 1.02-1.06x; working sets 6-12x the budget 0.97-1.07x. RSS +60 MiB when full; both caches together can hold at most 64 MiB whatever the store size.
* **Strongest negative result.** The first chunk-cache design (64 MiB, door remembering 32 k keys) cost **9-14%** on whole GETs of 128-512 KiB objects with a working set 12x the budget (every repeat sighting copied a chunk that was evicted before reuse; copy + GC ~2-3 reads each; `chunkcache_v1_workingset.jsonl`). Three changes fixed it: a door sized to ~4x the budget's entry count (a repeat later than that could not have hit), per-shard hit/admit feedback that throttles copy-ins, and the 32 MiB default. The residual cost at 12x oversubscription is about 3%.
* Other changed designs: a pipeline with 4 MiB runs, 16 MiB in flight and 2 streams cost 3-5% at c4-c16 -> adaptive stream count and 1 MiB runs (lower RSS, equal throughput; `pipe_sweep_*.jsonl`); an in-line hashing client hid the server behind a 0.8 GiB/s cap -> sampled verification for server-limited rows; the manifest `TTL`-free cache was replaced by 30 s re-verification after a corruption test showed damage masked until eviction.

## 10. P1C integration (all tested, race detector clean)
Cached inline manifest in an open segment serves unchanged after the segment seals (no reload: `Loads` constant), after `reloadPacks` and from a cold cache on sealed packs; readers racing `sealSegments` while writers PUT, overwrite and delete see only complete versions (4 readers x 8 rounds, then `verify -deep` clean); locator staleness is handled where it always was (casRead refresh-after-seal), because entries hold no physical reference; read-only openers are unaffected (cache is per `Store`); pruned/deleted versions are not served merely because cached (namespace is consulted first). Tier moves, repack and GC take the existing quiesce path and purge the caches.

## 11. Correctness and fault qualification
`zeros3_p2_test.go`, `zeros3_p2_read_test.go`: exact-byte range matrix (all chunk edges +-1, first/last byte, whole, open/suffix/clamped/416/malformed, repeated 3x to hit miss/sighting/hit) over loose, segment, sealed and compressed-pack layouts in serial, pipelined and cache-off modes plus HEAD headers; parser = `encoding/json` on 3 000 mutated manifests + fuzz target; index vs linear scan incl. zero-length chunks; cache bounds, CLOCK, single flight, panic in loader, name mismatch, purge fence, disabled cache; pipelined corruption (cut short, never wrong, no bytes past the damage), client disconnects, 8 concurrent streams, stream-slot and goroutine leaks, adaptive stream limit; revalidation expiry; conditional/versions/delete/restore with a warm cache. One existing test (`TestStreamingGet_Corruption`) now purges the caches after damaging a chunk it had read before (its subject is the read that reaches damaged storage); the warm-cache semantics have their own tests.

## 12. Production compatibility and validation
Root `go.mod` unchanged, no new dependency, still one `zeros3.go`; store/journal/pack/wire formats and defaults unchanged; no new capability floor. Old/new stores: base-written stores verify (-deep) and read byte-exact on the candidate and vice versa for 4 KiB, 1 MiB, 64 MiB objects (`results/compat.txt`). `gofmt` clean, `go vet` clean, build reproducible (two `-trimpath` builds identical), full root suite `ok` (460 s), `go test -race .` : RACE_RESULT. External harnesses (AWS SDK v2, minio-go via conformance profile, multipart, presign, range, copy, conditional, streaming/aws-chunked PUT, packed/compressed CAS, sync/replicate/fork/snapshot/repair/bundle, consumer apps): 26 run, 0 failed, 2 skipped (rclone, package-killer binaries absent). The `static` source-map check fails identically before and after (34 source-map, 23 test-map entries, pre-existing drift; unchanged by P2).

## 13. Remaining opportunities
1. SHA-256 of whole chunks is now the floor (40% of a cold 4 KiB range, 62% of a whole GET): only sub-chunk Merkle metadata or a faster hash (SHA-NI is already used) lowers it; both need a format decision.
2. SigV4 verification + net/http cost ~100 us/request and now bounds small-object c32 (~18 k ops/s on 4 cores).
3. 1 M-object store-open RSS (1.3 GiB) and a persistent locator checkpoint: P8, untouched.
4. Compressed packs: block-indexed records would let a range inflate only its blocks (future format).
5. Optional: descriptor cache for packs (no evidence it matters), multi-process shared caches (none planned).
