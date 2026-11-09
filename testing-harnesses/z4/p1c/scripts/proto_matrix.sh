#!/bin/bash
# Interleaved prototype comparison. Usage: proto_matrix.sh BIN WORKDIR OUT.jsonl ROUNDS
BIN=$1; W=$2; OUT=$3; R=${4:-3}
for r in $(seq 1 $R); do
 for cfg in "4096 1 800" "4096 4 2400" "4096 8 3000" "4096 32 4000" "65536 8 1500" "16384 8 2400" "262144 8 800"; do
  set -- $cfg; size=$1; c=$2; n=$3
  for mode in loose pack packinl seg seginl; do
   $BIN -mode $mode -dir $W/run -c $c -n $n -size $size -note "round=$r" >> $OUT 2>>$OUT.err || echo "FAIL $mode $cfg" >&2
  done
 done
done
