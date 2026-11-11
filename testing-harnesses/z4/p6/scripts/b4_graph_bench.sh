#!/bin/bash
# usage: b4_graph_bench.sh BIN OUT.jsonl  -- identity (reference vs shared engine vs compact) and matching benchmarks on
# synthetic LLVM-shaped graphs. Each measurement is a fresh process. bytes/node figures are live heap after GC
# (graph_bytes_per_node, graph_plus_idents_bytes_per_node) and peak heap in use (peak_heap_bytes_per_node).
set -euo pipefail
BIN=$1; OUT=$2; : > "$OUT"
for n in 100000 1000000; do for m in ref graph compact; do "$BIN" graphbench -n $n -mode $m >> "$OUT"; done; done
"$BIN" graphbench -n 10000000 -mode compact >> "$OUT"
for n in 100000 1000000; do "$BIN" matchbench -n $n >> "$OUT"; done
