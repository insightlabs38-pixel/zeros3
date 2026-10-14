# Z3-14 — CAR/IPLD content-addressed archive representation

Laboratory result. Nothing here touches `zeros3.go`, the store format, manifests, the journal or any persistent
format; all code is under `testing-harnesses/z3` (`carz3/`, `lab/carreps.go`, `lab/bytesrun.go`, `cmd/z3car/`).
Corpora are generated deterministically in memory; the real samples are pinned in `realworld/car.json` and fetched
on demand (`z3car fetch`), never committed (two ≤2 KB go-car fixtures live in `carz3/testdata`).

Question: when the workload already carries content identities (CIDs), can Z3 store the *semantic blocks*
and treat the archive serialisation (order, length prefixes, CIDs, roots, CARv2 wrapper/index) as a small exact
recipe — and does that beat byte-level CDC?

## What was built

| piece | what it is |
|---|---|
| `carz3` | stdlib-only bounded parser: unsigned varints (canonical only), CIDv0/v1 (`ParseCID`, `Verify` for identity, sha2-256/512, sha3-256/512; anything else is `not-verified`), the CARv1 DAG-CBOR header subset (either key order, exact header bytes kept), streaming section `Reader`, CARv2 wrapper (pragma, 40-byte header, padding, payload, optional index), sorted CARv2 index parser/builder (`IndexSorted` 0x0400, `MultihashIndexSorted` 0x0401), exact `Rep` (Build → Marshal/Unmarshal → Reconstruct/ReadRange) |
| errors | `MalformedError` (truncated / out of bounds / inconsistent) and `UnsupportedError` (well-formed but outside the subset: non-minimal varint, zero-length section, unknown header shape). Both mean *fall back to raw CDC* (`IsFallback`) |
| bounds | header bytes, roots, block count, block bytes, section length, total bytes (clamped to the real archive size), CID digest ≤ 64, varint ≤ 9 bytes, padding ≤ 1 MiB, index ≤ 1 GiB; blocks > 1 MiB are read with growth driven by data actually present, so a hostile length costs nothing |
| representations (`lab/carreps.go`) | `car-section` (structural cut: whole sections), `car-block` (block payloads keyed by SHA-256; CIDs/header/wrapper/index verbatim), `car-block+recipe` (sha2-256 CIDs *derived* from the component digest, CARv2 index *rebuilt* from the section list when it matches byte for byte), `…-i512` (blocks < 512 B inline in the recipe), `car-set` (sorted digest table of the distinct blocks as CDC components + a permutation: set and order separated), `car-auto` (admission: parse failure or mean block < 1 KiB → production CDC) |
| shared lab change | `Piece.Via`: a component reachable through another component of the same plan (a manifest lists its digest) costs no recipe reference. Additive; defaults to the old behaviour |
| corpus (`carz3/fam.go`) | 12 families × 2 scales (`big`: 120 sections of 16–192 KiB ≈ 10.5 MiB; `small`: 12 000 sections of 120–900 B ≈ 5.6 MiB) + a `tiny` scale for unit tests: `v1-reorder`, `v1-roots`, `v1-add`, `v1-remove`, `v1-overlap`, `v1-dup`, `v1-recodec` (same payloads under dag-pb / CIDv0 / sha2-512 / sha3-256), `v1-append`, `v1-mixhash` (sha2-256/512, sha3-256, a lab-unverifiable blake-class code, identity CIDs), `v2-wrap` (no index / MultihashIndexSorted / IndexSorted / padding / characteristics), `v2-reorder` (index regenerated, and once left stale), `v2-add` |
| real samples | ipld/go-car `v2/testdata`: `sample-v1.car` (479 907 B), `sample-wrapped-v2.car` (521 708 B, has an index), `sample-v2-indexless.car`, plus two malformed ones (`…zero-len-section`, `…tailing-corrupt-section`) used as fallback tests. SHA-256 pinned |

Cost metric used everywhere ("cost"): new component bytes + the serialised recipe under the per-object
digest-table layout of `lab/recipe.go` (32 B digest + varint length once per distinct component, one varint per
piece) + the plan's own extras (header, wrapper, CID prefixes / exact CIDs, kept index bytes). Every
representation is charged the same way; `cumulative` sums a family chain with one store per representation.
Every accepted row was reconstructed through the *serialised* recipe and compared byte for byte.

