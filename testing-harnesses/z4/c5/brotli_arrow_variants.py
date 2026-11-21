import hashlib
import json
from pathlib import Path
import sys
import pyarrow
import pyarrow.parquet as pq

original, out = (Path(arg) for arg in sys.argv[1:])
source = pq.ParquetFile(original)
manifest = {"pyarrow": pyarrow.__version__, "source_sha256": hashlib.sha256(original.read_bytes()).hexdigest(), "variants": []}
for level in (10, 11):
    dest = out / f"arrow-q{level}.parquet"
    with pq.ParquetWriter(dest, source.schema_arrow, compression="brotli", compression_level=level) as writer:
        for rowgroup in range(source.num_row_groups):
            writer.write_table(source.read_row_group(rowgroup))
    converted = pq.ParquetFile(dest)
    if converted.num_row_groups != source.num_row_groups:
        raise RuntimeError("row group count changed")
    for rowgroup in range(source.num_row_groups):
        if not source.read_row_group(rowgroup).equals(converted.read_row_group(rowgroup), check_metadata=True):
            raise RuntimeError(f"logical content changed in row group {rowgroup}")
    manifest["variants"].append({"level": level, "row_groups_checked": source.num_row_groups,
                                 "file": dest.name, "bytes": dest.stat().st_size,
                                 "sha256": hashlib.sha256(dest.read_bytes()).hexdigest()})
(out / "arrow-variants.json").write_text(json.dumps(manifest, indent=2) + "\n")
