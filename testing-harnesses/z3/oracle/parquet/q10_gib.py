"""Native PyArrow 18.1.0 Parquet, exactly 1 GiB including native footer metadata.
The deterministic INT32 column is written in bounded batches, without a dictionary.
Only footer metadata padding is adjusted through PyArrow's public writer API.
"""
import hashlib, json, os, sys
import numpy as np
import pyarrow as pa
import pyarrow.parquet as pq
assert pa.__version__ == "18.1.0"
path = sys.argv[1]
target, rows, batch = 1 << 30, 267_000_000, 1_000_000
schema = pa.schema([("value", pa.int32())])
def write(padding):
    rng = np.random.RandomState(1039)
    s = schema
    with pq.ParquetWriter(path, s, compression="snappy", use_dictionary=False,
                          store_schema=False, write_statistics=False,
                          data_page_size=1 << 20) as w:
        w.add_key_value_metadata({"zeros3_memory_control": "x" * padding})
        for start in range(0, rows, batch):
            n = min(batch, rows - start)
            values = rng.randint(-(1 << 31), 1 << 31, n, dtype=np.int32)
            w.write_table(pa.table({"value": values}, schema=s), row_group_size=n)
write(0)
padding = target - os.path.getsize(path)
assert 0 < padding < 16 << 20
for attempt in range(3):
    write(padding)
    delta = target - os.path.getsize(path)
    if not delta:
        break
    padding += delta
assert os.path.getsize(path) == target
h = hashlib.sha256()
with open(path, "rb") as f:
    while b := f.read(1 << 20): h.update(b)
out = dict(producer="pyarrow", version=pa.__version__, source_bytes=target,
           rows=rows, logical_value_bytes=rows*4, footer_padding=padding,
           sha256=h.hexdigest(), class_="SYNTHETIC_MEMORY_CONTROL")
with open(path + ".json", "w") as f: json.dump(out, f, indent=2)
print(json.dumps(out))
