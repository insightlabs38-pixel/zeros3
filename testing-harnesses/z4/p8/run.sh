#!/bin/bash
# P8 measurement driver. usage: run.sh BIN OUTDIR TAG what...   (what: migrate matcher rep)
#   BIN     a root test binary built with `go test -c -o BIN .` (the baseline binary is built from the P8 base commit)
#   migrate FAMILIES VARIANTS REPS   one fresh process per family (peak RSS is per process)
#   matcher FAMILIES MATCHERS        xd = pre-P8 production matcher, dw = shared dcore matcher
#   rep     FILES REPS               gzip/zstd structured-representation build + replay
# Corpus: ZEROS3_B2_CORPUS (default /home/user/p8corpus/econ, layout of testing-harnesses/z4/p8/CORPUS.md).
set -u
BIN=$1; OUT=$2; TAG=$3; shift 3
mkdir -p "$OUT/$TAG"
export ZEROS3_B2_CORPUS=${ZEROS3_B2_CORPUS:-/home/user/p8corpus/econ} ZEROS3_B2_OUT=$OUT/$TAG.jsonl ZEROS3_P8_PROF=$OUT/$TAG
case $1 in
migrate) for fam in $(echo "$2" | tr , ' '); do
    ZEROS3_P8_FAMILY=$fam ZEROS3_P8_VARIANTS=$3 ZEROS3_P8_REPS=${4:-3} "$BIN" -test.run TestP8Migration -test.count=1 -test.timeout 3h > "$OUT/$TAG/$fam.log" 2>&1 || echo "FAIL $fam"
  done ;;
matcher) for fam in $(echo "$2" | tr , ' '); do
    ZEROS3_P8_FAMILY=$fam ZEROS3_P8_MATCHERS=$3 "$BIN" -test.run 'TestP8Matcher$' -test.count=1 -test.v > "$OUT/$TAG/matcher-$fam.log" 2>&1 || echo "FAIL $fam"
  done ;;
rep) ZEROS3_P8_FILES=$2 ZEROS3_P8_REPS=${3:-2} "$BIN" -test.run TestP8Rep -test.count=1 -test.v -test.cpuprofile "$OUT/$TAG/rep.pprof" > "$OUT/$TAG/rep.log" 2>&1 || echo FAIL ;;
esac
