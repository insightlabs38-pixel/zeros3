#!/bin/bash
# Master driver: records the exit status of every family run in OUT/status.txt (no failure is hidden).
BIN=$1; OUT=$2; HERE=$(cd "$(dirname "$0")" && pwd); mkdir -p "$OUT"; touch "$OUT/status.txt"
for f in pythia14m gpt2 pythia70m smollm2 smollm2ctl; do
  "$HERE/run_policies.sh" "$BIN" "$OUT" $f > "$OUT/run_$f.log" 2>&1; echo "$f exit=$?" >> "$OUT/status.txt"
done
"$HERE/run_policies.sh" "$BIN" "$OUT" qwen05 direct first prev sel2 lin2 > "$OUT/run_qwen05.log" 2>&1; echo "qwen05 exit=$?" >> "$OUT/status.txt"
echo ALLDONE >> "$OUT/status.txt"
