"""Measure explicit base preparation, shaped transfer and exact receiver storage."""
import argparse
import json
import os
from pathlib import Path
import shutil
from types import SimpleNamespace
import sys

harness = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(harness/"c3"))
from transfer import BUCKET, KEY, SECRET, OPTIMIZE, file_digest, put, run, serve, stage, trial, verified

LEAN = ("authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest",
        "-max-depth", "2", "-max-bases", "1", "-min-saving", ".10", "-locality", "0",
        "-record-groups", "-canonical", "-codec-replay", "-no-rep", "-retain-delta-plans")
SHAPES = {"loopback": (0, 0), "fast-lan": (80<<20, .002),
          "wan": (8<<20, .035), "constrained": (1<<20, .080)}


def inputs(args, family):
    if family == "typescript":
        return tuple(args.downloads/f"typescript-5.6.{i}.tgz" for i in (2, 3))
    if family in ("shift", "entropy"):
        return tuple(args.c1/family/str(i)/"payload.bin" for i in (0, 1))
    name = {"sqlite": "snapshot.sqlite", "tensor-dtypes": "weights.safetensors", "mobilenet": "weights.pth"}[family]
    return tuple(args.c14/family/str(i)/name for i in (0, 1))


def campaign(args, env, root, family, emit):
    root.mkdir()
    prior, target = inputs(args, family)
    paths = {"prior": prior, "target": target}
    proof = {key: {"sha256": file_digest(p), "bytes": p.stat().st_size} for key, p in paths.items()}
    emit({"family": family, "stage": "fixture", **proof})
    seed = root/"ordinary-source"
    with serve(args.source_binary, seed, root/"seed.log", env) as (_, source, _):
        source.create_bucket(Bucket=BUCKET)
        put(source, "prior", prior)
        put(source, "target", target, {"scene": "c3"})
    methods = args.preparations if family == "typescript" else ("lean",)
    for method in methods:
        prepared = root/method
        prepared.mkdir()
        store = prepared/"source"
        shutil.copytree(seed, store)
        if method != "none":
            flags = OPTIMIZE if method == "full" else LEAN
            stats = run(args.source_binary, {**env, "TMPDIR": str(prepared)},
                        [*flags, "-store", str(store)], 115)
            report = json.loads(stats.pop("stdout"))
            emit({"family": family, "preparation": method, "stage": "source-optimize",
                  "report": report, **stats})
        with serve(args.source_binary, store, prepared/"source.log", env) as source:
            for key, path in paths.items():
                verified(source[1], key, path, {"scene": "c3"} if key == "target" else {})
            states = args.states if family == "typescript" else ("empty", "different-key")
            if method == "none":
                states = ("different-key",)
            for state in states:
                staging = prepared/state
                staging.mkdir()
                stage(args, env, staging, source, state, prior, target)
            shapes = args.shapes if family == "typescript" and method != "none" else ("loopback",)
            for i, shape in enumerate(shapes):
                rate, delay = SHAPES[shape]
                options = SimpleNamespace(**vars(args), request_delay=delay, rate_bytes=rate,
                                          model_gzip=False, finalize=True)
                lanes = args.lanes
                for state in states:
                    for rep in range(args.repeats):
                        for lane in lanes[::1 if (i+rep)%2 == 0 else -1]:
                            if lane == "closure-derived" and state == "empty":
                                continue
                            work = prepared/f"{shape}-{state}-{rep}-{lane}"
                            work.mkdir()
                            shutil.copytree(prepared/state/"destination", work/"destination")
                            item = trial(options, env, work, source, family, state, lane, prior, target)
                            emit({"stage": "transfer", "preparation": method, "shape": shape,
                                  "repeat": rep, **item})


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("binary", "downloads", "c1", "c14", "work", "results"):
        p.add_argument("--"+n, type=lambda name: Path(name).resolve(), required=True)
    p.add_argument("--receiver-binary", type=lambda name: Path(name).resolve())
    p.add_argument("--source-binary", type=lambda name: Path(name).resolve())
    p.add_argument("--families", nargs="+", choices=("typescript", "shift", "entropy", "sqlite", "tensor-dtypes", "mobilenet"),
                   default=("typescript",))
    p.add_argument("--preparations", nargs="+", choices=("full", "lean", "none"), default=("lean",))
    p.add_argument("--shapes", nargs="+", choices=tuple(SHAPES), default=("loopback",))
    p.add_argument("--states", nargs="+", choices=("empty", "same-key", "different-key", "same-authority", "different-authority"),
                   default=("same-key", "different-key"))
    p.add_argument("--lanes", nargs="+", choices=("s3", "replicate", "closure-cross", "closure-derived"),
                   default=("s3", "replicate", "closure-cross", "closure-derived"))
    p.add_argument("--repeats", type=int, default=1)
    args = p.parse_args()
    args.receiver_binary = args.receiver_binary or args.binary
    args.source_binary = args.source_binary or args.binary
    args.work.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for family in args.families:
            campaign(args, env, args.work/family, family, lambda row: print(json.dumps(row), file=out, flush=True))


if __name__ == "__main__":
    main()
