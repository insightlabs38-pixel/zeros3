"""Measure loopback TCP and investigate persistent S3 range-read latency."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from http.client import HTTPConnection
import json
from pathlib import Path
import shutil
import socket
from statistics import median
import time
from urllib.parse import urlsplit

from corpus import MIB, blob
from local_s3 import BUCKET, put, server


def loopback():
    packet = b"\0" * MIB
    with socket.socket() as listener, ThreadPoolExecutor(max_workers=1) as worker:
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)

        def receive():
            conn, _ = listener.accept()
            with conn:
                total = 0
                buf = bytearray(MIB)
                while size := conn.recv_into(buf):
                    total += size
                return total

        received = worker.submit(receive)
        with socket.create_connection(listener.getsockname()) as client:
            start = time.perf_counter()
            for _ in range(128):
                client.sendall(packet)
            client.shutdown(socket.SHUT_WR)
            assert received.result() == 128 * MIB
    return 128 / (time.perf_counter() - start)


def range_samples(api, key, expected, mode):
    url = urlsplit(api.generate_presigned_url("get_object", Params={
        "Bucket": BUCKET, "Key": key, "Range": "bytes=32-287",
    }, ExpiresIn=60))
    path = url.path + "?" + url.query
    conn = HTTPConnection(url.hostname, url.port, timeout=10)
    timings = []
    try:
        for i in range(35):
            start = time.perf_counter()
            if conn.sock is None:
                conn.connect()
            if mode == "quickack":
                conn.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_QUICKACK, 1)
            headers = {"Range": "bytes=32-287"}
            if mode == "fresh-connection":
                headers["Connection"] = "close"
            conn.request("GET", path, headers=headers)
            response = conn.getresponse()
            assert response.status == 206 and response.read() == expected
            if i >= 5:
                timings.append((time.perf_counter() - start) * 1000)
    finally:
        conn.close()
    return {"p50_ms": median(timings), "minimum_ms": min(timings), "maximum_ms": max(timings),
            "operations": len(timings)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("result", type=Path)
    args = parser.parse_args()
    with args.result.open("x", buffering=1) as out:
        print(json.dumps({"phase": "loopback-tcp",
                          "throughput_mib_s": [loopback() for _ in range(3)]}), file=out)
        data = blob(529, MIB)
        for name in ("garage", "zeros3"):
            run = args.work / "runs" / f"socket-probe-{name}"
            try:
                with server(name, args.work, run) as (api, _, _):
                    put(api, "one", data)
                    for mode in ("keepalive", "fresh-connection", "quickack"):
                        print(json.dumps({"phase": "range", "system": name, "mode": mode,
                                          **range_samples(api, "one", data[32:288], mode)}), file=out)
            except Exception as exc:
                print(json.dumps({"phase": "error", "system": name, "error": str(exc)}), file=out)
            finally:
                if run.exists():
                    shutil.rmtree(run)


if __name__ == "__main__":
    main()
