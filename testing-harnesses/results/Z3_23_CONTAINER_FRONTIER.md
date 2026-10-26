# Z3-23 — modern content-aware container frontier (lab, characterization)

**Question.** How much reuse do production ZeroS3 CDC + pack rule still miss in OCI `tar+zstd`, eStargz,
zstd:chunked and Nydus RAFS v6 — formats that already expose independent compression, chunk digests or CAS structure?
Production untouched; nothing here is a format. Starting point Z3-22 (`c9265a1`). Costs are **incremental
bytes = pack-rule bytes of new CDC chunks + 96 B/chunk manifest** (lab.Acct convention); native identity adds 36 B/ref.

## Corpus and provenance
One deterministic tree (`frontier/`, shared with Z3-24): 6580 files / 220 MiB logical / 153 MiB data — 6000 tiny, 400
medium text/json/log, 8 random 1–8 MiB binaries, 4 compressible, 100 identical 128 KiB files, 32 MiB records file,
72 MiB sparse file (5 MiB data), 20 hardlink pairs, 30 symlinks, xattrs, threshold-size files. Revisions R0–R11 (R1 40 B
edit · R2 six distant edits · R3 mode/mtime/xattr only · R4 rename/move · R5 +40/−40 · R6 ~3.2k tiny files · R7 dup
rename/modify/delete · R8 tails · R9 sparse layout · R10 producer order · R11 ~90 % unrelated payload).
Tools (all outside Git, `oracle/containers/setup.sh`): zstd 1.5.5 `-3 -T1`; `estargz` v0.16.3 (gzip level 9, 1 MiB chunks);
containers/storage v1.59.1 `pkg/chunked/compressor` (zstd:chunked, default level); nydus-image v2.3.6 (`cb50bd9`,
`--fs-version 6 --compressor zstd --chunk-size 1MiB --digester sha256`, dir input); gzip -6 comparator.
Every producer gave identical bytes for R0 twice. Oracles are labelled: FLOOR = per-file production CDC of regular-file
content (hardlinks once) + a binary manifest (paths, attrs, chunk digests) through the same CDC chain.

## Format decision matrix (cumulative R0→R11, incremental bytes)
| Lane | R0 | R1..R11 | Cum | ×FLOOR | Native identity (cum) | Decision |
|---|---|---|---|---|---|---|
| FLOOR (oracle) | 75.5M | 71.0M | 146.5M | 1.00 | — | — |
| tar CDC (uncompressed) | 76.9M | 109.4M | 186.2M | 1.27 | tar structure+payload floor 144.8M | baseline |
| tar.gz CDC (comparator) | 80.8M | 668M | 749M | 5.11 | — | (known gap) |
| **OCI tar+zstd** | 70.9M | 308M | 379M | 2.59 | 422M (blocks, worse than CDC) | **PROMOTE** (conditional) |
| **eStargz** | 79.6M | 134M | 214M | 1.46 | members 236M · chunk-digest 152M | **CONTROL** |
| **zstd:chunked** | 84.2M | 139M | 223M | 1.52 | frames 154M (1.05×) | **CONTROL** |
| **Nydus RAFS v6** | 73.3M | 87M | 161M | 1.10 | chunks ≈ FLOOR | **CONTROL** |
Rows R1..R11 exclude R0. Excluding R0 and the unrelated control R11, FLOOR is 9.1M: tar+zstd 247M (27×), eStargz 66M
(7×), zstd:chunked 69M (7.6×; native frames 14M, 1.6×), Nydus 24M (2.7×).

## Representative revision cost (KiB unless M)
| rev | FLOOR | tar CDC | tar.gz | **tar.zst CDC** (native blk) | eStargz CDC (members / digests) | zstd:chunked CDC (frames) | Nydus blob+boot |
|---|---|---|---|---|---|---|---|
| R1 40 B edit | 124 | 241 | 74.1M | 445 (330) | 878 (739 / 360) | 1018 (4) | 543+395 |
| R3 meta only | 337 | 7364 | 73.8M | **41.4M** (54.3M) | 12.0M (6.8M / 0) | 12.1M (0.1) | 104+203 |
| R4 rename/move | 183 | 1945 | 79.9M | **56.8M** (60.0M) | 3143 (5.3M / 0) | 3280 (0.06) | 539+512 |
| R5 add/remove | 3080 | 3331 | 82.0M | **60.7M** (73.4M) | 3900 (4.6M / 2.9M) | 4399 (2.9M) | 3197+512 |
| R6 tiny churn | 2152 | 3712 | 3582 | 3073 (2889) | 5061 (2.8M / 2.1M) | 5542 (2.0M) | 4210+512 |
| R7 dup rename/del | 130 | 4058 | 37.3M | 11.8M (13.3M) | 7500 (**24.7M** / 47) | 7127 (0.01) | 437+511 |
| R10 order only | 0.7 | 13.5M | 80.3M | **56.7M** (70.5M) | 20.0M (28.7M / 0) | 22.1M (0.6) | 104+1 |
| R11 unrelated | 61.9M | 67.3M | 69.4M | 60.6M | 68.3M | 70.2M | 62.5M+511 |
R11 shows no format falsely wins. First-sight premium vs FLOOR R0: zstd −6 % (stronger codec), eStargz +5.4 %, zstd:chunked +11.5 %
(19 098 tiny frames), Nydus −3 % (zstd over 1 MiB chunks). Index cost per revision: eStargz TOC 380 KB (0.46 %), zstd:chunked
manifest 943 KB (1.1 %), Nydus bootstrap 1.34 MB raw / 512 KB CDC-new (1.8 %); FLOOR manifest 93–336 KB.

