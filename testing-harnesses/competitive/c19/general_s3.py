"""Compare ordinary signed S3 on matched small, medium and large objects."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from hashlib import sha256
import json
from pathlib import Path
import random
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/"c1"))
from local_s3 import BUCKET, allocation, measure, sample, server


def fixture():
    rng = random.Random(19017)
    primary = {}
    for group, count, size in (("small", 48, 4096), ("medium", 18, 256<<10), ("large", 4, 4<<20)):
        for i in range(count):
            key = f"{group}/{i:03d}"
            if i % 3:
                data = rng.randbytes(size)
            else:
                block = rng.randbytes(256)
                data = block*(size//len(block))
            primary[key] = data
    writes = {}
    for key in list(primary)[:24]+list(primary)[48:55]:
        prior = primary[key]
        writes[key] = b"C19 exact edit"+prior[14:]
    return primary, writes


def upload(s3, key, payload):
    r = s3.put_object(Bucket=BUCKET, Key=key, Body=payload, ContentType="application/octet-stream",
                      Metadata={"fixture": "c19"})
    if not r.get("ETag"):
        raise ValueError("S3 PUT returned no ETag")
    return len(payload)


def measured(pool, pid, function, requests):
    before = sample(pid)
    perf = measure(pool, requests, function)
    after = sample(pid)
    return {**perf, "server_cpu_seconds": after["cpu_seconds"]-before["cpu_seconds"],
            "server_rss_bytes": after["peak_rss_bytes"],
            "server_disk_write_bytes": after["disk_write_bytes"]-before["disk_write_bytes"]}


def metadata_matches(response):
    return any(k.lower() == "fixture" and v == "c19" for k, v in response["Metadata"].items())


def trial(args, system, pair, work, primary, writes, emit):
    expectation = {**primary, **writes}
    keyset = sorted(expectation)
    lane = "zeros3" if system in ("c9", "integrated") else system
    tool = work/"tools"
    if lane == "zeros3":
        tool.mkdir(parents=True)
        (tool/"zeros3").symlink_to(args.c9 if system == "c9" else args.integrated)
    else:
        tool = args.competitors
    keep = work/"run"

    def output(phase, **values):
        emit({"engine": system, "pair": pair, "phase": phase, **values})

    with server(lane, tool, keep) as (api, pid, store), ThreadPoolExecutor(max_workers=4) as pool:
        output("immutable-put", **measured(pool, pid, lambda *x: upload(api, *x), list(primary.items())))
        immutable_bytes = allocation(store)
        output("mutable-put", **measured(pool, pid, lambda *x: upload(api, *x), list(writes.items())))

        range_metadata = []

        def get(key, start, n, digest):
            headers = {"Range": f"bytes={start}-{start+n-1}"} if n else {}
            resp = api.get_object(Bucket=BUCKET, Key=key, **headers)
            metadata_ok = metadata_matches(resp)
            range_metadata.append(metadata_ok)
            with resp["Body"] as body:
                got = sha256(body.read()).digest()
            if got != digest:
                raise ValueError(f"S3 range or whole digest differs for {key}")
            return n or len(expectation[key])

        def reads(size, count, group=None):
            rng = random.Random(190421+size)
            req = []
            selected = [k for k in keyset if len(expectation[k]) >= size and
                        (group is None or k.startswith(group+"/"))]
            for i in range(count):
                key = selected[i % len(selected)]
                data = expectation[key]
                start = rng.randrange(len(data)-size+1) if size else 0
                expected = data[start:start+size] if size else data
                req.append((key, start, size, sha256(expected).digest()))
            range_metadata.clear()
            timing = measured(pool, pid, get, req)
            output("reads", range_bytes=size, group=group, metadata_present=sum(range_metadata), **timing)

        for size, count in ((4096, 240), (64<<10, 144), (0, 72)):
            reads(size, count)
        for group in ("small", "medium", "large"):
            reads(4096, 120, group)
            reads(0, 48, group)

        head_metadata = []

        def head(key):
            item = api.head_object(Bucket=BUCKET, Key=key)
            if item["ContentLength"] != len(expectation[key]):
                raise ValueError(f"HEAD differs for {key}")
            head_metadata.append(metadata_matches(item))
            return 0

        timing = measured(pool, pid, head, [(x,) for x in keyset])
        output("head", metadata_present=sum(head_metadata), **timing)
        def listed():
            pages = list(api.get_paginator("list_objects_v2").paginate(
                Bucket=BUCKET, PaginationConfig={"PageSize": 13}))
            if [obj["Key"] for p in pages for obj in p.get("Contents", [])] != keyset:
                raise ValueError("ordinary paginated LIST differs")
            return 0

        timing = measured(pool, pid, listed, [()]*24)
        output("list", keys=len(keyset), page_size=13, immutable_allocated=immutable_bytes, **timing)
    finalized = allocation(store)
    begin = time.perf_counter()
    with server(lane, tool, keep) as (api, _, _):
        restart = time.perf_counter()-begin
        head_metadata.clear()
        for key in keyset:
            head(key)
            with api.get_object(Bucket=BUCKET, Key=key)["Body"] as data:
                if sha256(data.read()).digest() != sha256(expectation[key]).digest():
                    raise ValueError("restart read differs")
    output("restart", verified_objects=len(keyset), restart_seconds=restart, metadata_present=sum(head_metadata),
           finalized_allocated=finalized, mutable_bytes=sum(map(len, writes.values())),
           immutable_logical=sum(map(len, primary.values())))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("c9", "integrated", "competitors", "work", "results"):
        p.add_argument("--"+name, type=lambda name: Path(name).resolve(), required=True)
    args = p.parse_args()
    args.work.mkdir(parents=True)
    data, writes = fixture()
    engines = ("c9", "integrated", "minio", "rustfs")
    with args.results.open("x") as out:
        for pair in (0, 1):
            for name in engines[::1 if pair == 0 else -1]:
                trial(args, name, pair, args.work/f"{pair}-{name}", data, writes,
                      lambda row: print(json.dumps(row), file=out, flush=True))


if __name__ == "__main__":
    main()
