#!/usr/bin/env python3
"""Split WOFF2 files into oracle views. usage: woff2split.py CACHE OUT
OUT/<family>/<ver>/{raw,dec,tbl,sfnt}/<file>[.<tag>]. dec = brotli-decoded stream,
tbl = transformed table slices of that stream, sfnt = original sfnt tables (fontTools)."""
import sys, os, io, struct, brotli, json, hashlib
from fontTools.ttLib import TTFont
from fontTools.ttLib.woff2 import woff2KnownTags, unpackBase128
cache, out = sys.argv[1], sys.argv[2]
stats = []; flagged = []
def parse(data):
    assert data[:4] == b"wOF2"
    n = struct.unpack(">H", data[12:14])[0]
    total_comp = struct.unpack(">I", data[20:24])[0]
    pos, tabs = 48, []
    for _ in range(n):
        flags = data[pos]; pos += 1
        ti = flags & 0x3f; xf = flags >> 6
        if ti == 63: tag = data[pos:pos+4].decode("latin1"); pos += 4
        else: tag = woff2KnownTags[ti]
        ol, rest = unpackBase128(data[pos:]); pos = len(data)-len(rest)
        transformed = (xf == 0) if tag in ("glyf", "loca") else (xf != 0)
        tl = ol
        if transformed:
            tl, rest = unpackBase128(data[pos:]); pos = len(data)-len(rest)
        tabs.append((tag, ol, tl, xf))
    return n, tabs, data[pos:pos+total_comp], pos
for pkg in sorted(os.listdir(os.path.join(cache, "npm"))):
    for ver in sorted(os.listdir(os.path.join(cache, "npm", pkg))):
        d = os.path.join(cache, "npm", pkg, ver)
        for f in sorted(os.listdir(d)):
            data = open(os.path.join(d, f), "rb").read()
            n, tabs, comp, hdr = parse(data)
            try: dec = brotli.decompress(comp)
            except brotli.error:  # tolerate streams the CLI decodes fully but flags at the end
                import subprocess
                p = subprocess.run(["brotli", "-d", "-c"], input=comp, capture_output=True)
                dec = p.stdout; flagged.append((pkg, ver, f))
            assert len(dec) == sum(t[2] for t in tabs)
            base = os.path.join(out, pkg, ver)
            for v in ("raw", "dec", "tbl", "sfnt"): os.makedirs(os.path.join(base, v), exist_ok=True)
            open(os.path.join(base, "raw", f), "wb").write(data)
            open(os.path.join(base, "dec", f), "wb").write(dec)
            off = 0
            for tag, ol, tl, xf in tabs:
                open(os.path.join(base, "tbl", f + "." + tag.strip().replace("/","_")), "wb").write(dec[off:off+tl]); off += tl
            try:
                ft = TTFont(os.path.join(d, f))
                for tag in ft.keys():
                    if tag == "GlyphOrder": continue
                    open(os.path.join(base, "sfnt", f + "." + tag.strip().replace("/","_")), "wb").write(ft.reader[tag])
            except Exception as e:
                print("sfnt fail", pkg, ver, f, e, file=sys.stderr)
            stats.append(dict(pkg=pkg, ver=ver, f=f, raw=len(data), comp=len(comp), dec=len(dec), hdr=hdr, tables=[(t[0], t[1], t[2], t[3]) for t in tabs]))
json.dump(stats, open(os.path.join(out, "stats.json"), "w"))
print(len(stats), "files; brotli-flagged:", len(flagged), flagged[:3])