## 1. How much raw CDC misses block-level reuse

Production CDC (16/64/256 KiB) sees an archive as a byte stream. It recovers reuse only when whole runs of blocks stay
adjacent and in the same order.

| family (cost of the revision, % of its size) | CDC | `car-block+recipe` |
|---|--:|--:|
| big, same blocks reordered (r1/r2/r3) | 73.5 / 70.9 / 69.3 % | 0.04 % each |
| big, `v1-overlap` r2: shuffled slice of earlier blocks | 70.2 % | 0.04 % |
| big, `v1-recodec` (same payloads, other CID encoding) | 67–69 % | 0.04–0.12 % |
| small blocks (≈450 B), reordered | **99.0 %** | 7.6 % (`car-set`: **0.62 %**) |
| big, `v1-add` +1 appended | 1.31 % | 1.16 % |
| big, `v1-append` (new bytes extend the old bytes) | 27.7 / 16.5 % | 27.2 / 15.1 % |
| big, `v1-overlap` r1 (shared blocks stay contiguous) | 52.6 % | 52.3 % |

CDC is already as good as the CAR representation when the change is a pure append or a contiguous shared run; it
loses exactly when order, CID encoding or small-block granularity breaks byte adjacency. Small blocks are the
worst case for CDC (99 % on a reorder) because a chunk is larger than a block and block order is the whole signal.

## 2. Reorder / root / index change cost

Big blocks (revision ≈ 10.5 MiB, 122 sections, avg 90 KB), cost per revision:

| change | CDC | fixed-4k | car-section | car-block | car-block+recipe | car-set |
|---|--:|--:|--:|--:|--:|--:|
| shuffle / reverse / sort by CID | 69–74 % | 100.9 % | 0.08 % | 0.08 % | 0.04 % (4.5 KiB) | 0.00 % (0.3 KiB) |
| roots array changed / emptied / key order swapped | 0.88 % | 100.9 %/0.9 % | 0.08 % | 0.08 % | 0.04 % | 0.00 % |
| v2: add index (Multihash / IndexSorted) | 1.07 % / 0.24 % | 1.0 % | 0.13 % | 0.13 % | 0.04 % | 0.00 % |
| v2: 4 KiB + 512 B padding + index | 1.11 % | 1.0 % | 0.17 % | 0.17 % | 0.08 % | 0.05 % |
| v2: reorder, index regenerated | 70–71 % | 100.9 % | 0.13 % | 0.13 % | 0.04 % | 0.00 % |
| v2: reorder, index left **stale** (unrebuildable) | 1.13 % | 0.9 % | 0.08 % | 0.13 % | 0.09 % | 0.05 % |

Small blocks (revision ≈ 5.6 MiB, 12 001 sections, avg ≈ 450 B):

| change | CDC | car-section | car-block | car-block+recipe | …-i512 | car-set |
|---|--:|--:|--:|--:|--:|--:|
| reorder | 99.0 % | 36.4 % | 15.2 % | 7.64 % (437 KiB) | 36.0 % | **0.62 % (35 KiB)** |
| v2 add MultihashIndexSorted | 9.7 % | 41.2 % | 21.6 % | 7.06 % | 33.2 % | 0.57 % |
| v2 reorder, stale index | 2.2 % | 33.7 % | 21.6 % | 14.6 % | 40.8 % | 8.1 % |

Reading: with large blocks the reorder cost *is* the recipe, and the recipe is dominated by the digest table
(4255 of 4593 B): the same 122 digests are written again for every revision. `car-set` removes that by making the
sorted digest table a shared component and keeping only the permutation (245 B → 0.3 KiB total). With small blocks
the digest table is 408 KB (34 B × 12 001, 7.3 % of the data) and per-revision order costs 35 KB; there `car-set`
is the only representation that makes a reorder nearly free (0.62 %).

The CARv2 index is cheap to carry once understood: for every index the generator or go-car produced the index was
rebuilt from the section list **byte for byte** (`index mode: derived`, 0 stored bytes). Writers do not index
identity-hash CIDs; that rule had to be added to match the real go-car file. An index that does not match (the stale
case) or has an unknown codec is kept as exact bytes (4.9 KB big / 480 KB small).

