# Z3-15 — Merkle-guided state and blockchain revision representation

Laboratory result. Nothing here touches `zeros3.go`, the root module, manifests, the journal, pack/store formats or any
persistent format. Code: `merklez3/`, `srev/`, `eraez3/`, `lab/{merklereps,erareps,srevplan,bytesrun}.go`,
`cmd/z3merkle`, `cmd/z3era`, `cmd/z3sol`. Real artifacts are fetched on demand and pinned (`realworld/erae.json`);
nothing downloaded is committed.

Question: when a workload already carries content identities and graph structure, can equal hashes replace byte
comparison — and does one small shared shape express CAR blocks, Merkle nodes, EraE entries and state snapshots?

## What was built

| piece | what it is |
|---|---|
| `merklez3` | Engine over an **opaque** native identity (`ID string`) and ordered child lists. `Discover` (prune a subtree when its identity is known), `DiffAligned` (classic slot-aligned Merkle diff), `Walk` (full scan), optional `Verify` hook. The engine never hashes: hashing lives in the synthetic adapter (SHA3-256, SHA-512/256, SHA-256, truncated 16-byte) |
| synthetic state | Deterministic 16-ary trie over 64-bit keys, leaves ≤ 8 entries, node bytes = exact payload. Mutations: one/several clustered/scattered updates, insert, remove, subtree replacement, layout-only rewrite (encoding byte), rebalance (leaf capacity), merge-root DAG, shared-subtree DAG, fork graph. Exact snapshot archive (`Snapshot`/`ParseSnapshot`) for byte-exact reconstruction |
| `DiskStore` | node log + sorted fixed-width index searched with `ReadAt`: traversal memory independent of node count |
| lab representations | `merkle-flat` (every node a component, listed in the recipe), `merkle-guided` (root referenced, new nodes `Via` the root, native→SHA-256 index charged), `merkle-auto` (admission: guided unless verification fails or ≥ 30 % of nodes are new; a first revision is always accepted) |
| `eraez3` | Bounded e2store parser, Snappy block/framing decoder, **port of the reference Snappy encoder** (proves an entry canonical), RLP-delimited plaintext streams, derived component index, writer for re-exports |
| `srev` | The candidate shared shape (Component, Member{Via}, Revision{Roots, Bases, Members, Residual, Index, Rebuild}) and `lab.PlanFromRevision` |
| real artifacts | EraE: Sepolia epochs 0–3 (3.4 MB each) and mainnet epochs 0–1 (4.1/3.8 MB) from the ethPandaOps mirror. Solana: two testnet **incremental** snapshots (55/58 MB compressed, 5.8 GB decompressed each) |

Cost metric as in Z3-14: new component bytes + recipe under the per-object digest-table layout + the plan's own extras +
index bytes; "packed" applies the production pack rule (DEFLATE per component, kept if it saves 1/16). Every accepted
row was reconstructed from components and compared byte for byte.

## 1. Hash-guided comparison under sparse change

100 000 keys, 55 708 nodes (8.2 MiB snapshot), SHA3-256 native identities, **scattered** updates
(`z3merkle sparse -n 100000`). Work is what the traversal does; cost is the physical cost of the revision as % of the snapshot.

| keys changed | new nodes | guided: nodes read / hash compares | full scan: nodes read | aligned diff: nodes read | cost: CDC | flat | guided |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 0.001 % | 5 | 5 / 65 | 55 708 | 10 | 3.19 % | 23.58 % | **0.03 %** |
| 0.01 % | 38 | 38 / 449 | 55 708 | 76 | 18.71 % | 23.74 % | 0.21 % |
| 0.1 % | 295 | 295 / 3 137 | 55 708 | 590 | 66.87 % | 24.85 % | 1.51 % |
| 1 % | 2 148 | 2 148 / 18 513 | 55 708 | 4 296 | 99.60 % | 31.53 % | 9.57 % |
| 10 % | 13 233 | 13 233 / 63 985 | 55 708 | 26 466 | 100.06 % | 60.85 % | 47.15 % |
| 50 % | 39 458 | 39 458 / 69 921 | 55 708 | 78 916 | 100.06 % | 104.48 % | 110.32 % |
| 100 % | 55 697 | 55 697 / 69 921 | 55 708 | 111 394 | 100.06 % | 122.58 % | 140.51 % |

