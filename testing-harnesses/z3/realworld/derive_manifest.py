#!/usr/bin/env python3
"""Z3-29: print derived_models.json (name, size, SHA-256, derivation, producer) for the REAL_REEXPORT
artifacts under $Z3WORK/derived. Run: python3 -I derive_manifest.py /home/user/z3cache/derived > derived_models.json"""
import hashlib, json, os, sys
root = sys.argv[1]
PROD = {"gguf-py": "0.19.0", "llama.cpp": "7aca0d2ea8d92b56911233b822db01f9fbb1ae50 (2026-10-26)", "torch": "2.14.1+cpu",
        "transformers": "4.57.6", "safetensors": "0.8.0"}
def sha(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""): h.update(b)
    return h.hexdigest()
out = []
for d, _, fs in sorted(os.walk(root)):
    for f in sorted(fs):
        p = os.path.join(d, f); rel = os.path.relpath(p, root)
        if rel.endswith(".f16.gguf") or "/step" in rel or rel.startswith("py70/main"):
            how = "convert_hf_to_gguf.py (f16) / llama-quantize from the pinned pythia-70m .bin checkpoint of the same step"
            prod = {k: PROD[k] for k in ("llama.cpp", "gguf-py", "torch", "transformers")}
        elif rel.startswith("reexport/") and rel.endswith(".gguf"):
            how = "z3model derive: RewriteGGUF of QuantFactory SmolLM2-135M-Instruct Q4_K_M; tensor bytes copied unchanged"
            prod = {"tool": "testing-harnesses/z3 modelz3.RewriteGGUF"}
        elif rel.startswith("reexport/pythia70m-main"):
            how = "pt_reexport.py: torch.load(weights_only) + torch.save of the pinned Pythia-70M main .bin state_dict"
            prod = {k: PROD[k] for k in ("torch",)}
        elif rel.startswith("reexport/"):
            how = "z3model derive: RewriteST of SmolLM2-135M-Instruct; tensor bytes copied unchanged"
            prod = {"tool": "testing-harnesses/z3 modelz3.RewriteST"}
        else:
            continue
        out.append({"name": rel, "size": os.path.getsize(p), "sha256": sha(p), "class": "REAL_REEXPORT", "derivation": how, "producer": prod})
print(json.dumps({"schema": "z3-derived-models/1", "note": "locally produced from pinned real artifacts; never committed", "artifacts": out}, indent=1))
