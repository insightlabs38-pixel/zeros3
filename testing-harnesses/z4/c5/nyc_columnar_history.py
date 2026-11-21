#!/usr/bin/env python3
"""Derive comparable append histories from two published NYC TLC Parquet files."""
import argparse
import hashlib
import json
from pathlib import Path

import duckdb
import pyarrow as pa
import pyarrow.parquet as pq


def read_first(path, limit):
    rows = []
    for batch in pq.ParquetFile(path).iter_batches(batch_size=32768):
        rows.append(batch)
        limit -= batch.num_rows
        if limit <= 0:
            return pa.Table.from_batches(rows).slice(0, sum(b.num_rows for b in rows) + limit)
    raise ValueError(f"{path} lacks requested rows")


def describe(path):
    with path.open("rb") as f:
        return {"file": path.name, "bytes": path.stat().st_size, "sha256": hashlib.file_digest(f, "sha256").hexdigest()}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("january", type=Path)
    p.add_argument("february", type=Path)
    p.add_argument("output", type=Path)
    args = p.parse_args()
    args.output.mkdir()
    first, second = read_first(args.january, 80000), read_first(args.february, 40000)
    first = first.replace_schema_metadata(None).combine_chunks()
    second = second.replace_schema_metadata(None).combine_chunks()
    revisions = (first, pa.concat_tables([first, second.slice(0, 20000)]), pa.concat_tables([first, second]))
    manifest = {"source": [describe(args.january), describe(args.february)],
                "pyarrow": pa.__version__, "duckdb": duckdb.__version__, "cohorts": []}
    with duckdb.connect() as con:
        con.execute("SET threads=1")
        for writer, codecs in (("pyarrow", ("snappy", "zstd", "lz4", "brotli")), ("duckdb", ("snappy", "zstd"))):
            for codec in codecs:
                name = f"{writer}-{codec}"
                files = []
                for i, revision in enumerate(revisions):
                    dest = args.output / f"{name}-{i}.parquet"
                    if writer == "pyarrow":
                        options = {"compression_level": 8} if codec == "brotli" else {}
                        pq.write_table(revision, dest, compression=codec, row_group_size=40000, **options)
                    else:
                        con.register("revision", revision)
                        con.table("revision").write_parquet(str(dest), compression=codec, row_group_size=40000)
                        con.unregister("revision")
                    loaded = pq.read_table(dest).cast(revision.schema)
                    if not loaded.equals(revision):
                        raise RuntimeError(f"round-trip changed {dest.name}")
                    files.append({"rows": revision.num_rows, **describe(dest)})
                manifest["cohorts"].append({"name": name, "files": files})
    (args.output / "MANIFEST.json").write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
