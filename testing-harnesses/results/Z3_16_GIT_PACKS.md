# Z3-16 — Git packs and loose objects

Laboratory result. Nothing here touches `zeros3.go`, the root `go.mod`, manifests, the journal, pack/store formats, persistent
object formats, read paths, GC/root handling, descriptors or native-ID indexes. All code is under `testing-harnesses/z3`
(`gitz3/`, `cmd/z3git/`, `oracle/gitdelta/`, `realworld/git.json`). The Z3 module is still stdlib-only. The installed `git` (2.43.0,
zlib 1.3) is an optional **producer**; Git's `create_delta` is an optional **oracle** (fetched and compiled outside Git). Neither is
needed to build, test, reconstruct or persist anything. No result here is permission to productionize.

Question: Git's physical packs churn (order, zlib bytes, delta bases) while the object set barely changes. How much of that churn can
ZeroS3 remove while still reproducing the exact bytes of the repository files it represents, and where does it stop paying?

## What was built

| piece | what it is |
|---|---|
| `gitz3` parsers | loose objects (zlib via `deflate.ParseZlib`, canonical `<type> <size>\0` header, declared size, SHA-1/SHA-256 id verified); pack v2/v3 (`PACK` header, count, full / `OFS_DELTA` / `REF_DELTA` entries, zlib boundaries found by inflating once, Adler-32, entry CRC, trailer checksum); Git delta parser/applier; iterative delta resolver (depth-first from full objects, no recursion); pack index **v2** (fanout, ids, CRCs, 32/64-bit offsets, both checksums, `Lookup`, `BuildIdx`) and a small **v1** parser/builder for characterisation |
| bounds | object count (and `count <= bytes/9`), per-object size, total inflated bytes, delta depth, total delta output, checked offsets, a delta target must be producible from the delta (`<= 64 KiB` per delta byte) before anything is allocated, zlib streams never allocate from the claimed size, duplicate ids and unresolved bases are errors. Thin packs / REF cycles are `ErrThin` → ordinary CDC |
| tests (no git needed) | tiny committed fixtures (`gitz3/testdata`, ≈25 KB: SHA-1 and SHA-256 × {OFS, REF, v1-index} packs, thin pack, one loose object of each type): parse, resolve, id of every object, idx byte-derivable, every representation reconstructs; ~30 hostile pack cases (count, versions, entry types, size varint overflow, OFS zero / before header / mid-entry / endless varint, REF cycle, duplicate object, depth / inflated / delta-output limits, delta amplification, copy outside base, trailing bytes, inflate bomb); truncation at **every** byte of every fixture pack/idx/loose file; a byte mutation at every position (checksum repaired so the parser, not the trailer, must cope); recipe truncation/mutation for both layouts; 64-bit idx offsets |
| tests (git optional) | the producer pipeline loose → pack → reversed pack through the whole representation set in both object formats; identical commands reproduce identical pack/idx bytes; fixtures regenerate byte-for-byte with the recorded git version |
| fuzz smoke (run once, after the deterministic tests were green) | `FuzzParsePack` 20 s (6 157 execs, parse + resolve + `BuildIdx` + `ParseIdx` round trip), `FuzzDelta` 8 s (200 011), `FuzzParseIdx` 8 s (279), `FuzzParseLoose` 6 s (305 387): no failure. Exec counts are low for the pack/idx targets because the sandbox fuzz workers are slow, not because inputs were rejected early |
| producer harness (`producer.go`, `scenarios.go`) | controlled environment (`HOME`, `LC_ALL=C`, `TZ=UTC`, `GIT_CONFIG_NOSYSTEM`, fixed author/committer/date, `-c pack.threads=1 gc.auto=0 pack.writeReverseIndex=false`), `fast-import` streams from a seeded generator, repack settings (window, depth, compression level, `-F`, REF/OFS, no-delta, reversed object list), loose via `unpack-objects`, a command log for provenance |
| families (`scenarios.go`) | common 24-revision history (24 source files, a 256 KiB text, a 1 MiB binary-like file, 60 tiny files; ≈0.9 MiB pack) then the final revision: add small file; edit text near begin / middle / end; sparse binary edit; rename; duplicate blob; 300 tiny adds + 30 deletes; tree restructure; plus a branch/fork/merge history. Scenarios: loose → pack, repeat, reorder, window/depth, compression level only, OFS → REF, one small revision (full `-f`, two packs, consolidation), five-step gc chain |
| representations (`rep.go`, `setperm.go`, `loose.go`, `wholeplan.go`) | `raw`, `cdc` (production CDC v1 copy), `fixed-64k`, `fixed-4k`, `cdc-snapshot` (framed state as one object) — and `gp-entry` (exact entry bytes as components), `gp-zlib` (zlib streams as components; type/size/OFS/REF/header/trailer derived), `gp-plain` / `gp-auto` (plaintext components: canonical object bytes or delta program, plus exact DEFLATE reconstruction through `ir`; `auto` keeps the zlib bytes when plaintext+reconstruction costs more than 1.1x), `gp-set` / `gp-set-plain` (set + permutation layout), `.idx` regenerated from the pack (one flag byte when byte-identical), loose objects (plain mode: canonical bytes are the component, path derived from the id), and the oracle `floor-native-obj` |
| exactness | every non-oracle row serialises its piece list in the lab's table layout (checked: the bytes equal the accounted size), deserialises it, and rebuilds the original file from stored components only; length, SHA-256 and bytes compared. **0 inexact rows** in every run reported below |
| `oracle/gitdelta` | Git's `create_delta` (from `diff-delta.c` at tag `v2.43.0`, SHA-256 pinned in `setup.sh`; other tags selectable) behind a pipe protocol. GPL source is fetched into `~/.cache`, never committed |

