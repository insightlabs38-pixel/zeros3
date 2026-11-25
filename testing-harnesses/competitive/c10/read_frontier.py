"""Prepare immutable physical lanes, then replay exact HTTP reads against either binary."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import random
import sys

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness / "c3"), str(harness / "c2")]
from transfer import BUCKET, KEY, SECRET, allocated, file_digest, put, run, serve, stats, verified
from frontier import command
from local_s3 import measure

LANES = {"cdc": [], "canonical": ["-canonical", "-codec-replay", "-hard-codecs"],
         "deep": ["-canonical", "-codec-replay", "-hard-codecs", "-splice"]}

def items(args, family):
    return {str(p.relative_to(args.corpus / family)): p
            for p in sorted((args.corpus / family).rglob("*")) if p.is_file()}

def prepare(args, env, family, emit):
    files = items(args, family)
    if not files:
        raise ValueError(f"missing input: {family}")
    for lane in args.lanes:
        work = args.work / family / lane
        work.mkdir(parents=True)
        store = work / "store"
        with serve(args.baseline, store, work / "service.log", env) as (_, s3, _):
            s3.create_bucket(Bucket=BUCKET)
            for k, path in files.items():
                put(s3, k, path)
        stages = [("gc", ["gc", "-apply", "-json"]), ("repack", ["repack", "-apply", "-json", "-max-live-percent", "100", "-recompress"]),
                  ("compact", ["compact", "-pack-size-mib", "1", "-json"]), ("final-gc", ["gc", "-apply", "-json"])]
        if lane != "cdc":
            stages.insert(0, ("optimize", ["authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest",
                           "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10", "-record-groups", *LANES[lane]]))
        for name, flags in stages:
            emit({"family": family, "lane": lane, "stage": name, **command(args.baseline, store, flags, work)})
        emit({"family": family, "lane": lane, "stage": "verify", **command(args.baseline, store, ["verify", "-deep", "-json"], work)})
        with serve(args.baseline, store, work / "service.log", env) as (_, s3, _):
            for k, path in files.items():
                verified(s3, k, path)
        (work / "fixtures.json").write_text(json.dumps({k: file_digest(p) for k, p in files.items()}))

def replay(args, env, family, emit):
    files = items(args, family)
    for lane in args.lanes:
        work = args.work / family / lane
        expected = json.loads((work / "fixtures.json").read_text())
        if {k: file_digest(p) for k, p in files.items()} != expected:
            raise ValueError("fixture differs from the prepared lane")
        payload = {k: p.read_bytes() for k, p in files.items()}
        store = work / "store"
        for pair in range(args.repeats):
            builds = [("c9", args.baseline), ("c10", args.candidate)] if args.candidate else [("c9", args.baseline)]
            if pair % 2:
                builds.reverse()
            for build, binary in builds:
                for size in args.sizes:
                    for width in args.widths:
                        with serve(binary, store, work / "service.log", env) as (_, s3, pid):
                            rng = random.Random(10 + pair)
                            keys = list(payload)
                            refs = []
                            samples = args.full_samples if size == 0 and args.full_samples else args.samples
                            for i in range(max(len(keys), samples)):
                                key = keys[i % len(keys)]
                                data = payload[key]
                                n = min(size or len(data), len(data))
                                at = rng.randrange(len(data)-n+1)
                                refs.append((key, data[at:at+n], f"bytes={at}-{at+n-1}" if size else None))
                            def get(key, want, span):
                                opts = {"Range": span} if span else {}
                                with s3.get_object(Bucket=BUCKET, Key=key, **opts)["Body"] as response:
                                    got = hashlib.file_digest(response, "sha256").digest()
                                if got != hashlib.sha256(want).digest():
                                    raise ValueError(f"wrong bytes: {key}")
                                return len(want)
                            with ThreadPoolExecutor(max_workers=width) as pool:
                                first = stats(pid)
                                for mode in ("first-pass", "same-offset-repeat"):
                                    before = stats(pid)
                                    result = measure(pool, refs, get)
                                    end = stats(pid)
                                    emit({"family": family, "lane": lane, "stage": "read", "build": build,
                                          "pair": pair, "range_bytes": size, "width": width, "mode": mode,
                                          "allocated": allocated(store), "rss_at_start": first["process_peak_rss_bytes"],
                                          "rss_peak": end["process_peak_rss_bytes"],
                                          "server_cpu_seconds": end["cpu_seconds"]-before["cpu_seconds"],
                                          "process_read_bytes": end["logical_read_bytes"]-before["logical_read_bytes"],
                                          "disk_read_bytes": end["physical_read_bytes"]-before["physical_read_bytes"],
                                          **result})

def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("baseline", "candidate", "work", "corpus", "results"):
        p.add_argument("--" + name, type=Path, required=name != "candidate")
    p.add_argument("--prepare", action="store_true")
    p.add_argument("--families", nargs="+", required=True)
    p.add_argument("--lanes", nargs="+", choices=LANES, default=list(LANES))
    p.add_argument("--sizes", nargs="+", type=int, default=[4096, 65536, 1048576, 0])
    p.add_argument("--widths", nargs="+", type=int, default=[1, 4])
    p.add_argument("--samples", type=int, default=12)
    p.add_argument("--full-samples", type=int, help="override samples for whole-object reads")
    p.add_argument("--repeats", type=int, default=2)
    args = p.parse_args()
    for name, value in vars(args).items():
        if isinstance(value, Path):
            setattr(args, name, value.resolve())
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        def emit(row):
            print(json.dumps(row), file=out, flush=True)
        for family in args.families:
            (prepare if args.prepare else replay)(args, env, family, emit)

if __name__ == "__main__":
    main()
