"""Run bounded C1 S3 operations and immutable-key history comparisons."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import hashlib
import json
import math
import os
from pathlib import Path
import random
import re
import resource
import shutil
import socket
import subprocess
import time
from threading import Event, Thread

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

from corpus import FAMILIES, MIB, blob

PORT = 19800
BUCKET = "c1-bench"
KEY = "GK0123456789abcdef01234567"
SECRET = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
SYSTEMS = ("zeros3", "rustfs", "seaweed", "garage", "minio")


def allocation(path):
    return sum(p.lstat().st_blocks * 512 for p in [path, *path.rglob("*")])


def sample(pid):
    base = Path("/proc", str(pid))
    stat = (base / "stat").read_text().rsplit(")", 1)[1].split()
    io = {k: int(v) for k, v in (l.split(":") for l in (base / "io").read_text().splitlines())}
    status = dict(l.split(":", 1) for l in (base / "status").read_text().splitlines())
    return {"cpu_seconds": sum(map(int, stat[11:13])) / os.sysconf("SC_CLK_TCK"),
            "peak_rss_bytes": int(status["VmHWM"].split()[0]) * 1024,
            "rss_bytes": int(status["VmRSS"].split()[0]) * 1024,
            "disk_read_bytes": io["read_bytes"], "disk_write_bytes": io["write_bytes"]}


def client(concurrency):
    return boto3.client("s3", endpoint_url=f"http://127.0.0.1:{PORT}",
                        aws_access_key_id=KEY, aws_secret_access_key=SECRET,
                        region_name="us-east-1", config=Config(
                            s3={"addressing_style": "path"},
                            request_checksum_calculation="when_required",
                            response_checksum_validation="when_required",
                            retries={"total_max_attempts": 1},
                            connect_timeout=2, read_timeout=15,
                            max_pool_connections=concurrency))


@contextmanager
def server(system, work, run, representations=False):
    store = run / "store"
    store.mkdir(parents=True, exist_ok=True)
    address = f"127.0.0.1:{PORT}"
    binary = work / "bin" / system
    env = os.environ.copy()
    env.update({"AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
                "MINIO_ROOT_USER": KEY, "MINIO_ROOT_PASSWORD": SECRET,
                "RUSTFS_ACCESS_KEY": KEY, "RUSTFS_SECRET_KEY": SECRET,
                "RUSTFS_CONSOLE_ENABLE": "false", "GOMAXPROCS": "2",
                "RAYON_NUM_THREADS": "2"})
    garage = None
    if system == "zeros3":
        cmd = [str(work / "zeros3"), "serve", "-addr", address, "-store", str(store)]
        if representations:
            cmd += ["-representations", "prefer"]
    elif system == "rustfs":
        cmd = [str(binary), "--address", address, str(store)]
    elif system == "seaweed":
        config = run / "seaweed.json"
        config.write_text(json.dumps({"identities": [{"name": "c1",
            "credentials": [{"accessKey": KEY, "secretKey": SECRET}],
            "actions": ["Admin", "Read", "Write", "List", "Tagging"]}]}))
        cmd = [str(work / "bin/weed"), "server", "-s3", "-ip=127.0.0.1",
               "-ip.bind=127.0.0.1", f"-dir={store}", f"-s3.port={PORT}",
               f"-s3.config={config}", "-volume.max=2", "-master.defaultReplication=000",
               "-master.port=19801", "-filer.port=19802", "-volume.port=19803",
               "-s3.port.iceberg=0", "-s3.port.lance=0"]
    elif system == "garage":
        config = run / "garage.toml"
        config.write_text(f"""
