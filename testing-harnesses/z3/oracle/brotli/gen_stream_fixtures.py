#!/usr/bin/env python3
"""Real streaming-producer fixtures for brotliz3. usage: gen_stream_fixtures.py OUTDIR  (python with brotli 1.2.0 and pyarrow 26.0.1)
Each stream is the output of a real library's streaming API (python-brotli 1.2.0 Compressor, Arrow's streaming
compressor, libbrotlienc 1.1.0 through ctypes). MANIFEST.json records producer, parameters, the call pattern and the SHA-256
of the stream and of its decoded bytes; the Go tests decode the stream and require a recipe that regenerates it."""
import hashlib, io, json, os, random, sys
import brotli, pyarrow as pa

out = sys.argv[1]; os.makedirs(out, exist_ok=True)
rnd = random.Random(2024)
words = [b"alpha ", b"bravo ", b"charlie ", b"delta ", b"the ", b"and ", b"<div>", b"</div>\n", b"http://", b"value=", b"information ", b"available "]
def text(n):
    b = bytearray()
    while len(b) < n:
        b += rnd.choice(words)
        if rnd.random() < 0.3: b += b"%04x" % rnd.randrange(65536)
    return bytes(b[:n])
def mixed(n):
    b = bytearray()
    while len(b) < n:
        k = rnd.randrange(3)
        b += text(rnd.choice([100, 3000, 70000])) if k < 2 else bytes(rnd.getrandbits(8) for _ in range(rnd.choice([50, 2000])))
    return bytes(b[:n])

man = []
def save(name, producer, params, pattern, data, comp):
    open(os.path.join(out, name + ".br"), "wb").write(comp)
    man.append(dict(file=name + ".br", producer=producer, params=params, pattern=pattern, size=len(comp), sha256=hashlib.sha256(comp).hexdigest(),
                    decoded_size=len(data), decoded_sha256=hashlib.sha256(data).hexdigest()))

def pyc(q, w, data, chunk, flush_every=0, lgblock=0):
    c = brotli.Compressor(quality=q, lgwin=w, lgblock=lgblock)
    o = b""; i = 0; k = 0
    while i < len(data):
        o += c.process(data[i:i + chunk]); i += chunk; k += 1
        if flush_every and k % flush_every == 0: o += c.flush()
    return o + c.finish()

d = text(300000)
save("py_chunk10k_q8", "python-brotli 1.2.0 Compressor", dict(quality=8, lgwin=22), "process 10000-byte chunks, finish", d, pyc(8, 22, d, 10000))
save("py_flush_q9", "python-brotli 1.2.0 Compressor", dict(quality=9, lgwin=22), "process 7000-byte chunks, flush every 5th, finish", d, pyc(9, 22, d, 7000, 5))
d = text(131072)
save("py_exact2blocks_q8", "python-brotli 1.2.0 Compressor", dict(quality=8, lgwin=22), "process 131072 bytes (two full blocks), finish with no data", d, pyc(8, 22, d, 131072))
d = mixed(1500000)
save("py_chunk32k_q9_big", "python-brotli 1.2.0 Compressor", dict(quality=9, lgwin=22), "process 32768-byte chunks, finish (first block sees a small hint)", d, pyc(9, 22, d, 32768))
d = text(700001)
save("py_oneshot_q8_w18_wrap", "python-brotli 1.2.0 compress()", dict(quality=8, lgwin=18), "compress() of 700001 bytes (ring of 2^19 wraps)", d, brotli.compress(d, quality=8, lgwin=18))
d = mixed(250000)
save("py_lgblock18_q7", "python-brotli 1.2.0 Compressor", dict(quality=7, lgwin=20, lgblock=18), "process 50000-byte chunks, finish, lgblock 18", d, pyc(7, 20, d, 50000, 0, 18))
d = text(400000)
sink = pa.BufferOutputStream(); s = pa.CompressedOutputStream(sink, "brotli")
for i in range(0, len(d), 16384): s.write(d[i:i + 16384])
s.close()
save("arrow_stream_16k", "pyarrow 26.0.1 CompressedOutputStream('brotli')", dict(quality=8, lgwin=22), "write 16384-byte chunks, close", d, sink.getvalue().to_pybytes())
d = text(260000)
sink = pa.BufferOutputStream(); s = pa.CompressedOutputStream(sink, "brotli")
for i in range(0, len(d), 40000):
    s.write(d[i:i + 40000])
    if (i // 40000) % 2 == 1: s.flush()
s.close()
save("arrow_stream_flush", "pyarrow 26.0.1 CompressedOutputStream('brotli')", dict(quality=8, lgwin=22), "write 40000-byte chunks, flush after every second, close", d, sink.getvalue().to_pybytes())
json.dump(man, open(os.path.join(out, "MANIFEST.json"), "w"), indent=1)
print(len(man), "streams", sum(m["size"] for m in man), "bytes")
