"""Seekable codec replay fixtures: one deterministic 384 KiB text-and-binary payload compressed by zlib (gzip 6 and 9,
mtime 0) and libzstd (levels 3 and 19, content size). Needs python-zstandard; usage: python3 gen.py OUTDIR"""
import gzip, os, random, sys, zstandard

r = random.Random(2026)
words = [''.join(r.choice('abcdefghijklmnopqrstuvwxyz_') for _ in range(r.randint(2, 11))) for _ in range(900)]
out = bytearray()
while len(out) < 384 << 10:
    k = r.random()
    if k < 0.80:  # source-like lines
        out += (' ' * 4 * r.randint(0, 3) + ' '.join(r.choice(words) for _ in range(r.randint(1, 12))) + ';\n').encode()
    elif k < 0.995 and len(out) > 70000:  # a repeat, near or beyond the DEFLATE window
        a = r.randint(len(out) - 30000, len(out) - 6000) if k < 0.9 else r.randint(0, len(out) - 60000)
        out += out[a:a + r.randint(200, 5000)]
    else:  # incompressible run: zlib stores it
        out += bytes(r.getrandbits(8) for _ in range(r.randint(2000, 12000)))
plain = bytes(out[:384 << 10])
d = sys.argv[1]
for lvl in (6, 9):
    with gzip.GzipFile(os.path.join(d, f'text.l{lvl}.gz'), 'wb', compresslevel=lvl, mtime=0) as f:
        f.write(plain)
for lvl in (3, 19):
    open(os.path.join(d, f'text.l{lvl}.zst'), 'wb').write(zstandard.ZstdCompressor(level=lvl, write_content_size=True).compress(plain))
