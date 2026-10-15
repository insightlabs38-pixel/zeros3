# Z3-17 OCI registry content and image layers (lab)

Laboratory measurement only. Nothing here changes `zeros3.go`, the root `go.mod`, manifests, journals, packs, stores, descriptors,
native-ID indexes or any production read path, and no result is permission to productionize. Code: `testing-harnesses/z3/ociz3`,
`fstate`, `cmd/z3oci`; pins: `testing-harnesses/z3/realworld/oci.json`. The Z3 module stays standard-library-only.

## Summary

* **The OCI model is small and safe to implement.** Index -> manifest -> config/layers with Docker and OCI media types, size and SHA-256
  checks on every descriptor, DiffID checks on every layer, exact JSON bytes kept, unknown/unsupported content opaque. Across the 870
  (state, representation) cells measured below (570 synthetic, 300 real) there is **no exactness failure**: every cell that makes a reconstruction claim rebuilt through its serialised recipe, matching by length, SHA-256 and bytes (the three oracle rows per state carry no such claim).
* **Registry blob granularity is already almost all of the reuse for fully shared layers.** `cdc` (production chunking of the blobs) is within
  0.1 % of the digest-addressed `reg-blob` model on every scenario; the price of chunk granularity is a digest -> component-list mapping of
  5-13 KiB per image state (at most 0.06 %, section 17B).
* **One real pair gains enormously from the transformed tar+gzip layer representation, three do not.** In `dotnet/runtime-deps` 8.0.1 -> 8.0.2 -> 8.0.3 the
  one changed layer (1.0 MB, new digest and DiffID, mostly the same filesystem) costs **1002.3 K under CDC and 15.4 K transformed (-98.5 %)**; the 3-release sequence is
  **-26.8 %** (-28.1 % for the two-platform index) after a 6.3 % first-sight premium. In `prometheus/node-exporter` v1.7.0 -> v1.8.0 -> v1.8.1 (a layer that is one 19 MB Go binary)
  the same representation costs **+4.4 %** (+2.2 % with CDC inside large members); only the last patch release gains (-5.1 %), not enough to repay the premium.
* **The chooser problem is first sight, not the decision on a changed layer.** The greedy chooser (`oci-auto`: transform when cheaper against the current
  store) never starts transforming because a layer first stored as CDC leaves no plaintext to reuse: it equals `cdc` on every real scenario. A fixed first-sight
  premium bound (`oci-auto-s10`, <= 10 %) recovers -25.9 % on runtime-deps and costs +0.9...+1.2 % on node-exporter. Whether a family resembles its previous release
  is not knowable from one revision; this is the same causal admission problem as Z3-16 (section 17H).
* **Most of the synthetic revision kinds favour the transform; some are clear losses.** Gain: one file changed, member reorder, header metadata, gzip header only,
  other gzip producers, tar layout. Loss or no gain: appended file (CDC 9.8 K vs 15.4 K), whiteout layer, many tiny files (the per-member recipe, 107 K, exceeds the saving),
  a sparse edit of one large file when members are kept whole (626.8 K vs 400.4 K CDC; CDC inside members: 106.1 K).
* **Partial reads are a trade, not a win.** Transformed layers serve a member from its own components (12.5 K for a 12 K file vs a 653.8 K compressed prefix inflate)
  but cannot serve the registry-format blob without re-deflating it: 7.5 MB of components in 0.8 s for a 3.3 MB layer, ~6.5 MB/s of tar, peak RSS ~11x the tar size, and a
  fall back to CDC for layers beyond the existing 32 Mi token bound (70-140 MiB of tar).
* `srev.StructuredRevision` needed **no new fields** for Git or OCI; one generic adapter (`lab.RevisionOfPlan`) now serves both.

## Method, provenance and accounting

* Fetch date 2026-10-15; Go 1.27.0; `git` 2.43.0 (Z3-16 oracle only, not used here); GNU gzip 1.12 and CPython 3.13.16 / zlib 1.3 (optional producers for D7, `ociz3.ProducerVersions`).
  The registry client is Z3's own anonymous HTTP code (bearer-token challenge, digest-pinned, size- and SHA-256-verified, byte-capped, cached outside Git); no external fetch tool was needed.
  Docker Hub (429) and ECR Public (data limit) were exhausted in this environment, so images come from `quay.io` and `mcr.microsoft.com`.
* Accounting is the shared lab accounting: SHA-256 of exact component bytes is the only identity; a table recipe (32 B digest + varint length per distinct component + varint index per
  reference), skeletons, units, plan meta and indexes are all charged; cost columns use the production pack rule (DEFLATE per new component kept when it saves >= 1/16). Unchanged
  files (same digest-named file) are not re-planned. Every non-oracle cell is built, its piece list serialised and parsed back, and rebuilt from stored components
  only (`fstate`).
