#!/usr/bin/env python3
"""Pinned, hash-verified model downloads for P0 (stdlib only).
usage: fetch_models.py CACHEDIR [set ...]   sets: pythia14m pythia70m smollm2 smol5
Pins come from z4/b5/pins.json (sha256 verified) and z4/b3/hf_pins.txt (SmolLM2 five-format set)."""
import hashlib, json, os, sys, urllib.request
HERE = os.path.dirname(os.path.abspath(__file__))
B5 = os.path.join(HERE, '../../b5/pins.json')
SETS = {
 'pythia14m': ['pythia14m/step1000.safetensors', 'pythia14m/step2000.safetensors', 'pythia14m/step3000.safetensors'],
 'pythia70m': ['pythia70m/step1000.safetensors', 'pythia70m/step4000.safetensors', 'pythia70m/step16000.safetensors'],
 'smollm2': ['smollm2/base.safetensors', 'smollm2/instruct.safetensors', 'smollm2/sft_only.safetensors', 'smollm2/shakespeare.safetensors', 'smollm2/smollm_v1.safetensors'],
}
SMOL5 = [  # B3 pinned SmolLM2 multi-format set: (sha256, local, repo, rev, file)
 ('5af571cbf074e6d21a03528d2330792e532ca608f24ac70a143f6b369968ab8c', 'smol5/model.safetensors', 'HuggingFaceTB/SmolLM2-135M-Instruct', '12fd25f77366fa6b3b4b768ec3050bf629380bac', 'model.safetensors'),
 ('1e790f1d6fac94097a6516ed1f07c29049097271d536d538562cd31ffc011a71', 'smol5/model.onnx', 'HuggingFaceTB/SmolLM2-135M-Instruct', '12fd25f77366fa6b3b4b768ec3050bf629380bac', 'onnx/model.onnx'),
 ('e85e706ce14a3499e1f698c0d638b430df75b5f67131d1516bd0d46b844dfb2e', 'smol5/model_fp16.onnx', 'HuggingFaceTB/SmolLM2-135M-Instruct', '12fd25f77366fa6b3b4b768ec3050bf629380bac', 'onnx/model_fp16.onnx'),
 ('f535f83ec568d040f88ddc04a199fa6da90923bbb41d4dcaed02caa924d6ef57', 'smol5/f16.gguf', 'bartowski/SmolLM2-135M-Instruct-GGUF', '09816acd5d99df7be770d85ea30822623dab342c', 'SmolLM2-135M-Instruct-f16.gguf'),
 ('5a1395716f7913741cc51d98581b9b1228d80987a9f7d3664106742eb06bba83', 'smol5/q8_0.gguf', 'bartowski/SmolLM2-135M-Instruct-GGUF', '09816acd5d99df7be770d85ea30822623dab342c', 'SmolLM2-135M-Instruct-Q8_0.gguf'),
]
def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        while b := f.read(1 << 22):
            h.update(b)
    return h.hexdigest()
def get(url, dest, want):
    if os.path.exists(dest) and sha(dest) == want:
        return
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    req = urllib.request.Request(url, headers={'User-Agent': 'zeros3-p0'})
    with urllib.request.urlopen(req, timeout=120) as r, open(dest + '.part', 'wb') as f:
        while b := r.read(1 << 22):
            f.write(b)
    got = sha(dest + '.part')
    if got != want:
        os.remove(dest + '.part'); sys.exit(f'HASH MISMATCH {dest}: {got} != {want}')
    os.rename(dest + '.part', dest)
def main():
    root = sys.argv[1]; sets = sys.argv[2:] or list(SETS) + ['smol5']
    pins = {p['local']: p for p in json.load(open(B5))}
    out = []
    for s in sets:
        if s == 'smol5':
            for want, local, repo, rev, fn in SMOL5:
                get(f'https://huggingface.co/{repo}/resolve/{rev}/{fn}', os.path.join(root, local), want)
                out.append(dict(local=local, sha256=want, size=os.path.getsize(os.path.join(root, local))))
            continue
        for local in SETS[s]:
            p = pins[local]
            get(f"https://huggingface.co/{p['repo']}/resolve/{p['commit']}/{p['file']}", os.path.join(root, local), p['sha256'])
            out.append(dict(local=local, sha256=p['sha256'], size=p['size'], repo=p['repo'], commit=p['commit']))
    json.dump(out, open(os.path.join(root, 'p0-models-manifest.json'), 'w'), indent=1)
    print(json.dumps(out))
main()
