#!/bin/bash
# run_final2.sh BASEDIR P1CDIR : second measurement sequence (after run_final.sh): store-layer bench, syscalls,
# profiles, batching-window experiment, scale / long-history / maintenance. Results -> ../results/
set -u
BASE=$1; FIN=$2; H=$(cd "$(dirname "$0")" && pwd); R=$H/../results; ROOT=$(cd "$H/../../../.." && pwd)
step() { echo "[$(date +%T)] $*"; }
mkdir -p $R/prof
step store-layer bench
( cd $ROOT && ZEROS3_P1C_BENCH=1 go test -count=1 -run StoreLayerPutThroughput -v . 2>&1 | grep -E "P1C_STORE|^(ok|FAIL|---)" ) > $R/store_layer_bench.txt
step syscalls per object
: > $R/syscalls_per_object.txt
for spec in "4kib 1000 8" "64kib 600 8" "1mib 100 4"; do
  set -- $spec
  for v in base final; do
    d=$BASE; [ $v = final ] && d=$FIN
    echo "### $v size=$1 objs=$2 conc=$3" >> $R/syscalls_per_object.txt
    P0BENCH=$d/p0bench $ROOT/testing-harnesses/z4/p1b/scripts/syscount.sh $d/zeros3 /tmp/claude-0/p1c/sc $v-$1 $1 $2 $3 >> $R/syscalls_per_object.txt 2>&1
  done
done
step profiles
for v in base final; do
  d=$BASE; [ $v = final ] && d=$FIN
  $d/p0bench put -label prof-$v -work /tmp/claude-0/p1c/work -bin $d/zeros3 -profbin $d/zeros3-prof -reps 1 -objs 6000 -size 4kib -conc 8 -prof full -out $R/prof_runs.jsonl > /dev/null 2>&1
  sh $ROOT/testing-harnesses/z4/p0/scripts/profsum.sh /tmp/claude-0/p1c/work/prof/prof-$v-r0 $R/prof/$v-4k-c8.txt
done
step batching window experiment
P=/tmp/claude-0/p1c/p1cproto
(cd $ROOT/testing-harnesses/z4 && go build -o $P ./p1c/proto)
: > $R/proto_delay.jsonl
for r in 1 2 3; do for c in 1 8 32; do for d in 0 100us 500us 2ms; do
  $P -mode seginl -dir /tmp/claude-0/p1c/work/run -c $c -n 3000 -size 4096 -delay $d -note "delay=$d round=$r" >> $R/proto_delay.jsonl 2>/dev/null
done; done; done
step scale
S=$H/scale_p1c.sh
: > $R/scale.jsonl
for n in 1000 10000 100000; do for v in base final; do d=$BASE; [ $v = final ] && d=$FIN; $S $v $d $R/scale.jsonl $n 0 4kib; done; done
for v in base final; do d=$BASE; [ $v = final ] && d=$FIN; $S $v $d $R/scale.jsonl 100000 0.5 4kib; $S $v $d $R/scale.jsonl 100000 0.9 4kib; done
$S final $FIN $R/scale.jsonl 1000000 0 4kib
$S base $BASE $R/scale.jsonl 1000000 0 4kib
step done
