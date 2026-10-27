"""Z3-31 Parquet producers (pyarrow 18.1.0, DuckDB 2.0.0): the Z3-18 table (400k rows, 8 row groups, Snappy; its
six columns have the same contents and seeds as gen_parquet.py's) extended with the columns Parquet encodes
differently: nullable INT32 `qty`, nullable FLOAT `score`, high-entropy BINARY `blob` and a
FIXED_LEN_BYTE_ARRAY(8) `code`. Output under $Z3_STRUCT_CACHE (default ~/.cache/z3-parquet):

  parquet/pyarrow-variants/<rev>/db.parquet   PRODUCER_WORKLOAD: the 12 Z3-18 variants + layout/codec/page/null/dictionary controls
  parquet/duckdb-variants/<rev>/db.parquet    REAL_REEXPORT of the same logical table by DuckDB (+ DuckDB-only options)
  parquet/pyarrow-chain/c0..c7/db.parquet     a coherent 8-revision table history (each revision a full rewrite)
  parquet/controls/<name>/db.parquet          unsupported-feature / nested / gzip controls

Every file is a complete Parquet file written by the named producer; nothing is hand-assembled."""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "structured"))
os.environ.setdefault("Z3_STRUCT_CACHE", os.path.join(os.path.expanduser("~"), ".cache", "z3-parquet"))
import numpy as np, pyarrow as pa, pyarrow.parquet as pq, duckdb
from common import Family, rng

N = 400000
RG = 50000
WORDS = np.array(["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"])


def table6(r, n=N):  # exactly Z3-18's table()
    pay = ["%s-%s-%08x" % (WORDS[a], WORDS[b], c) for a, b, c in zip(r.randint(0, 10, n), r.randint(0, 10, n), r.randint(0, 2**31, n))]
    return {
        "id": pa.array(np.arange(n, dtype=np.int64)),
        "ts": pa.array(np.arange(n, dtype=np.int64) * 1000 + 1_700_000_000_000, type=pa.timestamp("ms")),
        "cat": pa.array(["c%02d" % x for x in r.randint(0, 50, n)]),
        "value": pa.array(r.standard_normal(n)),
        "payload": pa.array(pay),
        "flag": pa.array(r.randint(0, 2, n).astype(bool)),
    }


def extras(r, n):
    qty = np.where(r.rand(n) < 0.2, r.randint(0, 5, n), r.randint(0, 100000, n)).astype(np.int32)
    score = r.standard_normal(n).astype(np.float32)
    blob = r.randint(0, 256, (n, 24), dtype=np.uint8)
    code = r.randint(0, 256, (n, 8), dtype=np.uint8)
    return {
        "qty": pa.array(qty, mask=r.rand(n) < 0.10),
        "score": pa.array(score, mask=r.rand(n) < 0.15),
        "blob": pa.array([bytes(x) for x in blob], type=pa.binary()),
        "code": pa.array([bytes(x) for x in code], type=pa.binary(8)),
    }


def base_table(n=N, seed="base"):
    t = table6(rng("pq-" + seed), n)
    t.update(extras(rng("pq-x-" + seed), n))
    return pa.table(t)


def with_col(t, name, arr):
    return t.set_column(t.schema.get_field_index(name), name, arr)


def write(fam, name, mutation, t, logical, klass="PRODUCER_WORKLOAD", base="r0", rg=RG, comp="snappy", **kw):
    d = fam.rev(name)
    pq.write_table(t, os.path.join(d, "db.parquet"), row_group_size=rg, compression=comp, **kw)
    fam.record(name, mutation, logical, "class=" + klass, base)


def duck(fam, name, mutation, t, logical, base="r0", klass="REAL_REEXPORT", **opt):
    d = fam.rev(name)
    p = os.path.join(d, "db.parquet")
    con = duckdb.connect()
    con.execute("SET threads=1")
    con.register("t", t)
    o = {"FORMAT": "PARQUET", "COMPRESSION": "snappy", "ROW_GROUP_SIZE": RG}
    o.update(opt)
    con.execute("COPY (SELECT * FROM t) TO '%s' (%s)" % (p, ", ".join("%s %s" % kv for kv in o.items())))
    fam.record(name, mutation, logical, "class=" + klass, base)


