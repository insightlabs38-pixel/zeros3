#!/usr/bin/env python3
"""B6 Brotli encoder-reproducibility probe (an ORACLE for the 'verified re-encoding' strategy; no Brotli
encoder exists in the Go laboratory). usage: brotli_probe.py CACHE   (CACHE from fetch_npm.py)
1. real WOFF2 streams: does google/brotli (python-brotli 1.1.x) reproduce the compressed block at q9-11?
2. Arrow/parquet-cpp brotli (level 8, lgwin 22): reproduced by google brotli quality 8?"""
import sys, glob, itertools, os, random, struct
import brotli
sys.argv = ["x", "a", "b"] + sys.argv[1:]
CACHE = sys.argv[3]
src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "woff2split.py")).read().split("for pkg in sorted")[0]
exec(src)
hit = tot = 0
for pat in ["npm/katex/0.16.11/*", "npm/@fortawesome_fontawesome-free/6.7.0/*", "npm/bootstrap-icons/1.11.3/*",
            "npm/material-icons/1.13.14/*", "npm/@fontsource_inter/5.2.5/files__inter-latin-400-normal.woff2",
            "npm/@fontsource_inter/5.2.5/files__inter-cyrillic-700-normal.woff2"]:
    for f in sorted(glob.glob(os.path.join(CACHE, pat)))[:4]:
        d = open(f, "rb").read(); n, tabs, comp, hdr = parse(d); tot += 1
        try: dec = brotli.decompress(comp)
        except brotli.error: print("%-48s strict decoder rejects the stream" % os.path.basename(f)); continue
        found = None
        for q, lg, mode in itertools.product((11, 10, 9), (22, 24, 18, 16, 20, 21, 23), (brotli.MODE_FONT, brotli.MODE_GENERIC)):
            if brotli.compress(dec, quality=q, lgwin=lg, mode=mode) == comp: found = (q, lg, mode); break
        print("%-48s %7d B reproduced by (quality, lgwin, mode) = %s" % (os.path.basename(f), len(comp), found)); hit += found is not None
print("woff2: %d of %d streams reproduced" % (hit, tot))
import pyarrow as pa
random.seed(3)
for n in (1000, 50000, 300000, 1048576):
    out = bytearray()
    while len(out) < n: out += random.choice([b"alpha", b"beta", b"gamma"]) + b"-%08x" % random.randrange(2**31)
    d = bytes(out[:n]); a = pa.Codec("brotli").compress(d, asbytes=True)
    print("arrow brotli(level 8) %8d B -> %7d B; google brotli quality 8 lgwin 22 reproduces: %s" % (n, len(a), brotli.compress(d, quality=8, lgwin=22) == a))
