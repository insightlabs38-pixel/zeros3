#!/bin/bash
# Family definitions: source this file. D = corpus cache dir (fetch.py output).
D=${D:-/home/user/b5data}
fam_pythia70m="$D/pythia70m/step0.safetensors $D/pythia70m/step1000.safetensors $D/pythia70m/step4000.safetensors $D/pythia70m/step16000.safetensors $D/pythia70m/step64000.safetensors $D/pythia70m/step100000.safetensors $D/pythia70m/step101000.safetensors $D/pythia70m/step102000.safetensors $D/pythia70m/step143000.safetensors $D/pythia70m/dedup_step0.safetensors $D/pythia70m/dedup_step100000.safetensors"
fam_pythia14m=""; for s in 1000 2000 3000 4000 8000 16000 32000 64000 100000 101000 102000 143000; do fam_pythia14m="$fam_pythia14m $D/pythia14m/step$s.safetensors"; done
fam_smollm2="$D/smollm2/base.safetensors $D/smollm2/sft_only.safetensors $D/smollm2/instruct.safetensors $D/smollm2/shakespeare.safetensors $D/smollm2/capybara.safetensors $D/smollm2/bnc_cpt.safetensors $D/smollm2/kd_alpaca.safetensors $D/smollm2/smollm_v1.safetensors"
fam_qwen05="$D/qwen05/base.safetensors $D/qwen05/instruct.safetensors $D/qwen05/coder.safetensors $D/qwen05/abliterated.safetensors $D/qwen05/unsloth_instruct.safetensors"
fam_gpt2="$D/gpt2/base.safetensors $D/gpt2/imdb.safetensors $D/gpt2/imdb_pos.safetensors"
fam_smollm2ctl="$D/smollm2/base.safetensors $D/controls/lora.safetensors $D/controls/rows.safetensors $D/controls/scale.safetensors $D/controls/dense-tiny.safetensors"
