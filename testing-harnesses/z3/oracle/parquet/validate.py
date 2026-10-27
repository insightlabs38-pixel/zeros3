"""Z3-31 parser differential validation: the Go parser/decoder (`z3parquet dump`) against PyArrow and DuckDB as
ORACLES (schema, row count, row-group count and sizes, codec and encoding sets per column, and, for flat columns the
decoder claims, a SHA-256 over the canonical value stream and over the null-run stream computed here from the
reference readers' logical values). Exactness of reconstruction never depends on this script: it is additional
validation. Usage: validate.py Z3PARQUET_BINARY FILE..."""
import hashlib, json, subprocess, sys
import numpy as np, pyarrow as pa, pyarrow.parquet as pq, duckdb

PHYS = ["BOOLEAN", "INT32", "INT64", "INT96", "FLOAT", "DOUBLE", "BYTE_ARRAY", "FIXED_LEN_BYTE_ARRAY"]


def uv(v):
    out = bytearray()
    while v >= 0x80:
        out.append(v & 0x7F | 0x80)
        v >>= 7
    out.append(v)
    return bytes(out)


def canon_values(col, phys):
    """canonical PLAIN-form bytes of the non-null values of a pyarrow ChunkedArray (None: not comparable)"""
    h = hashlib.sha256()
    for ch in col.chunks:
        if pa.types.is_dictionary(ch.type):
            ch = ch.dictionary_decode()
        ch = ch.drop_null()
        t = ch.type
        if phys == 0:
            h.update(bytes(np.asarray(ch.to_numpy(zero_copy_only=False), dtype=np.uint8)))
        elif phys in (1, 2):
            dt = "<i4" if phys == 1 else "<i8"
            if pa.types.is_temporal(t):
                ch = ch.cast(pa.int32() if phys == 1 else pa.int64())
            elif pa.types.is_unsigned_integer(t):
                ch = ch.cast(pa.int32() if phys == 1 else pa.int64(), safe=False)
            h.update(np.asarray(ch.to_numpy(zero_copy_only=False)).astype(dt).tobytes())
        elif phys in (4, 5):
            h.update(np.asarray(ch.to_numpy(zero_copy_only=False)).astype("<f4" if phys == 4 else "<f8").tobytes())
        elif phys == 6:
            for v in ch.to_pylist():
                b = v.encode() if isinstance(v, str) else v
                h.update(len(b).to_bytes(4, "little") + b)
        elif phys == 7:
            for v in ch.to_pylist():
                h.update(v)
        else:
            return None
    return h.hexdigest()


def canon_levels(col, maxdef):
    if maxdef != 1:
        return None
    h = hashlib.sha256()
    level, run = None, 0
    for ch in col.chunks:
        valid = np.asarray(ch.is_valid().to_numpy(zero_copy_only=False), dtype=np.uint8)
        if len(valid) == 0:
            continue
        # run-length encode the validity of this chunk, merging runs across chunks
        change = np.flatnonzero(np.diff(valid)) + 1
        starts = np.concatenate(([0], change))
        ends = np.concatenate((change, [len(valid)]))
        for s, e in zip(starts, ends):
            lv = int(valid[s])
            if level is None:
                level, run = lv, 0
            if lv != level:
                h.update(uv(run << 4 | level))
                level, run = lv, 0
            run += int(e - s)
    if level is not None:
        h.update(uv(run << 4 | level))
    return h.hexdigest()