Clustered changes (consecutive keys): 5 / 10 / 58 / 548 new nodes at 0.001 / 0.01 / 0.1 / 1 %; guided cost 0.03 / 0.04 /
0.17 / 1.41 % versus CDC 3.9 / 4.4 / 4.6 / 5.0 %. At 10 % clustered the two meet (14.1 % vs 13.0 %).

* The work is exactly the new nodes: `NodesRead == new nodes` in every row, equal to a brute-force set difference of the
  two node sets (`TestSparseChange`, `TestInsertRemoveSubtreeReplace`). Identity comparisons are 9–13 per new node (the child edges of
  new nodes), ~0.1 % of a full scan at 0.001 %.
* `DiffAligned` reads twice the new nodes (both sides along the path) but needs no membership index.
* Dense change is the limit: at ≥ 50 % scattered the snapshot is simply new, hash guidance has nothing to prune and the
  native index makes it *worse* than raw (110–141 %). Admission has to see this (§7).
* Mutation catalogue (`z3merkle mutate`): identical rewrite 0 new nodes (0.00 %); one leaf 5 nodes; 10 inserts / removes
  41 / 40 nodes (0.07 %); subtree replacement 1/16 of keys 6.32 %, 1/256 0.39 %; rebalance (leaf capacity 8→6) 0.96 % of
  nodes; **layout-only rewrite** (same state, encoding byte changed) 100 % new — hash guidance is defeated by anything that
  changes node bytes, and guided costs 140 % there while CDC is 100 %.

## 2. Graph/state storage reused, and 3. fork cost

History `A → B → C → D`, `B → E → F`, plus `G` = merge of D and F (D's slots 0–7, F's 8–15), 100 000 keys each
(`z3merkle fork`): snapshot ≈ 8.2 MiB each.

| revision | CDC | merkle-flat | merkle-guided | guided: new nodes / compares / reads |
|---|--:|--:|--:|--:|
| A | 100.06 % | 122.60 % | 140.53 % | 55 708 / 69 921 / 55 708 |
| B (A + 1 % scattered) | 99.60 % | 31.54 % | 9.59 % | 2 151 / 18 561 / 2 151 |
| C (B + 0.1 % clustered) | 3.73 % | 23.68 % | 0.16 % | 54 / 129 / 54 |
| D (C + 10 inserts) | 17.94 % | 23.73 % | 0.21 % | 38 / 449 / 38 |
| E (B − 20 removes, fork) | 27.03 % | 23.88 % | 0.37 % | 68 / 785 / 68 |
| F (E + subtree 1/256) | 6.66 % | 23.97 % | 0.56 % | 215 / 305 / 215 |
| G (merge D+F) | 2.62 % | 23.57 % | **0.01 %** | 1 / 17 / 1 |

| storage for all 7 snapshots | KiB | % of raw |
|---|--:|--:|
| each snapshot saved raw | 58 731.9 | 100 % |
| CDC | 21 617.6 | 36.8 % |
| merkle-flat | 22 903.0 | 39.0 % |
| merkle-guided | 12 706.3 | 21.6 % |
| unique node bytes across all revisions (floor) | 9 066.1 | 15.4 % |

A fork costs only its divergent nodes (E: 68 nodes, 0.37 %); a merge of two known lines costs the new root (1 node, 0.7 KiB).
`merkle-flat` is the generic component CAS: the same bytes are stored, but every revision lists every node (34 B each)
and must read and hash all of them, so every revision costs ≥ 23.6 %. The gap between guided (21.6 %) and the floor
(15.4 %) is almost entirely the **native→SHA-256 index** (64 B per new node, 3.6 MB for A): see §6.

Native-hash independence (`z3merkle hashes`, 1 % scattered): SHA3-256, SHA-512/256, SHA-256 and a 16-byte truncated hash give
identical structural results (2 148 new nodes, 2 148 reads, 18 513 compares). Only the index differs: 137 472 B for 32-byte
ids that are not SHA-256, 103 104 B for 16-byte ids, **0 B when the native id is the SHA-256 of the node** (guided cost 7.97 %
instead of 9.57 %). A native hash equal to ZeroS3's own removes the lookup table entirely.

