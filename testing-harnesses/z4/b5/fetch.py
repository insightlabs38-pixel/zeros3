#!/usr/bin/env python3
"""Resolve, pin and download the Z4-B5 real model corpus. usage: fetch.py CACHEDIR [--pins-only]
Writes CACHEDIR/pins.json (repo, commit, file, size, sha256 from the HF LFS oid) and verifies every download."""
import hashlib, json, os, sys, urllib.request, subprocess
H = {'User-Agent': 'zeros3-research'}
def get(u):
    return json.load(urllib.request.urlopen(urllib.request.Request(u, headers=H), timeout=60))
# (family, role, repo, revision, file, local name, relationship)
P70 = 'EleutherAI/pythia-70m'
M = []
def add(*a): M.append(a)
for s in [1000, 4000, 16000, 64000, 100000, 101000, 102000, 143000]:
    add('pythia70m', 'train', P70, f'step{s}', 'model.safetensors', f'pythia70m/step{s}.safetensors', 'training checkpoint (F32)')
add('pythia70m', 'main-f16', P70, 'a39f36b100fe8a5377810d56c3f4789b9c53ac42', 'model.safetensors', 'pythia70m/main_f16.safetensors', 'main branch, F16 release export')
add('pythia70m', 'init', P70, 'step0', 'model.safetensors', 'pythia70m/step0.safetensors', 'initialisation checkpoint')
add('pythia70m', 'sibling', 'EleutherAI/pythia-70m-deduped', 'step0', 'model.safetensors', 'pythia70m/dedup_step0.safetensors', 'deduped-data sibling run, init')
add('pythia70m', 'sibling', 'EleutherAI/pythia-70m-deduped', 'step100000', 'model.safetensors', 'pythia70m/dedup_step100000.safetensors', 'deduped-data sibling run')
for s in [1000, 2000, 3000, 4000, 8000, 16000, 32000, 64000, 100000, 101000, 102000, 143000]:
    add('pythia14m', 'train', 'EleutherAI/pythia-14m', f'step{s}', 'model.safetensors', f'pythia14m/step{s}.safetensors', 'training checkpoint (F16)')
SM = 'HuggingFaceTB/SmolLM2-135M'
add('smollm2', 'base', SM, '93efa2f097d58c2a74874c7e644dbc9b0cee75a2', 'model.safetensors', 'smollm2/base.safetensors', 'base (BF16)')
add('smollm2', 'sft-only', 'HuggingFaceTB/smollm2-135M-SFT-Only', '79528469cd11749bac3e8e9200fd9c192f', 'model.safetensors', 'smollm2/sft_only.safetensors', 'SFT stage of Instruct')
add('smollm2', 'instruct', 'HuggingFaceTB/SmolLM2-135M-Instruct', '12fd25f77366fa6b3b4b768ec3050bf629380bac', 'model.safetensors', 'smollm2/instruct.safetensors', 'SFT + DPO of base (BF16)')
add('smollm2', 'sibling', 'denzelj06/lab7-shakespeare-generator', '7fa7af2b31028b6e097d6dc9e323fe1d0', 'model.safetensors', 'smollm2/shakespeare.safetensors', 'independent SFT (BF16)')
add('smollm2', 'sibling', 'pngwn/SmolLM2-135M-Capybara-SFT', 'main', 'model.safetensors', 'smollm2/capybara.safetensors', 'independent SFT, F32 save')
add('smollm2', 'sibling', 'chao68681/smollm2-135m-bnc-continued-pretraining', 'main', 'model.safetensors', 'smollm2/bnc_cpt.safetensors', 'continued pretraining, F32 save')
add('smollm2', 'sibling', 'yoshitomo-matsubara/smollm2_135m_from_smollm2_1_7b-alpaca-on_policy_kd', 'main', 'model.safetensors', 'smollm2/kd_alpaca.safetensors', 'knowledge-distilled fine-tune, F32 save')
add('smollm2', 'unrelated', 'HuggingFaceTB/SmolLM-135M', '1d461723eec654e65efdc40cf49301c89c0c92f4', 'model.safetensors', 'smollm2/smollm_v1.safetensors', 'negative control: SmolLM v1, same shapes, independent training')
Q = 'Qwen/Qwen2.5-0.5B'
add('qwen05', 'base', Q, '060db6499f32faf8b98477b0a26969ef7d8b9987', 'model.safetensors', 'qwen05/base.safetensors', 'base (BF16)')
add('qwen05', 'instruct', 'Qwen/Qwen2.5-0.5B-Instruct', '7ae557604adf67be50417f59c2c2f167def9a775', 'model.safetensors', 'qwen05/instruct.safetensors', 'SFT+RL of base')
add('qwen05', 'sibling', 'Qwen/Qwen2.5-Coder-0.5B', 'main', 'model.safetensors', 'qwen05/coder.safetensors', 'continued pretraining on code (large divergence)')
add('qwen05', 'sibling', 'huihui-ai/Qwen2.5-0.5B-Instruct-abliterated-v3', 'main', 'model.safetensors', 'qwen05/abliterated.safetensors', 'weight-space edit of Instruct (abliteration)')
add('qwen05', 'repackaged', 'unsloth/Qwen2.5-0.5B-Instruct', 'main', 'model.safetensors', 'qwen05/unsloth_instruct.safetensors', 'third-party re-upload of Instruct')
for n, f, rel in [('gpt2', 'model.safetensors', 'GPT-2 124M base (F32)')]:
    add('gpt2', 'base', 'openai-community/gpt2', 'main', f, 'gpt2/base.safetensors', rel)
