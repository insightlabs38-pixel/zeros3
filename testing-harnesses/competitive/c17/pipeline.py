"""Profile exact native pipeline stages with matched seeds and resource policies."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import random
import re
import shutil
import sys
import time
from types import SimpleNamespace

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/sub) for sub in ("c16", "c8", "c3")]
from frontier import LEAN, inputs
from stream import PAYLOAD, foreground
from transfer import BUCKET, KEY, SECRET, allocated, delta, file_digest, put, run, serve, stage, stats, trial, verified


RESOURCE = {"tight": ("2", "64MiB"), "oversubscribed": ("4", "192MiB")}
FINALIZATION = {
    "gc": ("gc", "-apply", "-json"),
    "repack": ("repack", "-apply", "-recompress", "-max-live-percent", "100", "-json"),
    "compact": ("compact", "-pack-size-mib", "1", "-json"),
    "post-gc": ("gc", "-apply", "-json"),
    "verify": ("verify", "-deep", "-json"),
}
SYSCALLS = "read,pread64,write,pwrite64,fsync,fdatasync,openat,unlink,rename,renameat"


def gc_data(raw):
    pauses = []
    for line in raw.splitlines():
        if m := re.match(r"^gc \d+ @\S+s \d+%: ([0-9.]+)\+([0-9.]+)\+([0-9.]+) ms clock", line):
            pauses.append(float(m[1])+float(m[3]))
    return {"cycles": len(pauses), "pause_total_ms": sum(pauses), "pause_max_ms": max(pauses, default=0)}


def scratch(root, paths):
    n = 0
    for parent in paths:
        for p in (root/parent).rglob("*"):
            try:
                n += p.stat().st_blocks*512
            except FileNotFoundError:
                pass
    return n


def profile(args, binary, env, root, store, name, flags, trace):
    wrapper = binary
    command = [*flags, "-store", str(store)]
    trace_file = root/f"{name}.strace"
    if trace:
        wrapper = args.strace
        command = ["-f", "-c", "-e", f"trace={SYSCALLS}", "-o", str(trace_file), "--", str(binary), *command]
    temp = root/"planner-tmp"
    temp.mkdir(exist_ok=True)
    with ThreadPoolExecutor(max_workers=1) as worker:
        future = worker.submit(run, wrapper, {**env, "TMPDIR": str(temp)}, command, 150, separate_stderr=True)
        peak = 0
        while not future.done():
            peak = max(peak, scratch(root, ("planner-tmp", "store/tmp")))
            time.sleep(.018)
        row = future.result()
    report = json.loads(row.pop("stdout"))
    gc = gc_data(row.pop("stderr"))
    return {**row, "report": report, "scratch_peak_bytes": peak, "disk_allocated": allocated(store),
            "gc": gc, "syscalls": trace_file.read_text() if trace else None}


def read_paths(api, pid, paths):
    result = {}
    for key, path in paths.items():
        before = stats(pid)
        before_time = time.perf_counter()
        sha = verified(api, key, path, {"scene": "c3"} if key == "target" else {})
        whole_seconds = time.perf_counter()-before_time
        whole = delta(before, stats(pid))
        rng = random.Random(1717)
        elapsed = []
        original = path.read_bytes()
        before = stats(pid)
        for _ in range(16):
            start = rng.randrange(max(1, len(original)-4096))
            now = time.perf_counter()
            with api.get_object(Bucket=BUCKET, Key=key, Range=f"bytes={start}-{start+4095}")["Body"] as body:
                got = body.read()
            if got != original[start:start+4096]:
                raise ValueError("pipeline sparse read differs")
            elapsed.append(time.perf_counter()-now)
        result[key] = {"sha256": sha, "whole_seconds": whole_seconds, "whole_io": whole,
                       "p95_4k_seconds": sorted(elapsed)[15], "read_4k_io": delta(before, stats(pid))}
    return result


def optimize(args, env, family, root, emit):
    root.mkdir()
    paths = dict(zip(("prior", "target"), inputs(args, family)))
    raw = root/"raw-store"
    begin = time.perf_counter()
    with serve(args.baseline, raw, root/"put.log", env) as (_, api, pid):
        api.create_bucket(Bucket=BUCKET)
        before = stats(pid)
        for key, path in paths.items():
            put(api, key, path, {"scene": "c3"} if key == "target" else None)
        io = delta(before, stats(pid))
    emit({"family": family, "stage": "ingest", "seconds_with_startup": time.perf_counter()-begin,
          "disk_allocated": allocated(raw), "io": io,
          "paths": {key: {"sha256": file_digest(p), "size": p.stat().st_size} for key, p in paths.items()}})
    lanes = {"c16": args.baseline, "retained-recipe": args.candidate}
    for config in args.resources:
        procs, mem = RESOURCE[config]
        limits = {**env, "GOMAXPROCS": procs, "GOMEMLIMIT": mem, "GODEBUG": "gctrace=1"}
        for rep in range(args.repeats):
            for lane, binary in list(lanes.items())[::1 if rep % 2 == 0 else -1]:
                work = root/f"{config}-{rep}-{lane}"
                work.mkdir()
                store = work/"store"
                shutil.copytree(raw, store)
                options = [*LEAN]
                if lane != "c16":
                    options.append("-retain-canonical-plans")
                shared = {"family": family, "profile": config, "lane": lane, "repeat": rep, "traced": args.trace}
                emit({**shared, "stage": "optimize", **profile(args, binary, limits, work, store, "optimize", options, args.trace)})
                for name, flags in FINALIZATION.items():
                    row = profile(args, binary, limits, work, store, name, flags, args.trace)
                    emit({**shared, "stage": name, **row})
                with serve(binary, store, work/"read.log", limits) as (_, api, pid):
                    reads = read_paths(api, pid, paths)
                emit({**shared, "stage": "read", "reads": reads, "retained": allocated(store),
                      "server_gc": gc_data((work/"read.log").read_text())})


def transfer(args, env, root, emit):
    root.mkdir()
    prior, target = inputs(args, "typescript")
    seed = root/"seed"
    seed.mkdir()
    with serve(args.baseline, args.prepared, root/"source.log", env) as source:
        options = SimpleNamespace(binary=args.baseline, receiver_binary=args.baseline, rate_bytes=0,
                                  model_gzip=False, finalize=True, request_delay=0)
        stage(options, env, seed, source, "different-key", prior, target)
        with serve(args.baseline, seed/"destination", root/"operations.log", env) as (_, api, _):
            for i in range(16):
                api.put_object(Bucket=BUCKET, Key=f"foreground/{i}", Body=PAYLOAD)
        for config in args.resources:
            procs, mem = RESOURCE[config]
            limits = {**env, "GOMAXPROCS": procs, "GOMEMLIMIT": mem}
            options.receiver_env = {**limits, "GODEBUG": "gctrace=1"}
            for rep in range(args.repeats):
                lanes = [("c16", args.baseline), ("fused", args.candidate)]
                for lane, binary in lanes[::1 if rep % 2 == 0 else -1]:
                    work = root/f"{config}-{rep}-{lane}"
                    work.mkdir()
                    shutil.copytree(seed/"destination", work/"destination")
                    options.receiver_binary = binary
                    row = trial(options, limits, work, source, "typescript", "different-key",
                                "closure-derived", prior, target, foreground=foreground if args.fg else None)
                    row.update({"profile": config, "receiver_build": lane, "repeat": rep, "stage": "transfer",
                                "server_gc": gc_data((work/"destination.log").read_text())})
                    emit(row)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("baseline", "candidate", "downloads", "c1", "c14", "work", "results", "prepared", "strace"):
        p.add_argument("--"+n, type=lambda name: Path(name).resolve(), required=n not in ("prepared", "strace"))
    p.add_argument("--phase", choices=("optimize", "transfer"), required=True)
    p.add_argument("--families", nargs="+", choices=("typescript", "sqlite", "entropy"), default=("typescript",))
    p.add_argument("--resources", nargs="+", choices=tuple(RESOURCE), default=("tight",))
    p.add_argument("--repeats", type=int, default=1)
    p.add_argument("--trace", action="store_true")
    p.add_argument("--fg", action="store_true")
    args = p.parse_args()
    if args.trace and not args.strace or args.phase == "transfer" and not args.prepared:
        p.error("trace needs strace and transfer needs a prepared source")
    args.work.mkdir(parents=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        def emit(row):
            print(json.dumps(row), file=out, flush=True)
        if args.phase == "optimize":
            for family in args.families:
                optimize(args, env, family, args.work/family, emit)
        else:
            transfer(args, env, args.work/"native", emit)


if __name__ == "__main__":
    main()
