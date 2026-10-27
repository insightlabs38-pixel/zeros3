#!/usr/bin/env python3
"""Z3-29 PyTorch producer/layout re-export (research; REAL_REEXPORT): load a real checkpoint's
state_dict and write it again with the *current* torch (a different producer version from the
file's uploader) and with the key order reversed. Tensor bytes are unchanged.
Run: python3 -I pt_reexport.py <in.bin> <outdir>"""
import hashlib, os, sys, torch
src, out = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)
sd = torch.load(src, map_location="cpu", weights_only=True)
def emit(name, obj):
    p = os.path.join(out, name)
    torch.save(obj, p)
    b = open(p, "rb").read()
    print("%s\t%d\t%s\ttorch %s" % (name, len(b), hashlib.sha256(b).hexdigest(), torch.__version__))
emit("resave.bin", sd)
emit("reversed.bin", {k: sd[k] for k in reversed(list(sd.keys()))})
