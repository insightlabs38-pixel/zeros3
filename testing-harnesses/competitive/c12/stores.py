"""Measure fully finalized CDC and generic authority histories."""
import argparse
import json
import os
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c10"))
from read_frontier import LANES, prepare, replay
from transfer import KEY, SECRET


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("baseline", "work", "corpus", "results"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--families", nargs="+", required=True)
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--samples", type=int, default=120)
    args = parser.parse_args()
    for name, val in vars(args).items():
        if isinstance(val, Path):
            setattr(args, name, val.resolve())
    args.lanes = ["cdc", "generic"]
    args.candidate = None
    args.repeats = 1
    args.sizes = [4096, 0]
    args.widths = [1]
    args.full_samples = 6
    LANES["generic"] = ["-discovery", "region"]
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        def emit(row):
            print(json.dumps(row), file=out, flush=True)
        for family in args.families:
            (prepare if args.prepare else replay)(args, env, family, emit)


if __name__ == "__main__":
    main()
