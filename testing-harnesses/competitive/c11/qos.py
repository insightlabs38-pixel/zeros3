"""Run paired native import, maintenance and restore under bounded signed S3 load."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import random
import shutil
import subprocess
import sys
from types import SimpleNamespace
import time

compet = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(compet / name) for name in ("c1", "c3", "c4")]
from local_s3 import measure
from operations import signed_json
from transfer import (BUCKET, FAMILIES, KEY, OPTIMIZE, SECRET, allocated, delta, file_digest,
                      put, relay, run, serve, stage, stats, verified)

MAINT = ["-maintenance", "all", "-maintenance-interval", "0",
         "-maintenance-max-live-percent", "100"]
FOREGROUND = random.Random(1101).randbytes(16 << 10)
LANES = ("idle", "incumbent", "fixed", "adaptive")
EVENTS = ("maintenance", "gc", "import", "restore", "mixed")


def counters():
    root = Path("/sys/fs/cgroup")
    result = {}
    for name in ("cpu.max", "cpu.stat", "cpu.pressure", "io.stat", "io.pressure"):
        path = root / name
        if path.is_file():
            result[name] = path.read_text().strip()
    return result


def seed(args, env, original, target):
    work = args.work
    work.mkdir(parents=True)
    frozen = next(row for row in map(json.loads, (compet / "c3/inputs.jsonl").read_text().splitlines())
                  if row["family"] == "typescript")
    if (file_digest(original), file_digest(target)) != (frozen["prior_sha256"], frozen["target_sha256"]):
        raise ValueError("TypeScript artifacts differ from C3")
    with serve(args.baseline, work / "source", work / "source.log", env) as (_, source, _):
        source.create_bucket(Bucket=BUCKET)
        put(source, "prior", original)
        put(source, "target", target, {"scene": "c3"})
    run(args.baseline, env, [*OPTIMIZE, "-store", str(work / "source")], 150)
    with serve(args.baseline, work / "source", work / "source.log", env) as source:
        config = SimpleNamespace(receiver_binary=args.baseline)
        stage(config, env, work, source, "different-authority", original, target)
    store = work / "destination"
    with serve(args.baseline, store, work / "seed.log", env) as (_, sink, _):
        for i in range(16):
            sink.put_object(Bucket=BUCKET, Key=f"foreground/{i:02}", Body=FOREGROUND)
        for i in range(32):
            data = random.Random(1102+i).randbytes(288 << 10) + bytes([i]) * (288 << 10)
            key = f"{'dead' if i % 2 else 'live'}/{i:02}"
            sink.put_object(Bucket=BUCKET, Key=key, Body=data)
    run(args.baseline, env, ["compact", "-pack-size-mib", "1", "-json", "-store", str(store)])
    with serve(args.baseline, store, work / "seed.log", env) as (_, sink, _):
        for i in range(1, 32, 2):
            sink.delete_object(Bucket=BUCKET, Key=f"dead/{i:02}")
    run(args.baseline, env, ["versions", "prune", "-apply", "-keep-last", "0", "-bucket",
                             BUCKET, "-prefix", "dead/", "-store", str(store)])
    (work / "seed.json").write_text(json.dumps(frozen, sort_keys=True) + "\n")


def request(sink, i):
    key = f"foreground/{i % 16:02}"
    op = i % 5
    if op == 0:
        sink.put_object(Bucket=BUCKET, Key=f"writes/{i:05}", Body=FOREGROUND)
        return len(FOREGROUND)
    if op == 1:
        if sink.head_object(Bucket=BUCKET, Key=key)["ContentLength"] != len(FOREGROUND):
            raise ValueError("incorrect HEAD length")
        return 0
    if op == 2:
        result = sink.list_objects_v2(Bucket=BUCKET, Prefix="foreground/", MaxKeys=16)
        if result["KeyCount"] != 16:
            raise ValueError("incorrect listing")
        return 0
    opts = {"Range": "bytes=17-4112"} if op == 3 else {}
    with sink.get_object(Bucket=BUCKET, Key=key, **opts)["Body"] as stream:
        want = FOREGROUND[17:4113] if opts else FOREGROUND
        if stream.read() != want:
            raise ValueError("incorrect foreground object")
    return len(want)


def reclaim(port):
    signed_json(port, "POST", "/_zeros3/v1/maintenance")
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        status = signed_json(port, "GET", "/_zeros3/v1/maintenance")
        if status["passes"] and not status["running"]:
            result = status["last"]
            if result.get("error") or not result["live_set_ok"]:
                raise ValueError(f"maintenance failed: {result}")
            return result
        time.sleep(.035)
    raise TimeoutError("maintenance did not finish")


def transfer(args, work, env, source, port):
    with relay(source[0], 0) as (sender, src), relay(port, 0) as (receiver, dst):
        call = run(args.baseline, {**env, "TMPDIR": str(work)},
                   ["replicate", "-workers", "2", "-from", src, "-to", dst,
                    "-from-access-key", KEY, "-from-secret-key", SECRET,
                    "-to-access-key", KEY, "-to-secret-key", SECRET,
                    "-authority-closure", "-authority-base", f"s3://{BUCKET}/prior",
                    f"s3://{BUCKET}/target", f"s3://{BUCKET}/target"], 25)
        wire = receiver.snapshot()
        requests = wire["requests"]
        if (requests.get("POST /_zeros3/v1/authority/import") != 1
                or requests.get("PUT /s3-object")
                or wire["client_to_server"] > 1 << 20):
            raise ValueError("expected the verified TypeScript closure, without ordinary PUT fallback")
        return {"source": sender.snapshot(), "receiver": wire, "client": call}


def trial(args, env, source, original, target, lane, event, width, pair):
    name = f"{args.results.stem}-{pair}-{width}-{event}-{lane}-{args.cpus}"
    work = args.work / name
    work.mkdir()
    store = work / "destination"
    shutil.copytree(args.work / "destination", store)
    requested = () if lane == "idle" or event == "none" else (event,)
    if requested == ("mixed",):
        requested = ("maintenance", "import", "restore")
    if requested == ("gc",):
        requested = ("maintenance",)
    flags = [*MAINT] if "maintenance" in requested else []
    if flags and event == "gc":
        flags[1] = "gc"
    binary = (args.candidate or args.baseline) if lane in ("fixed", "adaptive") else args.baseline
    if lane in ("fixed", "adaptive"):
        flags += ["-background-yield", lane]
    with serve(binary, store, work / "receiver.log", env, *flags) as (port, sink, pid):
        refusal = {}
        if "maintenance" in requested and args.check_leases:
            for check, command in (("indexed", ["authority", "optimize", "-apply", "-indexed-discovery"]),
                                   ("archive", ["authority", "optimize", "-apply", "-profile", "archive", "-finalize"])):
                result = subprocess.run([str(binary), *command, "-store", str(store)],
                                        env=env, capture_output=True, text=True, timeout=6)
                if result.returncode == 0 or "needs exclusive access" not in result.stderr:
                    raise ValueError(f"{check} did not report the writer lease")
                refusal[check] = {"status": result.returncode, "stderr": result.stderr[-400:]}
        before, cpu_before = stats(pid), counters()
        started = time.monotonic()

        def timed(fn, *values):
            start = time.monotonic() - started
            result = fn(*values)
            return {"start": start, "end": time.monotonic() - started, "result": result}

        jobs = {"maintenance": (reclaim, port), "import": (transfer, args, work, env, source, port),
                "restore": (verified, sink, "prior", original)}
        with ThreadPoolExecutor(max_workers=4) as coordinator, ThreadPoolExecutor(max_workers=width) as pool:
            fg = coordinator.submit(timed, measure, pool, [(sink, i) for i in range(args.operations)], request)
            bg = {event: coordinator.submit(timed, *jobs[event]) for event in requested}
            foreground = fg.result()
            outcomes = {event: future.result() for event, future in bg.items()}
        receiver = delta(before, stats(pid))
        after = counters()
        exact = {"prior": verified(sink, "prior", original)}
        if "import" in requested:
            exact["target"] = verified(sink, "target", target, {"scene": "c3"})
        with sink.get_object(Bucket=BUCKET, Key="writes/00000")["Body"] as body:
            if body.read() != FOREGROUND:
                raise ValueError("incorrect acknowledged foreground write")
    result = {"lane": lane, "event": event, "width": width, "pair": pair, "cpus": args.cpus,
              "foreground": foreground, "background": outcomes, "receiver": receiver,
              "before": cpu_before, "after": after, "sha256": exact, "allocated": allocated(store),
              "offline_while_serving": refusal}
    if args.deep_verify:
        result["deep_verify"] = run(binary, env, ["verify", "-deep", "-json", "-store", str(store)], 40)
    shutil.rmtree(store)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("baseline", "candidate", "work", "downloads", "results"):
        parser.add_argument("--" + name, type=Path, required=name != "candidate")
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--events", nargs="+", choices=(*EVENTS, "none"), default=EVENTS)
    parser.add_argument("--lanes", nargs="+", choices=LANES, default=LANES[:2])
    parser.add_argument("--widths", nargs="+", type=int, default=[1, 4, 8, 16])
    parser.add_argument("--operations", type=int, default=512)
    parser.add_argument("--repeats", type=int, default=2)
    parser.add_argument("--cpus", type=int, default=2)
    parser.add_argument("--check-leases", action="store_true")
    parser.add_argument("--deep-verify", action="store_true")
    parser.add_argument("--runtime-trace", action="store_true")
    args = parser.parse_args()
    for name, value in vars(args).items():
        if isinstance(value, Path):
            setattr(args, name, value.resolve())
    if args.operations < 100 or args.repeats < 1 or any(w < 1 for w in args.widths):
        parser.error("at least 100 operations and positive width and repetitions are required")
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": str(args.cpus)}
    if args.runtime_trace:
        env["GODEBUG"] = "schedtrace=250,gctrace=1"
    original, target = (args.downloads / filename for filename in FAMILIES["typescript"])
    if args.prepare:
        seed(args, env, original, target)
        return
    with serve(args.baseline, args.work / "source", args.work / "source.log", env) as source:
        with args.results.open("x") as out:
            for pair in range(args.repeats):
                lanes = args.lanes if pair % 2 == 0 else args.lanes[::-1]
                for event in args.events:
                    for width in args.widths:
                        for lane in lanes:
                            row = trial(args, env, source, original, target, lane, event, width, pair)
                            print(json.dumps(row), file=out, flush=True)


if __name__ == "__main__":
    main()
