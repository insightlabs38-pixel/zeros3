"""Run the existing compact namespace probe on bounded metadata-only stores."""
import argparse
import json
import os
from pathlib import Path
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c3"))
from transfer import run


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("root-tests", "zeros3", "scratch", "results"):
        parser.add_argument("--" + arg, type=Path, required=True)
    parser.add_argument("--counts", nargs="+", type=int, default=[1000, 10000, 100000])
    args = parser.parse_args()
    if any(n < 1 or n > 1000000 for n in args.counts):
        parser.error("bounded namespace probe accepts up to 1,000,000 entries")
    args.scratch.mkdir(parents=True, exist_ok=True)
    base_env = {**os.environ, "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for count in args.counts:
            with tempfile.TemporaryDirectory(dir=args.scratch) as tmp:
                env = {**base_env, "ZEROS3_B3_N": str(count), "TMPDIR": tmp}
                store = Path(tmp) / "namespace-only"
                build = run(args.root_tests, {**env, "ZEROS3_B3_BUILD": str(store)},
                            ["-test.run", "^TestB3BuildFixture$", "-test.v"])
                publish = run(args.zeros3, env, ["namespace-checkpoint", "-store", str(store)])
                for stage, data in (("build", build), ("checkpoint", publish)):
                    print(json.dumps({"entries": count, "stage": stage, **data}), file=out, flush=True)
                for mode in ("checkpoint", "journal"):
                    data = run(args.root_tests,
                               {**env, "ZEROS3_B3_PROBE": str(store), "ZEROS3_B3_MODE": mode},
                               ["-test.run", "^TestB3Probe$"])
                    raw = next(line[len("B3RESULT "):] for line in data.pop("stdout").splitlines()
                               if line.startswith("B3RESULT "))
                    print(json.dumps({"entries": count, "stage": "probe", **data, **json.loads(raw)}),
                          file=out, flush=True)


if __name__ == "__main__":
    main()