## 4. EraE structural and component reuse

Real files: `*.erae` from ethPandaOps. Layout (all 6 files): `Version | 8192 × Header | 8192 × Body | 8192 × SlimReceipts |
8192 × TotalDifficulty | Accumulator | ComponentIndex`; entries are e2store (type u16, length u32, reserved u16).

| sepolia-00000 (3 464 070 B) | entries | file bytes | share | plaintext bytes | distinct payloads |
|---|--:|--:|--:|--:|--:|
| Header (framed Snappy of RLP) | 8 192 | 2 415 414 | 69.7 % | 4 177 906 | 8 192 |
| Body | 8 192 | 237 568 | 6.9 % | 24 576 | **1** |
| SlimReceipts | 8 192 | 221 184 | 6.4 % | 8 192 | **1** |
| TotalDifficulty | 8 192 | 327 680 | 9.5 % | 262 144 | 8 192 |
| ComponentIndex | 1 | 262 176 | **7.6 %** | — | derivable |

* All 24 576 framed entries of each file are **reproduced byte for byte by the reference Snappy encoder** (my port of
  golang/snappy, verified against the library as well): the compression layer is canonical, so plaintext + "reference
  encoder" is an exact description. The component index (7.6 % of the file) is rebuilt from entry positions exactly in all 6 files
  (offsets are relative to the index entry; the only non-derivable field is the first block number, 8 B).
* Bodies/receipts of early Sepolia blocks are one repeated payload (8 192 identical entries); mainnet epoch 0 bodies have 1 296
  distinct payloads, epoch 1 541.
* `era-plain` represents a file as the layout (a repeated-pattern list, 80 B for the whole recipe), per-type plaintext streams
  cut with production CDC (headers 4 MB → 32 chunks), the derived index and the version/accumulator entries.

Cost per revision, % of file, raw components / with pack compression (`z3era families`):

| family | revision | CDC | era-entry | era-plain |
|---|---|--:|--:|--:|
| adjacent Sepolia epochs 0–3 (disjoint ranges) | each | 100 / 33–33.4 | 105 / 99.8 | 128–129 / **29.7–30.1** |
| mainnet epochs 0, 1 | each | 100 / 38.3, 37.2 | 106 / 101.8, 100.7 | 133 / **35.1**, 130 / **34.1** |
| re-export [4096,12288) | | 67.7 / 19.4 | 65.6 / 60.2 | 77.6 / 17.9 |
| re-export [6144,14336) (all blocks stored) | | 40.6 / 7.9 | 26.0 / 20.5 | 18.4 / **3.8** |
| re-export [0,8192), accumulator omitted | | 7.9 / 2.2 | 26.0 / 20.6 | **0.04 / 0.04** (1.3 KiB) |
| same blocks interleaved (Era1 layout) | | 100 / 33.0 | 25.1 / 19.4 | **0.04 / 0.04** |
| same blocks, no TotalDifficulty | | 13.2 / 2.0 | 17.6 / 13.1 | 0.04 / 0.04 |
| same blocks, uncompressed Snappy chunks (other producer) | | 100 / 21.4 | 97.3 / 62.6 | **0.02 / 0.02** (recognised encoder) |
| same blocks, index offsets shifted by 3 | | 7.9 / 2.2 | 26.0 / 20.6 | 7.6 / 2.2 (index kept verbatim) |
| same blocks, no index | | 0.4 / 0.1 | 20.0 / 20.0 | 0.04 / 0.04 |

Reading:

1. **Disjoint adjacent epochs share nothing** (different blocks): reuse is limited to the one-payload bodies/receipts, which cost
   less than a reference. The structural wins here come from *compression*, not reuse: plaintext header streams pack to
   29.7–30.1 % versus 33.0–33.4 % for CDC over the Snappy bytes — about 10 % relative. Snappy is weak enough that production DEFLATE over the framed
   bytes already recovers most of what plaintext exposes (plaintext headers: DEFLATE-9 984 KB, xz 909 KB for 4.18 MB; framed
   Snappy 2.35 MB; production DEFLATE over the whole framed file, via CDC chunks, 1.11 MB versus 1.01 MB for `era-plain`). `era-entry` pays 34 B per 289-byte entry and is a loss.
