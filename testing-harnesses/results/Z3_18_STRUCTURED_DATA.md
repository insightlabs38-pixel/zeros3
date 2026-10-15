# Z3-18 — Structured-data representation shootout (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journals, pack/store formats, read paths, GC, snapshots).
Code: `z3/sqlitez3` (promoted), `z3/cmd/z3struct`, producers `z3/oracle/structured/*.py` (venv + corpora outside Git; Z3 stays stdlib-only).
Carry-forward done first: `gitz3.ParseIdxAt(io.ReaderAt, size, …)`; `ParseIdx([]byte)` is a thin wrapper, `Idx` keeps only fanout + geometry (v1 stays byte-backed, characterization-only).

**Question.** Which structured-storage families hide reusable content from production CDC (16/64/256 KiB), which structural boundary exposes it
economically, and which deserve deeper ZeroS3 support?

**Conclusion.** One of four is promoted. SQLite's page structure exposes large reuse that CDC cannot see (up to 14× on VACUUM, 8–10× on sparse
updates), but only two things made it economical: a **page tree** (a flat page list costs more than CDC on small edits) and a **head split** of
overflow/freelist pages (their first 4 bytes are a page number that moves). SST and Parquet hide real reuse too, but their native block/chunk
boundaries recover almost none of it: the content moves *inside* blocks, which needs entry/value-level exact re-encoding. Zarr is a control that
needs nothing beyond object-level CDC + manifest (sharded v3 is a small gain).

## Producers and corpora (seed 18; costs = revision added to a store holding r0; production pack rule; 96 B/ref; K = KiB)

