#!/bin/sh
# Build the unmodified production source twice: plain (timing) and with the
# P0 profiling overlay (profiling runs only). Usage: build.sh REPO_ROOT OUTDIR
set -eu
ROOT=${1:?repo root}; OUT=${2:?out dir}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
cd "$ROOT"
go build -trimpath -o "$OUT/zeros3" .
cat > "$OUT/overlay.json" <<J
{"Replace":{"$ROOT/zeros3_p0prof.go":"$HERE/overlay/zeros3_p0prof.go"}}
J
go build -trimpath -overlay "$OUT/overlay.json" -o "$OUT/zeros3-prof" .
sha256sum "$OUT/zeros3" "$OUT/zeros3-prof" "$ROOT/zeros3.go" "$ROOT/go.mod"
