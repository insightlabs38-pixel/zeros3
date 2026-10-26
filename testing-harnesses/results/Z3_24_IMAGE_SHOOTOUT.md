# Z3-24 — read-only filesystem image shootout (lab, characterization)

**Question.** Is SquashFS or EROFS ZeroS3's strongest next deep representation target, and how close is composefs (metadata image +
external content store) to the content-native ideal? **Decision: `NO_DEEP_TARGET`.** SquashFS is the runner-up (modest, feasible gain),
EROFS-compressed is a negative (large gap, unrecoverable without compressor emulation), composefs is the strongest control. Production untouched.

## Corpus, producers, exactness
Same tree and R0–R11 as Z3-23 (6580 files, 220 MiB logical, 72 MiB sparse file, 100 identical files, hardlinks, xattrs). Everything zstd level 3,
128 KiB blocks/clusters, fixed build time: `mksquashfs 4.6.1` (`-comp zstd -Xcompression-level 3 -b 131072`, default fragments/dedupe);
`mkfs.erofs 1.8.10` built from source (distro 1.7.1 has no zstd; `-zzstd,level=3 -C131072 -Efragments`); `mkcomposefs 1.0.8 --digest-store`.
Controls: uncompressed SquashFS (`-noI -noD -noF -noX`), plain EROFS, chunk-based EROFS (`--chunksize=131072`, has holes), SquashFS
`-no-fragments`/`-tailends`, EROFS without fragments / `-Eall-fragments`.
- Reproducibility: R0 twice is byte-identical for every lane **once the EROFS UUID is pinned** (`mkfs.erofs` draws a random one; the only nondeterminism
  found). `-Ededupe` is pathological on this zero-heavy tree (>3 min, killed) but works on the dup subtree.
- Exactness: every image of every revision was extracted (`unsquashfs`; `fsck.erofs --extract --xattrs`) and compared with the source tree (paths, types,
  modes, sizes, SHA-256, symlinks, file and directory mtimes, xattrs, hardlinks, sparse content): 12/12 for sq, er and the uncompressed controls. `fsck.erofs`
  does not extend a trailing hole, so chunk-EROFS files were confirmed with `dump.erofs --cat`. composefs: all objects and attributes verified (hardlinks not
  asserted). No representation was added; ordered-piece rebuild of both scanners' region tilings is byte-exact (tests). The EROFS tar-input lane drops 30 symlinks (not chased).
- New bounded stdlib scanners: `ScanSquashfs` (superblock, metadata chains, inodes, dirs, fragment table, block lists; metadata codec injected) and `ScanErofs`
  (superblock, inodes, dirs, plain/inline/chunk layouts; compressed extents from an injected oracle). Tests: tiling, ~1400 mutated/truncated scans, hostile fields,
  limits; 25 s fuzz smoke (≈500 execs on shared CPU), no panic/hang.
Cost = pack-rule bytes of new CDC chunks + 96 B/chunk; native = new exact blocks/units + 36 B/ref; FLOOR and composefs-objects are oracles (Z3-23 definitions).

## Revision-chain cost (MiB; R1–R10 excludes first sight R0 and the unrelated control R11; FLOOR R0 75.5, R1–R10 9.1, cum 146.5)
| Lane | image R0 | R0 | R1–R10 CDC (×FLOOR) | R1–R10 native (×) | cum CDC (×) |
|---|---|---|---|---|---|
| SquashFS (fragments) | 72.9 | 73.0 | 33.7 (3.7) | 22.7 (2.5) | 168.5 (1.15) |
| SquashFS `-no-fragments` | 74.1 | 74.2 | 35.2 (3.9) | **12.0 (1.3)** | 172.4 (1.18) |
| EROFS (fragments) | 74.8 | 71.7 | **81.6 (9.0)** | 86.2 (9.5) | 214.6 (1.46) |
| EROFS no fragments / `-Eall-fragments` | 87.4 / 71.9 | 74.2 / 71.3 | 58.1 (6.4) / 237.0 (26) | 75.3 / 244.1 | 198.2 (1.35) / 369.3 (2.52) |
| uncompressed SquashFS / EROFS plain / EROFS chunked | 139 / 221 / 152 | 74.6 / 75.3 / 75.1 | 29.0 / 31.2 / 16.9 | — | 165.9 / 169.5 / 154.7 |
| composefs: image + objects via CDC | 1.6 + | 76.6 | **11.5 (1.3)** (image alone 3.7) | raw objects 156.9 | 150.9 (1.03) |
KiB (FLOOR · sq CDC (native) · sq-no-frag native · er CDC · composefs): R1 124 · 335 (191) · 437 · 8569 · 78 | R3 337 · 251 (156) · 396 · 799 · 635 |
R4 183 · 823 (354) · 392 · 11847 · 632 | R5 3080 · 5052 (4781) · 3278 · 14039 · 3583 | R6 2152 · 3371 (3144) · 2269 · 3270 · 2641 |
R8 2000 · 7925 (5814) · 3097 · 9748 · 2361 | R9 976 · 1304 (647) · 903 · 15377 · 921 | R10 1 · 13733 (7548) · 396 · 106 · 2 | R11 (MiB) 61.9 · 61.9 · 61.8 · 61.3 · 62.9.
Exact SquashFS block identity recovers 45 % of the gap above FLOOR without decoding data (65 % on the real pair below); without fragments it reaches 1.3×.
Raw composefs objects cost 32 MiB for the 40-byte edit (R1): content identity must sit *under* whole-file objects (CDC), as in the 1.03× row.

