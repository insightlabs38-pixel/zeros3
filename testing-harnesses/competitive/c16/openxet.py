"""Run the official Xet client against a disposable local OpenXet dataset."""
import argparse
from pathlib import Path
import sys

harness = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(harness/"c1"))
import local_xet
import xet
from frontier import inputs


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for key in ("binary", "downloads", "c1", "c14", "work", "results"):
        p.add_argument("--"+key, type=lambda name: Path(name).resolve(), required=True)
    args = p.parse_args()
    (args.work/"bin").mkdir(parents=True)
    (args.work/"bin"/"openxet").symlink_to(args.binary)
    cases = []
    for family in ("typescript", "sqlite", "shift", "entropy", "tensor-dtypes", "mobilenet"):
        for revision, path in enumerate(inputs(args, family)):
            folder = args.work/"corpus"/family/str(revision)
            folder.mkdir(parents=True)
            (folder/path.name).symlink_to(path)
            cases.append((f"{family}-{revision}", family, revision, f"{family}/object"))
    xet.CASES = tuple(cases)
    sys.argv = [sys.argv[0], str(args.work), str(args.work/"disposable"), str(args.results)]
    local_xet.main()


if __name__ == "__main__":
    main()
