# Z3-38 — structured candidate breadth hardening

Verdict: **retain a bounded I3 subset, with Zstd SST and delta-length Parquet**.
Research-only extension; no new kind, production numbering, wire/store/default
change. This does not promote every current Pebble/RocksDB or Parquet feature.

## Corpus and selection
Reused Q6's scaled native operation logs with byte-identical shell copies;
RocksDB9.8.4/rocksdict0.3.29: Snappy/none/Zstd/LZ4,8 states each,44 SST files.
Pebble1.1.5: Snappy/none/Zstd,8 states each,33 files; no LZ4 producer support.
Newer pinned Pebble2.1.7: same three histories,33 files; native table format7.
These are PRODUCER_WORKLOAD, not independent natural restore histories. No
LevelDB lane reopened. Three small native tables committed with SHA provenance.
Parquet reused pinned PyArrow18.1.0/DuckDB2.0.0 producers at their original
400k-row scale: DuckDB8 REAL_REEXPORT variants224.61MiB; PyArrow8-step chain
270.73MiB;6 controls38.21MiB. Apache parquet-testing commit
`eedfad3f086678d282a00d8ff3cf8932d5d29af5`:30 REFERENCE_FIXTURE files1.83MiB.
The Apache subset includes gzip concatenation, LZ4_RAW, delta encodings, nested,
Brotli and Spark INT96. Spark is one reference fixture, not a producer history.
All162 inputs have full SHA/size, serialized representation or explicit CDC
fallback proof, reachable-cost accounting and reason. No failed case removed.

## SST implementation and coverage
Mechanically extracted exact Zstd frame analysis/replay glue from Parquet via a
shell Python script; reused zstdz3's parser, skeleton, units and replay unchanged.
RocksDB/Pebble's original decoded-length varint prefix is retained verbatim.
Blocks bound decoded bytes/window/literals/sequences by SST limits; IR bytes by
MaxRecipe. Parse verifies decoded-size equality and byte-exact compressed replay.
Only proven Zstd forms enter the existing lab SST recipe. Snappy/none fixture
bytes remain compatible; no production parser or recipe changed.
All22 Zstd tables replay in all6 forms (132 exact reconstructions); native kind
closure/GC accounting proofs and partial-range checks also pass.
RocksDB Zstd histories expose1565 Zstd blocks/2.5MiB (18.4% across the balanced
four-codec corpus's known data-block bytes). This is not a field-usage estimate.
The old audit skipped rejected tables and compressed-index failures hid blocks.
It now counts available block bytes even on rejection and labels unknown-index
bytes explicitly; it never interprets unknown bytes as absence of a codec.
LZ4 index compression still prevents complete block-byte coverage: all11 LZ4
tables use exact CDC fallback. No unsupported LZ4 percentage is inferred.
New Pebble's33 format7 tables all fall back. Reader now rejects its unknown footer
layout before interpreting old checksum/handle fields; audit reports unknown
layout instead of random checksum types/tail coverage. Regression fixture kept.
CRC32C trailers are checked where already supported; other SST checksum trailers
remain verbatim, not newly verified. Complete reconstruction SHA authenticates
research output. Dictionary compression and unknown layouts remain fallbacks.

## Parquet implementation and byte-weighted priority
DuckDB's largest natural reexport gap was DELTA_LENGTH_BYTE_ARRAY:17.00MiB,
versus0.82MiB DELTA_BINARY_PACKED. Implemented bounded length-prefix decoding,
retaining the original block/miniblock headers, widths, padding and deltas as
explicit layout bytes; string values enter existing canonical grouped streams.
Replay checks the retained lengths against every value before rebuilding bytes.
Bound1M values/page,64K-value blocks,32-bit lengths, arithmetic wrap and strict
geometry/remaining-byte checks; unsupported/corrupt forms retain existing fallback.
No new lab kind or external decoder. Prefix/layout bytes are charged, never free.
DuckDB opaque payload7.9→0.4%; control opaque44.5→41.4%; reference28.2→28.1%.
Remaining real reexport gap0.82MiB is DELTA_BINARY_PACKED. Controls additionally
contain GZIP6.90MiB/LZ4_RAW8.47MiB/delta-byte-array0.18MiB/nested0.05MiB.
Those chosen codec controls cannot establish natural byte share. No blanket codec
implementation: GZIP would reuse Z3-05..08; LZ4_RAW/Brotli, binary/delta-byte,
nested/repeated stay opaque or CDC until proven. Existing whole-file fallbacks
remain when no column is logical or a declared page exceeds limits.
All52 Parquet files:39 exact representation replays,13 verified CDC fallbacks,
zero failures. Fallbacks:12 no-logical-column cases;1 deliberate reference page
claiming1073741828 decoded bytes, rejected before allocation. Tests that formerly
expected delta-length opacity now assert its exact logical page replay; binary
columns still must remain opaque. Every original case/assertion is retained.

## Complete-closure economics
Cumulative representation-only / CDC, including serialized roots, index nodes,
compression recipes, stored components and64B/object (lab compressed-size model):

|History|REP/CDC|CDC+REP/CDC|Fallback files|
|---|---:|---:|---:|
|RocksDB Snappy/none/Zstd|.339/.397/.467|1.339/1.397/1.467|0/0/0|
|Pebble1.1.5 Snappy/none/Zstd|.549/.643/.646|1.549/1.643/1.646|0/0/0|
|RocksDB LZ4|1.000|1.000|11|
|Pebble2.1.7 each codec|1.000|1.000|11 each|
|DuckDB variants|.209|1.209|0|
|PyArrow chain|.326|1.326|0|
Identical-input pre-Q8 replay: DuckDB REP44,838,900→28,108,704B (−37.3%).
These are union costs across retained states/options, not net production savings.
CDC remains authoritative. Small-group/large-recipe first-sight premiums persist.
Zstd recipe replay is relatively slow (~0–1MiB/s encoding in six-form study);
no production latency claim. Existing range recipe/group scans and amplification
remain visible; caching a parsed index is future work, not a false O(logN) claim
for a decoder that still parses the entire recipe per call.

## Gates and carry-forward
Full lab/root tests and vet pass; changed-package race passes. Delta fuzz973822
executions/25s; Zstd block fuzz111/26s with native seeds and tight decoded bounds.
Hand-packed/native oracles pass; PyArrow/DuckDB10 files/79 columns match (5 opaque excluded).
Production diff empty; detailed manifests, audits, costs and fallback reasons in
`testdata/q8-breadth`. I3 draft may use only the broadened subset proved here.
