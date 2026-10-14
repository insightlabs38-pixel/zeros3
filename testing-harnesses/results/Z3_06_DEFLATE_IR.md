# Z3-06: block-local compact exact DEFLATE representation

Laboratory evidence only. Nothing here touches `zeros3.go`, the root `go.mod`, the store format, manifests, journal or
any production behaviour. Package `z3/ir` is stdlib-only research code on top of the native parser of Z3-05.
Synthetic corpora (one seed, 3 update revisions per family), Go 1.27.0 toolchain, 4-core VM shared with other jobs
(timings are conservative). Reproduce:

```sh
cd testing-harnesses/z3
go run ./cmd/z3corpus gen -out C -size-kib 4096 -revisions 3 -families 'deflate-*,gzip-*,zip-deflate-*'   # 4 MiB base, 12 MiB plaintext/revision
go run ./cmd/z3corpus ext -out E -size-kib 1024 -revisions 3                                               # python zlib, GNU gzip, Info-ZIP
go run ./cmd/z3ir units   -corpus C                      # 6A  unit survival
go run ./cmd/z3ir entropy -corpus C                      # 6B/6C  sizes, bits/token, categories
go run ./cmd/z3ir family  -corpus C -json r.json         # 6D/6E  family cost with breakdown (96 B/ref; 35 B via -show-ref-bytes)
go run ./cmd/z3ir matrix  -from r.json -reps cdc,preflate-rcdc,ir-tok4k-e2 -ref-bytes 35
```

## Answer to the central question

**Yes, but only if the reconstruction units are cut independently of DEFLATE block boundaries, and only down to the
noise floor of the producer.**

* Native DEFLATE blocks (and groups or content-derived groups of blocks) are **useless as reuse units**: a Go L6 stream
  has 3-27 blocks averaging 0.4-4 MiB of plaintext; an edit anywhere changes every later block's boundaries and token
  statistics (insert-mid keeps 9% of block plaintext, insert-begin 0%), and zlib's fixed-symbol-count blocks shift
  after any insertion (49% survive = exactly the prefix before the edit).
* **Token-run units** (a run of the literal/match sequence, cut at rolling-hash positions of the *plaintext*, with a
  small block table as a separate skeleton) survive: after the edit 72-88% (Go L6, 1-4 KiB units) and 97-99% (zlib/GNU
  gzip/Info-ZIP) of the plaintext they cover keep byte-identical reconstruction units; append keeps 99.9%.
* That moves the incremental cost of a 4 MiB-family edit from "fraction of the whole stream" to "fraction of the
  changed units + unit references": with 35 B references, insert-mid on Go L6 costs **13.7% (2 KiB units)** vs CDC
  **52.2%** and Preflate+CDC **12.2%**; on python-zlib L6 **5.1% (16 KiB units)** vs CDC 54.3% and Preflate 5.8%;
  append costs **1.0% (16 KiB units) vs CDC 2.3%** (no regression, unlike Preflate's 10.0%).
* The remaining cost is **not** the plaintext (lower bound 0.3-1.4%) but (a) the **producer's token noise** (Go's
  matchers re-decide about 1 match in 600 differently after an unrelated edit, so a unit that holds one noisy token is
  new), (b) **unit references** (35 B each; 96 B at the production `chunkRef` size) and (c) for **explicit** units, a
  stored IR of 0.76-0.98x the compressed size, so every changed unit costs about what its compressed bytes cost.
  A **predictive** reconstruction (Z3-07) removes (c): see `Z3_07_PREDICTIVE_RECONSTRUCTION.md`.
* The price of the explicit IR is the **entry cost**: r0 stores plaintext (packed, about the compressed size) plus the
  IR (76-98% of the compressed size), i.e. 150-210% of the original stream. Over r0 + 3 updates the explicit IR is
  **not** a winning storage format: it beats CDC only where CDC is poor (insert-begin 72% vs 100%, fixed emitter 42% vs 63%,
  L9 57% vs 64%), ties on insert-mid (63% vs 64%) and loses on append/replace/delete, and Preflate+CDC stays ahead
  cumulatively (39% on L6 insert-mid) because its entry cost is only 118%. That is the case for prediction (Z3-07).

## What was built

