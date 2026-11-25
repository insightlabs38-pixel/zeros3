#!/usr/bin/env bash
# Compile matched default-S3 handler profiles from immutable sources.
set -euo pipefail
work=$(realpath -m "${1:?pass a profiling scratch directory}")
compiler=${C4_GO:-go}
harness=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$harness" rev-parse --show-toplevel)
export GOMAXPROCS=2
mkdir -p "$work"
for ref in b10:dcefbcb5c2539c74a0d33bfc0d6e4a380f671743 c2:4bc66f3af3d1d97f90c483c2b26676d3912fbb02; do
  name=${ref%:*}
  commit=${ref#*:}
  scratch="$work/$name"
  mkdir -p "$scratch"
  git -C "$repo" show "$commit:zeros3.go" > "$scratch/zeros3.go"
  git -C "$repo" show "$commit:go.mod" > "$scratch/go.mod"
  cp "$harness/http_profile.go.txt" "$scratch/c4_profile_test.go"
  (cd "$scratch" && "$compiler" test -c -o "$scratch/bench")
done
for name in b10 c2; do
  scratch="$work/$name"
  for operation in PUT GET Range; do
    "$scratch/bench" -test.run '^$' -test.bench "^BenchmarkC4DefaultHTTP/$operation$" \
      -test.benchtime 1200x -test.count=1 -test.cpuprofile "$scratch/$operation.cpu" \
      -test.memprofile "$scratch/$operation.heap" -test.blockprofile "$scratch/$operation.block" \
      -test.mutexprofile "$scratch/$operation.mutex" > "$scratch/$operation.txt"
    for kind in cpu heap block mutex; do
      "$compiler" tool pprof -top -nodecount=12 "$scratch/bench" "$scratch/$operation.$kind" \
        >> "$scratch/$operation.txt"
    done
  done
  "$scratch/bench" -test.run '^$' -test.bench '^BenchmarkC4DefaultHTTP/(Sign|HEAD)$' \
    -test.benchtime 1200x -test.count=3 > "$scratch/Control.txt"
done
