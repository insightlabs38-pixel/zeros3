#!/bin/bash
# Integrated production workloads (workstream I): ingest -> reads -> lifecycle maintenance -> reads -> replay, on a
# persistent store through the real server and CLI. usage: run_integrated.sh CORPUS_DIR [workload ...]
. "$(dirname "$0")/lib.sh"
C=$(realpath "${1:?corpus}"); shift
F=integrated.jsonl; S=$P0_WORK/integ; mkdir -p $S
wl() { # NAME MODE PRUNE mkstore-args...
  n=$1; mode=$2; prune=$3; shift 3
  sa=""; [ $mode != off ] && sa="-representations $mode"
  d=$S/${n}_$mode; rm -rf $d $d.manifest.jsonl
  echo "#### $n $mode"
  $P0 mkstore -label int_${n}_${mode}_ingest -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -sargs "$sa" "$@" >/dev/null || { echo "INGEST FAILED $n" >&2; return; }
  man=$d.manifest.jsonl
  $P0 readstore -label int_${n}_${mode}_read_range64k_cold -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -manifest $man -mode range -rsize 64kib -nreq 300 -cache cold -reps 1 -sargs "$sa" >/dev/null
  $P0 readstore -label int_${n}_${mode}_read_whole_cold -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -manifest $man -mode whole -nreq 20 -cache cold -reps 1 -sargs "$sa" >/dev/null
  clirun int_${n}_${mode}_stats $F -store $d -cache warm stats -store $d -json
  [ $prune = yes ] && clirun int_${n}_${mode}_prune $F -store $d -cache warm versions prune -store $d -bucket bench -prefix "" -keep-last 2 -apply -json
  clirun int_${n}_${mode}_gc $F -store $d -cache warm gc -apply -store $d -json
  clirun int_${n}_${mode}_compact $F -store $d -cache warm compact -store $d -json
  clirun int_${n}_${mode}_repack $F -store $d -cache warm repack -apply -max-live-percent 80 -store $d -json
  clirun int_${n}_${mode}_gc2 $F -store $d -cache warm gc -apply -store $d -json
  clirun int_${n}_${mode}_vdeep $F -store $d -cache cold verify -deep -store $d -json
  $P0 readstore -label int_${n}_${mode}_read_range64k_packed -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -manifest $man -mode range -rsize 64kib -nreq 300 -cache cold -reps 1 -sargs "$sa" >/dev/null
  $P0 readstore -label int_${n}_${mode}_read_whole_packed -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -manifest $man -mode whole -nreq 20 -cache cold -reps 1 -sargs "$sa" >/dev/null
  $P0 verifystore -label int_${n}_${mode}_replay -out $P0_RES/$F -work $P0_WORK -bin $P0_BIN/zeros3 -dir $d -manifest $man -sargs "$sa" >/dev/null || echo "REPLAY FAILED $n $mode" >&2
  du -sb $d | awk -v n=$n -v m=$mode '{printf "{\"label\":\"int_%s_%s_final_bytes\",\"scenario\":\"size\",\"bytes\":%d}\n",n,m,$1}' >> $P0_RES/$F
  rm -rf $d
}
W=${*:-archive registry artifacts database model general}
for w in $W; do case $w in
 archive)   wl archive off yes -objs 8 -size 24mib -revs 8 -editfrac 0.02 -conc 4 -bucket bench ;;
 registry)  wl registry off yes -objs 500 -sizes 4k:30,64k:25,1m:20,4m:20,16m:5 -revs 2 -editfrac 0.1 -dupfrac 0.2 -conc 4 -bucket bench ;;
 artifacts) for m in off shadow; do wl artifacts $m no -files $(ls $C/src/coreutils-9.*.tar $C/q6/fdroid-apk/*.dex $C/q6/clr/*.dll $C/git/h*.pack | tr '\n' ',' | sed 's/,$//') -conc 1 -bucket bench; done ;;
 database)  for m in off shadow; do wl database $m no -files $(ls $C/sqlite/v*.db $C/parquet/zstd/c*.parquet $C/sst/zstd/c*.sst | tr '\n' ',' | sed 's/,$//') -conc 1 -bucket bench; done ;;
 model)     for m in off shadow; do wl model $m no -files $(ls $C/models/pythia70m/*.safetensors $C/models/smollm2/base.safetensors $C/models/smollm2/instruct.safetensors | tr '\n' ',' | sed 's/,$//') -conc 1 -bucket bench; done ;;
 general)   wl general off no -objs 1500 -sizes 4k:50,64k:30,1m:15,16m:5 -conc 4 -delfrac 0.1 -bucket bench ;;
esac; done
echo INTEGRATED_DONE
