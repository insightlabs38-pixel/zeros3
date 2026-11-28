# GF7 · Compressed-domain DEFLATE token translation

GF2 reused already-encoded bit spans. Code table changes destroyed much of
that reuse on adjacent TypeScript releases and urllib3. GF7 locates shared
LZ tokens in source blocks and translates them into the target's original
codes. The second variant indexes matches by literal or exact length
representation, then stores XOR corrections for the distance code and
extra bits. The reader starts at indexed compressed-source bit boundaries,
decodes that bounded run and emits the target's exact codewords.

The program retains the target's original dynamic-header syntax, end marker,
gzip header/trailer and tail bits. It falls back to raw masked block slices
when packed program pricing loses. Blocks are independently digested.
A range touching a shared boundary reads both blocks and combines validated
masked slices. Whole-object reconstruction additionally checks the original
SHA-256. The decoder never invokes the plaintext LZ match finder.

## Results

Complete allocations include both versions and finalized pack and metadata
blocks. Signed p95 measures 4 KiB range and whole-object GET respectively.
Development and both held-out family orders are pinned.

| Family | CDC native build, bytes | Exact token copy, native build | Corrected, native build | Corrected signed | Archive signed | Signed p95 corrected / archive |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| TypeScript v0 to v1 | 8,409,088 | 4,354,048 | 4,317,184 | 4,333,568 | 4,829,184 | 2.64 / 32.66 ms range, 238 / 1,292 ms whole |
| TypeScript v1 to v2 | 8,466,432 | 7,532,544 | 5,656,576 | 5,681,152 | 5,177,344 | 3.65 / 31.84 ms range, 241 / 1,269 ms whole |
| urllib3 v0 to v1 | 614,400 | 512,000 | 421,888 | 438,272 | 405,504 | 3.25 / 23.03 ms range, 17.4 / 102.7 ms whole |
| Compressor settings control | 24,576 | 24,576 | 24,576 | — | — | Raw floor dominates allocation |

On the held-out TypeScript pair GF2's bit-span compiler used 8,065,024 B
in the same native-build comparison. Correction raised matched-token
coverage from 1,300,853 to 2,220,373 of 2,359,662 tokens. The profitable
selected programs cover 31,686,493 target bits before program overhead;
334,390 tokens needed corrections. The signed compiler and proof took
1.42 CPU-seconds versus 26.40 for archive planning. Both measurements
include their own real preparation work. Cross-order latency ranges are
reported separately in GF8.

The runtime caches the bounded index and avoids repeated expansion for
sparse reads. This cuts held-out 4 KiB p95 from 9.92 ms in the uncached
native-build harness to 3.65 ms through signed S3 and the cached reader.
These paths differ in their harness and cache state. Whole read cost remains
higher than GF2's bit copy on the development pair. Independent held-out
gzip results support a specialized frontier and leave the general gzip
population and large objects open.

The native reader is opt-in and bounds input, index expansion, frame
coverage and output. Signed closures verify exact target and source
identities. Legacy-export bytes, old-reader refusal, anchor deletion,
native transfer and real process-interruption boundaries have direct
tests. The encoder stays in the research harness. See [raw tables](results)
and [reproduction](REPRODUCE.md).
