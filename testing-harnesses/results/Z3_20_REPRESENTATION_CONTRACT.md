# Z3-20 — Serialized representation contract (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journals, pack/store formats, read paths, GC/roots, descriptors, native-ID indexes).
Code: `z3/repz3` (contract, stdlib only), `z3/repz3/kinds` (kind table + thin adapters), `z3/cmd/z3rep`. **Not a frozen wire format and not a compatibility promise.**

**Question.** Can one small typed versioned serialized contract express CAR, SQLite, Git, Merkle and nested/DERIVED composition with exact closure-free reconstruction,
real accounting, bounded I/O, range reads and GC reachability, without format semantics in the contract?

**Decision: REVISE.** The shape is right and all five workloads migrated unchanged in their parsers; four concepts must be settled before shadow integration (section "Open").

## Contract shape (one rep object)

`"Z3RP" | version | kind | size | members | recipe | index refs`, deterministic, minimal varints only, consumed exactly. Integrity = the rep object is a SHA-256-addressed object (digest checked
before parse) + component length/SHA on every read + derived length/SHA + output length (SHA returned to the caller); no checksum field.
Primitives: **STORED** member (SHA-256, length) · **DERIVED** member (framed-output SHA-256 + length, child rep digest, frame = prefix bytes + zero suffix) · **VIA** (no wire form: the kind's `Expand`
finds components named only inside stored ones) · opaque `Recipe` · optional native **Index** (one type: key→SHA-256 records, stored as a component, binary-searched with ranged reads) ·
`Source{Size,ReadAt}` · finite kind switch (`Concat` built in; CAR/SQLite/Git/Merkle in `kinds.Lookup`) · depth ≤ 2 (hard max 4) · `Reachable`/`Account`. No bases, roots, inline pieces, closures or maps.

## Coverage (first stage, full scale; every concept is used by ≥2 formats or stays in a recipe)

| concept | CAR | SQLite | Git gp-zlib | Merkle | tar+SQLite+CAR |
|---|---:|---:|---:|---:|---:|
| STORED members | 1 table chunk | 47 nodes | 518 zlib streams | 1 root | 24 |
| VIA objects | 122 blocks | 3000 pages | 0 | 6250 nodes | 1062 |
| DERIVED / nested child | – | – | – | – | 2 / 2 |
| framing | – | – | – | – | 292 B (pad) |
| format recipe | 318 B | 11 B | 1747 B | 34 B | 209 B |
| native index | none (CID from digest) | – | 452 × 52 B | 6251 × 64 B (0 for SHA-256 ids) | – |
| ordered membership | order in recipe | node order | pack order | DFS (derived) | tar order |
| range read | block: table + block | page: node + page | object: index + 1 stream | whole archive only | inner page |
| reachability | Expand (table) | Expand (nodes) | members only | Expand (graph) | 3 rep objects |

## Exactness and cost (bytes persisted; candidate vs CDC expressed in the same contract; store holds earlier states)

All 25 stages: serialize → discard analysis → reconstruct from rep + decoder table + `Source` → length, SHA-256, bytes equal; objects read == objects `Reachable` lists (index components excepted); cost == bytes stored.

| state | candidate payload + recipe + index (objs) | CDC (objs) |
|---|---:|---:|
| CAR first / reorder / +1 block / v2 wrapper | 10.5M+0.4K (124) / 0.4K (1) / 125K (3) / 0.4K (1) | 10.5M+5K (152) / 7.9M (96) / 136K (4) / 88K (2) |
| SQLite first / 40 sparse / VACUUM / wholesale | 11.8M+1.7K (3048) / 218K (69) / 87K (44) / 11.8M (3048) | 11.7M (202) / 2.8M (38) / 10.5M (175) / 11.7M (194) |
| Git first / OFS→REF / reversed / +1 commit | 4.56M+19K+22K (520) / 22K (1) / 9K+41K (101) / 11K+42K (20) | 4.57M (72) / 138K (5) / 432K (8) / 335K (7) |
| Merkle first / 1 key / 10 keys | 1.49M+390K idx (6253) / 2K+0.3K (6) / 13K+2K (34) | 1.50M (23) / 207K (4) / 1.03M (15) |
| layer first / DB sparse | 4.63M+1.2K (1089) / 148K+1K (46) | 4.60M (67) / 1.50M (21) |

## Per-object physical overhead (cumulative candidate / CDC, % ; "wins <N B" = crossover per new object)

| workload | 0 B | 16 | 32 | 64 | 128 | 256 | wins while overhead below |
|---|---:|---:|---:|---:|---:|---:|---|
| CAR (3 families) | 58–98 | = | = | = | = | = | always (fewer objects than CDC) |
| SQLite | 65.2 | 65.5 | 65.7 | 66.2 | 67.1 | 69.1 | 2.4 KB cumulative; sparse 86 KB; VACUUM always; first sight never |
| tar+SQLite+CAR | 78.4 | 78.7 | 78.9 | 79.5 | 80.6 | 82.7 | 1.3 KB; DB update 55 KB |
| Git repack chain | 86.4 | 86.5 | 86.7 | 87.0 | 87.6 | 88.9 | 1.4 KB; reorder 4 KB; +1 commit 22 KB; first sight never |
| Git committed fixtures (2 KB) | 89.7 | 94.3 | 98.7 | 107.2 | 122.7 | 148.8 | 36 B (tiny packs lose) |
| Merkle (SHA3 ids) | 69.4 | 72.9 | 76.5 | 83.7 | 98.0 | 126.4 | **137 B**; 1 key 105 KB; first sight never |

