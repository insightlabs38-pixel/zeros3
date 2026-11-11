#!/bin/bash
# usage: b3_scale.sh BIN WORKDIR OUTDIR   -- bounded-memory scale sweep over synthetic related families
# For N in 64,256,1024 MiB: generate (a BF16, b exact F32, c exact F16, d reordered a, e unrelated), ingest in one
# fresh process with a soft heap limit, then replay in another fresh process and verify with hashlib.
set -euo pipefail
BIN=$1; W=$2; O=$3; D=$(cd "$(dirname "$0")" && pwd); M="python3 $D/../../b3/measure.py"; mkdir -p "$W" "$O"
for N in ${SIZES:-64 256 1024}; do
  big=0; [ "$N" = 256 ] && big=320
  rm -rf "$W/n$N" "$W/st$N"; "$BIN" gen -dir "$W/n$N" -mib $N -tensor-mib 8 -big-mib $big > /dev/null
  files=$(ls "$W/n$N"/*.safetensors | tr '\n' ' ')
  logical=$(cat $files | wc -c 2>/dev/null || du -cb $files | tail -1 | cut -f1)
  GOMEMLIMIT=${LIMIT:-512MiB} $M "$BIN" ingest -store "$W/st$N" -cache-mib 64 -work-mib 128 $files > "$O/scale_n${N}_ingest.jsonl"
  python3 -I "$D/verify_replay.py" "$BIN" "$W/st$N" "$W/out$N" $files > "$O/scale_n${N}_replay.jsonl" || echo "REPLAY MISMATCH N=$N"
  "$BIN" cost -store "$W/st$N" > "$O/scale_n${N}_cost.json"
  echo "N=$N logical_bytes=$(du -cb $files | tail -1 | cut -f1) $(tail -1 "$O/scale_n${N}_ingest.jsonl") $(tail -1 "$O/scale_n${N}_replay.jsonl")"
  rm -rf "$W/n$N" "$W/st$N"
done
