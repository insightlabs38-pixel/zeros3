# Z4-P2 read caches: identity, integrity contract and invalidation

Normative for `zeros3.go` sections 5b (served manifests), 14a (verified chunk cache) and 14b (pipelined streaming).
Nothing here changes a stored byte: no store, journal, pack, manifest or wire format is touched, and the caches are never authoritative.

## 1. What is cached

| cache | key | value | bound | default |
|---|---|---|---|---|
| manifest (`manifestCache`) | SHA-256 of the manifest's exact bytes (the digest the journal records for the root) | `servedManifest`: header fields, chunk digests as raw 32-byte values, cumulative end offsets (40 B per chunk) | bytes, 8 CLOCK shards | 32 MiB (`ZEROS3_MANIFEST_CACHE_MIB`, 0 = off) |
| chunk (`chunkCache`) | chunk SHA-256 | private copy of the verified chunk bytes (<= 256 KiB) | bytes, 16 CLOCK shards | 64 MiB (`ZEROS3_CHUNK_CACHE_MIB`, 0 = off) |

Both are fields of `Store` (never package globals): two stores in one process never share an entry, and a reopened store starts empty.
The `ZEROS3_*` switches exist for benchmarking and tests, not as a tuning interface.

## 2. Identity: why an entry can never be the wrong object or version

* The manifest key is a content digest. Equal digests mean equal bytes, hence equal logical contents; the entry stores the manifest UUID too and a hit also requires that name to equal the one the namespace supplied (a mismatch is treated as a miss and never shared).
* The namespace (journal replay, history, snapshots) remains the only authority for *which* `(UUID, digest)` a key currently has. The cache is asked only with a pair the namespace produced, so an overwritten, deleted, pruned or restored version is never looked up through a stale name, and a retained version (history, snapshot) stays readable because its pair is still produced by its root. Delete + recreate of identical content yields a new UUID and a new manifest: a different entry.
* The chunk key is the chunk digest, the value is bytes that hash to it. A chunk can only be requested by a manifest listing it.
* The key says nothing about *where* the bytes live. An entry contains no pack number, offset, file handle or locator, so segment sealing, repack, compaction, tier moves and locator replacement cannot make it stale; chunk bytes are re-resolved through `casRead`/`packRunReader` (which keep P1C's refresh-after-seal retry) whenever the chunk cache misses.

## 3. Integrity contract

Before P2 every GET re-read the manifest and hashed it, and every chunk it served was read and hashed. After P2:

1. **Creation.** An entry exists only if the bytes hashed to the journal digest, parsed completely and passed validation (32-byte digests, non-negative lengths, no offset overflow, lengths sum to `total_length`). A failed or panicking load inserts nothing and reaches every concurrent waiter as the same error. A chunk is copied into the cache only after its full SHA-256 check (including DEFLATE decoding) succeeded, from a request that began in the current cache epoch.
2. **Serving.** A hit serves the verified in-memory copy. Every chunk byte a response contains still passed a digest check at some point after the cache entry that supplied it was created; **a cache hit can never put bytes on the wire that differ from what the digest names.**
3. **Bounded trust.** An entry older than `cacheRevalidateAfter` (30 s) is a miss: the read that replaces it re-reads storage, re-hashes it, and a failure evicts the entry. Damage to a hot manifest or chunk is therefore visible to GET/HEAD/range within that interval without any diagnostic, and a missing or corrupt chunk can never be hidden for longer.
4. **Diagnostics never use the caches.** `Verify` (basic and deep), stats, reachability, GC, repair, bundle export, `CopyObject`, `RestoreObjectVersion`, snapshot description and history operations read the real manifest and chunk records. `Verify`, repair (`repairFromPeer`), `compact`, `repack` and history prune additionally purge both caches (a generation fence stops loads that started before the purge from re-inserting), so a read after them is loaded afresh.
5. **Namespace-only decisions** (existence, ETag, size, `If-Match`/`If-None-Match`) come from the in-memory namespace entry, exactly as before. A failed `If-Match` (412) is now decided without loading the manifest at all.

What changed for a client: a manifest or chunk file that rots *after* it was cached may be served correctly from memory for up to 30 s instead of failing immediately. What did not change: wrong bytes are never served, `verify -deep` always finds the damage, and nothing is hidden permanently.

## 4. Invalidation and relocation matrix

| event | manifest entry | chunk entry | why |
|---|---|---|---|
| segment seals / locator refresh | kept | kept | no physical reference inside |
| `reloadPacks`, tier move, repack, compact | kept (purged defensively by compact/repack) | same | content identical |
| peer repair replaces a bad chunk | purged at start and end | purged | repair reads storage, caches must not mask what it fixes |
| manifest/chunk damaged after warming | served until re-verification (<= 30 s) or `Verify` | same | section 3 |
| manifest/chunk missing before first load | not cached; error to all waiters | not cached | fail closed, same status as before |
| overwrite / delete / restore / prune | not consulted for unreachable pairs; prune purges to release memory | unaffected | namespace is the authority |
| store reopen / new process | empty | empty | per-`Store` |
| concurrent first loads | one load, others wait (single flight) | each reader reads; the copy-in is idempotent | |
| budget reached | CLOCK evicts unreferenced entries; oversize entries are returned but not kept | same | memory bound is strict (checked by tests) |
| purge racing a load/offer | generation / epoch fence rejects the insert | same | |

## 5. Admission and scan resistance

* Manifest cache: every first load is inserted (the work saved per hit is large and the entry is small); listings (`ListObjectsV2`) peek but never insert, because they only need `created_at` and would evict the working set.
* Chunk cache: a key is copied in only on its **second** sighting (a 2048-slot direct-mapped fingerprint door per shard) and never for requests longer than 4 MiB, so one-pass streaming of large objects neither pays the copy nor displaces anything. New entries start unreferenced, so a scan of one-touch keys evicts itself first.

## 6. Pipelined streaming (14b)

Applies to ranges of at least 4 MiB on a store with a free stream slot (`GOMAXPROCS/2` slots, at least 1) and at least two usable CPUs. The planner is the serial reader's own run planner; up to `min(4, GOMAXPROCS-1)` runs are read and verified concurrently by the same `packRunReader.read` (or `casRead` for non-packed chunks), at most 12 runs / 16 MiB are in flight, and chunks reach the consumer strictly in order. A record that fails inside a run is retried through `casRead`'s other copies exactly as the serial reader does. `close()` cancels pending work, waits for running workers, returns every buffer and the stream slot; a client disconnect therefore never leaks goroutines or buffers (tested). Pipelined readers bypass the chunk cache.

## 7. Memory

Per cached manifest: 768 B + 40 B per chunk + strings (1 GiB at 64 KiB chunks: ~0.65 MB; a 1 KiB-manifest small object: ~1 KB). Budgets are hard: the tests recompute shard byte counts from the ring and assert they never exceed the budget. Worst case with both caches full: 96 MiB, independent of store size, object count and chunk count.
