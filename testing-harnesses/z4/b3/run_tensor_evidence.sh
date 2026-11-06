#!/bin/bash
# Tensor-lane evidence: pinned real SmolLM2-135M-Instruct family and Pythia-14m controlled exports.
# usage: run_tensor_evidence.sh BIN HFDIR WORKDIR   (HFDIR holds smol/, pythia/, pythia_ctrl/)
set -e
BIN=$1; HF=$2; W=$3; D=$(dirname "$0")
M="python3 $D/measure.py"; V="python3 $D/verify_replay.py"
mkdir -p $W; cd $W
S=$HF/smol; P=$HF/pythia; C=$HF/pythia_ctrl
fwd="$S/model.safetensors $S/f16.gguf $S/q8_0.gguf $S/model_fp16.onnx $S/model.onnx"
rev="$S/q8_0.gguf $S/f16.gguf $S/model.onnx $S/model_fp16.onnx $S/model.safetensors"
for mode in identity semantic full; do rm -rf smol_fwd_$mode; $M $BIN ingest -store smol_fwd_$mode -mode $mode $fwd > smol_fwd_$mode.log 2>&1; done
rm -rf smol_rev; $M $BIN ingest -store smol_rev -mode full $rev > smol_rev.log 2>&1
rm -rf smol_rev_opt; cp -r smol_rev smol_rev_opt; $M $BIN optimize -store smol_rev_opt > smol_rev_opt.log 2>&1
$M $BIN cdc $fwd > smol_cdc.log 2>&1
rm -rf smol_out; $V $BIN smol_fwd_full smol_out $fwd > smol_verify_fwd.log 2>&1 || true
rm -rf smol_out_rev; $V $BIN smol_rev_opt smol_out_rev $fwd > smol_verify_rev_opt.log 2>&1 || true
for f in $S/f16.gguf $S/q8_0.gguf $S/model_fp16.onnx $S/model.onnx $S/model.safetensors; do $BIN ranges -store smol_fwd_full -orig $f; done > smol_ranges.log 2>&1
pyf="$P/model.safetensors $P/pytorch_model.bin $C/st_reordered.safetensors $C/st_shard1.safetensors $C/st_shard2.safetensors $C/st_shard3.safetensors $C/torch_contig.pt $C/torch_colmajor.pt $C/gguf_f16.gguf $C/gguf_f32.gguf $C/gguf_q8_0.gguf $C/onnx_f16.onnx $C/onnx_f16_T.onnx $C/st_f32.safetensors $C/st_i16_reinterpret.safetensors"
for mode in identity semantic full; do rm -rf py_$mode; $M $BIN ingest -store py_$mode -mode $mode $pyf > py_$mode.log 2>&1; done
$M $BIN cdc $pyf > py_cdc.log 2>&1
rm -rf py_out; $V $BIN py_full py_out $pyf > py_verify.log 2>&1 || true
$BIN stats -store py_full > py_stats.log 2>&1
$BIN stats -store smol_fwd_full > smol_stats.log 2>&1
for f in $P/model.safetensors $C/gguf_q8_0.gguf $C/onnx_f16_T.onnx $C/torch_colmajor.pt; do $BIN ranges -store py_full -orig $f; done > py_ranges.log 2>&1
echo finished > tensor_evidence.done