2. **Layout, index and producer normalisation are where the structure matters**: the same 8 192 blocks interleaved, without
   total difficulty, framed by another producer, or without an index cost ≈ 0 (the recipe, 1.3 KiB) with `era-plain`, versus
   25 % (per-entry components), 100 % (CDC on the interleaved layout), or 8 % (CDC: the new index entry).
3. **Overlapping ranges** reuse exactly the shared blocks (`[6144,14336)` 3.8 % packed, all of whose blocks were stored); edges of
   the run re-chunk (CDC cuts content-defined), and CDC alone does comparably when runs stay contiguous and grouped.
4. **Fallbacks are visible**: a producer the lab cannot reproduce (a valid framing no encoder here emits) keeps the entries
   verbatim (`TestRebuild/unknown-framing`), a non-derivable index is kept exact (the +3 row costs the 262 KB index again), and
   a non-EraE or malformed object is not accepted (`n/a`) and stays on CDC.
5. Reading one header: the original index gives a 310 B range read; `era-plain` needs the plaintext chunk(s) between a seek
   checkpoint (64 varints per header stream) and the item: **217 748 B on average** (chunks are 130 KB of plaintext), then
   re-framing the entry. Whole-file rebuild: 36 ms, 91 MiB/s including the Snappy encoder over 4.0 MiB.

The "another implementation" revision is synthetic (re-export of real components with other layout/framing/range) because only one
producer's files are published; the production framing was reproduced by the library encoder, so the claim that *this* producer is
canonical is measured, the claim about other producers is not.

## 5. Solana snapshot characterisation (testnet incremental pair)

Source: `api.testnet.solana.com/incremental-snapshot.tar.bz2` redirects to the current incremental snapshot; two taken ~4 minutes apart
(the node only serves the latest, so this pair is **not reproducible later**):

| | file | compressed | decompressed | sha256 (compressed) |
|---|---|--:|--:|---|
| A | `incremental-snapshot-452455563-452519665-F2zys8rB….tar.zst` | 54 896 525 | 5 828 427 776 | f76fbd072997e2fcd6647a724b2967785463494c14445fb34bd2c996a3ac1d17 |
| B | `incremental-snapshot-452455563-452520165-RLzpRuuE….tar.zst` | 57 725 220 | 5 823 561 728 | c501bac2fbc59f801cc8c50068b75d6d8a81275fa05ede381a341649f45e14ac |

Same base slot 452 455 563, B 500 slots later. (Full snapshots were measured with `HEAD` only: testnet 8.9 GB, devnet 67 GB, mainnet 124 GB;
not downloaded.)

Structure (`z3sol scan`): tar of `version` (5 B), `snapshots/<slot>/<slot>` (bank fields, 123 MB, 53.7 % zero bytes),
`snapshots/status_cache` (1.4 MB → 6.3 MB), and `accounts/<slot>.<id>` AppendVec files (8 552 / 8 577 files, 5.70 GB, 99.1 % zero bytes). Every AppendVec
parses cleanly with the 136-byte record header (StoredMeta 48 + AccountMeta 56 + hash 32): 95 413 records in A, 92 713 in B.
* 98.9 % of record bytes belong to one program owner: 512 accounts of up to 10 MiB, almost entirely zero (testnet load).
* The 32-byte per-record account hash field is **all zero in every record** (92 713 of 92 713): this format carries **no per-account native hash**.
  State hashes (bank hash / accounts hash) live in the manifest; they were not parsed.
* `(pubkey, write_version)` is a weak identity: of B's 84 887 keys, 84 870 occur in A, 82 277 byte-identical, **2 593 disagree**, and 10 543 / 7 826 keys repeat inside one snapshot.
* Compression layer: the `.tar.zst` is 174 consecutive 32 MiB zstd frames (window 512 KiB). Compressing the decompressed tar in 32 MiB slices with
  libzstd level 1 (python-zstandard) reproduces **both files byte for byte** (`inc1`, `inc2`). Not reproducible from the standard library; a pure-Go zstd was not tried.