* Comparison points: `raw`, production `cdc` (16K/64K/256K), `fixed-64k/4k`, `cdc-archive` (the OCI export tar chunked), `archive-tar` (Z3-11 tar split of the export),
  `reg-blob (model)` (oracle: every blob one atomic component), `layer-gzip-exact` (Z3-05..08 exact DEFLATE over the layer, tar left to CDC), `layer-tar+gzip` (tar members as
  components + exact DEFLATE; `-cdc` chunks members), `oci-auto`, `oci-auto-s10`, and the labelled oracles `layer-oracle-logical` / `layer-oracle-tar` (no reconstruction
  path; not floors: real rows can sit below `layer-oracle-logical`).

Pinned images (all by index digest; amd64 and arm64 manifests; Docker manifest-list and Docker v2 media types; every DiffID verified `ok`; attestation manifests are absent in these lists):

| image | platform | index | manifest | config | layers: blob digest, bytes (DiffID) |
|---|---|---|---|---|---|
| quay.io/prometheus/node-exporter v1.7.0 | amd64 | `4cb2b9019f17` | `52a6f10ff102` | `72c9c2088986` | `2abcce694348` 778849 (`faf755cf727c`); `455fd88e5221` 480647 (`aaa2c4c90b67`); `324153f2810a` 10512407 (`24cc87562193`) |
| | arm64/v8 | | `772b09d94861` | `b7ce331e7cd8` | `ed490e74f363` 833089 (`2448dcf83a2c`); `3aa4fa234265` 480660 (`3e7aea2cd90a`); `29a8a9cb3e36` 9818863 (`ea5affef5263`) |
| node-exporter v1.8.0 | amd64 | `8a57af80a4c7` | `6af385633d03` | `9b82e0f21071` | `9fa9226be034` 783033 (`1e604deea57d`); `1617e25568b2` 480923 (`6b83872188a9`); `7d3b8dd56633` 10770623 (`5d9bf8ea81d9`) |
| | arm64/v8 | | `72b3677798ff` | `dfe91411660f` | `6ce8b87a9754` 836725 (`f86c5e3025ef`); `d2f8aae8d80e` 480921 (`e6d95b698944`); `1502960579d5` 10084774 (`0247457b1d5b`) |
| node-exporter v1.8.1 | amd64 | `fa7fa12a57ef` | `e91be75cf2b2` | `0c6f6c1bdd47` | `9fa9226be034`, `1617e25568b2` (shared with v1.8.0); `432171469b8e` 10766139 (`926d7671a64d`) |
| | arm64/v8 | | `c13575817446` | `75954a939881` | `6ce8b87a9754`, `d2f8aae8d80e` (shared); `1ca9a3668648` 10085791 (`1cdbbc020138`) |
| mcr.microsoft.com/dotnet/runtime-deps 8.0.1-alpine3.19 | amd64 | `f7edf2a72da5` | `4c701e30d846` | `9a3f996170d6` | `4abcf2066143` 3408729 (`d4fc045c9e3a`); `a5b36613b222` 1022980 (`845870c50e7a`); `59aee732fc16` 1236 (`bd7e26315af0`) |
| | arm64 | | `f8ae92d64985` | `0bd2dad82482` | `bca4290a9639` 3347715 (`b09314aec293`); `12fd2b3f5d9e` 986655 (`18246819a351`); `aa96f2c622b2` 1235 (`cab08f4745e4`) |
| runtime-deps 8.0.2-alpine3.19 | amd64 | `d6ee8dbe148a` | `d066088ea119` | `3a5e2af631a3` | `4abcf2066143` (shared); `1085594f2b23` 1022970 (`347b5e94b56a`); `bf0cc1260f5e` 1236 (`451ce86d212e`) |
| | arm64 | | `17bb324f3200` | `fb56e3745b86` | `bca4290a9639` (shared); `a16fb911310a` 986651 (`70178a04e9a1`); `96831a181497` 1236 (`e1e46e631665`) |
| runtime-deps 8.0.3-alpine3.19 | amd64 | `ddda904d7c75` | `6d9f07e3caed` | `f57c5f732868` | `4abcf2066143` (shared); `4e1692478f05` 1022968 (`0eb7a8c6f45f`); `73df137ef55b` 1237 (`7088ab30013e`) |
| | arm64 | | `ee4eeba0e925` | `bd5905dd6956` | `bca4290a9639` (shared); `bc100247df55` 986663 (`9b9c04f7b3cc`); `542d13d78ba4` 1236 (`0ad0995eb371`) |

The cache holds ~77 MiB for the six images (amd64 and arm64 each). All layers are `application/vnd.docker.image.rootfs.diff.tar.gzip`. 28 distinct DiffIDs occur across the 36 layer references; **none appears
under two different blob digests**, so there is no real "same uncompressed layer, different gzip bytes" evidence in this corpus; D6/D7 below are synthetic producers, and only GNU gzip and CPython zlib are
independent implementations (the others are the Go compressor at other settings or header fields).

## 17A Model and parser

