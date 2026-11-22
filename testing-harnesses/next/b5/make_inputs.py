#!/usr/bin/env python3
"""B5 inputs beyond the P0 corpus, Python standard library only (deterministic).
usage: make_inputs.py CORPUS_DIR   (CORPUS_DIR/src/coreutils-9.7.tar from fetch_corpus.sh tars)
  sqlite/v0..3.db           one database, four states (P0 make_structured.py rules: 800 inserts + 800 updates each)
  gz/coreutils-9.7.l6.tar.gz  zlib level 6, mtime 0 (seekable gzip representation, B2)
  random/r0..2.bin          16 MiB of unrelated random bytes each (negative control)"""
import os, sys, random, sqlite3, gzip, shutil
C = sys.argv[1]
os.makedirs(f'{C}/sqlite', exist_ok=True)
def row(i, v=0):
    r = random.Random(i * 1000 + v)
    return (i, f'user-{i:07d}', ''.join(r.choice('abcdefghij klmnopqrstuvwxyz') for _ in range(120)), r.randbytes(200), r.random())
db = f'{C}/sqlite/v0.db'
if not os.path.exists(f'{C}/sqlite/v3.db'):
    if os.path.exists(db): os.remove(db)
    con = sqlite3.connect(db); con.execute('PRAGMA page_size=4096'); con.execute('PRAGMA journal_mode=DELETE')
    con.execute('CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, doc TEXT, blob BLOB, score REAL)'); con.execute('CREATE INDEX t_name ON t(name)')
    con.executemany('INSERT INTO t VALUES(?,?,?,?,?)', (row(i) for i in range(80000))); con.commit(); con.execute('VACUUM'); con.close()
    for v in range(1, 4):
        shutil.copy(f'{C}/sqlite/v{v-1}.db', f'{C}/sqlite/v{v}.db')
        con = sqlite3.connect(f'{C}/sqlite/v{v}.db')
        base = 80000 + (v - 1) * 800
        con.executemany('INSERT INTO t VALUES(?,?,?,?,?)', (row(i, v) for i in range(base, base + 800)))
        ids = random.Random(v).sample(range(base), 800)
        con.executemany('UPDATE t SET score=?, doc=? WHERE id=?', ((random.Random(i + v).random(), row(i, v)[2], i) for i in ids))
        con.commit(); con.close()
os.makedirs(f'{C}/gz', exist_ok=True)
gz = f'{C}/gz/coreutils-9.7.l6.tar.gz'
if not os.path.exists(gz):
    with gzip.GzipFile(gz, 'wb', compresslevel=6, mtime=0) as f:
        f.write(open(f'{C}/src/coreutils-9.7.tar', 'rb').read())
os.makedirs(f'{C}/random', exist_ok=True)
for i in range(3):
    p = f'{C}/random/r{i}.bin'
    if not os.path.exists(p):
        open(p, 'wb').write(random.Random(500 + i).randbytes(16 << 20))
