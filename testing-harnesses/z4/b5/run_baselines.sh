#!/bin/bash
# B1 (aligned-chunk oracle) and lab Model8 baselines; resumable, runs every step and exits nonzero if any failed.
# usage: run_baselines.sh BIN EVDIR
BIN=$1; EV=$2; mkdir -p "$EV"; source "$(dirname "$0")/families.sh"; fail=0
for f in pythia14m gpt2 pythia70m smollm2 qwen05; do
  [ -s "$EV/model8_$f.json" ] || { $BIN model8 $(eval echo \$fam_$f) > "$EV/model8_$f.json" || { echo "model8 $f failed"; rm -f "$EV/model8_$f.json"; fail=1; }; }
done
for f in pythia14m gpt2 smollm2 qwen05 pythia70m; do
  algs="xor,plane,adaptive,pxr"; [ $f = qwen05 ] && algs="xor,plane,adaptive"
  [ -s "$EV/b1_$f.json" ] || { $BIN b1 -algs $algs -json "$EV/b1_$f.json" $(eval echo \$fam_$f) > "$EV/b1_$f.txt" || { echo "b1 $f failed"; fail=1; }; }
done
[ $fail = 0 ] && echo done > "$EV/baselines.done"
exit $fail