## 3. Cross-archive overlapping-block reuse

`v1-overlap`, big, cumulative over 4 archives (41.6 MiB): CDC 73.4 %, `car-section` 53.9 %, `car-block+recipe`
53.9 % (the new blocks alone sum to 53.9 %). Per archive: r1 (50 % shared, contiguous) CDC 52.6 % vs 52.3 %;
r2 (every block already stored, shuffled) CDC 70.2 % vs 0.04 %; r3 (50 % new, mixed with old blocks in another
order) CDC 70.8 % vs 69.4 %. Small blocks: cumulative 81.2 % (CDC) vs 57.2 % (`car-block+recipe`) vs 57.2 %
(`car-set`). Reuse is exactly the intersection of block SHA-256s; the generator's ground truth
(`Rev.NewBlocks`) matches the measured new-component bytes.

## 4. CARv1 vs CARv2

A CARv2 file is `pragma(11) + header(40) + padding + CARv1 + padding + index`. In the Z3 recipe the wrapper costs
20–21 B (characteristics + empty paddings; the 40-byte header is *derived* from padding/payload size and only kept
verbatim when it is not derivable) plus exactly the padding bytes (4 631 B for the 4096+512 case) and 0 B for a
rebuildable index. Same payload as CARv1 vs CARv2 (`v2-wrap`): block bytes new = 0 in every variant; CDC pays
0.24–1.11 % per variant (and fixed 4 KiB pays 100 % whenever the wrapper shifts alignment by 51 bytes, e.g. r1 and
r5 of `v2-wrap`). Cumulative for six wrappings of one payload: CDC 17.4 %, `car-block+recipe` 16.7 % ≈ one payload.

## 5. Recipe / reference metadata cost

`car-block+recipe` recipe per archive (bytes, header + wrapper + CID prefix table + digest table + order):

| archive | blocks | digest table | order | exact CIDs | total | % of archive |
|---|--:|--:|--:|--:|--:|--:|
| big (10.5 MiB) | 122 | 4 255 | 245 | 0 | 4 572 | 0.04 % |
| small (5.6 MiB) | 12 001 | 407 939 | 35 878 | 0 | 443 889 | 7.6 % |
| small, sha2-512 CIDs | 12 001 | 407 939 | 47 879 | 816 068 | 1 271 980 | 20 % |
| real go-car sample (blake2b CIDs, 469 KiB) | 1 049 | 35 332 | 4 071 | 39 725 | 79 192 | 16.5 % |
| 1 GiB generated, 16 KiB blocks | 65 536 | — | — | 0 | 2 539 467 | 0.24 % |

A reference costs ≈ 34 B + ≈ 3 B of order per section. That is about what the CID plus length varint it replaces cost (37 B for a
sha2-256 CID): `car-block+recipe` r0 is 99.86 % of the archive at small scale while `car-block` (naive, CIDs kept
verbatim) is 107.4 %. A unique block is therefore close to cost-neutral at any size; the limit is the digest table,
i.e. the per-revision cost of a reorder or re-wrap, which only `car-set` removes.

## 6. CID / native identity usefulness

Across 48 archives (big scale incl. the two real samples: 7 623 sections, 501 MB) and the small scale (543 962 sections):

* native key → two different payloads: **0** (75 460 keys). Where the lab can verify the multihash it always matched.
* CID digest *equals* the ZeroS3 SHA-256 of the block (so the CID is a 4-byte prefix + the component digest): 5 135 of 7 623 (big), 503 462 of 543 962 (small). The rest are sha2-512, sha3-256, identity, or the blake-class code the lab cannot verify; those CIDs are carried exactly (36–68 B each).
* payloads known under more than one native identity: 122 (big, `v1-recodec`, 390 extra identities) and 12 001 (small, 38 403 extra). **A CID is not a payload identity**: the same bytes under dag-pb vs raw, CIDv0 vs CIDv1, sha2-256 vs sha2-512 are different CIDs. `car-section` (section bytes include the CID) pays 100 % for that; SHA-256 components pay 0.04–0.15 %.
* duplicate exact CIDs inside one archive: handled as repeated references (`v1-dup`: 0.00–0.04 %).
* real go-car files use blake2b-256: the lab reports 1 043 of 1 049 CIDs `not-verified` (an independent Python check confirmed all 1 043 digests are correct blake2b of their blocks) and 6 identity-hash CIDs as verified. Nothing was mis-interpreted; they are simply carried verbatim.