## Range reads, reachability, execution

* One read = rep object (always read whole to check its digest) + parts: SQLite page 3 objects/6.2 KB comps (+1.7 KB rep); CAR block 3 objects/97 KB (4 KB table + 93 KB block);
  Git object 3 objects/144 B comps + 364 B index probes but **19.7 KB rep** (flat member list); compound inner page 5 objects/6.2 KB (+1.2 KB recipes). Git delta objects: base chains not implemented.
* Delete each class of child-only dependency (VIA page, STORED child chunk, child rep) → walker listed it, rebuild fails `ErrMissing`; depth 3 → `ErrDepth` (opt-in `MaxDepth:3` works); cycles/self-reference rejected.
* If VIA were on the wire it costs +33 B/object: SQLite +99 KB (0.8 %), CAR 4 KB, compound 35 KB, Merkle +206 KB (14 %). Walking via `Expand` reads stored nodes: SQLite 101 KB/0.6 ms, Merkle whole graph 6.2 MB/25 ms.
* Reconstruct 290–390 MB/s (Merkle 56 = SHA3 per node); SHA-256 = 1250 MB/s. Versus the unverified pre-contract path 2.1–3.3× (Merkle 8.5×); versus the *same path plus the same SHA-256 checks* 1.07× CAR/Git, 1.7× SQLite, 2.1× Merkle
  (per-component copy/alloc through `ReadAt`). Marshal/Unmarshal 0.16–2.1 GB/s, ≤ 20 KB objects. Peak heap +5–28 MiB for 1.5–11 MB outputs (garbage included).

## Open (why REVISE) and rejected concepts

1. **VIA/GC**: GC must run kind `Expand` over stored bytes (Merkle reads the whole graph) or pay the wire form above; decide per kind. 2. **Native index ownership**: Merkle reuse needs a global native→SHA resolver
(`NativeResolver`; a bad record is caught by the node's native hash, a missing one is `ErrMissing`); persistence policy (390 KB at first sight = +26 %) and entry lifetime vs GC are undecided.
3. **Gzip outer**: only uncompressed tar is on the contract; the DEFLATE skeleton of real OCI layers still lives in Z3-17 closures. 4. **Flat member lists** make point reads O(rep size) (Git 19.7 KB); hierarchy stays decoder-side (SQLite nodes).
Rejected: `Bases`/`Roots` (no workload needs them: Merkle reuse is a global index, Git bases are recipe content), inline pieces, per-member native ids, an output SHA in the rep, a checksum field, VIA wire entries.

## Production portability

* **Largely unchanged**: `repz3` core (rep, Marshal/Unmarshal, limits, Source, Reconstruct, Reachable, Account), kind decoders, CAR/SQLite/Git/Merkle mechanisms (changes: `sqlitez3.Components`, `gitz3` first-piece list, Git recipe now rejects trailing bytes).
* **Store/CAS wiring**: `Source` over the CAS, global native index + put-time ingestion, GC calling `Reachable`, bounded `Get` (whole components ≤ 64 MiB) replaced by ranged reads in the old codecs.
* **Lab-only**: `MemStore`, `Admit` (per-object-overhead policy, first-sight rule), synthetic SQLite/tar/Merkle states, `z3rep`.
* **Redesign remaining**: exact-DEFLATE throughput and gzip outer, duplicate hashing in legacy decoders, first-sight premium (SQLite/Git/Merkle never win first sight), Git base-chain reads, index persistence.

## Limitations

SQLite and tar states are synthetic (byte-level; `ps4096.sqlite` fixture too small for costs); Git needs the optional git 2.43 producer for the 4-state repack chain (fixtures committed); one producer, SHA-1 costs only; no real OCI/gzip layers; Merkle is the synthetic trie; single-threaded.
Analysis falls back to CDC (`Admit`: wholesale SQLite, junk input); decoding never does (damaged/missing component → `ErrMismatch`/`ErrMissing`).

## Reproduce

```
cd testing-harnesses/z3 && go run ./cmd/z3rep -scale full            # cost, sens, reads, cover, perf (~25 s; Git chain needs git)
go test ./repz3/... && go test ./repz3 -run '^$' -fuzz '^FuzzUnmarshal$' -fuzztime 25s
gofmt -l . && go vet ./... && go test ./...
```
Tests: golden wire fixture, every truncation, malformed/overflow/limit headers, short source, writer failure at every point, wrong length/bytes/missing, wrong child hash/length, depth/cycle, kind-recipe cuts, Merkle index tampering; 25 s fuzz smoke (2.9 M execs) clean.