```text
exact object
  |
  +-- wrapper bytes (gzip header/trailer, ZIP structure): inline, exact
  +-- semantic plaintext: CDC v1 over the decoded bytes (same CAS as the Preflate representation)
  +-- reconstruction (per DEFLATE stream)
        skeleton   block table (BFINAL/BTYPE, exact dynamic header, stored padding, token count),
                   tail padding, unit index (plain length + tokens per unit), stream length + CRC-32
        units      content-addressed runs of the token sequence, literal VALUES NOT STORED
```

* **Units never cut the bitstream, they cut the metadata.** The bits of a block are a function of its header and its
  tokens; `ir.Reconstruct` concatenates the units' tokens, fills literal values from the semantic plaintext, carves
  blocks by the skeleton's token counts and replays through the exact writer. Length and CRC-32 are checked **before a
  byte is written**; the lab additionally compares length, SHA-256 and bytes with the original.
* Independent reconstruction metadata vs independent semantic decompression state: replaying a unit needs only its own
  plaintext span (literals); matches are never re-discovered. *Validating* that matches copy equal bytes
  (`DecodeOptions.VerifyMatches`) needs up to 32 KiB of plaintext **before** the unit: stored boundary context is
  **0 bytes** (the unit index holds the unit's plaintext offset; the window is read from the semantic plaintext, never
  duplicated); the largest distance a unit uses averages 30-32 KiB, so a validating read is `32 KiB + unit` per unit.
* Cut policies compared (`ir.Policy`): `stream` (control), `block`, `group64K/256K/1M` (adjacent blocks to a plaintext
  target, respecting block boundaries), `cblk*` (block-boundary cuts selected by a gear hash of the 64 plaintext bytes
  before the boundary, so unrelated edits do not renumber later groups), `tok*` (token-run cuts at gear-hash positions of
  the plaintext, tested at every byte and applied at the next token boundary; min/avg/max = 1/4, 1, 4x target; a unit
  never spans a stored block).
* Codecs over the same exact `deflate.Stream`: **E0** naive (Z3-05 formula, estimate only), **E1** compact structural
  (16-bit control word per match, varint literal run, distance extra bytes), **E1+flate**, **E2** adaptive binary range
  coder (stdlib only, ~100 lines) over the same fields with a recent-distance cache, models reset per unit.
  Dynamic headers are stored exactly as DEFLATE transmits them (Huffman-coded code-length symbols): 40-48 B per dynamic
  block, 0.0-0.1% of compressed bytes (11.9% only for zlib memLevel 1, 4048 tiny blocks per stream).
* Accounting counts new plaintext chunks (production pack rule), new unit bytes, one reference per plaintext chunk and
  per unit (`-ref-bytes`: **96 B** = the continuity number, one manifest `chunkRef` JSON entry; **35 B** = a binary
  reference: 32 B SHA-256 + varint length, which is what this structure actually needs), the skeleton, and inline
  wrappers. Units under 64 B are inlined into the recipe instead of referenced.

## 6A. Reconstruction-unit study (survival across revisions)

Go L6 text (4 MiB base, 12 MiB plaintext per revision), averages over r1..r3. "Plaintext covered by kept units" is the
share of the revision's plaintext whose reconstruction unit already exists byte-identically in the previous revision.
For `insert-mid` half of the stream precedes the edit and survives trivially.

