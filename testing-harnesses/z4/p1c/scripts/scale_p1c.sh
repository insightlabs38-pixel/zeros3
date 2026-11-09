#!/bin/bash
# Scale / long-history / lifecycle runs for one binary directory (plain zeros3 + p0bench).
# Usage: scale_p1c.sh VARIANT BINDIR OUT.jsonl OBJS [DELFRAC] [SIZE]
# Builds a store of OBJS objects through a real server (mkstore), then measures: store inodes/bytes, open ("startup")
# latency warm and cold, `verify -deep`, `gc` dry-run/apply and `repack` (all untimed-populate, timed-maintenance).
set -eu
V=$1; BIN=$2; OUT=$3; N=$4; DEL=${5:-0}; SIZE=${6:-4kib}
W=${P1C_WORK:-/tmp/claude-0/p1c/work}/scale-$V-$N; rm -rf "$W"; mkdir -p "$W"
D=$BIN/p0bench; S=$W/store
note="variant=$V objs=$N delfrac=$DEL size=$SIZE"
export P0_NOTE="$note"
$D mkstore -label mk_$N -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -dir $S -objs $N -size $SIZE -conc 16 -delfrac $DEL >/dev/null
inodes=$(find $S -type f | wc -l); dirs=$(find $S -type d | wc -l)
echo "{\"scenario\":\"inodes\",\"note\":\"$note\",\"files\":$inodes,\"dirs\":$dirs,\"du_kib\":$(du -sk $S | cut -f1)}" >> $OUT
$D startup -label startup_warm_$N -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -dir $S -reps 3 >/dev/null
$D startup -label startup_cold_$N -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -dir $S -reps 3 -cache cold >/dev/null
for c in "verify -deep" "gc" "gc -apply" "repack -apply"; do
  l=$(echo $c | tr ' -' '__')
  $D cli -label cli_${l}_$N -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -store $S -- $c -store $S >/dev/null || echo "cli $c failed" >&2
done
inodes=$(find $S -type f | wc -l)
echo "{\"scenario\":\"inodes_after_maintenance\",\"note\":\"$note\",\"files\":$inodes,\"du_kib\":$(du -sk $S | cut -f1)}" >> $OUT
rm -rf "$W"
