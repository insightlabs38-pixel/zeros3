#!/bin/bash
# usage: b3_ranges.sh BIN STORE HFDIR OUT.jsonl   -- random 4 KiB/64 KiB/1 MiB range reads per component kind,
# unverified legacy reader (block=0) versus verified blocks of 4/16/64 KiB. Every returned range is compared
# with the original file inside the driver.
set -euo pipefail
BIN=$1; ST=$2; HF=$3; OUT=$4; S=$HF/smol; : > "$OUT"
for f in model.onnx model_fp16.onnx f16.gguf q8_0.gguf model.safetensors; do
  for b in 0 4096 16384 65536; do "$BIN" ranges -store "$ST" -orig "$S/$f" -block $b -n ${N:-200} >> "$OUT"; done
done
