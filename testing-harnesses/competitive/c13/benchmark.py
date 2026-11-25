"""Measure matched small-object pack geometry, reads, and churn."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import random
import resource
import subprocess
import sys
import time

from cohort import Archive, LANES, allocation, publish, sha


def fixtures(corpus, manifest, scope):
    out = {}
    for row in map(json.loads, manifest.read_text().splitlines()):
        if row["scope"] != scope:
            continue
        key = row["key"]
        data = (corpus / scope / key).read_bytes()
        if sha(data) != row["sha256"] or len(data) != row["bytes"]:
            raise ValueError(f"fixture digest differs at {key}")
        out[key] = data
    return out


def timed(where, *args, **kwargs):
    cpu, wall = time.process_time(), time.monotonic()
    output = where(*args, **kwargs)
    return {"cpu_seconds": time.process_time()-cpu, "seconds": time.monotonic()-wall, **output}


def check(archive, expected):
    if archive.index["objects"].keys() != expected.keys():
        raise ValueError("visible scope differs")
    for key, data in expected.items():
        if archive.get(key) != data:
            raise ValueError(f"restore differs at {key}")


def locality(root, scope, items, width, size):
    rng = random.Random(1309)
    keys = [key for key in items if "/eval/" in key]
    requests = []
    for _ in range(192):
        key = rng.choice(keys)
        n = min(size or len(items[key]), len(items[key]))
        at = rng.randrange(len(items[key])-n+1)
        requests.append((key, at, n))
    started = time.monotonic()
    with Archive(root, scope) as archive:
        open_seconds = time.monotonic()-started
        advised = 0
        for fd in archive.fds.values():
            try:
                os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED)
                advised += 1
            except OSError:
                pass

        def get(row):
            key, at, n = row
            start = time.perf_counter_ns()
            result = archive.get(key)[at:at+n]
            elapsed = time.perf_counter_ns()-start
            if result != items[key][at:at+n]:
                raise ValueError("range differs")
            return elapsed, n

        out = []
        with ThreadPoolExecutor(max_workers=width) as pool:
            for phase in ("first-pass", "same-request-repeat"):
                before = resource.getrusage(resource.RUSAGE_SELF)
                started = time.perf_counter()
                rows = list(pool.map(get, requests))
                elapsed = time.perf_counter()-started
                end = resource.getrusage(resource.RUSAGE_SELF)
                lat = sorted(t/1000 for t, _ in rows)
                out.append({"phase": phase, "width": width, "range_cap": size,
                            "samples": len(rows), "operations_per_s": len(rows)/elapsed,
                            "read_bytes": sum(n for _, n in rows),
                            "cpu_seconds": end.ru_utime+end.ru_stime-before.ru_utime-before.ru_stime,
                            "physical_read_blocks": end.ru_inblock-before.ru_inblock,
                            "open_seconds": open_seconds, "fadvise_files": advised,
                            **{f"p{p}_us": lat[math.ceil(len(lat)*p/100)-1] for p in (50,95,99)}})
    return out


def churn(root, scope, lane, items):
    removed = [key for i, key in enumerate(sorted(k for k in items if "/eval/" in k)) if i%4 == 0]
    survivors = {k: v for k, v in items.items() if k not in removed}
    additions = {}
    for i, key in enumerate(removed):
        cohort = key.split("/")[0]
        data = bytearray(items[key])
        at = (i*37) % len(data)
        data[at] ^= 0x41
        additions[f"{cohort}/update/{i:04}.bin"] = bytes(data)
    with Archive(root, scope) as old:
        changed = timed(publish, root, scope, lane, additions, old, delete=removed)
        survivors.update(additions)
        with Archive(root, scope) as current:
            check(current, survivors)
            rebuilt = timed(publish, root, scope, lane, survivors, current, rebuild=True)
        if old.get(removed[0]) != items[removed[0]]:
            raise ValueError("reader lost its open generation")
    with Archive(root, scope) as current:
        check(current, survivors)
    return {"deleted": len(removed), "added": len(additions), "append": changed, "rebuild": rebuilt,
            "allocated_after_reopen": allocation(root)}


def trial(args, lane, repeat):
    root = args.work / f"{args.scope}-{repeat}-{lane}"
    root.mkdir(parents=True)
    items = fixtures(args.corpus, args.manifest, args.scope)
    built = timed(publish, root, args.scope, lane, items)
    with Archive(root, args.scope) as reader:
        check(reader, items)
    reads = []
    for width in (1, 8):
        for size in (4096, 0):
            reads.extend(locality(root, args.scope, items, width, size))
    return {"scope": args.scope, "lane": lane, "repeat": repeat, "objects": len(items),
            "logical": sum(map(len, items.values())), "initial": built, "reads": reads,
            "churn": churn(root, args.scope, lane, items),
            "peak_rss_bytes": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss*1024}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("work", "corpus", "manifest", "results"):
        p.add_argument("--" + n, type=Path, required=True)
    p.add_argument("--scope", choices=("main", "heldout"), required=True)
    p.add_argument("--lanes", choices=LANES, nargs="+", default=LANES)
    p.add_argument("--repeats", type=int, default=2)
    p.add_argument("--trial", nargs=2, help=argparse.SUPPRESS)
    args = p.parse_args()
    if args.trial:
        lane, repeat = args.trial
        print(json.dumps(trial(args, lane, int(repeat))))
        return
    with args.results.open("x") as output:
        for repeat in range(args.repeats):
            lanes = args.lanes[::-1] if repeat % 2 else args.lanes
            for lane in lanes:
                run = subprocess.run([sys.executable, str(Path(__file__).resolve()), *sys.argv[1:],
                                      "--trial", lane, str(repeat)], text=True, capture_output=True, timeout=120)
                if run.returncode:
                    raise RuntimeError(run.stderr)
                print(json.dumps(json.loads(run.stdout)), file=output, flush=True)


if __name__ == "__main__":
    main()