def variants():
    fam = Family("parquet", "pyarrow-variants", {"pyarrow": pa.__version__, "rows": N, "row_group": RG, "compression": "snappy"})
    t = base_table()
    r = rng("pq-mut")
    write(fam, "r0", "base", t, 0)
    v = t["value"].to_numpy().copy(); v[123456] = 3.25
    write(fam, "m01-update-one-value", "one float value changed", with_col(t, "value", pa.array(v)), 8)
    v = t["value"].to_numpy().copy(); v[r.randint(0, N, 100)] = 1.5
    write(fam, "m02-update-100-sparse", "100 sparse floats changed", with_col(t, "value", pa.array(v)), 800)
    extra = table6(rng("pq-app"), 5000)
    extra["id"] = pa.array(np.arange(N, N + 5000, dtype=np.int64))
    extra.update(extras(rng("pq-x-app"), 5000))
    write(fam, "m03-append-5k", "append 5000 rows (new partial row group)", pa.concat_tables([t, pa.table(extra)]), 5000 * 50)
    keep = np.ones(N, dtype=bool); keep[200000:204000] = False
    write(fam, "m04-delete-4k-middle", "delete 4000 rows mid-file (later rows shift)", t.filter(pa.array(keep)), 0)
    write(fam, "m05-metadata-only", "key/value metadata added (footer only)", t.replace_schema_metadata({"job": "z3-31"}), 8)
    write(fam, "m06-row-group-100k", "same contents, row_group_size 50k->100k", t, 0, rg=100000)
    write(fam, "m07-zstd", "same contents, snappy->zstd", t, 0, comp="zstd")
    write(fam, "m08-rewrite-same-options", "same contents rewritten by a fresh writer", t, 0)
    s = t["value"].to_numpy() * 1.01
    write(fam, "m09-one-column-recomputed", "one whole column recomputed", with_col(t, "value", pa.array(s)), N * 8)
    idx = r.permutation(N)
    write(fam, "m10-reordered-rows", "rows permuted (same set)", t.take(pa.array(idx)), 0)
    v = t["value"].to_numpy().copy(); v[r.randint(0, N, 4000)] = 2.5
    write(fam, "m11-tiny-updates-4000", "4000 scattered floats changed", with_col(t, "value", pa.array(v)), 4000 * 8)
    write(fam, "m12-no-dictionary-v2", "same contents, dictionary off + data page v2", t, 0, use_dictionary=False, data_page_version="2.0")
    # --- Z3-31 additions ---
    write(fam, "m13-uncompressed", "same contents, no compression", t, 0, comp="none")
    write(fam, "m14-no-dictionary-v1", "same contents, dictionary off, data page v1", t, 0, use_dictionary=False)
    write(fam, "m15-dictionary-v2", "same contents, dictionary on, data page v2", t, 0, data_page_version="2.0")
    write(fam, "m16-page-64k", "same contents, data_page_size 64 KiB", t, 0, data_page_size=64 << 10)
    sc = t["score"].to_pylist()
    nz = [x for x in sc if x is not None]
    r2 = rng("pq-nulls")
    pos = np.sort(r2.choice(N, len(nz), replace=False))
    vals = np.zeros(N, dtype=np.float32); vals[pos] = np.array(nz, dtype=np.float32)
    mask = np.ones(N, dtype=bool); mask[pos] = False
    write(fam, "m17-nulls-moved", "score: same non-null values in order, null positions changed", with_col(t, "score", pa.array(vals, mask=mask)), 0)
    labels = ["c%02d" % i for i in range(50)]
    perm = [int(x) for x in r.permutation(50)]
    inv = {lab: perm.index(i) for i, lab in enumerate(labels)}
    dic = pa.array([labels[i] for i in perm])
    codes = np.array([inv[x] for x in t["cat"].to_pylist()], dtype=np.int32)
    write(fam, "m18-dictionary-order", "cat: same strings, Arrow dictionary array with a permuted dictionary order",
          with_col(t, "cat", pa.DictionaryArray.from_arrays(pa.array(codes), dic)), 0)
    r3 = rng("pq-wholesale")
    pay = ["%s-%s-%08x" % (WORDS[a], WORDS[b], c) for a, b, c in zip(r3.randint(0, 10, N), r3.randint(0, 10, N), r3.randint(0, 2**31, N))]
    write(fam, "m19-wholesale-payload", "payload column regenerated (wholesale change)", with_col(t, "payload", pa.array(pay)), N * 22)
    write(fam, "m20-byte-stream-split", "float/double with BYTE_STREAM_SPLIT", t, 0, use_dictionary=False, use_byte_stream_split=["value", "score"])
    write(fam, "u01-unrelated", "unrelated data, same schema (negative control)", base_table(seed="other"), N * 60)
    write(fam, "a01-last-rg-partial-395k", "first 395k rows (last row group partial: append lands inside it)", t.slice(0, 395000), 0)
    fam.close()
    return t


