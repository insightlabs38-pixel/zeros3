"""Z3-31 small committed fixtures (REAL producer bytes, a few KiB each) for the parquetz3 tests. Regenerate with
the pinned venv (setup.sh): python gen_fixtures.py ../../parquetz3/testdata"""
import os, sys
import numpy as np, pyarrow as pa, pyarrow.parquet as pq, duckdb

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
n = 3000
r = np.random.RandomState(31)
words = np.array(["alpha", "bravo", "charlie", "delta", "echo"])
t = pa.table({
    "id": pa.array(np.arange(n, dtype=np.int64)),
    "cat": pa.array(["c%02d" % x for x in r.randint(0, 20, n)]),
    "value": pa.array(r.standard_normal(n)),
    "payload": pa.array(["%s-%08x" % (words[a], c) for a, c in zip(r.randint(0, 5, n), r.randint(0, 2**31, n))]),
    "flag": pa.array(r.randint(0, 2, n).astype(bool)),
    "qty": pa.array(r.randint(0, 1000, n).astype(np.int32), mask=r.rand(n) < 0.1),
    "score": pa.array(r.standard_normal(n).astype(np.float32), mask=r.rand(n) < 0.15),
    "code": pa.array([bytes(x) for x in r.randint(0, 256, (n, 8), dtype=np.uint8)], type=pa.binary(8)),
})
def w(name, **kw):
    pq.write_table(t, os.path.join(out, name), row_group_size=1250, **kw)
w("pa_snappy_v1.parquet", compression="snappy", data_page_size=8 << 10)
w("pa_snappy_v2.parquet", compression="snappy", data_page_version="2.0", data_page_size=8 << 10)
w("pa_zstd_v1.parquet", compression="zstd", data_page_size=8 << 10)
w("pa_none_plain.parquet", compression="none", use_dictionary=False)
w("pa_bss.parquet", compression="snappy", use_dictionary=False, use_byte_stream_split=["value", "score"])
pq.write_table(t, os.path.join(out, "pa_rg500.parquet"), row_group_size=500, compression="snappy")
pq.write_table(t.slice(0, 1500), os.path.join(out, "pa_gzip.parquet"), compression="gzip")
pq.write_table(t.slice(0, 1500).select(["id", "qty", "payload"]), os.path.join(out, "pa_delta.parquet"), compression="snappy", use_dictionary=False,
               column_encoding={"id": "DELTA_BINARY_PACKED", "qty": "DELTA_BINARY_PACKED", "payload": "DELTA_LENGTH_BYTE_ARRAY"})
nt = pa.table({"id": pa.array(np.arange(500, dtype=np.int64)), "tags": pa.array([[int(i), int(i) + 1] for i in range(500)], type=pa.list_(pa.int32()))})
pq.write_table(nt, os.path.join(out, "pa_nested.parquet"), compression="snappy")
con = duckdb.connect()
con.execute("SET threads=1")
con.register("t", t)
for name, opt in [("dd_snappy.parquet", "COMPRESSION snappy"), ("dd_zstd.parquet", "COMPRESSION zstd"), ("dd_none.parquet", "COMPRESSION uncompressed")]:
    con.execute("COPY (SELECT * FROM t) TO '%s' (FORMAT PARQUET, %s, ROW_GROUP_SIZE 1250)" % (os.path.join(out, name), opt))
