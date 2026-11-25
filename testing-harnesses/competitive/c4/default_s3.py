"""Alternating frozen default-S3 trials with verified payloads and warm/cold labels."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import random
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c1"))
from corpus import blob
from local_s3 import MIB, measure, put, sample, server, verified


def phase(operation, pool, records, call, pid, phase_cache):
    initial = sample(pid)
    result = measure(pool, records, call)
    end = sample(pid)
    result.update({
        "operation": operation, "cache": phase_cache,
        "ops_per_second": result["operations"] / result["seconds"],
        "server_cpu_seconds": end["cpu_seconds"] - initial["cpu_seconds"],
        "disk_read_bytes": end["disk_read_bytes"] - initial["disk_read_bytes"],
        "disk_write_bytes": end["disk_write_bytes"] - initial["disk_write_bytes"],
        "server_peak_rss_bytes": end["peak_rss_bytes"],
    })
    return result


def trial(binary, scratch, records, width, block):
    order = records.copy()
    random.Random(142 + block).shuffle(order)
    with tempfile.TemporaryDirectory(dir=scratch) as tmp:
        work = Path(tmp)
        (work / "zeros3").symlink_to(binary)
        with ThreadPoolExecutor(max_workers=width) as pool:
            with server("zeros3", work, work) as (api, pid, _):
                yield phase("put", pool, records, lambda k, d: put(api, k, d), pid, "empty-store")
            with server("zeros3", work, work) as (api, pid, _):
                get = lambda key, data: verified(api, key, data)
                get_range = lambda key, data: verified(api, key, data[37:549], "bytes=37-548")
                yield phase("get-first", pool, order, get, pid, "process-restart")
                yield phase("get-warm", pool, order, get, pid, "repeated")
                yield phase("range-warm", pool, order, get_range, pid, "after-full-read")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--b10", type=Path, required=True)
    parser.add_argument("--c2", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--scratch", type=Path, required=True)
    parser.add_argument("--pairs", type=int, default=6)
    parser.add_argument("--objects", type=int, default=768)
    parser.add_argument("--width", type=int, choices=(1, 4, 8), default=4)
    args = parser.parse_args()
    if not 2 <= args.pairs <= 12 or not 120 <= args.objects <= 1536:
        parser.error("use 2–12 pairs and 120–1536 objects")
    args.scratch.mkdir(parents=True, exist_ok=True)
    lengths = (1024, 4096, 16384, 65536, 262144, MIB)
    records = []
    for i in range(args.objects):
        size = lengths[i % len(lengths)]
        text = f'{{"event":"stored","record":{i}}}\n'.encode()
        data = blob(i, size) if i % 4 < 2 else (text * (size // len(text) + 1))[:size]
        records.append((f"c4/{i:05}", data))
    builds = [("b10", args.b10.resolve()), ("c2", args.c2.resolve())]
    with args.results.open("x") as report:
        for block in range(args.pairs):
            order = builds if block % 2 == 0 else builds[::-1]
            for position, (build, binary) in enumerate(order):
                for row in trial(binary, args.scratch, records, args.width, block):
                    print(json.dumps({"build": build, "pair": block, "position": position,
                                      "width": args.width, **row}), file=report, flush=True)


if __name__ == "__main__":
    main()
