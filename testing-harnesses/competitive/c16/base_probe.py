"""Measure the read-only named-base proof separately from ingest and import."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import sys
import time
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/"c3"))
from transfer import BUCKET, KEY, SECRET, allocated, delta, put, serve, stats


def request(url, method, body=None):
    digest = hashlib.sha256(body or b"").hexdigest()
    proposed = AWSRequest(method=method, url=url, data=body, headers={"X-Amz-Content-Sha256": digest})
    SigV4Auth(Credentials(KEY, SECRET), "s3", "us-east-1").add_auth(proposed)
    ready = proposed.prepare()
    req = Request(ready.url, data=body, headers=dict(ready.headers), method=method)
    with urlopen(req, timeout=20) as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("binary", "gzip", "work", "results"):
        parser.add_argument("--"+arg, type=lambda name: Path(name).resolve(), required=True)
    args = parser.parse_args()
    args.work.mkdir(parents=True)
    store = args.work/"store"
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET, "GOMAXPROCS": "2"}
    expected = gzip.decompress(args.gzip.read_bytes())
    identity = hashlib.sha256(expected).hexdigest()
    with args.results.open("x") as out, serve(args.binary, store, args.work/"service.log", env) as (port, api, pid):
        api.create_bucket(Bucket=BUCKET)
        put(api, "ordinary", args.gzip)
        origin = f"http://127.0.0.1:{port}"
        named = {"bucket": BUCKET, "key": "ordinary"}
        descriptor = request(origin+"/_zeros3/v1/object?"+urlencode(named), "GET")
        body = json.dumps({**named, "version_id": descriptor["version_id"],
                           "object_sha256": descriptor["object_sha256"]}).encode()
        initial = allocated(store)
        for i in range(5):
            before = stats(pid)
            start = time.perf_counter()
            result = request(origin+"/_zeros3/v1/authority/base", "POST", body)
            seconds = time.perf_counter()-start
            if result["base_sha256"] != identity or result["base_size"] != len(expected):
                raise ValueError("base proof differs from independent gzip")
            if allocated(store) != initial:
                raise ValueError("base discovery changed persistent space")
            print(json.dumps({"repeat": i, "seconds": seconds, "server": delta(before, stats(pid)),
                              "allocation": initial, "base_sha256": identity, "base_bytes": len(expected)}),
                  file=out, flush=True)


if __name__ == "__main__":
    main()
