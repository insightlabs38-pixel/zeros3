#!/usr/bin/env python3
"""B6 LZ4HC stress corpus: pattern-heavy blocks (runs, short periods, copies) compressed by python-lz4's
high_compression levels 3/6/8/9. usage: gen_hc_stress.py OUTDIR  -> hc<level>_<n>_<i>.lz4 (480 blocks)"""
import sys, os, random, lz4.block
random.seed(77); out = sys.argv[1]; os.makedirs(out, exist_ok=True)
def gen(n):
    b = bytearray()
    while len(b) < n:
        k = random.random()
        if k < .25: b += bytes([random.choice([0, 0x20, 0xff])]) * random.randrange(1, 400)
        elif k < .5:
            p = bytes(random.randrange(256) for _ in range(random.randrange(2, 8))); b += p * random.randrange(2, 60)
        elif k < .7: b += bytes(random.randrange(256) for _ in range(random.randrange(1, 80)))
        elif k < .85 and len(b) > 20:
            s = random.randrange(len(b) - 10); b += b[s:s + random.randrange(4, 200)]
        else: b += random.choice([b"alpha", b"zeros3", b"\x00\x00\x00\x01"]) * random.randrange(1, 5)
    return bytes(b[:n])
for i in range(120):
    n = random.choice([700, 4096, 9000, 30000, 70000]); d = gen(n)
    for lv in (3, 6, 8, 9):
        open(f"{out}/hc{lv}_{n}_{i}.lz4", "wb").write(lz4.block.compress(d, store_size=False, mode="high_compression", compression=lv))