`ociz3` reads a layout directory, an in-memory store or registry-fetched content: `index.json`/image index/manifest list -> manifest -> config + layers. Limits (`ociz3.Limits`): document 4 MiB, 4096 descriptors per document,
100 000 distinct blobs, index depth 8, blob 4 GiB, DiffID inflation 8 GiB. Every descriptor is checked for size and SHA-256 **before** its blob is trusted; documents are parsed only to find descriptors (unknown fields ignored, bytes
never regenerated). A blob that is missing, mismatching, of an unknown or unsupported media type (zstd layers, in-toto attestations, `sha512:` digests), unparsable or below a too-deep index becomes an opaque blob node; the image stays
representable. DiffID is verified by bounded streaming inflate. A path is built only from a validated 64-hex digest (`DirStore` cannot escape its directory).

Tests (`go test ./ociz3 ./fstate` pass): descriptor and DiffID mismatch, unknown/unsupported media types, hostile documents (counts, negative/huge sizes, short/upper-case/traversal digests, 20 000-deep nesting, oversize), nesting/node bounds,
**truncation at every byte of the index, manifest and config**, duplicate/ambiguous identities (one digest as two roles, wrong size, repeated layer, config as layer, duplicated root entries), **malformed layers** (empty, header only, truncated, bit-flipped data and CRC, not gzip, tar in the gzip slot,
two members, trailing junk, a 64 MiB inflation bomb) all falling back exactly in bounded time, 600 random mutations of tiny valid documents walked and represented exactly, the transform guard, builder determinism, and a local TLS fake registry
(token negotiation, verification, cache, byte budget, hostile sizes, 429 retry, insecure/malformed challenges, platform selection). One 25 s `go test -fuzz FuzzDocuments` smoke on the three parsers and the walker (193 k executions, no failure).
The transform guard (`ociz3.Guard`) keeps a gzip layer out of the exact-DEFLATE path when its bounded streaming inflate exceeds 256 MiB or 128x its size, or is malformed: the 64 MiB bomb took 85 s through all representations before the guard (measured while other jobs were running) and 0.4 s after.

## 17B Registry-native baseline and identity

A registry stores blobs by SHA-256 digest; ZeroS3 components are at most one CDC chunk, so a blob digest equals a component SHA only when the blob is a single chunk. Measured under production CDC (`z3oci walk`):

| image | verified blobs | single-chunk blobs (digest = component SHA, no mapping) | bytes | multi-chunk blobs (need digest -> component list) | bytes | chunks | mapping recipe bytes |
|---|---:|---:|---:|---:|---:|---:|---:|
| node-exporter v1.7.0 | 10 | 4 | 6988 | 6 | 21.8M | 345 | 12.3K |
| node-exporter v1.8.0 | 10 | 4 | 6605 | 6 | 22.4M | 353 | 12.6K |
| node-exporter v1.8.1 | 10 | 4 | 6567 | 6 | 22.3M | 358 | 12.8K |
| runtime-deps 8.0.1 | 10 | 6 | 7626 | 4 | 8560.6K | 141 | 5079 |
| runtime-deps 8.0.2 | 10 | 6 | 7629 | 4 | 8560.6K | 146 | 5259 |
| runtime-deps 8.0.3 | 10 | 6 | 7630 | 4 | 8560.6K | 134 | 4828 |

(two platforms per image; manifests, configs and the 1.2 KB layers are single chunks.) The mapping is the table recipe already charged in every `cdc` row; the optional lookup index adds 32 B per multi-chunk
blob digest (6 x 32 = 192 B for node-exporter). `reg-blob (model)` (no mapping, atomic blobs, an oracle) and `cdc` differ by less than 0.1 % on all seven scenarios below, so identical layers cost nothing extra under either, and
the `cdc` recipe overhead for the whole image state is at most 0.06 % (node-exporter v1.8.1 amd64: 6151 B for 168 chunks against 146 B for 4 blobs).

## 17C Layer decomposition and exactness

For a gzip layer `layer-tar+gzip` stores the tar members (header+small members, payload components, optionally CDC-chunked) and the exact DEFLATE reconstruction (skeleton + units, Z3-06/07), re-deflating on demand;
`layer-gzip-exact` applies the same DEFLATE reconstruction but chunks the tar plaintext with production CDC. Documents are never transformed. Every layer of the six real images (36 references, 28 distinct blobs per DiffID) was transformed with no fallback and no guard rejection; the synthetic Go-compressed layers, GNU gzip 1.12 and CPython zlib (D7a/b) are reproduced the same way. No cell failed its exactness check.

## 17D Synthetic revisions (producer and metadata churn)

Base: a three-layer image (150 + 40 + 10 files of 1-4 KB, 20 % random bytes; Go gzip level 6, mtime 0), cost of the **revision** on top of the stored base (bytes, production pack rule). "blobs new to a registry" is
what a digest-addressed blob store adds. `oci-auto` keeps the base as CDC and therefore equals `cdc`; `oci-auto-s10` equals `cdc` in all rows but D9 (106.1 K) and D7c (329.7 K: some layers of the revision pass the 10 % test and are stored transformed against a CDC-stored base). The standard base's first-sight premium is 18.6 %, above 10 %, so its layers stay CDC.

