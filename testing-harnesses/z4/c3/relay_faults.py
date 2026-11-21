#!/usr/bin/env python3
"""Shape and fracture loopback replication on exact, retained release histories."""
import argparse
import contextlib
import http.client
import http.server
import importlib.util
import json
from pathlib import Path
import resource
import shutil
import socket
import subprocess
import threading
import time
from urllib.parse import urlsplit

IMPORT = "/_zeros3/v1/authority/import"
CHUNK = 64 << 10


def module(path):
    spec = importlib.util.spec_from_file_location("pilot", path)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


@contextlib.contextmanager
def proxy(upstream, rate, fault, ledger):
    target = urlsplit(upstream)
    lock = threading.Lock()

    class Proxy(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def relay(self):
            length = int(self.headers.get("Content-Length", "0"))
            is_import = self.path == IMPORT
            cut = fault == "cut-body" and is_import
            after = fault == "drop-ack" and is_import
            row = {"method": self.command, "path": self.path, "announced_bytes": length,
                   "forwarded_body_bytes": 0, "upstream_status": None, "fault": fault if cut or after else None}
            with contextlib.closing(http.client.HTTPConnection(target.hostname, target.port, timeout=60)) as dst:
                try:
                    dst.putrequest(self.command, self.path, skip_host=True, skip_accept_encoding=True)
                    for name, value in self.headers.items():
                        if name.lower() not in ("connection", "proxy-connection", "expect"):
                            dst.putheader(name, value)
                    dst.putheader("Connection", "close")
                    dst.endheaders()
                    remaining = length * 2 // 3 if cut else length
                    start = time.monotonic()
                    while remaining:
                        data = self.rfile.read(min(CHUNK, remaining))
                        if not data:
                            raise EOFError("client closed request before its declared length")
                        dst.send(data)
                        row["forwarded_body_bytes"] += len(data)
                        remaining -= len(data)
                        wait = start + row["forwarded_body_bytes"] / rate - time.monotonic()
                        if wait > 0:
                            time.sleep(wait)
                    if cut:
                        dst.sock.shutdown(socket.SHUT_WR)
                    with dst.getresponse() as reply:
                        row["upstream_status"] = reply.status
                        if cut or after:
                            row["withheld_response_bytes"] = len(reply.read())
                            return
                        self.send_response_only(reply.status)
                        for name, value in reply.getheaders():
                            if name.lower() not in ("connection", "transfer-encoding"):
                                self.send_header(name, value)
                        self.send_header("Connection", "close")
                        self.end_headers()
                        while data := reply.read(CHUNK):
                            self.wfile.write(data)
                except (ConnectionError, EOFError, OSError) as exc:
                    row["connection_error"] = str(exc)
                    if not (cut or after):
                        raise
                finally:
                    with lock:
                        ledger.append(row)

        do_GET = do_HEAD = do_PUT = do_POST = do_DELETE = relay

    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), Proxy) as server:
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            worker.join()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True, type=Path)
    p.add_argument("--pilot-helper", required=True, type=Path)
    p.add_argument("--source-store", required=True, type=Path)
    p.add_argument("--seed", required=True, type=Path)
    p.add_argument("--before", required=True, type=Path)
    p.add_argument("--after", required=True, type=Path)
    p.add_argument("--output", required=True, type=Path)
    p.add_argument("--bytes-per-second", type=int, default=2 << 20)
    a = p.parse_args()
    if a.bytes_per_second <= 0:
        p.error("a positive upload budget is required")
    pilot = module(a.pilot_helper)
    output = a.output.resolve()
    output.mkdir()
    source_store = output / "source"
    shutil.copytree(a.source_store.resolve(), source_store)
    binary = a.binary.resolve(strict=True)
    key, source_key = "coreutils-gzip/revision-1", "coreutils-gzip/revision-2"
    target_uri = f"s3://{pilot.BUCKET}/{key}"
    source_uri = f"s3://{pilot.BUCKET}/{source_key}"
    before, after = pilot.digest(a.before), pilot.digest(a.after)
    rows = []

    with pilot.serve(binary, source_store, output / "source.log") as source:
        def replicate(attempt, label, upstream, native, fault=None):
            ledger = []
            with proxy(upstream, a.bytes_per_second, fault, ledger) as dest:
                cmd = [binary, "replicate", "-from", source, "-to", dest,
                       "-from-access-key", pilot.ACCESS, "-from-secret-key", pilot.SECRET,
                       "-to-access-key", pilot.ACCESS, "-to-secret-key", pilot.SECRET]
                if native:
                    cmd.append("-authority-closure")
                start = time.monotonic()
                r0 = resource.getrusage(resource.RUSAGE_CHILDREN)
                with (attempt / f"{label}.log").open("xb") as log:
                    proc = subprocess.run([*cmd, source_uri, target_uri], stdout=log, stderr=subprocess.STDOUT, timeout=180)
                r1 = resource.getrusage(resource.RUSAGE_CHILDREN)
                elapsed = time.monotonic() - start
            result = {"label": label, "native_requested": native, "fault": fault, "exit": proc.returncode,
                      "wall_seconds": elapsed, "client_cpu_seconds": r1.ru_utime-r0.ru_utime+r1.ru_stime-r0.ru_stime,
                      "forwarded_payload_bytes": sum(x["forwarded_body_bytes"] for x in ledger), "request_metrics": ledger}
            pilot.save(attempt / f"{label}.json", result)
            if (proc.returncode == 0) != (fault is None):
                raise RuntimeError(f"{label} returned {proc.returncode}")
            imports = [r for r in ledger if r["path"] == IMPORT]
            if fault == "cut-body":
                if len(imports) != 1 or not 0 < imports[0]["forwarded_body_bytes"] < imports[0]["announced_bytes"]:
                    raise RuntimeError("interrupted native import was not exercised")
            if fault == "drop-ack" and (len(imports) != 1 or imports[0]["upstream_status"] != 200):
                raise RuntimeError("native publication did not complete before dropping the acknowledgement")
            return result

        for trial, steps in (
            ("cdc", (("reliable", False, None),)),
            ("native", (("reliable", True, None),)),
            ("recovery", (("interrupted", True, "cut-body"), ("ack-lost", True, "drop-ack"), ("retry", True, None))),
        ):
            attempt = output / trial
            attempt.mkdir()
            store = attempt / "store"
            shutil.copytree(a.seed.resolve(), store)
            checks = []
            for label, native, fault in steps:
                with pilot.serve(binary, store, attempt / f"{label}-serve.log") as upstream:
                    result = replicate(attempt, label, upstream, native, fault)
                with pilot.serve(binary, store, attempt / f"{label}-reopen.log") as upstream:
                    body = attempt / f"{label}-readback.bin"
                    pilot.request(upstream, "GET", key, output=body)
                    actual = pilot.digest(body)
                    if actual != (before if fault == "cut-body" else after):
                        raise RuntimeError(f"{label} exposed the wrong release after reopening")
                    result["reopened_sha256"] = actual
                    body.unlink()
                checks.append(result)
            listed = subprocess.check_output([binary, "versions", "-store", store, "-bucket", pilot.BUCKET, "-key", key, "-json"])
            versions = json.loads(listed)
            if len(versions) != 2:
                raise RuntimeError(f"{trial} expected one retained and one visible version, found {len(versions)}")
            pilot.timed(binary, store, attempt, "verify", "-deep", "-json")
            verified = json.loads((attempt / "verify.out").read_text())
            if any(verified[k] for k in ("missing", "corrupt", "invalid")):
                raise RuntimeError(f"{trial} reported integrity failures")
            rows.append({"trial": trial, "attempts": checks, "versions": len(versions), "verify": verified})
    pilot.save(output / "results.json", {"rate_bytes_per_second": a.bytes_per_second,
                                       "binary_sha256": pilot.digest(binary), "trials": rows})


if __name__ == "__main__":
    main()