| family | policy | units/rev | plain KiB/unit | units kept % | plaintext covered by kept units % | header-coupled kept % |
|---|---|---:|---:|---:|---:|---:|
| L6 insert-mid | stream | 1 | 12288 | 0.0 | 0.0 | 0.0 |
| L6 insert-mid | block | 9 | 1365 | 33.3 | 9.4 | 9.4 |
| L6 insert-mid | group1M | 4 | 3072 | 25.0 | 9.4 | 9.4 |
| L6 insert-mid | cblk64K | 6 | 2048 | 33.3 | 9.4 | 9.4 |
| L6 insert-mid | tok1K | 10398 | 1.2 | 95.9 | 94.2 | - |
| L6 insert-mid | tok4K | 2685 | 4.6 | 89.4 | 86.2 | - |
| L6 insert-mid | tok16K | 673 | 18.3 | 75.1 | 68.1 | - |
| L6 insert-mid | tok64K | 159 | 77.3 | 52.8 | 52.9 | - |
| L6 insert-begin | block / group1M / cblk64K | 18 / 6 / 9 | 683-1843 | 0.0 | 0.0 | 0.0 |
| L6 insert-begin | tok1K | 10433 | 1.2 | 92.3 | 89.1 | - |
| L6 insert-begin | tok4K | 2750 | 4.5 | 79.7 | 72.4 | - |
| L6 insert-begin | tok16K | 699 | 17.6 | 53.2 | 38.8 | - |
| L6 insert-begin | tok64K | 166 | 74.0 | 12.4 | 4.7 | - |
| L6 delete | block | 27 | 450 | 36.6 | 42.7 | 42.7 |
| L6 delete | tok1K / tok4K / tok16K | 10472 / 2679 / 629 | 1.2 / 4.6 / 19.5 | 95.6 / 89.0 / 75.3 | 93.9 / 85.0 / 66.2 | - |
| L6 replace | block | 16 | 768 | 93.8 | 57.8 | 57.8 |
| L6 replace | tok1K / tok4K / tok16K | 10375 / 2655 / 654 | 1.2 / 4.6 / 18.8 | 99.8 / 99.7 / 99.6 | 99.7 / 99.6 / 99.4 | - |
| L6 append | block | 3 | 4097 | 66.7 | 2.1 | 2.1 |
| L6 append | tok1K / tok16K / tok64K | 10515 / 644 / 149 | 1.2 / 19.1 / 82.5 | 100 / 99.8 / 99.3 | 100 / 99.9 / 99.4 | - |
| L9 insert-mid | block | 68 | 181 | 50.5 | 50.4 | 50.4 |
| L9 insert-mid | tok1K / tok4K / tok16K | 10435 / 2673 / 665 | 1.2 / 4.6 / 18.5 | 97.6 / 95.5 / 89.7 | 97.2 / 94.6 / 86.3 | - |
| fixed-Huffman emitter insert-mid | block | 140 | 88 | 49.3 | 49.3 | 49.3 |
| fixed-Huffman emitter insert-mid | tok1K / tok4K / tok16K | 10480 / 2761 / 670 | 1.2 / 4.5 / 18.3 | 99.8 / 99.9 / 99.7 | 99.8 / 99.7 / 99.7 | - |
| **L1 insert-mid** | tok1K / tok4K / tok16K | 10437 / 2707 / 672 | 1.2 / 4.5 / 18.3 | 50.3 / 51.4 / 49.4 | 50.0 / 50.0 / 49.8 | - |

zlib-family producers (1 MiB base, 6 MiB plaintext per revision, `text-insert-mid`):

| producer | block kept (plaintext %) | cblk64K | tok2K | tok4K | tok16K | tok64K |
|---|---:|---:|---:|---:|---:|---:|
| python zlib L1 | 49.0 | 43.3 | 97.4 | 97.0 | 96.3 | 92.2 |
| python zlib L6 | 48.7 | 48.7 | 98.9 | 98.9 | 98.7 | 96.1 |
| python zlib L6 memLevel 1 (4048 blocks) | 50.0 | 41.7 | 98.9 | 98.4 | 98.2 | 89.3 |
| GNU gzip L1 | 46.2 | 46.2 | 98.2 | 98.0 | 96.8 | 84.7 |
| Info-ZIP L6 | 45.4 | 38.8 | 98.9 | 98.9 | 98.7 | 98.3 |

Findings:

1. **Block-based units fail whatever the grouping.** Go's blocks span 0.2-6 MiB, so the prefix is all that survives; zlib's
   blocks end after a fixed symbol count, so an insertion shifts every later boundary and Huffman table. Grouping to
   64 KiB / 256 KiB / 1 MiB and content-derived grouping change nothing because the *blocks* differ, not the grouping.
   "Header-coupled" identity (unit + the block headers it touches) is never better than the decoupled unit alone: the
   header is a tiny, always-changing part and belongs in the skeleton.
2. **Index-based identity is dead, content identity is what survives.** Token-run units are keyed by their bytes; an insertion
   at the beginning leaves 72-89% (Go L6, 1-4 KiB) / 97% (zlib) of the later units identical although every ordinal
   changed.
