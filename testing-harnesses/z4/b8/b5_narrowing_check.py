#!/usr/bin/env python3 -I
"""B8: how much of an F32 safetensors file survives exact F32->F16 round trip, and what zlib gives on F32 vs F16 planes.
Usage: b5_narrowing_check.py FILE.safetensors"""
import sys, json, struct, zlib, numpy as np
p = sys.argv[1]; b = open(p, "rb").read()
n = struct.unpack("<Q", b[:8])[0]; h = json.loads(b[8:8+n]); base = 8 + n
tot = ex = 0; raw32 = 0; z32 = 0; z16 = 0
for k, v in h.items():
    if k == "__metadata__" or v["dtype"] != "F32": continue
    s, e = v["data_offsets"]; a = np.frombuffer(b[base+s:base+e], dtype="<f4")
    f16 = a.astype("<f2"); back = f16.astype("<f4")
    ok = (back.view(np.uint32) == a.view(np.uint32)) | (np.isnan(a) & np.isnan(back))
    tot += a.size; ex += int(ok.sum()); raw32 += a.nbytes
    z32 += len(zlib.compress(a.tobytes(), 6)); z16 += len(zlib.compress(f16.tobytes(), 6))
print(f"F32 elements {tot}, exactly F16-representable {ex} ({100*ex/tot:.2f}%)")
print(f"raw F32 {raw32}  zlib6(F32) {z32} ({z32/raw32:.3f})  zlib6(F16 narrowed) {z16} ({z16/raw32:.3f} of raw F32)")
