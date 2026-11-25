"""Replay Xet's logical upload sequence over the ZeroS3 S3 interface."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import shutil

from local_s3 import allocation, measure, put, sample, server, verified
from xet import CASES, counter


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("result", type=Path)
    args = parser.parse_args()
    run = args.work / "runs" / "zeros3-xet-control"
    try:
        with args.result.open("x", buffering=1) as out, server("zeros3", args.work, run) as (api, pid, store):
            retained = []
            for case, family, revision, suffix in CASES:
                source = next((args.work / "corpus" / family / str(revision)).iterdir())
                data = source.read_bytes()
                path = f"c1/{case}/{suffix}"
                retained.append((case, path, data))
                before = counter("lo")["tx_bytes"]
                cpu = sample(pid)["cpu_seconds"]
                with ThreadPoolExecutor(max_workers=1) as pool:
                    result = measure(pool, [(path, data)], lambda key, body: put(api, key, body))
                print(json.dumps({"phase": "upload", "case": case, **result,
                                  "loopback_bytes": counter("lo")["tx_bytes"] - before,
                                  "server_cpu_seconds": sample(pid)["cpu_seconds"] - cpu,
                                  "server_allocated_bytes": allocation(store)}), file=out)
            for case, path, data in retained:
                before = counter("lo")["tx_bytes"]
                with ThreadPoolExecutor(max_workers=1) as pool:
                    result = measure(pool, [(path, data)], lambda key, body: verified(api, key, body))
                print(json.dumps({"phase": "download", "case": case, **result,
                                  "loopback_bytes": counter("lo")["tx_bytes"] - before,
                                  "server_allocated_bytes": allocation(store)}), file=out)
    finally:
        if run.exists():
            shutil.rmtree(run)


if __name__ == "__main__":
    main()