Cost metric as in Z3-14/15: new component bytes + the serialised recipe (table layout: 32 B digest + varint length once per distinct
component, one varint per reference; **bytes actually produced** by `MarshalPieces`) + the plan's own meta + index bytes. The
headline number is the **production pack-rule** cost (new components DEFLATE'd, kept if it saves 1/16); the unpacked table cost is in
parentheses in the raw output. "A → B" always means: the cost of storing B when A, and everything before it in the family, is already
stored under the *same* representation; a file with an unchanged name (= content hash for packs) costs nothing.

## Evidence classes

| class | here |
|---|---|
| deterministic synthetic | every `matrix`, `probe`, `mapping`, `reads`, `scale`, `derive` row on seeded families (seed `20260616`), git 2.43.0 |
| real, reproducible | lua/lua (1 576 commits reachable, tip `b35736b9d71…`), ocornut/imgui (400, `c1aaac65e89…`), microsoft/vscode-codicons (823, `8853e0b1b0e…`), this repository (`25d7590726f…`): fetched by SHA with a shallow depth, **all packs produced locally** by one git (`pack-objects --threads=1`), never a server pack. SHA-1 only. Fetched 2026-10-14 |
| real but producer/toolchain dependent | the pack **bytes** (git 2.43.0 + zlib 1.3). A different git or zlib would produce different bytes; the structure results (churn classes) should carry over, the exact-DEFLATE success rate is only measured for this producer |
| synthetic re-export of real components | five F-Droid client APKs (12.5–13.3 MB, SHA-256 pinned in `sources.json`) committed one per revision of a single path. Says nothing about an independent Git producer |
| non-reproducible characterisation | none |

## 1. Producer reproducibility (not assumed)

`z3git probe`: the same command line, run twice on a freshly imported repository, reproduced the **pack and the index byte for byte in
14/14** configurations (7 settings × SHA-1/SHA-256). Settings: default `repack -a -d -f`; reuse (`-a -d`); window 3/depth 3; no-delta
(`--window=0`); `repack.useDeltaBaseOffset=false`; `-c pack.compression=1 … -F`; reversed object list through `pack-objects`. Pack size
and SHA-256 differ across settings (e.g. SHA-1 default 889 469 B `2e5ff7a3…`, reversed 889 688 B, level 1 930 850 B, no-delta 1 167 265 B).
Determinism is a property of *this* git, threads = 1, and the same environment; it was measured, not inferred from the deterministic commits.

## 2. How bad is raw CDC under a repack of the same object set? (Q1)

Cost of B given A, % of the pack size (synthetic mixed history, 0.87 MiB; SHA-1; SHA-256 within ±1.5 points):

| A → B (same objects unless noted) | CDC | gp-entry | gp-zlib | gp-set | gp-set-plain | floor (new objects only) |
|---|---:|---:|---:|---:|---:|---:|
| same command twice | 0 % | 0 % | 0 % | 0 % | 0 % | 0 % |
| reversed object order | **30.8 %** | 4.7 % | 2.8 % | 4.0 % | 4.6 % | 0 % |
| OFS_DELTA → REF_DELTA (headers only) | 14.7 % | 4.1 % | 2.0 % | 3.1 % | 3.0 % | 0 % |
| window 3 / depth 3 | 4.3 % | 4.1 % | 2.4 % | 3.0 % | 3.5 % | 0 % |
| window 50 / depth 250 | 4.2 % | 3.3 % | 1.7 % | 0.9 % | 1.1 % | 0 % |
| delta search off (a different, larger object representation) | 36.7 % | 26.4 % | 25.5 % | 27.7 % | 28.1 % | – |
| compression level 6 → 1 (`-F`) | **99.9 %** | 101.4 % | 100.0 % | 14.5 % | **1.5 %** | 0 % |
| compression level 6 → 9 | 100.1 % | 101.5 % | 100.0 % | 7.7 % | 2.6 % | 0 % |
| loose objects → first pack | **99.6 %** | 101.5 % | 100.0 % | 7.6 % | 5.8 % | 0 % |
| one small revision, full `-f` repack | 19.0 % | 3.3 % | 1.9 % | 1.2 % | 1.5 % | 0.2 % |
| the same, new objects left as a second pack | 0.2 % | 0.2 % | 0.2 % | 0.2 % | 0.2 % | 0.2 % |
| gc after each of 4 more revisions (per step, cumulative store) | 30.5 / 30.7 / 30.4 / 19.0 % | 4.3 / 4.8 / 5.1 / 3.3 % | 2.8 / 3.3 / 3.4 / 1.9 % | 2.6 / 3.4 / 4.0 / 1.2 % | 2.9 / 4.0 / 4.6 / 1.5 % | 1.8 / 1.7 / 1.7 / 0.2 % |

The same table for SHA-256 repositories is within 1.2 points in every cell (reorder CDC 31.6 %, `gp-set` 4.9 %, level 1 `gp-set-plain` 1.7 %, loose → pack `gp-set` 8.2 %).
Cumulative over the five gc states the store holds, in KiB: CDC 1 842, `gp-zlib` 973, `gp-set` 1 001, `gp-set-plain` 1 031 (the exact-layout row is 1.9× smaller than CDC after four extra revisions; it was 1.0× after one).

Real histories, one revision / ten revisions later (full local repack each time; pack sizes 2.29 / 2.45 / 3.53 / 2.22 MiB):

| repository | CDC 1 rev | CDC 10 revs | gp-zlib 1 / 10 | gp-set 1 / 10 | gp-set-plain 1 / 10 | floor 1 / 10 |
|---|---:|---:|---:|---:|---:|---:|
| lua (8 431 objects) | **77 %** | 89 % | 15 % / 24 % | 6.2 % / 21 % | 6.6 % / 22 % | 1.2 % / 15 % |
| imgui (2 616 objects) | 53 % | 74 % | 13 % / 37 % | 5.3 % / 22 % | 5.7 % / 23 % | 11 % / 42 % |
| codicons (7 153 objects) | 58 % | 69 % | 7.6 % / 10 % | 2.6 % / 8.9 % | 2.8 % / 9.8 % | 1.8 % / 15 % |
| zeros3 (1 243 objects) | 54 % | 94 % | 7.9 % / 66 % | 9.8 % / 51 % | 10.3 % / 51 % | 6.7 % / 61 % |

Cases where CDC is as good or better (not hidden): a pack that is only *appended* as a second pack (synthetic 0.2 % everywhere; tiny-churn family: CDC 46.2 K, `gp-zlib` 48.2 K, `gp-set` 55.5 K, `gp-set-plain` 64.2 K — 300 new tiny files pay a descriptor each); a sparse 40-edit rewrite of a 1 MiB binary blob, where nothing helps because every 64 KiB chunk is touched (CDC 878 K, exact 661–689 K, floor 649 K); a window/depth change that rewrites most deltas (lua w3d3: CDC 85 %, `gp-zlib` 73 %, `gp-set` 84 %); and every rewrite whose ordering changes little in a small pack (zeros3: `gp-zlib` 7.9 % beats `gp-set` 9.8 %).

CDC loses almost everything that ZeroS3 could reuse even though 89–99 % of the entry bytes in B are byte-identical to entries in A
(table in §3). A *single new commit* costs CDC 53–77 % of the pack for these repositories.

## 3. How much of the churn is ordering, compression, delta choice? (Q2)

Composition of B's entry bytes relative to A (`Attribute`; every entry of B is in exactly one class; "adjacency broken" = identical entries
whose predecessor in B is not their predecessor in A, i.e. what stops CDC):

