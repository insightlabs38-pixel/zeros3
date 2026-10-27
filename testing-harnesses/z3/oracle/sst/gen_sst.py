"""Z3-30 LevelDB (plyvel / LevelDB 1.23) and RocksDB (rocksdict / RocksDB 9.8.4) producer driver.
Replays the operation logs and plan written by `z3sst gen-ops` against the real engine and snapshots
the live table files after every revision. usage: gen_sst.py leveldb|rocksdb OPS OUT WORK"""
import glob, hashlib, json, os, shutil, sys

producer, opsdir, outroot, workroot = sys.argv[1:5]
only = sys.argv[5] if len(sys.argv) > 5 else None  # run one family
FILE = 4 << 20
BLOCK = 4096
plan = json.load(open(os.path.join(opsdir, "plan.json")))


def read_ops(path):
    b = open(path, "rb").read()
    i, n = 0, len(b)

    def uv():
        nonlocal i
        v = s = 0
        while True:
            c = b[i]; i += 1
            v |= (c & 0x7F) << s
            if c < 0x80:
                return v
            s += 7
    while i < n:
        op = b[i]; i += 1
        kl = uv(); k = b[i:i + kl]; i += kl
        if op == 1:
            vl = uv(); v = b[i:i + vl]; i += vl
            yield 1, k, v
        else:
            yield 2, k, None


class LevelDB:
    ext = ".ldb"

    def __init__(self, path):
        import plyvel
        self.plyvel, self.path, self.db = plyvel, path, None
        self.codec = "snappy"
        self.info = {"producer": "LevelDB", "version": plyvel.__leveldb_version__, "binding": "plyvel " + plyvel.__version__,
                     "options": {"max_file_size": FILE, "write_buffer_size": "4 MiB (scaled with the table size)", "block_size": BLOCK, "compression": "snappy", "bloom_filter_bits": 10}}

    codecs = {"snappy": "snappy", "none": None}

    def open(self, scale=1.0):
        self.scale = scale
        self.db = self.plyvel.DB(self.path, create_if_missing=True, write_buffer_size=int((4 << 20) * scale), max_file_size=int(FILE * scale),
                                 block_size=BLOCK, compression=self.codec, bloom_filter_bits=10)

    def close(self):
        if self.db is not None:
            self.db.close(); self.db = None

    def apply(self, path, batch):
        wb = self.db.write_batch(); n = 0
        for op, k, v in read_ops(path):
            if op == 1: wb.put(k, v)
            else: wb.delete(k)
            n += 1
            if n % batch == 0:
                wb.write(); wb = self.db.write_batch()
        wb.write()

    def flush(self):
        self.close(); self.open(self.scale)  # recovery turns the log into a level-0 table

    def compact(self):
        # LevelDB's manual compaction only compacts the levels above the deepest populated one, and
        # level-0 tables written while it runs survive it: repeat until the database is settled
        for _ in range(4):
            self.db.compact_range()
            self.close(); self.open(self.scale)

    def autocompact(self):
        pass  # LevelDB compacts by itself: the snapshot step settles the database

    def settle(self):
        """close, reopen and close again so recovery writes the log into a table and obsolete files are purged"""
        self.close(); self.open(self.scale); self.close()

    def live(self):
        return sorted(os.path.basename(p) for p in glob.glob(os.path.join(self.path, "*.ldb")))


