#!/usr/bin/env python3
"""B6 producer matrix input: raw LZ4 blocks of the same source slices from python-lz4 (bundled lz4 library)
in default/fast/high-compression modes. usage: gen_lz4_blocks.py SRC OUTDIR"""
import sys, os, lz4.block
src = open(sys.argv[1], "rb").read(); out = sys.argv[2]; os.makedirs(out, exist_ok=True)
modes = {"pylz4-default": dict(mode="default"), "pylz4-fast3": dict(mode="fast", acceleration=3),
         "pylz4-fast20": dict(mode="fast", acceleration=20), "pylz4-hc3": dict(mode="high_compression", compression=3),
         "pylz4-hc9": dict(mode="high_compression", compression=9), "pylz4-hc12": dict(mode="high_compression", compression=12)}
n = 0
for bs in (4096, 65536):
    for off in range(0, len(src) - bs, bs * 7):
        d = src[off:off + bs]
        for name, kw in modes.items():
            open(f"{out}/{name}_{bs}_{off}.lz4", "wb").write(lz4.block.compress(d, store_size=False, **kw))
        n += 1
        if n >= 80: break
    n = 0
