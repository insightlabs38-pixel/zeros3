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
import zlib

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
STATES = ("empty", "same-key", "different-key", "exact", "partial",
          "same-authority", "different-authority")
OPTIMIZE = ("authority", "optimize", "-apply", "-profile", "archive", "-order", "oldest",
            "-max-depth", "2", "-max-bases", "2", "-min-saving", ".10",
            "-record-groups", "-canonical", "-codec-replay", "-hard-codecs", "-splice")


class Relay(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, port, model_gzip, rate_bytes):
        self.port = port
        self.model_gzip = model_gzip
        self.rate_bytes = rate_bytes
        self.ready_at = [0., 0.]
        self.gzip = [0, 0, 0.]
        self.lock = threading.Lock()
        self.sent = [0, 0]
        self.connections = 0
        self.requests = {}
        super().__init__(("127.0.0.1", 0), Forward)

    def count(self, index, size):
        with self.lock:
            self.sent[index] += size

    def pace(self, index, size):
        if self.rate_bytes:
            with self.lock:
                now = time.monotonic()
                ready = max(now, self.ready_at[index]) + size / self.rate_bytes
                self.ready_at[index] = ready
            time.sleep(max(0., ready - time.monotonic()))

    def snapshot(self):
        with self.lock:
            result = {"client_to_server": self.sent[0], "server_to_client": self.sent[1],
                      "connections": self.connections, "requests": self.requests.copy()}
            if self.model_gzip:
                result["gzip_model"] = dict(zip(("input_bytes", "gzip_bytes", "compression_cpu_seconds"), self.gzip))
            return result


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
                        self.server.pace(index, len(data))
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
        self.compressor = None
        self.gzip_size = 0
        self.gzip_cpu = 0.
        self.frame_size = 0

    def feed(self, data):
        while data and not self.unparsed:
            if self.body_remaining:
                n = min(self.body_remaining, len(data))
                self.body_remaining -= n
                if self.compressor:
                    start = time.thread_time()
                    self.gzip_size += len(self.compressor.compress(data[:n]))
                    if self.body_remaining == 0:
                        self.gzip_size += len(self.compressor.flush())
                    self.gzip_cpu += time.thread_time() - start
                    if self.body_remaining == 0:
                        with self.server.lock:
                            for i, value in enumerate((self.frame_size, self.gzip_size, self.gzip_cpu)):
                                self.server.gzip[i] += value
                        self.compressor = None
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
            if self.server.model_gzip and self.body_remaining and key == "POST /_zeros3/v2/chunks/upload":
                self.compressor = zlib.compressobj(level=1, wbits=31)
                self.frame_size = self.body_remaining
                self.gzip_size = 0
                self.gzip_cpu = 0.


@contextmanager
def relay(port, rate_bytes, model_gzip=False):
    with Relay(port, model_gzip, rate_bytes) as server:
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


def run(binary, env, flags, timeout=90):
    start = time.monotonic()
    with tempfile.TemporaryFile() as out:
        child = subprocess.Popen([str(binary), *flags], env=env, stdout=out, stderr=out)
        deadline = start + timeout
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
    return {"seconds": time.monotonic() - start,
            "cpu_seconds": usage.ru_utime + usage.ru_stime, "peak_rss_bytes": usage.ru_maxrss * 1024,
            "input_blocks": usage.ru_inblock, "output_blocks": usage.ru_oublock, "stdout": result}


