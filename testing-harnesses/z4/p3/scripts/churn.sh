#!/bin/bash
P3_ROOT=${P3_ROOT:-/tmp/claude-0/p3}   # fixtures in $P3_ROOT/fix, binaries in $P3_ROOT/{base,new}, raw results in $P3_ROOT/res
# churn.sh SRCFIX OUT.jsonl : reclamation matrix. dead% x {random,range} x arm x {base,new}
SRC=$P3_ROOT/fix/$1; OUT=$P3_ROOT/res/$2; W=$P3_ROOT/work
T=$P3_ROOT/zeros3.test
for dead in 0 10 30 50 75 90; do
 for mode in random range; do
  [ $dead = 0 ] && [ $mode = range ] && continue
  CH=$W/churn_${1}_${dead}_${mode}; rm -rf $CH; cp -a $SRC $CH
  [ $dead != 0 ] && ZEROS3_P3_CHURN_DIR=$CH ZEROS3_P3_CHURN_FRAC=$(python3 -c "print($dead/100)") ZEROS3_P3_CHURN_MODE=$mode $T -test.run P3_ChurnFixture >/dev/null 2>&1
  [ $dead = 90 ] && ZEROS3_P3_CHURN_DIR=$CH true
  for bin in base new; do
   B=$P3_ROOT/$bin
   for arm in gc repack50 repack100 gc_then_repack; do
    C=$W/churn_clone; rm -rf $C; cp -a $CH $C
    export P0_NOTE="dead=$dead mode=$mode arm=$arm bin=$bin"
    run() { lab=$1; shift; $B/p0bench cli -label "${arm}_${bin}_${dead}_${mode}_$lab" -out $OUT -work $W -bin $B/zeros3 -profbin $B/zeros3-prof -store $C -cache warm -- "$@" >/dev/null 2>&1 || echo "NONZERO $arm $bin"; }
    case $arm in
     gc) run a gc -apply -store $C -json;;
     repack50) run a repack -apply -store $C -json;;
     repack100) run a repack -apply -max-live-percent 100 -store $C -json;;
     gc_then_repack) run a gc -apply -store $C -json; run b repack -apply -store $C -json;;
    esac
    rm -rf $C
   done
  done
  rm -rf $CH
 done
done
