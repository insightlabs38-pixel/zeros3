# Z3-08: DEFLATE generalization, streaming and production-shaping research

Laboratory evidence only. `zeros3.go`, the root `go.mod`, store/manifest/journal/pack formats are untouched; every transform below is
rebuilt and compared byte for byte (length, SHA-256, bytes) by the lab, raw CDC stays the floor and the explicit E2 coding the universal
fallback. All numbers are packed bytes as a percentage of the update revisions' compressed bytes unless stated; 35 B per reference unless stated.

```sh
cd testing-harnesses/z3
go run ./cmd/z3fetch -group deflate -out "$TMPDIR/rw" -max-mib 800 && go run ./cmd/z3ir family -corpus "$TMPDIR/rw" -reps cdc,preflate,oracle-logical,irp-tok16k,irp-tok16k-bal,irp-tok16k-xh
go run ./cmd/z3ir fast|producers|modes|stream|range|recipe|lifecycle ...        # see README
```

## Answers

1. **Does the ~3% synthetic result survive real data? Only where the plaintext really repeats; elsewhere nothing can.** 19 real revision
   families (91 pinned files, 600 MB of compressed artifacts) were measured. The semantic floor (plaintext CDC) is the limit:

   | real family (producer) | CDC | Preflate+CDC | **Z3 predictive** | plaintext floor | note |
   |---|---:|---:|---:|---:|---|
   | Debian slim OCI layer (Go gzip) | 99.9 | 99.9 (rejects the stream) | **17.9** | 16.7 | 29 MB layers, 4 builds |
   | TypeScript npm tarball | 100.1 | 70.7 | **41.8** | 39.7 | |
   | pip wheel (zlib via ZIP) | 94.1 | 29.9 | **29.6** | - | 2 370 member streams over 5 wheels |
   | guava / junit jar | 83.5 / 88.8 | 51.1 / 51.1 | **50.9 / 50.9** | - | thousands of members |
   | Go source tarball (Go gzip) | 99.5 | 99.5 (rejects) | **78.2** | 75.3 | |
   | Alpine OCI layer | 100.1 | 100.1 (rejects) | **83.4** | 81.0 | |
   | botocore wheel / F-Droid APK | 88.2 / 73.8 | 77.8 / 69.4 | **77.7 / 70.6** | - | |
   | GNU coreutils / grep tarball (`gzip -9 --rsyncable`) | 100.1 / 97.6 | 100.3 / 99.9 | **106.2 / 105.7** | 100 | every file changes; transform only costs |
   | Django sdist, tokio/syn crates, six, attrs | 100 | 87-110 | 87-115 | 78-109 | plaintext changes everywhere |

   The mechanism that made the synthetic 3% (revisions that share most plaintext) exists in container layers, npm/wheel/jar families and
   rebuilt artifacts, where Z3 cuts the cost 2-5x versus CDC and ties Preflate on zlib producers while also covering Go/GNU streams Preflate
   rejects. It does not exist where release engineering changes every file (tar headers carry the release mtime, so even the plaintext
   CDC floor is 75-100% on tarballs). Byte-weighted over all 19 real families: CDC 94.7, Z3 predictive 66.7 (bin32 recipe). Re-run of the 15
   synthetic families (2 MiB bases): CDC 45.5, explicit 15.5, **predictive 4.5**, floor 2.2 (3.1 at 4 MiB bases: the metadata share is larger
   for smaller objects).
