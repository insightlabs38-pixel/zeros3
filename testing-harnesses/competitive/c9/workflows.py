"""Exercise a versioned artifact mirror and an exact database snapshot backup."""
import argparse
from contextlib import closing
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tarfile
import tempfile
import time

from botocore.exceptions import ClientError
import pyarrow.parquet as pq
from rocksdict import Options, Rdict

base = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(base / "c3"), str(base / "c1")]
from corpus import blob
from transfer import BUCKET, KEY, SECRET, allocated, file_digest, put, run, serve, verified

ARTIFACTS = ("5.6.2", "5.6.3", "5.7.2")
OPT = ["authority", "optimize", "-apply", "-finalize", "-profile", "archive",
       "-order", "oldest", "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10",
       "-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice"]


def read_inside(s3, key, file, kind, revision, work):
    began = time.perf_counter()
    byte_range = "bytes=31-4126"
    part = s3.get_object(Bucket=BUCKET, Key=key, Range=byte_range)
    with file.open("rb") as fixture, part["Body"] as response:
        fixture.seek(31)
        if response.read() != fixture.read(4096):
            raise ValueError(f"invalid 4 KiB range: {key}")
    seconds_range = time.perf_counter() - began
    began = time.perf_counter()
    response = s3.get_object(Bucket=BUCKET, Key=key)
    with response["Body"] as content:
        exact = content.read()
    seconds_full = time.perf_counter() - began
    digest = hashlib.sha256(exact).hexdigest()
    if file_digest(file) != digest:
        raise ValueError(f"invalid full object: {key}")
    if kind == "package":
        with tarfile.open(fileobj=io.BytesIO(exact), mode="r:gz") as tar:
            entry = tar.extractfile("package/package.json")
            with entry:
                meta = json.load(entry)
        if meta["name"] != "typescript" or meta["version"] != ARTIFACTS[revision]:
            raise ValueError("unexpected package metadata")
    else:
        validate_db(exact, kind, revision, work)
    return {"key": key, "sha256": digest, "bytes": len(exact),
            "range_seconds": seconds_range, "full_seconds": seconds_full}


def validate_db(data, kind, revision, work):
    if kind == "parquet":
        parquet = pq.ParquetFile(io.BytesIO(data))
        if parquet.metadata.num_rows != 65536:
            raise ValueError("unexpected Parquet row count")
        row = revision * 512
        columns = parquet.read_row_group(0, columns=["id", "reading"])
        expected = f"updated-{revision}-{row:05}" if revision else "region-000-reading-0000"
        if columns["id"][row].as_py() != row or columns["reading"][row].as_py() != expected:
            raise ValueError("invalid Parquet revision row")
        return
    with tempfile.TemporaryDirectory(prefix="rocks-", dir=work) as tmp:
        root = Path(tmp)
        sst = root / "snapshot.sst"
        sst.write_bytes(data)
        row = revision * 512
        key = f"segment-{row // 128:04}/{row:06}".encode()
        opts = Options(raw_mode=True)
        with closing(Rdict(str(root / "db"), opts)) as db:
            db.ingest_external_file([str(sst)])
            expected = b"sample/" * 8 + blob(5000 + row + revision, 48)
            if db.get(key) != expected or revision == 2 and db.get(b"segment-0000/000000") is not None:
                raise ValueError("invalid RocksDB revision row")


def state(args, name):
    if name == "artifact":
        return [(f"{version}/artifact.tgz", args.corpus / "typescript" / str(i) / "package.tgz", "package", i)
                for i, version in enumerate(ARTIFACTS)]
    return [(f"{i}/snapshot.{kind}", args.corpus / kind / str(i) / f"snapshot.{kind}", kind, i)
            for i in range(3) for kind in ("parquet", "sst")]


def verify_inputs(args, fixtures):
    manifest = {row["path"]: row["sha256"] for row in json.loads((base / "c5" / "inputs.json").read_text())}
    for _, path, _, _ in fixtures:
        relative = path.relative_to(args.corpus).as_posix()
        if file_digest(path) != manifest[relative]:
            raise ValueError(f"input differs from frozen C5 digest: {relative}")


