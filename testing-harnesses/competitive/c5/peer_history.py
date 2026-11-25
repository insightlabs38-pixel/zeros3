"""Measure the same live revision keys on independent S3 engines."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import sys
import tempfile

compat = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(compat / "c2"), str(compat / "c1")]
from frontier import inventory, measure_reads, records
from local_s3 import measure, put, sample, server, verified


def history(args, engine, family, run):
    items = list(records(args.work / "corpus", family))
    if not items:
        raise ValueError(f"missing fixtures for {family}")
    with server(engine, args.work, run) as (client, pid, store):
        cpu = sample(pid)["cpu_seconds"]
        with ThreadPoolExecutor(max_workers=1) as pool:
            ingestion = measure(pool, items, lambda key, data: put(client, key, data))
        ingestion["server_cpu_seconds"] = sample(pid)["cpu_seconds"] - cpu
        before = sum(inventory(store).values())
        yield {"stage": "ingest", **ingestion, "allocated": before}
    with server(engine, args.work, run) as (client, pid, store):
        read_stats = sample(pid)
        latency = measure_reads(client, items)
        measured = sample(pid)
        for key, data in items:
            verified(client, key, data)
        yield {"stage": "restore", "latency": latency, "verified": len(items),
               "logical": sum(len(data) for _, data in items),
               "allocated": sum(inventory(store).values()),
               "server_cpu_seconds": measured["cpu_seconds"] - read_stats["cpu_seconds"],
               "server_rss_bytes": measured["peak_rss_bytes"]}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("work", type=Path)
    p.add_argument("results", type=Path)
    p.add_argument("--families", nargs="+", required=True)
    p.add_argument("--systems", nargs="+", default=["minio", "rustfs"], choices=("minio", "rustfs"))
    args = p.parse_args()
    args.work = args.work.resolve()
    with args.results.open("x") as out:
        for i, family in enumerate(args.families):
            for system in args.systems[::1 if i % 2 == 0 else -1]:
                with tempfile.TemporaryDirectory(prefix="c5-", dir=args.work) as tmp:
                    for row in history(args, system, family, Path(tmp)):
                        print(json.dumps({"system": system, "family": family, **row}), file=out, flush=True)


if __name__ == "__main__":
    main()
