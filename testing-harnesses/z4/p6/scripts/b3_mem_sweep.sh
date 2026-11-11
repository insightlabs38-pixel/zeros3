#!/bin/bash
# usage: b3_mem_sweep.sh BIN HFDIR OUTDIR   -- SmolLM2 family ingest under different memory settings.
# Each configuration is a fresh process; wall/RSS come from measure.py (getrusage of the child).
set -euo pipefail
BIN=$1; HF=$2; O=$3; D=$(cd "$(dirname "$0")/../../b3" && pwd); S=$HF/smol; mkdir -p "$O"
files="$S/model.safetensors $S/f16.gguf $S/q8_0.gguf $S/model_fp16.onnx $S/model.onnx"
run() { # name gomemlimit cache_mib work_mib parallel
  rm -rf "$O/$1"; GOMEMLIMIT=$2 python3 "$D/measure.py" "$BIN" ingest -store "$O/$1" -cache-mib "$3" -work-mib "$4" -parallel "$5" $files > "$O/$1.jsonl"
  echo "$1 $(tail -1 "$O/$1.jsonl")"; }
run default off 256 512 0
run lim768  768MiB 128 256 0
run lim512  512MiB 64 128 0
run seq512  512MiB 64 128 1
