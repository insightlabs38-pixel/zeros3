#!/bin/bash
# Z3-29: REAL_REEXPORT of the pinned EleutherAI/pythia-70m .bin checkpoints (realworld/models.json, group
# pt-pythia70m) as GGUF with the pinned producer. Producer: llama.cpp 7aca0d2ea8d92b56911233b822db01f9fbb1ae50
# (2026-10-26; convert_hf_to_gguf.py + llama-quantize built with -DGGML_NATIVE=OFF), gguf-py 0.19.0,
# torch 2.14.1+cpu, transformers 4.57.6. Inputs: model.bin + the revision's config/tokenizer files.
# Outputs go to $Z3WORK/derived/py70/<step>.{f16,Q4_K_M,Q8_0}.gguf (never committed).
set -e
cd /home/user/z3cache
python3 - <<'P'
import json,os,subprocess,shutil
d=json.load(open('/home/user/zeros3/testing-harnesses/z3/realworld/models.json'))
for s in d['sources']:
    if s['group']!='pt-pythia70m': continue
    sha=s['ref'].split('@')[1]
    dd='py70/'+s['name']; os.makedirs(dd,exist_ok=True)
    for f in ['config.json','tokenizer.json','tokenizer_config.json','special_tokens_map.json','generation_config.json']:
        subprocess.run(['curl','-sSLf','-o',dd+'/'+f,'https://huggingface.co/EleutherAI/pythia-70m/resolve/%s/%s'%(sha,f)])
    if not os.path.exists(dd+'/pytorch_model.bin'): os.symlink('/home/user/z3cache/rw/'+s['sha256'],dd+'/pytorch_model.bin')
P
export PYTHONPATH=/home/user/z3cache/llama.cpp/gguf-py
for st in step1000 step2000 step4000 step8000 main; do
  if [ ! -s py70/$st.f16.gguf ]; then
  ./venv/bin/python llama.cpp/convert_hf_to_gguf.py py70/$st --outtype f16 --outfile py70/$st.f16.gguf 2>&1 | tail -2
  fi
  for q in Q4_K_M Q8_0; do
    [ -s py70/$st.$q.gguf ] || ./llama.cpp/build/bin/llama-quantize py70/$st.f16.gguf py70/$st.$q.gguf $q 2>&1 | tail -1
  done
done
ls -la py70/*.gguf