| revision | blobs new to a registry | cdc | cdc-archive | archive-tar | layer-gzip-exact | layer-tar+gzip | layer-tar+gzip-cdc | oci-auto-s10 | layer-oracle-logical |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| D1 one file changed (4 KB) | 228.6K | 205.9K | 287.3K | 207.1K | 84.4K | **15.7K** | 15.7K | 205.9K | 81.5K |
| D2 one file added | 230.1K | **9806** | 89.3K | 10.7K | 37.7K | 15.4K | 15.4K | 9806 | 35.4K |
| D3 whiteout layer (+1 tiny layer) | 1419 | 1132 | 52.7K | 1978 | 1159 | 1188 | 1188 | 1132 | 1115 |
| D3b file removed in place | 227.6K | 205.0K | 285.1K | 206.1K | 90.1K | **21.2K** | 21.2K | 205.0K | 80.3K |
| D4 member reorder | 229.3K | 229.2K | 288.8K | 230.1K | 205.6K | **24.6K** | 24.6K | 229.2K | 192.4K |
| D5 tar mtime/uid/gid/mode of layer 0 | 228.6K | 228.6K | 288.3K | 229.7K | 244.3K | **37.2K** | 37.2K | 228.6K | 230.5K |
| D6 gzip header only (DiffID unchanged) | 283.1K | 55.2K | 89.0K | 86.4K | **3402** | 17.1K | 17.1K | 55.2K | 927 |
| D7a recompressed by GNU gzip -6 | 262.1K | 262.1K | 269.1K | 263.5K | **5772** | 19.4K | 19.4K | 262.1K | 927 |
| D7b recompressed by CPython zlib -6 | 261.6K | 261.7K | 266.0K | 263.0K | **5739** | 19.3K | 19.3K | 261.7K | 926 |
| D7c Go compressor, level 1 | 308.5K | 308.7K | 312.0K | 310.1K | **19.6K** | 33.3K | 33.3K | 329.7K | 926 |
| D7d Go compressor, level 9 | 260.0K | 260.1K | 264.0K | 262.0K | **6296** | 19.9K | 19.9K | 260.1K | 926 |
| D8 tar layout (PAX -> GNU format) | 283.5K | 283.6K | 285.0K | 285.0K | 303.0K | **46.7K** | 46.7K | 283.6K | 286.0K |
| D9 sparse 16 B edit in a 1.5 MB file | 634.5K | 400.4K | 430.0K | 401.4K | 104.6K | 626.8K | 106.1K | **106.1K** | 100.8K |
| D10 3000 files of ~100 B, one changed | 199.8K | 142.5K | 212.2K | 143.2K | **27.5K** | 114.2K | 114.2K | 142.5K | 19.7K |
| D11 config only | 1099 | 919 | 52.5K | 1633 | 919 | 919 | 919 | 919 | 919 |
| D12 manifest annotation only | 786 | 643 | 54.5K | 1289 | 643 | 643 | 643 | 643 | 643 |
| D13 index: annotation + arm64 entry over the same layers | 1091 | 1000 | 88.8K | 1866 | 1000 | 1000 | 1000 | 1000 | 1000 |
| D14 layer order swapped | 1091 | 913 | 87.8K | 1630 | 913 | 913 | 913 | 913 | 913 |
| D15 layers 0+1 merged (same final filesystem; oracle-only equivalence) | 278.2K | 57.8K | 91.4K | 58.6K | 47.5K | **18.8K** | 18.8K | 57.8K | 42.9K |

Reading:

* **Identical DiffID, different compressed digest (D6, D7).** A registry exposes zero reuse (283.1 K / 262 K new). Production CDC finds part of it only when the bytes happen to align (D6: header shifts by 10-20 bytes, 55.2 K; D7a-d: 260-309 K, i.e. nothing, because DEFLATE streams from different compressors share no bytes).
  `layer-gzip-exact` makes the layer **nearly free (5.7-6.3 K for GNU gzip, CPython zlib and Go level 9; 19.6 K for Go level 1; 3.4 K for the header-only change)**: the existing predictors reproduce all of these producers without fallback, and the remaining cost is reconstruction units
  (3.7 K of units for the three layers) plus the table recipe. Where reconstruction metadata dominates: `layer-tar+gzip` pays an extra ~15 K per three-layer image for per-member references (D7: 15.0 K recipe vs 4.2 K of new components), which `layer-gzip-exact` (tar plaintext chunked by CDC) avoids. `layer-gzip-exact` pays 5.8 K for GNU gzip (3.7 K of reconstruction units plus recipe and skeleton); the oracle charges 0.9 K.
* D1/D3b/D4/D5/D8 (a layer rebuilt with the same payload bytes): CDC pays for the whole recompressed layer (205-288 K); the transform pays 15.7-46.7 K. D5 and D8 are the cases where `layer-gzip-exact` alone does not help
  (244 K, 303 K) because tar headers and padding sit inside the CDC chunks of the plaintext.
