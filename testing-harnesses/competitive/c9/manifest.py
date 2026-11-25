"""Build the final reproducibility manifest from the pinned binaries and traces."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess


def file_row(path):
    with path.open("rb") as stream:
        return {"bytes": path.stat().st_size, "sha256": hashlib.file_digest(stream, "sha256").hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--engine-head", required=True)
    parser.add_argument("--main", required=True)
    parser.add_argument("--go", type=Path, required=True)
    args = parser.parse_args()
    runner = Path(__file__).resolve().parents[1]
    budget = Path("/sys/fs/cgroup")
    quota_path, mem_path = budget / "cpu.max", budget / "memory.max"
    cpu = quota_path.read_text().split() if quota_path.is_file() else []
    memory = mem_path.read_text().strip() if mem_path.is_file() else ""
    host = os.uname()
    data = {
        "version": 1,
        "engine_checkpoint": args.engine_head,
        "stable_main": args.main,
        "comparison_revisions": {
            "b10": "dcefbcb5c2539c74a0d33bfc0d6e4a380f671743",
            "c1": "fe14dfd2de3915852e143aa4c557f5596bdb47e9",
            "c2": "4bc66f3af3d1d97f90c483c2b26676d3912fbb02",
        },
        "environment": {"architecture": host.machine, "kernel": host.sysname,
                        "cpu_quota": int(cpu[0]) / int(cpu[1]) if cpu and cpu[0] != "max" else None,
                        "memory_bytes": int(memory) if memory.isdigit() else None,
                        "go": subprocess.check_output([str(args.go), "version"], text=True).strip(),
                        "paced_transfer_bytes_per_second_per_leg": 768000,
                        "s3_cache": "ordinary loopback with uncontrolled Linux page cache"},
        "inputs": {rel: file_row(runner / rel) for rel in
                   ("c1/results/corpus.json", "c1/results/codec-corpus.json", "c3/inputs.jsonl", "c5/inputs.json")},
        "binaries": {name: file_row(args.work / relative) for name, relative in
                     (("zeros3", "zeros3"), ("minio", "bin/minio"), ("rustfs", "bin/rustfs"),
                      ("restic", "bin/restic"), ("openxet", "bin/openxet"))},
        "outputs": {path.name: file_row(path) for path in sorted(args.results.iterdir()) if path.is_file()},
    }
    print(json.dumps(data, indent=2))


if __name__ == "__main__":
    main()
