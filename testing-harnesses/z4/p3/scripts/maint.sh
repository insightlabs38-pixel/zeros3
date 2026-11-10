#!/bin/bash
P3_ROOT=${P3_ROOT:-/tmp/claude-0/p3}   # fixtures in $P3_ROOT/fix, binaries in $P3_ROOT/{base,new}, raw results in $P3_ROOT/res
# maint.sh BINDIR LABEL FIXTURE... : read-only ops on the fixture, mutating ops on cp -a clones; JSONL via p0bench cli
B=$1; L=$2; shift 2
OUT=$P3_ROOT/res/maint_$L.jsonl; W=$P3_ROOT/work
run() { lab=$1; store=$2; shift 2; $B/p0bench cli -label "$lab" -out $OUT -work $W -bin $B/zeros3 -profbin $B/zeros3-prof -store $store -cache warm -- "$@" >/dev/null 2>&1 || echo "NONZERO $lab"; }
for f in "$@"; do
  S=$P3_ROOT/fix/$f
  for rep in 1 2 3; do
    run ${f}_stats   $S stats -store $S -json
    run ${f}_verify  $S verify -store $S -json
    run ${f}_vdeep   $S verify -deep -store $S -json
    run ${f}_doctor  $S doctor -store $S -json
    run ${f}_gcdry   $S gc -store $S -json
    run ${f}_compactdry $S compact -dry-run -store $S -json
    run ${f}_repackdry  $S repack -store $S -json
  done
  # mutating: each on a fresh clone
  for op in "gc_apply|gc -apply" "repack_apply|repack -apply" "compact_apply|compact" ; do
    n=${op%%|*}; c=${op#*|}
    for rep in 1 2; do
      C=$P3_ROOT/work/clone_$f; rm -rf $C; cp -a $S $C
      run ${f}_$n $C $c -store $C -json
      rm -rf $C
    done
  done
  C=$P3_ROOT/work/clone_$f; rm -rf $C; cp -a $S $C
  run ${f}_prune $C versions prune -store $C -bucket bench -keep-last 1 -apply -json
  run ${f}_gc_after_prune $C gc -apply -store $C -json
  run ${f}_repack_after_prune $C repack -apply -store $C -json
  rm -rf $C
done
