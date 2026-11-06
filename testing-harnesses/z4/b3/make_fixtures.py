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

# ---- container fixtures: tar, tar.gz, git pack (no deltas), arrow, parquet ----
import subprocess, tarfile, io, gzip, shutil, tempfile, pyarrow as pa, pyarrow.parquet as pq
files = {f'src/f{i}.txt': (f'file {i}\n' * (50 + 7 * i)).encode() for i in range(6)}
buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode='w', format=tarfile.USTAR_FORMAT) as t:
    for n, d in files.items():
        ti = tarfile.TarInfo(n); ti.size = len(d); ti.mtime = 0; t.addfile(ti, io.BytesIO(d))
open(f'{out}/tiny.tar', 'wb').write(buf.getvalue())
open(f'{out}/tiny.tar.gz', 'wb').write(gzip.compress(buf.getvalue(), 6, mtime=0))
tmp = tempfile.mkdtemp()
subprocess.run(['git', 'init', '-q', tmp], check=True)
ids = []
for n, d in files.items():
    ids.append(subprocess.run(['git', 'hash-object', '-w', '--stdin'], cwd=tmp, input=d, capture_output=True, check=True).stdout)
pk = subprocess.run(['git', 'pack-objects', '--stdout', '--window=0', '--depth=0'], cwd=tmp, input=b''.join(i for i in ids), capture_output=True, check=True).stdout
open(f'{out}/tiny.pack', 'wb').write(pk)
shutil.rmtree(tmp)
tbl = pa.table({'a': pa.array(list(range(1000)), pa.int64()), 'b': pa.array([i * 0.5 for i in range(1000)], pa.float64()), 's': pa.array([f'v{i % 17}' for i in range(1000)])})
with pa.ipc.new_file(f'{out}/tiny.arrow', tbl.schema) as w:
    for b in tbl.to_batches(max_chunksize=300): w.write_batch(b)
pq.write_table(tbl, f'{out}/tiny_plain.parquet', compression='none', use_dictionary=False, row_group_size=400)
pq.write_table(tbl, f'{out}/tiny_snappy.parquet')
json.dump({'files': {k: v.hex() for k, v in files.items() if False}}, open(f'{out}/files.json', 'w'))
print('containers ok')
