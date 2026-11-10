#!/bin/bash
P3_ROOT=${P3_ROOT:-/tmp/claude-0/p3}   # fixtures in $P3_ROOT/fix, binaries in $P3_ROOT/{base,new}, raw results in $P3_ROOT/res
# 1M-object CLI measurements, base and new interleaved
S=$P3_ROOT/fix/s1m; OUT=$P3_ROOT/res/maint_1m.jsonl; W=$P3_ROOT/work
run() { bin=$1; lab=$2; store=$3; shift 3; B=$P3_ROOT/$bin; $B/p0bench cli -label "s1m_${bin}_$lab" -out $OUT -work $W -bin $B/zeros3 -profbin $B/zeros3-prof -store $store -cache warm -- "$@" >/dev/null 2>&1 || echo "NONZERO $bin $lab"; }
for rep in 1 2; do
 for bin in base new; do
  for op in "stats|stats -store $S -json" "verify|verify -store $S -json" "gcdry|gc -store $S -json" "repackdry|repack -store $S -json"; do
    run $bin ${op%%|*} $S ${op#*|}
  done
 done
done
for bin in base new; do
  run $bin vdeep $S verify -deep -store $S -json
done
for bin in base new; do
  C=$W/clone1m; rm -rf $C; cp -a $S $C
  run $bin prune $C versions prune -store $C -bucket bench -keep-last 0 -apply -json
  run $bin gc_apply $C gc -apply -store $C -json
  run $bin repack_apply $C repack -apply -store $C -json
  run $bin vdeep_after $C verify -deep -store $C -json
  rm -rf $C
done
