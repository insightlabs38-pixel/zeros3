#!/usr/bin/env python3
"""B10 derived inputs: Parquet and RocksDB SST histories written by real producers from real data revisions.

usage: make_inputs.py CORPUS   (CORPUS/src/owid: a clone of github.com/owid/co2-data; pyarrow 18.1.0, rocksdict 0.3.29)
Writes CORPUS/b10/{pq-snappy,pq-zstd,pq-lz4,sst-snappy,sst-lz4}/v{0..3}.* and CORPUS/b10/ctl/* controls.
"""
import io, os, subprocess, sys, random, tarfile, gzip
import pyarrow as pa, pyarrow.csv as pc, pyarrow.parquet as pq
from rocksdict import Options, SstFileWriter, DBCompressionType

REVS = ["5dea2d0bf78dc98ba1b0910b477e778c72706b14", "db7e13b2f28093b564fea40e1449babb184e687e",
        "8726b7ecd9e8739269843bc9f2db8407a3ad4a49", "c67340b1f482f61b3c342a191ff4c6ecfc065266"]
C = sys.argv[1]
out = os.path.join(C, "b10")
for d in ["pq-snappy", "pq-zstd", "pq-lz4", "sst-snappy", "sst-lz4", "ctl"]:
    os.makedirs(os.path.join(out, d), exist_ok=True)
for i, rev in enumerate(REVS):
    csv = subprocess.run(["git", "-C", os.path.join(C, "src/owid"), "show", rev + ":owid-co2-data.csv"], check=True, capture_output=True).stdout
    t = pc.read_csv(io.BytesIO(csv))
    for codec, d in [("snappy", "pq-snappy"), ("zstd", "pq-zstd"), ("lz4", "pq-lz4")]:
        pq.write_table(t, os.path.join(out, d, f"v{i}.parquet"), compression=codec)
    lines = csv.split(b"\n")
    head, rows = lines[0], [l for l in lines[1:] if l]
    kv = {}
    for r in rows:  # key: country|year (the first two columns); value: the whole CSV row
        f = r.split(b",", 2)
        kv[f[0] + b"|" + f[1]] = r
    for comp, d in [(DBCompressionType.snappy(), "sst-snappy"), (DBCompressionType.lz4(), "sst-lz4")]:
        o = Options()
        o.set_compression_type(comp)
        w = SstFileWriter(options=o)
        w.open(os.path.join(out, d, f"v{i}.sst"))
        for k in sorted(kv):
            w[k] = kv[k]
        w.finish()
# controls: incompressible tars of gzip-looking members, a truncated Parquet file, unrelated gems are fetched
rnd = random.Random(10)
for i in range(3):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tf:
        for j in range(6):
            data = b"\x1f\x8b\x08" + rnd.randbytes(200_000)  # gzip magic, not gzip
            ti = tarfile.TarInfo(f"blob{j}.gz"); ti.size = len(data); ti.mtime = 1700000000
            tf.addfile(ti, io.BytesIO(data))
            data = gzip.compress(rnd.randbytes(150_000), mtime=0)  # real gzip of random bytes
            ti = tarfile.TarInfo(f"rand{j}.gz"); ti.size = len(data); ti.mtime = 1700000000
            tf.addfile(ti, io.BytesIO(data))
    open(os.path.join(out, "ctl", f"rnd{i}.tar"), "wb").write(buf.getvalue())
pqb = open(os.path.join(out, "pq-snappy", "v3.parquet"), "rb").read()
open(os.path.join(out, "ctl", "trunc.parquet"), "wb").write(pqb[: len(pqb) // 2] + pqb[-8:])