def workflow(args, name, work, env, emit):
    store, log = work / "store", work / "service.log"
    fixtures = state(args, name)
    verify_inputs(args, fixtures)
    live = {f"history/{key}": (path, kind, revision) for key, path, kind, revision in fixtures}
    with serve(args.binary, store, log, env) as (port, sink, pid):
        sink.create_bucket(Bucket=BUCKET)
        for key, (path, kind, revision) in live.items():
            start = time.monotonic()
            put(sink, key, path, {"revision": str(revision)})
            emit(name, "ingest", {"kind": kind, "revision": revision, "key": key,
                                  "sha256": file_digest(path), "logical_bytes": path.stat().st_size,
                                  "seconds": time.monotonic() - start})
        last = next(reversed(live))
        path, kind, revision = live[last]
        before = sink.head_object(Bucket=BUCKET, Key=last)
        put(sink, last, path, {"revision": str(revision)})
        after = sink.head_object(Bucket=BUCKET, Key=last)
        if before["ETag"] != after["ETag"]:
            raise ValueError("exact repeat changed the ETag")
        try:
            sink.put_object(Bucket=BUCKET, Key=last, Body=b"rejected update", IfNoneMatch="*")
        except ClientError as error:
            if error.response["ResponseMetadata"]["HTTPStatusCode"] != 412:
                raise
        else:
            raise ValueError("conditional write overwrote an existing revision")
        verify_tag = sink.head_object(Bucket=BUCKET, Key=last)["ETag"]
        if verify_tag != after["ETag"]:
            raise ValueError("conditional failure changed the ETag")
        pages = sink.get_paginator("list_objects_v2").paginate(Bucket=BUCKET, Prefix="history/",
                                                                PaginationConfig={"PageSize": 2})
        objects = [row["Key"] for page in pages for row in page["Contents"]]
        if objects != sorted(live):
            raise ValueError("invalid paginated history")
        emit(name, "repeat_list_failure", {"objects_listed": len(objects), "conditional_status": 412,
                                           "etag": verify_tag})
        snapshot = run(args.binary, env, ["snapshot", "create", "-json", "-endpoint",
                                            f"http://127.0.0.1:{port}", f"s3://{BUCKET}/history/"])
        snap = json.loads(snapshot["stdout"])["snapshot_id"]
        oldest = next(iter(live))
        sink.delete_object(Bucket=BUCKET, Key=oldest)
        remaining = sink.list_objects_v2(Bucket=BUCKET, Prefix=oldest).get("Contents", [])
        if remaining:
            raise ValueError("deleted revision still visible")
        os.kill(pid, signal.SIGKILL)
    with serve(args.binary, store, log, env) as (_, sink, _):
        verified(sink, last, path, {"revision": str(revision)})
        emit(name, "post_ack_restart", {"sha": file_digest(path), "deleted_key": oldest})
    pre = allocated(store)
    start = time.monotonic()
    stage = run(args.binary, env, [*OPT, "-store", str(store)], timeout=160)
    report = json.loads(stage.pop("stdout"))
    if report.get("failed", 0):
        raise ValueError("failed archive publication")
    emit(name, "full_archive", {**stage, "before_allocated": pre, "allocated": allocated(store),
                                "changes": [c["to"] for c in report.get("changes", [])],
                                "elapsed": time.monotonic() - start})
    maintenance = run(args.binary, env, ["repack", "-apply", "-max-live-percent", "100", "-recompress",
                                          "-keep-unchanged", "-json", "-store", str(store)])
    details = json.loads(maintenance.pop("stdout"))
    emit(name, "repeat_repack", {**maintenance, "allocated": allocated(store),
                                   "pack_bytes_written": details["bytes_written"],
                                   "kept_unchanged": details.get("packs_kept_unchanged", 0)})
    source = work / "backup.zs3b"
    run(args.binary, env, ["bundle", "export", "-store", str(store), "-snapshot", snap, "-out", str(source)])
    run(args.binary, env, ["bundle", "inspect", "-verify", "-in", str(source)])
    restored = work / "restored"
    run(args.binary, env, ["bundle", "import", "-store", str(restored), "-in", str(source)])
    with serve(args.binary, store, log, env) as (_, sink, _):
        for key, (expected, kind, revision) in live.items():
            if key != oldest:
                emit(name, "archive_read", read_inside(sink, key, expected, kind, revision, work))
    with serve(args.binary, restored, log, env) as (port, sink, _):
        sink.create_bucket(Bucket=BUCKET)
        command = run(args.binary, env, ["snapshot", "restore", "-endpoint",
                                          f"http://127.0.0.1:{port}", snap, f"s3://{BUCKET}/"])
        emit(name, "bundle_restore", {**command, "bundle_bytes": source.stat().st_size,
                                        "verified_objects": len(fixtures)})
        for key, expected, kind, revision in fixtures:
            emit(name, "restored_read", read_inside(sink, key, expected, kind, revision, work))
    for root in (store, restored):
        run(args.binary, env, ["verify", "-deep", "-store", str(root)])
    emit(name, "deep_verify", {"stores": 2})


