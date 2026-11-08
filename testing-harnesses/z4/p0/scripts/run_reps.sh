#!/bin/bash
# Production representation kinds 1-12 through the real server (workstream E).
# usage: run_reps.sh CORPUS_DIR   (see fetch_corpus.sh). Each kind: CDC baseline (off), shadow, prefer.
. "$(dirname "$0")/lib.sh"
C=$(realpath "${1:?corpus}"); F=reps.jsonl
LISTS=$P0_WORK/replists; mkdir -p $LISTS
ls_() { ls -1 "$@" 2>/dev/null | sort; }
declare -A K
K[k01_tar]="$(ls_ $C/src/coreutils-9.{5,6,7}.tar)"
K[k02_car]="$(ls_ $C/q6/car/*.car.car)"
K[k03_sqlite]="$(ls_ $C/sqlite/v*.db)"
K[k04_git]="$(ls_ $C/git/h*.pack)"
K[k05_merkle]="$(ls_ $C/merkle/*.snap)"
K[k06_gzip]="$(ls_ $C/src/coreutils-9.{5,6,7}.tar.gz)"
K[k07_zstd]="$(ls_ $C/zstd/*.zst)"
K[k08_model_pythia]="$(ls_ $C/models/pythia14m/*.safetensors)"
K[k08_model_smollm2]="$(ls_ $C/models/smollm2/base.safetensors $C/models/smollm2/instruct.safetensors)"
K[k09_sst_zstd]="$(ls_ $C/sst/zstd/*.sst)"
K[k09_sst_snappy]="$(ls_ $C/sst/snappy/*.sst)"
K[k10_parquet_zstd]="$(ls_ $C/parquet/zstd/*.parquet)"
K[k10_parquet_snappy]="$(ls_ $C/parquet/snappy/*.parquet)"
K[k11_dex]="$(ls_ $C/q6/fdroid-apk/*.dex)"
K[k12_clr]="$(ls_ $C/q6/clr/*.dll)"
REPS=${REPS:-3}
for k in $(echo "${!K[@]}" | tr ' ' '\n' | sort); do
  R=$REPS; case $k in k08_model_smollm2) R=1;; esac
  [ -n "${K[$k]}" ] || { echo "NO INPUT $k" >&2; echo "{\"label\":\"$k\",\"scenario\":\"skipped\",\"reason\":\"no input files\"}" >> $P0_RES/$F; continue; }
  echo "${K[$k]}" > $LISTS/$k.txt
  for mode in off shadow prefer; do
    sa=""; [ $mode != off ] && sa="-representations $mode"
    putrun ${k}_put_$mode $F -files @$LISTS/$k.txt -conc 1 -reps $R -sargs "$sa"
    getrun ${k}_get_$mode $F -files @$LISTS/$k.txt -mode whole -conc 1 -cache warm -reps $R -sargs "$sa" -nreq $(wc -l < $LISTS/$k.txt)
    getrun ${k}_r4k_$mode $F -files @$LISTS/$k.txt -mode range -rsize 4kib -nreq 1000 -conc 1 -cache cold -reps $R -sargs "$sa"
    getrun ${k}_r1m_$mode $F -files @$LISTS/$k.txt -mode range -rsize 1mib -nreq 300 -conc 1 -cache cold -reps $R -sargs "$sa"
  done
done
echo REPS_DONE