## Churn attribution (native new MiB over R1–R10)
SquashFS: data 4.3 · fragment/tail **17.1** · inode 0.70 · dir 0.26 · pad 0.10. EROFS: data 20.4 · tail/packed inode **54.0** · inode 5.2 · dir 2.0.
- Metadata relocation is real but small: SquashFS inodes hold absolute data offsets (≈112 KiB rewritten every revision, 0.15 % of the image); EROFS nids are
  physical positions (≈0.8 MiB/revision, 1.1 %; xattr growth in R3 moved inodes, plain EROFS R3/R4 cost 4.2 MiB). composefs metadata: 7 KiB when only content
  changes (R1, R2, R9), 0.63 MiB when attributes change.
- EROFS compressed clusters are *variable-length* (fill 128 KiB of output), so an edit changes every later cluster of that file (R1: all clusters after the edit,
  5.3 MiB), and `-Efragments` compresses all file tails into one packed inode: any shift (R4 rename, R9 sparse change, the real patch release) rewrites 10.8–24 MiB.
  SquashFS has fixed 128 KiB logical blocks per file, so an edit changes one block.
- Fragments/tails: first-sight saving is small for SquashFS (72.9 vs 74.1, −1.6 %) and large for EROFS (74.8 vs 87.4, −14 %); revision churn is +89 % (SquashFS
  native 22.7 vs 12.0) and +40 % (EROFS CDC). ZeroS3 could normalize tails only by decoding and exactly re-encoding zstd. Threshold files (R8, 131 KiB−100 B → +100 B):
  one 45.7 KB block becomes block + 100 B tail block (`-no-fragments`); EROFS 3996 B inline → 4196 B in a 4 KiB cluster.

## Sparse, duplicates, order, compression setting
| 72 MiB sparse file | SquashFS | EROFS plain / compressed / chunked | composefs |
|---|---|---|---|
| image bytes (data = 5.25 MiB) | 5.25 MiB (43 data blocks, 533 size-0 hole entries) | **72 MiB** stored zeros / 5.2 / 5.4 (533 NULL chunks) | whole-file object |
| R9 layout change CDC (native) | 1304 KiB (647) | 1092 KiB / 15.0 MiB (packed inode) / 1396 KiB | 921 KiB total |
Holes are explicit in SquashFS and chunked-EROFS block lists, so a format recipe carries them, and CDC already dedupes stored zeros: **no generic `ZERO`/`HOLE`
primitive is needed here**; that case is logical spaces ≫ stored bytes (QCOW2/VHDX, Z3-26).
- Duplicates (100×128 KiB + 3×2×1 MiB, 18.5 MiB): SquashFS 3.05 MiB with default dedupe vs 10.4 `-no-duplicates`; EROFS 5.97 → 3.07 with `dedupe` (plain 18.5 → 3.14).
  R7 (rename 50, modify 1, delete 25, add 20): FLOOR 130 KiB, sq 488 (202), er 6980, composefs 669.
- Order (R10, same tree): SquashFS `-sort` reversed → CDC 13.4 MiB (data 3.2, tails 9.9), native 7.4 (data 0); `-no-fragments` CDC 14.7 vs native 0.4 — relocated blocks
  are fully recovered by exact block identity. EROFS output is **input-order invariant** (identical bytes for directory and tar input).
- Level 3 → 9, same tree: SquashFS keeps 64.0 % of the second image's bytes (EROFS 58.7 %), only incompressible blocks stored raw; every compressed block changes
  (CDC new bytes 27.4 MiB). Native identity does not survive producer settings.