So native identity is useful as (a) an *equality hint / lookup key*, (b) a free-ish integrity check where the hash is
supported, (c) the thing that makes the CARv2 index rebuildable. It must not be the CAS key: ZeroS3 identity stays
SHA-256 of the exact component bytes, and a mismatching verifiable CID fails the transform (`TestBlockMalformed`).

## 7. Streaming and range implications

Streaming (generated CAR, sampled `HeapAlloc`, this machine):

| archive | stream: header → sections → SHA-256 → emit refs | peak heap | retained recipe build | peak heap |
|---|--:|--:|--:|--:|
| 1 GiB, 4 096 × 256 KiB | 976 MiB/s | 1.3 MiB (0.13 %) | 529 MiB/s | 3.6 MiB |
| 1 GiB, 65 536 × 16 KiB | 913 MiB/s | 3.6 MiB (0.35 %) | 461 MiB/s | 31.9 MiB |
| 256 MiB, 65 536 × 4 KiB | 850 MiB/s | 3.7 MiB | 418 MiB/s | 33.9 MiB |

Memory is bounded by the largest block when nothing is retained and by the *number of sections* (≈ 0.5 KB per
retained `Ref` in this unoptimised lab, GC slack included) when a recipe is kept — independent of archive bytes
(same 33 MiB for 256 MiB and 1 GiB archives with the same section count). Streaming reconstruction hashes to the
file's SHA-256 exactly (checked for both 1 GiB and 256 MiB runs).

Reads (CARv2 with index; lab, in-memory store):

| read one block by CID | metadata touched | extra stored |
|---|--:|--:|
| raw CDC (no structure) | whole archive | 0 |
| exact original index + walk to the offset | 442 B (big) / 18 445 B (small) | 4.9 KB / 480 KB |
| rebuild index from the recipe each time | 4.6 KB / 444 KB | 0 |
| ZeroS3 side index (digest → component ref) | **288 B / 540 B** | 4.4 KB / 432 KB |

The side index wins because the recipe's block table already has `(digest, length)`: a CID lookup is a binary
search plus one component fetch; the original index additionally forces an O(n) walk of the recipe to turn an offset
into a section (offsets are not stored). A production design would use the table itself, sorted, rather than an
offset index. Archive byte-range reads touch only the overlapping blocks (fetch = whole blocks; small blocks give
165 B for a 100-byte range, CDC fetches 119 KB; at big scale a block is larger than a CDC chunk so CDC fetches
less: 23.9 KB vs 109 KB); the recipe walk is O(n) in sections without offset checkpoints. Sequential reconstruct:
603 MiB/s (big) / 228 MiB/s (small), SHA-256 of the output included.

## 8. Exact reconstruction evidence

* Every accepted row of every family at every scale reconstructs byte for byte through the serialised recipe (`lab.TestCAR`; unit tests `TestRebuild` covers v1/v2 × inline 0/512 × CID variants × index derived/exact/unknown-codec/tampered).
* Real samples: `sample-v1.car` (1 049 sections), `sample-wrapped-v2.car` (index **derived** and byte-identical, 41 750 B), `sample-v2-indexless.car`, and the two go-car fixtures in `testdata` reconstruct exactly (`TestRealSamples`, `TestRealTiny`).
* 1 GiB / 256 MiB generated archives reconstruct to the same SHA-256 as the file.
* Fuzz smoke: `FuzzBuildReconstruct` (whatever `Build` accepts must reconstruct exactly through `Marshal`/`Unmarshal`, and every rejection must be a classified fallback): one 25 s run, 140 k executions, no failure; a second 12 s run to check an apparent start-up stall (240 k, no failure). `TestMutatedInputsAreFast`: 30 000 random mutations of valid archives, slowest 2 ms.

## 9. Admission / fallback behaviour

