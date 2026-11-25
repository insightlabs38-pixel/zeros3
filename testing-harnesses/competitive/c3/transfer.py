"""Measure C2/C3 transfer through counted loopback TCP streams."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import resource
import shutil
import socket
import socketserver
import subprocess
import tarfile
import tempfile
import threading
import time

import boto3
from botocore.config import Config

BUCKET = "frontier"
KEY = "c3-local-test-key"
SECRET = "c3-local-disposable-test-secret"
FAMILIES = {
    "repeat": ("repeat/0/payload.bin", "repeat/1/payload.bin"),
    "shift": ("shift/0/payload.bin", "shift/1/payload.bin"),
    "entropy": ("entropy/0/payload.bin", "entropy/1/payload.bin"),
    "tensor": ("tensor/0/weights.safetensors", "tensor/1/weights.safetensors"),
    "oci": ("oci/0/layer.tar.gz", "oci/1/layer.tar.gz"),
    "source": ("source/0", "source/1"),
    "coreutils": ("coreutils/0", "coreutils/1"),
    "gzip-control": ("gzip-control/0/asset.gz", "gzip-control/1/asset.gz"),
    "parquet": ("parquet/0/snapshot.parquet", "parquet/1/snapshot.parquet"),
    "sst": ("sst/0/snapshot.sst", "sst/1/snapshot.sst"),
    "typescript": ("typescript-5.6.2.tgz", "typescript-5.6.3.tgz"),
    "django": ("Django-5.1.1.tar.gz", "Django-5.1.2.tar.gz"),
    "rubygems": ("rubocop-1.66.0.gem", "rubocop-1.66.1.gem"),
}
LANES = ("s3", "sync", "replicate", "closure")
STATES = ("empty", "same-key", "different-key", "exact", "partial")


class Relay(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, port):
        self.port = port
        self.lock = threading.Lock()
        self.sent = [0, 0]
        self.connections = 0
        self.requests = {}
        super().__init__(("127.0.0.1", 0), Forward)

    def count(self, index, size):
        with self.lock:
            self.sent[index] += size

    def snapshot(self):
        with self.lock:
            return {"client_to_server": self.sent[0], "server_to_client": self.sent[1],
                    "connections": self.connections, "requests": self.requests.copy()}


class Forward(socketserver.BaseRequestHandler):
    def handle(self):
        with self.server.lock:
            self.server.connections += 1
        with socket.create_connection(("127.0.0.1", self.server.port), timeout=30) as upstream:
            self.request.settimeout(30)
            self.request.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            upstream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

            def pump(src, dst, index):
                parser = RequestCounter(self.server) if index == 0 else None
                try:
                    while data := src.recv(65536):
                        if parser:
                            parser.feed(data)
                        self.server.count(index, len(data))
                        dst.sendall(data)
                except OSError:
                    pass
                finally:
                    try:
                        dst.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass

            request = threading.Thread(target=pump, args=(self.request, upstream, 0), daemon=True)
            request.start()
            pump(upstream, self.request, 1)
            request.join()


class RequestCounter:
    """Track Content-Length framed requests without retaining headers after parsing."""

    def __init__(self, server):
        self.server = server
        self.header = bytearray()
        self.body_remaining = 0
        self.unparsed = False

    def feed(self, data):
        while data and not self.unparsed:
            if self.body_remaining:
                n = min(self.body_remaining, len(data))
                self.body_remaining -= n
                data = data[n:]
                continue
            self.header.extend(data)
            end = self.header.find(b"\r\n\r\n")
            if end < 0:
                if len(self.header) > 64 * 1024:
                    self.unparsed = True
                return
            request, *fields = self.header[:end].split(b"\r\n")
            method, path, _ = request.decode("ascii").split(" ")
            route = path.split("?", 1)[0]
            if not route.startswith("/_zeros3/"):
                route = "/s3-object"
            key = f"{method} {route}"
            with self.server.lock:
                self.server.requests[key] = self.server.requests.get(key, 0) + 1
            data = self.header[end + 4:]
            self.header.clear()
            for field in fields:
                name, value = field.split(b":", 1)
                if name.lower() == b"content-length":
                    self.body_remaining = int(value)
                if name.lower() == b"transfer-encoding":
                    self.unparsed = True


@contextmanager
def relay(port):
    with Relay(port) as server:
        thread = threading.Thread(target=server.serve_forever, args=(.01,), daemon=True)
        thread.start()
        try:
            yield server, f"http://127.0.0.1:{server.server_address[1]}"
        finally:
            server.shutdown()
            thread.join()


def api(url):
    return boto3.client("s3", endpoint_url=url, region_name="us-east-1",
                        aws_access_key_id=KEY, aws_secret_access_key=SECRET,
                        config=Config(s3={"addressing_style": "path"},
                                      request_checksum_calculation="when_required",
                                      response_checksum_validation="when_required",
                                      retries={"total_max_attempts": 1}, read_timeout=30))


@contextmanager
def serve(binary, store, log, env):
    with socket.socket() as addr:
        addr.bind(("127.0.0.1", 0))
        port = addr.getsockname()[1]
    url = f"http://127.0.0.1:{port}"
    with log.open("ab") as out:
        child = subprocess.Popen([str(binary), "serve", "-store", str(store),
                                  "-addr", f"127.0.0.1:{port}"], env=env,
                                 stdout=out, stderr=out)
        try:
            for _ in range(150):
                if child.poll() is not None:
                    raise RuntimeError(log.read_text()[-2000:])
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=.1):
                        break
                except OSError:
                    time.sleep(.02)
            else:
                raise TimeoutError(log)
            yield port, api(url), child.pid
        finally:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()


def put(s3, key, path, metadata=None):
    with path.open("rb") as body:
        s3.put_object(Bucket=BUCKET, Key=key, Body=body, ContentType="application/octet-stream",
                      Metadata=metadata or {})


def verified(s3, key, path, metadata=None):
    expected = file_digest(path)
    obj = s3.get_object(Bucket=BUCKET, Key=key)
    with obj["Body"] as stream:
        actual = hashlib.file_digest(stream, "sha256").hexdigest()
    headers = {name.lower(): value for name, value in obj["Metadata"].items()}
    if expected != actual or (metadata is not None and headers != metadata):
        raise ValueError(f"{key}: sha {actual} expected {expected}, metadata {headers!r} expected {metadata!r}")
    return actual


def file_digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def stats(pid):
    base = Path("/proc", str(pid))
    stat = (base / "stat").read_text().rsplit(")", 1)[1].split()
    io = dict(line.split(": ", 1) for line in (base / "io").read_text().splitlines())
    status = dict(line.split(":\t", 1) for line in (base / "status").read_text().splitlines()
                  if ":\t" in line)
    return {"cpu_seconds": (int(stat[11]) + int(stat[12])) / os.sysconf("SC_CLK_TCK"),
            "logical_read_bytes": int(io["rchar"]), "physical_read_bytes": int(io["read_bytes"]),
            "physical_write_bytes": int(io["write_bytes"]),
            "process_peak_rss_bytes": int(status["VmHWM"].split()[0]) * 1024}


def delta(before, after):
    return {key: value if key == "process_peak_rss_bytes" else value - before[key]
            for key, value in after.items()}


def allocated(path):
    return sum(p.lstat().st_blocks * 512 for p in (path, *path.rglob("*")))


def run(binary, env, flags):
    with tempfile.TemporaryFile() as out:
        child = subprocess.Popen([str(binary), *flags], env=env, stdout=out, stderr=out)
        deadline = time.monotonic() + 90
        while True:
            pid, status, usage = os.wait4(child.pid, os.WNOHANG)
            if pid:
                child.returncode = os.waitstatus_to_exitcode(status)
                break
            if time.monotonic() >= deadline:
                child.kill()
                child.wait()
                raise TimeoutError(flags[0])
            time.sleep(.003)
        out.seek(0)
        result = out.read().decode()
        if child.returncode:
            raise RuntimeError(f"{flags[0]}: {result[-4000:]}")
    return {"cpu_seconds": usage.ru_utime + usage.ru_stime, "peak_rss_bytes": usage.ru_maxrss * 1024,
            "input_blocks": usage.ru_inblock, "output_blocks": usage.ru_oublock, "stdout": result}


def trial(args, env, directory, source, case, state, lane, seed, target):
    store = directory / "destination"
    with serve(args.binary, store, directory / "destination.log", env) as (port, sink, pid):
        sink.create_bucket(Bucket=BUCKET)
        temp = directory / "client-tmp"
        temp.mkdir()
        metadata = {"scene": "c3"}
        if state != "empty":
            payload = target if state == "exact" else seed
            if state == "partial":
                payload = directory / "partial"
                with seed.open("rb") as src, payload.open("wb") as dst:
                    dst.write(src.read(seed.stat().st_size // 2))
            put(sink, "target" if state == "same-key" else "prior", payload)
        if state == "same-key":
            # Match the source's metadata and keep prior bytes for fallback detection.
            verified(sink, "target", seed)

        source_port, _, source_pid = source
        with relay(source_port) as (src_tap, src_url), relay(port) as (dst_tap, dst_url):
            before_dst, before_src = stats(pid), stats(source_pid)
            start = time.perf_counter()
            if lane == "s3":
                usage = resource.getrusage(resource.RUSAGE_SELF)
                put(api(dst_url), "target", target, metadata)
                end = resource.getrusage(resource.RUSAGE_SELF)
                cli = {"cpu_seconds": end.ru_utime + end.ru_stime - usage.ru_utime - usage.ru_stime,
                       "peak_rss_bytes": end.ru_maxrss * 1024}
            else:
                if lane == "sync":
                    command = ["sync", "-workers", "2", "-endpoint", dst_url, str(target),
                               f"s3://{BUCKET}/target"]
                else:
                    command = ["replicate", "-workers", "2", "-from", src_url, "-to", dst_url,
                               "-from-access-key", KEY, "-from-secret-key", SECRET,
                               "-to-access-key", KEY, "-to-secret-key", SECRET]
                    if lane in ("closure", "closure-cross"):
                        command += ["-authority-closure"]
                    if lane == "closure-cross" and state in ("different-key", "partial", "exact"):
                        command += ["-authority-base", f"s3://{BUCKET}/prior"]
                    command += [f"s3://{BUCKET}/target", f"s3://{BUCKET}/target"]
                cli = run(args.binary, {**env, "TMPDIR": str(temp)}, command)
            elapsed = time.perf_counter() - start
            receiver, sender = delta(before_dst, stats(pid)), delta(before_src, stats(source_pid))
            src_wire, dst_wire = src_tap.snapshot(), dst_tap.snapshot()
        return {"family": case, "state": state, "lane": lane, "seconds": elapsed,
                "sender": sender, "receiver": receiver, "client": cli,
                "seed_sha256": file_digest(seed), "logical_bytes": target.stat().st_size,
                "object_sha256": verified(sink, "target", target, {} if lane == "sync" else metadata),
                "source_http_bytes": src_wire, "destination_http_bytes": dst_wire,
                "allocated_before_finalization": allocated(store)}


def materialize(source, target):
    if not source.is_dir():
        return source
    with tarfile.open(target, "w", format=tarfile.USTAR_FORMAT) as archive:
        for path in sorted(source.rglob("*")):
            if path.is_file():
                info = tarfile.TarInfo(path.relative_to(source).as_posix())
                info.size = path.stat().st_size
                info.mode = 0o644
                with path.open("rb") as data:
                    archive.addfile(info, data)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("binary", "corpus", "downloads", "scratch", "results"):
        parser.add_argument("--" + arg, type=Path, required=True)
    parser.add_argument("--families", nargs="+", choices=FAMILIES, default=list(FAMILIES))
    parser.add_argument("--states", nargs="+", choices=STATES, default=STATES)
    parser.add_argument("--lanes", nargs="+", choices=(*LANES, "closure-cross"), default=LANES)
    parser.add_argument("--repeats", type=int, default=1)
    parser.add_argument("--seed-only", action="store_true", help="skip source authority optimization")
    args = parser.parse_args()
    for arg in ("binary", "corpus", "downloads", "scratch", "results"):
        setattr(args, arg, getattr(args, arg).resolve())
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    args.scratch.mkdir(parents=True, exist_ok=True)
    args.results.parent.mkdir(parents=True, exist_ok=True)
    with args.results.open("x") as result:
        for family in args.families:
            root = args.downloads if family in ("typescript", "django", "rubygems") else args.corpus
            seed, target = (root / rel for rel in FAMILIES[family])
            with tempfile.TemporaryDirectory(prefix=f"{family}-", dir=args.scratch) as scratch:
                work = Path(scratch)
                seed = materialize(seed, work / "seed.tar")
                target = materialize(target, work / "target.tar")
                store, log = work / "source", work / "source.log"
                with serve(args.binary, store, log, env) as (_, source, _):
                    source.create_bucket(Bucket=BUCKET)
                    put(source, "prior", seed)
                    put(source, "target", target, {"scene": "c3"})
                source_optimize = None
                if not args.seed_only:
                    source_optimize = run(args.binary, {**env, "TMPDIR": str(work)}, [
                        "authority", "optimize", "-store", str(store), "-apply",
                        "-profile", "archive", "-order", "oldest", "-max-depth", "2", "-max-bases", "2",
                        "-min-saving", ".10", "-record-groups", "-canonical", "-codec-replay",
                        "-hard-codecs", "-splice",
                    ])
                    analysis = json.loads(source_optimize.pop("stdout"))
                    source_optimize["changes"] = [c.get("to") for c in analysis.get("changes", [])]
                with serve(args.binary, store, log, env) as source:
                    verified(source[1], "target", target, {"scene": "c3"})
                    verified(source[1], "prior", seed)
                    for repetition in range(args.repeats):
                        order = args.lanes if repetition % 2 == 0 else args.lanes[::-1]
                        for state in args.states:
                            for lane in order:
                                directory = work / "trial"
                                directory.mkdir()
                                try:
                                    row = trial(args, env, directory, source, family, state, lane, seed, target)
                                    print(json.dumps({**row, "repetition": repetition,
                                                      "source_optimize": source_optimize}),
                                          file=result, flush=True)
                                finally:
                                    shutil.rmtree(directory)


if __name__ == "__main__":
    main()
