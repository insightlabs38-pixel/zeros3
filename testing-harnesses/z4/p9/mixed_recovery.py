#!/usr/bin/env python3
"""Validate acknowledged writes and maintenance exclusion under parallel S3 traffic."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import math
from pathlib import Path
import subprocess
import time

from scale_recovery import check_objects, curl, digest, process, sample_fixture, wait_live


def percentiles(samples):
    values = sorted(samples)
    return {f"p{p}_ms": round(values[math.ceil(len(values) * p / 100) - 1], 3) for p in (50, 95, 99)}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--shadow-store", type=Path, required=True)
    p.add_argument("--manifest", type=Path, required=True)
    p.add_argument("--out", type=Path, required=True)
    p.add_argument("--rounds", type=int, default=24)
    args = p.parse_args()
    binary, store = args.binary.resolve(strict=True), args.shadow_store.resolve(strict=True)
    if store.name != "store" or not (store.parent / "results.json").is_file():
        p.error("use a scratch store produced by a successful scale_recovery.py trial")
    if not 1 <= args.rounds <= 1000:
        p.error("rounds must be between one and 1000")
    args.out.mkdir()
    known = sample_fixture(args.manifest)
    ingress = args.out / "request.bin"
    ingress.write_bytes(b"mixing-complete-writes-and-readers" * 128)
    expected_sha = digest(ingress)

    with process(binary, store, args.out / "mixed-serve.log") as (child, port, endpoint):
        wait_live(child, port)
        curl(endpoint, "/p9-mixed-recovery", args.out / "bucket-response", "-X", "PUT")
        pool = ThreadPoolExecutor(max_workers=7)
        def get(worker):
            latency = []
            file = args.out / f"worker-{worker}.bin"
            for offset in range(args.rounds * 2):
                start = time.monotonic_ns()
                check_objects(endpoint, [known[(offset + worker) % len(known)]], file)
                latency.append((time.monotonic_ns() - start) / 1e6)
            return latency

        def put():
            latency, writes = [], []
            for offset in range(args.rounds):
                key = f"/p9-mixed-recovery/ack-{offset:05d}"
                start = time.monotonic_ns()
                curl(endpoint, key, args.out / "ack", "-T", str(ingress), "-H", "If-None-Match: *")
                latency.append((time.monotonic_ns() - start) / 1e6)
                writes.append((key, expected_sha))
            return latency, writes

        def maintenance(op):
            report = subprocess.run([binary, op, "-store", store, "-apply", "-json"],
                                    capture_output=True, timeout=90)
            if report.returncode == 0 or b"requires exclusive access" not in report.stderr:
                raise RuntimeError(f"{op} did not respect the writer lock: {report.returncode} {report.stderr!r}")
            (args.out / f"competing-{op}.log").write_bytes(report.stderr)
            return report.returncode

        with pool:
            readers = [pool.submit(get, task) for task in range(4)]
            writer = pool.submit(put)
            refusals = {op: pool.submit(maintenance, op) for op in ("gc", "repack")}
            read_ms = [x for future in readers for x in future.result()]
            write_ms, completed = writer.result()
            refused = {op: code.result() for op, code in refusals.items()}
        child.kill()
        child.wait(timeout=30)
    with process(binary, store, args.out / "reopened.log") as (child, port, endpoint):
        wait_live(child, port)
        check_objects(endpoint, known + completed, args.out / "reopened-response.bin")
    report = {"reads_exact": len(read_ms), "conditional_puts_exact_after_kill": len(completed),
              "original_objects_exact_after_kill": len(known), "lock_refusals": refused,
              "read_end_to_end": percentiles(read_ms), "write_end_to_end": percentiles(write_ms)}
    (args.out / "results.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
