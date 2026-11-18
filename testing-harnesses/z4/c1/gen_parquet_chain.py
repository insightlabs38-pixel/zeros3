#!/usr/bin/env python3
"""Pinned real Parquet-Brotli corpus: an 8-revision table history written by pyarrow 26.0.1 (Arrow C++, Brotli level 8,
one-shot BrotliEncoderCompress per page). usage: gen_parquet_chain.py OUTDIR   -> OUTDIR/c0.parquet .. c7.parquet + MANIFEST.json
Revisions: c0 base (400k rows), c1 append 8000, c2 update 200 sparse values, c3 delete the first 3000 rows, c4 append 8000,
c5 compaction rewrite with 100k-row groups, c6 update 400 sparse values, c7 append 8000 and a metadata tag."""
import hashlib, json, os, sys
import numpy as np, pyarrow as pa, pyarrow.parquet as pq

out = sys.argv[1]; os.makedirs(out, exist_ok=True)
WORDS = np.array(["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"])

def table(n, seed, start=0):
    r = np.random.RandomState(seed)
    pay = ["%s-%s-%08x" % (WORDS[a], WORDS[b], c) for a, b, c in zip(r.randint(0, 10, n), r.randint(0, 10, n), r.randint(0, 2**31, n))]
    qty = np.where(r.rand(n) < 0.2, r.randint(0, 5, n), r.randint(0, 100000, n)).astype(np.int32)
    return pa.table({
        "id": pa.array(np.arange(start, start + n, dtype=np.int64)),
        "ts": pa.array(np.arange(start, start + n, dtype=np.int64) * 1000 + 1_700_000_000_000, type=pa.timestamp("ms")),
        "cat": pa.array(["c%02d" % x for x in r.randint(0, 50, n)]),
        "value": pa.array(r.standard_normal(n)),
        "payload": pa.array(pay),
        "flag": pa.array(r.randint(0, 2, n).astype(bool)),
        "qty": pa.array(qty, mask=r.rand(n) < 0.10),
        "score": pa.array(r.standard_normal(n).astype(np.float32), mask=r.rand(n) < 0.15),
        "blob": pa.array([bytes(x) for x in r.randint(0, 256, (n, 24), dtype=np.uint8)], type=pa.binary()),
        "code": pa.array([bytes(x) for x in r.randint(0, 256, (n, 8), dtype=np.uint8)], type=pa.binary(8)),
    })

def update(t, k, seed):
    r = np.random.RandomState(seed)
    idx = np.sort(r.choice(t.num_rows, k, replace=False))
    v = t.column("value").to_numpy().copy(); v[idx] = r.standard_normal(k)
    return t.set_column(t.schema.get_field_index("value"), "value", pa.array(v))

def write(name, t, rg=50000, md=None):
    if md: t = t.replace_schema_metadata(md)
    pq.write_table(t, os.path.join(out, name), row_group_size=rg, compression="brotli", compression_level=8)

t = table(400000, 1); write("c0.parquet", t)
t = pa.concat_tables([t, table(8000, 2, 400000)]); write("c1.parquet", t)
t = update(t, 200, 3); write("c2.parquet", t)
t = t.slice(3000); write("c3.parquet", t)
t = pa.concat_tables([t, table(8000, 4, 408000)]); write("c4.parquet", t)
write("c5.parquet", t, rg=100000)
t = update(t, 400, 5); write("c6.parquet", t, rg=100000)
t = pa.concat_tables([t, table(8000, 6, 416000)]); write("c7.parquet", t, rg=100000, md={"release": "7"})
man = []
for i in range(8):
    p = os.path.join(out, "c%d.parquet" % i); b = open(p, "rb").read()
    man.append(dict(file="c%d.parquet" % i, size=len(b), sha256=hashlib.sha256(b).hexdigest()))
json.dump(dict(producer="pyarrow %s" % pa.__version__, compression="brotli level 8", files=man), open(os.path.join(out, "MANIFEST.json"), "w"), indent=1)
print(sum(m["size"] for m in man), "bytes in 8 files")
