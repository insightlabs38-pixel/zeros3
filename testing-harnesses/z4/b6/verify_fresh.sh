#!/bin/sh
# Fresh-process exactness: for every native file, `z4b6 pack` (one process) writes the serialized
# representation, `z4b6 unpack` (another process, original not read) rebuilds it, and sha256sum
# independently compares. Any failure stops the script with a non-zero status.
# usage: verify_fresh.sh Z4B6_BINARY KIND SCRATCH FILE...   (KIND = sst|parquet|sqfs)
set -eu
BIN=$1; KIND=$2; SCR=$3; shift 3
n=0
for f in "$@"; do
  d="$SCR/$(echo "$f" | tr '/' '_')"
  rm -rf "$d"; mkdir -p "$d"
  if [ "$KIND" = sqfs ]; then
    "$BIN" sqfs-pack "$f" "$d/p"
    "$BIN" sqfs-unpack "$d/p" "$d/out" >/dev/null
  else
    "$BIN" pack "$KIND" "$f" "$d/p"
    "$BIN" unpack "$KIND" "$d/p" "$d/out"
  fi
  a=$(sha256sum < "$f" | cut -d' ' -f1); b=$(sha256sum < "$d/out" | cut -d' ' -f1)
  [ "$a" = "$b" ] || { echo "MISMATCH $f ($a vs $b)" >&2; exit 1; }
  [ "$(wc -c < "$f")" = "$(wc -c < "$d/out")" ] || { echo "LENGTH $f" >&2; exit 1; }
  rm -rf "$d"
  n=$((n+1))
done
echo "$KIND: $n files exact (fresh process, independent sha256sum)"
