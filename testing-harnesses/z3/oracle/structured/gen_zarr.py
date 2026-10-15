"""Zarr v2 family (zarr 2.18.4 + numcodecs): 2048x2048 float32, 128x128 chunks (256 chunk objects, 64 KiB each, uncompressed).
Variants are independent derivations of r0; the revision directory is the object set (chunk files + .zarray/.zattrs)."""
import os, shutil, json
import numpy as np, zarr, numcodecs
from common import Family, rng

S, C = 2048, 128


def make(path, data, chunks=(C, C), comp=None):
    z = zarr.open(path, mode="w", shape=data.shape, chunks=chunks, dtype=data.dtype, compressor=comp, zarr_version=2)
    z[:] = data
    return z


def main():
    fam = Family("zarr", "a2048", {"zarr": zarr.__version__, "numcodecs": numcodecs.__version__, "shape": S, "chunk": C})
    r = rng("zarr-base")
    data = (r.standard_normal((S, S)) * 10).astype(np.float32)
    make(fam.rev("r0") + "/a", data); fam.record("r0", "base", 0)

    def var(name, mutation, logical, fn, comp=None, chunks=(C, C), base="r0"):
        d = data.copy()
        d = fn(d)
        make(fam.rev(name) + "/a", d, chunks=chunks, comp=comp)
        fam.record(name, mutation, logical, base=base)

    def one(d): d[1000, 1000] = 1.25; return d
    var("m01-one-element", "one float32 element changed", 4, one)
    def chunk(d): d[256:384, 512:640] += 1; return d
    var("m02-one-chunk", "one whole chunk changed", C * C * 4, chunk)
    def rows(d): return np.concatenate([d, (r.standard_normal((C, S)) * 10).astype(np.float32)])
    var("m03-append-rows", "128 rows appended (resize + new chunks)", C * S * 4, rows)
    var("m04-rechunk-256", "same contents, chunks 128->256", 0, lambda d: d, chunks=(256, 256))
    var("m05-zstd", "same contents, compressor none->zstd", 0, lambda d: d, comp=numcodecs.Zstd(level=3))
    def attrs(d): return d
    var("m06-metadata-only", "attribute added (.zattrs only)", 16, attrs)
    z = zarr.open(os.path.join(fam.dir, "m06-metadata-only", "a"), mode="r+"); z.attrs["job"] = "z3-18"
    def every(d): return d + np.float32(1e-3)
    var("m07-every-element-tiny", "every element perturbed (all chunks change)", S * S * 4, every)
    var("m08-rewrite-same", "same contents rewritten by a fresh writer", 0, lambda d: d)
    def one_z(d): d[1000, 1000] = 1.25; return d
    var("m09-zstd-one-element-base", "zstd base (stand-in for compressed store)", 0, lambda d: d, comp=numcodecs.Zstd(level=3))
    var("m10-zstd-one-element", "zstd, one element changed (base m09)", 4, one_z, comp=numcodecs.Zstd(level=3), base="m09-zstd-one-element-base")
    fam.close()


main()
