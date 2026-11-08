#!/bin/sh
# build.sh REPO_ROOT OUTDIR : plain + P0 overlay-profiled production binaries, plus the p0bench driver.
set -eu
ROOT=${1:?repo root}; OUT=${2:?out dir}; mkdir -p "$OUT"; cd "$ROOT"
go build -trimpath -o "$OUT/zeros3" .
printf '{"Replace":{"%s/zeros3_p0prof.go":"%s/testing-harnesses/z4/p0/overlay/zeros3_p0prof.go"}}' "$ROOT" "$ROOT" > "$OUT/overlay.json"
go build -trimpath -overlay "$OUT/overlay.json" -o "$OUT/zeros3-prof" .
go -C testing-harnesses/z4 build -o "$OUT/p0bench" ./p0/cmd/p0bench
sha256sum "$OUT/zeros3" "$ROOT/zeros3.go"
