"""Compare the native file-backed preset store with ordinary packing."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import subprocess
import sys

here = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(here/"c1"), str(here/"c3")]
from local_s3 import measure
from transfer import BUCKET, KEY, SECRET, allocated, delta, run, serve, stats


def load(args, scope):
    entries = {}
    for row in map(json.loads, args.manifest.read_text().splitlines()):
        if row["scope"] != scope:
            continue
        data = (args.corpus/scope/row["key"]).read_bytes()
        if len(data) != row["bytes"] or hashlib.sha256(data).hexdigest() != row["sha256"]:
            raise ValueError(f"fixture changed at {row['key']}")
        entries[row["key"]] = data
    return entries


def context(args, scope, entries):
    label = {"main": "config", "heldout": "httpx", "wide": "typescript"}[scope]
    size = 8192 if scope == "wide" else 4096
    samples = [v for k, v in sorted(entries.items()) if k.startswith(f"{label}/train/")]
    data = b"".join(samples[-16:])[-size:]
    path = args.work/f"{scope}.dictionary"
    path.write_bytes(data)
    return path


def command(binary, env, store, flags):
    info = run(binary, env, [*flags, "-store", str(store)], 150)
    info["report"] = json.loads(info.pop("stdout"))
    return info


def seed(args, env, scope, entries):
    root = args.work/f"{scope}-seed"
    root.mkdir()
    store = root/"store"
    with serve(args.binary, store, root/"server.log", env) as (_, api, _):
        api.create_bucket(Bucket=BUCKET)
        for key, data in sorted(entries.items()):
            api.put_object(Bucket=BUCKET, Key=key, Body=data)
    command(args.binary, env, store, ["compact", "-pack-size-mib", "2", "-compression", "off", "-json"])
    return store


def observe(api, entries, pid):
    rng = random.Random(13331)
    targets = [k for k in sorted(entries) if "/eval/" in k]
    requests = [(rng.choice(targets),) for _ in range(512)]

    def get(key):
        with api.get_object(Bucket=BUCKET, Key=key, Range="bytes=0-4095")["Body"] as body:
            got = body.read()
        if got != entries[key][:4096]:
            raise ValueError(f"preset range differs at {key}")
        return len(got)

    rows = []
    for width in (1, 8):
        with ThreadPoolExecutor(max_workers=width) as workers:
            for phase in ("first-pass", "same-request-repeat"):
                before = stats(pid)
                row = measure(workers, requests, get)
                rows.append({"width": width, "phase": phase, "read_server": delta(before, stats(pid)), **row})
    return rows


def trial(args, env, scope, lane, pair, src, prefix, entries):
    root = args.work/f"{scope}-{pair}-{lane}"
    root.mkdir()
    store = root/"store"
    shutil.copytree(src, store)
    options = ["repack", "-apply", "-recompress", "-max-live-percent", "100", "-pack-size-mib", "16", "-json"]
    if lane == "preset":
        options.extend(["-preset-dictionary", str(prefix)])
    packed = command(args.binary, env, store, options)
    reclaimed = command(args.binary, env, store, ["gc", "-apply", "-json"])
    command(args.binary, env, store, ["verify", "-deep", "-json"])
    features = json.loads((store/"FORMAT.json").read_text()).get("features", [])
    legacy = None
    feature = next((f for f in features if f in ("packpreset", "packlocaldict")), None)
    if feature:
        previous = subprocess.run([args.baseline, "verify", "-store", store], env=env,
                                  capture_output=True, text=True, timeout=6)
        legacy = {"status": previous.returncode, "detail": previous.stderr[-350:]}
        if not previous.returncode or feature not in previous.stderr:
            raise ValueError("older binary accepted a preset-only store")
    with serve(args.binary, store, root/"server.log", env) as (_, api, pid):
        reads = observe(api, entries, pid)
        for key, want in entries.items():
            with api.get_object(Bucket=BUCKET, Key=key)["Body"] as body:
                if body.read() != want:
                    raise ValueError(f"preset whole read differs at {key}")
    return {"scope": scope, "pair": pair, "lane": lane, "allocated": allocated(store),
            "objects": len(entries), "features": features, "legacy": legacy, "repack": packed,
            "gc_cpu": reclaimed["cpu_seconds"], "reads": reads}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("work", "corpus", "manifest", "binary", "baseline", "results"):
        p.add_argument("--"+n, type=lambda name: Path(name).resolve(), required=True)
    p.add_argument("--scopes", choices=("main", "heldout", "wide"), nargs="+", required=True)
    p.add_argument("--repeats", type=int, default=2)
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for scope in args.scopes:
            entries = load(args, scope)
            prefix = context(args, scope, entries)
            src = seed(args, env, scope, entries)
            for pair in range(args.repeats):
                order = ("control", "preset") if pair % 2 == 0 else ("preset", "control")
                records = []
                for lane in order:
                    row = trial(args, env, scope, lane, pair, src, prefix, entries)
                    records.append(row)
                    print(json.dumps(row), file=out, flush=True)
                if records[0]["repack"]["report"]["packs_selected"] != records[1]["repack"]["report"]["packs_selected"]:
                    raise ValueError("preset write trials used different input pack counts")


if __name__ == "__main__":
    main()
