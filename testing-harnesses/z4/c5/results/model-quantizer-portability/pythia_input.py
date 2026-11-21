import hashlib
import sys
from pathlib import Path
import numpy as np
from safetensors.numpy import load_file
p = Path(sys.argv[1])
expected = "ffa0ce742b321ae28485d73d6471a5e6c96b8aa205000acc4536b4bd185cb1a2"
if hashlib.sha256(p.read_bytes()).hexdigest() != expected:
    raise RuntimeError("unexpected Pythia checkpoint digest")
a = load_file(str(p))
x = a["gpt_neox.embed_in.weight"]
if x.dtype != np.dtype("<f2"):
    raise RuntimeError("expected F16 producer data")
payload = x.flatten()[:768*12].astype("<f4").tobytes()
Path(sys.argv[2]).write_bytes(payload)
print(hashlib.sha256(payload).hexdigest())
