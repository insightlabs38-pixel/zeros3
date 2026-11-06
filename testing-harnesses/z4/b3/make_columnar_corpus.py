#!/usr/bin/env python3
"""Paired columnar exports of one pinned logical table (pyarrow producer + independent logical-equality oracle).
usage: make_columnar_corpus.py OUTDIR"""
import hashlib, json, sys, os
import numpy as np, pyarrow as pa, pyarrow.parquet as pq, pyarrow.orc as orc
out = sys.argv[1]; os.makedirs(out, exist_ok=True)
rng = np.random.default_rng(20261106)
n = 200_000
cats = np.array(['alpha', 'beta', 'gamma', 'delta', 'epsilon-long-category-name', 'z'])
tables = {
 # all columns REQUIRED (nullable=False): the exactly supported profile
 'req': pa.table({
   'id': pa.array(np.arange(n, dtype=np.int64)),
   'k32': pa.array(rng.integers(-1000, 1000, n).astype(np.int32)),
   'u32': pa.array(rng.integers(0, 2**31, n).astype(np.uint32)),
   'x': pa.array(np.cumsum(rng.standard_normal(n)).astype(np.float64)),
   'f': pa.array(rng.standard_normal(n).astype(np.float32)),
   'name': pa.array(cats[rng.integers(0, len(cats), n)]),
   'blob': pa.array([bytes(rng.integers(0, 256, rng.integers(0, 24)).astype(np.uint8)) for _ in range(n)], type=pa.binary()),
 }, schema=pa.schema([pa.field(k, t, nullable=False) for k, t in [('id', pa.int64()), ('k32', pa.int32()), ('u32', pa.uint32()), ('x', pa.float64()), ('f', pa.float32()), ('name', pa.string()), ('blob', pa.binary())]])),
}
t = tables['req']
# default (OPTIONAL) schema with no nulls: what most real writers emit
tables['opt'] = pa.table({k: t[k] for k in t.column_names})
# nullable + narrow-int + dictionary variants (difficult profile)
nullable = pa.table({'id': t['id'], 'x': pa.array(t['x'].to_numpy(), mask=rng.random(n) < 0.1), 'name': t['name'], 'i16': pa.array(rng.integers(-300, 300, n).astype(np.int16))})
tables['hard'] = nullable
manifest = {}
def sha(p): return hashlib.sha256(open(p, 'rb').read()).hexdigest()
def w_arrow(name, tbl, batch, stream=False):
    p = f'{out}/{name}.arrows' if stream else f'{out}/{name}.arrow'
    with (pa.ipc.new_stream if stream else pa.ipc.new_file)(p, tbl.schema) as w:
        for b in tbl.to_batches(max_chunksize=batch): w.write_batch(b)
    return p
for tn, tbl in tables.items():
    paths = {}
    paths['arrow_64k'] = w_arrow(f'{tn}.arrow64k', tbl, 65536)
    paths['arrow_50k'] = w_arrow(f'{tn}.arrow50k', tbl, 50000)
    perm = tbl.select(list(reversed(tbl.column_names)))
    paths['arrow_rev_cols'] = w_arrow(f'{tn}.arrowrev', perm, 65536)
    for nm, kw in {'pq_plain_none_rg100k_v1': dict(compression='none', use_dictionary=False, row_group_size=100000, data_page_version='1.0'),
                   'pq_plain_none_rg64k_v2': dict(compression='none', use_dictionary=False, row_group_size=65536, data_page_version='2.0'),
                   'pq_default_snappy_dict': dict(),
                   'pq_zstd_dict': dict(compression='zstd'),
                   'pq_plain_snappy': dict(compression='snappy', use_dictionary=False)}.items():
        p = f'{out}/{tn}.{nm}.parquet'; pq.write_table(tbl, p, **kw); paths[nm] = p
    otbl = tbl.drop(['u32']) if 'u32' in tbl.column_names else tbl  # ORC has no unsigned 32-bit type
    p = f'{out}/{tn}.orc_zlib.orc'; orc.write_table(otbl, p, compression='zlib'); paths['orc_zlib'] = p
    p = f'{out}/{tn}.orc_none.orc'; orc.write_table(otbl, p, compression='uncompressed'); paths['orc_none'] = p
    # independent oracle: every file decodes to the identical logical table (columns by name)
    ref = tbl
    for k, p in paths.items():
        if p.endswith('.arrow'): got = pa.ipc.open_file(p).read_all()
        elif p.endswith('.parquet'): got = pq.read_table(p)
        elif p.endswith('.orc'): got = orc.read_table(p)
        r = ref.select(got.column_names)
        if tn == 'hard' and k.startswith('orc'):
            r = r.cast(got.schema)  # ORC stores int16 as smallint: compare after the schema cast
        assert all(got[c].equals(r[c]) for c in got.column_names), (tn, k, [c for c in got.column_names if not got[c].equals(r[c])])
    manifest[tn] = {k: {'path': os.path.basename(p), 'size': os.path.getsize(p), 'sha256': sha(p)} for k, p in paths.items()}
json.dump(manifest, open(f'{out}/manifest.json', 'w'), indent=1)
print(json.dumps({tn: {k: v['size'] for k, v in m.items()} for tn, m in manifest.items()}, indent=0))