2. **Producer dependence.** 32 producer/level configurations over the same real Go-source plaintext (`z3ir producers`, whole-stream
   correction / 16 KiB-unit correction, % of the stream; incremental vs CDC 100 and Preflate):

   | producer | profile | whole | tok16K | Z3 incr | Preflate incr | floor |
   |---|---|---:|---:|---:|---:|---:|
   | zlib 1.3 L1, GNU gzip -1 | zlib-L1-exact | 0.0 | 0.9 | 14.4 | 13.6 | 13.6 |
   | zlib L6 / gzip -6 | generic | 0.8 | 2.8 | 16.8 | 15.9 | 15.8 |
   | zlib L9 / gzip -9 | zlib-like | 2.2 | 3.8 | 17.8 | 16.8 | 16.7 |
   | zlib memLevel 1 (4 000 tiny blocks) | generic | 2.2 | 3.6 | 15.7 | 14.3 | 14.2 |
   | miniz_oxide L6 / L9 | zlib-like | 1.2 / 2.3 | 2.6 / 3.9 | 17.9 / 18.1 | 19.9 / 16.8 | 16.7 |
   | zlib-ng L6 / L3 / L1 | zlib-like / go-like / zlib-fast-like | 1.3 / 0.8 / 6.1 | 2.1 / 1.8 / 8.0 | 17.4 / 16.3 / 14.7 | 19.3 / 15.4 / 11.2 | 16.1 / 15.4 / 11.2 |
   | Go compress/flate L1 / L5 / L9 | go-like | 12.8 / 7.8 / 2.8 | 14.1 / 8.9 / 4.6 | 22.1 / 20.8 / 19.0 | 83 / **rejected** / 18.7 | 13.8 / 15.4 / 16.9 |
   | libdeflate L1 / L6 / L9 / L12 | go-like / go-like / zlib-like / zlib-like | 1.7 / 2.3 / 5.0 / 11.6 | 2.4 / 4.6 / 7.0 / 13.3 | 16.8 / 19.8 / 20.7 / 24.7 | 15.6 / 22.2 / 20.8 / 32.3 | 15.3 / 16.9 / 17.2 / 18.0 |
   | ISA-L L0-L3 | go-like | 6.0-7.3 | 6.3-7.7 | 12.8-18.3 | 26.8 / 32.0 / **rejected** / 98.8 | 11.7-14.3 |
   | zopfli (5 iterations) | zlib-like | 10.5 | 12.2 | 26.6 | 31.8 | 18.2 |

   Entry cost (r0) 85-127% (zopfli/libdeflate L12 worst, zlib L1 85%). Throughput 11-47 MiB/s of plaintext encode (zlib fast levels fastest,
   zopfli/L12 slowest), 11-55 decode. Residual producer dependence: optimal-parsing and SIMD/hardware compressors (zopfli, libdeflate L12,
   ISA-L, Go L1/L5, zlib-ng L1, miniz L1) cost 4-13% of the stream in corrections whole-stream. Those are not emulated; they degrade
   gracefully to the generic engine and, in the worst case, to explicit coding, never to wrong bytes. Preflate rejects Go L5 and ISA-L L2, nearly fails ISA-L L3 (98.8) and
   Go L1 (83.2), and rejects the real Go-gzip layers; Z3 rejected nothing.
3. **zlib level 1-3 reset penalty: removed.** Exact emulation of `deflate_fast` (zlib's 15-bit hash buckets that count colliding strings in the
   chain budget, `max_insert_length` skipping of match interiors, `nice_length`, MAX_DIST) as profiles 4-6, with the decoder free-running a
   64 KiB warm-up before every unit (approach A+C). 8 MiB of Go source, whole-stream / 16 KiB units, % of stream:

   | producer | old zlib-fast-like profile | emulation, warm-up 0 / 32 / **64** / 128 KiB | exact (whole stream) |
   |---|---|---|---|
   | zlib L1 | 1.0 / 3.97 | 13.4 / 2.3 / **1.20** / 1.03 | 0.29 |
   | zlib L2 | 8.3 / 11.5 | 13.8 / 2.2 / **1.14** / 1.03 | 0.29 |
   | zlib L3 | 13.7 / 16.9 | 14.3 / 2.1 / **1.10** / 1.02 | 0.29 |
   | GNU gzip -1/-2/-3 | 0.9 / 3.8 | | 0.17-0.18 -> 0.99-1.08 at 64 KiB |

   GNU gzip's own `deflate_fast` is matched by the same emulator. The earlier penalty (12 points on synthetic text) is now 0.8-0.9 points
   (adaptive-model learning, ~50 B per unit). Approach B (a hint listing which window positions the compressor did not insert) was rejected by
   measurement: ~900-1 000 long-match boundaries per unit start = ~1 KB per 5.8 KB unit, 20x the residual penalty. Warm-up costs ~25% encode
   throughput (47 -> 40 MiB/s); encode is 3-5x faster than the generic profiles. Unit tests use committed 160 KiB zlib 1.3 level 1-3 streams.
