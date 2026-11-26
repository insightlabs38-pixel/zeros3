"""Pair signed foreground reads with the same scheduled delete workload."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import sys
import threading
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "competitive" / "c3"))
from transfer import BUCKET, KEY, SECRET, api, serve


def percentiles(values):
    values.sort()
    return {"samples": len(values), **{
        f"p{p}_ms": values[min(len(values)-1, int(len(values)*p/100))]*1000
        for p in (50, 95, 99)
    }}


def trial(binary, path, pair, lane):
    path.mkdir(parents=True)
    env = dict(os.environ, AWS_ACCESS_KEY_ID=KEY, AWS_SECRET_ACCESS_KEY=SECRET,
               GOMAXPROCS="2", ZEROS3_JOURNAL_WATERMARK="lazy", ZEROS3_JOURNAL_FORMAT="")
    hot = bytes(range(256))*16
    keys = [f"delete-{i:04}" for i in range(1000)]
    with serve(binary, path/"store", path/"service.log", env) as (port, client, _):
        client.create_bucket(Bucket=BUCKET)
        client.put_object(Bucket=BUCKET, Key="hot", Body=hot)
        with ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(lambda key: client.put_object(Bucket=BUCKET, Key=key, Body=hot), keys))
        gate = threading.Barrier(3)
        stop = threading.Event()

        def read(method):
            reader = api(f"http://127.0.0.1:{port}")
            rows = []
            gate.wait()
            while not stop.is_set():
                at = time.perf_counter()
                if method == "GET":
                    obj = reader.get_object(Bucket=BUCKET, Key="hot")
                    with obj["Body"] as body:
                        if body.read() != hot:
                            raise ValueError("background GET changed")
                else:
                    obj = reader.head_object(Bucket=BUCKET, Key="hot")
                    if obj["ContentLength"] != len(hot):
                        raise ValueError("background HEAD changed")
                rows.append(time.perf_counter()-at)
            return percentiles(rows)

        elapsed = []
        with ThreadPoolExecutor(max_workers=2) as pool:
            get, head = (pool.submit(read, m) for m in ("GET", "HEAD"))
            gate.wait()
            started = time.perf_counter()
            try:
                for i in range(10):
                    deadline = started+i*0.075
                    time.sleep(max(0, deadline-time.perf_counter()))
                    at = time.perf_counter()
                    result = client.delete_objects(Bucket=BUCKET, Delete={
                        "Objects": [{"Key": key} for key in keys[100*i:100*(i+1)]]
                    })
                    elapsed.append(time.perf_counter()-at)
                    if result.get("Errors") or len(result.get("Deleted", [])) != 100:
                        raise ValueError("delete omitted or failed a key")
                time.sleep(max(0, started+0.750-time.perf_counter()))
            finally:
                stop.set()
        if [r["Key"] for r in client.list_objects_v2(Bucket=BUCKET)["Contents"]] != ["hot"]:
            raise ValueError("namespace differs after deletes")
        return {"pair": pair, "lane": lane, "delete": percentiles(elapsed),
                "read": get.result(), "head": head.result(), "keys": len(keys),
                "request_period_ms": 75}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("before", "after", "work"):
        parser.add_argument(f"--{name}", type=Path, required=True)
    args = parser.parse_args()
    for pair in range(3):
        for lane in (("before", "after") if pair % 2 == 0 else ("after", "before")):
            print(json.dumps(trial(getattr(args, lane), args.work/f"{pair}-{lane}", pair, lane)), flush=True)


if __name__ == "__main__":
    main()