* **Losses.** D2 (append): the appended file lands at the end of the gzip stream, so CDC re-uses the whole prefix (9.8 K) while the transform pays the per-member recipe (11.4 K of 15.4 K). D3 (whiteout layer) is a wash. D10 (many tiny files): the transform's recipe is 107.4 K for ~3000 members
  (new components 1.4 K) against 142.5 K for CDC; `layer-gzip-exact` (27.5 K) wins, the oracle bound is 19.7 K; a first-sight premium of 398 K (190 %) makes the transform unprofitable (break-even 14 revisions). D9: members kept whole store 1.5 MB again (626.8 K); chunking inside large members is necessary (106.1 K).
* Document-level changes (D11-D14) cost 0.6-1.1 K under every non-archive representation. The export-archive baselines pay 52-89 K for the same change because the archive is one tar whose offsets shift (`cdc-archive`) or because the archive's own members move (`archive-tar`: 1.3-1.9 K).

First-sight premium and break-even (base stored first; premium = `layer-tar+gzip` base cost - `cdc` base cost: 336.4 K vs 283.6 K = +18.6 % for the standard base; break-even = premium / saving of one revision of that kind):

| revision | saving per revision | break-even revisions | CDC-chunked members: premium / saving / break-even |
|---|---:|---:|---|
| D1, D3b, D4, D5, D7a-d, D8 | 184-275 K | 0.19-0.29 | 52.8K / same / same |
| D6 gzip header only | 38.1K | 1.39 | 52.8K / 38.1K / 1.39 |
| D15 layer partitioning | 39.0K | 1.35 | 52.8K / 39.0K / 1.35 |
| D9 sparse edit (1.5 MB file) | -226.4K | never | 44.3K / 294.3K / 0.15 |
| D10 many tiny files | 28.3K | 14.06 | 398.1K / 28.3K / 14.06 |
| D2, D3, D11-D14 | <= 0 | never | |

## 17E Real corpus

Cumulative cost over each three-release sequence through one component store (production pack rule), relative to `cdc`. Sequences: node-exporter v1.7.0 -> v1.8.0 -> v1.8.1 (a 19 MB Go binary layer plus two ~0.5-0.8 MB base layers; v1.8.0 and v1.8.1 share two layers
by digest), runtime-deps 8.0.1 -> 8.0.2 -> 8.0.3 (one shared 3.3-3.4 MB base layer, one changed 1.0 MB layer, one changed 1.2 KB layer). "index" = both platforms stored together. `cross-platform` = the v1.7.0 amd64 image followed by v1.7.0 arm64.

| representation | node-exp amd64 | node-exp arm64 | node-exp index | runtime-deps amd64 | runtime-deps arm64 | runtime-deps index | cross-platform |
|---|---:|---:|---:|---:|---:|---:|---:|
| `cdc` (absolute) | 33.0M | 31.1M | 64.1M | 6337.7K | 6168.8K | 12.2M | 21.9M |
| `cdc-archive` | +0.8 % | +0.5 % | +0.8 % | +3.2 % | +3.6 % | +4.0 % | +0.0 % |
| `archive-tar` | +0.0 % | +0.0 % | +0.0 % | +0.1 % | +0.1 % | +0.1 % | +0.0 % |
| `reg-blob (model)` (oracle) | -0.1 % | -0.1 % | -0.1 % | -0.1 % | -0.0 % | -0.0 % | -0.1 % |
| `layer-gzip-exact` | +0.9 % | +0.9 % | -0.5 % | -24.0 % | -23.3 % | -24.4 % | +1.8 % |
| `layer-tar+gzip` | +4.4 % | +3.9 % | +2.5 % | **-26.8 %** | **-26.8 %** | **-28.1 %** | +4.1 % |
| `layer-tar+gzip-cdc` | +2.2 % | +2.2 % | +0.2 % | -25.9 % | -26.2 % | -27.4 % | +4.4 % |
| `oci-auto` (greedy) | +0.0 % | +0.0 % | +0.0 % | +0.0 % | +0.0 % | +0.0 % | +0.0 % |
| `oci-auto-s10` | +1.2 % | +1.2 % | +0.9 % | -25.9 % | -26.2 % | -27.4 % | +3.7 % |
| *`layer-oracle-logical`* | -1.1 % | -1.0 % | -2.4 % | -26.3 % | -25.6 % | -26.7 % | -0.2 % |
| *`layer-oracle-tar`* | +2.3 % | +2.1 % | +0.6 % | -29.1 % | -29.1 % | -30.4 % | +2.1 % |

First image / later images (runtime-deps amd64): `cdc` 4333.2K / 2004.5K; `layer-tar+gzip` 4607.8K / **30.9K**; `oci-auto-s10` 4662.8K / 33.0K. The first-sight premium is +6.3 % (runtime-deps amd64), +5.7 % (arm64), +4.1 % (index); node-exporter: +8.4 % (amd64, `layer-tar+gzip`),
+8.2 % (arm64), +4.1 % (index; with `-cdc` +9.0 %/+9.0 %/+4.4 %). Where it comes from (runtime-deps amd64, `layer-tar+gzip`): new component bytes 4548.0K against 4330.4K (the pack rule's DEFLATE of the tar plaintext is ~5 % bigger than the producer's gzip) plus 59.7K of recipe and plan meta.

