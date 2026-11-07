#!/bin/bash
# Track D/E/B/G experiments with machine-readable output. usage: run_experiments.sh BIN EVDIR   (D = corpus dir)
set -euo pipefail
BIN=$1; EV=$2; D=${D:-/home/user/b5data}; mkdir -p "$EV"; P=$D/pythia70m; S=$D/smollm2; Q=$D/qwen05; C=$D/controls
ex() { n=$1; shift; [ -s "$EV/explore_$n.json" ] && return 0; $BIN explore -json "$EV/explore_$n.json" "$@" > "$EV/explore_$n.txt"; }
sp() { n=$1; shift; [ -s "$EV/sparse_$n.json" ] && return 0; $BIN sparse -json "$EV/sparse_$n.json" "$@" > "$EV/sparse_$n.txt"; }
# D: numerical prediction frontier
ex pythia_prev     $P/step101000.safetensors $P/step100000.safetensors
ex pythia_interp   $P/step101000.safetensors $P/step100000.safetensors $P/step102000.safetensors      # non-causal oracle: interpolation
ex pythia_extrap   $P/step102000.safetensors $P/step101000.safetensors $P/step100000.safetensors      # causal: previous + older
ex pythia_anchor   $P/step102000.safetensors $P/step101000.safetensors $P/step64000.safetensors       # previous + anchor
ex pythia_far      $P/step143000.safetensors $P/step100000.safetensors
ex pythia_sibling  $P/dedup_step100000.safetensors $P/step100000.safetensors
ex smol_instruct   $S/instruct.safetensors $S/base.safetensors
ex smol_sft        $S/sft_only.safetensors $S/base.safetensors
ex smol_dpo        $S/instruct.safetensors $S/sft_only.safetensors
ex smol_dpo_lin    $S/instruct.safetensors $S/sft_only.safetensors $S/base.safetensors
ex smol_shake      $S/shakespeare.safetensors $S/base.safetensors
ex smol_capybara   $S/capybara.safetensors $S/base.safetensors
ex smol_ctl_lora   $C/lora.safetensors $S/base.safetensors
ex smol_ctl_scale  $C/scale.safetensors $S/base.safetensors
ex qwen_instruct   $Q/instruct.safetensors $Q/base.safetensors
ex qwen_abl        $Q/abliterated.safetensors $Q/instruct.safetensors
ex qwen_coder      $Q/coder.safetensors $Q/base.safetensors
# E: sparse / structured
sp smol_shake      $S/shakespeare.safetensors $S/base.safetensors
sp smol_instruct   $S/instruct.safetensors $S/base.safetensors
sp smol_dpo        $S/instruct.safetensors $S/sft_only.safetensors
sp ctl_rows        $C/rows.safetensors $S/base.safetensors
sp ctl_dense_tiny  $C/dense-tiny.safetensors $S/base.safetensors
sp ctl_lora        $C/lora.safetensors $S/base.safetensors
sp qwen_abl        $Q/abliterated.safetensors $Q/instruct.safetensors
sp qwen_instruct   $Q/instruct.safetensors $Q/base.safetensors
sp pythia_prev     $P/step101000.safetensors $P/step100000.safetensors
echo done > "$EV/experiments.done"
