# B2: seekable byte-exact gzip and zstd reconstruction

Linux amd64, Go 1.27.0, 4 vCPU. Cold reads drop the page cache and empty the representation caches; chunk
cache off. Medians through the production GET path, exact SHA-256 of every range checked against the original file.
Raw rows: [measurements.jsonl.gz](measurements.jsonl.gz) (`tables.py`), stores: [fixtures.jsonl](fixtures.jsonl),
inputs: [corpus.sha256](corpus.sha256). All four C5 Coreutils fixtures were reproduced byte-exactly.

## Bottleneck (baseline `b2cde27`)
* Authority gzip/zstd bodies had no range decoder: any range replayed and materialized the whole object, fetching
  77–79 MB of CAS (all 64.5 MB of tar plaintext, units, tables) for 4 KiB. Cold 4 KiB: gzip 1.64 s wall / 5.0 s CPU
  (four decode workers), zstd-3 2.80 s single-threaded. C5's 9.5 s was a shared-host sample of the same path.
* The cost is producer reconstruction, not fetching: the exact-DEFLATE predictor's match search is ~70% of CPU,
  ~2.5 ms per IR unit; zstd re-encodes every block's entropy streams and match-model ranks in order.
* Unused restart structure: IR units already decode from plaintext alone (their 128 KiB warm-up is plaintext),
  and the encoder computed each unit's output bit offset but discarded it. zstd sequences are stored as coded
  offset values (no repeat-offset history); inherited Huffman/FSE tables are verbatim in earlier skeleton records;
  only rebuilt compressed-block sizes were missing.
* Unavoidable preceding work: a predicted DEFLATE header needs all tokens of its block (2–3 units); zstd
  model-coded blocks need the frame window of plaintext (2 MiB at level 3) indexed.

## Architecture
* **Seek index** after the recipe: gzip, per-unit bit deltas; zstd, every compressed block's rebuilt size
  (Coreutils: +5.6 KB per gzip, +1.7 KB zstd). Built for top-level authority codec containers only.
* **Range execution**: header/trailer bytes come from the recipe; DEFLATE restarts at the last unit starting before
  the range, recomputes a predicted header from its block, resumes the bit writer and clips; zstd lays out frames
  and blocks, derives the inherited tables from skeleton records (a fallback block from its original bytes), loads
  the model window and rebuilds blocks. Plaintext is read by range from the derived child, a sliding window shared by
  overlapping unit warm-ups. Ranges ≥512 KiB (gzip) / 1 MiB (zstd) and whole zstd reads are cut at restart points
  into pieces run by up to four workers and written in order.
* **Integrity**: inputs are authenticated (representation digest covers the index; components SHA-256); every whole
  decode compares replayed unit bit offsets / block headers with the index, so admission proofs and deep verify
  establish it. Ranges check each unit start and block size they pass; output is not re-hashed (B1 model); whole
  reads keep the object digest check. Damaged units, plaintext or index fail closed (tests).
* **B1 integration**: rep bodies in ranged authority contexts (delta bases, executor intervals) read through the
  seeker without materializing; parsed seekers live in the representation read cache.
* **Format**: store feature `cxseek`; the B1 build refuses such a store by name without touching it; closure
  transfer carries it; `revert -all` clears it. Records without an index read as before (`compat` rows). A plaintext
  that is not a concatenation of stored components (nested codec members) stays on whole replay.
* **Read-ahead**: virtual components prefetched a 64-chunk pack run on every miss; now only as far as the
  sequential run reaches (TypeScript 4 KiB: 5.5 MB → 0.95 MB CAS, 38 → 19 ms; scans unchanged).