| pair | identical entry | OFS-distance only | zlib bytes only | delta base/program | new objects | adjacency broken (of identical) |
|---|---:|---:|---:|---:|---:|---:|
| synthetic reversed order | 98.1 % | 0.6 % | 0 | 1.3 % | 0 | **98.7 %** |
| synthetic OFS → REF | 98.7 % | 1.3 % | 0 | 0 | 0 | 0 |
| synthetic level 6 → 1 / 9 | 0 | 0 | **100 %** | 0 | 0 | – |
| synthetic window 3 / depth 3 | 98.7 % | 0.6 % | 0 | 0.7 % | 0 | 0 |
| synthetic one revision (`-f`) | 99.6 % | 0.3 % | 0 | 0 | 0.1 % | 0.1 % |
| lua one revision | 88.9 % | **9.5 %** | 0 | 0.3 % | 1.3 % | 0.3 % |
| lua ten revisions | 70.6 % | 18.3 % | 0 | 1.4 % | 9.8 % | 3.8 % |
| lua reversed | 49.9 % | 32.8 % | 0 | 17.3 % | 0 | 80.4 % |
| lua OFS → REF | 51.5 % | 48.5 % | 0 | 0 | 0 | 0 |
| lua level 6 → 1 / 9 | 0 | 0 | 100 % | 0 | 0 | – |
| codicons one revision | 99.1 % | 0.4 % | 0 | 0.4 % | 0.1 % | 0.1 % |
| zeros3 one revision | 93.5 % | 0.5 % | 0 | 0.1 % | 6.0 % | 1.1 % |

Four distinct mechanisms, each answered by a different piece of knowledge (staircase on the same pairs, packs in % of pack size):

