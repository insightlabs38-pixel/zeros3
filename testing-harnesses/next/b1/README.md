# B1: indexed range execution through nested delta representations

Linux amd64, Go 1.27.0, 4 vCPU, `GOMAXPROCS` 4. Page cache dropped before every measured read
(`ZEROS3_B1_COLD=1`); store chunk cache off unless marked `cc` (then 32 MiB, emptied before each
read). "CAS" is logical CAS bytes fetched by the read's context (the pricing unit); wall is the
instrumented read, which matched the signed-GET path (`GetObjectRange`) within noise. Raw rows:
[measurements.jsonl.gz](measurements.jsonl.gz) (`tables.py`), store sizes: [fixtures.jsonl](fixtures.jsonl),
inputs: [corpus.sha256](corpus.sha256).

## Bottleneck (baseline `d309426`, trace of cold 4 KiB reads)
* A ZW1 frame was authenticated only by its output digest, so any byte of a frame required
  reconstructing the whole frame, and every base page that frame touched recursed into whole base
  frames: 2–17 frames (mean 6.9–7.3 MB) decoded per 4 KiB at depth 2, 92–98% of the CAS bytes being
  depth-0 chunks pulled in by that fan-out. `xd` envelopes already ran COPY operations precisely (0.5–0.7 MB).
* Whole reads decoded 1.03 GB of frames and fetched 4.5 GB for a 63 MB object: base chunks fell out
  of the 8 MiB request cache because COPY sources span the whole base.
* Remaining after precise execution: piece granularity (32 KiB pieces for 4-byte magics, tables and
  10 KB programs), whole 1 MiB frame programs (72–131 KB deflated) per touched frame, and depth-0
  CDC chunks (95–100 KB mean), each pulled by a short COPY.

## Architecture
* **Input-authenticated execution.** A partial-frame read executes only the operations producing the
  range: programs are authenticated by their CAS pieces, bases by their own records, recursively
  (bounded by the existing depth, cycle and open-body limits). COPY sources are sorted per base and
  merged when they overlap or nearly touch, so each needed interval is requested once (`authExecState`,
  shared by `xd` and ZW1/ZW2; multi-base aware). Frames a read covers whole still verify their output
  digest, as do proofs and deep verify (scan contexts never use the ranged path).
* **ZW2** (new default for new windowed records, store feature `zw2`): ZW1 whose frame programs are
  128 KiB restartable segments with a per-frame directory; the reader fetches and inflates one segment.
  Older builds refuse a `zw2` store by name; closure transfer carries the feature; `revert -all` clears it.
  `authority migrate -segment-kib -1` keeps writing ZW1. ZW1 and `xd` remain readable.
* **Aligned pieces** (no format change): delta blobs are cut at magic, header, program/segment and
  table boundaries. Old readers read new stores unchanged (`compat` rows: exact, old cost).
* Per-body LRU of parsed programs (8 MiB); served reads share verified base chunks through the
  existing store chunk cache (doorkeeper admission); pricing and proofs read storage.
* Admission probes now price the ranged reader, so read-amplification gates see the real cost.

## Results (CAS mean / wall median; depth-2 target unless noted)
| Coreutils 9.5→9.6→9.7 | baseline | new reader, old store | + aligned pieces | + ZW2 | ZW2, cc |
|---|---:|---:|---:|---:|---:|
| 4 KiB, 1 MiB/256 KiB frames | 31.09 MB / 172 ms | 612 KB / 5.6 ms | 433 KB / 4.8 ms | **369 KB / 3.0 ms** | 369 KB / 3.9 ms |
| 4 KiB, 1 MiB frames | 89.28 MB / 527 ms | 620 KB / 6.3 ms | 454 KB / 5.5 ms | 368 KB / 3.0 ms | |
| 4 KiB, depth 1 | 8.60 MB / 57 ms | 502 KB / 4.3 ms | 415 KB / 3.8 ms | 369 KB / 2.8 ms | |
| 1 MiB range | 123.9 MB / 667 ms | 8.48 MB / 67 ms | 8.14 MB / 61 ms | 7.79 MB / 49 ms | 7.79 MB / 59 ms |
| 16 MiB range | 871.9 MB / 4.20 s | 64.2 MB / 355 ms | 63.7 MB / 383 ms | 65.8 MB / 378 ms | 49.7 MB / 319 ms |
| whole 63 MB | 4540 MB / 22.9 s | 259 MB / 1.50 s | 254 MB / 1.49 s | 245 MB / 1.42 s | 113 MB / 0.87 s |
| allocated store bytes | 22,310,912 | same store | 22,323,200 | 22,679,552 | CDC 45,035,520 |

