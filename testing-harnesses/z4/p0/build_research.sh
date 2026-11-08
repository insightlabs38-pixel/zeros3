#!/bin/sh
# Build the research drivers (z3 module, stdlib-only) twice: plain and with the P0 profiling overlay.
# usage: build_research.sh REPO_ROOT OUTDIR
set -eu
ROOT=${1:?repo}; OUT=${2:?out}; HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"; cd "$ROOT/testing-harnesses/z3"
for c in z4similarity z4b3 z4b4 z4b5 z4b6 z4b7 z4b6rank; do
  go build -trimpath -o "$OUT/$c" ./cmd/$c
  printf '{"Replace":{"%s/zeros3_p0prof.go":"%s/overlay/zeros3_p0prof.go"}}' "$ROOT/testing-harnesses/z3/cmd/$c" "$HERE" > "$OUT/ov-$c.json"
  go build -trimpath -overlay "$OUT/ov-$c.json" -o "$OUT/$c-prof" ./cmd/$c
done
ls -la "$OUT" | grep -v ov-