Why node-exporter does not gain: the only changed layer is one 19 MB statically linked Go binary. Across v1.7.0 -> v1.8.0 the transform gains nothing (+1.7 % with members whole, +2.3 % chunked; with members kept whole the binary is one 19 MB component that cannot be shared at all); across the patch release v1.8.0 -> v1.8.1 CDC inside the member recovers
611.8 K of 10.3 M (-5.1 % amd64, -4.7 % arm64, -4.9 % index) for 74.5 K of recipe+meta, but the first-sight premium (~1.0 M) is repaid only after ~3 releases like that one, and the v1.7 -> v1.8 step costs +2.3 %. For comparison the oracle bounds say the achievable gain on these binaries is also small (`layer-oracle-logical` -1.1...-2.4 %).

The two arch builds of node-exporter share nothing: `cross-platform` shows the second platform adding its full 10.6 M under CDC (10.6 M transformed, -0.4 %); the transformed variants are 1.8-4.4 % worse on the pair because of the first-sight premium.

## 17F Cross-image and cross-platform reuse: the three numbers

(1) = bytes the registry already shares at blob level (digest present in an earlier image of the sequence); "new" = the blobs a digest-addressed store would add; (2) = additional saving inside those new blobs = new blob bytes - new component bytes of the representation
(pack rule, negative = worse than the blob); (3) = recipe + plan meta + index bytes paid for it. `cdc` row = what production CDC alone gives; total = new component bytes + (3).

| step | image bytes | (1) reuse at blob level | blobs new | representation | new component bytes | (2) additional saving | (3) recipe+meta+index | ZeroS3 total | vs blobs new |
|---|---:|---:|---:|---|---:|---:|---:|---:|---:|
| runtime-deps amd64 8.0.2 | 4331.6K | 3328.8K (76.9 %) | 1002.7K | cdc | 1001.6K | 1185 | 792 | 1002.3K | -0.0 % |
| | | | | layer-tar+gzip | 5284 | **997.6K** | 10.3K | 15.4K | **-98.5 %** |
| | | | | layer-tar+gzip-cdc | 5284 | 997.6K | 11.3K | 16.5K | -98.4 % |
| runtime-deps amd64 8.0.3 | 4331.6K | 3328.8K | 1002.7K | cdc / layer-tar+gzip | 1001.6K / 5317 | 1187 / 997.5K | 649 / 10.3K | 1002.2K / 15.5K | -0.1 % / -98.5 % |
| runtime-deps arm64 8.0.2 | 4236.5K | 3269.3K (77.2 %) | 967.3K | cdc / layer-tar+gzip | 963.4K / 5078 | 3962 / 962.3K | 829 / 13.3K | 964.2K / 18.3K | -0.3 % / -98.1 % |
| runtime-deps index 8.0.2 | 8568.1K | 6598.1K | 1970.0K | cdc / layer-tar+gzip | 1964.8K / 9554 | 5304 / 1960.6K | 1585 / 23.6K | 1966.3K / 32.9K | -0.2 % / -98.3 % |
| node-exporter amd64 v1.8.1 | 11.5M | 1234.3K (10.5 %) | 10.3M | cdc | 10.3M | 1899 | 6151 | 10.3M | +0.0 % |
| | | | | layer-tar+gzip | 10.5M | **-262.2K** | 65.6K | 10.6M | +3.1 % |
| | | | | layer-tar+gzip-cdc | 9905.2K | 611.8K | 74.5K | 9979.7K | **-5.1 %** |
| node-exporter arm64 v1.8.1 | 10.9M | 1286.8K | 9852.6K | cdc / layer-tar+gzip / -cdc | 9850.8K / 10036.2K / 9319.4K | 1896 / -183.6K / 533.2K | 5855 / 65.0K / 73.4K | 9856.5K / 10101.2K / 9392.8K | 0 / +2.5 % / -4.7 % |
| node-exporter index v1.8.1 | 22.4M | 2521.1K (11.0 %) | 19.9M | cdc / layer-tar+gzip / -cdc | 19.9M / 20.3M / 18.8M | 3950 / -445.5K / 1145.3K | 11.7K / 130.5K / 147.9K | 19.9M / 20.5M / 18.9M | 0 / +2.8 % / -4.9 % |

Findings:

* **Blob-level reuse (1) is real and already large when images share base layers** (77 % of runtime-deps, 10.5 % of node-exporter amd64 v1.8.1). Production CDC adds essentially nothing inside the new blobs: (2) is 0.02-0.4 % of them (1.2-5.3 KB) because compressed layers share no bytes.
  The same layer under several tags/manifests (`4abcf2066143` in three manifests, `9fa9226be034`/`1617e25568b2` in two) is a digest hit; only the small manifest and config documents are new.
