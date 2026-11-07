#!/bin/bash
# B1 (aligned-chunk oracle) and lab Model8 baselines. usage: run_baselines.sh BIN EVDIR
set -euo pipefail
BIN=$1; EV=$2; mkdir -p "$EV"; source "$(dirname "$0")/families.sh"
for f in pythia14m gpt2 pythia70m smollm2; do
  $BIN model8 $(eval echo \$fam_$f) > "$EV/model8_$f.json"
done
$BIN model8 $fam_qwen05 > "$EV/model8_qwen05.json"
for f in pythia14m gpt2 smollm2 qwen05 pythia70m; do
  algs="xor,plane,adaptive,pxr"; [ $f = qwen05 ] && algs="xor,plane,adaptive"
  $BIN b1 -algs $algs -json "$EV/b1_$f.json" $(eval echo \$fam_$f) > "$EV/b1_$f.txt"
done
echo done > "$EV/baselines.done"
