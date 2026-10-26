# Z3-26 — QCOW2 vs VHDX sparse/differencing image shootout (lab, characterization)

**Question.** Do QCOW2 or VHDX hide enough relocation / metadata / differencing / allocation churn that a structure-aware *exact* representation
of the image **file** materially beats production CDC? **Decision: `NO_DEEP_TARGET`** (Z3-27 not run). Production untouched, no `repz3` change.

## Corpus, producers, exactness
- One logical corpus, fixed seed, 4 GiB virtual, ~410 MiB payload (16 MiB 64-B records, 64 MiB text, 64 MiB random, 128 MiB of 6 distinct 1 MiB blocks,
  100 MiB 256-B records, 8192 scattered 4 KiB pages, 64 MiB explicit zeros, 1 MiB zero island). Oracle = generator stream SHA-256 (equals the raw file).
- Family A: each state rebuilt standalone, R0..R7 cumulative edits (4 KiB edit / hole write / zero 1 MiB / 1 MiB unaligned / 128 scattered pages / 64 KiB in one
  duplicate / 8 KiB across a region end), **R8** same disk as R7 written high→low, **R8b** shuffled 2 MiB groups, **R9** same layout, new seeds (negative control).
  Family B: B0, children C1–C6 of B0 (4 KiB; 6 spread; zero override 1 MiB+16 KiB; hole + zero-range write; 3 sectors in one block; 4 MiB), grandchild C7 of C6.
- Lanes: `qemu-img 8.2.2` QCOW2 v3 64 KiB (also extended-L2, one zlib lane); VHDX 2 MiB blocks / 4 KiB sectors (control: 1 MiB) from pinned `vhdx-rs 0.2.0` via an
  isolated helper (`oracle/vhdxgen`), read back by system `libvhdi` (`oracle/vhdiread`). `qemu-img` cannot create VHDX differencing images, and refuses these files (not pursued).
- **True differencing VHDX was independently tested**: every image (93 total, 5 lanes × A/B) is read back by `qemu-img compare/check` or a libvhdi **chain** read and matches
  the guest-disk SHA-256 (0 failures), including partially-present blocks, zero overrides and hole writes (scanner reports the exact present-sector runs; test asserts them).
- Determinism: QCOW2 R0 twice is byte-identical (3 lanes). VHDX differs in 88 B: both headers' FileWrite/DataWrite GUID + CRC (72) and Virtual Disk ID (16).
  Disclosed normalisation rewrites exactly those (CRC repaired, re-read by libvhdi). `vhdx-rs` also stores GUIDs RFC-ordered, which made libvhdi reject the parent link;
  `parent_linkage` is rewritten from the parent's stored DataWriteGuid.
- Scanners (`diskimg/`, stdlib): exact tiling of the file, native maps, checked bounds; QCOW2 map agrees with `qemu-img map`; truncation and ≈10 hostile-field mutations each;
  25 s fuzz smoke each (92k / 24k execs) clean. `qemu-img convert -B` writes the whole disk and safe `rebase` keeps allocated clusters, so QCOW2 children are written as deltas.
- Costs (MiB unless noted): **CDC** = production CDC + pack rule + 96 B/chunk (`frontier.Chain`). **native** = new 64 KiB-or-native pieces (packed) + recipe delta-coded
  against the previous image (24 B copy run / 36 B ref). **fine floor** = 4 KiB exact pieces, same recipe (oracle). **guest floor** = new unique guest 4 KiB sectors + 48 B per changed run (**not** an exact-file cost).

