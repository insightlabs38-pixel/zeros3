"""Build and verify SQLite, ext4, tensor and event histories from frozen inputs."""
import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import struct
import subprocess
import tarfile
import tempfile


EPOCH = "1700000000"
BLK = 4096
DISK = 16 << 20
FILESYSTEM_UUID = "323a006f-2325-4767-9885-c14000000001"
HASH_SEED = "323a006f-2325-4767-9885-c14000000002"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def sources(archive):
    result = {}
    with tarfile.open(archive) as tar:
        for member in tar:
            if not member.isfile() or not member.name.endswith((".js", ".ts", ".json", ".md")):
                continue
            with tar.extractfile(member) as data:
                result[member.name] = data.read(32768)
    return dict(sorted(result.items()))


def query(path, *, replay_wal=False):
    options = "?mode=ro" if replay_wal else "?mode=ro&immutable=1"
    with closing(sqlite3.connect(path.resolve().as_uri() + options, uri=True)) as db:
        if db.execute("pragma integrity_check").fetchone() != ("ok",):
            raise ValueError("SQLite integrity check failed")
        curr = hashlib.sha256()
        n = 0
        for key, filename, payload in db.execute("select id, name, content from assets order by id"):
            curr.update(str(key).encode()+b"\0"+filename.encode()+b"\0"+hashlib.sha256(payload).digest())
            n += 1
        schema = db.execute("select sql from sqlite_master where name='assets'").fetchone()[0]
    return {"rows": n, "query_sha256": curr.hexdigest(), "schema_sha256": sha(schema.encode())}


def sqlite_history(root, app, corpora):
    live = root / "live.sqlite"
    conn = sqlite3.connect(live)
    conn.execute("pragma page_size=4096")
    conn.execute("pragma journal_mode=wal")
    conn.execute("pragma wal_autocheckpoint=0")
    conn.execute("create table assets(id integer primary key,name text not null unique, content blob not null)")
    case = []
    for revision, files in enumerate(corpora):
        keys = {name: (int(sha(name.encode())[:12], 16), data) for name, data in files.items()}
        if revision:
            stale = [name for (name,) in conn.execute("select name from assets") if name not in keys]
            conn.executemany("delete from assets where name=?", [(name,) for name in stale])
        if revision == 2:
            conn.execute("alter table assets add column flags integer not null default 0")
            conn.execute("create index assets_bytes on assets(length(content),id)")
        conn.executemany("insert into assets(id,name,content) values(?,?,?) "
                         "on conflict(id) do update set content=excluded.content where content!=excluded.content",
                         [(key, name, data) for name, (key, data) in sorted(keys.items())])
        conn.commit()
        if revision == 1:
            stage = app / "wal"
            stage.mkdir(parents=True)
            dbfile = stage / "snapshot.sqlite"
            wal = stage / "snapshot.sqlite-wal"
            shutil.copyfile(live, dbfile)
            shutil.copyfile(live.with_name("live.sqlite-wal"), wal)
            wal_pair = {"database_sha256": digest(dbfile), "wal_sha256": digest(wal), **query(dbfile, replay_wal=True)}
        conn.execute("pragma wal_checkpoint(truncate)").fetchone()
        if revision == 2:
            conn.execute("vacuum")
            conn.execute("pragma wal_checkpoint(truncate)").fetchone()
        target = root / "sqlite" / str(revision) / "snapshot.sqlite"
        target.parent.mkdir(parents=True)
        shutil.copyfile(live, target)
        case.append({"revision": revision, "sha256": digest(target), **query(target)})
    conn.close()
    live.unlink()
    if {k: v for k, v in wal_pair.items() if k.endswith("query_sha256") or k == "rows"} != {
            k: v for k, v in case[1].items() if k.endswith("query_sha256") or k == "rows"}:
        raise ValueError("checkpoint changed the visible SQLite rows")
    return {"versions": case, "wal": wal_pair, "sqlite_version": sqlite3.sqlite_version}


def fsenv(tools):
    lib = tools / "usr/lib/x86_64-linux-gnu"
    config = tools / "etc/mke2fs.conf"
    return {**os.environ, "LD_LIBRARY_PATH": str(lib), "MKE2FS_CONFIG": str(config),
            "E2FSPROGS_FAKE_TIME": EPOCH, "SOURCE_DATE_EPOCH": EPOCH}


