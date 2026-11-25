#!/usr/bin/env bash
set -euo pipefail
scratch=$(realpath -m "${1:?pass profiling scratch}")
store=$(realpath "${2:?pass a finalized store}")
key=${3:?pass the object key}
ref=${4:-HEAD}
tool=${C10_GO:-go}
harness=$(cd "$(dirname "$0")" && pwd)
repo=$(git -C "$harness" rev-parse --show-toplevel)
mkdir -p "$scratch"
if [[ "$ref" == worktree ]]; then
  cp "$repo/zeros3.go" "$scratch/zeros3.go"
else
  git -C "$repo" show "$ref:zeros3.go" > "$scratch/zeros3.go"
fi
cp "$repo/go.mod" "$scratch/go.mod"
cp "$harness/profile_test.go.txt" "$scratch/read_test.go"
cd "$scratch"
export GOMAXPROCS=2 C10_STORE="$store" C10_KEY="$key"
"$tool" test -c -o read.test .
for span in 0 4096 65536; do
  C10_RANGE="$span" ./read.test -test.run '^$' -test.bench '^BenchmarkC10Read$' \
    -test.benchtime=4x -test.cpuprofile "$span.cpu" -test.memprofile "$span.heap" > "$span.txt"
  "$tool" tool pprof -top -nodecount=18 read.test "$span.cpu" >> "$span.txt"
done
