# Z3-02 — Initial representation characterization

Measurement only. `zeros3.go`, the store format, manifests and the journal are
untouched. The baseline "cdc" row is a copy of production CDC v1 verified
against the real `zeros3.go` by `TestCDCMatchesProduction`.

```sh
cd testing-harnesses/z3
go run ./cmd/z3corpus gen -out "$TMPDIR/z3c" -size-kib 4096     # seed 1, 3 revisions, 61 families, ~1 GiB
go run ./cmd/z3lab -corpus "$TMPDIR/z3c" -json "$TMPDIR/z3.json" -per-family
```

2024 rows (61 families x 4 revisions x applicable representations), all
non-oracle rows byte-exact by length + SHA-256, sampled range reads byte-exact.
Whole run: a few minutes on 4 cores.

## What is measured

Update cost = bytes a representation must add for revisions r1..r3 beyond what
it already stores for the family, plus recipe bytes (96 B per component
reference, inline for regions < 512 B). "packed" applies the production pack
rule (DEFLATE, kept only if it saves 1/16) to new components. Percentages are of
logical bytes; "vsCDC" is cost relative to current ZeroS3 CDC (lower wins).
Sums are over every family of the format.

| format | families | CDC packed% | best exact rep | packed% | vsCDC | best oracle* |
|---|---|---|---|---|---|---|
| gzip | 9 | 46.9 | none better than CDC (no segments) | 46.9 | 100% | plaintext CDC 1.5% (3%) |
| zip (stored+deflate) | 10 | 10.9 | struct-cdc | 2.3 | 21% | member logical 2.4% |
| pytorch-zip | 3 | 14.3 | delta-xorz | 4.1 | 28% | - |
| safetensors | 9 | 30.4 | delta-xorz | 11.9 | 39% | - |
| gguf | 4 | 31.7 | delta-xorz | 2.5 | 8% | - |
| zbin (synthetic binary) | 4 | 32.9 | delta-xorz | 15.2 | 46% | ref-masked CDC 4.5% (14%) |
| car | 5 | 52.8 | struct-seg | 10.6 | 20% | - |
| pagedb | 3 | 12.0 | fixed-4k = struct-seg | 2.5 | 21% | - |
| colstore | 3 | 12.3 | delta-xor | 9.7 | 79% | - |
| ndarray | 2 | 3.5 | delta-xorz | 1.9 | 54% | chunk logical 2.1% |
| protorec | 3 | 4.0 | struct-seg | 2.4 | 60% | - |
| raw controls | 6 | 23.3 | (CDC; fixed-64k is 163% of it) | - | - | - |

Selected families (CDC packed% -> best exact -> best oracle):

| family | CDC | best exact | oracle |
|---|---|---|---|
| gzip-L6-insert-begin | 100.1 | none | 0.9 |
| gzip-L6-insert-mid / delete / L1 / L9 | 50-52 | ~50 (fixed-64k) | 1.0-2.8 |
| gzip-L6-append | 0.6 | - | - |
| car-reorder / car-remove | 93 / 65 | struct-seg 0.4 | - |
| car-overlap | 97 | struct-seg 47.5 (floor: half the blocks are new) | - |
| bin-rebase | 61 | delta-xorz 1.0 (struct-seg 59) | ref-masked 0.9 |
| bin-insert-func | 62 | struct-seg 59 | ref-masked 6.5 (+refs 11.9) |
| st-modify-sparse | 10.1 | delta-xorz 0.1 (struct-seg 8.4) | - |
| st-finetune-dense | 98.5 | delta-xor 4.4 (struct-seg 98.5) | - |
| st-reorder-data | 22.3 | struct-seg 0.0 | - |
| zip-deflate-meta / zip-stored-meta | 42 / 19 | struct-seg 0.1 / 0.0 | - |
| st-unrelated | 98.3 | none (96.4) | - |

## Answers

- **Worst reuse gap:** gzip with an edit near the start (CDC 100% vs ~1%
  plaintext), CAR re-ordering/removal (93%/65% vs 0.4%), and relocated binaries
  (61% vs ~1-7% once reference fields are masked).
- **DEFLATE churn:** a small plaintext edit shifts the whole compressed
  stream after the edit point. CDC retains the prefix only: ~100% rewritten for
  an edit at the start, ~51% mid-stream, ~0.6% for an append. Averaged over the
  gzip families CDC pays 47% of logical size for edits that change a tiny fraction of the
  plaintext; the plaintext oracle pays 1.5%.
- **Structural segmentation alone:** recovers most of the ZIP/CAR/PyTorch-ZIP
  gap (ZIP 10.9 -> 3.1 whole-member; 2.3 with CDC inside members, which matters:
  whole-member storage alone is 167% of CDC when a member is partly rewritten).
  For ZIP, member-level reuse already approaches the decoded-content oracle
  (2.4%), so DEFLATE reconstruction adds little there; it matters for bare
  gzip/DEFLATE streams.
- **Tensor-level reuse vs CDC:** only modest. Safetensors struct-seg is 86% of
  CDC; ZIP-wrapped PyTorch 72%. CDC already reuses unchanged tensors; the gain
  is layout changes (reorder: 22% -> 0%) and little else.
- **Reference shifting vs CDC:** yes, it defeats CDC (bin-rebase 61%,
  bin-insert-func 62%). Section-level segmentation does not help (140% of CDC
  in aggregate: every text section changes). Aligned XOR against the previous
  image recovers a pure rebase (1.0%) because the image size is unchanged, but
  not a function insertion (59%). Reference normalization (oracle) handles
  both: 0.9% and 6.5%, or 11.9% with reference bytes charged verbatim.
  Metadata-only and small semantic changes are already cheap under CDC (2.3% /
  6.3%).
- **CAR:** block segmentation trivially recovers reorder, removal, root-set
  changes and the shared half of overlapping archives.
- **Too weak to prioritize:** workloads where CDC is already at or near the floor
  (ndarray 3.5%, protorec 4.0%, zip-stored change/add/remove 3-6%, GGUF
  metadata edits, safetensors rename/metadata reorder, appends, raw
  insert/delete); unrelated models (98%, nothing helps).

## Caveats (read before quoting any number)

- The corpus is synthetic: random-weight tensors, random-walk columns,
  text-like members. Absolute percentages are not predictions for real data.
- XOR deltas look strong on `st-finetune-dense`, `gguf-requant`, `st-modify-sparse`
  because the generator flips only the low 1-3 mantissa bits of 0.1-5% of
  elements. Real fine-tunes perturb nearly every element; their XOR will
  compress far worse. This is an upper bound pending real checkpoints.
- `delta-xorz` on `zip-deflate-*` (2.1%) comes from incompressible members whose
  stored-block DEFLATE output keeps the same length; it is not evidence that
  XOR works on genuine DEFLATE output.
- Delta bases are read from the previous revision's file; chain walking, base
  availability and reconstruction cost of long chains are not modelled.
- Oracles (`*`) have no reconstruction path and exclude the cost of the
  reconstruction information a real transform needs (e.g. DEFLATE parameters).
- Tiny scale distorts CDC (at 512 KiB per artifact most objects have ~8
  chunks); numbers above use 4 MiB bases. Per-reference recipe cost is a flat
  96 B (`-ref-bytes`); production pack record overhead is not charged.
- Range reads: median read amplification (component bytes fetched / bytes
  requested, 4 KiB-1 MiB reads) is 1.2 for CDC and fixed-64k, 1.9 for whole
  segments, 1.0 for fixed-4k, 11.3 for the raw control. Median reconstruct
  throughput is ~700-860 MiB/s for all concat plans (warm cache).