class RocksDB:
    ext = ".sst"

    def __init__(self, path):
        import rocksdict
        self.r, self.path, self.db = rocksdict, path, None
        self.codec = "snappy"
        self.codecs = {"snappy": "snappy", "none": "none", "lz4": "lz4", "zlib": "zlib", "zstd": "zstd"}
        self.info = {"producer": "RocksDB", "version": "9.8.4", "binding": "rocksdict 0.3.29",
                     "options": {"target_file_size_base": FILE, "block_size": BLOCK, "compression": "snappy", "bloom_filter_bits": 10,
                                 "format_version": "default (6)", "checksum": "default (xxh3)", "auto_compactions": False}}

    def open(self, scale=1.0):
        r = self.r
        o = r.Options(raw_mode=True); o.create_if_missing(True)
        o.set_write_buffer_size(32 << 20); o.set_target_file_size_base(int(FILE * scale))
        o.set_disable_auto_compactions(True); o.set_compression_type(getattr(r.DBCompressionType, self.codec)())
        o.set_max_background_jobs(1)
        bb = r.BlockBasedOptions(); bb.set_block_size(BLOCK); bb.set_bloom_filter(10.0, False)
        o.set_block_based_table_factory(bb)
        self.scale = scale
        self.db = r.Rdict(self.path, o)

    def close(self):
        if self.db is not None:
            self.db.close(); self.db = None

    def apply(self, path, batch):
        wb = self.r.WriteBatch(raw_mode=True); n = 0
        for op, k, v in read_ops(path):
            if op == 1: wb.put(k, v)
            else: wb.delete(k)
            n += 1
            if n % batch == 0:
                self.db.write(wb); wb = self.r.WriteBatch(raw_mode=True)
        self.db.write(wb)

    def flush(self):
        self.db.flush()

    def compact(self):
        self.db.flush()
        co = self.r.CompactOptions()
        co.set_bottommost_level_compaction(self.r.BottommostLevelCompaction.force())
        self.db.compact_range(None, None, co)

    def autocompact(self):
        self.compact()  # auto compactions are off in this harness: a full compaction stands in

    def settle(self):
        self.db.flush()

    def live(self):
        return sorted(os.path.basename(f["name"]) for f in self.db.live_files() if f["name"].endswith(".sst"))


def sha(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for c in iter(lambda: f.read(1 << 20), b""):
            h.update(c)
    return h.hexdigest()


Engine = LevelDB if producer == "leveldb" else RocksDB
for fam in plan["families"]:
    if only and fam["name"] != only:
        continue
    work = os.path.join(workroot, producer, fam["name"])
    shutil.rmtree(work, ignore_errors=True); os.makedirs(work)
    path, basepath = os.path.join(work, "db"), os.path.join(work, "base")
    eng = Engine(path)
    out = os.path.join(outroot, producer, fam["name"])
    shutil.rmtree(out, ignore_errors=True); os.makedirs(out)
    files = {}
    skip = False
    for st in fam["steps"]:
        c = st["c"]
        if skip and c != "newdb":
            continue
        if c == "newdb":
            eng.close(); shutil.rmtree(path, ignore_errors=True)
            eng.codec = "snappy"
            if "codec" in st:
                if st["codec"] not in eng.codecs:
                    skip = True
                    continue
                eng.codec = eng.codecs[st["codec"]] if producer == "rocksdb" else st["codec"] if st["codec"] != "none" else None
            skip = False
            eng.open(st.get("scale", 1.0))
        elif c == "apply":
            eng.apply(os.path.join(opsdir, st["ops"]), st["batch"])
        elif c == "flush":
            eng.flush()
        elif c == "compact":
            eng.compact()
        elif c == "autocompact":
            eng.autocompact()
        elif c == "reopen":
            eng.close(); eng.open(st["scale"])
        elif c == "savebase":
            eng.close(); shutil.rmtree(basepath, ignore_errors=True); shutil.copytree(path, basepath); eng.open()
        elif c == "frombase":
            eng.close(); shutil.rmtree(path, ignore_errors=True); shutil.copytree(basepath, path); eng.open()
        elif c == "snapshot":
            dst = os.path.join(out, st["rev"]); os.makedirs(dst)
            eng.settle()
            live = eng.live()
            eng.close()
            for f in live:
                shutil.copy(os.path.join(path, f), dst)
            if st.get("dir"):
                oth = os.path.join(dst, "_other"); os.makedirs(oth)
                for f in sorted(os.listdir(path)):
                    if f != "LOCK" and not f.endswith(eng.ext):
                        shutil.copy(os.path.join(path, f), oth)
            files[st["rev"]] = [{"name": f, "size": os.path.getsize(os.path.join(dst, f)), "sha256": sha(os.path.join(dst, f))} for f in sorted(os.listdir(dst)) if f.endswith(eng.ext)]
            eng.open(eng.scale)
        else:
            raise SystemExit("unknown step " + c)
    eng.close()
    meta = {"lane": "sst", "family": fam["name"], "producer": eng.info,
            "revisions": [dict(r, files=files.get(r["Rev"], [])) for r in fam["revs"]]}
    json.dump(meta, open(os.path.join(out, "meta.json"), "w"), indent=1)
    print(producer, fam["name"], "done", sum(len(v) for v in files.values()), "tables", flush=True)
