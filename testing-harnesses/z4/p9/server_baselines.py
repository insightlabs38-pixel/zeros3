#!/usr/bin/env python3
"""Run the existing two-SDK profile against pinned local reference servers."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import time

ACCESS = "AKIAZEROS3EXAMPLE01"
SECRET = "zeros3exampleSecretKeyForM1TestingOnly01"


def address():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()


@contextlib.contextmanager
def launch(kind, binary, path):
    host, port = address()
    addr = f"{host}:{port}"
    store = path / "store"
    store.mkdir()
    env = {**os.environ, "GOMAXPROCS": "2"}
    if kind == "minio":
        env |= {"MINIO_ROOT_USER": ACCESS, "MINIO_ROOT_PASSWORD": SECRET, "MINIO_BROWSER": "off", "MINIO_UPDATE": "off"}
        host2, port2 = address()
        command = [binary, "server", "--address", addr, "--console-address", f"{host2}:{port2}", store]
    else:
        versions = path / "versions"
        versions.mkdir()
        command = [binary, "--port", addr, "--access", ACCESS, "--secret", SECRET,
                   "posix", "--fsync", "--versioning-dir", versions, store]
    with (path / "server.log").open("xb") as log:
        proc = subprocess.Popen(command, env=env, stdout=log, stderr=log, cwd=path)
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline and proc.poll() is None:
                try:
                    with socket.create_connection((host, port), timeout=0.3):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError(f"{kind} did not bind its scratch listener")
            yield "http://" + addr
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def sha(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--profile", type=Path, required=True)
    p.add_argument("--minio", type=Path, required=True)
    p.add_argument("--versity", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    a = p.parse_args()
    a.output.mkdir()
    results = {"profile_sha256": sha(a.profile), "servers": {}}
    for server, binary in (("minio", a.minio), ("versity", a.versity)):
        binary = binary.resolve(strict=True)
        work = (a.output / server).resolve()
        work.mkdir()
        with launch(server, binary, work) as endpoint:
            with (work / "profile.json").open("xb") as out, (work / "profile.err").open("xb") as err:
                result = subprocess.run([a.profile.resolve(), "-endpoint", endpoint, "-json"], stdout=out, stderr=err, timeout=240)
        results["servers"][server] = {
            "binary_sha256": sha(binary), "exit": result.returncode,
            "profile": json.loads((work / "profile.json").read_text()),
        }
    (a.output / "summary.json").write_text(json.dumps(results, indent=2) + "\n")


if __name__ == "__main__":
    main()