4. **Tiny blocks: worth predicting, and it matters on real data.** Real `gzip --rsyncable` tarballs (grep, coreutils) have empty stored
   sync-flush blocks and ~450 B blocks: the skeleton was 5-6% of the stream, more than the units. `ir/ztrees.go` ports zlib/gzip's `trees.c`
   decisions (heap Huffman with depth tie-break and length limit, `scan_tree`/`send_tree` run rules, `_tr_flush_block` stored/fixed/dynamic
   cost test): **every non-stored block of every zlib 1.3 / gzip 1.12 fixture is reproduced exactly** (L1-3, gzip -9, `--rsyncable`, memLevel 1).
   The skeleton stores one flag per block (kind + header predicted; canonical zero stored padding; "same token count as previous block"):

   | stream | skeleton before | after |
   |---|---:|---:|
   | zlib memLevel 1 (127 blocks, 32 KB) | 4 815 B (15% of the stream) | **168 B** |
   | gzip --rsyncable (36 blocks) | 1 200 B | 158 B |
   | real grep tarballs (skeleton % of stream) | 5.9 | **0.8** |
   | real guava / junit / commons-lang3 jars | 6.9 / 8.7 / 6.1 | 1.3 / 1.8 / 1.2 |
   | zlib memLevel 1, 6 MiB Go source: whole-stream / Z3 incremental | 18.3 / 31.8 | **2.2 / 15.7** (CDC 100, Preflate 14.3) |

   Real incremental effect: grep 110.8 -> 105.7, guava jar 56.5 -> 50.9 (now level with Preflate's 51.1), commons-lang3 95.5 -> 90.6. Block
   boundaries are cheap where they repeat (one byte per block with the "same count" flag); content-defined gzip --rsyncable boundaries cost
   ~3 B each and were not predicted (that would need gzip's rolling-sum rule; the residual is 0.2-0.8% of the stream). Exact skeleton is
   kept as the fallback (`NoHeaderPred`), and the Go-stream headers (Go's own Huffman builder) stay exact.
5. **Repeated distances: no.** New deterministic workloads (`z3corpus plain`: recurring phrases, 64 B records, 4 KiB periodic blocks, C-like
   source, JSON lines, logs, stride-48 binary structs) x 6 producers. Whole-stream corrections, % of stream:

   | variant | phrases | records | periodic | source | json | logs | binary |
   |---|---:|---:|---:|---:|---:|---:|---:|
   | full model | 1.6 | 2.8 | 1.6 | 3.2 | 4.4 | 3.6 | 2.0 |
   | + last-distance context (Z3-07) | 1.6 | 2.8 | **1.6 vs 1.8 without** | 3.2 | 4.4 | 3.6 | 2.0 |
   | + 4-distance cache context | identical to the single-distance context everywhere | | | | | | |

   The repeat-distance context is worth 0.2 points on periodic data and 0.0 on the other six; the explicit E2 coder's 4-entry distance cache
   saves 0.2 points on periodic and **costs 3 points on binary structures** (48.7 vs 45.6 without). Both were **removed** as dead complexity
   (the lazy hint, 30-40% and candidate-length contexts, 20-45%, stay).
6. **Encode speed.** Profiling: candidate search 61% of encode (`window.search`), `insertTo` 11%, the rest range coding. Measures taken:
   pooled generation-tagged window state (no 384 KB zero/alloc per unit): +21% throughput, identical bytes; zlib's `nice_length` stop; chain/candidate
   limits. Three operating points over 9 producers x 3 revisions of real Go source (all rebuilt exactly):

   | mode | incremental | metadata (units+skeleton) | encode MiB/s | alloc per MiB |
   |---|---:|---:|---:|---:|
   | ratio (exhaustive, Z3-07 behaviour) | 17.9 | 1.1 | 11.6 | 10.5 MiB |
   | **balanced** (chain 32/64/16, nice 64-128) | 18.3 (+0.4) | 1.5 | **19.4 (1.7x)** | 3.7 MiB |
   | fast (chain 6-16, no lazy hint on generic/go-like) | 19.0 (+1.1) | 2.2 | **25.3 (2.2x)** | 3.7 MiB |

   Balanced loses 0.4 points for 1.7x; on real families it is +0.1-1.6 points (e.g. Debian 18.2 vs 17.9, TypeScript 43.9 vs 41.8). Exact
   emulation profiles (zlib L1-3) are already 40-55 MiB/s in every mode. Laboratory knob only (`Config.Mode`), not a production flag.
7. **Bounded-memory streaming: demonstrated.** `deflate.StreamParse` (reader-driven bit reader, ring window, handler callbacks),
   `deflate.StreamWriter`, `ir.EncodeStream` / `ir.ReconstructStream`. State: a 512 KiB plaintext ring (32 KiB match window + 128 KiB warm-up
   reach + the current <=64 KiB unit), the current unit's runs, the current block's tokens only while a header may be predicted (<=32 767),
   and the skeleton kept *serialised* (26 B per block + 5 B per unit). The whole-stream parser stays the oracle: with a fixed profile the
   streamed skeleton and every unit are **byte-identical** to `Encode`, all test streams (stored, fixed, mixed, tiny, `HalfReader`/
   `OneByteReader` inputs) rebuild exactly, truncation/corruption fail. Deterministic gzip stream produced on the fly from 16 MiB of Go source,
   encoded to on-disk plaintext/unit files and rebuilt only from them (SHA-256 of the rebuilt gzip == SHA-256 of the produced one, gzip
   trailer CRC checked):

   | plaintext | producer | compressed | encode / decode MiB/s | peak heap (enc / dec) | peak RSS (enc) | skeleton | units |
   |---:|---|---:|---:|---|---:|---:|---:|
   | 64 MiB | gzip -6 | 11.6 MiB | 11.2 / 13.7 | 31 / 41 MiB | 38 MiB | 9.8 KB | 1 879 |
   | 256 MiB | gzip -6 | 46.1 MiB | 11.5 / 13.5 | 38 / 44 MiB | 49 MiB | 40 KB | 7 751 |
   | **1 024 MiB** | gzip -6 | 183.8 MiB | 11.8 / 14.3 | 43 / 53 MiB | 52 MiB | 162 KB | 31 095 |
   | 256 MiB | gzip -1 (emulated) | 53.8 MiB | 22.3 / 32.5 | 39 / 43 MiB | 46 MiB | 38 KB | 7 799 |
   | 256 MiB | Go gzip L5 | 47.4 MiB | 12.1 / 20.1 | 42 / 50 MiB | 51 MiB | 358 KB | 7 782 |

   Memory is flat in the input size (heap 31 -> 43 MiB over 16x the data; the measured growth was the struct-form skeleton, now serialised, and
   GC slack). Throughput equals the in-memory path. **Range reads** (`ir.RangeIndex`, `ir.MaterializeRange`, verified byte-exact on random
   ranges, step 1 and 3, stored/dynamic/predicted-header blocks): a range needs (a) the skeleton, (b) the units covering it plus the units
   of the first/last *block* when headers are predicted (a predicted header needs every token of its block: 7-9 extra units on TypeScript's 7-unit
   blocks, 0 on Go layers), (c) plaintext `[unit start - 64 KiB, unit end)` per unit (warm-up) - and nothing before it. Real objects:

   | object | index | index bytes (% of stream) | 4 KiB range | 64 KiB | 1 MiB |
   |---|---|---|---|---|---|
   | TypeScript npm 5.5 MiB (zlib) | every unit | 3 483 B (0.060%) | 8.9 units, 372 KiB plaintext | 23.6 units, 729 KiB | 243 units, 5.8 MiB |
   | | every 16 units | 242 B (0.004%) | 18.1 units, 577 KiB | 29.6, 902 KiB | 255, 6.2 MiB |
   | Alpine layer 3.4 MiB (Go gzip) | every unit | 879 B (0.026%) | 1.4 units, 189 KiB | 6.7, 307 KiB | 93, 2.5 MiB |
   | | every 128 units | 10 B | 50 units, 1.3 MiB | 72, 1.7 MiB | 134, 3.3 MiB |

   Positioning needs only a per-unit compressed-bit-offset index (0.03-0.06% of the stream, or 0.004% at 1/16 density); no checkpoint of
   decoder state. The fetched plaintext is dominated by the 64 KiB warm-up (a 4 KiB range reads ~90x its size with one unit per entry); the
   skeleton is read whole in this lab (0.1-0.6% of the stream, splittable by block range).
8. **Unit/reference size (Z3-08H).** Real serialised recipes (`lab/recipe.go`, per revision piece list), byte-weighted incremental % (r0 entry in brackets):

   | layout | CDC real | tok4K | tok16K | tok64K | synthetic tok16K / tok64K |
   |---|---:|---:|---:|---:|---:|
   | production `chunkRef` JSON (88 B + digits) | 94.8 | 69.3 | 67.9 | 67.3 | 5.5 / 4.9 |
   | flat 35 B | 94.7 | 67.8 | 66.8 | 66.5 | 4.5 / 4.5 |
   | **32 B digest + varint length** | 94.7 | 67.6 | 66.7 | 66.5 | 4.5 / 4.5 |
   | per-object digest table | 94.7 | 67.6 | 66.7 | 66.5 | 4.5 / 4.5 |
   | 16 B truncated digest | 94.7 | 67.2 | 66.4 | 66.2 | 4.2 / 4.3 |
   | 6 B store locator (no SHA in the recipe) | 94.7 | 66.9 | 66.2 | 66.1 | 4.0 / 4.3 |

   16 KiB remains right for compact references (synthetic: 4K 6.0, **16K 4.5**, 64K 4.5; real: 67.6/66.7/66.5): 64 KiB ties once refs are
   32+varint and wins only with the 96 B JSON ref (4.9 vs 5.5), because larger units cut reference count. So the production-realistic unit
   is 16-64 KiB (16 KiB with compact refs). A per-object digest table saves nothing (no digest repeats inside one object); inline SHA-256 vs locator is
   worth 0.2-0.5 points, not worth giving up self-verifying recipes. Units under 35 B are inlined.
9. **Lifecycle (Z3-08I).** Cumulative physical bytes, % of compressed bytes of r0..rk (`z3ir lifecycle`, bin32 refs):

   | family | revisions | all-CDC | Z at once | Z after 1st update | Z after 2nd | break-even k (Z / Z@1 / Z@2) | r0 entry |
   |---|---:|---:|---:|---:|---:|---|---:|
   | Debian layer | 4 | 99.9 | **40.2** | 79.0 | 101.7 | 1 / 3 / never | 107 |
   | pip wheel | 5 | 95.1 | 46.1 | 69.9 | 82.9 | 1 / 3 / 4 | 106 |
   | TypeScript npm | 4 | 100.1 | 59.2 | 81.2 | 103.0 | 1 / 3 / never | 112 |
   | guava / junit jar | 5 | 85.5 / 89.4 | 60.4 / 57.5 | 71.1 / 62.3 | 83.0 / 77.5 | 2 / 3 / 4 | 101 / 100 |
   | Go source tarball | 4 | 99.6 | 86.3 | 97.2 | 102.6 | 1 / 3 / never | 111 |
   | coreutils, grep, Django, crates, six | 3-5 | 98-100 | 105-115 | 102-110 | 102-107 | never | 107-118 |
   | synthetic Go L6 insert-mid, 8 updates | 9 | 57.6 | 15.9 | 31.9 | 37.3 | 1 / 4 / 5 | 112 |
   | synthetic gzip append | 9 | 12.9 | 14.0 | 25.0 | 25.0 | never | 113 |

   A transformed entry costs 100-118% (>=106% typical), so **one update repays it** when the family reuses plaintext (Z at once, break-even k=1-2),
   and waiting multiplies the loss: transforming after the first update breaks even only at k=3-5, after the second often never within 5
   revisions. The optimizer rule that falls out: decide at ingest from a cheap predictor of plaintext reuse (same name/size class as an
   existing object, container layers, wheels/jars/npm), keep tarballs of freshly released sources and anything with `new plaintext ~100%` as CDC,
   and keep appends as CDC (CDC 1-2% beats every transform). Rewriting CDC -> Z3 after evidence costs the transform entry (~110% of the object, read +
   write) to save a CDC copy; only worthwhile for families with >=3 more expected revisions.
10. **Is DEFLATE ready to pause?** Yes, for research. Open items that need storage integration, not more lab work: the recipe/descriptor
    (below), plaintext reuse prediction for the admission policy, tar-aware plaintext cuts (tar headers defeat plaintext CDC: 75-100% new on
    release tarballs), per-member amortisation for ZIP (jars: refs 11-13% + skeleton 1-2%), and exact emulation of `deflate_slow`
    (zlib L4-9 are 2-4% of the stream in corrections; ~1 point of incremental) and of miniz/libdeflate/Go if those producers matter.

## Experimental recipe proposal (Z3-08J) - not a format, no store-format version, nothing persisted

| field | content | measured size |
|---|---|---|
| transform id | label only (`deflate.pred/0`), selected per object by the chooser; absent = CDC | 1-2 B |
| wrapper | exact bytes before/after each DEFLATE stream (gzip header+trailer, zlib header+Adler, ZIP local header) inline, or CDC chunks if large | 18 B (gzip) .. 30+n B (ZIP member) |
| semantic plaintext | ordered CDC-v1 chunk refs of the decompressed stream (or member contents) | 32 B digest + varint len per chunk; ~190 chunks per 12 MiB |
| unit policy | token-run units, target 16 KiB (lo/hi = 1/4 .. 4x), gear-hash cut, stored-block boundaries forced | 2 B |
| predictor profile | per unit header: 0 generic, 1 zlib-like, 2 go-like, 3 zlib-fast-like, 4-6 exact zlib L1-3, 7-12 speed variants; zlib warm-up 64 KiB | 4 bits/unit |
| units | refs (32 B + varint) or inline when < ~35 B; unit = range-coded corrections | 0.1-2% of the stream; 670 per 12 MiB |
| explicit escape | per token (E2 range-coded token inside a unit); per object: explicit E2 units; per object: CDC of the original bytes | - |
| skeleton | block table (flags: final / btype / header-predicted / zero padding / same token count; exact dynamic header otherwise), tail padding, unit index (plain length, token count), stream length, CRC-32; inline if <= ~256 B else one ref | 0.1-0.8% (zlib/gzip) .. 0.6% (Go) of the stream |
| exact output | stream length + CRC-32 in the skeleton; object SHA-256 = the manifest's existing identity | 8 B |
| optional range index | per-unit compressed-bit offsets, delta varints, every k-th unit | 0.004-0.06% of the stream |

Per stream the fixed cost is ~35 B (skeleton ref) + 3 B/unit; admission is a per-object decision (CDC / explicit / predictive) made from
measured reuse, with the decode path (plaintext + units + skeleton) validated by CRC before any byte is served.

## What changed in the laboratory

`deflate/stream.go`, `swriter.go` (streaming parser/writer); `ir/zfast.go` (deflate_fast emulation), `ztrees.go` (zlib/gzip header
prediction), `stream.go`, `range.go`; pooled generation-tagged predictor windows; profiles 4-12 and modes; removal of the repeat-distance
contexts; skeleton v2 (flags, canonical forms; `FuzzSkeleton` keeps it canonical); `lab/recipe.go` layouts; `corpus` producers (Go flate,
zlib-ng, libdeflate, ISA-L, zopfli, miniz_oxide) and `extreal`/`plain` generators; `z3fetch` multi-group, `oci://` blobs; 16 new pinned
real DEFLATE families. Fixed a lab defect: an empty ZIP member (empty DEFLATE stream) made the IR rebuild loop forever.

## Validation

`gofmt`, `go vet ./...`, `go test ./...` in `testing-harnesses/z3`; new tests: emulation vs zlib fixtures, header prediction, predicted skeleton,
streaming vs oracle (byte-identical units, all profiles, odd readers), streaming malformed input, range materialization (exact), recipe layouts.
Fuzz (60 s each, all clean): `FuzzSkeleton`, `FuzzReconstruct`, `FuzzUnitDecode`, `FuzzEncodeDecode`, `deflate.FuzzParse`.

## Limitations

Real families are small (3-5 revisions); producers other than the emulated zlib/gzip L1-3 are modelled by the generic engines; skeleton and
reconstruction are exact but not optimized for zip members; Preflate numbers use the pinned preflate-rs 0.7.6 wrapper; CPU numbers are from a
shared 4-core VM; the OCI, F-Droid and Maven sources were pinned at first fetch against the registry digest / upstream .sha1 where published.
