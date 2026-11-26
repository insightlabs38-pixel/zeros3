"""Replay the independent C1 snapshot and exact-restore baseline on C14."""
import argparse
import json
from pathlib import Path
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/"c1"))
from backup import run_test


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("restic", "corpus", "work", "results"):
        p.add_argument("--"+name, type=lambda value: Path(value).resolve(), required=True)
    args = p.parse_args()
    bin_dir = args.work/"bin"
    bin_dir.mkdir(parents=True)
    (bin_dir/"restic").symlink_to(args.restic)
    (args.work/"corpus").symlink_to(args.corpus, target_is_directory=True)
    version = subprocess.check_output([args.restic, "version"], text=True).strip()
    with args.results.open("x") as out:
        for family in ("sqlite", "volume", "volume-incremental"):
            for row in run_test("restic", args.work, args.work/family, family, None):
                print(json.dumps({"family": family, "version": version, **row}), file=out, flush=True)


if __name__ == "__main__":
    main()
