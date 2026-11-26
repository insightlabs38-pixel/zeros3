"""Pair signed loopback reads with a bounded series recompression pass."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
import json
import os
from pathlib import Path
import random
import shutil
import sys
import time

compet = Path(__file__).resolve().parents[2] / "competitive"
sys.path[:0] = [str(compet / name) for name in ("c1", "c3", "c4", "c11")]
from qos import reclaim
from transfer import BUCKET, KEY, SECRET, api, put, serve


BODY = random.Random(5).randbytes(8192)
MAINT = ["-maintenance", "all", "-maintenance-interval", "0",
         "-maintenance-max-rewrite-mib", "32", "-maintenance-max-stage-mib", "32"]


def prepare(binary, root, corpus, env):
    p = root / "seed"
    with serve(binary, p, root / "seed.log", env) as (_, client, _):
        client.create_bucket(Bucket=BUCKET)
        client.put_object(Bucket=BUCKET, Key="foreground", Body=BODY)
        for path in sorted((corpus / "src-series").glob("*.go")):
            put(client, path.name, path)
    return p


def read_slots(port, method, start, slots, interval):
    client = api(f"http://127.0.0.1:{port}")
    rows = []
    for i in range(slots):
        due = start + interval * i
        time.sleep(max(0, due - time.monotonic()))
        before = time.monotonic()
        if method == "GET":
            obj = client.get_object(Bucket=BUCKET, Key="foreground")
            with obj["Body"] as body:
                if body.read() != BODY:
                    raise ValueError("foreground bytes differ")
        elif client.head_object(Bucket=BUCKET, Key="foreground")["ContentLength"] != len(BODY):
            raise ValueError("foreground length differs")
        rows.append((before - start, (time.monotonic() - before) * 1000,
                     (before - due) * 1000))
    return rows


def lane(args, root, seed, env, mode):
    root.mkdir()
    store = root / "store"
    shutil.copytree(seed, store)
    flags = [*MAINT]
    if mode != "idle":
        flags += ["-maintenance-recompress-raw", "-background-yield",
                  "adaptive" if mode == "duty" else mode]
    if mode == "duty":
        flags += ["-maintenance-duty-percent", str(args.duty)]
    with serve(args.binary, store, root / "service.log", env, *flags) as (port, _, _):
        with ThreadPoolExecutor(max_workers=2) as pool:
            start = time.monotonic() + .2
            epoch = time.time() + start - time.monotonic()
            reads = {verb: pool.submit(read_slots, port, verb, start, args.slots, .01)
                     for verb in ("GET", "HEAD")}
            time.sleep(max(0, start + .25 - time.monotonic()))
            rep = reclaim(port) if mode != "idle" else None
            reads = {verb: t.result() for verb, t in reads.items()}
    shutil.rmtree(store)
    if rep is not None:
        if not rep["bytes_written"] or rep.get("deferred"):
            raise ValueError(f"missing recompression {rep}")
        bounds = [datetime.fromisoformat(rep[k]).timestamp() - epoch
                  for k in ("started", "finished")]
    else:
        bounds = None
    return reads, rep, bounds


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--corpus", type=Path, required=True)
    p.add_argument("--work", type=Path, required=True)
    p.add_argument("--pairs", type=int, default=5)
    p.add_argument("--slots", type=int, default=360)
    p.add_argument("--duty", type=int, default=25)
    p.add_argument("--modes", nargs="+", choices=("idle", "off", "adaptive", "duty"),
                   default=("idle", "off", "adaptive", "duty"))
    args = p.parse_args()
    args.work.mkdir()
    env = dict(os.environ, AWS_ACCESS_KEY_ID=KEY, AWS_SECRET_ACCESS_KEY=SECRET,
               GOMAXPROCS="2", ZEROS3_JOURNAL_FORMAT="")
    seed = prepare(args.binary, args.work, args.corpus, env)
    for pair in range(args.pairs):
        order = list(args.modes)
        if pair % 2:
            order.reverse()
        for mode in order:
            reads, rep, bounds = lane(args, args.work / f"{pair}-{mode}", seed, env, mode)
            if bounds and (bounds[0] < 0 or bounds[1] > args.slots * .01):
                raise ValueError(f"measurements do not fully cover pass window {bounds}")
            print(json.dumps({"pair": pair, "mode": mode, "reads": reads, "pass": rep,
                              "pass_window_s": bounds}), flush=True)


if __name__ == "__main__":
    main()
