"""Verify S3 object persistence across a normal server restart."""
import argparse
import json
import os
from pathlib import Path
import shutil

from corpus import blob
from local_s3 import SYSTEMS, put, server, verified


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("result", type=Path)
    parser.add_argument("--systems", nargs="+", choices=SYSTEMS, default=SYSTEMS)
    args = parser.parse_args()
    data = blob(6182, 128 * 1024)
    os.environ["C1_WEED_VOLUMES"] = "8"
    with args.result.open("x", buffering=1) as out:
        for name in args.systems:
            run = args.work / "runs" / f"restart-{name}"
            row = {"system": name, "phase": "restart", "logical_bytes": len(data)}
            try:
                with server(name, args.work, run) as (api, _, _):
                    put(api, "restart", data)
                with server(name, args.work, run) as (api, _, _):
                    verified(api, "restart", data)
                row["status"] = "verified"
            except Exception as exc:
                row.update(status="error", error=str(exc))
            finally:
                if run.exists():
                    shutil.rmtree(run)
            print(json.dumps(row), file=out)


if __name__ == "__main__":
    main()