def fscommand(tools, command, *args):
    result = subprocess.run([tools/"usr/sbin"/command, *args], env=fsenv(tools),
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=40)
    if result.returncode:
        raise ValueError(f"{command}: {result.stdout}")
    return result.stdout


def sparse_copy(source, target, offset=0):
    with source.open("rb") as src, target.open("wb") as dst:
        dst.seek(offset)
        while data := src.read(BLK):
            if any(data):
                dst.write(data)
            else:
                dst.seek(len(data), os.SEEK_CUR)
        dst.truncate(offset + source.stat().st_size)


def inspect_fs(source, offset, files, app, tools):
    with tempfile.TemporaryDirectory(dir=app) as temp:
        work = Path(temp)
        image = source
        if offset:
            image = work / "unshifted.img"
            with source.open("rb") as src, image.open("wb") as out:
                src.seek(offset)
                shutil.copyfileobj(src, out, length=1 << 20)
        fscommand(tools, "e2fsck", "-f", "-n", str(image))
        for key, proof in files.items():
            restored = work / key
            fscommand(tools, "debugfs", "-R", f'dump /{key} "{restored}"', str(image))
            if digest(restored) != proof:
                raise ValueError(f"ext4 consumer differed at {key}")
    return len(files)


def volume_history(root, app, corpora, tools):
    result = []
    for revision, files in enumerate(corpora):
        with tempfile.TemporaryDirectory(dir=app) as temp:
            work = Path(temp)
            tree = work / "tree"
            tree.mkdir()
            checks = {}
            for name, data in list(files.items())[::2 if revision < 2 else 3]:
                key = sha(name.encode())[:16] + ".bin"
                path = tree / key
                path.write_bytes(data)
                os.utime(path, (int(EPOCH), int(EPOCH)))
                checks[key] = sha(data)
            os.utime(tree, (int(EPOCH), int(EPOCH)))
            raw = work / "scratch.img"
            with raw.open("wb") as f:
                f.truncate(DISK)
            fscommand(tools, "mke2fs", "-F", "-q", "-t", "ext4", "-b", str(BLK), "-U", FILESYSTEM_UUID,
                      "-O", "^has_journal", "-E", f"hash_seed={HASH_SEED},lazy_itable_init=0,lazy_journal_init=0",
                      "-d", str(tree), str(raw))
            dst = root / "volume" / str(revision) / "volume.raw"
            dst.parent.mkdir(parents=True)
            offset = 65536 if revision == 2 else 0
            sparse_copy(raw, dst, offset)
            sample = dict(list(sorted(checks.items()))[::max(1, len(checks)//8)])
            inspected = inspect_fs(dst, offset, sample, app, tools)
            result.append({"revision": revision, "offset": offset, "sha256": digest(dst),
                           "files": len(checks), "sample": sample, "inspected": inspected})
            compressed = root / "volume-compressed" / str(revision) / "volume.zst"
            compressed.parent.mkdir(parents=True)
            subprocess.run(["zstd", "--quiet", "-9", str(dst), "-o", str(compressed)], check=True, timeout=30)
    return result


def volume_updates(root, app, sources, tools):
    payloads = [p.read_bytes() for p in sources]
    result = []
    with tempfile.TemporaryDirectory(dir=app) as temp:
        work = Path(temp)
        tree = work/"tree"
        tree.mkdir()
        # Store files from a separate published Python package, plus data
        # that receives a fixed edit in the first live-filesystem update.
        prev = {"source.tar.gz": payloads[0], "mutable.raw": bytes(range(256)) * 2048}
        for key, data in prev.items():
            (tree/key).write_bytes(data)
            os.utime(tree/key, (int(EPOCH), int(EPOCH)))
        os.utime(tree, (int(EPOCH), int(EPOCH)))
        raw = work/"scratch.img"
        with raw.open("wb") as f:
            f.truncate(DISK)
        fscommand(tools, "mke2fs", "-F", "-q", "-t", "ext4", "-b", str(BLK), "-U", FILESYSTEM_UUID,
                  "-O", "^has_journal", "-E", f"hash_seed={HASH_SEED},lazy_itable_init=0,lazy_journal_init=0",
                  "-d", str(tree), str(raw))
        for revision in range(3):
            if revision == 1:
                before = prev["mutable.raw"]
                prev["mutable.raw"] = before[:75321]+payloads[1][:32768]+before[108089:]
            elif revision == 2:
                prev["source.tar.gz"] = payloads[1]
            if revision:
                name = "mutable.raw" if revision == 1 else "source.tar.gz"
                local = work/name
                local.write_bytes(prev[name])
                fscommand(tools, "debugfs", "-w", "-R", f"rm /{name}", str(raw))
                fscommand(tools, "debugfs", "-w", "-R", f'write "{local}" /{name}', str(raw))
                fscommand(tools, "debugfs", "-w", "-R", f"set_inode_field /{name} mtime @{EPOCH}", str(raw))
                fscommand(tools, "debugfs", "-w", "-R", f"set_inode_field /{name} ctime @{EPOCH}", str(raw))
                fscommand(tools, "debugfs", "-w", "-R", f"set_inode_field /{name} atime @{EPOCH}", str(raw))
            target = root/str(revision)/"volume.raw"
            target.parent.mkdir(parents=True)
            offset = 131072 if revision == 2 else 0
            sparse_copy(raw, target, offset)
            expected = {key: sha(data) for key, data in sorted(prev.items())}
            inspected = inspect_fs(target, offset, expected, app, tools)
            result.append({"revision": revision, "offset": offset, "sha256": digest(target),
                           "files": len(expected), "sample": expected, "inspected": inspected})
    return result


def tensors(root):
    model = []
    for revision in range(3):
        header, data = {}, bytearray()
        for dtype, width in (("F32", 4), ("F16", 2), ("BF16", 2), ("I8", 1)):
            name = dtype.lower()
            start = len(data)
            for i in range(1 << 17):
                v = (i % 2048 - 1024) / 256
                if revision and i % 6151 == 0:
                    v += revision / 16
                if dtype == "F32":
                    packed = struct.pack("<f", v)
                elif dtype == "F16":
                    packed = struct.pack("<e", v)
                elif dtype == "BF16":
                    packed = struct.pack("<f", v)[2:]
                else:
                    packed = bytes([int(v*8) & 255])
                data.extend(packed)
            if len(data)-start != width*(1 << 17):
                raise ValueError("tensor dtype width differs")
            header[name] = {"dtype": dtype, "shape": [1 << 17], "data_offsets": [start, len(data)]}
        blob = json.dumps(header, separators=(",", ":")).encode()
        blob += b" " * (-len(blob)%8)
        path = root / "tensor-dtypes" / str(revision) / "weights.safetensors"
        path.parent.mkdir(parents=True)
        path.write_bytes(struct.pack("<Q", len(blob))+blob+data)
        model.append({"revision": revision, "dtypes": header, "sha256": digest(path)})
    return model


def populate(args):
    args.root.mkdir(parents=True)
    args.app.mkdir(parents=True)
    corpora = [sources(args.c12/"typescript-tar"/str(i)/"package.tar") for i in range(3)]
    result = {"sqlite": sqlite_history(args.root, args.app, corpora),
              "volume": volume_history(args.root, args.app, corpora, args.tools),
              "volume-incremental": volume_updates(args.root/"volume-incremental", args.app,
                  [args.c5/"httpx"/str(r)/"source.tar.gz" for r in (0, 1)], args.tools),
              "model": tensors(args.root)}
    for revision in range(3):
        target = args.root/"events"/str(revision)/"events.jsonl"
        target.parent.mkdir(parents=True)
        shutil.copyfile(args.c12/"events"/str(revision)/"events.jsonl", target)
        for family, name in (("mobilenet", "weights.pth"), ("parquet", "snapshot.parquet"),
                             ("sst", "snapshot.sst"), ("entropy", "payload.bin")):
            source = args.c5/family/str(revision)/name
            if source.is_file():
                target = args.root/family/str(revision)/name
                target.parent.mkdir(parents=True)
                shutil.copyfile(source, target)
    paths = sorted(p for p in args.root.rglob("*") if p.is_file())
    result["files"] = [{"path": str(p.relative_to(args.root)), "sha256": digest(p),
                        "logical_bytes": p.stat().st_size, "allocated_bytes": p.stat().st_blocks*512}
                       for p in paths]
    if any(r["logical_bytes"] > 32 << 20 for r in result["files"]):
        raise ValueError("dataset exceeded its budget")
    args.results.write_text(json.dumps(result, sort_keys=True, indent=2)+"\n")


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for key in ("root", "c12", "c5", "app", "tools", "results"):
        p.add_argument("--"+key, type=lambda name: Path(name).resolve(), required=True)
    populate(p.parse_args())
