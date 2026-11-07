#!/usr/bin/env python3
"""B6 producer control: Z3-31's pyarrow 8-revision chain rewritten with Parquet LZ4 codecs.
usage: gen_parquet_lz4.py OUT [codec]  (PRODUCER_WORKLOAD; every file written by the named producer)"""
import os, sys
out = sys.argv[1]; codec = sys.argv[2] if len(sys.argv) > 2 else "lz4"
os.environ["Z3_STRUCT_CACHE"] = out
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "z3", "oracle", "parquet"))
import gen_pyarrow as g, pyarrow.parquet as pq
orig = g.write
def write(fam, name, mutation, t, logical, klass="PRODUCER_WORKLOAD", base="r0", rg=g.RG, comp=codec, **kw):
    return orig(fam, name, mutation, t, logical, klass, base, rg, comp, **kw)
g.write = write
g.chain(g.base_table())