A → B reuse, by level (`z3sol compare/cdc/estimate`):

| level | result |
|---|---|
| tar members (name + bytes) | 8 450 members / **97.3 %** of B's bytes identical to A; 129 new members 2.6 % (152.7 MB); 1 changed (status cache) |
| account records (SHA-256 of exact bytes) | 82 280 of 92 705 distinct records (**99.5 %**, 5.66 GB) already in A; 10 425 new records = 29.7 MB |
| raw CDC of the `.tar.zst` as stored | 38.1 % reused (zstd emits identical frames for identical zero runs); new 35.7 MB, pack-compressed **28.9 MB** |
| CDC of the decompressed tar | 89.9 % reused, new 588 MB (CDC mis-aligns data adjacent to long zero runs), pack-compressed **32.3 MB** |
| member + record representation | new records 29.7 MB → 5.4 MB packed; bank fields/status cache CDC 54.6 MB → 12.4 MB packed; 9 480 refs + tar headers 4.7 MB; **total 22.5 MB** |

So this snapshot pair hides **little extra reuse**: most of the 5.8 GB is zero-filled account data that zstd already collapses
100×; decompressing alone makes it worse (32.3 vs 28.9 MB); record awareness plus the exact zstd reproducer would save ~22 %.
That saving also needs libzstd for rebuilding. A full-snapshot pair (hundreds of GB) and the manifest-level state hash were not measured.

## 6. Whether native identities materially reduce analysis work

Yes where the source is random-access, no where the whole archive must be read anyway.
* Synthetic trie, disk-backed (`z3merkle scale`, 0.01 % of keys changed, scattered):

| keys | nodes | snapshot MiB | new nodes | guided: nodes read / compares / time | full scan time | traversal peak heap |
|--:|--:|--:|--:|--|--:|--:|
| 25 000 | 9 557 | 2 | 9 | 9 / 113 / 0.7 ms | 53 ms | ≈ 0 MiB |
| 100 000 | 55 708 | 8 | 36 | 36 / 417 / 4.4 ms | 387 ms | 0.05 MiB |
| 400 000 | 151 485 | 30 | 142 | 142 / 1 633 / 16 ms | 1.22 s | 0.24 MiB |
| 1 600 000 | 890 942 | 130 | 611 | 611 / 7 217 / 79 ms | 9.2 s | 0.96 MiB |
| 6 400 000 | 2 425 511 | 480 | 2 289 | 2 289 / 26 385 / 340 ms | 27.6 s | 3.08 MiB |

* A flat archive (EraE, a CAR, the Solana tar) must be read end to end regardless; native identities there save hashing and
  recipe size, not I/O. EraE indexes avoid scanning for seeks but not for ingestion.
* The cost of using native identity is a native→ZeroS3 index: 64 B per new node for 32-byte non-SHA-256 ids (3.6 MB of the 12.7 MB total for the fork graph), 0 B when the native id *is* the SHA-256.
* Solana gives the opposite answer: with a zeroed per-record hash and an unreliable `(pubkey, write_version)`, there is no usable native shortcut; byte hashes of exact records are the only equality test.

## 7. Memory / scaling and hash-guided admission

* Traversal memory is O(depth × fan-out + new nodes): max explicit stack 47–83 entries for 10⁴–2.4·10⁶ nodes, peak heap above baseline
  0.00–3.08 MiB (the `seen` set of new nodes). Node store memory is zero beyond file handles (log + sorted index; 1 `ReadAt` per index probe, ~22 probes at 4 M nodes).
  Building the lab corpus is not streaming-free: states of 6.4 M keys fit in memory (8 B/key + overrides).
* Admission data (`merkle-auto`): objects/nodes total, compares, nodes descended, bytes read, new physical bytes and recipe bytes are in §1–§2.
  Rule used: node components when verification passes and < 30 % of nodes are new (first revision always accepted), else CDC. On the
  synthetic chain, sparse revisions go guided and the dense revision (every key changed) falls back to CDC (`TestMerkleRevisions`). Crossover
  scattered: guided 47 % vs CDC 100 % at 10 %, 110 % vs 100 % at 50 %; clustered: 14.1 % vs 13.0 % at 10 %.