| mechanism | evidence | what removes it |
|---|---|---|
| **order** | reversed synthetic: CDC 30.8 % → `gp-entry` 4.7 % (entries as components, order in the recipe) → `gp-zlib` 2.8 %; lua reversed: 90 % → 59 % → 28 % | entry boundaries |
| **small dispersed header changes** | lua one revision: only 0.3 % of entry *bytes* are not identical, but 9.5 % are OFS-distance changes spread over the whole pack, and CDC pays 77 %. `OFS → REF` on lua changes **header bytes only** and costs CDC **75 %** (1 852 K of 2 455 K) against `gp-zlib` 18 %, `gp-entry` 58 % | deriving OFS distances (lua: `gp-zlib` 15 % vs `gp-entry` 31 %, which still keeps them inside the component) |
| **zlib bytes** | level change: 100 % of B's bytes differ only in compression; CDC 93–100 % of the pack, `gp-entry` and `gp-zlib` 100–104 % | exact DEFLATE (`gp-set-plain` 1.5 % / 2.6 % synthetic; lua 10–11 %; imgui 3.9–6.2 %; zeros3 2.9–3.5 %; codicons 6–8 %) |
| **delta base/program choice** | window/depth changes: 0.7–1.3 % of bytes differ; 17 % on lua reversed; 70 % on lua w3d3 | stored again as new programs; whether they could be derived is §6 |

So repack churn is not one phenomenon. For small-entry packs the dominant CDC loss is the *dispersion* of header edits, not reordering.

## 4. Does exact DEFLATE recover meaningful reuse? (Q3)

Reconstruction itself is solved for this producer: **every entry of every pack reconstructs exactly** through the existing Z3-05–08 machinery
(`ir`, Pred codec, auto profile; no new DEFLATE primitive was written, only adapter glue), including 12–13 MB APK blobs and loose
objects at level 1:

| pack | entries exact | zlib B ← plaintext B + skeleton B + units B | B / entry |
|---|---:|---|---:|
| synthetic | 382 / 382 | 887 228 ← 1 638 340 + 6 427 + 9 745 | 42.3 |
| lua T | 8 431 / 8 431 | 2 130 625 ← 4 369 526 + 135 883 + 33 523 | 20.1 |
| codicons T | 7 153 / 7 153 | 3 482 697 ← 6 988 027 + 119 712 + 60 006 | 25.1 |
| zeros3 T | 1 243 / 1 243 | 2 283 302 ← 6 417 927 + 21 755 + 38 309 | 48.3 |
| imgui T | 2 616 / 2 616 | 2 482 409 ← 7 895 828 + 43 314 + 50 153 | 35.7 |

The reconstruction metadata is 1.8–7.9 % of the compressed bytes (synthetic 1.8 %, zeros3 2.6 %, imgui 3.8 %, codicons 5.2 %, lua 7.9 % — tiny entries pay the fixed skeleton). Reuse when compression changes (cost of B as % of the CDC cost):

| event | CDC | `gp-set-plain` | `gp-set` (auto) | `gp-zlib` |
|---|---:|---:|---:|---:|
| synthetic level 6 → 1 / 9 | 99.9 % / 100.1 % | **1.5 % / 2.6 %** | 14.5 % / 7.7 % | 100 % / 100 % |
| lua level 6 → 1 / 9 | 93 % / 93 % | **10.0 % / 11.0 %** | 49 % / 51 % | 101 % / 101 % |
| imgui level 6 → 1 / 9 | 99.8 % / 99.5 % | 3.9 % / 6.2 % | 24 % / 18 % | 100 % / 100 % |
| codicons level 6 → 1 / 9 | 97.5 % / 97.4 % | 6.4 % / 7.9 % | 30 % / 27 % | 100 % / 100 % |
| zeros3 level 6 → 1 / 9 | 100.1 % / 99.1 % | 2.9 % / 3.5 % | 12.9 % / 8.2 % | 100 % / 100 % |
| APK pack (51.6 MB) level 6 → 1 / 9 | 100 % / 70 % (stored blocks are identical) | **0.12 % / 0.43 %** | 0.17 % / 0.49 % | 99.8 % / 70 % |
| loose → pack: synthetic / lua / imgui / codicons / zeros3 | 100 % | 5.8 / 71 / 24 / 39 / 11 % | 7.6 / 71 / 24 / 42 / 12 % | 100 % |