def stage(args, env, directory, source, state, seed, target):
    store = directory / "destination"
    log = directory / "destination.log"
    warm = state.endswith("-authority")
    prior_key = "target" if state.startswith("same-") else "prior"
    with serve(args.receiver_binary, store, log, env) as (_, sink, _):
        sink.create_bucket(Bucket=BUCKET)
        if state != "empty":
            payload = target if state == "exact" else seed
            if state == "partial":
                payload = directory / "partial"
                with seed.open("rb") as src, payload.open("wb") as dst:
                    dst.write(src.read(seed.stat().st_size // 2))
            put(sink, prior_key, payload)
            verified(sink, prior_key, payload)
    if warm:
        run(args.receiver_binary, env, ["authority", "recover", "-store", str(store),
                               "-bucket", BUCKET, "-key", prior_key, "-peer-key", "prior",
                               "-from", f"http://127.0.0.1:{source[0]}",
                               "-access-key", KEY, "-secret-key", SECRET])

def trial(args, env, directory, source, case, state, lane, seed, target):
    store = directory / "destination"
    log = directory / "destination.log"
    metadata = {"scene": "c3"}
    with serve(args.receiver_binary, store, log, env) as (port, sink, pid):
        temp = directory / "client-tmp"
        temp.mkdir()
        source_port, _, source_pid = source
        with (relay(source_port, args.rate_bytes) as (src_tap, src_url),
              relay(port, args.rate_bytes, args.model_gzip) as (dst_tap, dst_url)):
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
                    if lane == "closure-cross" and state in ("different-key", "different-authority", "partial", "exact"):
                        command += ["-authority-base", f"s3://{BUCKET}/prior"]
                    command += [f"s3://{BUCKET}/target", f"s3://{BUCKET}/target"]
                cli = run(args.binary, {**env, "TMPDIR": str(temp)}, command)
            elapsed = time.perf_counter() - start
            receiver, sender = delta(before_dst, stats(pid)), delta(before_src, stats(source_pid))
            src_wire, dst_wire = src_tap.snapshot(), dst_tap.snapshot()
        row = {"family": case, "state": state, "lane": lane, "seconds": elapsed, "rate_bytes": args.rate_bytes,
                "sender": sender, "receiver": receiver, "client": cli,
                "seed_sha256": file_digest(seed), "logical_bytes": target.stat().st_size,
                "object_sha256": verified(sink, "target", target, {} if lane == "sync" else metadata),
                "source_http_bytes": src_wire, "destination_http_bytes": dst_wire,
                "allocated_before_finalization": allocated(store)}
    if args.finalize:
        maintenance = {}
        for stage, flags in (
            ("gc", ["gc", "-apply", "-json"]),
            ("repack", ["repack", "-apply", "-json", "-max-live-percent", "100", "-recompress"]),
            ("compact", ["compact", "-json", "-pack-size-mib", "1"]),
            ("final-gc", ["gc", "-apply", "-json"]),
            ("deep-verify", ["verify", "-deep", "-json"]),
        ):
            step = run(args.receiver_binary, env, [*flags, "-store", str(store)])
            maintenance[stage] = {**step, "stdout": json.loads(step["stdout"]), "allocated": allocated(store)}
        row["finalization"] = {"allocated": allocated(store), "maintenance": maintenance}
        with serve(args.receiver_binary, store, log, env) as (_, sink, _):
            verified(sink, "target", target, {} if lane == "sync" else metadata)
            if state in ("different-key", "different-authority"):
                verified(sink, "prior", seed)
    return row



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
    parser.add_argument("--source-binary", type=Path)
    parser.add_argument("--receiver-binary", type=Path)
    for arg in ("binary", "corpus", "downloads", "scratch", "results"):
        parser.add_argument("--" + arg, type=Path, required=True)
    parser.add_argument("--families", nargs="+", choices=FAMILIES, default=list(FAMILIES))
    parser.add_argument("--states", nargs="+", choices=STATES, default=STATES[:5])
    parser.add_argument("--lanes", nargs="+", choices=(*LANES, "closure-cross"), default=LANES)
    parser.add_argument("--repeats", type=int, default=1)
    parser.add_argument("--freeze-inputs", action="store_true", help="emit fixture digests without running transfer")
    parser.add_argument("--rate-bytes", type=int, default=0, help="per-direction rate on each relay leg")
    parser.add_argument("--finalize", action="store_true", help="finalize and deep-verify each destination")
    parser.add_argument("--model-gzip", action="store_true", help="separate gzip sizing pass on exact bulk frames")
    parser.add_argument("--seed-only", action="store_true", help="skip source authority optimization")
    args = parser.parse_args()
    if args.rate_bytes < 0 or args.repeats < 1:
        parser.error("rate must be nonnegative and repeats must be positive")
    for arg in ("binary", "corpus", "downloads", "scratch", "results"):
        setattr(args, arg, getattr(args, arg).resolve())
    args.source_binary = (args.source_binary or args.binary).resolve()
    args.receiver_binary = (args.receiver_binary or args.binary).resolve()
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    args.scratch.mkdir(parents=True, exist_ok=True)
    args.results.parent.mkdir(parents=True, exist_ok=True)
    known = {}
    if not args.freeze_inputs:
        with (Path(__file__).resolve().parent / "inputs.jsonl").open() as manifest:
            known = {row["family"]: row for row in map(json.loads, manifest)}
    with args.results.open("x") as result:
        for family in args.families:
            root = args.downloads if family in ("typescript", "django", "rubygems") else args.corpus
            seed, target = (root / rel for rel in FAMILIES[family])
            with tempfile.TemporaryDirectory(prefix=f"{family}-", dir=args.scratch) as scratch:
                work = Path(scratch)
                seed = materialize(seed, work / "seed.tar")
                target = materialize(target, work / "target.tar")
                identity = {"family": family, "prior_sha256": file_digest(seed),
                            "target_sha256": file_digest(target),
                            "prior_bytes": seed.stat().st_size, "target_bytes": target.stat().st_size}
                if args.freeze_inputs:
                    print(json.dumps(identity), file=result, flush=True)
                    continue
                if known.get(family) != identity:
                    raise ValueError(f"fixture does not match frozen manifest: {identity}")
                store, log = work / "source", work / "source.log"
                with serve(args.source_binary, store, log, env) as (_, source, _):
                    source.create_bucket(Bucket=BUCKET)
                    put(source, "prior", seed)
                    put(source, "target", target, {"scene": "c3"})
                source_optimize = None
                if not args.seed_only:
                    source_optimize = run(args.source_binary, {**env, "TMPDIR": str(work)},
                                          [*OPTIMIZE, "-store", str(store)], timeout=150)
                    analysis = json.loads(source_optimize.pop("stdout"))
                    source_optimize["changes"] = [c.get("to") for c in analysis.get("changes", [])]
                with serve(args.source_binary, store, log, env) as source:
                    verified(source[1], "target", target, {"scene": "c3"})
                    verified(source[1], "prior", seed)
                    for state in args.states:
                        staging = work / state
                        staging.mkdir()
                        stage(args, env, staging, source, state, seed, target)
                    for repetition in range(args.repeats):
                        order = args.lanes if repetition % 2 == 0 else args.lanes[::-1]
                        for state in args.states:
                            for lane in order:
                                directory = work / "trial"
                                directory.mkdir()
                                try:
                                    shutil.copytree(work / state / "destination", directory / "destination")
                                    row = trial(args, env, directory, source, family, state, lane, seed, target)
                                    print(json.dumps({**row, "repetition": repetition,
                                                      "source_optimize": source_optimize}),
                                          file=result, flush=True)
                                finally:
                                    shutil.rmtree(directory)


if __name__ == "__main__":
    main()