metadata_dir = "{store}/meta"
data_dir = "{store}/data"
db_engine = "lmdb"
replication_factor = 1
metadata_fsync = true
data_fsync = true
rpc_bind_addr = "127.0.0.1:19804"
rpc_public_addr = "127.0.0.1:19804"
rpc_secret = "{SECRET}"
[s3_api]
s3_region = "us-east-1"
api_bind_addr = "{address}"
""")
        garage = [str(binary), "-c", str(config)]
        cmd = [*garage, "server"]
    elif system == "minio":
        cmd = [str(binary), "server", "--address", address,
               "--console-address", "127.0.0.1:19805", str(store)]
    else:
        raise ValueError(system)
    with (run / "service.log").open("ab") as log:
        proc = subprocess.Popen(cmd, cwd=store, env=env, stdout=log, stderr=log)
        try:
            for _ in range(150):
                if proc.poll() is not None:
                    raise RuntimeError(f"{system} exited with {proc.returncode}")
                try:
                    with socket.create_connection(("127.0.0.1", PORT), timeout=0.1):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise TimeoutError(f"{system} did not listen within 15 seconds")
            if garage:
                def admin(*args):
                    return subprocess.check_output([*garage, *args], stderr=log, timeout=20).decode()
                if not (store / "c1-provisioned").exists():
                    node = re.search(r"(?m)^([a-f0-9]{16})\b", admin("status"))[1]
                    admin("layout", "assign", "-z", "local", "-c", "1G", node)
                    admin("layout", "apply", "--version", "1")
                    admin("key", "import", "--yes", "-n", "c1", KEY, SECRET)
                    admin("bucket", "create", BUCKET)
                    admin("bucket", "allow", "--read", "--write", "--owner", BUCKET, "--key", "c1")
                    (store / "c1-provisioned").touch()
            api = client(8)
            if not garage:
                for _ in range(30):
                    try:
                        api.create_bucket(Bucket=BUCKET)
                        break
                    except ClientError as error:
                        if error.response["Error"]["Code"] in ("BucketAlreadyOwnedByYou", "BucketAlreadyExists"):
                            break
                        time.sleep(0.25)
                else:
                    api.create_bucket(Bucket=BUCKET)
            yield api, proc.pid, store
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=6)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def verified(api, key, expected, byte_range=None):
    args = {"Range": byte_range} if byte_range else {}
    with api.get_object(Bucket=BUCKET, Key=key, **args)["Body"] as body:
        actual = body.read()
    if hashlib.sha256(actual).digest() != hashlib.sha256(expected).digest():
        raise ValueError(f"checksum mismatch for {key}")
    return len(actual)


def put(api, key, data):
    api.put_object(Bucket=BUCKET, Key=key, Body=data)
    return len(data)


def measure(pool, rows, operation):
    def single(item):
        began = time.perf_counter_ns()
        size = operation(*item)
        return time.perf_counter_ns() - began, size

    began = time.perf_counter()
    observations = list(pool.map(single, rows))
    seconds = time.perf_counter() - began
    latencies = sorted(ns / 1e6 for ns, _ in observations)
    return {"seconds": seconds, "operations": len(rows),
            "logical_bytes": sum(size for _, size in observations),
            "p50_ms": latencies[math.ceil(len(rows) * .50) - 1],
            "p95_ms": latencies[math.ceil(len(rows) * .95) - 1],
            "p99_ms": latencies[math.ceil(len(rows) * .99) - 1] if len(rows) >= 100 else None}


def smoke(api):
    key = "smoke"
    payload = blob(812, 6 * MIB)
    issues = {}
    put(api, key, payload[:4096])
    verified(api, key, payload[:4096])
    try:
        uid = api.create_multipart_upload(Bucket=BUCKET, Key=key)["UploadId"]
        parts = []
        try:
            for number, part in enumerate((payload[:5 * MIB], payload[5 * MIB:]), 1):
                result = api.upload_part(Bucket=BUCKET, Key=key, UploadId=uid,
                                         PartNumber=number, Body=part)
                parts.append({"PartNumber": number, "ETag": result["ETag"]})
            api.complete_multipart_upload(Bucket=BUCKET, Key=key, UploadId=uid,
                                          MultipartUpload={"Parts": parts})
        except Exception:
            api.abort_multipart_upload(Bucket=BUCKET, Key=key, UploadId=uid)
            raise
        verified(api, key, payload)
    except (ClientError, ValueError) as exc:
        issues["multipart"] = str(exc)
    api.delete_object(Bucket=BUCKET, Key=key)
    return issues


def operations(api, pid, store, rep):
    yield {"phase": "smoke", "issues": smoke(api)}
    widths = (1, 4, 8)
    for concurrency in (*widths[rep:], *widths[:rep]):
        payloads = []
        for i in range(144):
            size = (1024, 4096, 16384, 65536, 262144, MIB)[i % 6]
            data = (blob(i, size) if i % 4 < 2 else
                    (f'{{"event":"stored","record":{i}}}\n'.encode() * (size // 25 + 2))[:size])
            payloads.append((f"c{concurrency}/{i:04}", data))
        def head(key, data):
            assert api.head_object(Bucket=BUCKET, Key=key)["ContentLength"] == len(data)
            return 0
        def listing(prefix):
            keys = api.list_objects_v2(Bucket=BUCKET, Prefix=prefix).get("Contents", [])
            assert len(keys) == len(payloads)
            return 0
        def delete(key, _):
            api.delete_object(Bucket=BUCKET, Key=key)
            return 0
        shuffled = payloads.copy()
        random.Random(rep).shuffle(shuffled)
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            for op, items, call in (
                ("put", payloads, lambda k, d: put(api, k, d)),
                ("get-sequential", payloads, lambda k, d: verified(api, k, d)),
                ("get-random", shuffled, lambda k, d: verified(api, k, d)),
                ("range", shuffled, lambda k, d: verified(api, k, d[32:288], "bytes=32-287")),
                ("head", shuffled, head),
                ("list", [(f"c{concurrency}/",)] * 20, listing),
                ("mixed", shuffled, lambda k, d: put(api, "mix/" + k, d)
                 if int(k.rsplit("/", 1)[1]) % 4 == 0 else verified(api, k, d)),
                ("delete", shuffled, delete),
            ):
                before = sample(pid)
                try:
                    result = measure(pool, items, call)
                except (ClientError, ValueError, AssertionError) as exc:
                    yield {"phase": "requests", "operation": op, "concurrency": concurrency,
                           "status": "invalid", "error": str(exc)}
                    continue
                after = sample(pid)
                result.update({k: after[k] - before[k] for k in
                               ("cpu_seconds", "disk_read_bytes", "disk_write_bytes")})
                result.update(peak_rss_bytes=after["peak_rss_bytes"], rss_bytes=after["rss_bytes"],
                              allocated_bytes=allocation(store), phase="requests",
                              operation=op, concurrency=concurrency, status="verified")
                yield result
        assert "Contents" not in api.list_objects_v2(Bucket=BUCKET, Prefix=f"c{concurrency}/")


def histories(args, family, system, run, profile):
    records = []
    with server(system, args.work, run, profile == "structured") as (api, pid, store):
        for rev in range(3):
            names = FAMILIES if family == "combined" else [family]
            rows = []
            for name in names:
                path = args.work / "corpus" / name / str(rev)
                rows.extend((f"history/{rev}/{name}/{p.relative_to(path).as_posix()}",
                             p.read_bytes()) for p in sorted(path.rglob("*")) if p.is_file())
            records.append(rows)
            before = sample(pid)
            with ThreadPoolExecutor(max_workers=1) as pool:
                result = measure(pool, rows, lambda k, d: put(api, k, d))
            after = sample(pid)
            result["server_cpu_seconds"] = after["cpu_seconds"] - before["cpu_seconds"]
            result["server_peak_rss_bytes"] = after["peak_rss_bytes"]
            yield {"phase": "ingest", "revision": rev, **result,
                   "allocated_bytes": allocation(store)}
        if profile in ("plain", "structured") or system != "zeros3":
            yield from restore(api, records, store)
            return
    binary = str(args.work / "zeros3")
    order = "oldest" if profile == "archive" else "newest"
    optimize = [binary, "authority", "optimize", "-store", str(store), "-apply",
                "-profile", profile, "-order", order, "-max-depth", "2", "-max-bases", "2",
                "-min-saving", "0.10", "-canonical", "-codec-replay", "-hard-codecs",
                "-splice", "-record-groups"]
    reuse = [binary, "gc", "-store", str(store), "-apply"]
    yield {"phase": "maintenance", **maintenance(run, store, [optimize, reuse])}
    with server(system, args.work, run) as (api, _, store):
        yield from restore(api, records, store)
    yield {"phase": "reclaim", **maintenance(run, store, [reuse])}


def maintenance(run, store, commands):
    before = allocation(store)
    peak = before
    stopped = Event()

    def monitor():
        nonlocal peak
        while not stopped.wait(.05):
            try:
                peak = max(peak, allocation(store))
            except FileNotFoundError:
                pass

    began = time.perf_counter()
    usage_before = resource.getrusage(resource.RUSAGE_CHILDREN)
    monitor_thread = Thread(target=monitor)
    monitor_thread.start()
    try:
        with (run / "service.log").open("ab") as log:
            for command in commands:
                subprocess.run(command, stdout=log, stderr=log, check=True, timeout=300)
    finally:
        stopped.set()
        monitor_thread.join()
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    actual = allocation(store)
    return {"seconds": time.perf_counter() - began,
            "cpu_seconds": after.ru_utime + after.ru_stime - usage_before.ru_utime - usage_before.ru_stime,
            "before_bytes": before, "allocated_bytes": actual,
            "sampled_peak_store_bytes": max(peak, actual)}


def restore(api, records, store):
    for rev, rows in enumerate(records):
        with ThreadPoolExecutor(max_workers=1) as pool:
            result = measure(pool, rows, lambda k, d: verified(api, k, d))
        yield {"phase": "restore", "revision": rev, **result,
               "allocated_bytes": allocation(store)}
    began = time.perf_counter()
    for key, _ in records[0]:
        api.delete_object(Bucket=BUCKET, Key=key)
    assert "Contents" not in api.list_objects_v2(Bucket=BUCKET, Prefix="history/0/")
    yield {"phase": "delete", "seconds": time.perf_counter() - began,
           "allocated_bytes": allocation(store)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("result", type=Path)
    parser.add_argument("--systems", nargs="+", choices=SYSTEMS, default=SYSTEMS)
    parser.add_argument("--families", nargs="+", choices=(*FAMILIES, "combined"), default=FAMILIES)
    parser.add_argument("--lane", choices=("requests", "histories"), required=True)
    parser.add_argument("--profiles", nargs="+", default=["plain", "archive", "mixed", "hot", "structured"])
    parser.add_argument("--repetitions", type=int, choices=(1, 2, 3), default=3)
    args = parser.parse_args()
    with args.result.open("x", buffering=1) as out:
        for rep in range(args.repetitions):
            order = (*args.systems[rep:], *args.systems[:rep])
            for system in order:
                families = args.families if args.lane == "histories" else [None]
                for family in families:
                    profiles = args.profiles if family and system == "zeros3" else ["plain"]
                    for profile in profiles:
                        run = args.work / "runs" / f"{args.result.stem}-{rep}-{system}-{family}-{profile}"
                        ids = {"system": system, "repetition": rep, "family": family, "profile": profile}
                        try:
                            if family:
                                for row in histories(args, family, system, run, profile):
                                    print(json.dumps({**ids, **row}), file=out)
                            else:
                                with server(system, args.work, run) as (api, pid, store):
                                    for row in operations(api, pid, store, rep):
                                        print(json.dumps({**ids, **row}), file=out)
                        except Exception as exc:
                            print(json.dumps({**ids, "phase": "error", "error": str(exc)}), file=out)
                            print(f"{run.name}: {exc}", flush=True)
                            if (run / "service.log").exists():
                                shutil.copyfile(run / "service.log", run.with_suffix(".log"))
                        finally:
                            if run.exists():
                                shutil.rmtree(run)


if __name__ == "__main__":
    main()