def check(binary, path):
    d = json.loads(subprocess.check_output([binary, "dump", "-file", path]))
    pf = pq.ParquetFile(path)
    md = pf.metadata
    errs = []
    if d["rows"] != md.num_rows:
        errs.append("rows %d != %d" % (d["rows"], md.num_rows))
    if d["row_groups"] != md.num_row_groups or d["rg_rows"] != [md.row_group(i).num_rows for i in range(md.num_row_groups)]:
        errs.append("row groups differ")
    leafs = [md.schema.column(i) for i in range(md.num_columns)]
    if len(d["columns"]) != len(leafs):
        errs.append("column count %d != %d" % (len(d["columns"]), len(leafs)))
        return d, errs, 0, 0
    for i, (c, leaf) in enumerate(zip(d["columns"], leafs)):
        if c["name"] != leaf.path or PHYS[c["phys"]] != leaf.physical_type or c["maxdef"] != leaf.max_definition_level or c["maxrep"] != leaf.max_repetition_level:
            errs.append("column %d schema: %s/%s vs %s/%s" % (i, c["name"], PHYS[c["phys"]], leaf.path, leaf.physical_type))
        codecs = sorted({md.row_group(g).column(i).compression.replace("LZ4", "LZ4_RAW") if md.row_group(g).column(i).compression == "LZ4" else md.row_group(g).column(i).compression for g in range(md.num_row_groups)})
        encs = sorted({e for g in range(md.num_row_groups) for e in md.row_group(g).column(i).encodings})
        if codecs != c["codecs"]:
            errs.append("column %s codecs %s vs %s" % (c["name"], c["codecs"], codecs))
        if sorted(set(encs)) != c["encodings"]:
            errs.append("column %s encodings %s vs %s" % (c["name"], c["encodings"], encs))
    # DuckDB oracle: rows, row groups, per-column codec and encodings
    con = duckdb.connect()
    nested = any(c["maxrep"] for c in d["columns"])  # DuckDB counts repeated leaf values, not rows
    n = con.execute("select count(*) from read_parquet(?)", [path]).fetchone()[0]
    if n != d["rows"] and not nested:
        errs.append("duckdb rows %d != %d" % (n, d["rows"]))
    rgs = con.execute("select count(distinct row_group_id) from parquet_metadata(?)", [path]).fetchone()[0]
    if rgs != d["row_groups"]:
        errs.append("duckdb row groups %d != %d" % (rgs, d["row_groups"]))
    for name, comp, enc in con.execute("select path_in_schema, compression, encodings from parquet_metadata(?)", [path]).fetchall():
        c = next((x for x in d["columns"] if x["name"] == name), None)
        if c is None:
            continue
        if comp not in c["codecs"] and comp.replace("LZ4", "LZ4_RAW") not in c["codecs"]:
            errs.append("duckdb codec %s for %s not in %s" % (comp, name, c["codecs"]))
        for e in enc.split(","):
            e = e.strip()
            if e and e not in c["encodings"]:
                errs.append("duckdb encoding %s for %s not in %s" % (e, name, c["encodings"]))
    # logical values of the columns the Go decoder takes
    compared = skipped = 0
    try:
        t = pq.read_table(path)
    except Exception as ex:  # a reader that cannot read the file: skip the value comparison
        return d, errs, 0, len(d["columns"])
    names = {leaf.path: i for i, leaf in enumerate(leafs)}
    for c in d["columns"]:
        if c["opaque_chunks"] or c["maxrep"] or c["phys"] == 3 or c["name"] not in t.column_names:
            skipped += 1
            continue
        col = t[c["name"]]
        if pa.types.is_decimal(col.type):  # decimal -> integer is an interpretation, not the physical bytes
            skipped += 1
            continue
        want = canon_values(col, c["phys"])
        if want is None:
            skipped += 1
            continue
        if want != c["value_sha"]:
            errs.append("column %s: canonical values differ from PyArrow's" % c["name"])
        wl = canon_levels(col, c["maxdef"])
        if c["maxdef"] == 1 and wl != c["level_sha"]:
            errs.append("column %s: null runs differ from PyArrow's" % c["name"])
        if c["maxdef"] == 0 and c["level_sha"] != hashlib.sha256(b"").hexdigest():
            errs.append("column %s: required column with levels" % c["name"])
        compared += 1
    return d, errs, compared, skipped


if __name__ == "__main__":
    binary, files = sys.argv[1], sys.argv[2:]
    bad = 0
    print("| file | rows | row groups | columns | pages | columns compared by value | skipped (opaque/nested/INT96/unreadable) | result |")
    print("|---|---:|---:|---:|---:|---:|---:|---|")
    for f in files:
        try:
            d, errs, cmp_, skp = check(binary, f)
        except Exception as ex:
            print("| %s | | | | | | | ERROR %s |" % (f.split("/")[-1], ex))
            bad += 1
            continue
        print("| %s | %d | %d | %d | %d | %d | %d | %s |" % (f.split("/")[-1], d["rows"], d["row_groups"], len(d["columns"]), d["pages"], cmp_, skp, "match" if not errs else "; ".join(errs[:3])))
        bad += bool(errs)
    sys.exit(1 if bad else 0)