* Trust model: a node whose payload does not hash to its native id under the adapter's hash fails the guided transform
  (`TestVerifyRejectsTamperedNode`, `TestMerkleTamperedNodeFallsBack`: `merkle-auto` stores the tampered archive as CDC bytes, exactly). EraE
  `Build` reconstructs and compares before accepting; CARs (Z3-14) fail on a CID mismatch under a supported hash. Native hashes are used for
  pruning only; ZeroS3 identity is always the SHA-256 of the exact component bytes.

## 8. Partial-read implications

Disk-backed, 400 000 keys (151 485 nodes, 30 MiB) (`z3merkle partial`):

| read | nodes touched | bytes read | time |
|---|--:|--:|--:|
| one component by native id | 1 | 532 | 10 µs |
| lookup one key (root → leaf) | 6 | 2 630 | 61 µs |
| traverse a depth-1 subtree (~1/16) | 9 413 | 1.97 MB | 76 ms |
| traverse a depth-2 subtree (~1/256) | 564 | 123 KB | 4.5 ms |
| whole snapshot (DFS walk) | 151 485 | 31.5 MB | 1.24 s |

A node-per-component layout supports exact partial reads (the path to a key is 6 components) — but only through a native→component
lookup (the index above). Archive-shaped objects behave differently: CAR block by CID 288–540 B of metadata with a side index (Z3-14); EraE
header by block number ≥ 217 KB with CDC-sized plaintext chunks versus 310 B in the original file; a Solana record would need a
record index the format lacks.

## 9. Does one `StructuredRevision` primitive span these workloads?

`srev.Revision` {Roots, Bases, Members(Component, Via), Residual, Index, Rebuild} was implemented in code for CAR (block components, `TestStructuredRevision`) and the Merkle snapshot
(root + `Via` nodes, native index); the EraE and `car-set` plans were built directly as lab `Plan`s from the same ingredients (components, `Via`, residual bytes) and were not routed through `srev`.
`lab.PlanFromRevision` turns a Revision into the existing accounting. Which fields mattered:

| field | CAR | Merkle | EraE | state snapshot (synthetic) |
|---|:-:|:-:|:-:|:-:|
| Component SHA + size + data | ✓ | ✓ | ✓ | ✓ |
| Component.Native (opaque id) | ✓ (kept or derived) | ✓ (index key, pruning) | — (entries have none) | ✓ |
| Member.Via | `car-set` | ✓ (all non-roots) | — | ✓ |
| Residual (exact non-component recipe bytes) | ✓ header/wrapper | ✓ header | ✓ layout/index params | ✓ |
| Index (native→SHA) | lab side index only | ✓ | — | ✓ |
| Rebuild | ✓ | ✓ | ✓ | ✓ |
| Roots | recorded, not used by storage | recorded, not used | — | recorded |
| Bases (history edges) | — | — | — | **never used** |
| Edges | not stored in `srev`; read from the payload | adapter-internal (`DecodeKids`) | — | adapter-internal |

* **Spans**: components with SHA identity, an optional opaque native id, a `Via` flag, an exact residual and a rebuild function express all four.
* **Does not span, honestly**: (a) EraE's gain comes from a *transform* (plaintext streams, producer id, derived index) that creates the
  components; the format provides none, so the primitive only describes its output. (b) CAR "ordered refs" versus "set + permutation" is a
  recipe-shape choice inside `Residual`, not a field. (c) Derivations (CID from SHA, index from positions) and edge extraction are adapter code.
  (d) Solana has no native identity to carry. `Bases`, `Roots` (for storage) and `Edges` are unused by any accounting and should not be in the primitive.
  The minimal shape that earned its place is **Component (sha, size, data, optional native, class) + Member{Via} + Residual + Index + Rebuild**;
  everything format-specific stays in the adapter, as intended.

## 10. Recommendation for post-blockchain Z3 work

