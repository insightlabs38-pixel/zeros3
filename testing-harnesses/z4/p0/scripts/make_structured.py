#!/usr/bin/env python3
"""Deterministic structured-data producers for P0 (run inside the corpus venv: pyarrow, zstandard, rocksdict, numpy).
usage: make_structured.py CORPUS_DIR   -> sqlite/v0..3.db, parquet/{snappy,zstd}/c0..3.parquet, sst/{none,snappy,zstd,lz4}/c0..3.sst,
zstd/*.zst, gz/*.gz. Producer versions are recorded in CORPUS_DIR/producers.json."""
import os, sys, json, random, sqlite3, gzip, hashlib
import numpy as np
C = sys.argv[1]
ONLY = os.environ.get('P0_ONLY', '')   # P0_ONLY=parquet_lz4 builds just the LZ4_RAW chain
os.makedirs(C, exist_ok=True)
info = {}
# ---- SQLite: 4 states of one database (updates + appends) ----
if not ONLY:
  pass
if not ONLY:
    os.makedirs(f'{C}/sqlite', exist_ok=True)
    rnd = random.Random(7)
    def row(i, v=0):
        r = random.Random(i * 1000 + v)
        return (i, f'user-{i:07d}', ''.join(r.choice('abcdefghij klmnopqrstuvwxyz') for _ in range(120)), r.randbytes(200), r.random())
    db = f'{C}/sqlite/v0.db'
    if os.path.exists(db): os.remove(db)
    con = sqlite3.connect(db); con.execute('PRAGMA page_size=4096'); con.execute('PRAGMA journal_mode=DELETE')
    con.execute('CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, doc TEXT, blob BLOB, score REAL)'); con.execute('CREATE INDEX t_name ON t(name)')
    con.executemany('INSERT INTO t VALUES(?,?,?,?,?)', (row(i) for i in range(80000))); con.commit(); con.execute('VACUUM'); con.close()
    import shutil
    for v in range(1, 4):
        shutil.copy(f'{C}/sqlite/v{v-1}.db', f'{C}/sqlite/v{v}.db')
        con = sqlite3.connect(f'{C}/sqlite/v{v}.db')
        base = 80000 + (v - 1) * 800
        con.executemany('INSERT INTO t VALUES(?,?,?,?,?)', (row(i, v) for i in range(base, base + 800)))
        ids = random.Random(v).sample(range(base), 800)
        con.executemany('UPDATE t SET score=?, doc=? WHERE id=?', ((random.Random(i + v).random(), row(i, v)[2], i) for i in ids))
        con.commit(); con.close()

# ---- Parquet: 4-revision chain, snappy and zstd ----
import pyarrow as pa, pyarrow.parquet as pq
info['pyarrow'] = pa.__version__
for codec in (['lz4'] if ONLY == 'parquet_lz4' else ['snappy', 'zstd']):
    os.makedirs(f'{C}/parquet/{codec}', exist_ok=True)
    rng = np.random.default_rng(11)
    n = 400000
    ids = np.arange(n); cat = rng.integers(0, 50, n); val = rng.normal(size=n); txt = np.array([f'item-{i % 5000:05d}-{rng.integers(0, 1 << 30):x}' for i in range(n)])
    for v in range(4):
        if v:
            idx = rng.choice(n, n // 100, replace=False); val[idx] = rng.normal(size=len(idx))
            ids = np.concatenate([ids, np.arange(n, n + 4000) + (v - 1) * 4000])
            cat = np.concatenate([cat, rng.integers(0, 50, 4000)]); val = np.concatenate([val, rng.normal(size=4000)])
            txt = np.concatenate([txt, np.array([f'new-{i}-{rng.integers(0, 1 << 30):x}' for i in range(4000)])]); n = len(ids)
        t = pa.table({'id': ids[:n], 'cat': cat[:n], 'val': val[:n], 'txt': txt[:n]})
        pq.write_table(t, f'{C}/parquet/{codec}/c{v}.parquet', compression=codec, row_group_size=100000, data_page_size=1 << 20)
if not ONLY:
    # ---- RocksDB SSTs (block-based), each codec ----
    try:
        import rocksdict
        from rocksdict import Options, SstFileWriter, DBCompressionType
        info['rocksdict'] = rocksdict.__version__ if hasattr(rocksdict, '__version__') else 'unknown'
        comp = {'none': DBCompressionType.none(), 'snappy': DBCompressionType.snappy(), 'zstd': DBCompressionType.zstd(), 'lz4': DBCompressionType.lz4()}
        for name, ct in comp.items():
            os.makedirs(f'{C}/sst/{name}', exist_ok=True)
            for v in range(4):
                o = Options(); o.set_compression_type(ct)
                w = SstFileWriter(o); w.open(f'{C}/sst/{name}/c{v}.sst')
                r = random.Random(5 + v)
                for i in range(250000):
                    k = f'key{i:09d}'.encode()
                    val = (f'value-{i}-' + ('x' * 40) + str(r.random() if i % 97 == 0 else i % 1000)).encode()
                    w[k] = val
                w.finish()
    except Exception as e:
        info['rocksdict_error'] = repr(e)
    # ---- zstd and gzip re-compressions of the newest coreutils tar ----
    import zstandard
    info['zstandard'] = zstandard.__version__
    tar = f'{C}/src/coreutils-9.7.tar'
    if os.path.exists(tar):
        os.makedirs(f'{C}/zstd', exist_ok=True); os.makedirs(f'{C}/gz', exist_ok=True)
        data = open(tar, 'rb').read()
        for lvl in (3, 19):
            open(f'{C}/zstd/coreutils-9.7.l{lvl}.tar.zst', 'wb').write(zstandard.ZstdCompressor(level=lvl, write_content_size=True).compress(data))
        for lvl in (6, 9):
            with gzip.GzipFile(f'{C}/gz/coreutils-9.7.l{lvl}.tar.gz', 'wb', compresslevel=lvl, mtime=0) as f:
                f.write(data)

json.dump(info, open(f'{C}/producers.json', 'w'), indent=1)
print(json.dumps(info))