# 2021 legacy .bin repos; the HF conversion bot published bit-identical Safetensors on refs/pr/1 (pinned commits)
add('gpt2', 'sft', 'lvwerra/gpt2-imdb', 'f0c412a62b752a83524a061dc68aac27a4031d94', 'model.safetensors', 'gpt2/imdb.safetensors', 'fine-tune on IMDB (HF-bot Safetensors conversion of the legacy .bin)')
add('gpt2', 'rl', 'lvwerra/gpt2-imdb-pos', 'aac16f2d106877f6ef523d002c88f6546739f307', 'model.safetensors', 'gpt2/imdb_pos.safetensors', 'PPO from imdb with positive-sentiment reward (HF-bot conversion)')
GG = os.path.join(os.path.dirname(os.path.abspath(__file__)), '../../z3/realworld/models.json')

def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        while (b := f.read(1 << 22)):
            h.update(b)
    return h.hexdigest()

def main():
    root = sys.argv[1]; pins_only = '--pins-only' in sys.argv
    os.makedirs(root, exist_ok=True); pins = []
    for fam, role, repo, rev, fn, loc, rel in M:
        d = get(f'https://huggingface.co/api/models/{repo}/revision/{rev}?blobs=true')
        commit = d['sha']
        e = next(s for s in d['siblings'] if s['rfilename'] == fn)
        pins.append(dict(family=fam, role=role, repo=repo, revision=rev, commit=commit, file=fn, local=loc, size=e['size'],
                         sha256=(e.get('lfs') or {}).get('sha256') or (e.get('lfs') or {}).get('oid'), relationship=rel))
    gg = json.load(open(GG))['sources']
    want = [('gguf-smollm2', n) for n in ['qf-base-Q8_0', 'qf-inst-Q8_0', 'qf-base-Q5_K_M', 'qf-inst-Q5_K_M', 'qf-base-Q4_K_M', 'qf-inst-Q4_K_M', 'qf-base-Q6_K', 'qf-inst-Q6_K']] + \
           [('gguf-smollm2-bartowski', 'bw-inst-Q8_0'), ('gguf-qwen05', 'qf-base-Q4_K_M'), ('gguf-qwen05', 'qf-inst-Q4_K_M')]
    for g, n in want:
        s = next(x for x in gg if x['group'] == g and x['name'] == n)
        pins.append(dict(family='gguf-' + g.split('-', 1)[1], role=n, repo=s['ref'].split('@')[0], revision=s['ref'].split('@')[1], commit=s['ref'].split('@')[1],
                         file=s['url'].split('/resolve/')[1], url=s['url'], local=f"gguf/{g}__{n}.gguf", size=int(s['size']), sha256=s['sha256'], relationship=s['relationship']))
    json.dump(pins, open(os.path.join(root, 'pins.json'), 'w'), indent=1)
    if pins_only: return
    for p in pins:
        path = os.path.join(root, p['local']); os.makedirs(os.path.dirname(path), exist_ok=True)
        if os.path.exists(path) and os.path.getsize(path) == p['size'] and sha(path) == p['sha256']:
            continue
        url = p.get('url') or f"https://huggingface.co/{p['repo']}/resolve/{p['commit']}/{p['file']}"
        subprocess.run(['curl', '-sSfL', '--retry', '4', '-C', '-', '-o', path, url], check=True)
        got = sha(path)
        if got != p['sha256']:
            sys.exit(f"HASH MISMATCH {p['local']} {got} != {p['sha256']}")
        print('ok', p['local'], p['size'], flush=True)
main()