SQLite 3.45.1 (Python 3.13) 20 MiB, 60k-row table + random-key index + 8×512 KiB blobs, 16 variants + 10-revision app history chain; LevelDB 1.23
(plyvel 1.5.1) 200k keys, 7 tables, 15 variants incl. three full rebuilds; RocksDB (rocksdict 0.3.29, format_version 6, xxh3) 5 variants;
Parquet (pyarrow 18.1.0) 400k rows × 6 cols, 8 row groups, 12 variants; Zarr v2 (zarr 2.18.4/numcodecs 0.15.1) 2048² f32, 256 chunk objects, 10 variants;
Zarr v3 sharded (zarr 3.1.0) 6 variants. All generated locally (~1.2 GiB regenerable scratch, no downloads); outputs verified deterministic on re-run.
Oracle rows are marked `[oracle]` (Parquet boundaries come from pyarrow's footer); the SST/Zarr-shard/SQLite scanners are lab code, not the promoted parser.

## Phase-1 promotion table

| format | CDC misses material reuse? | native boundaries stable? | exact parser plausible? | first-sight overhead | range-read | complexity | decision |
|---|---|---|---|---|---|---|---|
| SQLite main file | **yes**: sparse update 3.0M vs 0.3M, VACUUM 10.5M vs 0.76M | yes (fixed pages, 100 B header) | **yes** (done, ~350 lines) | +4.8K packed (+0.04 %), +0.8 % unpacked | better: 6.2 KB vs 117 KB chunk | low | **PROMOTE** |
| LevelDB / RocksDB SST | yes, hugely: rebuild +2000 keys 12.0M vs 230K logical; RocksDB compaction shares ~4 % | **no**: blocks shift after any insert/delete; RocksDB entries carry compaction-dependent sequence numbers | only with exact C++-snappy + builder reproduction | blocks +9 % | n/a | very high | **CHARACTERIZE-ONLY** |
| Parquet | yes, rewrite classes (row-group resize 7.9M, zstd 10.2M vs 0 logical) | chunk yes, **page contents move** | Thrift + page re-encode | chunk oracle −3 % | n/a | high | **CHARACTERIZE-ONLY** |
| Zarr v2 / unsharded v3 | no: whole chunk = object; CDC ≈ logical | yes (objects) | n/a — nothing to parse | 0 | native | none | **KILL** (control answered) |
| Zarr v3 sharded | little: 220K vs 66K for one 64 KiB chunk; 156K vs 61K zstd | yes (shard index) | yes, small | 0 | native | low | **CHARACTERIZE-ONLY** (gain ≤ ~150 KiB per changed chunk) |

## SQLite results (promoted; every row exact — see Exactness)

Cost of the revision (packed bytes). `tree-64` = `sqlitez3` (64 pages/node, head split); `flat 4k` = fixed-4k = one reference per page.

| variant | pages touched | CDC | flat 4k | tree-64 | CDC / tree |
|---|---:|---:|---:|---:|---:|
| 1 row updated | 0.0 % | 25K | 173K | **9.1K** | 2.8× |
| 10 / 100 / 1000 sparse rows | 0.2 / 2.1 / 17.5 % | 363K / 3016K / 7733K | 192K / 379K / 1962K | **46K / 309K / 1918K** | 7.9 / 9.8 / 4.0× |
| append 600 rows | 7.0 % | 6467K | 769K | **724K** | 8.9× |
| 16 B edit in 512 KiB blob | 0.1 % | 117K | 179K | **17K** | 6.9× |
| VACUUM (same logical content) | 27.9 % | 10.5M | 4829K | **755K** | 14× |
| delete 10 % rows + VACUUM | 99.8 % | 11.0M | 11.1M | **7257K** | 1.5× |
| PRAGMA user_version | 0.0 % | 12K | 171K | **4.8K** | 2.5× |
| **negative:** delete 10 % rows, no VACUUM | 78.7 % | 7422K | 7403K | 7358K | 1.0× |
| **negative:** page_size 4096→8192 | 100 % | 11.8M | 11.8M | 11.7M | 1.0× |
| 10-revision app history, total | — | 37.9M | 17.6M | **15.5M** | 2.4× |

Fan-out 16/64/256 changes small edits by ≤2.1× (m01: 13K/9.1K/19K); 64 is the default. The head split alone takes VACUUM from 4829K (flat) to 770K.

## Reads, admission, crossover

- One page: recipe (resident, 2.5 KB for 20 MiB) + 1 node + 1 page = **2 components, 6.2 KB** (CDC: one 117 KB chunk). Whole file: 4,942 components vs 310 chunks
  (**16× more objects**), decode 583–608 MB/s, encode 620–865 MB/s, working set 2.9 MiB decode / 0.04 MiB encode. Recipe 2,475 B per 20 MiB.
- Tree ≤ CDC in every measured cell (worst: +0.04 % at first sight; best 14×); gain vanishes only when every page changes (page-size change, 100 %).
- Admit when the object is a valid SQLite main file ≥ ~256 KiB and will be revised; first-sight premium is paid back by the first revision touching < 70 % of pages.
  Keep CDC for: invalid/unsupported files (exact fallback, tested), tiny databases, wholesale rewrites, and read paths dominated by whole-file fetches where 16× component count
  matters. Per-component store overhead is **not** modelled here — it is the main open cost of the tree.

## Exactness and portability

`sqlitez3.Encode` → serialised recipe + node/page components → `Decode` from the recipe bytes and stored components only; length, SHA-256 and bytes compared for every
non-oracle SQLite row (0 failures in 17 states + the 10-revision chain; non-SQLite falls back to CDC). Tests: 3 real DBs (1 K/4 K/64 K pages) × 5 fan-outs, odd tail, moved
chain shares bodies, every truncation and byte mutation of the recipe, tampered/missing components, hostile declared size (no allocation from it), short/over-declared sources,
sink/writer failures, no read past `size`; one 25 s fuzz smoke (3.3 M execs, clean). Gitz3 idx: 3 new ReaderAt tests (equivalence, short/bounded, hostile count).
Portability: stdlib only, no `lab`/`fstate` import, `io.ReaderAt`+size in, `io.Writer` out, checked arithmetic, recipe bytes are real. Debt: two-level tree only (>100 GB needs a third
level), `Getter` returns whole components (no ranged get), analysis hook for node/page classes lives in the lab adapter, lock-byte/pointer-map pages are treated as ordinary.

## Limitations and carry-forward (not pursued)

No public real SQLite family (local app-like history only). RocksDB structure unparsed (footer v6, xxh3, sequence numbers). Carry-forward: entry-level SST re-encoder (needs bit-exact
snappy), Parquet page re-layout, shard-index cut for Zarr v3, the same fixed-page tree for other paged files (LMDB, Postgres heap), WAL/journal semantics.

## Reproduce

```sh
sh z3/oracle/structured/setup.sh        # venv (needs libleveldb-dev); then run gen_sqlite.py gen_leveldb.py gen_parquet.py gen_zarr.py (+ gen_zarr3.py in a zarr 3 venv)
cd z3 && Z3_STRUCT_CACHE=… go run ./cmd/z3struct chars -lane all && go run ./cmd/z3struct sqlite && go test ./sqlitez3 ./gitz3
```