## Results, Coreutils 9.7 (wall ms; logical CAS of representation reads)
| | CDC | replay `b2cde27` | seek replay |
|---|---:|---:|---:|
| gzip-6 4 KiB | 0.3 | 1640 (77.3 MB) | **18.5** (0.92 MB, 5.5 units) |
| gzip-9 4 KiB | 0.4 | 1623 (77.5 MB) | **17.7** (0.94 MB) |
| zstd-3 4 KiB | 0.3 | 2804 (78.7 MB) | **49.4** (2.84 MB, 2.36 MB plaintext) |
| gzip-6 / zstd-3 1 MiB | 4.8 / 4.6 | 1688 / 2561 | 136 / 218 |
| gzip-6 / zstd-3 13 MB range | 121 / 42 | 1931 / 2453 | 1584 / 1045 |
| gzip-6 / zstd-3 whole | 80 / 69 | 1673 / 2339 | 1517 / 1031 |
| allocated store bytes | 47,058,944 | 30,670,848 (−34.8%) | 30,687,232 (−34.8%) |

zstd-19 stayed CDC in both replay stores (no admissible representation), as in C5. Repeated 4 KiB reads regenerate
(no decoded-output cache): 29–38 ms. Long gzip ranges cost the CPU of whole replay; parallel zstd pieces add ~15% CPU for their windows.

## Other families
* TypeScript 5.6.2/5.6.3/5.7.2 npm tarballs (node zlib): the planner keeps CDC (estimated saving 8% < 10%), the
  less favorable case. Forced admission: 9,687,040 vs CDC 12,627,968 bytes (−23%); 4 KiB 19 ms, 1 MiB 210 ms,
  whole 0.55–0.68 s (CDC 0.3 ms, 5–10 ms, 11–28 ms).
* Small artifacts (`testdata/b2-seek`, 97–188 KB): 4 KiB 6–12 ms against 9–50 ms whole replay.

## Limits and next target
* zstd restarts are dominated by the model window (2.36 MB read and hashed; 8 MiB at level 19). Recording each
  block's model reach at encode time (a change to the ported model code) would bound it to what ranks visit.
* gzip ranges pay the predicted header's block and the 128 KiB warm-up; whole and long gzip reads remain CPU-bound
  by the predictor (seek does not reduce them) and fetch 78–86 MB of CAS for a 15 MB object.
* The codec table (~340 KB for Coreutils gzip) is read whole by the first range per process.
* Ranged rep bodies re-read their plaintext window per call: authority contexts have no close hook to keep sources.
* Next: bound the zstd model window, split the gzip table, and give hot ranges a verified output cache.

## B1 follow-ups
F1: partial reads bind to the record's content-addressed pieces and proven bases; the record (CRC, filed under the
content SHA, published after a whole-object proof) is the trust root, as for pin records. `TestAuthorityPartialReadBinding`
covers substituted envelope, substituted base and an inconsistent ZW2 directory (fail closed; deep verify flags it).
F2: a parsed program larger than the budget is executed without being retained (`TestAuthorityProgramCacheBound`).
F3: B1 executor, chunk cache, ZW2 programs and concurrent reads race-clean; new concurrent test over shared caches.

## Reproduce
```sh
python3 testing-harnesses/testdata/b2-seek/gen.py OUT   # fixture regeneration (python-zstandard 0.25.0)
ZEROS3_B2_CORPUS=<corpus> ZEROS3_CX_DIR=<dir> go test -tags b1rangeexperiment,cxrangeexperiment -run '^TestCXFixtures$' .
ZEROS3_B1_COLD=1 ZEROS3_CX_OUT=rows.jsonl ZEROS3_B2_CORPUS=<corpus> ZEROS3_CX_DIR=<dir> \
  go test -tags b1rangeexperiment,cxrangeexperiment -run '^TestCXRangeBench$' -v .
```
`<corpus>` holds `gz/coreutils-9.7.l{6,9}.tar.gz` and `zstd/coreutils-9.7.l{3,19}.tar.zst` (C5's `make_structured.py`);
`ZEROS3_CX_INPUTS` replaces them (TypeScript: the registry tarballs). Lanes: `cdc`, `replay` (C5 policy), `forced`.
