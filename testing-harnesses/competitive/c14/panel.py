"""Measure unchanged, relocated and zero page opportunity across the panel."""
import argparse
import hashlib
import json
from pathlib import Path

from fixtures import BLK


def pages(data):
    return [data[i:i+BLK] for i in range(0, len(data), BLK)]


def measure(base, target):
    indexes = {hashlib.sha256(block).digest() for block in base if any(block)}
    zeros = same = moved = 0
    for i, page in enumerate(target):
        if not any(page):
            zeros += len(page)
        elif i < len(base) and page == base[i]:
            same += len(page)
        elif hashlib.sha256(page).digest() in indexes:
            moved += len(page)
    return {"zero_bytes": zeros, "same_offset_bytes": same, "relocated_bytes": moved,
            "total_bytes": sum(map(len, target)), "pages": len(target)}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--corpus", type=Path, required=True)
    p.add_argument("--results", type=Path, required=True)
    args = p.parse_args()
    with args.results.open("x") as out:
        for family in sorted(args.corpus.iterdir()):
            paths = sorted(p for p in family.rglob("*") if p.is_file() and not p.name.endswith(("-wal", "-shm")))
            for i, (before, after) in enumerate(zip(paths, paths[1:])):
                data = measure(pages(before.read_bytes()), pages(after.read_bytes()))
                print(json.dumps({"family": family.name, "pair": i, **data}), file=out)


if __name__ == "__main__":
    main()