| Go 1.27.0→1.27.1→1.27.4 tools tar | baseline | new reader, old store | + aligned pieces | + ZW2 | ZW2, cc |
|---|---:|---:|---:|---:|---:|
| 4 KiB, 1 MiB/256 KiB frames | 16.22 MB / 93 ms | 477 KB / 3.8 ms | 331 KB / 3.6 ms | **231 KB / 3.2 ms** | 231 KB / 3.0 ms |
| 4 KiB, 1 MiB frames | 41.06 MB / 217 ms | 636 KB / 7.6 ms | 519 KB / 7.0 ms | 224 KB / 3.0 ms | |
| 1 MiB range | 18.00 MB / 129 ms | 2.98 MB / 26 ms | 2.77 MB / 26 ms | 2.28 MB / 20 ms | 2.28 MB / 18 ms |
| whole 93 MB | 2664 MB / 20.4 s | 459 MB / 4.19 s | 450 MB / 4.20 s | 385 MB / 3.17 s | 231 MB / 2.31 s |
| allocated store bytes | 71,815,168 | same store | 71,843,840 | 72,134,656 | CDC 93,609,984 |

Controls: CDC 4 KiB 163 KB / 148 KB (GET 1.0 / 1.3 ms); a pinned depth-0 whole read 0.33 s / 0.63 s.
Repeated 4 KiB (warm chunk and page caches): 0 CAS bytes, 0.3–0.8 ms. `xd` depth 2 (already precise):
4 KiB 675→571 KB and 459→380 KB from aligned pieces. True multi-base `xd-merge` (half 9.5 + half 9.6,
bases {9.6, 9.5}, depth 2, shared base): 4 KiB 542 KB / 4.1 ms; whole 452 MB / 2.6 s, 126 MB / 1.1 s cc.
Depth one to two at 4 KiB: 369 KB → 369 KB (Coreutils), 193 → 231 KB (Go); baseline grew 3.6–4.6×.

Storage: aligned pieces +0.06% / +0.04%; ZW2 +1.6% / +0.4% over ZW1, from deflating segments
separately. Program growth (depth 1 / depth 2): 128 KiB +5.7 / +3.3% (Coreutils), +1.0 / +0.5% (Go);
64 KiB +10.5 / +8.1%, +2.3 / +1.5%; 16 KiB +24 / +19%, +7.7 / +6.0%. ZW2 keeps 98.4% of the
Coreutils and 98.7% of the Go saving against CDC.

## Negative and bounded results
* Window size alone: with precise execution 256 KiB vs 1 MiB frames no longer matter for random reads.
* Whole-read leaf caching (trace simulation): LRU instead of FIFO saved 15–19% at 8 MiB; executing
  4 MiB batches sorted by leaf chunk fetched more than LRU. Whole reads stay 1.9–2.5× CDC bytes with
  the 32 MiB shared cache and 4.0–4.2× without it.
* Depth-0 CDC chunks are now 65–77% of a cold 4 KiB read (286 of 369 KB, 150 of 231 KB). The traced
  leaf intervals priced at fixed pieces: 32 KiB → 76 / 35 KB, 16 KiB → 42 / 20 KB. Re-chunking the
  Coreutils base would add +1.09 MB at 32 KiB and +2.24 MB at 16 KiB (zlib-6 estimate of per-chunk
  DEFLATE; +4.9% / +10% of the store). Not done.

## Remaining limits
Leaf integrity granularity (whole-chunk SHA-256 over per-chunk DEFLATE) dominates cold reads;
whole reads of literal-heavy nested deltas still re-parse evicted programs (Go: 53 MB parsed);
the authority cache (off by default) does not hold parsed programs across requests.

## B2 primitives
`authExecState` (range → merged source intervals over any op stream), restartable program segments
with authenticated directories, unit-aligned piece writing, and the per-context work trace.

## Reproduce
```sh
ZEROS3_B2_CORPUS=<corpus> ZEROS3_B1_DIR=<dir> go test -tags b1rangeexperiment -run '^TestB1Fixtures$' -v .
ZEROS3_B1_COLD=1 ZEROS3_B1_OUT=rows.jsonl ZEROS3_B2_CORPUS=<corpus> ZEROS3_B1_DIR=<dir> \
  go test -tags b1rangeexperiment -run '^TestB1RangeBench$' -timeout 60m -v .
```
The Go family sets `ZEROS3_B1_INPUTS` to three tars of `bin` and `pkg/tool` from the module-proxy
toolchain zips (`tar --sort=name --mtime=@0 --owner=0 --group=0`). `ZEROS3_B1_CC_MIB=32` enables the
chunk cache; `TestB1Anatomy` and `TestB1SegmentCost` report structure and segment cost.
