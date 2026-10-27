# Z3-30 — Exact SST re-encoding frontier: LevelDB, RocksDB, Pebble (checkpoint 1: structure, corpus, baselines)

Laboratory only; production untouched (`zeros3.go`, tests, root `go.mod/go.sum`: diff empty). Code: `z3/snappyz3`, `z3/sstz3`, `z3/cmd/z3sst`, producers `z3/oracle/sst`.
This is the checkpoint-1 record; the final decisions and the entry-level economics replace it at checkpoint 2.

## Producers, formats, features (audit of the 548 tables actually generated)
| producer | version | table format | compression | checksum | index | features (class) | native parse / exact |
|---|---|---|---|---|---|---|---|
| LevelDB | 1.23 (Ubuntu 1.23-5build1, snappy 1.1.10, plyvel 1.5.1) | legacy 48 B footer, no properties | Snappy 69 % of data-block bytes, raw 31 % | masked CRC32C | 1 level | bloom filter (OPAQUE), no range-del | 183/183, 0 parse errors |
| RocksDB | 9.8.4 (rocksdict 0.3.29, bundled snappy) | BlockBasedTable format_version 6, extended footer, index handle in metaindex | Snappy 70 %, raw 30 % | xxh3, **per-file context seed** (OPAQUE, trailers kept) | 1 level, delta-encoded values | bloom (OPAQUE), properties (OPAQUE) | 169/169 |
| Pebble | v1.1.5 (golang/snappy 0.0.4) | Pebblev4 (magic f09faab3…, version 4), 1-byte value prefix | Snappy 70 %, raw 30 % | CRC32C | **two-level partitioned index in 203/214 tables** (traversed; index blocks OPAQUE) | bloom (OPAQUE), properties (OPAQUE), block-property collector `obsolete-key` | 196/196 |
Common to all three: the data-block core (prefix-compressed entries, restart array, 5-byte trailer, internal key = user key + 8-byte seq/type). They diverge in footer, checksum, index layout, metadata block names and the value prefix. Classified UNSUPPORTED_FALLBACK (never met, rejected by `Open`): hash data-block index, first-key/hash index types, dictionary compression, partitioned filters, non-contiguous (block-aligned) data blocks, formats beyond RocksDB 6 / Pebble 4, Zstd/LZ4/zlib blocks (CDC fallback; not in the corpus). Parsed entries equal the producers' own `rocksdb.num.entries` for every RocksDB/Pebble table (2 201 240 entries each); `z3sst logical` agrees with the generator's independent model on all 66 revisions.

## Corpus (1.93 GiB, deterministic, `PRODUCER_WORKLOAD` unless noted; seed 30)
99 250 live entries / 43.6 MB logical per revision: small metadata, JSON text, 0.6–3 KB half-random binary, 8–40 KB random blobs, 12 repeated values. Per producer, 4 MiB tables, 4 KiB blocks, bloom 10 bits: `variants` (r0 + append, update, update+compact, insert-middle, delete, recompact, three sorted rebuilds `REAL_REEXPORT` at 1×/½×/2× table size with identical sequence numbers, wholesale rewrite, random values, unrelated, renamed keys) and an 8-state `chain` with full directory (MANIFEST/CURRENT/OPTIONS/WAL) snapshots. Producer behaviour measured, not assumed: LevelDB manual compaction never rewrites the deepest level (so recompaction/split/merge are no-ops there); Pebble `Compact` likewise; RocksDB rewrites tables.

## Exactness (rt: parse, encode, discard, reload serialized recipe, decode from recipe + components only)
548 tables × 6 forms = 3 288 round trips, length/SHA-256/bytes identical, 0 failures (blocks, ukv, full, kv, ktv, kvt). Decode 80–100 MiB/s, encode 40–50 MiB/s per core.

## Exact Snappy (the Z3-18 blocker)
Explicit command recipe costs 180–210 commands/block ≈ 470 B per 4 KiB block ≈ **14 % of the table**. Replacing it by a per-block *verified predictor flag* (the block is re-encoded by a deterministic reference encoder and compared to the stored bytes; mismatch keeps the explicit commands) leaves **0 explicit blocks of 524 076** (all 548 tables): Pebble = Go snappy (100 %), LevelDB = C++ 1.1.10 (hash = top 14 bits & table mask), RocksDB-bundled = same algorithm with a 15-bit hash (found by sweeping hash widths on 4 433 blocks of 3–4 KiB: only 15 bits matched all). Snappy recipe ≈ 1 byte/block (8 KB per 28 MiB).

## Baselines: packed cost of a revision added to a store holding r0 (production pack rule, 96 B/ref)
| workload | LevelDB cdc / blocks / ublocks | RocksDB cdc / blocks / ublocks | Pebble cdc / blocks / ublocks |
|---|---|---|---|
| first sight | 21.0M / 27.1M / 26.2M | 20.5M / 27.0M / 26.2M | 20.5M / 27.1M / 26.2M |
| append (flush) | 179K / 967K / 1.0M | 179K / 1.0M / 1.1M | 181K / 1.0M / 1.1M |
| update+compact | 215K / 1.0M / 1.1M | 20.2M / 26.9M / **4.1M** | 10.4M / 9.8M / 9.1M |
| insert+compact | 322K / 1.1M / 1.1M | 20.5M / 27.2M / **11.0M** | 13.2M / 8.9M / 7.9M |
| delete+compact | 49K / 799K / 878K | 19.9M / 26.4M / **10.1M** | 13.3M / 10.7M / 10.0M |
| recompact (no change) | 34K / 783K / 864K | 20.2M / 26.9M / **1.0M** | 35K / 839K / 927K |
| sorted rebuild (REAL_REEXPORT) | 20.6M / 26.8M / 26.0M | 20.3M / 27.0M / **1.1M** | 20.6M / 27.2M / 26.4M |
| unrelated | 20.8M / 26.0M / 24.9M | 20.4M / 26.1M / 25.1M | 20.4M / 26.1M / 25.1M |
`blocks` = native data-block segments incl. trailer; `ublocks` = exact uncompressed blocks + trailer/Snappy recipe. **RocksDB format 6 trailers differ in every block of every file** (context checksum), so a rewritten, byte-identical-content table shares 0 % of CDC chunks (4 223 436-byte table pair: 1 447/1 447 payloads identical, 0/1 447 trailers). Entry-level forms and decisions: checkpoint 2.