## Economics (exact image bytes)
| lane (image MiB) | R0 CDC / native / guest | R1–R7 CDC / native / fine / guest | cum R0–R7 ×guest: CDC / best native |
|---|---|---|---|
| qcow2 (704) | 116.2 / 113.4 / 111.6 | 7.75 / 2.26 / 1.68 / 1.58 | 1.095 / 1.022 |
| qcow2 ext-L2 (704) | 116.2 / 113.4 / 111.6 | 7.88 / 2.32 / 1.72 / 1.58 | 1.096 / 1.023 |
| qcow2 zlib (235) | 115.3 / 112.4 / 111.6 | 8.46 / **82.3** / 82.4 / 1.58 | 1.093 / **1.720** |
| vhdx 2 MiB (888) | 116.6 / **167.1** (64 KiB sub-pieces 113.5) / 111.6 | 7.70 / **20.1** (64 KiB: 2.19) / 1.68 / 1.58 | 1.099 / 1.022 (64 KiB) |
| vhdx 1 MiB (887) | 116.6 / 113.1 / 111.6 | 7.59 / 11.2 (64 KiB: 2.19) / 1.65 / 1.58 | 1.098 / 1.019 |
- Ordinary revisions cost CDC 0.4–2.6 MiB on 700 MiB images (0.16 %/rev); **51–60 % of that is the 96 B × ~6.2–6.9 k chunk manifest** (qcow2 3.98 of 7.75 MiB over R1–R7),
  which any flat recipe also pays (11 258 × 36 B = 396 KiB) — a tree/delta manifest is a format-independent idea, not a disk-format one.
