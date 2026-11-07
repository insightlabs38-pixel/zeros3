#!/bin/bash
# Performance, memory, range amplification and coder throughput on a quiet machine. usage: run_perf.sh BIN OUT
set -euo pipefail
BIN=$1; OUT=$2; HERE=$(cd "$(dirname "$0")" && pwd); source "$HERE/families.sh"; mkdir -p "$OUT/perf"; D=${D:-/home/user/b5data}
P=$D/pythia70m; S=$D/smollm2; Q=$D/qwen05
cb() { n=$1; shift; [ -s "$OUT/perf/coder_$n.json" ] && return 0; $BIN coderbench -json "$OUT/perf/coder_$n.json" -label $n "$@" > "$OUT/perf/coder_$n.txt"; }
cb pythia70m_1000step $P/step101000.safetensors $P/step100000.safetensors
cb smollm2_instruct_vs_base $S/instruct.safetensors $S/base.safetensors
cb smollm2_shakespeare_vs_base $S/shakespeare.safetensors $S/base.safetensors
cb qwen_instruct_vs_base $Q/instruct.safetensors $Q/base.safetensors
cb qwen_abliterated_vs_instruct $Q/abliterated.safetensors $Q/instruct.safetensors
# retained stores: whole-model replay time and peak RSS, then range reads
for cfg in "smol:smollm2:pool3:65536" "smol_nosplit:smollm2:pool3:0" "pythia70m:pythia70m:prev:65536"; do
  IFS=: read n fam pol rs <<< "$cfg"; S2="$OUT/perf/store_$n"; rm -rf "$S2"
  files=$(eval echo \$fam_$fam)
  $BIN family -policy $pol -rangeseg $rs -store "$S2" $files > "$OUT/perf/family_$n.log" 2> "$OUT/perf/family_$n.time"
  (cd / && $BIN replay -store "$S2" -out "$OUT/perf/replay_$n") > "$OUT/perf/replay_$n.jsonl" 2> "$OUT/perf/replay_$n.time"
  python3 -I "$HERE/verify_replay.py" "$S2" "$OUT/perf/replay_$n" > "$OUT/perf/verify_$n.json"
  rm -rf "$OUT/perf/replay_$n"
  last=$(echo $files | tr ' ' '\n' | grep -v dedup | tail -1); [ $fam = smollm2 ] && last=$D/smollm2/kd_alpaca.safetensors
  $BIN ranges -store "$S2" -orig "$last" -n 60 > "$OUT/perf/ranges_$n.txt"
  rm -rf "$S2"
done
echo done > "$OUT/perf/perf.done"
