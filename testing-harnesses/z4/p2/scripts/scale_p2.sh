#!/bin/bash
# Cache sizing and store-scale runs for one binary directory.
# Usage: scale_p2.sh BINDIR OUT.jsonl OBJS SIZE MODE   (MODE = range | whole)
# Builds a store of OBJS objects through a real server (mkstore), then reads it uniformly at random (the worst case for a
# cache: the working set is the whole store) with the manifest/chunk cache budgets below, one fresh server process per run.
# Each run records ops/s, latency, server peak RSS and the caches' own counters (ZEROS3_READ_CACHE_STATS, written at shutdown).
set -eu
BIN=$1; OUT=$2; N=$3; SIZE=$4; MODE=${5:-range}
W=${P2_WORK:-/tmp/claude-0/p2/work}/scale-$N-$SIZE; rm -rf "$W"; mkdir -p "$W"
D=$BIN/p0bench; S=$W/store
note="objs=$N size=$SIZE mode=$MODE"
$D mkstore -label mk_${N}_$SIZE -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -dir $S -objs $N -size $SIZE -conc 16 >/dev/null
# manifest MiB : chunk MiB
for cfg in "0:0" "1:1" "8:8" "32:64" "256:256"; do
  mm=${cfg%%:*}; cm=${cfg##*:}
  ST=$W/stats-$mm-$cm.json; rm -f $ST
  P0_NOTE="$note mcache=$mm ccache=$cm" $D readstore -label scale_${N}_${SIZE}_${MODE} -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof \
    -dir $S -mode $MODE -rsize 4kib -nreq ${NREQ:-20000} -conc 8 -reps 2 -cache warm \
    -env ZEROS3_MANIFEST_CACHE_MIB=$mm,ZEROS3_CHUNK_CACHE_MIB=$cm,ZEROS3_READ_CACHE_STATS=$ST >/dev/null
  [ -f $ST ] && echo "{\"scenario\":\"cache_stats\",\"note\":\"$note mcache=$mm ccache=$cm\",\"stats\":$(cat $ST | paste -sd, | sed 's/^/[/;s/$/]/')}" >> $OUT
done
rm -rf "$W"
