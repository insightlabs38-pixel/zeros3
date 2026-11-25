"""Disposable single-node smoke suite for Linux and emulated Linux ARM64."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
import os
from pathlib import Path
import random
import signal
import sys
import tempfile
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from botocore.auth import S3SigV4Auth
from botocore.awsrequest import AWSRequest, AWSResponse
from botocore.credentials import Credentials
from botocore.exceptions import ClientError
from urllib3.response import HTTPResponse

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c3"))
from transfer import BUCKET, KEY, SECRET, allocated, api, file_digest, put, run, serve, verified


def endpoint(port):
    return f"http://127.0.0.1:{port}"


def signed_json(port, method, path):
    url = endpoint(port) + path
    request = AWSRequest(method=method, url=url, data=b"")
    S3SigV4Auth(Credentials(KEY, SECRET), "s3", "us-east-1").add_auth(request)
    with urlopen(Request(url, method=method, headers=dict(request.headers.items())), timeout=8) as response:
        return json.load(response)


def status(operation, number):
    try:
        operation()
    except (ClientError, HTTPError) as error:
        actual = error.code if isinstance(error, HTTPError) else error.response["ResponseMetadata"]["HTTPStatusCode"]
        if actual == number:
            return
        raise
    raise ValueError(f"expected HTTP {number}")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def exact(store, key, payload, byte_range=None):
    opts = {"Range": byte_range} if byte_range else {}
    obj = store.get_object(Bucket=BUCKET, Key=key, **opts)
    with obj["Body"] as body:
        if sha(body.read()) != sha(payload):
            raise ValueError(f"restored wrong bytes: {key}")
    return obj


def interoperability(port, s3, payload):
    metadata = {"revision": "first"}
    response = s3.put_object(Bucket=BUCKET, Key="data/base", Body=payload,
                             ContentType="application/octet-stream", Metadata=metadata)
    if {k.lower(): v for k, v in exact(s3, "data/base", payload)["Metadata"].items()} != metadata:
        raise ValueError("S3 metadata mismatch")
    exact(s3, "data/base", payload[17:544], "bytes=17-543")
    status(lambda: s3.put_object(Bucket=BUCKET, Key="data/base", Body=b"conflict",
                                 IfNoneMatch="*"), 412)
    status(lambda: s3.get_object(Bucket=BUCKET, Key="data/base", IfMatch='"obsolete"'), 412)
    status(lambda: s3.get_object(Bucket=BUCKET, Key="data/base", IfNoneMatch=response["ETag"]), 304)
    status(lambda: s3.get_object(Bucket=BUCKET, Key="data/base", Range="bytes=999999999-"), 416)
    status(lambda: urlopen(endpoint(port) + f"/{BUCKET}/data/base", timeout=5), 403)
    s3.copy_object(Bucket=BUCKET, Key="data/copy", CopySource={"Bucket": BUCKET, "Key": "data/base"})
    exact(s3, "data/copy", payload)
    initial = s3.list_objects_v2(Bucket=BUCKET, Prefix="data/", MaxKeys=1)
    following = s3.list_objects_v2(Bucket=BUCKET, Prefix="data/", MaxKeys=1,
                                   ContinuationToken=initial["NextContinuationToken"])
    keys = [row["Contents"][0]["Key"] for row in (initial, following)]
    if keys != ["data/base", "data/copy"] or following["IsTruncated"]:
        raise ValueError(f"unexpected pages: {keys}")
    s3.delete_object(Bucket=BUCKET, Key="data/copy")
    status(lambda: s3.head_object(Bucket=BUCKET, Key="data/copy"), 404)
    return {"checks": 11, "sha256": sha(payload)}

def retry_head(port, payload):
    retrying = api(endpoint(port), attempts=2)
    sent = 0

    def transient_failure(request, **_):
        nonlocal sent
        sent += 1
        if sent == 1:
            return AWSResponse(request.url, 503, {},
                               HTTPResponse(body=io.BytesIO(), preload_content=False))
        return None

    retrying.meta.events.register("before-send.s3.HeadObject", transient_failure)
    reply = retrying.head_object(Bucket=BUCKET, Key="data/base")
    if sent != 2 or reply["ContentLength"] != len(payload):
        raise ValueError("S3 client did not recover from one injected HTTP 503")
    return {"attempts": sent, "verified_size": reply["ContentLength"]}


def backup(restic, env, work, port, stage, snapshot=None):
    cfg = {**env, "RESTIC_REPOSITORY": f"s3:{endpoint(port)}/backup",
           "RESTIC_PASSWORD": "c4-local-backup-test", "RESTIC_CACHE_DIR": str(work / "restic-cache")}
    src = work / "backup-input"
    if stage == "backup":
        src.mkdir()
        (src / "history").write_bytes(random.Random(92).randbytes(419430))
        (src / "metadata").write_text("record 0\n" * 8096)
        out = [run(restic, cfg, ["init"])]
        digest = []
        for i in range(2):
            if i:
                (src / "metadata").write_text("record 1\n" * 8096)
                (src / "new").write_text("verified S3 backup")
            row = run(restic, cfg, ["backup", "--json", str(src)])
            summary = next(r for r in map(json.loads, row.pop("stdout").splitlines())
                           if r["message_type"] == "summary")
            row["summary"] = summary
            out.append(row)
            digest.append({path.name: file_digest(path) for path in src.iterdir()})
        return out, digest

    results, digest = snapshot
    restored = []
    for i, row in enumerate(results[1:]):
        target = work / f"restored-{i}"
        command = run(restic, cfg, ["restore", row["summary"]["snapshot_id"], "--target", str(target)])
        restored_dir = target / src.relative_to("/")
        if {p.name: file_digest(p) for p in restored_dir.iterdir()} != digest[i]:
            raise ValueError("restic restore checksum mismatch")
        restored.append(command)
    return {"backup": results, "restore": restored, "verified_revisions": len(restored)}


def operations(args, work, emit):
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    log = work / "serve.log"
    store, restored = work / "store", work / "bundle-store"
    fixture = work / "latest.bin"
    start_payload = random.Random(17).randbytes(1300000)
    revised = start_payload[:580000] + random.Random(18).randbytes(8192) + start_payload[588192:]
    fixture.write_bytes(revised)
    began = time.monotonic()
    with serve(args.binary, store, log, env) as (port, s3, _):
        emit("initialize_serve", {"seconds": time.monotonic() - began})
        s3.create_bucket(Bucket=BUCKET)
        emit("boto3_core", interoperability(port, s3, start_payload))
        emit("boto3_retry", retry_head(port, start_payload))
        put(s3, "data/latest", fixture, {"revision": "new"})
        s3.put_object(Bucket=BUCKET, Key="trash", Body=random.Random(19).randbytes(40000))
        s3.delete_object(Bucket=BUCKET, Key="trash")
    emit("graceful_stop", {"allocated": allocated(store)})
    command = ["authority", "optimize", "-profile", "archive", "-order", "oldest",
               "-apply", "-finalize", "-min-saving", ".02", "-store", str(store)]
    optimized = run(args.binary, env, command, timeout=150)
    optimization = json.loads(optimized.pop("stdout"))
    if optimization.get("failed", 0) != 0:
        raise ValueError(f"archive finalization failed: {optimization}")
    emit("authority_finalize", {**optimized, "changes": [c["to"] for c in optimization.get("changes", [])],
                                 "allocated": allocated(store)})

    began = time.monotonic()
    with serve(args.binary, store, log, env, "-maintenance", "all", "-maintenance-interval", "0") as (port, s3, _):
        emit("restart", {"seconds": time.monotonic() - began})
        verified(s3, "data/latest", fixture, {"revision": "new"})
        exact(s3, "data/latest", revised[65536:69632], "bytes=65536-69631")
        start = time.monotonic()
        with ThreadPoolExecutor(max_workers=3) as pool:
            reads = [pool.submit(exact, s3, "data/latest", revised) for _ in range(12)]
            signed_json(port, "POST", "/_zeros3/v1/maintenance")
            while True:
                maint = signed_json(port, "GET", "/_zeros3/v1/maintenance")
                if maint["passes"] and not maint["running"]:
                    break
                if time.monotonic() - start > 15:
                    raise TimeoutError("online maintenance")
                time.sleep(.02)
            for read in reads:
                read.result()
        emit("online_maintenance", {"seconds": time.monotonic() - start, "exact_reads": len(reads),
                                    "report": maint["last"]})
        snapshot = run(args.binary, env, ["snapshot", "create", "-endpoint", endpoint(port),
                                          "-json", f"s3://{BUCKET}/data/"])
        manifest = json.loads(snapshot.pop("stdout"))
        emit("snapshot", {**snapshot, "objects": manifest["object_count"],
                          "logical_bytes": manifest["logical_bytes"]})
        snapshot_id = manifest["snapshot_id"]
        if args.sdk:
            result = run(args.sdk, env, ["-endpoint", endpoint(port), "-bucket", BUCKET,
                                         "-key", "data/latest", "-original", str(fixture)])
            passed = json.loads(result.pop("stdout"))["passed"]
            emit("aws_sdk_go_v2", {**result, "passed": passed})
        if args.restic:
            s3.create_bucket(Bucket="backup")
            restic_snapshots = backup(args.restic, env, work, port, "backup")
            emit("restic_backup", {"revisions": len(restic_snapshots[0]) - 1})

    for name, flags in (
        ("verify", ["verify", "-json"]), ("verify_deep", ["verify", "-deep", "-json"]),
        ("checkpoint", ["namespace-checkpoint"]), ("authority_revert", ["authority", "revert", "-all"]),
    ):
        result = run(args.binary, env, [*flags, "-store", str(store)])
        emit(name, {**result, "allocated": allocated(store)})
    bundle = work / "snapshot.zs3b"
    emit("bundle_export", run(args.binary, env, ["bundle", "export", "-store", str(store), "-snapshot",
                                                  snapshot_id, "-out", str(bundle), "-json"]))
    emit("bundle_verify", run(args.binary, env, ["bundle", "inspect", "-in", str(bundle), "-verify"]))
    emit("bundle_import", run(args.binary, env, ["bundle", "import", "-store", str(restored), "-in", str(bundle)]))
    with serve(args.binary, restored, log, env) as (port, s3, _):
        s3.create_bucket(Bucket=BUCKET)
        emit("snapshot_restore", run(args.binary, env, ["snapshot", "restore", "-endpoint", endpoint(port),
                                                         snapshot_id, f"s3://{BUCKET}/"]))
        exact(s3, "base", start_payload)
        exact(s3, "latest", revised)
    emit("restored_verify", run(args.binary, env, ["verify", "-deep", "-store", str(restored)]))

    with serve(args.binary, store, log, env) as (port, s3, _):
        verified(s3, "data/latest", fixture, {"revision": "new"})
        if args.restic:
            emit("restic_restore", backup(args.restic, env, work, port, "restore", restic_snapshots))
        repair_store = work / "repair"
        loose = {**env, "ZEROS3_SMALL_OBJECT_STORAGE": "off", "ZEROS3_DIRECT_PACK_MIB": "0"}
        peer_payload = random.Random(37).randbytes(250000)
        s3.put_object(Bucket=BUCKET, Key="repair", Body=peer_payload)
        with serve(args.binary, repair_store, log, loose) as (repair_port, damaged, _):
            damaged.create_bucket(Bucket=BUCKET)
            damaged.put_object(Bucket=BUCKET, Key="damaged", Body=peer_payload)
            descriptor = signed_json(repair_port, "GET", f"/_zeros3/v1/object?bucket={BUCKET}&key=damaged")
        digest = descriptor["chunks"][0]["sha256"]
        victim = repair_store / "chunks" / digest[:2] / digest[2:4] / digest
        if file_digest(victim) != digest:
            raise ValueError("test repair chunk differs")
        victim.write_bytes(bytes(victim.stat().st_size))
        result = run(args.binary, loose, ["repair", "-store", str(repair_store), "-from", endpoint(port), "-json"])
        detail = json.loads(result.pop("stdout"))
        if detail["repaired"] != 1 or not detail["post_repair_ok"]:
            raise ValueError(f"peer repair did not restore an exact chunk: {detail}")
        emit("peer_repair", {**result, "repaired": detail["repaired"], "sha256": digest})
        with serve(args.binary, repair_store, log, loose) as (_, repaired, _):
            exact(repaired, "damaged", peer_payload)

    crash = work / "interrupted"
    with serve(args.binary, crash, log, env) as (_, s3, pid):
        s3.create_bucket(Bucket=BUCKET)
        put(s3, "durable", fixture)
        os.kill(pid, signal.SIGKILL)
    start = time.monotonic()
    with serve(args.binary, crash, log, env) as (_, recovered, _):
        verified(recovered, "durable", fixture)
        emit("post_ack_sigkill", {"restart_verified_seconds": time.monotonic() - start,
                                  "sha256": file_digest(fixture)})
    run(args.binary, env, ["verify", "-deep", "-store", str(crash)])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--scratch", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    parser.add_argument("--sdk", type=Path)
    parser.add_argument("--restic", type=Path)
    args = parser.parse_args()
    args.scratch.mkdir(parents=True, exist_ok=True)
    args.binary = args.binary.resolve()
    with tempfile.TemporaryDirectory(dir=args.scratch) as tmp, args.results.open("x") as result:
        def emit(phase, evidence):
            print(json.dumps({"phase": phase, **evidence}), file=result, flush=True)
        operations(args, Path(tmp), emit)


if __name__ == "__main__":
    main()
