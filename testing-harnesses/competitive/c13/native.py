"""Run the native packed CDC and grouped authority records on the same objects."""
import argparse
import json
import os
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c10"))
from read_frontier import LANES, prepare, replay
from transfer import KEY, SECRET


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("baseline", "work", "corpus", "results"):
        p.add_argument("--" + n, type=Path, required=True)
    p.add_argument("--families", nargs="+", choices=("main", "heldout"), required=True)
    p.add_argument("--prepare", action="store_true")
    args = p.parse_args()
    args.lanes = ["cdc", "grouped"]
    args.candidate = None
    args.repeats = 1
    args.sizes = [4096, 0]
    args.widths = [1, 8]
    args.samples = 160
    args.full_samples = 160
    LANES["grouped"] = ["-discovery", "region"]
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        def emit(row):
            if row["stage"] == "read":
                row["build"] = "c12"
            print(json.dumps(row), file=out, flush=True)
        for family in args.families:
            (prepare if args.prepare else replay)(args, env, family, emit)


if __name__ == "__main__":
    main()
