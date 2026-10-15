"""Parquet family (pyarrow): ~400k rows x 6 columns, 8 row groups, snappy; independent variants of r0.
Also writes each file's native structure (row groups / column chunks / offsets) to <rev>/db.parquet.layout.json
as the STRUCTURAL ORACLE (taken from pyarrow's footer metadata, not from a Z3 parser)."""
import json, os
import numpy as np, pyarrow as pa, pyarrow.parquet as pq
from common import Family, rng

N = 400000
RG = 50000


def table(r, n=N):
    words = np.array(["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"])
    pay = ["%s-%s-%08x" % (words[a], words[b], c) for a, b, c in zip(r.randint(0, 10, n), r.randint(0, 10, n), r.randint(0, 2**31, n))]
    return pa.table({
        "id": pa.array(np.arange(n, dtype=np.int64)),
        "ts": pa.array(np.arange(n, dtype=np.int64) * 1000 + 1_700_000_000_000, type=pa.timestamp("ms")),
        "cat": pa.array(["c%02d" % x for x in r.randint(0, 50, n)]),
        "value": pa.array(r.standard_normal(n)),
        "payload": pa.array(pay),
        "flag": pa.array(r.randint(0, 2, n).astype(bool)),
    })


def write(fam, name, mutation, t, logical, note="", rg=RG, comp="snappy", **kw):
    d = fam.rev(name)
    p = os.path.join(d, "db.parquet")
    pq.write_table(t, p, row_group_size=rg, compression=comp, **kw)
    md = pq.ParquetFile(p).metadata
    lay = {"footer_len": md.serialized_size, "num_rows": md.num_rows, "row_groups": []}
    for g in range(md.num_row_groups):
        rgm = md.row_group(g)
        cols = []
        for c in range(rgm.num_columns):
            cm = rgm.column(c)
            start = cm.dictionary_page_offset if cm.has_dictionary_page and cm.dictionary_page_offset else cm.data_page_offset
            cols.append({"name": cm.path_in_schema, "start": start, "len": cm.total_compressed_size})
        lay["row_groups"].append(cols)
    json.dump(lay, open(p + ".layout.json", "w"))
    fam.record(name, mutation, logical, note)


def main():
    import pyarrow
    fam = Family("parquet", "t400k", {"pyarrow": pyarrow.__version__, "rows": N, "row_group": RG, "compression": "snappy"})
    r = rng("pq-base")
    t = table(r)
    write(fam, "r0", "base", t, 0)
    r = rng("pq-mut")

    def with_col(t, name, arr): return t.set_column(t.schema.get_field_index(name), name, arr)
    v = t["value"].to_numpy().copy(); v[123456] = 3.25
    write(fam, "m01-update-one-value", "one float value changed", with_col(t, "value", pa.array(v)), 8)
    v = t["value"].to_numpy().copy(); v[r.randint(0, N, 100)] = 1.5
    write(fam, "m02-update-100-sparse", "100 sparse floats changed", with_col(t, "value", pa.array(v)), 800)
    r2 = rng("pq-app"); extra = table(r2, 5000).set_column(0, "id", pa.array(np.arange(N, N + 5000, dtype=np.int64)))
    write(fam, "m03-append-5k", "append 5000 rows (new partial row group)", pa.concat_tables([t, extra]), 5000 * 50)
    keep = np.ones(N, dtype=bool); keep[200000:204000] = False
    write(fam, "m04-delete-4k-middle", "delete 4000 rows mid-file (later rows shift)", t.filter(pa.array(keep)), 0)
    write(fam, "m05-metadata-only", "key/value metadata added (footer only)", t.replace_schema_metadata({"job": "z3-18"}), 8)
    write(fam, "m06-row-group-100k", "same contents, row_group_size 50k->100k", t, 0, rg=100000)
    write(fam, "m07-zstd", "same contents, snappy->zstd", t, 0, comp="zstd")
    write(fam, "m08-rewrite-same-options", "same contents rewritten by a fresh writer", t, 0)
    s = t["value"].to_numpy() * 1.01
    write(fam, "m09-one-column-recomputed", "one whole column recomputed", with_col(t, "value", pa.array(s)), N * 8)
    ids = np.arange(N); idx = r.permutation(N)
    write(fam, "m10-reordered-rows", "rows permuted (same set)", t.take(pa.array(idx)), 0)
    v = t["value"].to_numpy().copy(); v[r.randint(0, N, 4000)] = 2.5
    write(fam, "m11-tiny-updates-4000", "4000 scattered floats changed", with_col(t, "value", pa.array(v)), 4000 * 8)
    write(fam, "m12-no-dictionary-v2", "same contents, dictionary off + data page v2", t, 0, use_dictionary=False, data_page_version="2.0")
    fam.close()


main()
