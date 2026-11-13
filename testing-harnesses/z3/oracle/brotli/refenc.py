#!/usr/bin/env python3
"""Reference oracle for brotliz3 tests. usage: refenc.py QUALITY LGWIN < input > compressed
lgwin 22: Arrow's one-shot BrotliEncoderCompress (pyarrow 26, the Parquet producer of this study).
other windows: the `brotli` PyPI package 1.2.0, which drives the streaming API (process + finish);
it differs from the one-shot call only when the input ends exactly on an input-block boundary and
a meta-block was flushed there, so the tests skip block-multiple sizes for those windows."""
import sys
q, w = int(sys.argv[1]), int(sys.argv[2]); d = sys.stdin.buffer.read()
if w == 22:
    import pyarrow as pa
    sys.stdout.buffer.write(pa.Codec('brotli', compression_level=q).compress(d, asbytes=True))
else:
    import brotli
    sys.stdout.buffer.write(brotli.compress(d, quality=q, lgwin=w))
