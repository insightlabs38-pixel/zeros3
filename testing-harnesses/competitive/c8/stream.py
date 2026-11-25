"""Matched reconstruction and bounded foreground traffic against a frozen source."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import sys
import tempfile
import time
from types import SimpleNamespace

compet = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(compet / "c3"), str(compet / "c2"), str(compet / "c1")]
from local_s3 import measure
from transfer import (BUCKET, FAMILIES, KEY, OPTIMIZE, SECRET, file_digest,
                      materialize, put, run, serve, stage, trial, verified)

CASES = ("typescript", "sst", "parquet", "source", "shift", "entropy")
PAYLOAD = random.Random(8119).randbytes(16384)


@contextmanager
def foreground(sink):
    """Start a finite four-client workload kept off both relay counters."""
    report = {}

    def request(i):
        key = f"foreground/{i % 16}"
        if i % 4 == 0:
            sink.put_object(Bucket=BUCKET, Key=f"write/{i}", Body=PAYLOAD)
            return len(PAYLOAD)
        obj = sink.get_object(Bucket=BUCKET, Key=key, Range="bytes=17-4112")
        with obj["Body"] as body:
            if body.read() != PAYLOAD[17:4113]:
                raise ValueError(f"incorrect foreground range for {key}")
        return 4096

    with ThreadPoolExecutor(max_workers=1) as coordinator, ThreadPoolExecutor(max_workers=4) as pool:
        future = coordinator.submit(measure, pool, [(i,) for i in range(512)], request)
        try:
            yield report
        finally:
            report.update(future.result())


def seed_case(args, case, work, env, emit, inputs):
    root = args.downloads if case == "typescript" else args.corpus
    original, revised = (root / x for x in FAMILIES[case])
    original = materialize(original, work / "seed.tar")
    revised = materialize(revised, work / "target.tar")
    frozen = inputs[case]
    if file_digest(original) != frozen["prior_sha256"] or file_digest(revised) != frozen["target_sha256"]:
        raise ValueError(f"unexpected fixture: {case}")
    store, log = work / "source", work / "server.log"
    with serve(args.baseline, store, log, env) as (_, api, _):
        api.create_bucket(Bucket=BUCKET)
        put(api, "prior", original)
        put(api, "target", revised, {"scene": "c3"})
    temp = work / "planner-tmp"
    temp.mkdir()
    planning_binary = args.baseline
    flags = [*OPTIMIZE, "-store", str(store)]
    trace = work / "planning.strace"
    if args.strace:
        planning_binary = args.strace
        flags = ["-f", "-c", "-e", "trace=read,pread64,write,pwrite64,fsync,fdatasync,openat,unlink,rename",
                 "-o", str(trace), "--", str(args.baseline), *flags]
    with ThreadPoolExecutor(max_workers=1) as pool:
        done = pool.submit(run, planning_binary, {**env, "TMPDIR": str(temp)}, flags, 150)
        peak_scratch = 0
        while not done.done():
            total = 0
            for directory in (temp, store / "tmp"):
                for path in directory.rglob("*"):
                    try:
                        total += path.stat().st_blocks * 512
                    except FileNotFoundError:
                        pass
            peak_scratch = max(peak_scratch, total)
            time.sleep(.015)
        planning = done.result()
    report = json.loads(planning.pop("stdout"))
    emit({"case": case, "phase": "planning", **planning, "peak_planner_tmp_bytes": peak_scratch,
          "candidate_seconds": report.get("candidate_seconds"), "canonical": report.get("canonical"),
          "splice": report.get("splice"), "strace": trace.read_text() if args.strace else None})
    if args.strace:
        return

    state = "different-authority" if case == "typescript" else "same-key"
    if case == "typescript":
        lanes = ("closure-cross", "replicate", "s3", "sync") if args.ordinary else ("closure-cross", "replicate")
    else:
        lanes = ("s3", "sync", "closure")
    seed = work / "receiver"
    seed.mkdir()
    pre = SimpleNamespace(binary=args.baseline, receiver_binary=args.baseline)
    with serve(args.baseline, store, log, env) as src:
        verified(src[1], "prior", original)
        verified(src[1], "target", revised, {"scene": "c3"})
        stage(pre, env, seed, src, state, original, revised)
        if args.contended:
            with serve(args.baseline, seed / "destination", log, env) as (_, api, _):
                for i in range(16):
                    api.put_object(Bucket=BUCKET, Key=f"foreground/{i}", Body=PAYLOAD)
        for repetition in range(args.repeats):
            builds = [("c8", args.candidate)] if args.final_only else [("c7", args.baseline), ("c8", args.candidate)]
            if repetition % 2:
                builds.reverse()
            for lane in lanes:
                for build, binary in builds:
                    directory = work / "trial"
                    directory.mkdir()
                    try:
                        shutil.copytree(seed / "destination", directory / "destination")
                        config = SimpleNamespace(binary=binary, receiver_binary=binary, rate_bytes=args.rate,
                                                 model_gzip=False, finalize=False)
                        load = foreground if args.contended else None
                        row = trial(config, env, directory, src, case, state, lane, original, revised, foreground=load)
                        emit({"build": build, "phase": "transfer", "repetition": repetition, **row})
                    finally:
                        shutil.rmtree(directory)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("baseline", "candidate", "corpus", "downloads", "scratch", "results"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--cases", nargs="+", choices=CASES, default=CASES)
    parser.add_argument("--repeats", type=int, default=1)
    parser.add_argument("--rate", type=int, default=0)
    parser.add_argument("--contended", action="store_true")
    parser.add_argument("--final-only", action="store_true", help="measure only the current receiver and client")
    parser.add_argument("--ordinary", action="store_true", help="add direct S3 and native sync for TypeScript")
    parser.add_argument("--strace", type=Path, help="profile planning syscalls and skip transfer trials")
    args = parser.parse_args()
    if args.repeats < 1 or args.rate < 0:
        parser.error("positive repetitions and a nonnegative rate are required")
    for key, value in vars(args).items():
        if isinstance(value, Path):
            setattr(args, key, value.resolve())
    args.scratch.mkdir(parents=True, exist_ok=True)
    inputs = {r["family"]: r for r in map(json.loads, (compet / "c3" / "inputs.jsonl").read_text().splitlines())}
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as output:
        for case in args.cases:
            with tempfile.TemporaryDirectory(dir=args.scratch) as tmp:
                def emit(row):
                    print(json.dumps(row), file=output, flush=True)
                seed_case(args, case, Path(tmp), env, emit, inputs)


if __name__ == "__main__":
    main()
