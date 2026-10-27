#!/usr/bin/env python3
"""Z3-29: pin Hugging Face model artifacts (revision commit, size, LFS SHA-256, licence) into models.json.

Run: python3 -I pin_models.py > models.json   (network; reads only public metadata; downloads nothing)
Entry = (group, format, name, repo, file, relationship)."""
import json, sys, urllib.request, urllib.parse

def get(u):
    return json.load(urllib.request.urlopen(urllib.request.Request(u, headers={"User-Agent": "z3-pin"}), timeout=60))

E = []
def add(group, fmt, name, repo, file, rel, rev="main"): E.append((group, fmt, name, repo, file, rel, rev))

# Safetensors: SmolLM2-135M star (BF16) + Qwen2.5-0.5B base -> Instruct (BF16) + unrelated SmolLM-135M (F32)
for n, r in [("base", "HuggingFaceTB/SmolLM2-135M"), ("instruct", "HuggingFaceTB/SmolLM2-135M-Instruct"),
             ("sft-only", "HuggingFaceTB/smollm2-135M-SFT-Only"), ("shakespeare", "denzelj06/lab7-shakespeare-generator")]:
    add("st-smollm2-star", "safetensors", n, r, "model.safetensors", "SmolLM2-135M base and fine-tunes (BF16)")
add("st-qwen05", "safetensors", "base", "Qwen/Qwen2.5-0.5B", "model.safetensors", "Qwen2.5-0.5B base (BF16)")
add("st-qwen05", "safetensors", "instruct", "Qwen/Qwen2.5-0.5B-Instruct", "model.safetensors", "Qwen2.5-0.5B-Instruct (SFT/RL of the base)")
add("st-unrelated", "safetensors", "smollm-v1", "HuggingFaceTB/SmolLM-135M", "model.safetensors", "SmolLM v1 135M F32 (same shapes as SmolLM2, independent training)")

# GGUF: same producer (QuantFactory), base vs instruct, several quantisations
for q in ["Q4_K_M", "Q5_K_M", "Q6_K", "Q8_0"]:
    add("gguf-smollm2", "gguf", "qf-base-" + q, "QuantFactory/SmolLM2-135M-GGUF", "SmolLM2-135M.%s.gguf" % q, "QuantFactory base")
    add("gguf-smollm2", "gguf", "qf-inst-" + q, "QuantFactory/SmolLM2-135M-Instruct-GGUF", "SmolLM2-135M-Instruct.%s.gguf" % q, "QuantFactory instruct")
for q in ["Q4_K_M", "Q8_0"]:
    add("gguf-smollm2-bartowski", "gguf", "bw-inst-" + q, "bartowski/SmolLM2-135M-Instruct-GGUF", "SmolLM2-135M-Instruct-%s.gguf" % q, "bartowski imatrix instruct")
    add("gguf-smollm-v1", "gguf", "qf-v1-" + q, "QuantFactory/SmolLM-135M-GGUF", "SmolLM-135M.%s.gguf" % q, "unrelated same-architecture control")
add("gguf-qwen05", "gguf", "qf-base-Q4_K_M", "QuantFactory/Qwen2.5-0.5B-GGUF", "Qwen2.5-0.5B.Q4_K_M.gguf", "QuantFactory base")
add("gguf-qwen05", "gguf", "qf-inst-Q4_K_M", "QuantFactory/Qwen2.5-0.5B-Instruct-GGUF", "Qwen2.5-0.5B-Instruct.Q4_K_M.gguf", "QuantFactory instruct")
add("gguf-qwen05", "gguf", "dh-inst-Q4_K_M", "DhruvalLabs/Qwen2.5-0.5B-Instruct-GGUF", "Qwen2.5-0.5B-Instruct-Q4_K_M.gguf", "other producer, same instruct model")
add("gguf-qwen05", "gguf", "qwen-inst-q4_k_m", "Qwen/Qwen2.5-0.5B-Instruct-GGUF", "qwen2.5-0.5b-instruct-q4_k_m.gguf", "official Qwen")

# K-quant-dominant pair (hidden size 1024 = 4 x 256): same producer (mradermacher), Qwen3-0.6B-Base vs post-trained Qwen3-0.6B
add("gguf-qwen3", "gguf", "mr-base-Q4_K_M", "mradermacher/Qwen3-0.6B-Base-GGUF", "Qwen3-0.6B-Base.Q4_K_M.gguf", "mradermacher base")
add("gguf-qwen3", "gguf", "mr-post-Q4_K_M", "mradermacher/Qwen3-0.6B-GGUF", "Qwen3-0.6B.Q4_K_M.gguf", "mradermacher post-trained (SFT/RL of the base)")

# PyTorch ZIP checkpoints (pytorch_model.bin written by torch.save) and the same Pythia-70M states as Safetensors
for st in ["step1000", "step2000", "step4000", "step8000", "main"]:
    add("pt-pythia70m", "pytorch-zip", st, "EleutherAI/pythia-70m", "pytorch_model.bin", "Pythia-70M training checkpoint .bin (F32 in-repo, fp16 storage?)", st)
for st in ["main"]:
    add("pt-pythia70m-st", "safetensors", st, "EleutherAI/pythia-70m", "model.safetensors", "same Pythia-70M state as Safetensors", st)
for st in ["step1000", "step2000", "step3000", "step4000", "main"]:
    add("pt-pythia14m", "pytorch-zip", st, "EleutherAI/pythia-14m", "pytorch_model.bin", "Pythia-14M training checkpoint .bin", st)
add("pt-bert-tiny", "pytorch-zip", "base", "prajjwal1/bert-tiny", "pytorch_model.bin", "bert-tiny base")
add("pt-bert-tiny", "pytorch-zip", "ft-conll03", "tomaarsen/span-marker-bert-tiny-conll03", "pytorch_model.bin", "fine-tune of bert-tiny (NER)")
add("pt-bert-tiny", "pytorch-zip", "ft-fewnerd", "tomaarsen/span-marker-bert-tiny-fewnerd-coarse-super", "pytorch_model.bin", "fine-tune of bert-tiny (NER, sibling)")
add("pt-unrelated", "pytorch-zip", "pythia70m-deduped", "EleutherAI/pythia-70m-deduped", "pytorch_model.bin", "sibling training run (deduped data), same shapes")

out = []
info = {}
for group, fmt, name, repo, file, rel, rev in E:
    if (repo, rev) not in info:
        m = get("https://huggingface.co/api/models/%s/revision/%s" % (repo, rev))
        info[(repo, rev)] = (m["sha"], (m.get("cardData") or {}).get("license") or (get("https://huggingface.co/api/models/" + repo).get("cardData") or {}).get("license") or next((t[8:] for t in m.get("tags", []) if t.startswith("license:")), "unknown"))
        info[(repo, rev)] += ({f["path"]: f for f in get("https://huggingface.co/api/models/%s/tree/%s" % (repo, info[(repo, rev)][0]))},)
    sha, lic, tree = info[(repo, rev)]
    if isinstance(lic, list): lic = ",".join(lic)
    f = tree[file]
    out.append({"group": group, "format": fmt, "name": name,
                "url": "https://huggingface.co/%s/resolve/%s/%s" % (repo, sha, urllib.parse.quote(file)),
                "sha256": f["lfs"]["oid"], "size": f["lfs"]["size"],
                "ref": "%s@%s" % (repo, sha), "license": lic, "relationship": rel})
print(json.dumps({"schema": "z3-realworld-models/1", "licenses": {}, "note": "Z3-29 model artifacts; HF LFS SHA-256; never committed.", "sources": out}, indent=1))
