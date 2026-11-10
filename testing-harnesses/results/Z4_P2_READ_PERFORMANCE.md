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
The P0 figures reproduce on the P1C engine: 4 KiB random range p50 **0.41 ms (1 MiB), 1.15 (64 MiB), 3.09 (256 MiB), 10.2 (1 GiB)**; read amplification 22x / 46x / 114x / 387x; CPU 1.5 ms/request at 64 MiB, 13.4 ms at 1 GiB; 4 KiB range ceiling 2.7-3.0 k ops/s at c16-c32; whole 1 MiB GET 2.2 B allocated per byte served (now ~0.1).

## 3. Bottleneck decomposition (CPU profiles, `results/prof/`)
Base, 4 KiB range, large object: manifest read + SHA + JSON decode and their GC ~85-95% of server CPU (grows with object size; tables 6). Candidate, hot 4 KiB range on 64 MiB (c4): SHA-256 of the whole chunk 40%, syscalls 20%, SigV4 13% (was 4 HMACs per request), malloc 12%, JSON 0%, GC 1%. Candidate whole GET: SHA-256 62%, socket write 19%, pread 14% on one goroutine, which is what 14b spreads over cores. Mutex profile at c32: 8.8 ms of delay in a 9 s window; block profile only idle waits.

## 4. Headline results (median of 3 interleaved rounds, base -> candidate; full tables `results/TABLE_main.md`)
| workload | base | candidate | x |
|---|---|---|---|
| 4 KiB range, 1 GiB object, random | 95 ops/s, p50 10.2 ms, p99 15.9 | 1 780 ops/s, p50 0.51, p99 1.1 | **18.8x** (p50 20x) |
| same, hot offset / EOF / cold process | 96 / 94 / 92 ops/s | 3 445 / 3 299 / 1 663 | **35.9x / 35.0x / 18.0x** |
| 4 KiB range, 256 MiB / 64 MiB / 1 MiB | 311 / 786 / 2 172 ops/s | 1 746 / 1 926 / 3 227 | 5.6x / 2.5x / 1.5x |
| 64 KiB range, 1 GiB / 256 MiB | 91 / 304 | 1 453 / 1 644 | 16x / 5.4x |
| 1 MiB range, 1 GiB | 68.5 ops/s | 255 | 3.7x |
| HEAD, 1 GiB / 256 MiB / 64 MiB / 1 MiB | 96 / 327 / 1 084 / 4 031 ops/s | 5 428 / 5 169 / 5 047 / 5 125 | **56x / 16x / 4.7x / 1.3x** |
| first request on a cold process, 1 GiB / 256 MiB / 64 MiB / 1 MiB | 73 / 239 / 488 / 1 236 ops/s | 154 / 355 / 610 / 1 288 | 2.1x / 1.5x / 1.25x / 1.04x |
| whole GET, full client verification (client-bound ~0.9 GiB/s), 1 GiB / 64 MiB / 1 MiB | 788 / 768 / 417 MiB/s | 902 / 963 / 782 | 1.14x / 1.25x / 1.9x |
| whole GET, server-limited (client verifies 1 in 16), 1 GiB c1 / c2 / c4 / c8 | 884 / 1 685 / 2 501 / 2 579 MiB/s (64 MiB) | 2 082 / 2 600 / 2 564 / 2 580 | **2.4x / 1.5x / 1.03x / 1.00x** |
| whole GET small objects (0 B-64 KiB) c1 / c8, 128 KiB segments c8, 512 KiB c8 | 67.5 / 275, 936, 1 447 MiB/s | 73.6 / 346, 1 086, 1 539 | 1.09x / 1.26x, 1.16x, 1.06x |
| loose / direct-pack / deflate 4 KiB range | 1 455 / 1 405 / 1 365 ops/s | 2 111 / 2 321 / 1 673 | 1.45x / 1.65x / 1.23x |
| mixed GET+PUT (4 readers, 2 writers, 1 MiB / 16 KiB), reads/s ; writes/s | 3 127 ; 120 / 8 403 ; 1 003 | 3 691 ; 127 / 10 631 ; 1 077 | 1.18x ; 1.06x / 1.27x ; 1.07x |
Cold-vs-warm: "first" rows are the first read of each object after a server restart on a dropped page cache (manifest cold, chunk cold); `cold_r4k` is a random mix on a cold process (1 GiB: 1 663 vs 92 ops/s). Warm numbers are never quoted as cold. Whole-object rows with full client verification are bounded by the single-goroutine client (it hashes every byte), which is why the server-limited rows exist.

## 5. Range index and manifest cache
Lookup is `sort`-free binary search over `ends[]`: 105 ns at 16 k chunks (1 GiB), 167 ns at 262 k chunks (16 GiB); fixture of 100 k chunks / > 4 GiB checks offsets beyond 32 bits. First load of a 1 GiB-object manifest: read + SHA (1.2 ms) + parse 1.4 ms (was 9.7 ms parse alone). Eager index at load beat a lazy one because the arrays are built in the same pass as the parse. Cost per entry 768 B + 40 B/chunk (1 GiB object 0.65 MB; small object ~1 KB). Compared: `container/list` LRU (rejected: write lock on every hit), sharded CLOCK (chosen: hits take a read lock and set a bit), sparse prefix index (rejected: 8 B/chunk is already small and exact).