3. **Producer noise sets the minimum unit size.** Far from an edit, Go L6 still re-decides a few matches per 64 KiB
   (14 of 8737 in the window inspected: a different candidate of the same length, a different hash-table victim; matcher
   state depends on the sliding-window alignment, which an insertion shifts). After the edit, a Go L6 unit is changed with
   probability ~12% at 1 KiB, ~28% at 4 KiB, ~64% at 16 KiB and ~94% at 64 KiB. zlib/GNU gzip/Info-ZIP are almost
   noise-free (<=2% changed at 4-16 KiB). **Go level 1 never resynchronises at all** (about 20% of its matches differ for the
   whole rest of the stream): explicit units are useless there.
4. A match can reach 32 KiB back, so a unit's tokens depend on the previous 32 KiB of content: ~31 KiB beyond an edit are
   perturbed legitimately (insertions shift distances across the edit). This is the "boundary context", and it is a read
   dependency on the semantic plaintext, not extra stored bytes.

## 6B/6C. Exact IR entropy and codecs

Whole stream, skeleton included, as % of the compressed stream (4 MiB Go corpus; ext = python zlib/GNU gzip, 1 MiB base):

| family | tokens/comp byte | Huffman bits/token | E0 naive (Z3-05) | E1 | E1+flate | **E2** | E2 bits/token | E2 bits/match |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Go L1 | 0.69 | 11.66 | 150.5 | 134.9 | 102.7 | **77.4** | 9.02 | 18.92 |
| Go L6 | 0.54 | 14.89 | 177.4 | 161.8 | 122.6 | **92.5** | 13.77 | 18.08 |
| Go L9 | 0.55 | 14.61 | 170.2 | 153.9 | 118.7 | **89.9** | 13.14 | 18.58 |
| fixed-Huffman emitter | 0.40 | 20.12 | 151.7 | 136.9 | 101.3 | **76.4** | 15.36 | 16.60 |
| python zlib L1 | 0.50 | 16.07 | 180.1 | 168.3 | 126.9 | **96.0** | 15.45 | 17.56 |
| python zlib L6 | 0.49 | 16.39 | 175.9 | 164.2 | 127.0 | **96.0** | 15.76 | 18.51 |
| GNU gzip L6 | 0.49 | 16.39 | 175.8 | 164.2 | 127.0 | **95.9** | 15.74 | 18.49 |

* **Best explicit codec: E2.** Materially smaller than naive IR (gate 1: 92.5% vs 177.4%, 0.52x) and than E1+flate
  (0.75x). But explicit exact metadata cannot go meaningfully below ~0.76-0.98x of the compressed stream: the cost is the
  **distance**, 14 bits per match (4.0 symbol + 10.1 extra) of ~18 bits per match; length 3.0, literal run 1.0.
* Per-field static entropies agree with the adaptive coder (order-0 static total 17.9 bits/match vs E2 18.1): conditioning on
  the literal-run bucket / length bucket gains 0.02 bits/match; **a recent-distance cache (MTF 1/4/16) hits 0%** and
  nearby-distance deltas hit 0.2% on this corpus (distances are effectively random inside the window; real structured data
  with record strides would differ, and E2 contains the cache). The distance cannot be modelled without looking at the
  plaintext: that is the argument for prediction (Z3-07).
