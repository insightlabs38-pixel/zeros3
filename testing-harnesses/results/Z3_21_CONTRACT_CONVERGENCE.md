# Z3-21 — Representation contract convergence (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journals, pack/store formats, read paths, GC/roots, native-ID indexes, descriptors). Code: `z3/repz3` (contract v2, stdlib only), `z3/repz3/kinds`, `z3/cmd/z3conv` (replaces `z3rep`). Not a frozen wire format.

**Question.** Can the Z3-20 contract settle GC reachability, native-index ownership, the compressed outer and scalable point reads cleanly enough to be the basis of a shadow-integration batch?

**Decision: PROMOTE** — meaning only: `repz3` may be exercised by a later shadow-integration experiment (no production format, migration, GC, index or read-path change is authorised).

## The four blockers

| blocker | result | evidence |
|---|---|---|
| A VIA / GC | **SOLVED: hybrid** — `Expand` for CAR, SQLite, Git, gzip; Merkle uses an *owning* record section; no sidecar on the wire | table below; mark = exact, deleting any read VIA/owned/table/rep object fails with `ErrMissing` after being listed |
| B native-index ownership | **SOLVED: rebuildable accelerator from live rep indexes** | Merkle chain: sections canonical, `Catalog` derived, rebuilt from 12 roots in 5 ms, equal, all revisions rebuild |
| C gzip outer | **SOLVED: closure-free exact gzip on repz3** | `KindGzip`: header + IR skeleton/units + trailer; plaintext = DERIVED child; gzip → tar → SQLite/CAR exact, depth 3 |
| D point reads | **SOLVED: decoder-side Git table** | top-level rep 19,695 B → 85 B; one object read 20,203 B → 593 B (3 objects) |

Generic contract changes (all small): version 2; `Index` → record section `{KeyWidth, Count, Ref, Owns}` (≤16, strictly ascending by digest, canonical); `Owns` = targets live and the section replaces `Expand`; `KindGzip`; default depth 3 (hard max 4); `Call.WriteMember/GetExact/GetAny/Source`; `ErrAmbiguous`. Removed: `NativeResolver` and all global state in decoding. Every other mechanism (Git table, gzip table, Merkle chain/compaction, multi-piece reads) is decoder-side.

## A. VIA / GC (full scale; one root; read = bytes of non-representation objects; sidecar = count | sorted ids, modelled as real objects)

| root | VIA | A Expand read / objs / time | B sidecar +store / objs / read / time | chosen |
|---|---:|---|---|---|
| CAR reorder | 122 | 4.2K / 1 / 0.07 ms | +3.9K / 1 / 3.9K / 0.02 ms | Expand |
| SQLite first sight | 3000 | 97K / 47 / 1.0 ms | +93K / 1 / 93K / 0.43 ms | Expand |
| Git +1 commit | 530 | 19K / 1 / 0.18 ms | +16K / 1 / 16K / 0.11 ms | Expand |
| Merkle 1-key revision | 6251 | 1878K / 6253 / 27 ms | +195K / 1 / 195K / 1.2 ms **per revision** | owning section: 390K / 2 / 1.3 ms, +0 B |
| gzip→tar→SQLite sparse | 1226 | 44K / 18 / 0.5 ms | +38K / 3 / 38K / 0.2 ms | Expand |

A sidecar is as large as the structure `Expand` reads (32 B/id vs the node/table bytes), so it only buys fewer objects, never fewer bytes, for CAR/SQLite/Git/gzip. For Merkle it adds 195K to a 2.2K revision (~90×); the node index each revision needs anyway (64 B/node) is already the exact VIA list, so `Owns` costs nothing and reads 4.8× less than `Expand`. Superset: Merkle keeps 4 replaced nodes of 6258 until compaction; everything else live == read.

## B. Native ownership (Merkle revision chain, 6251 nodes)

* A revision lists its own new-node section plus the sections of the revision owning each reused subtree, so it rebuilds with no global state (checked on a 71-revision chain, including every compaction: each rebuilds from only its own reachable set). Revision cost +256 B; first sight 390K (64 B/node).
* Bounded: >8 sections → leveled compaction (keep the base, rewrite the rest: 1.5K at rev 8); a full rewrite only when the base covers < ½ of the tree (both exercised).
* Restart: accelerator discarded, rebuilt from the 12 live roots + their objects: 6295 keys / 6315 records, 5 ms, ≈1.4 MiB heap, mappings equal, all revisions reconstruct; removing the newest root: 6295 → 6291 keys == rebuild from survivors; shared mappings stay (tests: refcounted owners, shared DERIVED children).
* Lookup of one node (5 sections): local sections touch 1,664 B; accelerator 0 B and names the owning rep. Same key → same component: any number of owners; different components: `ErrAmbiguous` (catalog and `Call.Resolve`), never a pick. Damaged/missing section: ingest fails, catalog unchanged.
* Controls: CAR needs no section (CID derives from the digest). Git SHA-1 ofs+ref repack of one history: 12/12 ids agree. Native ids stay hints (node recomputes its native hash; swapped records → `ErrMismatch`).

