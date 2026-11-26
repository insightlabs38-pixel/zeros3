"""Matched exact history retention, native consumers and portable recovery."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing
from hashlib import sha256
import json
import os
from pathlib import Path
import random
import shutil
import sqlite3
import sys
import tarfile
import tempfile
import zipfile

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/name) for name in ("c1", "c3", "c14", "c16")]
from fixtures import inspect_fs, query
from frontier import LEAN
from local_s3 import measure
from native_store import command
from transfer import BUCKET, KEY, SECRET, OPTIMIZE, allocated, delta, file_digest, put, run, serve, stats

FINISH = (("gc", "-apply", "-json"),
          ("repack", "-apply", "-max-live-percent", "100", "-recompress", "-json"),
          ("compact", "-pack-size-mib", "1", "-json"),
          ("gc", "-apply", "-json"), ("verify", "-deep", "-json"))
ARCHIVES = ("typescript", "urllib3")
EXTERNAL = (*ARCHIVES, "chinook")
FAMILIES = (*EXTERNAL, "sqlite", "parquet", "sst", "tensor-dtypes", "mobilenet",
            "volume", "volume-incremental", "entropy")


def external(args, work):
    paths = {name: {} for name in EXTERNAL}
    pins = json.loads(Path(__file__).with_name("inputs.json").read_text())
    for p in pins:
        family = p["family"]
        path = (args.downloads if family == "typescript" else args.inputs)/p["filename"]
        if path.stat().st_size != p["bytes"] or file_digest(path) != p["sha256"]:
            raise ValueError(f"source differs from pin: {path}")
        paths[family][f"{len(paths[family])}/{path.name}"] = path
    changed = work/"chinook-edited.sqlite"
    shutil.copyfile(next(iter(paths["chinook"].values())), changed)
    with closing(sqlite3.connect(changed)) as db:
        db.execute("update Track set Name=Name || ' (C19)' where TrackId between 1 and 12")
        db.commit()
        if db.total_changes != 12:
            raise ValueError("controlled edit did not match")
    paths["chinook"][f"2/{changed.name}"] = changed
    return paths


def consumer(family, key, path, work, proof, tools):
    revision = int(key.split("/")[0])
    if family.startswith("volume"):
        meta = proof[family][revision]
        return inspect_fs(path, meta["offset"], meta["sample"], work, tools)
    if family == "sqlite":
        return query(path)
    if family == "chinook":
        with closing(sqlite3.connect(path.as_uri()+"?mode=ro&immutable=1", uri=True)) as db:
            if db.execute("pragma integrity_check").fetchone() != ("ok",):
                raise ValueError("Chinook integrity check failed")
            return {table: sha256(repr(db.execute(
                f'SELECT * FROM "{table}" ORDER BY 1').fetchall()).encode()).hexdigest()
                for table in ("Track", "Artist", "Invoice")}
    if family in ARCHIVES:
        with tarfile.open(path, "r:gz") as tar:
            names = tar.getnames()
            name = next(n for n in names if n.endswith("/"+(
                "package.json" if family == "typescript" else "PKG-INFO")))
            payload = tar.extractfile(name).read()
            version = (json.loads(payload)["version"] if family == "typescript"
                       else next(l[9:].decode() for l in payload.splitlines() if l.startswith(b"Version: ")))
            if version not in key:
                raise ValueError(f"{family} package version differs")
            return {"members": len(names), "version": version}
    if family == "parquet":
        import pyarrow.parquet as pq
        table = pq.read_table(path)
        return {"rows": len(table), "value": table["reading"][revision*512].as_py(),
                "schema": str(table.schema)}
    if family == "sst":
        from rocksdict import Options, Rdict
        with tempfile.TemporaryDirectory(dir=work) as temp:
            db = Rdict(temp, Options(raw_mode=True))
            try:
                db.ingest_external_file([str(path)])
                h = sha256()
                count = 0
                for k, v in db.items():
                    h.update(k+b"\0"+v)
                    count += 1
                return {"rows": count, "query_sha256": h.hexdigest()}
            finally:
                db.close()
    if family == "tensor-dtypes":
        with path.open("rb") as f:
            n = int.from_bytes(f.read(8), "little")
            if n > 1<<20:
                raise ValueError("oversize tensor header")
            info = json.loads(f.read(n))
        tensors = {k: v for k, v in info.items() if k != "__metadata__"}
        cursor = 0
        for begin, end in sorted(v["data_offsets"] for v in tensors.values()):
            if begin != cursor or end < begin:
                raise ValueError("invalid tensor offsets")
            cursor = end
        if cursor+n+8 != path.stat().st_size:
            raise ValueError("incomplete tensor data")
        return {"tensors": len(tensors), "dtypes": sorted(set(v["dtype"] for v in tensors.values()))}
    if family == "mobilenet" and zipfile.is_zipfile(path):
        with zipfile.ZipFile(path) as archive:
            if archive.testzip():
                raise ValueError("model bad ZIP member")
            return {"zip_members": len(archive.namelist())}
    return "sha256-only"


def exact(args, family, paths, expected, work, s3, bucket, proof):
    verified = {}
    for key, path in paths.items():
        target = work/"restore"
        with s3.get_object(Bucket=bucket, Key=key)["Body"] as src, target.open("wb") as out:
            shutil.copyfileobj(src, out, length=1<<20)
        if file_digest(target) != expected[key]["sha256"]:
            raise ValueError(f"restored bytes differ: {family}/{key}")
        result = consumer(family, key, target, work, proof, args.fs_tools)
        if result != expected[key]["consumer"]:
            raise ValueError(f"consumer differs: {family}/{key}")
        target.unlink()
        verified[key] = result
    return verified


def read_cost(s3, pid, paths):
    data = {k: p.read_bytes() for k, p in paths.items()}
    rows = []
    for n, count in ((4096, 72), (0, 12)):
        rng = random.Random(19073)
        items = []
        for i in range(count):
            key = list(data)[i % len(data)]
            start = rng.randrange(len(data[key])-n+1) if n else 0
            payload = data[key][start:start+n] if n else data[key]
            items.append((key, start, sha256(payload).digest()))
        def get(key, start, want):
            opts = {"Range": f"bytes={start}-{start+n-1}"} if n else {}
            with s3.get_object(Bucket=BUCKET, Key=key, **opts)["Body"] as body:
                got = body.read()
            if sha256(got).digest() != want:
                raise ValueError("signed read checksum differs")
            return len(got)
        before = stats(pid)
        with ThreadPoolExecutor(max_workers=1) as pool:
            timing = measure(pool, items, get)
        rows.append({"range_bytes": n, "server": delta(before, stats(pid)), **timing})
    return rows


def optimize(binary, env, store, family, lane):
    if lane == "cdc":
        return None
    if family in ARCHIVES:
        flags = OPTIMIZE if lane == "c9" else (*LEAN, "-fast-reencode", "-retain-canonical-plans")
    else:
        flags = ("authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest",
                 "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10", "-record-groups")
        if family.startswith("volume") and lane == "integrated":
            flags += ("-sparse-files",)
    return command(binary, env, store, *flags)


def slim(data):
    data.pop("stdout", None)
    return data


def trial(args, env, work, family, lane, paths, expected, proof, pair):
    work.mkdir()
    store = work/"store"
    binary = args.c9 if lane == "c9" else args.integrated
    with serve(binary, store, work/"seed.log", env) as (_, api, pid):
        api.create_bucket(Bucket=BUCKET)
        before = stats(pid)
        with ThreadPoolExecutor(max_workers=1) as pool:
            ingest = measure(pool, list(paths.items()), lambda k, p: (put(api, k, p), p.stat().st_size)[1])
        ingest["server"] = delta(before, stats(pid))
    command(binary, env, store, "compact", "-compression", "off", "-pack-size-mib", "2", "-json")
    analysis = optimize(binary, env, store, family, lane)
    final = [command(binary, env, store, *opts) for opts in FINISH]
    physical = allocated(store)
    snapshot = None
    flags = ("-representations", "prefer")
    active = {**env, "ZEROS3_GZIP_SEQUENTIAL": "1"} if lane == "integrated" and family in ARCHIVES else env
    with serve(binary, store, work/"service.log", active, *flags) as (port, api, pid):
        reads = read_cost(api, pid, paths)
        verified = exact(args, family, paths, expected, work, api, BUCKET, proof)
        if lane == "integrated" and pair == 0:
            endpoint = f"http://127.0.0.1:{port}"
            snapshot = json.loads(run(binary, env, ["snapshot", "create", "-endpoint", endpoint,
                                                   "-json", f"s3://{BUCKET}"])["stdout"])
    portable = None
    if snapshot:
        identifier = snapshot["snapshot_id"]
        bundle = work/"portable.zs3b"
        archive = command(binary, env, store, "bundle", "export", "-snapshot", identifier,
                          "-out", str(bundle), "-json")
        recovered = work/"recovered"
        imported = command(args.c9, env, recovered, "bundle", "import", "-in", str(bundle), "-json")
        with serve(args.c9, recovered, work/"import.log", env) as (port, api, _):
            api.create_bucket(Bucket="recovered")
            restored = slim(run(args.c9, env, ["snapshot", "restore", "-endpoint", f"http://127.0.0.1:{port}",
                                                identifier, "s3://recovered"]))
            exact(args, family, paths, expected, work, api, "recovered", proof)
        portable = {"bundle_bytes": bundle.stat().st_size, "sha256": file_digest(bundle),
                    "export": archive, "import_c9": imported, "restore_c9": restored}
    prune = None
    if family.startswith("volume"):
        oldest = next(iter(paths))
        with serve(binary, store, work/"prune.log", env) as (port, api, _):
            if snapshot:
                run(binary, env, ["snapshot", "delete", "-endpoint", f"http://127.0.0.1:{port}",
                                  snapshot["snapshot_id"]])
            api.delete_object(Bucket=BUCKET, Key=oldest)
        pruning = command(binary, env, store, "versions", "prune", "-bucket", BUCKET,
                          "-key", oldest, "-keep-last", "0", "-apply", "-json")
        pruned = [command(binary, env, store, *opts) for opts in FINISH]
        retained = {k: p for k, p in paths.items() if k != oldest}
        amount = allocated(store)
        with serve(binary, store, work/"pruned.log", env) as (_, api, _):
            exact(args, family, retained, expected, work, api, BUCKET, proof)
        prune = {"allocated": amount, "decision": pruning, "maintenance": pruned}
    return {"family": family, "pair": pair, "lane": lane, "ingest": ingest, "optimize": analysis,
            "allocated": physical, "maintenance": final, "reads": reads, "verified": verified,
            "portable": portable, "prune": prune}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("c9", "integrated", "inputs", "downloads", "corpus", "manifest", "fs-tools", "work", "results"):
        parser.add_argument("--"+name, type=lambda x: Path(x).resolve(), required=True)
    parser.add_argument("--families", nargs="+", choices=FAMILIES, default=FAMILIES)
    parser.add_argument("--pairs", type=int, default=2)
    args = parser.parse_args()
    args.work.mkdir(parents=True)
    externals = external(args, args.work)
    proof = json.loads(args.manifest.read_text())
    records = {p["path"]: p for p in proof["files"]}
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for family in args.families:
            paths = externals.get(family)
            if paths is None:
                paths = {k.removeprefix(family+"/"): args.corpus/k for k in sorted(records)
                         if k.startswith(family+"/")}
            expected = {}
            for key, path in paths.items():
                digest = file_digest(path)
                if family not in EXTERNAL and digest != records[f"{family}/{key}"]["sha256"]:
                    raise ValueError(f"reused source differs: {path}")
                expected[key] = {"sha256": digest, "bytes": path.stat().st_size,
                                 "consumer": consumer(family, key, path, args.work, proof, args.fs_tools)}
            print(json.dumps({"family": family, "fixture": expected}), file=out, flush=True)
            for pair in range(args.pairs):
                lanes = ("cdc", "c9", "integrated")
                for lane in lanes[::1 if pair % 2 == 0 else -1]:
                    result = trial(args, env, args.work/f"{family}-{pair}-{lane}",
                                   family, lane, paths, expected, proof, pair)
                    print(json.dumps(result), file=out, flush=True)


if __name__ == "__main__":
    main()