## Point reads at R0 (native KiB: metadata + data · regions · production-CDC chunk bytes covering them)
| Read | SquashFS | EROFS (fragments) | EROFS chunked |
|---|---|---|---|
| tiny file | 55 (13.7 + fragment block 41.3) · 10 · 579 KiB | 149 (21 + packed cluster 128) · 7 · 1.07 MiB | 22 · 7 · 1.69 MiB |
| 4 KiB in 32 MiB file | 53 (7.8 + block 44.7) · 6 · 487 KiB | 137 (9 + cluster 128) · 4 · 327 KiB | 9 · 3 · 596 KiB |
| list a 50-entry dir | 12 · 7 · 373 KiB | 17 · 5 · 624 KiB | 17 · 5 · 1.25 MiB |
Native reads are 10–80× smaller than the CDC chunks covering them; EROFS fragments make a tiny read decompress a 128 KiB packed cluster.

## Real family check (go1.27.0 → go1.27.3 release tarballs, SHA-256 verified; 15.6k files, 282 MiB; B has 202 changed files)
FLOOR B 35.5 · SquashFS CDC 45.3 (1.28×), native 38.9 (1.10×), `-no-fragments` native 37.0 (1.04×) · EROFS CDC 59.8 (1.68×; packed inode 24.2) · EROFS chunked 37.9 (1.07×)
· composefs 36.3 (1.02×). First sight: FLOOR 74.1, SquashFS 69.1, EROFS 68.1. Same ordering as the synthetic chain.

## Complexity and `repz3` audit
| | SquashFS | EROFS |
|---|---|---|
| parser | MEDIUM: 5 inode variants, metadata chains, fragment table | MEDIUM uncompressed; HIGH for lcluster indexes |
| reconstruction | LOW: ordered opaque blocks + exact metadata | LOW: opaque pieces |
| codec dependence | HIGH: block lists live in compressed metadata; normalization needs exact re-encode | MEDIUM: structure codec-free; normalization VERY HIGH |
| range reads | LOW risk: 53–55 KiB native | MEDIUM: 128 KiB clusters |
| metadata complexity | MEDIUM | HIGH: nid = position, inline tails/xattrs, compact/extended |
| exactness risk | LOW (blocks) / VERY HIGH (fragments) | LOW (pieces) / VERY HIGH (cluster cascade) |
| producer variance | LOW (deterministic) | MEDIUM (random UUID, fast-moving mkfs) |
| `repz3` fit | good: ordered STORED blocks + framing | fair: nothing to gain by representing it |
No `repz3` contract change: ordered block lists, size-0 holes and tail packing are format recipes (STORED/framing/opaque kind).

## Decision, synthesis, carry-forward
Weighted: (1) CDC gap: SquashFS 1.15× cum (3.7× on revisions, 25 MiB), EROFS 1.46× (9×, 73 MiB); (2) exactly recoverable: SquashFS 45 % (real 65 %) with an opaque block
list, EROFS 0 % (cascade); (3) rebuild is trivial for opaque pieces and impossible for normalization without zstd parse/replay; (4–5) both broad, reads fine; secondary:
first sight neutral, no new generic primitive. The achievable SquashFS gain is 11.6 of 169 MiB (7 % cumulative, 14 % on the real revision) and its biggest remainder
(fragments, 17 MiB) needs the same zstd parse/replay Z3-23 already promoted; EROFS gains nothing from native identity. Neither clears the bar for Z3-25.
**Cross-batch.** Z3-23: when compressed units are independent and named by content, CDC already reaches 1.1–1.5× and native digests ~1.05×. Images agree where blocks are
independent (SquashFS) and fail where layout couples content to position (EROFS clusters/packed inode/nids, SquashFS fragments); composefs is the floor (1.03×) because
regular-file bytes never share a compression unit with metadata.
**Queue.** Reorder: Z3-25 = **zstd exact parse/replay** (first consumer OCI tar+zstd, 27× the revision floor; later SquashFS fragment and EROFS cluster normalization), then
Z3-26 QCOW2/VHDX, Z3-27, Z3-28 SST, Z3-29 WIM unchanged. Revisit SquashFS as a cheap block-list consumer once a zstd decoder exists.
Limits: zstd only (no xz/lz4/lzo), one synthetic corpus + one real pair, packed-inode offsets via oracle. Reproduce: `oracle/containers/setup.sh` · `z3frontier i24 [-full] [-lanes …] [-real A=dir,B=dir]` · `i24sum -in i24.jsonl` · `i24x` · `dupes` · `go test ./frontier`.
