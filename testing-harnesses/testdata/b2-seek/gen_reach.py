"""Reach fixtures for zstd model-window reach: a deterministic 4 MiB payload of 128 KiB segments, fresh text or a lightly
edited copy of a segment 1-4, 8-16 or 24-31 segments back (up to ~4 MiB, beyond the default level-3 window), compressed by
libzstd at several levels and window sizes. Needs python-zstandard; usage: python3 gen_reach.py OUTDIR"""
import os, random, sys, zstandard

r = random.Random(7)
words = [''.join(r.choice('abcdefghijklmnopqrstuvwxyz_') for _ in range(r.randint(2, 11))) for _ in range(1500)]
SEG = 128 << 10
segs = []
while len(segs) < 32:
    n = len(segs)
    if n < 4 or r.random() < 0.33:
        b = bytearray()
        while len(b) < SEG:
            b += (' ' * 4 * r.randint(0, 3) + ' '.join(r.choice(words) for _ in range(r.randint(1, 12))) + ';\n').encode()
        segs.append(bytes(b[:SEG]))
        continue
    back = r.choice([r.randint(1, 4), r.randint(8, 16), r.randint(24, 31)])
    b = bytearray(segs[max(0, n - back)])
    for _ in range(r.randint(3, 40)):
        i = r.randrange(SEG - 20)
        b[i:i + r.randint(1, 12)] = bytes(r.getrandbits(8) for _ in range(r.randint(1, 12)))
    segs.append(bytes(b[:SEG]))
plain = b''.join(segs)
d = sys.argv[1]
for name, lvl, wlog in (('l3', 3, None), ('l9w23', 9, 23), ('l19', 19, None)):
    p = zstandard.ZstdCompressionParameters.from_level(lvl, window_log=wlog or 0, write_content_size=True)
    open(os.path.join(d, f'reach.{name}.zst'), 'wb').write(zstandard.ZstdCompressor(compression_params=p).compress(plain))