(The loose → pack rows also contain a separate effect: `gp-*` store the plaintext and the production pack rule re-compresses it at a
better level than Git's level-1 loose objects. That is a recompression gain, not reuse; it is why loose lua falls from 56.2 M to 45.8 M.)

Two things limit it. (1) The stable table and the reconstruction table must be separate: the first version of the set layout put the
skeleton inside the identity descriptor, so a recompression event rewrote the whole descriptor (≈ 80–90 B/entry); splitting them (stable ids/bases/digests vs
zlib-header/skeleton/units, same id-chosen section boundaries) took lua level 6 → 1 from 800 K to **249 K** and synthetic from 36 K to 13.7 K. (2) `auto`
decides per entry at first sight, with no foresight: tiny entries stay as zlib bytes, so a later recompression gets no benefit from them (lua `gp-set` 49 % vs
`gp-set-plain` 10 %). Plaintext-first costs 1.6–3 points more on the first ingest (§9). Loose → pack for delta-heavy packs reuses only the full objects: the pack's delta programs are new bytes (lua 71 %).

## 5. Delta programs: stability (Q4)

Delta entries present in both packs, how many keep their base **and** program bytes (`delta entries in both: same base / same program`):

| pair | synthetic | lua | imgui | codicons | zeros3 |
|---|---:|---:|---:|---:|---:|
| one revision, full repack | 147 / 147 | 6 468 / 6 479 (99.8 %) | 1 867 / 1 953 (96 %) | 3 356 / 3 358 | 494 / 541 (91 %) |
| ten revisions | – | 6 369 / 6 443 (98.9 %) | 1 707 / 1 849 (92 %) | 3 267 / 3 305 | 376 / 379 |
| OFS → REF, level change | all | all | all | all | all |
| window/depth 50/250 | 145 / 147 | 5 830 / 6 483 (90 %) | 1 797 / 1 963 (92 %) | 3 114 / 3 361 (93 %) | 529 / 559 |
| window/depth 3/3 | 102 / 138 (74 %) | 1 846 / 6 389 (**29 %**) | 795 / 1 924 (41 %) | 1 594 / 3 256 (49 %) | 332 / 529 |
| reversed input order | 71 / 126 (56 %) | 4 147 / 6 430 (64 %) | 1 100 / 1 911 (58 %) | 1 994 / 3 277 (61 %) | 393 / 539 |

Whenever a base is kept, **the program is kept too** (same base ⇒ same program in every row). Across adjacent revisions programs are 91–99.9 % stable;
the instability is base selection, which changes with ordering and window/depth. Program bytes are not always small (synthetic 29.8 KB inflated of 880 KB; lua 1.25 MB inflated of 2.1 MB compressed); a pack with the deltas switched off is a different, larger representation (synthetic: CDC 37 %, exact 26–28 % of the pack; lua/imgui: 94–98 % for every layout, because the resolved bytes are 20–70× the pack).

## 6. Can any producer or delta field be derived exactly? (Q7)

Derived and **verified byte-identical per instance** (anything else keeps the original bytes or falls back):

| field | how | verified |
|---|---|---|
| pack header, count, version | recipe | every rebuild |
| pack trailer checksum | hash of the rebuilt bytes | every rebuild |
| entry type/size header | kind in the recipe; size from the component length | every rebuild |
| OFS_DELTA distance | from the rebuilt offsets and a base index | every rebuild |
| REF_DELTA base id | stored (20/32 B) in `seq`; in the stable table in `set` | – |
| zlib header (2 B) | 3-bit code (4 common headers + explicit) | every rebuild |
| zlib Adler-32 | from the plaintext | every rebuild |
| zlib DEFLATE stream | exact reconstruction IR (§4) | 100 % of entries (git 2.43.0 / zlib 1.3; levels 1, 6, 9) |
| **`.idx` v2** | `BuildIdx(pack)` | **byte-identical for every pack in every state** (`idx_derived` = packs) and every fixture; v1 index also byte-derivable |
| loose object path | from the object id | every loose file |
| object ids | hash of the resolved object | needed in the `set` layout as lookup/base keys, so *stored* (see §7) |
| pack **order** | not derivable | permutation, ⌈log₂ n⌉ bits/entry (§8) |
| delta base choice | not derivable | stored |
| **delta programs** | Git's own `create_delta(base, target)` | **100 %** (oracle, below) |

Delta oracle (`z3git derive`, Git 2.43.0 `create_delta` fed the resolved base and target of every delta entry of packs made with 7 producer settings × 2 object formats):
**all deltas regenerate byte for byte** — 524/524 on every setting (29 826 B of programs in the default pack; 45 179 B at window/depth 3),
lua 6 485/6 485 (1 248 436 B), imgui 1 963/1 963 (492 653 B), codicons 3 362/3 362 (1 210 401 B), zeros3 559/559 (150 318 B). Across Git versions: `diff-delta.c` from **v1.7.12, v2.0.0, v2.20.0, v2.43.0, v2.50.0**
(five different source files) each regenerates 524/524 of the stored programs. The function is stable; a pack-producer-independent derivation looks plausible.

What it is worth, as an oracle bound (`derive -pairs`; B's programs that A does not already hold, and how many `create_delta` regenerates from objects A holds):
window 3/3: 133 new programs (19 792 B), all 133 regenerable; reversed input 147 (6 168 B), all; window 50/250 20 (838 B), all; one revision 2 (32 B) —
the base/target is itself new, so 0 regenerable; lua one revision: 17 new (4 240 B), 11 regenerable (2 203 B); imgui 30 new (3 170 B), 19 regenerable (1 835 B). **Not realisable here**: using it needs a
reimplementation in the reconstruction path (the upstream file is GPL-2.0 and was not ported; the Z3 repository is Apache-2.0), and storing the target in full
instead of as a program costs more than the programs (lua: resolved 166 MB vs pack 2.3 MB). So derived deltas are recorded as an upper bound: the bytes at stake are the *new* programs of an event — 0.8–19.8 KB in the measured pairs (≤ 2.3 % of the pack; 0.2 % for a one-revision repack) — not as a representation.

## 7. Native identity: SHA-1 mapping and SHA-256 simplification (Q5, Q6)

| | SHA-1 repo | SHA-256 repo |
|---|---|---|
| id bytes per entry in the `set` stable table (id + base id for 77 % of entries) | 20 + 20 | 32 + 32 |
| separate native → ZeroS3 map (id + component SHA-256) | 52 B/object = 20 124 B for 387 objects (2.3 % of the pack; 79 % of the size of the whole stable table, which already carries the ids) | 0 for single-component full objects; the Git id **is** the component SHA-256 |
| fraction of entries whose component SHA-256 equals the object id | 0 % | **60 %** (233 / 387; = every non-delta entry; the other 152 are delta entries whose component is the delta *program*) and 99 % when no deltas (385 / 387) |
| not equal when | – | inline (< 40 B), chunked (> 256 KiB; 2 objects), delta entries |
| stable table bytes/entry (sha-256 vs sha-1, auto) | 65.7 | 79.2 (self digests elided for single-component canonical objects) |
| `.idx` bytes/object (native index of the original pack) | 31 | 43 |

SHA-256 mode removes the mapping *table* but not the work: bases still need ids, entries that are deltas still need the digest of the delta program, and
the ids are 1.6× as wide. Net table cost is +21 % per entry in SHA-256 mode; costs of whole representations differ by at most 1.2 points between the two formats in every scenario.
The native-object **floor** (every object as canonical bytes, no pack structure) is also not a floor for a pack: lua's 8 426 objects are 45 M as full objects
against a 2.3 MB pack, because the pack's delta compression is the dominant gain (resolved 166 MB). The floor row is only the lower bound for *new objects* of a revision
(lua one revision: 28 K; the exact layouts pay 144–354 K) and, in the branchy history, exceeds the real pack cost (141.6 K vs `gp-zlib` 82.6 K, `gp-set` 103.9 K).

## 8. Exact recipe overhead and the order tables (Q8)

First ingest of one pack, SHA-1 default synthetic pack (387 entries, 889 469 B), table layout:

| layout | recipe B | meta B | B / entry | stored as components | B / entry | what it buys |
|---|---:|---:|---:|---:|---:|---|
| `seq` (entries in pack order, each with its pieces) | 17 011 | 3 012 | **51.7** | – | – | simplest; recipe re-paid by every repack |
| `set` (id-sectioned stable table + reconstruction table + permutation) | 2 555 (section refs) | 444 (of which permutation 436 = 9 bits/entry) | 7.7 | 25 439 stable + recon | 65.7 | order-independent; sections reused across packs; id lookup |

Permutations cost ⌈log₂ n⌉ bits per entry: 436 B for 387 entries, ≈ 13 bits (≈ 11 KiB) for 7 153. Section size K (mean entries per id-chosen section) trades
section references against rewrite on change (synthetic, `gp-set-plain`, K = 2 / 4 / 8 / 16 / 32): first ingest 932.7 / 928.2 / 924.6 / 922.5 / 920.6 K; reorder 37.7 / 39.9 / 40.3 / 43.0 / 42.1 K;
one revision 15.1 / **10.7** / 13.1 / 15.5 / 25.5 K; level 6 → 1 21.7 / 17.2 / 13.7 / 11.7 / **10.2** K. K = 4–8 is the flat optimum for churn, K = 16–32 for recompression. Inlining pieces
below 256 B instead of 40 B saves 15.5 K at first ingest and costs +10 K on reorder and +5 K on one revision. The stable/reconstruction split costs +0.5 % at first ingest (more section references).

When `set` beats `seq` depends on pack size: below ≈ 1 500 entries the `seq` recipe is cheaper (zeros3 one revision: `gp-zlib` 180 K, `gp-set` 223 K), above ≈ 5 000 entries `set` wins by 2.4–2.9× (codicons 276 K vs 94 K; lua 353 K vs 144 K; synthetic chain 16.5 K vs 10.9 K), and in every case `gp-zlib` is the cheapest first ingest (−0.1 to +1.1 % against CDC of pack + `.idx`) because it adds only the recipe and the derived index pays for it.

## 9. Admission: what the evidence supports (Q-admission)

First-ingest premium over CDC of pack + `.idx` (dry run, empty store, production pack rule; `z3git admit`) and the break-even `q*` = premium / saving, measured on the events of §2–§4 (the layout pays when the chance of such a rewrite exceeds `q*`). `gitz3.Admit(pack, idx, algo, q)` chooses by expected cost and answers CDC for every parse / resolve / checksum / limit / thin failure.

| pack | premium: gp-zlib | gp-auto | gp-set | gp-set-plain | `q*` gp-zlib (one revision / reorder / chain) | `q*` gp-set (one revision) | `q*` gp-set-plain (recompression 6→1 / 6→9) |
|---|---:|---:|---:|---:|---|---|---|
| synthetic (SHA-1) | −0.1 % | +2.1 % | +3.5 % | +5.1 % | 2.0 / 1.2 / 0.6–0.9 % | 22 % | 5.4 / 5.7 % |
| lua | +1.1 % | +3.5 % | +18.0 % | +21.0 % | 1.8 / 1.8 / 1.8–2.0 % | 25 % | 24 / 26 % |
| imgui | +0.8 % | +6.0 % | +10.2 % | +12.4 % | 2.1 / 1.3 / 2.1–4.1 % | 21 % | 11 / 13 % |
| codicons | +0.4 % | +3.1 % | +10.1 % | +12.6 % | 0.8 / 0.5 / 0.8 % | 18 % | 13.5 / 14.3 % |
| zeros3 | +0.1 % | +5.2 % | +7.1 % | +8.3 % | 0.1 / 0.1 / 0.1 % | 15 % | 7.6 / 8.8 % |
| 5 APKs (51.6 MB) | 0 % | −10 % | −10 % | −10 % | – (plaintext dedupe and re-compression already win) | – | – |

There is **no static size threshold**: `gp-zlib` is nearly free everywhere (≤ 1.2 %) and always the safest tier for reorder / rebase / repack-after-commit; the plaintext tier costs 3–20 % and
repays only when recompression (loose → pack, a different level, a different zlib, a different Git) is likely: break-even `q*` 5–26 % (the more tiny entries, the higher), ≈ 0 for loose objects (their conversion to a pack is certain and `gp-*` plaintext tier cuts it to 6–71 %) — so
loose objects are always admitted plaintext-first; the set layout repays for ≥ 5 000-entry packs (2.4–2.9× cheaper than `gp-zlib` per rewrite) and where ids are needed for lookups (§10). `Admit` uses one fixed expected saving (70 % of the CDC cost), so it cannot see that the set layout saves more on big packs; it picks `gp-zlib` for every measured pack at q ≥ 2 % and CDC below it. Fallback triggers observed and implemented:
thin / unresolvable (`thin-or-unresolved-delta`), checksum, limits, malformed, unsupported, mismatch, IR failure of a single entry (entry carried as zlib bytes, never the pack), and "exact costs ≥ CDC" (the dry run).

## 10. Reads and scale (Q9)

**Reading one object by native id** (300 random objects, `z3git reads`; mean bytes of stored components that must be fetched; CDC chunks are 16–256 KiB):

| pack | entries | mean chain depth | CDC: idx + pack chunks (of which idx) | `gp-entry`: idx chunks + chain entries | `gp-set`: table sections + plaintext/delta components | floor: the object's own packed bytes | section directory in the recipe |
|---|---:|---:|---:|---:|---:|---:|---:|
| mixed history, window 10 / depth 50 | 824 | 3.4 | 155 528 (23 435) | 24 751 | **4 924** | 1 132 | 396 B |
| depth ≤ 250 | 824 | 3.9 | 154 305 (24 144) | 25 481 | 5 217 | 1 132 | 396 B |
| no deltas | 824 | 0 | 95 852 (24 144) | 25 222 | 1 890 | 1 101 | 396 B |
| tiny-file churn | 758 | 1.2 | 84 469 (22 296) | 25 431 | 5 411 | 3 091 | 396 B |

The deepest sampled chain (47 deltas) needs 33 405 B from the set layout against 240 881 B of CDC chunks. The set layout finds an object with a binary search of its
directory (4 B per section) and then reads the sections and components of its chain: **16–51× less than CDC** for mean objects (30× for the delta-heavy packs), 1.7–4.6× the object's own bytes. The `.idx`
is not needed (it is regenerated only for whole-pack reads), but a lookup against a CDC-stored `.idx` already costs 22–24 KB (one chunk) before any object byte moves.
These are fetch counts from the real component sizes, not a second reader implementation; reading *object content* never needs DEFLATE replay, serving the *original pack bytes* does (below).

**Throughput, memory, scaling** (`z3git scale`, one pack per row, plaintext tier everywhere so the replay path is exercised; heap = peak `HeapAlloc` growth over the already-loaded input with default GC, so it includes garbage and — for rebuild — the lab's in-memory component store):

| point | pack MiB | entries | deltas | max depth | parse ms (MiB/s) | resolve ms (MiB/s) | peak heap parse / resolve MiB | analyse s (MiB/s) | rebuild s (MiB/s) | peak rebuild MiB |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 300 files × 8 KiB | 0.8 | 787 | 119 | 4 | 35 (23) | 37 (22) | 3 / 2 | 1.7 (0.5) | 0.18 (4.4) | 12 |
| 300 × 32 KiB | 2.8 | 791 | 120 | 4 | 85 (33) | 84 (34) | 4 / 4 | 9.7 (0.3) | 1.4 (1.9) | 37 |
| 300 × 128 KiB | 10.4 | 792 | 120 | 4 | 284 (37) | 319 (33) | 10 / 12 | 54.8 (0.19) | 8.7 (1.2) | 116 |
| 300 × 512 KiB | 40.3 | 789 | 118 | 4 | 1 076 (37) | 1 222 (33) | 38 / 43 | 141.7 (0.28) | 37.2 (1.1) | 451 |
| 2 000 tiny files | 0.6 | 3 602 | 1 338 | 4 | 95 (6) | 121 (5) | 5 / 3 | 0.9 (0.7) | 0.15 (4) | 16 |
| 8 000 tiny | 2.3 | 13 025 | 4 747 | 5 | 209 (11) | 230 (10) | 15 / 19 | 3.0 (0.8) | 0.54 (4) | 54 |
| 32 000 tiny | 9.2 | 49 235 | 16 884 | 5 | 580 (16) | 632 (15) | 35 / 46 | 11.0 (0.8) | 2.2 (4) | 202 |
| 96 000 tiny | 27.4 | 145 661 | 49 172 | 6 | 1 852 (15) | 2 327 (12) | 91 / 127 | 35.4 (0.8) | 6.4 (4) | 613 |
| 1 file 64 KiB × 600 revs, depth ≤ 10 | 0.3 | 2 404 | 599 | 10 | 45 | 113 | 3 / 5 | 0.64 | 0.098 | 10 |
| … depth ≤ 50 | 0.2 | 2 404 | 599 | 50 | 43 | 73 | 4 / 9 | 0.54 | 0.075 | 11 |
| … depth ≤ 250 (actual 186) | 0.2 | 2 404 | 600 | 186 | 43 | 77 | 5 / 29 | 0.43 | 0.062 | 12 |
| … depth ≤ 1000 (actual 187) | 0.2 | 2 404 | 600 | 187 | 44 | 90 | 8 / 28 | 0.44 | 0.059 | 11 |

All rows rebuilt exactly. Observations: parse and resolve are linear in pack bytes and, for tiny objects, in object count (≈ 13 µs per entry); the exact-DEFLATE analysis is the expensive step (0.2–0.8 MiB/s of pack) and replay runs at 1.1–4.4 MiB/s —
**orders of magnitude below memory-speed CDC reassembly** (not measured separately: reassembly is concatenation). Peak heap grows with the input (parse ≈ pack size for large files, ≈ 3× for 100 k-entry packs because of per-entry structures); the replayed rebuild
is 11× the pack in this lab because the store lives in memory. Whole-pack work does not see delta depth (resolve 73–113 ms for depth ≤ 10 … 187); only random reads do:
materialising one object without a cache takes 192 / 317 / 559 / 567 µs for depth limits 10 / 50 / 250 / 1000 (mean chain 2.2 / 8.7 / 20.6 / 20.9; entry bytes touched 6.1 / 6.5 / 7.9 / 7.9 KB).
Timings come from single-process runs on an otherwise idle machine (4 cores); matrix runs that shared the machine were used for byte counts only.

## 11. Negative results and limitations

* The native-object representation is not a floor for packs, and plaintext-first is a measurable premium (5–21 % at first ingest for `gp-set-plain`, 2–6 % for `gp-auto`); it is not worth it without a recompression event.
* The reconstruction path is slow: exact-DEFLATE replay runs at ≈ 1–4 MiB/s of pack bytes against memory-speed concatenation for CDC; the analysis (`ir.Encode` with profile search) at 0.2–1 MiB/s. `gp-zlib` avoids replay entirely.
* Small-entry packs pay more than they gain at first ingest in the set layout (lua +18 %), because 4 585 of 8 426 entries compress to < 100 B and the per-entry descriptor is 65–90 B.
* Delta derivation is an oracle result; its saving is small compared with the other costs.
* One producer (git 2.43.0, zlib 1.3, threads = 1). No other zlib builds (zlib-ng, libdeflate) or libgit2/JGit packs were measured; `gp-*` plaintext tier results for them are unknown.
* SHA-256 repositories exist only for the synthetic families (public repositories are SHA-1). Bitmaps, commit-graph, MIDX, reverse indexes, promisor/cruft packs and alternates were not touched.
* Real histories are shallow windows (150–1 500 commits) of four repositories; "binary-ish" coverage is one synthetic re-export of real APKs (no deltas are chosen for them: Git stores five 12 MB blobs).

## 12. Carry-forward into Z3-17

* The `Via` / digest-table idea transfers directly: a manifest lists layer digests, so layers are `Via` members; the registry's SHA-256 digest is already the component identity (no mapping), unlike Git SHA-1.
* Separate identity (stable) from reconstruction (volatile): OCI configs/manifests (identity) vs gzip metadata (reconstruction).
* `gzip` layers are the same exact-DEFLATE problem measured here; Z3-11's tar split composes with it.
* Per-class admission with a break-even probability instead of a threshold; zlib-bytes tier first, plaintext tier when recompression is expected.
* `srev.StructuredRevision` fits the Git recipes without new fields (`gitz3.RevisionOf`; `Roots`/`Bases` unused; `Native` carries the Git id where the component SHA-256 is not the id).

## Not run

Root ZeroS3 tests (root module untouched); old DEFLATE / Safetensors / tar / ELF / PE / CAR / Merkle fuzz suites; a race run.
