#!/usr/bin/env python3
"""Native WIM lookup-table oracle: prints per-WIM resource counts and compressed/original bytes,
and the new-vs-previous resource reuse by SHA-1 (resource-level identity). usage: wimtable.py WIM..."""
import sys, struct, json
def rd(path):
    f = open(path, "rb"); h = f.read(208)
    assert h[:8] == b"MSWIM\0\0\0"
    flags = struct.unpack("<I", h[16:20])[0]
    ts, off = lambda b: (int.from_bytes(b[:7], "little"), b[7]), None
    sz = int.from_bytes(h[48:55], "little"); fl = h[55]
    off = struct.unpack("<Q", h[56:64])[0]; osz = struct.unpack("<Q", h[64:72])[0]
    f.seek(off); tab = f.read(sz)
    if fl & 4: raise SystemExit("compressed lookup table: not supported by this oracle")
    res = {}
    for i in range(0, len(tab) - 49, 50):
        e = tab[i:i+50]
        csz = int.from_bytes(e[:7], "little"); rf = e[7]
        o, osize = struct.unpack("<QQ", e[8:24]); part, refc = struct.unpack("<HI", e[24:30]); sha = e[30:50]
        res[sha] = dict(c=csz, o=osize, flags=rf, refs=refc)
    return flags, res
prev = None; out = []
for p in sys.argv[1:]:
    flags, r = rd(p)
    data = [v for v in r.values() if not v["flags"] & 2]
    row = dict(path=p, resources=len(r), data_res=len(data), comp=sum(v["c"] for v in data), orig=sum(v["o"] for v in data), hdr_flags=hex(flags))
    if prev is not None:
        new = [v for k, v in r.items() if k not in prev and not v["flags"] & 2]
        row.update(new_res=len(new), new_comp=sum(v["c"] for v in new), new_orig=sum(v["o"] for v in new))
    out.append(row); prev = r
for r in out: print(json.dumps(r))
