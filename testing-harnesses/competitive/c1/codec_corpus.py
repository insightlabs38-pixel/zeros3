"""Generate independent Parquet and SST page histories."""
import argparse
import hashlib
import json
from pathlib import Path

from corpus import blob

FAMILIES = ("parquet", "sst")


def parquet(path, revision):
    import pyarrow as pa
    import pyarrow.parquet as pq

    count = 65536
    ids = list(range(count))
    value = [f"region-{i % 127:03}-reading-{i % 4096:04}" for i in ids]
    for i in range(revision * 512, revision * 512 + revision * 16):
        value[i] = f"updated-{revision}-{i:05}"
    table = pa.table({"id": ids, "reading": value,
                      "payload": [blob(100 + (i % 3072), 32) for i in ids]})
    pq.write_table(table, path, compression="snappy", data_page_size=32768,
                   write_batch_size=512, row_group_size=8192)


def sst(path, revision):
    from rocksdict import DBCompressionType, Options, SstFileWriter

    options = Options(raw_mode=True)
    options.set_compression_type(DBCompressionType.lz4())
    writer = SstFileWriter(options=options)
    writer.open(str(path))
    for i in range(65536):
        if revision == 2 and i < 64:
            continue
        key = f"segment-{i // 128:04}/{i:06}".encode()
        changed = revision if revision * 512 <= i < revision * 512 + revision * 16 else 0
        value = b"sample/" * 8 + blob(5000 + i % 8192 + changed, 48)
        writer[key] = value
    writer.finish()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--parquet-only", action="store_true",
                        help="omit SST when byte-identical regeneration is required")
    args = parser.parse_args()
    rows = []
    writers = [("parquet", parquet)]
    if not args.parquet_only:
        writers.append(("sst", sst))
    for family, writer in writers:
        for revision in range(3):
            rel = f"{family}/{revision}/snapshot.{family}"
            path = args.root / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            if not path.exists():
                writer(path, revision)
            data = path.read_bytes()
            rows.append({"path": rel, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
    encoded = json.dumps(rows, indent=2) + "\n"
    if args.manifest.exists() and args.manifest.read_text() != encoded:
        raise ValueError("codec corpus does not match the saved byte manifest")
    args.manifest.write_text(encoded)


if __name__ == "__main__":
    main()