* Not a CAR, truncated, bad bounds, CID/payload mismatch under a verifiable hash, non-minimal varints: classified fallback; `car-auto` stores the object with production CDC and it reconstructs exactly (`lab.TestCARFallback`).
* Real malformed go-car files: `sample-v1-with-zero-len-section.car` → `unsupported: zero-length section`; `sample-v1-tailing-corrupt-section.car` → `malformed: truncated block`; both take the CDC path, exact.
* Unknown CARv2 index codec, an index that does not rebuild, unknown multihash: *not* rejected — kept as exact bytes / `not-verified`; only an index/offset we cannot validate safely would refuse.
* The static rule in `car-auto` (mean block ≥ 1 KiB → blocks, else CDC) is **not** the best rule. It is right for big blocks and for any reorder/rewrap-dominated workload at large blocks, wrong for small blocks where `car-set` wins reorders (0.62 % vs 99 %) but loses appends (`small-v1-add` +1: CDC 0.52 %, `car-set` 2.7 %, `car-block+recipe` 7.65 %). No static rule is right for both; admission needs the previous revision or a predicted-cost probe, as Z3-11 found for tar.

## 10. What carries into Z3-15

1. **Component = (native identity, SHA-256 of exact bytes, payload).** Native identity is an opaque hint; ZeroS3 identity is always the SHA-256. 0 conflicts in 75 k native keys, and where the lab could check a native hash it always matched.
2. **Derivations are encoding shortcuts, verified per instance and with an exact fallback** (CID = prefix ‖ sha256 when equal; index = f(section list) when byte-equal). The same pattern will apply to any native-hash structure.
3. **Set vs order.** The per-revision order list and the (sorted) digest set are separable; sharing the set as CDC'd manifest components is what makes reorder/re-wrap nearly free, at the price of an unstable permutation under insertion. Append-style growth wants an ordered list instead; the choice depends on the workload.
4. **`Via` components**: members reachable through a manifest/parent node need no recipe reference. In a Merkle DAG every non-root node is `Via` — this is exactly Z3-15's shape.
5. **Edges are already inside the payload** (the dag-cbor nodes list child CIDs); Z3-15 can treat them as native-identity edges and use equal hashes to skip subtrees instead of comparing bytes.
6. The exact-residual-plus-derived-parts shape (`header`, `wrapper`, `index`) is the same as the tar/ELF/PE residuals and needs no new storage mechanism.

## Not done / limits

* Synthetic payloads are incompressible random bytes; DEFLATE/gzip composition was not tried (CAR blocks are usually already-compressed file chunks).
* The real corpus is 3 small go-car files (all blake2b CIDs); no Filecoin/web3.storage-scale archives were fetched. The big-block conclusions rest on the generator, whose index and CID layout were validated against the real go-car files (index rebuilt exactly).
* Section offsets are recomputed by an O(n) walk; no checkpointed offset table, no sorted side index in the recipe format (measured as a lab experiment only).
* CBOR is the CAR header subset only; DAG-CBOR block payloads are opaque. No CAR-specific production work: no manifest field, journal record, pack version, store level, CID index or transformed read path.
* Only `IndexSorted` and `MultihashIndexSorted` are parsed; other index codecs are carried as opaque exact bytes.

## Tests actually run

Focused during development: `go test ./carz3` (varint, CID, header, block stream, malformed/bounds, CARv2, rebuild, range, real samples, mutation-speed), `go test ./lab -run TestCAR`. One smoke fuzz of the new CAR parser (25 s) plus a 12 s repeat. Checkpoint: `gofmt -l .` (clean), `go vet ./...` (clean), `go test ./...` once (all packages pass, 76 s). No DEFLATE, Safetensors, tar, ELF or PE fuzzing was rerun; root ZeroS3 tests were not run (`zeros3.go` and the root module are untouched).

## Reproduce

```sh
cd testing-harnesses/z3
go run ./cmd/z3car fetch                       # pinned real samples -> ~/.cache/z3-realworld/car
go run ./cmd/z3car families -scale big -detail # also: small, tiny; -only reorder
go run ./cmd/z3car real
go run ./cmd/z3car ident -scale small
go run ./cmd/z3car read  -scale big
go run ./cmd/z3car stream -mib 1024 -block 262144
```