1. **Do not build a Merkle/DAG database.** The generic win is small and local: `Via` membership and a native→SHA index let a revision cost its changed
   components (fork 0.4 %, merge 0.01 %, sparse 0.03–1.5 %) instead of 24 % for a flat reference list. Promote only: component refs with `Via`, derived
   fields with exact fallback, the recipe layouts already in `lab`. Index costs (64 B/node) decide whether it pays; prefer native ids equal to SHA-256 or a compact (8+8 B) lookup.
2. **Next adapter pair worth testing**: (a) **Git packs / loose-object repositories** — the optional stretch was not run, but pack entries are DEFLATE-compressed objects with native SHA-1/SHA-256 ids and delta chains: it
   combines Z3-05 (DEFLATE producer reproduction) with the native-id/Via machinery measured here; (b) **OCI/Docker registry content** (manifests + layers) where
   digests are SHA-256 natively (index free) and layers are the tar+gzip stack already solved in Z3-11. Both have the property that made this batch work: native ids equal or near SHA-256, canonical producers, big shared subtrees.
3. Deprioritise: Solana-style snapshots without per-record hashes and dominated by zero-filled data (22 % saving needs an exact libzstd reproducer),
   and dense Merkle updates (≥ 30 % changed), where the admission rule should fall back to CDC.
4. The reusable code shapes to keep: producer-reproduction check (reference-encoder port proven against real files, then a recognised-encoder id), derived-field
   switches with exact fallback, `Via` accounting and the shared `RunBytesFamily` harness.

## Not done / limits

* Full Solana snapshots (8.9–124 GB) and the manifest state hash were not fetched or parsed; only the incremental pair above, which is not reproducible later.
* Only one EraE producer's files are published; the "other implementation" rows are synthetic re-exports of real components. Era1 (`0x3266` index, receipts type 5) is not parsed.
* The Merkle workload is synthetic (16-ary trie, SHA3/SHA-2 stand-ins); no Merkle Patricia/RLP trie or Ethereum state, no Keccak (not in the standard library).
* The guided representation counts the work against a random-access source; reading a flat archive file is not avoided. The disk-backed run (`scale`) is the real random-access measurement.
* The git-pack stretch was not run.
* No production code, manifest field, journal record, pack version, store level, CID/native index, DAG database or transformed read path was added or changed.

## Tests actually run

Focused during development: `go test ./merklez3` (sparse/dense/layout/insert/remove/cluster/subtree/merge/share/verify/snapshot/disk store), `go test ./eraez3`
(Snappy round trip/rejects, reference-encoder reproduction on a real file, rebuild matrix, bounds, real files), `go test ./lab -run 'TestMerkle|TestEra|TestStructuredRevision'`,
`go test ./cmd/z3sol`. One 20 s smoke fuzz of the new EraE/Snappy parser (`FuzzBuildReconstruct`, 728 k executions, no failure). No fuzzing of Merkle logic, CAR (beyond Z3-14), DEFLATE,
Safetensors, tar, ELF or PE. Final: `gofmt -l .`, `go vet ./...`, `go test ./...` once. Root ZeroS3 tests were not run (`zeros3.go` and the root module are untouched).

## Reproduce

```sh
cd testing-harnesses/z3
go run ./cmd/z3era fetch && go run ./cmd/z3era characterize && go run ./cmd/z3era families && go run ./cmd/z3era read
go run ./cmd/z3merkle sparse -n 100000; go run ./cmd/z3merkle mutate; go run ./cmd/z3merkle fork; go run ./cmd/z3merkle hashes
go run ./cmd/z3merkle scale -keys 25000,100000,400000,1600000,6400000; go run ./cmd/z3merkle partial -n 400000
# Solana: decompress both tar.zst with any zstd tool first
go run ./cmd/z3sol scan A.tar; go run ./cmd/z3sol compare A.tar B.tar; go run ./cmd/z3sol cdc A.tar B.tar; go run ./cmd/z3sol estimate A.tar B.tar
# zstd reproduction check: for each 32 MiB slice of the tar, zstandard.ZstdCompressor(compression_params=from_level(1, source_size=len(slice))).compress(slice)
# must equal the next frame of the .tar.zst (python-zstandard, libzstd).
```
