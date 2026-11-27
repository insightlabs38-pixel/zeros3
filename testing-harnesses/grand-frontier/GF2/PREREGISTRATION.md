# GF2 exact compressed queries

Use original gzip bytes from TypeScript releases to develop. Delay the
urllib3 release trial until the execution mode and parameters are fixed.
Use Parquet pages or independent zstd frames as a separate compressed
format control. Verify every original byte and SHA and exercise different
compression levels, block boundaries, trailer/header variation, archive
entry order, damaged frames and unrelated compressed data.

1. Compile immutable compressed bytes into independently decodable frames.
   Compare byte-aligned matches against an eight-alignment source index.
   A source bit shift rearranges the same bits without a DEFLATE predictor.
   Allow framed literal fallback when coding choices have diverged.
2. Compare exact replay with stored corrections and a second representation
   that shares verified DEFLATE token phrases across versions. Preserve per-block
   tables and the exact dynamic header, stored data and padding. Measure
   how much space is paid to skip predictor work on the read path.
3. Sample a budgeted overlay of compiled compressed outputs selected for
   a query mix. Charge repeated bytes, verification and indexes. Keep the
   unaccelerated parent path for other queries and compare multiple hot sets.

Develop on at most a few MiB of compressed bytes per input. Report retained
allocation, two-read path costs and native comparators separately. Compare
5× whole-read speed at up to 15% additional allocation on a real family.
Relax the target only when a different demonstrated tradeoff warrants it.
