#!/usr/bin/env python3
"""Streaming reference for brotliz3: drives libbrotlienc's BrotliEncoderCompressStream directly (ctypes).
usage: streamref.py LIB QUALITY LGWIN LGBLOCK SIZEHINT OPS < input > compressed
OPS (or @env: read from $STREAMOPS) is a comma list: P<n> = PROCESS with the next n input bytes, F = FLUSH, Z = FINISH (with the rest of the input).
LIB is a libbrotlienc shared object (the system 1.1.0 or one built from brotli 1.2.0 sources).
SIZEHINT 0 leaves the parameter unset (the encoder derives it from the first block, like python-brotli and Arrow)."""
import ctypes, sys
lib = ctypes.CDLL(sys.argv[1])
q, w, lb, hint, ops = int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]), int(sys.argv[5]), (__import__('os').environ['STREAMOPS'] if sys.argv[6] == '@env' else sys.argv[6]).split(',')
data = sys.stdin.buffer.read()
lib.BrotliEncoderCreateInstance.restype = ctypes.c_void_p
lib.BrotliEncoderCreateInstance.argtypes = [ctypes.c_void_p] * 3
lib.BrotliEncoderSetParameter.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_uint32]
lib.BrotliEncoderHasMoreOutput.argtypes = [ctypes.c_void_p]
lib.BrotliEncoderCompressStream.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.POINTER(ctypes.c_size_t), ctypes.POINTER(ctypes.c_char_p),
                                            ctypes.POINTER(ctypes.c_size_t), ctypes.POINTER(ctypes.c_void_p), ctypes.POINTER(ctypes.c_size_t)]
s = lib.BrotliEncoderCreateInstance(None, None, None)
for p, v in ((1, q), (2, w), (3, lb), (5, hint)):
    if p == 5 and v == 0 or p == 3 and v == 0:
        continue
    assert lib.BrotliEncoderSetParameter(s, p, v)
out = bytearray()
pos = 0
def call(op, chunk):
    global out
    buf = ctypes.create_string_buffer(chunk, len(chunk)) if chunk else ctypes.create_string_buffer(1)
    avail_in = ctypes.c_size_t(len(chunk))
    nxt = ctypes.c_char_p(ctypes.addressof(buf))
    obuf = ctypes.create_string_buffer(1 << 20)
    while True:
        avail_out = ctypes.c_size_t(len(obuf)); nout = ctypes.c_void_p(ctypes.addressof(obuf)); tot = ctypes.c_size_t(0)
        assert lib.BrotliEncoderCompressStream(s, op, ctypes.byref(avail_in), ctypes.byref(nxt), ctypes.byref(avail_out), ctypes.byref(nout), ctypes.byref(tot))
        out += obuf.raw[:len(obuf) - avail_out.value]
        if avail_in.value == 0 and not lib.BrotliEncoderHasMoreOutput(s):
            break
for o in ops:
    if o[0] == 'P':
        n = int(o[1:]); call(0, data[pos:pos + n]); pos += n
    elif o == 'F':
        call(1, b'')
    elif o == 'Z':
        call(2, data[pos:]); pos = len(data)
assert pos == len(data), "ops do not consume the input"
sys.stdout.buffer.write(out)
