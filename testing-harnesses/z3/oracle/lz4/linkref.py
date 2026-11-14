#!/usr/bin/env python3
"""Linked-block LZ4 frames from python-lz4 (liblz4 1.9.4). usage: linkref.py OUTDIR [INPUT...]
one-shot  = lz4.frame.compress (LZ4F_compressFrame: blocks contiguous, prefix mode)
stream    = LZ4FrameCompressor(auto_flush=True), one compress() call per block (saved-dictionary mode)
Inputs default to deterministic synthetic corpora."""
import sys, os, random, lz4.frame as F

def synth(kind, n, seed):
    r = random.Random(seed)
    if kind == 'text':
        words = [bytes(r.choices(b'abcdefghijklmnopqrstuvwxyz', k=r.randint(2, 9))) for _ in range(600)]
        out = bytearray()
        while len(out) < n:
            out += r.choice(words) + b' '
        return bytes(out[:n])
    if kind == 'records':
        out = bytearray()
        i = 0
        while len(out) < n:
            out += b'{"id":%d,"k":"%s","v":%d}\n' % (i, r.choice([b'alpha', b'beta', b'gamma']), r.randint(0, 99999))
            i += 1
        return bytes(out[:n])
    if kind == 'mixed':  # incompressible spans force stored blocks inside the chain
        out = bytearray()
        while len(out) < n:
            out += synth('text', 90000, r.random()) + r.randbytes(70000)
        return bytes(out[:n])
    raise SystemExit(kind)

def oneshot(d, level, bs):
    return F.compress(d, compression_level=level, block_size=bs, block_linked=True)

def stream(d, level, bs, chunk):
    c = F.LZ4FrameCompressor(compression_level=level, block_size=bs, block_linked=True, auto_flush=True)
    out = [c.begin()]
    for i in range(0, len(d), chunk):
        out.append(c.compress(bytes(d[i:i + chunk])))
    out.append(c.flush())
    return b''.join(out)

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
ins = [(os.path.basename(p), open(p, 'rb').read()) for p in sys.argv[2:]] or \
      [(k + str(n), synth(k, n, 7)) for k, n in (('text', 200000), ('records', 300000), ('mixed', 400000))]
for name, d in ins:
    for level in (0, -4):
        for bs, bn in ((F.BLOCKSIZE_MAX64KB, 65536), (F.BLOCKSIZE_MAX256KB, 262144)):
            open(f'{out}/{name}_l{level}_b{bn}_oneshot.lz4', 'wb').write(oneshot(d, level, bs))
            open(f'{out}/{name}_l{level}_b{bn}_stream.lz4', 'wb').write(stream(d, level, bs, bn))
    open(f'{out}/{name}_l0_b65536_stream40k.lz4', 'wb').write(stream(d, 0, F.BLOCKSIZE_MAX64KB, 40000))
    for level in (3, 4, 6, 9, 10, 12):  # LZ4HC: LZ4_compress_HC_continue on contiguous blocks
        for bs, bn in ((F.BLOCKSIZE_MAX64KB, 65536), (F.BLOCKSIZE_MAX256KB, 262144)):
            open(f'{out}/{name}_hc{level}_b{bn}_oneshot.lz4', 'wb').write(oneshot(d, level, bs))