## C. Gzip (one layer: tar of conf, lib, text-row SQLite, CAR; Go gzip; bytes persisted per state)

| state | CDC | gzip exact | gzip+tar | gzip+tar+DERIVED |
|---|---:|---:|---:|---:|
| first sight | 1768K | 4522K | 4521K | 4553K |
| sparse DB update | 1693K | 1939K | 4084K | **189K** |
| VACUUM-like | 1536K | 3700K | 3753K | **178K** |

All 12 states reproduce the exact gzip bytes (header, DEFLATE bits, CRC/ISIZE trailer, length, SHA-256). Tests: levels 0/1/6/9/Huffman-only, name/comment/extra/mtime/OS headers, FHCRC, non-tar plaintext, empty; rejected → CDC: two members, trailing bytes, bad CRC/ISIZE, reserved flags, corrupt DEFLATE, ratio > 128. First sight loses 2.6× (inflated bytes are stored uncompressed; component compression not modelled).
Perf (4.3 MiB inflated, 69 blocks, 166 units, skeleton 4.1K, table 9.8K, rep 138 B): analysis 0.92 s = 4.6 MiB/s inflated (1.9 of gzip); rebuild 0.87 s = 4.9 (2.0); peak heap +26 MiB; a whole-layer rebuild touches 1256 objects / 4.5M. Single-digit MiB/s as before; not optimised.

## D. Git point read

| read | top rep | index | table | stream | total | objs |
|---|---:|---:|---:|---:|---:|---:|
| Z3-20 flat member list | 19,695 | 364 | 0 | 144 | 20,203 | 3 |
| Z3-21 single-component object | 85 | 364 | 0 | 144 | 593 | 3 |
| Z3-21 6 MiB blob in 60 pieces | 85 | 468 | 19,129 | 4.13M | 4.15M | 63 |

The table (gitz3 meta + stream list, 18K, CDC-chunked) is read whole by a pack rebuild and by `Expand`; a single-component read never loads it (265 of 266 non-delta objects in the test chain). Whole-pack rebuild unchanged; the bytes only moved: table 18K + index 22K + rep 85 B vs the old 19.7K rep + index 22K.

## Exactness and accounting

Matrix (CAR 3, SQLite 3, Git 4, Merkle 2, tar+SQLite 2, gzip 3 rows): all exact; reads ⊆ live, live − reads = only index/owned objects (Git index 1; Merkle 4), unexplained 0. Cost is persisted bytes by class (payload / rep / table / index / per-object); dependency sidecar bytes = 0 (not adopted). Overhead sensitivity, cumulative candidate/CDC at 0/32/64/128/256 B: SQLite 65.2→69.1 %, Git 86.3→88.9 %, Merkle 69.4→126.4 % (all within 0.4 points of Z3-20: reachability choices add no objects), gzip compound 98.5→105.6 % (first-sight dominated; sparse wins < 22 KB/object, vacuum < 10 KB).

## Production portability

* **Largely unchanged:** `repz3` core, kind decoders, `ir.ReconstructStream`, CAR/SQLite/Git/Merkle mechanisms.
* **Store wiring needed:** `Source` over the CAS (**two concurrent readers** — gzip decode streams its child through a pipe), put-time `Catalog.Ingest`, GC calling `ReachableAll`, ranged reads in legacy codecs.
* **Lab-only:** `MemStore`, `Admit`, synthetic SQLite/tar/Merkle states, `StudyReach`, `z3conv`.

## Limitations / carry-forward

Synthetic SQLite/tar/Merkle, one gzip producer (Go) here (the IR's earlier real-producer proofs are not repeated), single-member gzip only; SHA-256-hash Merkle keeps `Expand`. Carry-forward (one each): a multi-component Git read scans the table (O(pack); fixed-width table + ordinal would bound it) and delta-chain reads are unbuilt; Merkle index 64 B/node (+26 % at first sight; truncated hint keys would halve it); compress inflated gzip components before judging first sight.

## Reproduce

```
cd testing-harnesses/z3 && go run ./cmd/z3conv -scale full         # ~6 min; Git repack chain needs git
go test ./repz3/... && go test -race ./repz3/kinds -run 'TestGzipProgressive|TestGzipMalformed|TestGzipWithHeaderCRC' && go test ./repz3 -run '^$' -fuzz '^FuzzUnmarshal$' -fuzztime 25s
gofmt -l . && go vet ./... && go test ./...
```
Tests: golden v2 wire, obsolete/unknown version and kind, section order/flags, owning vs `Expand`, malformed sections and tables, ambiguity, catalog lifetime, mark/sweep, deletion per class, gzip malformed/writer-failure/goroutine leak, Git table cuts/flips; race clean on the gzip pipe path (progressive/malformed/header tests; the full `-run Gzip` race pass was too slow here); 25 s fuzz smoke 1.7 M execs.
