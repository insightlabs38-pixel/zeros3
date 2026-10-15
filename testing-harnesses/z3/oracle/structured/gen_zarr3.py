"""Zarr v3 family (zarr 3.1.0, separate venv): 2048x2048 float32, SHARDED (512x512 shards, 128x128 inner chunks, 16 shard files),
uncompressed and zstd inner codecs. Variants are independent derivations of r0 (r0 = uncompressed sharded)."""
import os, numpy as np, zarr
from common import Family, rng

S = 2048


def make(path, data, comp=None, shards=(512, 512), chunks=(128, 128)):
    kw = {"compressors": comp} if comp is not None else {"compressors": None}
    z = zarr.create_array(store=path, shape=data.shape, chunks=chunks, shards=shards, dtype=data.dtype, zarr_format=3, overwrite=True, **kw)
    z[:] = data


def main():
    fam = Family("zarr", "a2048v3", {"zarr": zarr.__version__, "format": 3, "shape": S, "shards": 512, "chunk": 128})
    data = (rng("zarr3-base").standard_normal((S, S)) * 10).astype(np.float32)
    make(fam.rev("r0") + "/a", data); fam.record("r0", "base", 0)

    def var(name, mutation, logical, fn, comp=None, base="r0", shards=(512, 512)):
        d = fn(data.copy()); make(fam.rev(name) + "/a", d, comp=comp, shards=shards); fam.record(name, mutation, logical, base=base)

    def one(d): d[1000, 1000] = 1.25; return d
    def chunk(d): d[256:384, 512:640] += 1; return d
    var("m01-sharded-one-element", "one float32 element changed (uncompressed shard)", 4, one)
    var("m02-sharded-one-chunk", "one inner chunk changed (uncompressed shard)", 128 * 128 * 4, chunk)
    zs = zarr.codecs.ZstdCodec(level=3)
    var("m03-zstd-base", "zstd inner codec base", 0, lambda d: d, comp=zs)
    var("m04-zstd-one-element", "zstd shard, one element changed (base m03)", 4, one, comp=zs, base="m03-zstd-base")
    var("m05-zstd-one-chunk", "zstd shard, one inner chunk changed (base m03)", 128 * 128 * 4, chunk, comp=zs, base="m03-zstd-base")
    var("m06-unsharded", "same contents, unsharded 128x128 chunk objects", 0, lambda d: d, shards=None)
    fam.close()


main()
