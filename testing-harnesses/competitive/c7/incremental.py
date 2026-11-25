"""Measure bounded base discovery, refresh, invalidation and restore."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import sys
import tempfile

comp = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(comp / "c3"), str(comp / "c2")]
from transfer import BUCKET, KEY, SECRET, materialize, serve
from frontier import command, inventory

OPT = ["authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest",
       "-min-saving", ".02", "-max-depth", "2", "-max-bases", "2", "-discovery", "sketch", "-no-rep"]
FINISH = (("gc", ["gc", "-apply", "-json"]),
          ("repack", ["repack", "-apply", "-json", "-recompress", "-max-live-percent", "100"]),
          ("compact", ["compact", "-json", "-pack-size-mib", "1"]),
          ("final-gc", ["gc", "-apply", "-json"]))


def input_rows(args, case, scratch):
    if case == "source":
        source = [(materialize(args.corpus / "coreutils" / str(i), scratch / f"{i}.tar")).read_bytes()
                  for i in range(3)]
    elif case == "httpx":
        source = [(args.corpus / "httpx" / str(i) / "source.tar.gz").read_bytes() for i in range(2)]
    else:
        source = [random.Random(642).randbytes(810000)]
        for i in range(1, 8):
            prior = source[-1]
            point = 25000 + i * 83733
            source.append(prior[:point] + random.Random(i).randbytes(14000) + prior[point+6500:])
    return {f"history/{i:03}": data for i, data in enumerate(source)}


def write(binary, store, log, env, values, initialize=False):
    with serve(binary, store, log, env) as (_, api, _):
        if initialize:
            api.create_bucket(Bucket=BUCKET)
        for key, body in values.items():
            api.put_object(Bucket=BUCKET, Key=key, Body=body)


def verify_reads(binary, store, log, env, values):
    with serve(binary, store, log, env) as (_, api, _):
        for key, expected in values.items():
            obj = api.get_object(Bucket=BUCKET, Key=key)
            with obj["Body"] as data:
                if hashlib.file_digest(data, "sha256").digest() != hashlib.sha256(expected).digest():
                    raise ValueError(f"exactness failure: {key}")
    return len(values)


def index_bytes(store):
    path = store / "similarity-index"
    return sum(inventory(path).values()) if path.exists() else 0


def run_case(args, env, work, case, emit):
    seed, log = work / "seed", work / "serve.log"
    revisions = input_rows(args, case, work)
    uid = next(reversed(revisions))
    new = revisions.pop(uid)
    noise = {f"unrelated/{i:05}": random.Random(10000+i).randbytes(4096) for i in range(args.noise)}
    write(args.baseline, seed, log, env, {**noise, **revisions}, True)

    def record(binary, store, phase, flags, lane):
        data = command(binary, store, flags, work)
        emit({"case": case, "lane": lane, "phase": phase, "index_allocated": index_bytes(store), **data})
        return data

    record(args.baseline, seed, "prior-optimize", OPT, "seed")
    for phase, flags in FINISH:
        record(args.baseline, seed, phase, flags, "seed")
    record(args.candidate, seed, "index-build", ["authority", "index-build", "-apply"], "seed")
    new_values = {"unrelated/new": random.Random(513).randbytes(4096), uid: new}
    write(args.candidate, seed, log, env, new_values)
    expected = {**noise, **revisions, **new_values}
    lanes = (("full", args.baseline, []), ("limited", args.baseline, ["-max-targets", "1"]),
             ("indexed", args.candidate, ["-max-targets", "1", "-indexed-discovery"]))
    for name, binary, options in lanes:
        trial = work / name
        shutil.copytree(seed, trial)
        try:
            record(binary, trial, "optimize-new", [*OPT, *options], name)
            for phase, flags in FINISH:
                record(binary, trial, phase, flags, name)
            record(binary, trial, "verify", ["verify", "-deep", "-json"], name)
            files = verify_reads(binary, trial, log, env, expected)
            allocated = sum(inventory(trial).values())
            emit({"case": case, "lane": name, "phase": "exact", "verified": files,
                  "retained_bytes_excluding_disposable_index": allocated - index_bytes(trial),
                  "total_allocated": allocated, "index_allocated": index_bytes(trial),
                  "latest_sha256": hashlib.sha256(new).hexdigest()})
            if name == "full" and case == "binary":
                record(binary, trial, "memo-first", [*OPT, "-reuse-analysis"], name)
                record(binary, trial, "memo-repeat", [*OPT, "-reuse-analysis"], name)
        finally:
            shutil.rmtree(trial)
    record(args.candidate, seed, "append-refresh", ["authority", "index-refresh", "-apply"], "index")
    if "unrelated/00000" in expected:
        with serve(args.candidate, seed, log, env) as (_, api, _):
            api.delete_object(Bucket=BUCKET, Key="unrelated/00000")
        record(args.candidate, seed, "prune", ["versions", "prune", "-apply", "-json",
                                              "-bucket", BUCKET, "-prefix", "unrelated/00000", "-keep-last", "0"], "index")
        record(args.candidate, seed, "delete-refresh", ["authority", "index-refresh", "-apply"], "index")
    record(args.candidate, seed, "integrity", ["authority", "index-status"], "index")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for arg in ("baseline", "candidate", "corpus", "scratch", "results"):
        p.add_argument("--" + arg, type=Path, required=True)
    p.add_argument("--cases", nargs="+", choices=("binary", "source", "httpx"), default=("binary", "source", "httpx"))
    p.add_argument("--noise", type=int, default=64)
    args = p.parse_args()
    if not 0 <= args.noise <= 10000:
        p.error("use between 0 and 10,000 small noise objects")
    for k, v in vars(args).items():
        if isinstance(v, Path):
            setattr(args, k, v.resolve())
    args.scratch.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as report:
        for case in args.cases:
            with tempfile.TemporaryDirectory(prefix=f"{case}-", dir=args.scratch) as temp:
                def emit(row):
                    print(json.dumps(row), file=report, flush=True)
                run_case(args, env, Path(temp), case, emit)


if __name__ == "__main__":
    main()
