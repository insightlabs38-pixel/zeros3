"""Compare identical-pack retention, reconstruction layout and churn on materialized stores."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import sys
import tempfile

compet = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(compet / "c3"), str(compet / "c2"), str(compet / "c1")]
from transfer import BUCKET, KEY, SECRET, serve
from frontier import command, inventory
from local_s3 import measure
from corpus import blob

FINALIZE = (("gc", ["gc", "-apply", "-json"]),
            ("repack", ["repack", "-apply", "-json", "-max-live-percent", "100", "-recompress"]),
            ("compact", ["compact", "-json", "-pack-size-mib", "1"]),
            ("final-gc", ["gc", "-apply", "-json"]))
MIXED = ("coreutils", "httpx", "parquet", "sst", "tensor")
LAYOUTS = ("baseline", "keep", "recon", "block-local", "dictionary")


def resources(store):
    sizes = inventory(store)
    packs = sorted(store.rglob("*.pack"))
    return {"paths": sizes, "allocated": sum(sizes.values()),
            "pack_ids": [p.name for p in packs],
            "pack_bytes": sum(p.stat().st_size for p in packs),
            "pack_slack_bytes": sum(p.stat().st_blocks * 512 - p.stat().st_size for p in packs)}


def put(client, key, body):
    client.put_object(Bucket=BUCKET, Key=key, Body=body)


def exact(client, key, expected, span=None):
    req = {"Range": span} if span else {}
    obj = client.get_object(Bucket=BUCKET, Key=key, **req)
    with obj["Body"] as content:
        if hashlib.file_digest(content, "sha256").digest() != hashlib.sha256(expected).digest():
            raise ValueError(f"reconstruction mismatch: {key}")
    return len(expected)


def restored(binary, case, store, expected, env, work):
    records = [(key, data) for key, data in sorted(expected.items()) if len(data) >= 4096]
    with serve(binary, store, work / "service.log", env) as (_, client, _):
        if not records:
            raise ValueError("range profile requires at least one 4 KiB fixture")
        rng = random.Random(71)
        spans = []
        for i in range(128):
            key, data = records[i % len(records)]
            start = rng.randrange(len(data) - 4096 + 1)
            spans.append((key, data[start:start+4096], f"bytes={start}-{start+4095}"))
        lane = {}
        for name, width in (("fresh_process", 1), ("parallel_repeat", 4)):
            with ThreadPoolExecutor(max_workers=width) as pool:
                lane[name] = measure(pool, spans, lambda key, data, span: exact(client, key, data, span))
        for key, body in expected.items():
            exact(client, key, body)
        return {"case": case, "files_verified": len(expected), "ranges": lane, **resources(store)}


def corpus(args, case):
    families = MIXED if case == "mixed" else (case,)
    items = {}
    for family in families:
        root = (args.bridge if family == "tensor" else args.corpus) / family
        for file in sorted(root.rglob("*")):
            if file.is_file():
                items[f"{family}/{file.relative_to(root).as_posix()}"] = file.read_bytes()
    return items


def lane_config(args, lane):
    binary = args.candidate if lane == "keep" else args.baseline
    extra = {"baseline": [], "keep": ["-keep-unchanged"], "recon": ["-layout", "recon"],
             "block-local": ["-record-blocks", "16"],
             "dictionary": ["-layout", "recon", "-record-blocks", "16"]}[lane]
    return binary, extra


def floor(args, case, work, env, emit):
    sample = corpus(args, case)
    seed = work / "seed"
    with serve(args.baseline, seed, work / "service.log", env) as (_, client, _):
        client.create_bucket(Bucket=BUCKET)
        for key, data in sample.items():
            put(client, key, data)
    if case in (*MIXED, "mixed"):
        flags = ["authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest", "-max-depth", "2",
                 "-max-bases", "2", "-min-saving", ".10", "-record-groups", "-canonical", "-codec-replay", "-hard-codecs"]
        emit({"case": case, "lane": "seed", "stage": "optimize", **command(args.baseline, seed, flags, work)})
    for phase, flags in FINALIZE:
        emit({"case": case, "lane": "seed", "stage": phase, **command(args.baseline, seed, flags, work)})
    for position, lane in enumerate(args.lanes[::-1] if args.reverse else args.lanes):
        binary, extra = lane_config(args, lane)
        trial = work / lane
        shutil.copytree(seed, trial)
        try:
            result = command(binary, trial, [*FINALIZE[1][1], *extra], work)
            emit({"case": case, "lane": lane, "position": position, "stage": "repack", **result})
            emit({"case": case, "lane": lane, "stage": "verify", **command(binary, trial, ["verify", "-deep", "-json"], work)})
            emit({"lane": lane, "stage": "restore", **restored(binary, case, trial, sample, env, work)})
        finally:
            shutil.rmtree(trial)


def churn(args, work, env, emit):
    for lane in args.lanes:
        binary, extra = lane_config(args, lane)
        store = work / lane
        expected = {}
        for cycle in range(4):
            with serve(binary, store, work / "service.log", env) as (_, client, _):
                if cycle == 0:
                    client.create_bucket(Bucket=BUCKET)
                for i in range(10):
                    key = f"live/{i:03}"
                    if i in (cycle * 2, cycle * 2 + 1, cycle + 5):
                        body = blob(1000 + i, 262144)
                        data = body[:131072] + blob(88 + cycle, 8192) + body[139264:]
                        if i % 2 == 0:
                            data = (f"object {i}: common versioned configuration content\n".encode() * 4600) + data
                        put(client, key, data)
                        expected[key] = data
                if cycle > 0:
                    key = f"live/{cycle * 2 - 2:03}"
                    client.delete_object(Bucket=BUCKET, Key=key)
                    expected.pop(key, None)
            steps = (("prune", ["versions", "prune", "-bucket", BUCKET, "-keep-last", "1", "-apply", "-json"]),
                     *FINALIZE)
            for phase, flags in steps:
                if phase == "repack":
                    flags = [*flags, *extra, "-pack-size-mib", "2"]
                emit({"case": "churn", "lane": lane, "cycle": cycle, "stage": phase,
                      **command(binary, store, flags, work)})
            emit({"lane": lane, "cycle": cycle, "stage": "restore",
                  **restored(binary, "churn", store, expected, env, work)})
        emit({"case": "churn", "lane": lane, "stage": "verify",
              **command(binary, store, ["verify", "-deep", "-json"], work)})
        shutil.rmtree(store)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("baseline", "candidate", "corpus", "bridge", "scratch", "results"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--cases", nargs="+", choices=(*MIXED, "mixed", "mobilenet", "entropy", "churn"),
                        default=["mixed", "mobilenet", "entropy", "churn"])
    parser.add_argument("--lanes", nargs="+", choices=LAYOUTS, default=list(LAYOUTS))
    parser.add_argument("--reverse", action="store_true")
    args = parser.parse_args()
    for k, v in vars(args).items():
        if isinstance(v, Path):
            setattr(args, k, v.resolve())
    args.scratch.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as trace:
        for case in args.cases:
            with tempfile.TemporaryDirectory(dir=args.scratch) as tmp:
                def emit(row):
                    print(json.dumps(row), file=trace, flush=True)
                if case == "churn":
                    churn(args, Path(tmp), env, emit)
                else:
                    floor(args, case, Path(tmp), env, emit)


if __name__ == "__main__":
    main()
