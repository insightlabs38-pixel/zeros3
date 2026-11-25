"""Replay the immutable C1 history and audit each maintenance stage."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import random
import shutil
import statistics
import subprocess
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c1"))
from local_s3 import FAMILIES, measure, put, sample, server, verified

LANES = {
    "cdc": [],
    "c1": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice"],
    "delta": ["-record-groups", "-no-rep"],
    "canonical": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs"],
    "depth4": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice",
               "-max-depth", "4", "-max-bases", "4", "-min-saving", ".02"],
    "standalone": ["-canonical", "-codec-replay", "-hard-codecs", "-splice"],
    "sketch": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice",
               "-discovery", "sketch"],
    "budget": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice",
               "-max-targets", "32"],
    "current": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice",
                "-price-current-storage"],
    "mixed": ["-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice",
              "-profile", "mixed", "-order", "newest"],
}


def inventory(store):
    allocations = {}
    seen = set()
    for path in (store, *store.rglob("*")):
        try:
            st = path.lstat()
        except FileNotFoundError:
            continue
        inode = st.st_dev, st.st_ino
        if inode in seen:
            continue
        seen.add(inode)
        parts = path.relative_to(store).parts
        group = "/".join(parts[:2]) if len(parts) > 1 and parts[0] in {"bodies", "reps"} else (
            parts[0] if parts else ".")
        if len(parts) > 1 and parts[0] in {"bodies", "reps"} and len(parts[1]) == 2:
            group = parts[0] + "/shards"
        allocations[group] = allocations.get(group, 0) + st.st_blocks * 512
    return allocations


def command(binary, store, flags, cwd):
    output, errors = cwd / "output", cwd / "errors"
    before = inventory(store)
    peak = sum(before.values())
    started = time.perf_counter()
    with output.open("w") as out, errors.open("w") as err:
        child = subprocess.Popen([str(binary), *flags, "-store", str(store)], stdout=out, stderr=err,
                                 env={**os.environ, "GOMAXPROCS": "2", "TMPDIR": str(cwd)})
        while True:
            pid, status, usage = os.wait4(child.pid, os.WNOHANG)
            if pid:
                child.returncode = os.waitstatus_to_exitcode(status)
                break
            peak = max(peak, sum(inventory(store).values()))
            if time.perf_counter() - started > 240:
                child.kill()
                child.wait()
                raise TimeoutError(flags)
            time.sleep(.05)
    elapsed = time.perf_counter() - started
    if child.returncode:
        raise RuntimeError(f"{flags}: {child.returncode}: {(errors.read_text()+output.read_text())[-1400:]}")
    data = json.loads(output.read_text())
    after = inventory(store)
    if "changes" in data:
        data["transition_counts"] = dict((name, sum(c["to"] == name for c in data["changes"]))
                                        for name in sorted({c["to"] for c in data["changes"]}))
        del data["changes"]
    if "failed" in data and data["failed"]:
        raise RuntimeError(f"partial application: {data}")
    return {"seconds": elapsed, "cpu_seconds": usage.ru_utime + usage.ru_stime,
            "rss_bytes": usage.ru_maxrss * 1024, "write_blocks": usage.ru_oublock,
            "allocated": sum(after.values()), "paths": after,
            "peak_store_bytes": max(peak, sum(after.values())), "result": data}


def records(corpus, family):
    for rev in range(3):
        for name in FAMILIES if family == "combined" else (family,):
            root = corpus / name / str(rev)
            for p in sorted(root.rglob("*")):
                if p.is_file():
                    yield f"history/{rev}/{name}/{p.relative_to(root).as_posix()}", p.read_bytes()


def measure_reads(api, objects):
    rng = random.Random(3597)
    latencies = {}
    for size in (4096, 65536, 1048576, 0):
        offsets = {}
        for repeat in range(2):
            times = []
            for key, body in objects:
                if size:
                    start = rng.randrange(max(1, len(body) - size + 1)) if repeat == 0 else offsets[key]
                    end = min(len(body), start + size)
                    if repeat == 0:
                        offsets[key] = start
                    expected, span = body[start:end], f"bytes={start}-{end-1}"
                else:
                    expected, span = body, None
                t = time.perf_counter()
                verified(api, key, expected, span)
                times.append((time.perf_counter() - t) * 1000)
            times.sort()
            latencies[f"{size}/{repeat}"] = {"n": len(times), "p50_ms": statistics.median(times),
                                            "p95_ms": times[(95 * len(times) - 1) // 100],
                                            "p99_ms": times[(99 * len(times) - 1) // 100] if len(times) >= 100 else None,
                                            "max_ms": times[-1]}
    return latencies


def concurrent_reads(api, pid, items):
    rng = random.Random(776)
    scans = []
    for _ in range(36):
        key, body = rng.choice(items)
        start = rng.randrange(max(1, len(body)-4096+1))
        end = min(start+4096, len(body))
        scans.append((key, body[start:end], f"bytes={start}-{end-1}"))
    for width in (1, 4):
        before = sample(pid)
        with ThreadPoolExecutor(width) as pool:
            row = measure(pool, scans, lambda key, expected, span: verified(api, key, expected, span))
        after = sample(pid)
        yield {"concurrency": width,
               "server_cpu_seconds": after["cpu_seconds"]-before["cpu_seconds"], **row}


def run(args, family, out):
    root = args.scratch / family
    root.mkdir()
    try:
        (root / "zeros3").symlink_to(args.binary)
        seed = root / "seed"
        items = list(records(args.corpus, family))
        if not items:
            raise ValueError(f"missing input for {family}")
        began = time.perf_counter()
        with server("zeros3", root, seed) as (api, pid, store):
            ingress_before = sample(pid)
            for key, content in items:
                put(api, key, content)
            ingress_after = sample(pid)
            initial = inventory(store)
        for lane in args.lanes:
            txn = root / lane
            shutil.copytree(seed, txn)
            store = txn / "store"

            def emit(stage, info):
                print(json.dumps({"family": family, "lane": lane, "stage": stage, **info}),
                      file=out, flush=True)

            report = inventory(store)
            emit("ingest", {"seconds_including_server": time.perf_counter() - began if lane == args.lanes[0] else None,
                             "allocated": sum(report.values()), "paths": report, "serving_paths": initial,
                            "ingest_server_cpu_seconds": ingress_after["cpu_seconds"] - ingress_before["cpu_seconds"],
                            "ingest_server_peak_rss_bytes": ingress_after["peak_rss_bytes"],
                            "logical_bytes": sum(len(body) for _, body in items)})
            opts = ["authority", "optimize", "-profile", "archive", "-order", "oldest",
                    "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10", *LANES[lane]]
            if lane != "cdc":
                if args.dry_plan:
                    emit("plan", command(args.binary, store, opts, txn))
                flags = [*opts, "-apply"]
                if args.integrated:
                    flags.append("-finalize")
                emit("apply", command(args.binary, store, flags, txn))
            steps = [] if args.integrated else [("gc", ["gc", "-apply", "-json"])]
            if not args.gc_only and not args.integrated:
                repack = ["repack", "-apply", "-json"]
                compact = ["compact", "-json"]
                if args.full:
                    repack += ["-max-live-percent", "100", "-recompress"]
                    compact += ["-pack-size-mib", "1"]
                steps += [("repack", repack), ("compact", compact),
                          ("final-gc", ["gc", "-apply", "-json"])]
            for stage, flags in steps:
                emit(stage, command(args.binary, store, flags, txn))
            emit("deep-verify", command(args.binary, store, ["verify", "-deep", "-json"], txn))
            if args.reoptimize and lane != "cdc":
                extra = ["-reuse-analysis"] if args.memo else []
                for phase in ("reoptimize", "memo-hit") if args.memo else ("reoptimize",):
                    emit(phase, command(args.binary, store, [*opts, "-apply", *extra], txn))
            with server("zeros3", root, txn) as (api, pid, store):
                if args.reads:
                    before = sample(pid)
                    latency = measure_reads(api, items)
                    after = sample(pid)
                    emit("reads", {"cache": "fresh process then repeated, OS cache uncontrolled",
                                   "latency": latency, "server_cpu_seconds": after["cpu_seconds"]-before["cpu_seconds"],
                                   "server_peak_rss_bytes": after["peak_rss_bytes"],
                                   "disk_read_bytes": after["disk_read_bytes"]-before["disk_read_bytes"]})
                else:
                    for key, content in items:
                        verified(api, key, content)
                if args.concurrent_reads:
                    for result in concurrent_reads(api, pid, items):
                        emit("concurrent-reads", result)
                emit("restore", {"objects_verified": len(items)})
            shutil.rmtree(txn)
    finally:
        shutil.rmtree(root)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("corpus", type=Path)
    parser.add_argument("scratch", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--families", nargs="+", required=True)
    parser.add_argument("--lanes", nargs="+", choices=LANES, default=["cdc", "c1", "delta"])
    parser.add_argument("--dry-plan", action="store_true")
    parser.add_argument("--gc-only", action="store_true")
    parser.add_argument("--full", action="store_true")
    parser.add_argument("--integrated", action="store_true")
    parser.add_argument("--reads", action="store_true")
    parser.add_argument("--concurrent-reads", action="store_true")
    parser.add_argument("--reoptimize", action="store_true")
    parser.add_argument("--memo", action="store_true", help="measure convergence pass and a memo hit")
    args = parser.parse_args()
    args.scratch.mkdir(parents=True, exist_ok=True)
    with args.output.open("x") as out:
        for family in args.families:
            run(args, family, out)


if __name__ == "__main__":
    main()