* **Additional reuse (2) exists only through the exact-DEFLATE transform, and its metadata (3) is small relative to the gain only when the changed layer is highly related.** runtime-deps: (2)=997.6 K for (3)=10.3 K; node-exporter: (2) is negative unless members are chunked (+611.8 K for 74.5 K).
* **Multi-architecture index**: a platform's layers are separate blobs; runtime-deps amd64 and arm64 share neither base layers nor changed layers, so the index doubles bytes (8.6 M) with no cross-platform gain (`cross-platform`: arm64 after amd64 costs 10.6 M under CDC and 10.6 M transformed). All reuse in the pinned content is along a platform's own release sequence.
* No pair of real layers with equal DiffID and different digests exists in the pinned set; D6/D7 above are the only evidence for the "same tar, different gzip" case.

## 17G Partial reads and scaling

Bytes touched (stored component bytes before the pack rule; the raw archive is the export tar of the layout; runtime-deps 8.0.1 amd64: export 4338.5K, layer `4abcf2066143` 3328.8K gz, 527 members):

| operation | raw OCI archive, production CDC | blob-level components (`cdc`) | transformed tar+gzip (members whole) | transformed (members CDC-chunked) |
|---|---|---|---|---|
| read one manifest (947 B) | 71.8K in 1 chunk | 947 B in 1 | same | same |
| fetch one layer by digest (3328.8K) | 3350.7K in 56 | 3328.8K in 56 | 7536.4K in 820 comps; rebuild 805 ms, analysis 1332 ms | 7536.4K in 897; rebuild 847 ms |
| one member, 790 KB (`bin/busybox`, first) | 546.0K in 11 (inflate prefix 502.0K) | 545.5K in 11 | 790.0K in 1 | 790.0K in 9 |
| one member, 12 KB (`etc/ssl/openssl.cnf`, middle) | 739.0K in 13 (prefix 653.8K) | 738.5K in 13 | **12.5K in 1** | 12.5K in 1 |
| one member, 3.7 KB (last file) | 3350.7K in 56 (prefix 3328.8K) | 3328.8K in 56 | **4096 in 1** | 4096 in 1 |

* A gzip layer has no random access: a member read inflates from the start (the compressed prefix is in the row); the OCI archive adds 0.5-21.9 KB of boundary chunks. The transformed representation serves the member alone (4 KB for the last file instead of the whole layer), **but fetching the registry-format blob
  needs all components (7.5 MB before the pack rule, 2.3x the gzip, 820 reads) and a full re-deflate at 6.4-6.6 MB/s of tar**. Reading a manifest from the archive touches 71.8 K (CDC chunk of the archive) against 947 B as a blob. The Z3-08G `ir.MaterializeRange` index
  (compressed-offset per unit) could serve range reads of the blob without a full re-deflate; it is not wired into these layer representations and not measured here.
* node-exporter v1.7.0 amd64: its 10 MB layer holds two members, so any member read inflates 10.0 M (CDC) or touches the 19.0 M binary (1 component; 225 chunks when chunked); peak RSS of that process 415 MB.

Throughput and memory on synthetic layers (one gzip layer of N MiB of tar, Go compressor level 6; `reads -scale`; peak RSS is the whole process including the synthetic input):

| layer | tar | gunzip | CDC of the gz | analysis (parse + plan) | rebuild | peak RSS | components touched by a rebuild |
|---|---:|---:|---:|---:|---:|---:|---:|
| 4 MiB tar / 1745.2K gz | 4606.5K | 25 ms | 3 ms | 1093 ms (4.3 MB/s of tar) | 716 ms (6.6 MB/s) | 56.1M | 4686.0K |
| 16 MiB / 6735.5K | 17.6M | 101 ms | 13 ms | 3485 ms (5.3 MB/s) | 2812 ms (6.6 MB/s) | 190.5M | 17.9M |
| 64 MiB / 25.7M | 70.3M | 392 ms | 51 ms | 12940 ms (5.7 MB/s) | 11576 ms (6.4 MB/s) | 750.8M | 71.5M |
| 128 MiB / 51.8M | 140.3M | 755 ms | 101 ms | 1588 ms: **fell back to CDC** (existing 32 Mi token bound) | 59 ms (copy) | 748.4M | 51.8M (the gzip) |

The existing IR path materialises the inflated layer and its tokens (peak RSS ~11x the tar size at 64 MiB); no streaming path exists in the lab primitives, so the transformed representation is bounded by `MaxTransformInflated` = 256 MiB (guard) and, in practice, by the 32 Mi token cap of
`deflate.Parse` (between 70 and 140 MiB of tar for this content); beyond it the layer is stored as ordinary CDC, as the 128 MiB row shows. Analysis runs at 4.3-5.7 MB/s and rebuild at 6.4-6.6 MB/s of tar, the dominant cost of using the transform at all; gunzip runs at ~180 MB/s and CDC of the gzip at 500+ MB/s.

