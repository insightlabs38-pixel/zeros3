#!/bin/bash
# Quantized lane: float->quant reproduction + sibling prediction, with fresh-process replay. usage: run_quant.sh BIN OUT
set -euo pipefail
BIN=$1; OUT=$2; HERE=$(cd "$(dirname "$0")" && pwd); source "$HERE/families.sh"; mkdir -p "$OUT/quant"
run() { # name files parents heads hfmap
  n=$1; files=$2; par=$3; heads=$4; hf=$5; S="$OUT/quant/store_$n"
  [ -s "$OUT/quant/$n.verify.json" ] && [ "${FORCE:-0}" = 0 ] && return 0
  rm -rf "$S" "$OUT/quant/replay_$n"
  $BIN family -policy explicit -parents "$par" -heads $heads $hf -store "$S" -json "$OUT/quant/$n.json" $files > "$OUT/quant/$n.log"
  (cd / && $BIN replay -store "$S" -out "$OUT/quant/replay_$n") > "$OUT/quant/$n.replay.jsonl"
  python3 -I "$HERE/verify_replay.py" "$S" "$OUT/quant/replay_$n" > "$OUT/quant/$n.verify.json"
  rm -rf "$S" "$OUT/quant/replay_$n"
}
run smol_with_bf16 "$fam_smolq" "$par_smolq" 9,3 -hfmap
run smol_gguf_only "$fam_smolqonly" "$par_smolqonly" 9,3 ""
run qwen_with_bf16 "$fam_qwenq" "$par_qwenq" 0,0 -hfmap
run qwen_gguf_only "$fam_qwenqonly" "$par_qwenqonly" 0,0 ""
for n in smolq smolqonly qwenq qwenqonly; do
  $BIN cdc $(eval echo \$fam_$n) > "$OUT/quant/cdc_$n.jsonl"
done
$BIN model8 $fam_smolqonly > "$OUT/quant/model8_smolqonly.json"
$BIN model8 $fam_qwenqonly > "$OUT/quant/model8_qwenqonly.json"
for g in qf-inst-Q8_0 qf-inst-Q5_K_M qf-inst-Q4_K_M qf-inst-Q6_K; do
  $BIN quant-repro $D/smollm2/instruct.safetensors $G/gguf-smollm2__$g.gguf 9 3 > "$OUT/quant/repro_smol_$g.txt"
done
echo done > "$OUT/quant/quant.done"