def controls(t):
    fam = Family("parquet", "controls", {"pyarrow": pa.__version__, "rows": N})
    small = t.slice(0, 100000)
    write(fam, "x01-gzip", "gzip codec (unsupported codec)", small, 0, comp="gzip")
    write(fam, "x02-delta", "DELTA_* encodings (unsupported encodings)", small, 0, use_dictionary=False,
          column_encoding={"id": "DELTA_BINARY_PACKED", "ts": "DELTA_BINARY_PACKED", "qty": "DELTA_BINARY_PACKED", "payload": "DELTA_LENGTH_BYTE_ARRAY", "cat": "DELTA_BYTE_ARRAY"})
    r = rng("pq-nested")
    n = 20000
    lists = [[int(x) for x in r.randint(0, 100, r.randint(0, 5))] for _ in range(n)]
    nt = pa.table({"id": pa.array(np.arange(n, dtype=np.int64)), "tags": pa.array(lists, type=pa.list_(pa.int32())),
                   "pt": pa.array([{"x": float(a), "y": (None if a % 7 == 0 else int(a))} for a in r.randint(0, 1000, n)],
                                  type=pa.struct([("x", pa.float64()), ("y", pa.int64())]))})
    write(fam, "x03-nested", "LIST<INT32> and STRUCT<x,y> columns (nested control)", nt, 0)
    write(fam, "x04-lz4", "lz4_raw codec (unsupported codec)", small, 0, comp="lz4")
    write(fam, "x05-page-index-checksum", "page index + page checksums", small, 0, write_page_index=True, write_page_checksum=True)
    write(fam, "x06-no-statistics", "no statistics", small, 0, write_statistics=False)
    fam.close()


def duckdb_variants(t):
    fam = Family("parquet", "duckdb-variants", {"duckdb": duckdb.__version__, "rows": N, "row_group": RG, "compression": "snappy"})
    duck(fam, "d01-snappy", "r0's table re-exported by DuckDB (snappy, 50k row groups)", t, 0)
    duck(fam, "d02-zstd", "DuckDB zstd", t, 0, COMPRESSION="zstd")
    duck(fam, "d03-uncompressed", "DuckDB uncompressed", t, 0, COMPRESSION="uncompressed")
    duck(fam, "d04-row-group-100k", "DuckDB row_group_size 100k", t, 0, ROW_GROUP_SIZE=100000)
    duck(fam, "d05-same-options", "DuckDB re-run, same options (determinism)", t, 0)
    duck(fam, "d06-v2", "DuckDB PARQUET_VERSION V2", t, 0, PARQUET_VERSION="V2")
    v = t["value"].to_numpy().copy(); v[123456] = 3.25
    duck(fam, "d08-update-one-value", "DuckDB, one float changed", with_col(t, "value", pa.array(v)), 8, base="d01-snappy", klass="PRODUCER_WORKLOAD")
    keep = np.ones(N, dtype=bool); keep[200000:204000] = False
    duck(fam, "d09-delete-4k-middle", "DuckDB, 4000 middle rows deleted", t.filter(pa.array(keep)), 0, base="d01-snappy", klass="PRODUCER_WORKLOAD")
    fam.close()


def chain(t):
    fam = Family("parquet", "pyarrow-chain", {"pyarrow": pa.__version__, "rows": N, "row_group": RG, "compression": "snappy"})
    r = rng("pq-chain")
    state = {"next": N}

    def append(cur, k, tag):
        nx = state["next"]
        e = table6(rng("pq-chain-app-" + tag), k)
        e["id"] = pa.array(np.arange(nx, nx + k, dtype=np.int64))
        e["ts"] = pa.array(np.arange(nx, nx + k, dtype=np.int64) * 1000 + 1_700_000_000_000, type=pa.timestamp("ms"))
        e.update(extras(rng("pq-chain-x-" + tag), k))
        state["next"] = nx + k
        return pa.concat_tables([cur, pa.table(e)])

    def sparse(cur, col, k):
        v = cur[col].to_numpy(zero_copy_only=False).copy()
        v[r.randint(0, len(v), k)] += 1.0
        return with_col(cur, col, pa.array(v))

    steps = [("c0", "base", lambda c: c, {}),
             ("c1", "append 8000 rows", lambda c: append(c, 8000, "1"), {}),
             ("c2", "update 200 sparse values", lambda c: sparse(c, "value", 200), {}),
             ("c3", "retention: delete the first 3000 rows", lambda c: c.slice(3000), {}),
             ("c4", "append 8000 rows", lambda c: append(c, 8000, "4"), {}),
             ("c5", "compaction rewrite, 100k row groups", lambda c: c, {"rg": 100000}),
             ("c6", "update 400 sparse values", lambda c: sparse(c, "value", 400), {"rg": 100000}),
             ("c7", "append 8000 rows + metadata tag", lambda c: append(c, 8000, "7"), {"rg": 100000})]
    cur = t
    for name, mut, f, kw in steps:
        cur = f(cur)
        tt = cur.replace_schema_metadata({"rev": name}) if name == "c7" else cur
        write(fam, name, mut, tt, 0, **kw)
    fam.close()


if __name__ == "__main__":
    what = sys.argv[1:] or ["variants", "duckdb", "chain", "controls"]
    t = base_table()
    if "variants" in what: variants()
    if "duckdb" in what: duckdb_variants(t)
    if "chain" in what: chain(t)
    if "controls" in what: controls(t)
