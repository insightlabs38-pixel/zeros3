"""Compare exact compressed inputs with standalone zstd base and patch files."""

import hashlib
import json
import os
from pathlib import Path
import resource
import subprocess
import tempfile
import time


def digest(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def cpu_seconds():
    usage = resource.getrusage(resource.RUSAGE_CHILDREN)
    return usage.ru_utime + usage.ru_stime


def zstd(*args):
    start = time.monotonic_ns()
    cpu = cpu_seconds()
    subprocess.run(["zstd", "-q", "-f", *args], check=True)
    return time.monotonic_ns() - start, round((cpu_seconds() - cpu) * 1e9)


def main():
    harness = Path(__file__).resolve().parents[1]
    fixture_root = Path(os.environ["ZEROS3_GF0_FIXTURES"])
    print(subprocess.check_output(["zstd", "--version"], text=True).strip())
    development = json.loads((harness / "GF0/corpus.json").read_text())
    holdout = json.loads((harness / "GF2/heldout.json").read_text())
    pairs = [family for family in development if family["name"] == "typescript-gzip"] + holdout
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        packed_base = root / "base.zst"
        packed_target = root / "target.zst"
        restored_base = root / "base"
        restored_target = root / "target"
        for pair in pairs:
            base, target = [fixture_root / path for path in pair["paths"][:2]]
            for path, pin in zip((base, target), pair["inputs"]):
                if path.stat().st_size != pin["bytes"] or digest(path) != pin["sha256"]:
                    raise RuntimeError(f"Fixture does not match its manifest: {path}")
            for level in (6, 19):
                args = (f"-{level}", "-T1", "--long=25")
                base_encode = zstd(*args, base, "-o", packed_base)
                target_encode = zstd(*args, f"--patch-from={base}", target, "-o", packed_target)
                base_decode = zstd("-d", packed_base, "-o", restored_base)
                target_decode = zstd("-d", f"--patch-from={restored_base}", packed_target, "-o", restored_target)
                for path, pin in zip((restored_base, restored_target), pair["inputs"]):
                    if digest(path) != pin["sha256"]:
                        raise RuntimeError(f"Restored bytes differ: {path}")
                files = [packed_base.stat(), packed_target.stat()]
                row = {
                    "family": pair["name"],
                    "level": level,
                    "output_bytes": [stat.st_size for stat in files],
                    "allocated": sum(stat.st_blocks * 512 for stat in files),
                    "encode_wall_cpu_ns": [base_encode, target_encode],
                    "decode_wall_cpu_ns": [base_decode, target_decode],
                    "target_range": False,
                }
                print("GF8ZSTD", json.dumps(row, sort_keys=True))


if __name__ == "__main__":
    main()
