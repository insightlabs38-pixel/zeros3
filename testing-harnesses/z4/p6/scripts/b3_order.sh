#!/bin/bash
# usage: b3_order.sh BIN_NEW BIN_BASE HFDIR WORKDIR OUTDIR
# Input-order sensitivity of the pinned SmolLM2 family: forward and reverse online ingest, the pre-P6
# offline pass (B3 'optimize'), and the P6 group-aware root planner ('roots -apply'); every store is
# replayed in a fresh process and verified with hashlib.
set -euo pipefail
NEW=$1; BASE=$2; HF=$3; W=$4; O=$5; D=$(cd "$(dirname "$0")" && pwd); S=$HF/smol; mkdir -p "$W" "$O"
M="python3 $D/../../b3/measure.py"
fwd="$S/model.safetensors $S/f16.gguf $S/q8_0.gguf $S/model_fp16.onnx $S/model.onnx"
rev="$S/q8_0.gguf $S/f16.gguf $S/model.onnx $S/model_fp16.onnx $S/model.safetensors"
cost() { "$NEW" cost -store "$1" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["total"])'; }
rm -rf "$W/fwd" "$W/rev" "$W/rev_old" "$W/rev_new" "$W/fwd_new"
GOMEMLIMIT=768MiB $M "$NEW" ingest -store "$W/fwd" -cache-mib 128 -work-mib 256 $fwd > "$O/order_fwd_ingest.jsonl"
GOMEMLIMIT=768MiB $M "$NEW" ingest -store "$W/rev" -cache-mib 128 -work-mib 256 $rev > "$O/order_rev_ingest.jsonl"
cp -r "$W/rev" "$W/rev_old"; cp -r "$W/rev" "$W/rev_new"; cp -r "$W/fwd" "$W/fwd_new"
$M "$BASE" optimize -store "$W/rev_old" > "$O/order_rev_old_optimize.jsonl"
$M "$NEW" roots -store "$W/rev_new" -apply -groups > "$O/order_rev_roots.jsonl"
$M "$NEW" roots -store "$W/fwd_new" -apply > "$O/order_fwd_roots.jsonl"
for s in fwd rev rev_old rev_new fwd_new; do echo "{\"store\":\"$s\",\"total_bytes\":$(cost $W/$s)}"; done > "$O/order_costs.jsonl"
for s in rev_old rev_new fwd_new; do
  python3 -I "$D/verify_replay.py" "$NEW" "$W/$s" "$W/out_$s" $fwd > "$O/order_replay_$s.jsonl" || echo "REPLAY MISMATCH $s"
done
cat "$O/order_costs.jsonl"; tail -1 "$O/order_rev_roots.jsonl"
