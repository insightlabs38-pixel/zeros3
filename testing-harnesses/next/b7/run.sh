#!/bin/bash
# B7 reproduction: corpus (B6 + make_inputs.py), CDC fixtures, discovery frontier and ablations, density and
# chunking sweeps, model-level comparisons, materialized lanes, paired cold reads, scale.
# usage: run.sh WORK   (needs root for drop_caches; ~4 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); R="$ROOT/testing-harnesses/next/b7/results"; mkdir -p "$R"
C=$W/corpus
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" tars
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" git
mkdir -p "$C/ts" "$C/go"
for v in 5.6.2 5.6.3 5.7.2; do [ -f "$C/ts/typescript-$v.tgz" ] || curl -sSfL -o "$C/ts/typescript-$v.tgz" "https://registry.npmjs.org/typescript/-/typescript-$v.tgz"; done
for v in 1.22.0 1.22.6 1.23.0 1.23.4; do [ -f "$C/go/go$v.src.tar.gz" ] || curl -sSfL -o "$C/go/go$v.src.tar.gz" "https://go.dev/dl/go$v.src.tar.gz"; done
python3 -I "$ROOT/testing-harnesses/next/b5/make_inputs.py" "$C"
python3 -I "$ROOT/testing-harnesses/next/b6/make_inputs.py" "$C"
python3 -I "$ROOT/testing-harnesses/next/b7/make_inputs.py" "$C"
(cd "$C" && sha256sum -c "$ROOT/testing-harnesses/next/b7/corpus.sha256" --quiet)
cd "$ROOT"
T="-tags b7bench,b6bench,b5locality,b1rangeexperiment -count=1 -timeout 6h"
E="ZEROS3_B6_FX=$W/fx ZEROS3_B6_CORPUS=$C"
env $E go test $T -run '^TestB7Build$' .
# B7-A frontier: exhaustive pairwise oracle vs the B6 sketch and B7 region shortlists; graph level (model only)
env $E ZEROS3_B6_OUT=$R/frontier.jsonl go test $T -run '^TestB7Oracle$' .
# B7-B/F ablations: sampling levels, posting caps, verification, chunk-boundary feature generators
env $E ZEROS3_B6_OUT=$R/ablations.jsonl ZEROS3_B6_FAMILIES=cu-files,go-files,vendor ZEROS3_B7_GRAPH=0 \
  ZEROS3_B7_ABLATE=lvl5,lvl6,lvl8,lvl9,rare4,rare64,short8,noverify,fastcdc,gear,cdcv1 go test $T -run '^TestB7Oracle$' .
# B7-D anchor density and B7-F chunk boundaries (corpus files only, no stores)
env $E ZEROS3_B6_OUT=$R/density.jsonl go test $T -run '^TestB7Density$' .
env $E ZEROS3_B6_OUT=$R/chunking.jsonl go test $T -run '^TestB7Chunking$' .
env $E ZEROS3_B6_OUT=$R/chunking.jsonl ZEROS3_B6_FAMILIES=ts ZEROS3_B7_GUNZIP=1 go test $T -run '^TestB7Chunking$' .   # format-informed
# model-level: B6 exactly, B6 discovery with the B7 search, B7 single-base, B7
env $E ZEROS3_B6_OUT=$R/graph.jsonl ZEROS3_B7_CONFIGS=b6,b6-search,b7-single,b7 go test $T -run '^TestB7Graph$' .
# materialized lanes (cdc, b6/b7 x archive/mixed), then paired cold reads where bytes change (+ go as control)
env $E ZEROS3_B6_OUT=$R/lanes.jsonl go test $T -run '^TestB7Lanes$' .
env $E ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=12 ZEROS3_B6_FAMILIES=coreutils,vendor,go \
  ZEROS3_B6_LANES=b6-archive,b7-archive,b6-mixed,b7-mixed go test $T -run '^TestB7Reads$' .
env $E ZEROS3_B6_OUT=$R/reads2.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=24 ZEROS3_B6_FAMILIES=coreutils \
  ZEROS3_B6_LANES=b6-archive,b7-archive,b6-mixed,b7-mixed go test $T -run '^TestB7Reads$' .   # session 2
env $E ZEROS3_B6_OUT=$R/reads2.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=12 ZEROS3_B6_FAMILIES=vendor \
  ZEROS3_B6_LANES=b6-archive,b7-archive,b6-mixed,b7-mixed go test $T -run '^TestB7Reads$' .
env $E ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=coreutils,vendor ZEROS3_B6_LANES=b6-archive,b7-archive go test $T -run '^TestB7Concurrent$' .
ZEROS3_B6_OUT=$R/scale.jsonl ZEROS3_B7_SCALE=1500,6000,24000 go test $T -run '^TestB7Scale$' .
python3 "$ROOT/testing-harnesses/next/b7/tables.py" "$R"/*.jsonl* > "$R/tables.md"