## 17H Admission and shared-primitive review

Chooser candidates: raw CDC (`cdc`), blob components (`reg-blob (model)`; no gain over CDC measured, not a ZeroS3 representation) and the transformed tar+gzip layer (`layer-tar+gzip[-cdc]`). Rules supported by the data above:

1. **An unchanged digest-addressed blob is never planned again.** A layer is a file named by its digest; the runner skips an unchanged name and content, so no parse and no inflate, only the SHA-256 that the descriptor check and the skip map already compute. Manifests, configs, indexes and unknown media types are always CDC.
2. **Transform only layers that pass the guard** (well-formed gzip or tar, <= 256 MiB inflated, <= 128x expansion) and whose reconstruction is exact; everything else is CDC (counted as `layer_guard` / `layer_cdc`).
3. **Compare the transformed plan with CDC against the current store.** With plaintext of the previous revision stored this decides correctly in all measured cases (changed runtime-deps layer: 15.4 K vs 1002.3 K; node-exporter v1.8.1 with whole members: reject).
4. **First sight needs a policy.** The greedy comparison never starts (`oci-auto` = `cdc` on all 7 scenarios). Admitting layers whose intrinsic cost is within 10 % of CDC (`oci-auto-s10`) gains -25.9...-27.4 % on runtime-deps and costs +0.9...+3.7 % on node-exporter
   and cross-platform content. Break-even probability of a similar later revision: runtime-deps premium 274.6 K vs saving 986.9 K per revision -> q* = 0.28; node-exporter amd64 premium ~1.0 M vs +0.34 M gain on the one patch step and a loss on the other -> not reached in two steps.
   A fixed bound cannot tell these apart; tar header/recipe statistics (tiny-file layers, appends) predict some losses (D10's 190 % premium is rejected, D2 and D3 are never improved) but not the Go-binary case.
5. **Chunk large members.** `-cdc` costs a little more at first sight (node-exporter amd64 +9.0 % vs +8.4 %; runtime-deps 4664.4 K vs 4607.8 K) and is the only variant that works for D9 and the patch-release step.
6. **Candidates not measured**: a late-bootstrap policy (store the first revision as CDC, transform both when a changed layer in the same position proves related) and range reads through `ir.MaterializeRange`; both are carry-forward.

Shared primitive: **no new `srev` field is earned.** `ociz3.RevisionOf` and `gitz3.RevisionOf` now share `lab.RevisionOfPlan` (Component with optional Native, Member with `Via`, Residual, Index, Rebuild); `TestSrevShapeFitsOCI` checks the accounting and rebuild round trip, empty `Roots`/`Bases`, and that a single-chunk blob's
registry digest is exactly its component SHA (the native hint is redundant there). A multi-chunk blob's digest names a recipe rather than a component; that is what the existing optional native index carries (32 B per digest, 192 B per image state here), so the registry digest needs no new storage concept and `Roots` still has no storage significance.
The shared runner `fstate` (state/file/representation matrix with serialised-recipe verification) was extracted and is used by both `gitz3` and `ociz3`.

## Negative results and limits

* The greedy per-layer chooser does not bootstrap; a fixed premium bound loses 1-4 % on binary-heavy content.
* The transform loses on appended files, whiteout layers, tiny-file layers (recipe-bound), cross-platform pairs, and on a Go binary layer unless its members are chunked and several releases follow.
* Fetching the registry-format blob from the transformed form reads 2.3x the blob and re-deflates at ~6.5 MB/s; memory ~11x the tar; the existing token bound sends layers beyond 70-140 MiB of tar back to CDC.
* Real evidence is three pinned families on two registries and two platforms; both are small (4-22 MB) and one changed layer (runtime-deps) carries the whole gain; "mostly related contents" is a property of that package layer, not of images in general.
  Docker Hub could not be sampled (anonymous rate limit), and there are no zstd layers, attestation manifests or equal-DiffID/different-digest pairs in the corpus.
* D6/D7 synthetic re-exports are not independent real producers except GNU gzip and CPython zlib; the Go compressor is also the base producer of the real layers.
* Time numbers come from a shared 4-core container (some runs overlapped); sizes are exact, timings are indicative.

## Reproduce

```sh
cd testing-harnesses/z3
go run ./cmd/z3oci fetch -group nodeexp,rtdeps -max-mib 150   # ~77 MiB, cache outside Git
go run ./cmd/z3oci walk -group rtdeps | -group nodeexp        # 17B
go run ./cmd/z3oci synth                                      # 17D
go run ./cmd/z3oci real                                       # 17E/F (about 3-4 min, parallelisable with -only <scenario>)
go run ./cmd/z3oci reads -group rtdeps -name 8.0.1; for m in 4 16 64 128; do go run ./cmd/z3oci reads -scale $m; done   # 17G
go test ./fstate ./ociz3; go test ./ociz3 -run '^$' -fuzz '^FuzzDocuments$' -fuzztime 25s
```
