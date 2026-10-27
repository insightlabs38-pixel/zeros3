"""Z3-31 large-file workload: the same 10-column schema, written batch by batch (one row group per batch) so the
producer never holds the table. Usage: gen_large.py ROWS OUT.parquet [codec]."""
import os, sys
sys.path.insert(0, os.path.dirname(__file__))
import numpy as np, pyarrow as pa, pyarrow.parquet as pq
import gen_pyarrow as g
from common import rng

rows, out = int(sys.argv[1]), sys.argv[2]
codec = sys.argv[3] if len(sys.argv) > 3 else "snappy"
B = 500000
w = None
done = 0
while done < rows:
    n = min(B, rows - done)
    t = g.table6(rng("pq-big", done), n)
    t["id"] = pa.array(np.arange(done, done + n, dtype=np.int64))
    t["ts"] = pa.array(np.arange(done, done + n, dtype=np.int64) * 1000 + 1_700_000_000_000, type=pa.timestamp("ms"))
    t.update(g.extras(rng("pq-big-x", done), n))
    t = pa.table(t)
    if w is None:
        w = pq.ParquetWriter(out, t.schema, compression=codec)
    w.write_table(t, row_group_size=n)
    done += n
w.close()
print(out, os.path.getsize(out))
