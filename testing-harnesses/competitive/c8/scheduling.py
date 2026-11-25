"""Compare idle and online-maintenance latency under bounded foreground work."""
import argparse
import json
import os
from pathlib import Path
import random
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c4"))
from operations import signed_json
from stream import BUCKET, KEY, PAYLOAD, SECRET, foreground, run, serve


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "scratch", "results"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    args.binary = args.binary.resolve()
    args.scratch.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with tempfile.TemporaryDirectory(dir=args.scratch) as temp, args.results.open("x") as report:
        work = Path(temp)
        seed, log = work / "seed", work / "server.log"
        with serve(args.binary, seed, log, env) as (_, sink, _):
            sink.create_bucket(Bucket=BUCKET)
            for i in range(16):
                sink.put_object(Bucket=BUCKET, Key=f"foreground/{i}", Body=PAYLOAD)
            for i in range(16):
                payload = random.Random(616+i).randbytes(600000)
                sink.put_object(Bucket=BUCKET, Key=f"deleted/{i}", Body=payload)
                sink.delete_object(Bucket=BUCKET, Key=f"deleted/{i}")
        run(args.binary, env, ["versions", "prune", "-apply", "-keep-last", "0",
                              "-bucket", BUCKET, "-prefix", "deleted/", "-store", str(seed)])
        for round in range(2):
            order = ("idle", "maintenance") if round == 0 else ("maintenance", "idle")
            for lane in order:
                store = work / lane
                shutil.copytree(seed, store)
                with serve(args.binary, store, log, env, "-maintenance", "all", "-maintenance-interval", "0",
                           "-maintenance-max-live-percent", "100") as (port, sink, _):
                    offline = subprocess.run([str(args.binary), "authority", "optimize", "-apply", "-store",
                                              str(store)], env=env, text=True, capture_output=True, timeout=5)
                    if offline.returncode == 0 or "exclusive access" not in offline.stderr:
                        raise ValueError("offline analysis did not refuse the live writer store")
                    started = time.monotonic()
                    maintenance = None
                    with foreground(sink) as load:
                        if lane == "maintenance":
                            signed_json(port, "POST", "/_zeros3/v1/maintenance")
                            while True:
                                status = signed_json(port, "GET", "/_zeros3/v1/maintenance")
                                if status["passes"] and not status["running"]:
                                    maintenance = status["last"]
                                    break
                                if time.monotonic() - started > 15:
                                    raise TimeoutError("maintenance was still in progress")
                                time.sleep(.01)
                    row = {"round": round, "lane": lane, "foreground": load, "maintenance": maintenance,
                           "offline_while_serving": "refused", "seconds": time.monotonic() - started}
                    print(json.dumps(row), file=report, flush=True)
                run(args.binary, env, ["verify", "-deep", "-store", str(store)])
                shutil.rmtree(store)


if __name__ == "__main__":
    main()