* Literal runs are never duplicated (run lengths only): ~1 bit per match; exact replay proven.
* Dynamic headers stored exactly: 40-48 B per dynamic block in the skeleton (DEFLATE's own header is 40-44 B); skeleton =
  0.0-0.1% of the compressed size (zlib memLevel 1: 11.9% because it emits 4048 blocks per stream).
* Reset penalty of block-local E2 (models reset per unit, no sharing): +3% at 16 KiB, +8% at 4 KiB, +12% at 2 KiB units
  (Go L6: 92.5% monolithic, 95.4 / 100.3 / 104.1).
* Throughput (single thread): E1 encode 23-91 MiB/s of plaintext; E2 encode 8-10, decode+replay+CRC 14-17 MiB/s.

## 6D/6E. Family cost: new plaintext + new units + references + skeleton

Incremental cost of update revisions r1..r3 as % of their compressed bytes, production pack rule, **35 B per reference**
(96 B in the second table). Plaintext-only lower bound (`oracle-logical`, no reconstruction path): 0.3-1.4% for the Go families.

Go compress/flate (synthetic text, 12 MiB plaintext per revision):

| family (35 B/ref) | CDC | Preflate+CDC | explicit IR mono | explicit block | tok1k | tok2k | tok4k | tok8k | tok16k | tok64k |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| L6 insert-mid | 52.2 | 12.2 | 93.2 | 84.6 | 15.6 | **13.7** | 15.0 | 21.3 | 29.7 | 44.3 |
| L6 insert-begin | 100.1 | 13.3 | 92.9 | 93.0 | 19.6 | **20.6** | 26.5 | 38.9 | 55.7 | 89.0 |
| L6 delete | 50.3 | 10.0 | 93.9 | 54.6 | 16.5 | **14.6** | 16.8 | 23.1 | 32.3 | 46.2 |
| L6 replace | 34.4 | 13.7 | 93.2 | 39.8 | 10.9 | 6.0 | 3.7 | 2.5 | **2.0** | 2.1 |
| L6 append | **2.3** | 10.0 | 92.8 | 90.9 | 10.2 | 5.4 | 3.0 | 1.6 | **1.0** | 1.0 |
| gzip L6 append | **0.4** | 2.7 | 93.0 | 3.1 | 10.5 | 5.6 | 3.2 | 2.0 | 1.4 | 1.2 |
| L9 insert-mid | 51.5 | 6.5 | 90.5 | 45.5 | 12.9 | 8.8 | **7.6** | 8.8 | 12.5 | 26.9 |
| L1 insert-mid | 50.1 | **19.2** | 77.8 | 42.9 | 54.7 | 48.4 | 44.6 | 42.4 | 41.1 | 40.0 |
| fixed-Huffman emitter insert-mid | 50.4 | **0.5** | 76.8 | 39.6 | 7.7 | 4.2 | 2.6 | 1.7 | 1.2 | **1.1** |
| ZIP of deflated members, meta-only change | 42.3 | 0.1 | 0.2 | 0.2 | 3.7 | 1.9 | 1.1 | 0.6 | 0.4 | 0.2 |
| ZIP, member content change | 9.5 | 8.3 | 8.4 | 8.4 | 11.9 | 10.1 | 9.3 | 8.8 | 8.6 | 8.4 |

Same families at 96 B/ref: L6 insert-mid CDC 52.3, Preflate 12.4, tok2k 21.7, **tok4k 19.3**, tok16k 30.9; L6 append CDC 2.4,
Preflate 10.2, tok4k 7.3, **tok16k 2.2**, tok64k 1.5; L6 replace tok16k 3.2; L9 insert-mid tok4k 11.8 / tok8k 11.0 (best);
fixed insert-mid tok64k 1.4. At production reference size the best unit size grows 2-4x; the references themselves
become the dominant term below 4 KiB.

Producers outside the Go standard library (python zlib, GNU gzip, Info-ZIP; 6 MiB plaintext per revision), 35 B/ref:

| family | CDC | Preflate+CDC | explicit mono | tok2k | tok4k | **tok16k** |
|---|---:|---:|---:|---:|---:|---:|
| zlib L1 insert-mid | 51.8 | **1.2** | 97.2 | 8.4 | 6.6 | 5.5 |
| zlib L6 insert-mid | 54.3 | 5.8 | 99.0 | 9.3 | 6.9 | **5.1** |
| zlib L6 insert-begin | 99.4 | 4.3 | 97.6 | 7.3 | 5.0 | **3.4** |
| zlib L6 repeat-replace | 47.5 | 7.0 | 79.0 | 11.0 | 7.4 | **5.5** |
| zlib L9 insert-mid | 50.7 | 4.6 | 97.9 | 8.1 | 5.8 | 4.6 |
| zlib L6 memLevel 1 insert-mid | 51.6 | **8.0** | 101.6 | 20.7 | 18.9 | 17.2 |
| zlib L6 `Z_FILTERED` insert-mid | 54.6 | 12.4 | 81.0 | 11.7 | 9.4 | **7.6** |
| zlib L6 `Z_RLE` insert-mid | 46.0 | **0.8** | 1.1 | 3.7 | 2.5 | 1.4 |
| zlib L6 `Z_FIXED` insert-mid | 54.2 | 5.1 | 79.9 | 7.8 | 6.0 | **5.0** |
| GNU gzip L1 insert-mid | 52.0 | **3.4** | 99.4 | 9.9 | 7.9 | 7.2 |
| GNU gzip L6 insert-mid | 50.5 | 6.3 | 99.5 | 9.7 | 7.5 | **6.1** |
| GNU gzip L6 insert-begin | 96.1 | 4.1 | 97.3 | 7.3 | 4.9 | **3.4** |
| GNU gzip L9 insert-mid | 53.7 | 5.0 | 98.2 | 8.4 | 5.9 | **4.4** |
| Info-ZIP L6 insert-mid | 50.9 | 6.4 | 99.6 | 9.7 | 7.2 | **5.6** |

### Cost structure and entry cost (L6 insert-mid, 35 B/ref, tok4k)

Per update revision: new plaintext 0.6% + new units 11.8% + references 2.4% + skeleton 0.3% = 15.0%. The plaintext is already at its
lower bound; what is left is unit bytes (the token noise) and references. The explicit IR's **entry cost** (r0, % of the original
stream) is plaintext ~100% + IR 92% = **208%** (L1 184, fixed emitter 159), against 100% for CDC and 118% for Preflate. Cumulative
cost over r0 + 3 updates, % of all four revisions' compressed bytes (35 B/ref):

