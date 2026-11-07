#!/bin/bash
# Discovery, order/matrix, coder benchmark, B1/Model8 baselines. usage: run_experiments2.sh BIN EVDIR
set -euo pipefail
BIN=$1; EV=$2; D=${D:-/home/user/b5data}; mkdir -p "$EV"; P=$D/pythia70m; P14=$D/pythia14m; S=$D/smollm2; Q=$D/qwen05; GP=$D/gpt2
source "$(dirname "$0")/families.sh"
dc() { n=$1; shift; [ -s "$EV/discover_$n.json" ] && return 0; $BIN discover -json "$EV/discover_$n.json" "$@" > "$EV/discover_$n.txt"; }
dc pythia14m   $P14/step64000.safetensors $P14/step1000.safetensors $P14/step2000.safetensors $P14/step3000.safetensors $P14/step4000.safetensors $P14/step8000.safetensors $P14/step16000.safetensors $P14/step32000.safetensors
dc pythia70m   $P/step102000.safetensors $P/step100000.safetensors $P/step101000.safetensors $P/step64000.safetensors $P/dedup_step100000.safetensors $P/dedup_step0.safetensors $P/step0.safetensors
dc pythia70m_renamed -rename 6 $P/step102000.safetensors $P/step100000.safetensors $P/step101000.safetensors $P/step64000.safetensors $P/dedup_step100000.safetensors $P/dedup_step0.safetensors $P/step0.safetensors
dc gpt2        $GP/imdb_pos.safetensors $GP/base.safetensors $GP/imdb.safetensors
dc smollm2     $S/instruct.safetensors $S/base.safetensors $S/sft_only.safetensors $S/shakespeare.safetensors $S/capybara.safetensors $S/smollm_v1.safetensors
dc smollm2_unrelated $S/smollm_v1.safetensors $S/base.safetensors $S/instruct.safetensors $S/sft_only.safetensors
# order / re-rooting matrices
for f in pythia14m gpt2 smollm2 pythia70m qwen05; do [ -s "$EV/matrix_$f.json" ] && continue; $BIN matrix -json "$EV/matrix_$f.json" $(eval echo \$fam_$f) > "$EV/matrix_$f.txt"; done
echo done > "$EV/experiments2.done"
