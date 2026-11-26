"""Run C1's independent exact-restore adapter on the C19 history panel."""
import argparse
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/"c1"))
from backup import run_test


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("binaries", "corpus", "work", "results"):
        parser.add_argument("--"+name, type=lambda x: Path(x).resolve(), required=True)
    args = parser.parse_args()
    args.work.mkdir()
    (args.work/"bin").symlink_to(args.binaries)
    (args.work/"corpus").symlink_to(args.corpus)
    systems = ("restic", "kopia", "borg")
    failed = []
    with args.results.open("x") as out:
        for family in ("sqlite", "volume", "volume-incremental"):
            for pair in range(2):
                for system in systems[::1 if pair == 0 else -1]:
                    work = args.work/f"{family}-{pair}-{system}"
                    ids = {"system": system, "repetition": pair, "family": family}
                    try:
                        for row in run_test(system, args.work, work, family, "zstd-fastest"):
                            print(json.dumps({**ids, **row}), file=out, flush=True)
                    except Exception as err:
                        failed.append((family, system, str(err)))
                        print(json.dumps({**ids, "phase": "error", "error": str(err)}), file=out, flush=True)
                    finally:
                        if work.is_dir():
                            shutil.rmtree(work)
    if failed:
        raise RuntimeError(f"independent snapshot controls failed: {failed}")


if __name__ == "__main__":
    main()
