#!/bin/bash
# B6 reproduction: corpus, CDC fixtures, matched lanes (materialized and reclaimed), paired reads, concurrency, scale.
# usage: run.sh WORK   (needs root for drop_caches; ~3 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); R="$ROOT/testing-harnesses/next/b6/results"; mkdir -p "$R"
C=$W/corpus
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" tars
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" git
mkdir -p "$C/ts" "$C/go"
for v in 5.6.2 5.6.3 5.7.2; do [ -f "$C/ts/typescript-$v.tgz" ] || curl -sSfL -o "$C/ts/typescript-$v.tgz" "https://registry.npmjs.org/typescript/-/typescript-$v.tgz"; done
for v in 1.22.0 1.22.6 1.23.0 1.23.4; do [ -f "$C/go/go$v.src.tar.gz" ] || curl -sSfL -o "$C/go/go$v.src.tar.gz" "https://go.dev/dl/go$v.src.tar.gz"; done
python3 -I "$ROOT/testing-harnesses/next/b5/make_inputs.py" "$C"
python3 -I "$ROOT/testing-harnesses/next/b6/make_inputs.py" "$C"
(cd "$C" && sha256sum -c "$ROOT/testing-harnesses/next/b6/corpus.sha256" --quiet)
cd "$ROOT"
T="-tags b6bench,b5locality,b1rangeexperiment -count=1 -timeout 6h"
E="ZEROS3_B6_FX=$W/fx ZEROS3_B6_CORPUS=$C"
env $E go test $T -run '^TestB6Build$' .
env $E ZEROS3_B6_OUT=$R/lanes.jsonl go test $T -run '^TestB6Lanes$' .
env $E ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=12 go test $T -run '^TestB6Reads$' .
env $E ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=coreutils,go,sqlite ZEROS3_B6_LANES=cdc,local,global-loc,global-hot go test $T -run '^TestB6Concurrent$' .
ZEROS3_B6_OUT=$R/scale.jsonl go test $T -run '^TestB6Scale$' .
python3 "$ROOT/testing-harnesses/next/b6/tables.py" "$R"/*.jsonl
