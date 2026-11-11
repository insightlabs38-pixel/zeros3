#!/usr/bin/env python3 -I
"""Bounded ORC feasibility probe (P6). For the pinned columnar corpus (z4/b3/make_columnar_corpus.py) it asks, per column,
whether the canonical dense value bytes that Arrow and Parquet PLAIN share occur VERBATIM inside the ORC file (uncompressed
codec) - i.e. whether ORC could join the cross-format component without an RLE reconstruction codec. Integer columns are
RLE-v2 encoded (no verbatim run), IEEE columns are raw little-endian streams.
usage: orc_feasibility.py COLDIR"""
import json, os, sys
import numpy as np, pyarrow as pa, pyarrow.orc as orc, pyarrow.parquet as pq
d = sys.argv[1]
res = {}
for prof in ('req', 'opt', 'hard'):
    p = os.path.join(d, f'{prof}.orc_none.orc')
    raw = open(p, 'rb').read()
    t = orc.read_table(p)
    ref = pq.read_table(os.path.join(d, f'{prof}.pq_plain_none_rg100k_v1.parquet'))
    out = {'orc_bytes': len(raw), 'columns': {}}
    for name in t.column_names:
        col = ref[name].combine_chunks()
        nn = col.drop_null() if col.null_count else col
        if pa.types.is_integer(nn.type) or pa.types.is_floating(nn.type):
            dense = np.asarray(nn.to_numpy(zero_copy_only=False)).astype(nn.type.to_pandas_dtype()).tobytes()
        else:
            out['columns'][name] = {'type': str(nn.type), 'dense_bytes': None, 'verbatim': None}
            continue
        probe = dense[len(dense) // 2: len(dense) // 2 + 4096] if len(dense) > 8192 else dense
        out['columns'][name] = {'type': str(nn.type), 'dense_bytes': len(dense), 'verbatim': raw.find(probe) >= 0}
    res[prof] = out
print(json.dumps(res, indent=1))
