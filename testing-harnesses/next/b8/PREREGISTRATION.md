# B8 pre-registration: cross-format canonical representations

Fixed before any comparative B8 measurement. Already known at this point (baseline audit, not B8 results): the
B3/B7 TypeScript discrepancy diagnosis (codec replay disabled by default in `authority optimize`; with it enabled
the B6 search still keeps CDC although the all-replay selection prices 9.66 MB against CDC 12.60 MB), and the
exact-DEFLATE residual sizes of the corpus producers (2.9-5.6% of the compressed stream).

## Families (inputs: `make_inputs.py`, hashes: `corpus.sha256`)
Positive, real producers:
* **ts**: TypeScript 5.6.2 / 5.6.3 / 5.7.2 npm `.tgz` (node zlib), one history key.
* **django**: Django 5.1.1 / 5.1.2 / 5.1.3 PyPI sdist `.tar.gz` (setuptools/Python gzip) and wheel `.whl`
  (Python zipfile, DEFLATE), six objects under two keys. Cross-container: wheel member bytes are ~98% contained
  in the sdist's members.
* **cu-gz**: GNU Coreutils 9.5 / 9.6 / 9.7 `.tar.gz` as published (GNU gzip).
Positive, constructed wrappers of real content (one controlled re-encoder, python-zstandard 0.25.0):
* **wrap**: Coreutils 9.7 as GNU `.tar.gz`, raw `.tar` (decoded from GNU `.tar.xz`) and `.tar.zst` (level 3),
  plus 9.6 `.tar.zst` (level 3): byte-stream equivalence across three wrappers and one version step.
* **ts-mix**: TypeScript 5.6.2 `.tgz` (npm), 5.6.3 raw `.tar`, 5.7.2 `.tar.zst` (level 3): a history whose
  versions change wrapper.
Negative controls:
* **random**: B6's three random files. **lone**: unrelated compressed objects with no relative
  (requests 2.32.3 sdist and wheel, Coreutils 9.5 `.tar.xz` (unsupported wrapper)).
* **zst19**: Coreutils 9.6 / 9.7 `.tar.zst` level 19 (replay inadmissible in B2/B3; must stay as before).

## Configurations (archive and mixed profiles each; hot on ts and django)
1. `cdc`: plain CDC.
2. `b7`: B7 `authority optimize` as committed (its actual default: codec replay not eligible).
3. `b7r`: B7 with existing codec replay explicitly eligible (`-codec-replay`), B7 search.
4. `b7rg`: existing codec-replay options with B8's search changes (group start, small exact search,
   incumbent) and B8's replay read pricing. Isolates search from canonical gains.
5. `b8s`: B8 canonical options with the unchanged B7 search.
6. `b8`: B8 canonical options, B8 search and pricing (the B8 default under `-canonical`).
Ablations on the positive families: `b8-nodelta` (canonical nodes may only be CDC: pure stream sharing),
`b8-nozip` (no ZIP adapter; django only).

## Criteria (allocated bytes after optimize + gc + repack + compact; every lane deep-verifies)
* Strongest pre-B8 baseline per family = min(`b7`, `b7r`). Canonical gain = `b8` vs `b7rg` and vs that baseline.
* `B8_CANONICAL_EFFECTIVE`: on at least three of {ts, django, cu-gz} (independent producers) `b8` archive stores
  at least 15% fewer allocated bytes than the strongest pre-B8 baseline, with at least 10% attributable to
  canonical sharing (`b8` vs `b7rg`), and all negative controls within ±0.5% of `b7`.
* `B8_CANONICAL_SPECIALIZED`: at least one positive family meets the archive bar, or gains are confined to
  constructed wrapper families.
* `B8_CANONICAL_NO_GENERAL_WIN`: no real family gains 5% over `b7rg`.
* Read cost: under the hot profile `b8` must not select a representation whose modeled 4 KiB read exceeds the
  hot budget; reported cold 4 KiB / 64 KiB / 1 MiB / whole latencies through the production reader for `b8`
  archive and mixed on ts, django, cu-gz.
* Planner accuracy: plan bytes equal the materialized model exactly (as B6/B7).
* Every byte of every read and every original object is checked (SHA-256 of whole objects).