## 6. Cache integrity (details in CACHE_MODEL.md; all tested)
Entries come only from digest-verified, fully parsed, validated manifests / digest-verified chunk bytes; a failed, corrupt, panicking or purge-raced load inserts nothing. A hit can serve only bytes the digest names. Cached entries are re-verified against storage after 30 s (`cacheRevalidateAfter`), so damage to a hot manifest or chunk becomes visible to GET/HEAD/range within that interval even if no diagnostic ran; Verify, stats, reachability, GC, repair, bundle export, copy and restore never use the caches, and Verify/repair/compact/repack/prune purge them. Tests: manifest corrupted before load / after warm / missing (file and inline record), chunk corrupted or removed after warm on loose, segment and sealed layouts, damaged candidate never cached, expiry forces the failure without Verify, concurrent first loads share one read, deleted/overwritten/restored versions never served from a stale entry, 412/304/HEAD headers follow the new version.

## 7. Physical reads, compression, allocation
* Read amplification (server bytes read / byte served), 4 KiB random: 1 GiB 387 -> 22.6 (cold chunks; full-chunk verification is the floor: a 4 KiB range is not authenticated by hashing 4 KiB), hot 378 -> 0.1; HEAD 1.5 MB -> 388 B per request. Sub-chunk proofs need persistent Merkle metadata: out of scope, proposed separately.
* Compressed (DEFLATE) packs: 4 KiB hot 2.27x (chunk cache holds the decoded chunk), random 1.23x, 1 MiB range 1.02x (inflate + SHA bound), cold 1.11x; raw packs behave the same (1.00-1.25x), so the P0 "compressed is 3x slower for ranges" gap is the shared per-chunk cost, not decoding. No format change; block-indexed compression recorded as future work.
* Allocation: 4 KiB range request 290 KiB -> ~12 KB (per request, incl. SigV4 and HTTP); GC 1-2% of CPU (was 17% in P0); whole 1 MiB GET ~0.1 B/B.
* File handles: syscr per range request 3.5 -> 2.3 hot; per-request `open` is not a measurable cost, so no descriptor cache (it would complicate pack replacement).

## 8. Concurrency (PR-4 resolved)
4 KiB random ranges on 64 MiB (3 rounds, shared CPUs): c1 839 -> 2 271, c4 2 128 -> 10 100, c8 2 469 -> 14 283, c16 2 712 -> 17 117, c32 2 964 -> 18 320 ops/s (**6.2x**; p99 at c32 46 -> 6.0 ms). The old plateau "2 cores, 3.6 k/s" was manifest parsing + GC: the base server used 2.5-2.7 cores (CPU per request 1.5 ms), the candidate needs ~0.1 ms and the load generator is the limit. Server pinned to 1 core: 1 327 -> 8 566 ops/s at c16 (6.5x, server at 0.99 cores); with the server on 3 cores and the generator on 1, the candidate used 1.7 server cores at 12 k ops/s: the single generator core saturates first. Mutex delay at c32: 8.8 ms per 9 s; no hot lock (shard RW locks, per-shard counters, no global lock added). Whole-object GET scales to the 4-core ceiling (~2.55 GiB/s, 62% SHA-256) and the adaptive pipeline neither helps nor hurts there (1.00-1.03x at c4-c8).

## 9. Cache sizing, thrash and memory (`results/TABLE_scale.md`, `chunkcache_sizing.jsonl`)
* Stores of 1 k / 10 k / 100 k / 1 M objects x 4 KiB, uniform random reads (worst case for a cache), manifest budgets 0-256 MiB: throughput within noise, CPU per request 155 -> 110 us at 1 k objects, RSS +10 MiB at 1 k-10 k, **identical at 100 k (140 MiB) and 1 M (1.29-1.31 GiB, the P1C open footprint)**; entries stop at the budget (19 k entries / 19 MiB at 32 MiB), evictions counted.
* Chunk cache sizing (hot sets fit): 16 MiB already gives 1.38x/1.49x on hot/spread 4 KiB ranges; Zipf 1 MiB objects 1.34x at 64 MiB. Default 32 MiB: RSS +60 MiB when full. Worst-case total of both caches 64 MiB.
* **Strongest negative result.** First chunk-cache design (64 MiB, door 32 k keys): with a working set 12x the budget (750 MiB of 128 KiB-512 KiB objects, 8 clients) whole GET fell to **0.86-0.91x** because every repeat sighting copied a chunk that was evicted before reuse (copy + GC cost 2-3 reads). Fixed: door sized to ~4x the budget's entry count (a repeat later than that could not hit), plus per-shard hit/admit feedback that throttles copy-ins. Final: every cell 0.98x-1.49x, thrash cells 1.00-1.03x (`chunkcache_v1_*.jsonl` keep the failing runs).
* Other rejected/changed: pipeline with 4 MiB runs / 16 MiB / 2 streams cost 3-5% at c4-c16 -> adaptive stream count, 1 MiB runs (RSS and throughput both better); a client that hashes in-line hid the server (0.8 GiB/s cap) -> sampled verification for server-limited rows.

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