| family | CDC | Preflate+CDC | explicit mono | explicit tok4k | explicit tok16k |
|---|---:|---:|---:|---:|---:|
| L6 insert-mid | 64.2 | **38.7** | 119.3 | 63.2 | 72.6 |
| L6 insert-begin | 100.1 | **39.6** | 119.2 | 72.0 | 92.2 |
| L6 delete | 62.7 | **37.1** | 119.9 | 64.6 | 74.5 |
| L6 replace | **50.8** | 39.8 | 119.4 | 54.7 | 51.8 |
| L6 append | **26.7** | 37.1 | 119.0 | 54.2 | 51.1 |
| L9 insert-mid | 63.7 | **32.9** | 116.9 | 57.1 | 59.2 |
| L1 insert-mid | 62.6 | **43.9** | 102.2 | 79.4 | 75.4 |
| fixed-Huffman emitter insert-mid | 62.8 | **19.1** | 95.4 | 41.6 | 39.3 |
| python zlib L6 insert-mid | 65.7 | **31.1** | - | 57.6 | 54.6 |
| GNU gzip L6 insert-mid | 62.9 | **31.7** | - | 58.3 | 55.6 |

Break-even of tok4k against CDC on insert-mid (entry +108 points, saving 37 points per revision) is ~3 updates; against Preflate it
needs >100 revisions on zlib-family data (entry +90, saving <1 point) and never on Go. **An explicit IR is evidence for locality, not a
storage format.**

### Append (6E)

Append was Preflate's clearest regression (10.0% vs CDC 2.3%: the global blob is paid again). Block-local units fix it: unchanged
units are reused, only the tail unit(s) plus a new skeleton are new. With 16 KiB units append costs **1.0% (35 B) / 2.2% (96 B)**, at or
below CDC (2.3% / 2.4%), and 64 KiB units reach 1.0 / 1.5%. Smaller units lose to CDC purely on references (tok1k: 10.2% at 35 B, 26.1% at 96 B).
Native-block units are no help (3 blocks, the last one changes: 90.9%) and neither is the monolithic IR (92.8%). The adaptive raw-CDC
fallback stays mandatory (Z3-07 chooser): gzip append is 0.4% under CDC against 1.0-1.4% for any IR.

### Plaintext chunking (semantic CAS) matters

Cutting the plaintext at unit boundaries instead of CDC (`-pu`) costs more everywhere (L6 insert-mid tok16k: 31.9 vs 30.9; block: 174.7 vs
84.8) because unit cuts drift with token noise while CDC boundaries follow the content; plaintext stays a separate CAS.

## 6F. Exactness, malformed input, fuzzing

