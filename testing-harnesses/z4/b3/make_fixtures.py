#!/usr/bin/env python3
"""Tiny pinned fixtures for the semcas unit tests: one tensor state in four formats plus quantizer vectors."""
import json, os, sys, numpy as np
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
rng = np.random.default_rng(20261106)
w = (rng.standard_normal((64, 96)) * 0.05).astype(np.float16)         # linear weight [out,in]
b = (rng.standard_normal(96) * 0.05).astype(np.float32)
import torch, safetensors.numpy as stn, gguf, onnx
from onnx import helper, TensorProto, numpy_helper
stn.save_file({'w': w, 'b': b}, f'{out}/tiny.safetensors')
torch.save({'w': torch.from_numpy(w.copy()), 'b': torch.from_numpy(b.copy()), 'wt': torch.from_numpy(w.copy()).t()}, f'{out}/tiny.pt')
wr = gguf.GGUFWriter(f'{out}/tiny.gguf', 'llama')
wr.add_tensor('w', w); wr.add_tensor('b', b)
wr.write_header_to_file(); wr.write_kv_data_to_file(); wr.write_tensors_to_file(); wr.close()
init = [numpy_helper.from_array(w.T.copy(), 'w_T'), numpy_helper.from_array(b, 'b')]
g = helper.make_graph([], 'g', [], [], initializer=init)
m = helper.make_model(g); m.ir_version = 8
onnx.save(m, f'{out}/tiny.onnx')
# quantizer oracle vectors: gguf-py reference implementation
x = (rng.standard_normal((4, 64)) * 0.3).astype(np.float32)
x[0, :32] = 0; x[1, 5] = -1.5
q8 = gguf.quants.quantize(x, gguf.GGMLQuantizationType.Q8_0)
q4 = gguf.quants.quantize(x, gguf.GGMLQuantizationType.Q4_0)
json.dump({'x': x.tobytes().hex(), 'q8_0': q8.tobytes().hex(), 'q4_0': q4.tobytes().hex()}, open(f'{out}/quant_vectors.json', 'w'))
print('ok')
