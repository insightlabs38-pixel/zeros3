#!/bin/bash
# churn_p1c.sh VARIANT BINDIR OUT.jsonl OBJS DELFRAC : space reclamation under churn.
# Populates OBJS 4 KiB objects, deletes DELFRAC of them, prunes all retained history (so the data is truly dead),
# then measures store size / files after `gc -apply` and after `repack -apply`.
set -eu
V=$1; BIN=$2; OUT=$3; N=$4; DEL=$5
W=${P1C_WORK:-/tmp/claude-0/p1c/work}/churn-$V-$N-$DEL; rm -rf "$W"; mkdir -p "$W"; D=$BIN/p0bench; S=$W/store
export P0_NOTE="variant=$V objs=$N delfrac=$DEL"
meas() { echo "{\"scenario\":\"churn\",\"stage\":\"$1\",\"note\":\"variant=$V objs=$N delfrac=$DEL\",\"files\":$(find $S -type f | wc -l),\"du_kib\":$(du -sk $S | cut -f1)}" >> $OUT; }
$D mkstore -label churn_mk -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -dir $S -objs $N -size 4kib -conc 16 -delfrac $DEL >/dev/null
meas populated
$D cli -label churn_prune -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -store $S -- versions prune -store $S -bucket bench -keep-last 0 -apply >/dev/null || echo "prune failed" >&2
meas pruned
$D cli -label churn_gc -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -store $S -- gc -store $S -apply >/dev/null
meas after_gc
$D cli -label churn_repack -out $OUT -work $W -bin $BIN/zeros3 -profbin $BIN/zeros3-prof -store $S -- repack -store $S -apply >/dev/null
meas after_repack
rm -rf "$W"
