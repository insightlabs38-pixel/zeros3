#!/usr/bin/env python3
"""Pin and fetch the P7 producer-conformance GGUF set. usage: fetch_quant.py ROOT [--pins-only]
Writes quant_pins.json next to this script (commit + LFS sha256) and verifies every download."""
import hashlib, json, os, subprocess, sys, urllib.request
BW = ('bartowski/SmolLM2-135M-Instruct-GGUF', '09816acd5d99df7be770d85ea30822623dab342c', 'SmolLM2-135M-Instruct-%s.gguf', 'bw',
      ['f16', 'Q4_0', 'Q2_K', 'Q3_K_M', 'Q3_K_L', 'Q4_K_S', 'Q4_K_M', 'Q5_K_S', 'Q5_K_M', 'Q6_K', 'Q8_0', 'IQ4_XS', 'IQ3_M', 'IQ3_XS'])
QF = ('QuantFactory/SmolLM2-135M-Instruct-GGUF', '476854d00ede130660aba430d15f9347ad2e7d0e', 'SmolLM2-135M-Instruct.%s.gguf', 'qf',
      ['Q4_0', 'Q4_1', 'Q5_0', 'Q5_1', 'Q2_K', 'Q3_K_S', 'Q3_K_M', 'Q3_K_L', 'Q4_K_S', 'Q4_K_M', 'Q5_K_S', 'Q5_K_M', 'Q6_K', 'Q8_0'])
def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        while (b := f.read(1 << 22)): h.update(b)
    return h.hexdigest()
root = sys.argv[1]; pins = []
for repo, commit, pat, tag, names in (BW, QF):
    d = json.load(urllib.request.urlopen(urllib.request.Request(f'https://huggingface.co/api/models/{repo}/revision/{commit}?blobs=true', headers={'User-Agent': 'research'})))
    sib = {s['rfilename']: s for s in d['siblings']}
    files = [(pat % n, f'{tag}/{n}.gguf') for n in names]
    if tag == 'bw': files.append(('SmolLM2-135M-Instruct.imatrix', 'bw/imatrix.dat'))
    for fn, loc in files:
        s = sib[fn]
        pins.append(dict(repo=repo, commit=commit, file=fn, local=loc, size=s['size'], sha256=(s.get('lfs') or {}).get('sha256'), blob=s.get('blobId')))
PJ = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'quant_pins.json')
old = {p['local']: p for p in json.load(open(PJ))} if os.path.exists(PJ) else {}
for p in pins:  # non-LFS files have no server-side sha256: keep the hash recorded at first download
    if not p['sha256'] and p['local'] in old: p['sha256'] = old[p['local']]['sha256']
if '--pins-only' in sys.argv: json.dump(pins, open(PJ, 'w'), indent=1); sys.exit(0)
for p in pins:
    path = os.path.join(root, p['local']); os.makedirs(os.path.dirname(path), exist_ok=True)
    if os.path.exists(path) and os.path.getsize(path) == p['size'] and p['sha256'] and sha(path) == p['sha256']: continue
    subprocess.run(['curl', '-sSfL', '--retry', '4', '-C', '-', '-o', path, f"https://huggingface.co/{p['repo']}/resolve/{p['commit']}/{p['file']}"], check=True)
    got = sha(path)
    if p['sha256'] and got != p['sha256']: sys.exit('HASH MISMATCH ' + p['local'])
    p['sha256'] = got
    print('ok', p['local'], p['size'], flush=True)
json.dump(pins, open(PJ, 'w'), indent=1)
