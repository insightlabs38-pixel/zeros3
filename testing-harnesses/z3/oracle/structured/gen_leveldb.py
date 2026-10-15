"""LevelDB (plyvel/LevelDB 1.23) and RocksDB (rocksdict) families. Only *.ldb / *.sst table files are kept
(no WAL, MANIFEST or LOG). Each variant is derived from r0 independently."""
import glob, os, shutil, sys, tempfile
import plyvel
from common import Family, rng

N = 200000
VAL = 100


def key(i): return b"key-%09d" % i


def val(r, n=VAL):
    # half incompressible, half repetitive: snappy shrinks it but not to nothing
    return r.bytes(n // 2) + (b"abcdefgh" * n)[: n - n // 2]


def build(path, r, opts=None, n=N, keys=None):
    db = plyvel.DB(path, create_if_missing=True, **(opts or {}))
    wb = db.write_batch()
    for i in range(n):
        wb.put(key(i), val(r))
        if i % 5000 == 4999:
            wb.write(); wb = db.write_batch()
    wb.write()
    db.compact_range()
    return db


def keep(src, dst, ext=".ldb"):
    os.makedirs(dst, exist_ok=True)
    files = sorted(glob.glob(os.path.join(src, "*" + ext)))
    for f in files:
        shutil.copy(f, dst)
    return len(files)


def derive(fam, base, name, mutation, logical, fn, opts=None, note=""):
    tmp = tempfile.mkdtemp(prefix="ldb")
    work = os.path.join(tmp, "db")
    shutil.copytree(base, work)
    db = plyvel.DB(work, **(opts or {}))
    fn(db)
    db.close()
    # reopen to flush the log into a table and settle
    db = plyvel.DB(work, **(opts or {})); db.close()
    keep(work, fam.rev(name))
    shutil.rmtree(tmp)
    fam.record(name, mutation, logical, note)


def main():
    fam = Family("leveldb", "kv200k", {"plyvel": plyvel.__version__, "leveldb": plyvel.__leveldb_version__, "n": N, "value": VAL})
    tmp = tempfile.mkdtemp(prefix="ldbbase")
    base = os.path.join(tmp, "base")
    r = rng("ldb-base")
    db = build(base, r); db.close()
    keep(base, fam.rev("r0")); fam.record("r0", "base", 0)
    r = rng("ldb-mut")

    def upd1(db): db.put(key(100000), val(r))
    derive(fam, base, "m01-update-one", "one value changed, no compaction", VAL, upd1)

    def upd1c(db): db.put(key(100000), val(r)); db.compact_range()
    derive(fam, base, "m02-update-one-compact", "one value changed + full compaction", VAL, upd1c)

    def app(db):
        wb = db.write_batch()
        for i in range(N, N + 2000): wb.put(key(i), val(r))
        wb.write()
    derive(fam, base, "m03-append-1pct", "append 2000 sorted keys, no compaction", 2000 * (VAL + 13), app)

    def appc(db):
        app(db); db.compact_range()
    derive(fam, base, "m04-append-compact", "append 2000 sorted keys + compaction", 2000 * (VAL + 13), appc)

    def dele(db):
        wb = db.write_batch()
        for i in range(0, N, 10): wb.delete(key(i))
        wb.write()
    derive(fam, base, "m05-delete-10pct", "delete 10% keys, no compaction", 0, dele)

    def delc(db):
        dele(db); db.compact_range()
    derive(fam, base, "m06-delete-compact", "delete 10% keys + compaction", 0, delc)

    def ins(db):
        wb = db.write_batch()
        for i in range(2000): wb.put(b"key-%09d.x" % int(r.randint(0, N)), val(r))
        wb.write(); db.compact_range()
    derive(fam, base, "m07-insert-interleaved-compact", "2000 keys interleaved in the key space + compaction", 2000 * (VAL + 15), ins)

    def tiny(db):
        wb = db.write_batch()
        for i in r.randint(0, N, size=1000): wb.put(key(int(i)), val(r))
        wb.write(); db.compact_range()
    derive(fam, base, "m08-tiny-updates-1000-compact", "1000 sparse values changed + compaction", 1000 * VAL, tiny)

    def noop(db): db.compact_range()
    derive(fam, base, "m09-noop-compact", "reopen + compact, no logical change", 0, noop)

    # same logical contents under different physical grouping / producer settings
    def rewrite(name, mutation, opts):
        t2 = tempfile.mkdtemp(prefix="ldbrw"); p = os.path.join(t2, "db")
        src = plyvel.DB(base)
        dst = plyvel.DB(p, create_if_missing=True, **opts)
        wb = dst.write_batch()
        for i, (k, v) in enumerate(src):
            wb.put(k, v)
            if i % 5000 == 4999: wb.write(); wb = dst.write_batch()
        wb.write(); dst.compact_range(); dst.close(); src.close()
        keep(p, fam.rev(name)); shutil.rmtree(t2)
        fam.record(name, mutation, 0)
    rewrite("m10-block-16k", "same contents, block_size 4096->16384", {"block_size": 16384})
    rewrite("m11-no-compression", "same contents, snappy -> none", {"compression": None})
    rewrite("m12-rewrite-same-options", "same contents rebuilt by a fresh writer, same options", {})

    # full rebuilds (what a major compaction produces): later blocks of a table shift after any insert/delete
    def rebuild(name, mutation, logical, extra=(), drop=()):
        t2 = tempfile.mkdtemp(prefix="ldbrb"); p = os.path.join(t2, "db")
        src = plyvel.DB(base)
        kv = {k: v for k, v in src}
        src.close()
        for k in drop: kv.pop(k, None)
        for k, v in extra: kv[k] = v
        dst = plyvel.DB(p, create_if_missing=True)
        wb = dst.write_batch()
        for i, k in enumerate(sorted(kv)):
            wb.put(k, kv[k])
            if i % 5000 == 4999: wb.write(); wb = dst.write_batch()
        wb.write(); dst.compact_range(); dst.close()
        keep(p, fam.rev(name)); shutil.rmtree(t2)
        fam.record(name, mutation, logical)
    rebuild("m13-rebuild-insert-2000", "full rebuild with 2000 interleaved new keys", 2000 * (VAL + 15),
            extra=[(b"key-%09d.x" % int(r.randint(0, N)), val(r)) for _ in range(2000)])
    rebuild("m14-rebuild-delete-one", "full rebuild with one key deleted", 0, drop=[key(50000)])
    rebuild("m15-rebuild-update-one", "full rebuild with one value changed", VAL, extra=[(key(100000), val(r))])
    shutil.rmtree(tmp)
    fam.close()

    # RocksDB: one external producer family (format_version default of the pinned build)
    try:
        from rocksdict import Rdict, Options, BlockBasedOptions, DBCompressionType
        rf = Family("rocksdb", "kv200k", {"rocksdict": __import__("rocksdict").__version__ if hasattr(__import__("rocksdict"), "__version__") else "0.3.29", "n": N})
        def rdb(path, mut=None, bs=4096, comp=DBCompressionType.snappy(), nn=N, rr=None):
            o = Options(); o.create_if_missing(True); o.set_compression_type(comp)
            bb = BlockBasedOptions(); bb.set_block_size(bs); o.set_block_based_table_factory(bb)
            return Rdict(path, o)
        def sst(path, dst):
            os.makedirs(dst, exist_ok=True)
            for f in sorted(glob.glob(os.path.join(path, "*.sst"))): shutil.copy(f, dst)
        r2 = rng("rocks-base"); t = tempfile.mkdtemp(prefix="rdb")
        p = os.path.join(t, "b"); d = rdb(p)
        for i in range(N): d[key(i)] = val(r2)
        d.compact_range(None, None); d.close(); sst(p, rf.rev("r0")); rf.record("r0", "base", 0)
        r2 = rng("rocks-mut")
        p2 = os.path.join(t, "u"); shutil.copytree(p, p2); d = rdb(p2); d[key(100000)] = val(r2); d.compact_range(None, None); d.close(); sst(p2, rf.rev("m01-update-one-compact")); rf.record("m01-update-one-compact", "one value changed + compaction", VAL)
        p3 = os.path.join(t, "a"); shutil.copytree(p, p3); d = rdb(p3)
        for i in range(N, N + 2000): d[key(i)] = val(r2)
        d.compact_range(None, None); d.close(); sst(p3, rf.rev("m02-append-compact")); rf.record("m02-append-compact", "append 2000 sorted keys + compaction", 2000 * (VAL + 13))
        p4 = os.path.join(t, "z"); d = rdb(p4, comp=DBCompressionType.none()); r3 = rng("rocks-base")
        for i in range(N): d[key(i)] = val(r3)
        d.compact_range(None, None); d.close(); sst(p4, rf.rev("m03-no-compression")); rf.record("m03-no-compression", "same contents, snappy -> none", 0)
        p5 = os.path.join(t, "k"); d = rdb(p5, bs=16384); r3 = rng("rocks-base")
        for i in range(N): d[key(i)] = val(r3)
        d.compact_range(None, None); d.close(); sst(p5, rf.rev("m04-block-16k")); rf.record("m04-block-16k", "same contents, block_size 4096->16384", 0)
        p6 = os.path.join(t, "r"); d = rdb(p6); r3 = rng("rocks-base")
        kv = [(key(i), val(r3)) for i in range(N)]
        kv.insert(100000, (b"key-%09d.x" % 100000, val(r2)))
        for k, v in sorted(kv): d[k] = v
        d.compact_range(None, None); d.close(); sst(p6, rf.rev("m05-rebuild-insert-one")); rf.record("m05-rebuild-insert-one", "full rebuild with one key inserted", VAL + 15)
        shutil.rmtree(t); rf.close()
    except Exception as e:
        print("rocksdb lane skipped:", repr(e), file=sys.stderr)


main()