def restic_workflow(args, work, env, emit):
    seed = work / "consumer"
    seed.mkdir()
    store, log = work / "restic-store", work / "restic.log"
    snapshots, wanted = [], []
    with serve(args.binary, store, log, env) as (port, s3, _):
        s3.create_bucket(Bucket="backup")
        cfg = {**env, "RESTIC_REPOSITORY": f"s3:http://127.0.0.1:{port}/backup",
               "RESTIC_PASSWORD": "c9-local-disposable", "RESTIC_CACHE_DIR": str(work / "restic-cache")}
        run(args.restic, cfg, ["init"])
        for i in range(3):
            for kind in ("parquet", "sst"):
                shutil.copyfile(args.corpus / kind / str(i) / f"snapshot.{kind}", seed / f"snapshot.{kind}")
            files = {p.name: file_digest(p) for p in seed.iterdir()}
            for p in seed.iterdir():
                # Revisions have independently verified digests and distinct mtimes.
                os.utime(p, ns=((i + 100) * 1000000000, (i + 100) * 1000000000))
            result = run(args.restic, cfg, ["backup", "--force", "--json", str(seed)])
            summary = next(r for r in map(json.loads, result.pop("stdout").splitlines()) if r["message_type"] == "summary")
            snapshots.append(summary["snapshot_id"])
            wanted.append(files)
            emit("restic", "backup", {**result, "revision": i, "summary": summary,
                                       "allocated": allocated(store)})
    with serve(args.binary, store, log, env) as (port, _, _):
        cfg["RESTIC_REPOSITORY"] = f"s3:http://127.0.0.1:{port}/backup"
        for i, snapshot in enumerate(snapshots):
            target = work / f"restic-restore-{i}"
            receipt = run(args.restic, cfg, ["restore", snapshot, "--target", str(target)])
            target = target / seed.relative_to("/")
            restored = sorted(target.iterdir())
            if {path.name: file_digest(path) for path in restored} != wanted[i]:
                raise ValueError("restic restored different files or bytes")
            for path in restored:
                validate_db(path.read_bytes(), path.suffix[1:], i, work)
            emit("restic", "restore", {**receipt, "revision": i, "verified": wanted[i]})
    run(args.binary, env, ["verify", "-deep", "-store", str(store)])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "restic", "corpus", "scratch", "results"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    for key, value in vars(args).items():
        setattr(args, key, value.resolve())
    args.scratch.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as report:
        def emit(workflow, scope, row):
            print(json.dumps({"workflow": workflow, "phase": scope, **row}), file=report, flush=True)
        for name in ("artifact", "database", "restic"):
            with tempfile.TemporaryDirectory(prefix=f"{name}-", dir=args.scratch) as tmp:
                if name == "restic":
                    restic_workflow(args, Path(tmp), env, emit)
                else:
                    workflow(args, name, Path(tmp), env, emit)


if __name__ == "__main__":
    main()
