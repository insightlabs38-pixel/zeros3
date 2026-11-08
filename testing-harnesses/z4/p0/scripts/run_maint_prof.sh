#!/bin/sh
# Profiled maintenance operations on the largest rungs (separate from the timing ladder).
. "$(dirname "$0")/lib.sh"
F=maint_prof.jsonl; mkdir -p "$P0_RES/prof"; BASE=$P0_WORK/base
sum() { for d in "$P0_WORK"/prof/$1-r*; do [ -d "$d" ] && "$(dirname "$0")/profsum.sh" "$d" "$P0_RES/prof/$(basename "$d").txt"; done; }
C() { l=$1; shift; clirun $l $F -prof full "$@"; sum $l; }
O=$BASE/objs64000
C mp_objs64k_stats      -store $O -cache warm stats -store $O -json
C mp_objs64k_vdeep      -store $O -cache warm verify -deep -store $O -json
C mp_objs64k_doctor     -store $O -cache warm doctor -store $O -json
C mp_objs64k_compactdry -store $O -cache warm compact -dry-run -store $O -json
W=$P0_WORK/mp_objs; rm -rf $W; cp -a $O $W
C mp_objs64k_prune      -store $W -cache warm versions prune -store $W -bucket b -prefix "" -keep-last 0 -apply -json
C mp_objs64k_gc_apply   -store $W -cache warm gc -apply -store $W -json
C mp_objs64k_compact    -store $W -cache warm compact -store $W -json
C mp_objs64k_repack     -store $W -cache warm repack -apply -max-live-percent 100 -compression off -store $W -json
$P0 startup -label mp_objs64k_startup -prof full -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -profbin $P0_BIN/zeros3-prof -dir $W -cache cold -reps 1 >/dev/null; sum mp_objs64k_startup
rm -rf $W
B=$BASE/bytes1024
W=$P0_WORK/mp_bytes; rm -rf $W; cp -a $B $W
C mp_bytes1024_compact  -store $W -cache warm compact -store $W -json
C mp_bytes1024_vdeep    -store $W -cache warm verify -deep -store $W -json
rm -rf $W
W=$P0_WORK/mp_bytes; rm -rf $W; cp -a $B $W
C mp_bytes1024_tiermove -store $W -cache warm tier move -store $W -from hot -to warm -all -apply -json
rm -rf $W
echo MAINT_PROF_DONE
