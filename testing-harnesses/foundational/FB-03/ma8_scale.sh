#!/bin/sh
# FB-03 M-A8 G-SCALE on the real locator (zeros3_fb03_scale_test.go).
# Usage: sh testing-harnesses/foundational/FB-03/ma8_scale.sh OUTDIR GENDIR
# OUTDIR gets ma8_*.jsonl; GENDIR holds generated pack directories (about
# 100 B per record; reused across runs). One open per process, so each
# memory high-water mark is that run's own.
set -eu
out=${1:?usage: ma8_scale.sh OUTDIR GENDIR}
gen=${2:?usage: ma8_scale.sh OUTDIR GENDIR}
mkdir -p "$out" "$gen"
bin="$out/zeros3.test"
go test -c -o "$bin" .
t() { "$bin" -test.count=1 -test.timeout 6h -test.run "$1" > /dev/null; }

FB03_OUT="$out/ma8_lookup.jsonl" FB03_LOOKUP=1000000,4000000,16000000 t '^TestFB03Scale_Lookup$'

for n in 1000000 4000000 16000000; do
  for l in lean ref; do
    FB03_OUT="$out/ma8_open.jsonl" FB03_OPEN_DIR="$gen/n$n-p1024" FB03_N=$n FB03_LOADER=$l t '^TestFB03Scale_Open$'
  done
done
for l in lean ref; do
  FB03_OUT="$out/ma8_open.jsonl" FB03_OPEN_DIR="$gen/n16000000-p8192" FB03_N=16000000 FB03_PER=8192 FB03_LOADER=$l t '^TestFB03Scale_Open$'
  FB03_OUT="$out/ma8_open.jsonl" FB03_OPEN_DIR="$gen/n1000000-p16" FB03_N=1000000 FB03_PER=16 FB03_LOADER=$l t '^TestFB03Scale_Open$'
done

g() { FB03_OUT="$out/ma8_growth.jsonl" FB03_GROWTH=$1 FB03_K=$2 FB03_M=$3 t '^TestFB03Scale_Growth$'; }
g 1000000 2 1000000
g 4000000 2 4000000
g 16000000 2 1000000
g 16000000 64 9000000

for n in 4000000 16000000; do
  FB03_OUT="$out/ma8_retire.jsonl" FB03_RETIRE=$n FB03_CYCLES=10 FB03_FRAC=0.1 t '^TestFB03Scale_Retire$'
done
rm -f "$bin"
