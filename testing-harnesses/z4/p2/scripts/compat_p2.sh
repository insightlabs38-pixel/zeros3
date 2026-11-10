#!/bin/bash
# Old/new store compatibility: a store written by one build is verified and read byte-for-byte by the other.
# Usage: compat_p2.sh BASEDIR CANDDIR OUT.txt   (each dir holds zeros3 and p0bench)
set -u
B=$1; C=$2; OUT=$3; W=${P2_WORK:-/tmp/claude-0/p2/work}/compat; rm -rf $W; mkdir -p $W; : > $OUT
run() { # writer reader label
  local w=$1 r=$2 lab=$3; local S=$W/$lab/store; mkdir -p $W/$lab
  for spec in "4kib 300" "1mib 40" "64mib 3"; do set -- $spec
    rm -rf $S; $w/p0bench mkstore -label c -out $W/$lab/mk.jsonl -work $W/$lab -bin $w/zeros3 -profbin $w/zeros3 -dir $S -objs $2 -size $1 -conc 4 >/dev/null 2>&1 || { echo "$lab $1: mkstore FAILED" >> $OUT; continue; }
    v=$($r/zeros3 verify -deep -store $S 2>&1 | tail -1)
    for mode in whole range; do
      $r/p0bench readstore -label c -out $W/$lab/rd.jsonl -work $W/$lab -bin $r/zeros3 -profbin $r/zeros3 -dir $S -manifest $S.manifest.jsonl -mode $mode -rsize 4kib -nreq 400 -conc 4 -reps 1 -cache warm >/dev/null 2>$W/$lab/err.txt && res=ok || res="FAILED: $(tail -1 $W/$lab/err.txt)"
      echo "$lab size=$1 verify-deep[$v] read-$mode=$res" >> $OUT
    done
  done
}
run $B $C base-writes_cand-reads
run $C $B cand-writes_base-reads
cat $OUT
