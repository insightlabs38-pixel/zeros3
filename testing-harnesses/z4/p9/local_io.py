#!/usr/bin/env python3
"""Measure repeatable local writes, sync tails and verified reads."""
import argparse
import hashlib
import json
import math
import mmap
import os
from pathlib import Path
import random
import subprocess
import time

BLOCK = 4096
WINDOW = 64 * 1024


def percentiles(samples):
    ordered = sorted(samples)
    return {f"p{p}_ms": ordered[math.ceil(len(ordered) * p / 100) - 1] / 1e6 for p in (50, 95, 99)}


def read_verified(fd, block, expected):
    if os.preadv(fd, [block], 0) != len(block) or hashlib.sha256(block).digest() != expected:
        raise RuntimeError("stored bytes do not match the fixture")


def measure(path, mib, syncs, direct, seed):
    rng = random.Random(seed)
    size = mib << 20
    block = mmap.mmap(-1, WINDOW)
    block[:] = rng.randbytes(WINDOW)
    expected = hashlib.sha256(block).digest()
    ceiling = os.O_DIRECT if direct else 0
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_RDWR | ceiling, 0o600)
    try:
        start = time.monotonic_ns()
        for off in range(0, size, WINDOW):
            if os.pwrite(fd, block, off) != WINDOW:
                raise RuntimeError("short sequential write")
        os.fsync(fd)
        sequential_ns = time.monotonic_ns() - start
        read_verified(fd, block, expected)
        # Keep the first window as an independently verifiable sentinel.
        offsets = [rng.randrange(WINDOW // BLOCK, size // BLOCK) * BLOCK for _ in range(syncs)]
        page = memoryview(block)[:BLOCK]
        try:
            start = time.monotonic_ns()
            for off in offsets:
                if os.pwrite(fd, page, off) != BLOCK:
                    raise RuntimeError("short random write")
            os.fsync(fd)
            random_batch_ns = time.monotonic_ns() - start
            tails = []
            for off in offsets:
                start = time.monotonic_ns()
                if os.pwrite(fd, page, off) != BLOCK:
                    raise RuntimeError("short sync-series write")
                os.fsync(fd)
                tails.append(time.monotonic_ns() - start)
            expected_page = hashlib.sha256(page).digest()
        finally:
            page.release()

        read_modes = ["direct"] if direct else ["advised-drop", "warm-repeat"]
        latencies = {}
        for label in read_modes:
            if label == "advised-drop":
                os.posix_fadvise(fd, 0, size, os.POSIX_FADV_DONTNEED)
            read_times = []
            for off in offsets:
                page = memoryview(block)[:BLOCK]
                try:
                    start = time.monotonic_ns()
                    n = os.preadv(fd, [page], off)
                    read_times.append(time.monotonic_ns() - start)
                    if n != BLOCK or hashlib.sha256(page).digest() != expected_page:
                        raise RuntimeError(f"random read mismatch at offset {off}")
                finally:
                    page.release()
            latencies[label] = percentiles(read_times)
        read_verified(fd, block, expected)
        return {
            "sequential_with_final_sync_mib_s": mib / (sequential_ns / 1e9),
            "random_write_batch_with_final_sync_iops": len(offsets) / (random_batch_ns / 1e9),
            "random_write_fsync_latency": percentiles(tails),
            "random_read_4k_latency": latencies,
            "bytes": size, "sync_operations": len(offsets),
        }
    finally:
        os.close(fd)
        block.close()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("directory", type=Path, help="new scratch directory on the tested filesystem")
    p.add_argument("--mib", type=int, default=64)
    p.add_argument("--syncs", type=int, default=128)
    p.add_argument("--trials", type=int, default=3)
    p.add_argument("--load", required=True, help="describe competing traffic for this run")
    a = p.parse_args()
    if min(a.mib, a.syncs, a.trials) < 1:
        p.error("mib, syncs and trials must be positive")
    a.directory.mkdir()
    result = {
        "environment": {
            "workspace_mount": subprocess.check_output(["findmnt", "-T", str(a.directory), "-n", "-o", "FSTYPE,SOURCE"], text=True).strip(),
            "visible_block_devices": subprocess.check_output(["lsblk", "-d", "-n", "-o", "NAME,TYPE,TRAN,SIZE"], text=True).strip(),
            "nvme": sorted(p.name for p in Path("/sys/class/nvme").glob("nvme*")),
            "cpu_quota": Path("/sys/fs/cgroup/cpu.max").read_text().strip(),
        },
        "load": a.load, "buffered": [], "direct": [],
    }
    for i in range(a.trials):
        for direct in (bool(i % 2), not bool(i % 2)):
            mode = "direct" if direct else "buffered"
            file = a.directory / f"{mode}-{i}.dat"
            try:
                row = measure(file, a.mib, a.syncs, direct, i)
            except OSError as exc:
                if not direct or exc.errno not in (22, 95):
                    raise
                row = {"unavailable": str(exc), "errno": exc.errno}
            finally:
                file.unlink(missing_ok=True)
            result[mode].append(row)
    with (a.directory / "measurements.json").open("x") as out:
        json.dump(result, out, indent=2, sort_keys=True)
        out.write("\n")


if __name__ == "__main__":
    main()
