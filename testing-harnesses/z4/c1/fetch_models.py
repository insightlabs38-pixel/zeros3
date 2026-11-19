#!/usr/bin/env python3
"""Fetch the pinned real-model files used by the C1 qualification into DIR (hash-verified, resumable).
usage: fetch_models.py DIR     ->  DIR/pythia14m/stepN.safetensors, DIR/smol/{f16,Q4_0,IQ3_M,IQ3_XS}.gguf, DIR/smol/imatrix.dat"""
import hashlib, json, os, subprocess, sys
root = sys.argv[1]
here = os.path.dirname(os.path.abspath(__file__))
b5 = json.load(open(os.path.join(here, "../b5/pins.json")))
p7 = {p["local"]: p for p in json.load(open(os.path.join(here, "../p7/quant_pins.json")))}
want = [(p["local"], p, f"https://huggingface.co/{p['repo']}/resolve/{p['commit']}/{p['file']}") for p in b5 if p["family"] == "pythia14m"]
for src, dst in [("bw/f16.gguf", "smol/f16.gguf"), ("bw/Q4_0.gguf", "smol/Q4_0.gguf"), ("bw/IQ3_M.gguf", "smol/IQ3_M.gguf"),
                 ("bw/IQ3_XS.gguf", "smol/IQ3_XS.gguf"), ("bw/imatrix.dat", "smol/imatrix.dat")]:
    p = p7[src]
    want.append((dst, p, f"https://huggingface.co/{p['repo']}/resolve/{p['commit']}/{p['file']}"))
for local, p, url in want:
    path = os.path.join(root, local)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    def ok():
        return os.path.exists(path) and os.path.getsize(path) == p["size"] and hashlib.sha256(open(path, "rb").read()).hexdigest() == p["sha256"]
    if not ok():
        subprocess.run(["curl", "-sSfL", "--retry", "4", "-C", "-", "-o", path, url], check=True)
        if not ok():
            sys.exit("HASH MISMATCH " + local)
    print("ok", local, flush=True)
