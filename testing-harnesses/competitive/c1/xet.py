"""Measure official Xet operations on a disposable hosted or loopback destination."""
import argparse
import getpass
import hashlib
import json
import os
from pathlib import Path
import resource
import shutil
import time
from urllib.parse import urlparse

from corpus import MIB
from local_s3 import allocation

CASES = (("new", "shift", 0, "shift.bin"),
         ("exact", "shift", 0, "copy.bin"),
         ("edit", "shift", 1, "shift.bin"),
         ("shift", "shift", 2, "shift.bin"),
         ("tensor", "tensor", 0, "tensor.safetensors"),
         ("tensor-edit", "tensor", 1, "tensor.safetensors"),
         ("entropy", "entropy", 0, "random.bin"),
         ("entropy-replace", "entropy", 1, "random.bin"))


def counter(interface):
    if not interface:
        return {}
    base = Path("/sys/class/net") / interface / "statistics"
    return {k: int((base / k).read_text()) for k in ("rx_bytes", "tx_bytes")}


def run(args, token, out):
    os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
    os.environ["HF_HUB_DISABLE_PROGRESS_BARS"] = "1"
    os.environ["HF_HUB_VERBOSITY"] = "error"
    os.environ["HF_HOME"] = str(args.scratch / "hf-home")
    os.environ["HF_XET_CACHE"] = str(args.scratch / "xet")
    os.environ["HF_XET_CHUNK_CACHE_SIZE_BYTES"] = str(64 * MIB)
    os.environ["HF_XET_SHARD_CACHE_SIZE_LIMIT"] = str(64 * MIB)
    os.environ["HF_XET_NUM_CONCURRENT_RANGE_GETS"] = "4"
    os.environ["HF_XET_LOG_DEST"] = str(args.scratch / "xet-log")
    from huggingface_hub import CommitOperationAdd, HfApi, get_hf_file_metadata, hf_hub_url

    api = HfApi(endpoint=args.endpoint, token=token)
    namespace = api.whoami()["name"]
    rid = f"{namespace}/storage_test"
    prefix = "c1"

    origin = counter(args.interface)
    uploaded = 0
    def measure(action, resource_type, case, logical, operation):
        nonlocal uploaded
        usage = counter(args.interface)
        global_delta = sum(usage[k] - origin[k] for k in usage)
        if global_delta + 8 * logical > 3 * 1024 * MIB:
            raise RuntimeError("transfer guard reached")
        if action == "upload":
            if uploaded + logical > 1024 * MIB:
                raise RuntimeError("logical upload guard reached")
            uploaded += logical
        started = time.monotonic()
        before = resource.getrusage(resource.RUSAGE_SELF)
        status = "verified"
        info = {}
        value = None
        try:
            value = operation()
        except Exception as error:
            status = "error"
            info = {"error_type": type(error).__name__,
                    "http_status": getattr(getattr(error, "response", None), "status_code", None)}
        after = resource.getrusage(resource.RUSAGE_SELF)
        elapsed = time.monotonic() - started
        network = counter(args.interface)
        row = {"phase": action, "resource": resource_type, "case": case,
               "logical_bytes": logical, "seconds": elapsed, "status": status,
               "client_cpu_seconds": after.ru_utime + after.ru_stime - before.ru_utime - before.ru_stime,
               "client_peak_rss_bytes": after.ru_maxrss * 1024, **info}
        row.update({f"interface_{k}": network[k] - usage[k] for k in usage})
        if args.store:
            row["server_allocated_bytes"] = allocation(args.store)
        if isinstance(value, dict):
            row.update(value)
        print(json.dumps(row), file=out, flush=True)
        return value if status == "verified" else False

    def bucket_create():
        api.create_bucket(rid, private=False, exist_ok=False)
        return {"destination": rid, "public": True}

    def dataset_create():
        api.create_repo(rid, repo_type="dataset", private=False, exist_ok=False)
        return {"destination": rid, "public": True}

    def check(source, target):
        expected = hashlib.sha256(source.read_bytes()).hexdigest()
        if hashlib.sha256(Path(target).read_bytes()).hexdigest() != expected:
            raise ValueError("download checksum mismatch")
        return {"sha256": expected}

    def bucket_download(path, local, source):
        api.download_bucket_files(rid, [(path, local)], raise_on_missing_files=True)
        return check(source, local)

    if "bucket" in args.resources and measure("create", "bucket", None, 0, bucket_create):
        bucket_cases = (CASES[0], ("same-key", "shift", 0, "shift.bin"), *CASES[1:])
        for case, family, rev, key in bucket_cases:
            source = next((args.work / "corpus" / family / str(rev)).iterdir())
            path = f"{prefix}/{key}"
            size = source.stat().st_size
            result = measure("upload", "bucket", case, size,
                             lambda: (api.batch_bucket_files(rid, add=[(source, path)]),
                                      {"key": path})[1])
            if result is False:
                break
            for mode in ("new-target", "same-target"):
                local = args.scratch / "bucket" / f"{case}.bin"
                local.parent.mkdir(exist_ok=True)
                measure("download-" + mode, "bucket", case, size,
                        lambda: bucket_download(path, local, source))
            local.unlink(missing_ok=True)

    if "dataset" in args.resources and measure("create", "dataset", None, 0, dataset_create):
        seen = []
        for case, family, rev, key in CASES:
            source = next((args.work / "corpus" / family / str(rev)).iterdir())
            path = f"{prefix}/{key}"
            size = source.stat().st_size

            def commit():
                commit_info = api.create_commit(rid, [CommitOperationAdd(path_in_repo=path,
                                                                        path_or_fileobj=source)],
                                                commit_message=f"C1 {case}", repo_type="dataset")
                return {"revision": commit_info.oid, "key": path}

            result = measure("upload", "dataset", case, size, commit)
            if result is False:
                break
            seen.append((case, source, path, result["revision"]))

        for case, source, path, revision in seen:
            size = source.stat().st_size
            url = hf_hub_url(rid, path, repo_type="dataset", revision=revision, endpoint=args.endpoint)

            def metadata():
                item = get_hf_file_metadata(url, token=token, endpoint=args.endpoint)
                return {"xet_metadata": item.xet_file_data is not None, "revision": revision}

            measure("metadata", "dataset", case, 0, metadata)
            cache = args.scratch / "hub" / case

            def download(force_download):
                downloaded = api.hf_hub_download(rid, path, repo_type="dataset", revision=revision,
                                                 cache_dir=cache, force_download=force_download)
                return {"revision": revision, **check(source, downloaded)}

            for mode, force in (("fresh-hub-cache", False), ("cached", False), ("forced", True)):
                measure("download-" + mode, "dataset", case, size, lambda: download(force))
            if cache.exists():
                shutil.rmtree(cache)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("scratch", type=Path)
    parser.add_argument("result", type=Path)
    parser.add_argument("--endpoint", default="https://huggingface.co")
    parser.add_argument("--resources", choices=("bucket", "dataset"), nargs="+",
                        default=["bucket", "dataset"])
    parser.add_argument("--interface")
    parser.add_argument("--store", type=Path)
    args = parser.parse_args()
    endpoint = urlparse(args.endpoint)
    if args.endpoint != "https://huggingface.co" and (
        endpoint.scheme != "http" or endpoint.hostname != "127.0.0.1"
    ):
        parser.error("use the official Hub or an OpenXet server on loopback")
    args.scratch.mkdir(mode=0o700)
    token = os.environ.get("HF_TOKEN") or getpass.getpass("Hugging Face token: ")
    with args.result.open("x") as out:
        try:
            run(args, token, out)
        except Exception as error:
            print(json.dumps({"phase": "driver-error", "error_type": type(error).__name__,
                              "http_status": getattr(getattr(error, "response", None),
                                                     "status_code", None)}), file=out)
            raise SystemExit(1) from None


if __name__ == "__main__":
    main()
