#!/bin/sh
# Maintenance / lifecycle scale ladders (workstream D).
# usage: run_maint.sh SERIES   where SERIES = objs | bytes | hist
# Base stores are built once through the real server; every operation runs on a fresh
# `cp -a` copy so mutating steps never contaminate each other. Fully deterministic.
. "$(dirname "$0")/lib.sh"
SERIES=${1:?series}
F=maint_$SERIES.jsonl
BASE=$P0_WORK/base; mkdir -p "$BASE"
CP="cp -a"
# ops_readonly NAME DIR : read-only maintenance commands, cold + warm
ops_readonly() {
  n=$1; d=$2
  for cache in cold warm; do
    clirun ${n}_stats_$cache   $F -store $d -cache $cache stats -store $d -json
    clirun ${n}_verify_$cache  $F -store $d -cache $cache verify -store $d -json
    clirun ${n}_vdeep_$cache   $F -store $d -cache $cache verify -deep -store $d -json
    clirun ${n}_doctor_$cache  $F -store $d -cache $cache doctor -store $d -json
    clirun ${n}_gcdry_$cache   $F -store $d -cache $cache gc -store $d -json
    clirun ${n}_compactdry_$cache $F -store $d -cache $cache compact -dry-run -store $d -json
  done
  $P0 startup -label ${n}_startup_cold -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -cache cold -reps 3 >/dev/null
  $P0 startup -label ${n}_startup_warm -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -cache warm -reps 5 >/dev/null
}
# ops_mutating NAME BASEDIR BUCKET : each step on its own copy of the base (R) or chained (C)
ops_mutating() {
  n=$1; b=$2; bucket=$3
  W=$P0_WORK/mut_$n
  # C: chained retire-history -> gc -> compact -> repack -> verify
  rm -rf $W; $CP $b $W
  clirun ${n}_prune_apply   $F -store $W -cache warm versions prune -store $W -bucket $bucket -prefix "" -keep-last 0 -apply -json
  clirun ${n}_gc_apply      $F -store $W -cache warm gc -apply -store $W -json
  clirun ${n}_compact_auto  $F -store $W -cache warm compact -store $W -json
  clirun ${n}_vdeep_packed  $F -store $W -cache cold verify -deep -store $W -json
  clirun ${n}_repack_dry    $F -store $W -cache warm repack -store $W -json
  clirun ${n}_repack_apply  $F -store $W -cache warm repack -apply -max-live-percent 100 -compression off -store $W -json
  clirun ${n}_gc_apply2     $F -store $W -cache warm gc -apply -store $W -json
  # restart/recovery on the maintained, packed store
  $P0 startup -label ${n}_startup_packed -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $W -cache cold -reps 3 >/dev/null
  $P0 verifystore -label ${n}_replay -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $W -manifest $b.manifest.jsonl >/dev/null || { echo "REPLAY FAILED $n" >&2; exit 1; }
  rm -rf $W
  # tier move cost on a compacted copy
  rm -rf $W; $CP $b $W
  $P0_BIN/zeros3 compact -store $W >/dev/null
  $P0_BIN/zeros3 tier init -store $W -tier warm >/dev/null
  clirun ${n}_tier_move_all $F -store $W -cache warm tier move -store $W -from hot -to warm -all -apply -json
  rm -rf $W
  # snapshot create / bundle export through a live server
  rm -rf $W; $CP $b $W
  clirun ${n}_snapshot_create $F -store $W -cache warm -serve snapshot create -endpoint {EP} -json s3://$bucket/
  rm -rf $W
}
mk() { # NAME args... : build base store unless present
  n=$1; shift
  [ -d $BASE/$n ] || $P0 mkstore -label mk_$n -out $P0_RES/mkstore.jsonl -work $P0_WORK -bin $P0_BIN/zeros3 -dir $BASE/$n "$@" >/dev/null || { echo "MKSTORE FAILED $n" >&2; exit 1; }
}
case $SERIES in
objs)   # object-count ladder, small mixed objects, 2 revisions, 30% deleted
  for N in 1000 4000 16000 64000; do
    mk objs$N -objs $N -sizes 4k:60,16k:25,64k:15 -revs 2 -editfrac 0.5 -delfrac 0.3 -conc 8 -bucket b
    ops_readonly objs$N $BASE/objs$N
    ops_mutating objs$N $BASE/objs$N b
  done ;;
bytes)  # retained-bytes / chunk-count ladder: 4 MiB objects, 1/4/16 GiB would be 256/1024/4096 objs
  for N in 64 256 1024; do
    mk bytes$N -objs $N -size 4mib -revs 1 -conc 4 -bucket b -dupfrac 0.1
    ops_readonly bytes$N $BASE/bytes$N
    ops_mutating bytes$N $BASE/bytes$N b
  done ;;
hist)   # version-history ladder: 16 objects x 4 MiB, V revisions each 2% edited, kept as history
  for V in 1 4 16 64; do
    mk hist$V -objs 16 -size 4mib -revs $V -editfrac 0.02 -conc 4 -bucket b -revkeys same
    ops_readonly hist$V $BASE/hist$V
    ops_mutating hist$V $BASE/hist$V b
  done ;;
esac
echo MAINT_${SERIES}_DONE