- First sight: native has no premium (qcow2 −2.4 %; part of it is this corpus's 1 MiB-aligned duplicate blocks), except 2 MiB VHDX blocks (+43 %: duplicates need 64 KiB pieces).
- Per revision CDC / native (KiB), qcow2: R1 612/64 · R3 816/29 · R4 1666/1089 · R5 2649/910 · R7 816/94. VHDX 64 KiB: R1 682/65 · R5 2360/885 · R7 845/70.
  Relocation is real (R3: 10 515 identical 64 KiB payload pieces moved, 657 MiB; R7: 7 006) yet CDC pays only ~0.8 MiB; zero-ing 1 MiB moves every later cluster.
- Negative control R9: CDC 114.8–115.2, native 112.6–113.0, guest 111.4: no false reuse (tests also assert unrelated content is fully charged).
- **Allocation history is the one large gap**: same final disk, different write order (R8 / R8b): CDC qcow2 0.84 / 11.7, VHDX 2 MiB **18.0 / 19.7**, VHDX 1 MiB **29.0 / 35.5**
  vs native ≤0.06 MiB. CDC re-cuts every chunk spanning a moved payload join (1.6–3.6 % of the image per rebuild); pure append/in-place histories (R1–R7) cost none of this.
- Compressed QCOW2: compressed clusters are packed back to back, so one size change shifts all later ones (R5 native 43 MiB vs CDC 4.4 MiB); an exact DEFLATE child
  would be needed before native identity helps — it cannot change the winner.

## Differencing (Family B; child file bytes only — a child never needs parent bytes)
| child KiB (CDC / best native / parent-aware guest) | C1 | C2 | C3 zero ovr | C4 | C5 partial blk | C6 4 MiB | C7 grandchild |
|---|---|---|---|---|---|---|---|
| qcow2 (child file 0.4–4.4 MiB) | 12/12/4 | 150/179/24 | 49/48/0 | 9/9/8 | 193/192/12 | 4168/4162/4096 | 77/77/4 |
| qcow2 ext-L2 | 5/4/4 | 27/27/24 | 1/0/0 | 9/9/8 | 13/13/12 | 4103/4098/4096 | 9/9/4 |
| vhdx 2 MiB (child file 7–18 MiB) | 9/5/4 | 38/28/24 | 6/1/0 | 15/9/8 | 18/14/12 | 4109/4100/4096 | 10/5/4 |
- Children are self-contained overlays; base + 7 children CDC 119.8–120.7 MiB vs parent-aware floor 115.6; **excluding C6, the six children together cost CDC 0.06–0.48 MiB**
  (floor 0.05). Standard QCOW2 copies the rest of a touched 64 KiB cluster from the parent (C1 12 vs 5 KiB with ext-L2 subclusters); VHDX partial blocks keep only present sectors + bitmap.
- Parent-path-only change (guest disk identical): QCOW2 18–22 image bytes (header), VHDX 1 byte (locator `relative_path`); linkage is the parent's DataWriteGuid, not its path.

## Churn (new bytes R1–R7, KiB, native piece vs CDC) and components
qcow2: data 9 664 / 23 754 · L2 1 344 / 1 344 (ext-L2 2 432) · L1 256 · refblock 256. VHDX 64 KiB: payload 9 664 / 40 320 · headers 896 / 1 344 · metadata 448 / 1 792 · BAT 192 / 768.
Pieces per image: qcow2 11 258 (64 KiB), VHDX 506 (2 MiB) / 14 208 (64 KiB), fine 4 KiB 180–227 k, CDC chunks 6.2–6.9 k. Zero-only CDC chunks: 0 (qcow2), 0.7 % (VHDX).

## Reads (guest reads through C7>C6>B0; ideal metadata / payload; native pieces; CDC chunk bytes)
| read (4 KiB / 1 MiB) | qcow2 meta·hops | qcow2 pieces · CDC | vhdx-2M meta·hops | vhdx pieces · CDC |
|---|---|---|---|---|
| child data (top) | 16 B / 136 B · 0 | 192 KiB / 1.1 MiB · 0.36 / 1.5 MB | 9 B / 40 B · 0 | 3.1 MiB (BAT+bitmap+block) · 0.55 / 1.56 MB |
| inherited from base, 2 hops | 48 B / 408 B · 2 | 448 KiB / 1.4 MiB · 1.5 / 2.5 MB | 24 B · 2 | 2.2 MiB · 1.0 / 2.0 MB |
| never written (3 layers) | 24 B · 2 | 192 KiB · 0 payload | 24 B · 2 | 192 KiB · 0 payload |
| partial block: sector present / absent | std: CoW-filled cluster, 0 hops; ext-L2: 24 B·0 / 48 B·1 | 3 / 3–5 pieces | 9 B·0 / 17 B·1 | 3.1 / 3.1 MiB |
- File range reads of the exact artifact (4 KiB / 64 KiB / 1 MiB): 1 piece (64 KiB, or a 2 MiB VHDX block mapped through its own CDC chunks ≈ 0.1–0.26 MiB) / 1–2 / 17 pieces ≈ 1.1 MB,
  CDC 1 chunk (78–262 KB) / 1 / 18 chunks ≈ 1.07 MB: no range-read penalty or advantage either way.

## Primitive audit (no `repz3` contract change)
| concept | QCOW2 evidence | VHDX evidence | generic primitive? |
|---|---|---|---|
| external BASE | child file is an overlay; backing name in header (18 B), no parent bytes needed | child needs only locator path + parent DataWriteGuid (1 B) | **NO** (guest-view chains: LATER) |
| ZERO / HOLE | L2 `0x0` inherit vs `0x1` zero flag vs allocated zero cluster (64 KiB payload) | NotPresent (inherit) vs present sector of zeros; file-level zero chunks ≤0.7 % | **NO** |
| sparse overlay mask | extended-L2 32-bit sub-cluster bitmaps (8 B/entry) | sector bitmap, partial block = present-sector runs | **NO** (format-local, bytes tiny) |
| ordered allocation map | L1/L2/refblock: 1.8 MiB over R1–R7 (ext-L2 2.9) | BAT 0.19 MiB (+headers/metadata 1.3) | **NO** (CDC absorbs relocation) |

## Classification and decision
- **QCOW2**: ordinary-revision gain ≤1.1 MiB/rev (≈6.7 % over 8 revisions, a third of it first sight), recoverable exactly with 64 KiB identity + delta recipe; compressed lane negative. Not enough.
- **VHDX**: 2 MiB-block identity loses to CDC; with 64 KiB sub-pieces it equals QCOW2. Largest effect is allocation-order churn (18–35 MiB per reordered rebuild, 1.7–3.6 %), producer-specific.
- Differencing children are already ≈ floor under CDC (0.06–0.48 MiB for six children); no BASE/HOLE primitive earns its place. Winner criteria 1, 3, 4 (gap size, cumulative economics, differencing relevance) are weak; 2 (exact recoverability) is good for uncompressed lanes; 5–9 tie.
`NO_DEEP_TARGET`. Evidence gaps (not decision-changing): no Hyper-V/Windows producer; QEMU's VHDX reader not usable; internal snapshots, encryption, external data files skipped;
real guest filesystems (ext4/NTFS) deliberately opaque.
