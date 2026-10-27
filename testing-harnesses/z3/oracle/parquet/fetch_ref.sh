#!/bin/sh
# Z3-31 REFERENCE_FIXTURE files: Apache parquet-testing, pinned to one commit.
set -e
ROOT="${Z3_PQ_CACHE:-$HOME/.cache/z3-parquet}/ref"
SHA=eedfad3f086678d282a00d8ff3cf8932d5d29af5
mkdir -p "$ROOT"
for f in alltypes_plain.parquet alltypes_plain.snappy.parquet alltypes_dictionary.parquet alltypes_tiny_pages.parquet \
         alltypes_tiny_pages_plain.parquet datapage_v2.snappy.parquet delta_binary_packed.parquet delta_length_byte_array.parquet \
         delta_byte_array.parquet nested_lists.snappy.parquet nested_structs.rust.parquet byte_stream_split.zstd.parquet \
         byte_stream_split_extended.gzip.parquet fixed_length_byte_array.parquet nulls.snappy.parquet \
         repeated_no_annotation.parquet rle_boolean_encoding.parquet plain-dict-uncompressed-checksum.parquet \
         page_v2_empty_compressed.parquet binary.parquet int32_decimal.parquet lz4_raw_compressed.parquet \
         data_index_bloom_encoding_stats.parquet column_chunk_key_value_metadata.parquet; do
  [ -s "$ROOT/$f" ] || curl -sS -f -m 60 -o "$ROOT/$f" "https://raw.githubusercontent.com/apache/parquet-testing/$SHA/data/$f" || echo "skip $f" >&2
done
find "$ROOT" -type f -empty -delete
ls "$ROOT" | wc -l
