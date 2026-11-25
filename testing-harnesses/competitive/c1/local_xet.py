"""Start isolated OpenXet and exercise the same official dataset driver."""
import argparse
import json
import os
from pathlib import Path
import select
import shutil
import subprocess

from xet import run


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("scratch", type=Path)
    parser.add_argument("result", type=Path)
    args = parser.parse_args()
    args.scratch.mkdir(mode=0o700)
    args.store = args.scratch / "store"
    args.interface = "lo"
    args.resources = ["dataset"]
    env = {**os.environ, "RAYON_NUM_THREADS": "2", "TOKIO_WORKER_THREADS": "2"}
    with (args.scratch / "server.log").open("w") as log, args.result.open("x") as out:
        with subprocess.Popen([str(args.work / "bin/openxet"), "serve", "--data-dir",
                               str(args.store), "--port", "0", "--json"],
                              stdout=subprocess.PIPE, stderr=log, text=True, env=env) as proc:
            try:
                if not select.select([proc.stdout], [], [], 20)[0]:
                    raise TimeoutError("OpenXet startup did not yield an endpoint")
                info = json.loads(proc.stdout.readline())
                args.endpoint = info["endpoint"]
                if not args.endpoint.startswith("http://127.0.0.1:"):
                    raise ValueError("expected loopback OpenXet endpoint")
                run(args, info["token"], out)
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                if args.store.exists():
                    shutil.rmtree(args.store)


if __name__ == "__main__":
    main()