## Point reads at R0 (native physical bytes · regions · production-CDC chunk bytes covering them)
| Lane | tiny file (1.1 KiB) | 4 KiB inside 32 MiB file |
|---|---|---|
| tar (raw) | 2 KB · 1 · 105 KB | 4 KB · 1 · 87 KB |
| tar.zst | 72.7 MB (single frame: decode all prior blocks) | 5.5 MB (prefix to block ≈ off/128 KiB, approximate) |
| eStargz | 389 KB = footer 51 B + **TOC 388 KB** + 1 member · 3 · 564 KB | 757 KB (TOC + 368 KB 1 MiB-chunk member) · 3 · 881 KB |
| zstd:chunked | 665 KB = **manifest 664 KB** + footer + frame · 3 · 869 KB | 674 KB (21 KB chunk) · 3 · 916 KB |
| Nydus | 636 B chunk + bootstrap ≈ 12 KiB (estimate) · 1 | 340 KB (1 MiB chunk) + ≈16 KiB · 1 |
TOC/manifest fetch dominates small reads unless cached; Nydus keeps lookup metadata randomly addressable.

## Native-vs-CDC synthesis
1. **Ordinary compressed layers hide a lot, but zstd is not gzip.** In-place equal-size edits (R1, R2, R9, R6) are nearly
   invisible to CDC for zstd (445 KB vs 74 MB for gzip) because 128 KiB blocks resync. Anything that shifts or rewrites many
   sites (R3, R4, R5, R7, R8, R10) rewrites ≥80 % of the zstd stream: 41–61 MB per revision against a 0.2–3 MB floor.
2. **Native compressed boundaries do not recover it** for tar+zstd (blocks 422M vs CDC 379M): blocks depend on encoder state,
   so only exact re-encoding (decode sequences/literals, replay bit-exactly) could expose the plaintext.
3. **Explicit chunk digests largely remove the gap** (zstd:chunked frames 1.05×, eStargz digests 1.04×, Nydus 1.10×) and CDC over
   the independent units already gets 1.5×; the residue is (a) first-sight premium, (b) **scattered small changes costing a
   whole 16–256 KiB CDC chunk** (zstd:chunked R3 12.1M vs 0.1M native), (c) **header/first-chunk coupling in eStargz** (name,
   mode or order changes rewrite the member: R7 24.7M, R10 28.7M vs 0 by chunk digest) and (d) tar-order sensitivity (R10).
4. Native chunks ≈ desired semantic components, but they are *coarser* than needed in two places: eStargz 1 MiB chunks (R1 739 KB vs
   124 KB floor) and Nydus 1 MiB chunks; zstd:chunked content-defined chunks are fine-grained.
5. Chunked formats win mainly because compression units are independent (no exact-compression reconstruction).
6. No missing generic `repz3` primitive: coupling is a header↔payload split (STORED/framing already express it); tar+zstd would need a
   *kind* (zstd block parse/replay), not a new generic concept.

## Exactness / feasibility
- eStargz: a stdlib-only gzip-member scanner tiles the 86 MB blob exactly (6736 members); the **existing** `deflate.ParseGzipAll`
  re-emits the whole blob byte-identically (no `KindGzip`/wire change). Re-encoding members with stdlib gzip at levels {−1,1,5,6,9}
  matched 0/6736 (producer inserts flush points inside members) — exact eStargz needs the existing parse/replay IR, per member.
- zstd frames/blocks scanned stdlib-only (`ScanZstd`, bounded, truncation/overrun tests); no zstd decoder or encoder written.
- tar+zstd exact transform = decode literals+sequences and replay FSE/Huffman bitstreams (DEFLATE-style parse/replay, no encoder
  emulation); large but well-defined. Gain is concentrated in layers rebuilt with shifted offsets — worth a later batch, not now.

## Carry-forward
tar+zstd exact parse/replay is the only PROMOTE; Z3-24 found no image target, so it becomes Z3-25 (a zstd parse/replay substrate shared with SquashFS/EROFS). eStargz/zstd:chunked/Nydus are
architecture controls: target semantic components with ~64 KiB-or-finer, independently stored units, and keep names/metadata out
of payload-bearing units. Nydus coverage: dir-input only (no tar-input ordering knob); no chunk-dict or lz4 runs.

## Reproduce
`testing-harnesses/z3/oracle/containers/setup.sh` · `z3frontier c23 -out c23.jsonl` · `z3frontier c23sum -in c23.jsonl` ·
`z3frontier estargz-replay` · tests: `go test ./frontier` (scratch ≈ 1 GB, results under /root/z3scratch, never committed).
