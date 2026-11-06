#!/usr/bin/env python3
"""Controlled exports of one pinned tensor state (EleutherAI/pythia-14m @cf967c0, F16) in many layouts.
Producers: safetensors, torch.save, gguf-py, onnx.helper. Trusted pinned input only (weights_only load).
usage: make_pythia_exports.py HF_DIR OUT_DIR"""
import sys, os, numpy as np, torch, gguf, onnx
from onnx import helper, numpy_helper
from safetensors.numpy import load_file, save_file
src, out = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)
sd = load_file(f'{src}/model.safetensors')                      # name -> float16 numpy
names = sorted(sd)
def two_d(n): return sd[n].ndim == 2
save_file({n: sd[n] for n in reversed(names)}, f'{out}/st_reordered.safetensors')
# three shards of roughly equal bytes (HF-style), different header contents
sizes = [sd[n].nbytes for n in names]; target = sum(sizes) / 3; shards = [[]]; acc = 0
for n in names:
    if acc >= target and len(shards) < 3: shards.append([]); acc = 0
    shards[-1].append(n); acc += sd[n].nbytes
for i, sh in enumerate(shards):
    save_file({n: sd[n] for n in sh}, f'{out}/st_shard{i+1}.safetensors', metadata={'shard': str(i + 1)})
save_file({n: sd[n].astype(np.float32) for n in names}, f'{out}/st_f32.safetensors')
save_file({n: sd[n].view(np.int16) for n in names}, f'{out}/st_i16_reinterpret.safetensors')   # same bytes, different type
tens = {n: torch.from_numpy(sd[n].copy()) for n in names}
torch.save(tens, f'{out}/torch_contig.pt')
torch.save({n: (t.t().contiguous().t() if t.ndim == 2 else t) for n, t in tens.items()}, f'{out}/torch_colmajor.pt')   # transposed storage, same logical tensors
def gguf_write(path, conv, quant=None):
    w = gguf.GGUFWriter(path, 'gptneox')
    for n in names:
        a = conv(sd[n])
        if quant is not None and a.ndim == 2 and a.shape[-1] % 32 == 0:
            q = gguf.quants.quantize(a.astype(np.float32), quant)
            w.add_tensor(n, q, raw_dtype=quant)
        else:
            w.add_tensor(n, a)
    w.write_header_to_file(); w.write_kv_data_to_file(); w.write_tensors_to_file(); w.close()
gguf_write(f'{out}/gguf_f16.gguf', lambda a: a)
gguf_write(f'{out}/gguf_f32.gguf', lambda a: a.astype(np.float32))
gguf_write(f'{out}/gguf_q8_0.gguf', lambda a: a, gguf.GGMLQuantizationType.Q8_0)
def onnx_write(path, tf):
    init = [numpy_helper.from_array(tf(n, sd[n]), n.replace('.', '_')) for n in names]
    m = helper.make_model(helper.make_graph([], 'g', [], [], initializer=init)); m.ir_version = 8
    onnx.save(m, path)
onnx_write(f'{out}/onnx_f16.onnx', lambda n, a: a)
onnx_write(f'{out}/onnx_f16_T.onnx', lambda n, a: np.ascontiguousarray(a.T) if a.ndim == 2 else a)   # MatMul-style transposed weights
print('ok', len(names), 'tensors', sum(sizes))