* Every lab row of every IR representation, every revision, every family rebuilt **bit-exactly** from skeleton + units + semantic
  plaintext chunks fetched from the store (length, SHA-256 and `bytes.Equal` against the original stream; streams embedded in gzip and ZIP
  keep wrappers inline). 100% exact in all runs reported here; streams the parser rejects fall back to CDC of the compressed bytes
  (none occurred; the stored-only and Huffman-only families reconstruct).
* `go test ./ir ./lab ./deflate` covers: round trip of {E1, E1+flate, E2} x six policies over Go levels 0-9, Huffman-only, mixed stored,
  fixed-Huffman emitter, empty and one-byte streams, a stream spelling length 258 as symbol 284+31; every strict prefix of the
  skeleton rejected; 400 random single-byte mutations of skeleton / unit / truncated unit / plaintext per codec never yield a
  different "successful" stream; garbage decoders cannot claim a different plaintext length; limits (block count, varint overflow, token
  counts over the plaintext bound, trailing bytes, non-minimal varints) fail cleanly; units contain no literal values; encoding is
  deterministic; an insertion leaves >=60% of unit bytes shared.
* Fuzz targets `FuzzUnitDecode`, `FuzzSkeleton` (also checks canonical re-marshal), `FuzzReconstruct`, `FuzzEncodeDecode`; see
  `Z3_07_PREDICTIVE_RECONSTRUCTION.md` for the campaign summary (same targets cover the predictive codec).
* Decoding allocates in proportion to the plaintext the caller already holds (every token covers >=1 byte, token and plaintext counts
  are cross-checked against the skeleton before allocation); skeleton and unit counts are capped.

## CPU, memory and streaming state

* Parse (Z3-05) 60-110 MiB/s plaintext; E1 encode 50-90; E2 encode 8-10 and decode+replay+CRC 14-17 MiB/s; unit cutting adds one
  gear-hash pass over the plaintext (not separately timed).
* Memory (whole-stream, lab): parsed stream 4 B/token + plaintext (31.8 MiB for a 16 MiB-plaintext stream, Z3-05); `ir.Encode` adds a
  flattened token copy (4 B/token) and per-unit run lists; decoding rebuilds tokens (4 B/token) before replay.
* What a streaming/windowed implementation needs (not built): encoder = inflate state with a 32 KiB window and the current block
  header, the cutter's 8-byte rolling hash plus counters, a per-block table accumulator (~45 B/block); decoder = per-unit decode,
  the current block's tokens and a bit writer, with random access to the plaintext range `[unit start - 32 KiB, unit end)`.
  The IR was kept independently addressable for exactly this: a unit is decodable from its bytes + its plaintext range.

## Gate

1. A compact exact codec is materially smaller than naive IR: **E2 0.52x of E0** (92.5% vs 177.4% of compressed size). Pass.
2. Block-local incremental accounting works: unit identity by content, per-revision new/reused units, reference and skeleton bytes. Pass.
3. Exact replay survives encode/decode: all rows exact, tests above. Pass.
4. Units reuse across realistic revisions: 72-99% of plaintext coverage after the edit (token-run units), 0-49% for native blocks. Pass
   (and it identifies that block units fail).
5. Direct comparison with CDC and Preflate in one accounting (`z3ir family/matrix`). Pass.
6. Malformed metadata fails safely. Pass.
7. No persistent production format changed (`zeros3.go`, root `go.mod` untouched; no store/manifest/journal change). Pass.

## Conclusions for the next step

1. The exact-metadata information content is in the distances (14 of 18 bits per match). An explicit IR is bounded below at ~0.76-0.98x of
   the compressed bytes; block-local reuse brings family cost near Preflate for zlib-family producers (5.1% vs 5.8% for zlib L6), well
   below CDC for Go (13.7% vs 52.2%), and fixes the append regression - but its entry cost is 2x.
2. Units must be content-cut at token granularity; 2-16 KiB depending on producer noise and reference size. Reference size matters as
   much as unit size: a binary reference (35 B) halves the optimum unit size relative to the 96 B manifest entry.
3. To remove the remaining unit bytes (the 11.8% above) the metadata must be *predicted from the plaintext*, with corrections: Z3-07.
