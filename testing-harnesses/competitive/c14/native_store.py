"""Price sparse matching through the native optimizer and real restore consumers."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from hashlib import sha256
import json
import os
from pathlib import Path
import random
import shutil
import sys

from fixtures import BLK, digest, inspect_fs, query

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/"c1"), str(harness/"c3")]
from local_s3 import measure
from transfer import BUCKET, KEY, SECRET, allocated, delta, put, run, serve, stats


def command(binary, env, store, *flags):
    info = run(binary, env, [*flags, "-store", str(store)], 150)
    info["report"] = json.loads(info.pop("stdout"))
    return info


def fixtures(corpus, family):
    return {str(p.relative_to(corpus/family)): p for p in sorted((corpus/family).rglob("*")) if p.is_file()}


def seed(args, env, root, paths):
    root.mkdir()
    store = root/"store"
    with serve(args.binary, store, root/"service.log", env) as (_, s3, _):
        s3.create_bucket(Bucket=BUCKET)
        for key, path in paths.items():
            put(s3, key, path)
    command(args.binary, env, store, "compact", "-compression", "off", "-pack-size-mib", "2", "-json")
    return store


def observe(args, family, lane, root, store, paths, proof, reader, env):
    results, verified = [], []
    with serve(reader, store, root/f"server-{lane}.log", env) as (_, api, pid):
        for key, path in paths.items():
            revision = int(key.split("/")[0])
            payload = root/"verified"
            with api.get_object(Bucket=BUCKET, Key=key)["Body"] as src, payload.open("wb") as out:
                shutil.copyfileobj(src, out, length=1<<20)
            if digest(payload) != digest(path):
                raise ValueError(f"native object differs at {key}")
            if family.startswith("volume"):
                meta = proof[family][revision]
                verified.append({"revision": revision, "consumer_files":
                                 inspect_fs(payload, meta["offset"], meta["sample"], root, args.fs_tools)})
            else:
                expected = proof[family]["versions"][revision]
                state = query(payload)
                if any(value != expected[key] for key, value in state.items()):
                    raise ValueError("native SQLite consumer differs")
                verified.append({"revision": revision, **state})
            payload.unlink()
        if lane == "revert":
            return [], verified
        expected = {k: p.read_bytes() for k, p in paths.items()}
        keys = sorted(expected)
        for n, count in ((BLK, 72), (0, 12)):
            rng = random.Random(214)
            requests = []
            for i in range(count):
                key = keys[i % len(keys)]
                body = expected[key]
                start = rng.randrange(len(body)-n+1) if n else 0
                expect = sha256(body[start:start+n] if n else body).digest()
                requests.append((key, start, expect))

            def get(key, start, expect):
                headers = {"Range": f"bytes={start}-{start+n-1}"} if n else {}
                with api.get_object(Bucket=BUCKET, Key=key, **headers)["Body"] as src:
                    h = sha256()
                    size = 0
                    for chunk in iter(lambda: src.read(1<<20), b""):
                        h.update(chunk)
                        size += len(chunk)
                if h.digest() != expect:
                    raise ValueError(f"native signed GET differs at {key}")
                return size

            with ThreadPoolExecutor(max_workers=1) as workers:
                before = stats(pid)
                row = measure(workers, requests, get)
            results.append({"range_bytes": n, "server": delta(before, stats(pid)), **row})
    return results, verified


def trial(args, env, family, lane, pair, src, paths, proof):
    root = args.work/f"{family}-{pair}-{lane}"
    root.mkdir()
    store = root/"store"
    shutil.copytree(src, store)
    optimize = None
    if lane != "cdc":
        base = ["authority", "optimize", "-apply", "-profile", args.profile, "-order", "oldest",
                "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10", "-record-groups"]
        if lane == "sparse":
            base.append("-sparse-files")
        optimize = command(args.binary, env, store, *base)
    stages = [command(args.binary, env, store, *flags) for flags in (
        ("gc", "-apply", "-json"), ("repack", "-apply", "-max-live-percent", "100", "-recompress", "-json"),
        ("compact", "-pack-size-mib", "1", "-json"), ("gc", "-apply", "-json"),
        ("verify", "-deep", "-json"))]
    physical = allocated(store)
    reads, verified = observe(args, family, lane, root, store, paths, proof, args.binary, env)
    compatibility = None
    if lane == "sparse" and pair == 0 and optimize["report"]["applied"]:
        command(args.baseline, env, store, "verify", "-deep", "-json")
        _, old = observe(args, family, "revert", root, store, paths, proof, args.baseline, env)
        report = command(args.binary, env, store, "authority", "revert", "-all")
        command(args.binary, env, store, "gc", "-apply", "-json")
        command(args.binary, env, store, "verify", "-deep", "-json")
        _, recovered = observe(args, family, "revert", root, store, paths, proof, args.binary, env)
        compatibility = {"reader_from_c13": old, "revert": report["report"], "restored": recovered}
    return {"family": family, "profile": args.profile, "pair": pair, "lane": lane, "allocated": physical,
            "optimize": optimize, "reads": reads, "verified": verified, "stages": stages,
            "compatibility": compatibility}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("binary", "baseline", "corpus", "fixture_manifest", "work", "fs_tools", "results"):
        p.add_argument("--"+n.replace("_", "-"), type=lambda name: Path(name).resolve(), required=True)
    p.add_argument("--families", nargs="+", default=("sqlite", "volume", "volume-incremental"))
    p.add_argument("--profile", choices=("archive", "mixed"), default="archive")
    p.add_argument("--repeats", type=int, default=2)
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    proof = json.loads(args.fixture_manifest.read_text())
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for family in args.families:
            paths = fixtures(args.corpus, family)
            src = seed(args, env, args.work/f"{family}-seed", paths)
            for pair in range(args.repeats):
                methods = ("cdc", "generic", "sparse")
                for lane in methods[::1 if pair % 2 == 0 else -1]:
                    row = trial(args, env, family, lane, pair, src, paths, proof)
                    print(json.dumps(row), file=out, flush=True)


if __name__ == "__main__":
    main()
