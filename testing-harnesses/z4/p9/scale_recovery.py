#!/usr/bin/env python3
"""Exercise recovery against a disposable shadow of an offline scale fixture."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
from urllib.parse import quote

ACCESS = "AKIAZEROS3SCALELOCAL01"
SECRET = "zeros3ScaleLocalScratchAuthenticationOnly01"
CHECKPOINT = "namespace-v2.zck"


def digest(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


@contextlib.contextmanager
def process(binary, store, log):
    with socket.socket() as port:
        port.bind(("127.0.0.1", 0))
        listen = port.getsockname()[1]
    target = f"127.0.0.1:{listen}"
    with log.open("xb") as output:
        child = subprocess.Popen(
            [binary, "serve", "-namespace-checkpoint", "-store", store, "-addr", target,
             "-access-key", ACCESS, "-secret-key", SECRET],
            stdout=output, stderr=output,
        )
        try:
            yield child, listen, f"http://{target}"
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
                    raise


def wait_live(child, port):
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        if child.poll() is not None:
            raise RuntimeError(f"server stopped while reopening ({child.returncode})")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return
        except OSError:
            time.sleep(0.15)
    raise TimeoutError("server did not reopen within three minutes")


def curl(endpoint, path, output, *extra):
    req = ["curl", "--silent", "--show-error", "--fail", "--noproxy", "*", "--max-time", "45",
           "--aws-sigv4", "aws:amz:us-east-1:s3", "--user", f"{ACCESS}:{SECRET}", "--output", str(output)]
    subprocess.run([*req, *extra, endpoint + path], check=True)


def check_objects(endpoint, sample, scratch):
    for key, stamp in sample:
        curl(endpoint, key, scratch)
        if digest(scratch) != stamp:
            raise RuntimeError(f"{key} contained the wrong acknowledged bytes")


def sample_fixture(manifest):
    samples = []
    with manifest.open() as stream:
        for index, line in enumerate(stream):
            if index % 16381:
                continue
            row = json.loads(line)
            if row["Deleted"]:
                continue
            samples.append(("/" + row["Bucket"] + "/" + quote(row["Key"]), row["SHA"]))
            if len(samples) == 6:
                break
    if len(samples) != 6:
        raise ValueError("fixture manifest has too few visible sampled objects")
    return samples


@contextlib.contextmanager
def flipped(path, offset, whence=os.SEEK_SET):
    with path.open("r+b") as stream:
        where = stream.seek(offset, whence)
        original = stream.read(1)
        if not original:
            raise RuntimeError("fault offset outside shadow file")
        stream.seek(where)
        stream.write(bytes([original[0] ^ 1]))
        stream.flush()
        os.fsync(stream.fileno())
        try:
            yield
        finally:
            stream.seek(where)
            stream.write(original)
            stream.flush()
            os.fsync(stream.fileno())


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True, type=Path)
    p.add_argument("--source", required=True, type=Path, help="offline source fixture with a v2 checkpoint")
    p.add_argument("--manifest", required=True, type=Path)
    p.add_argument("--shadow", required=True, type=Path)
    args = p.parse_args()
    src = args.source.resolve(strict=True)
    probe = args.shadow.resolve()
    if probe == src or src in probe.parents:
        p.error("shadow must be a separate scratch path")
    probe.mkdir()
    store = probe / "store"

    def copy_item(a, b):
        relative = Path(a).relative_to(src)
        if relative.parts[0] == "packs" and relative.suffix == ".pack":
            return os.link(a, b)
        return shutil.copy2(a, b)

    old_journal = src / "journal/visibility.log"
    old_checkpoint = src / "journal" / CHECKPOINT
    original = {p.name: digest(p) for p in (old_journal, old_checkpoint)}
    shutil.copytree(src, store, copy_function=copy_item, ignore=shutil.ignore_patterns("namespace-v1.zck"))
    journal = store / "journal/visibility.log"
    checkpoint = store / "journal" / CHECKPOINT
    targets = sample_fixture(args.manifest)
    payload = probe / "acknowledged.bin"
    scratch = probe / "response.bin"
    extra = "/p9-recovery"
    results = []

    for cut in range(3):
        log = probe / f"kill-{cut}.log"
        with process(args.binary.resolve(), store, log) as (child, port, endpoint):
            wait_live(child, port)
            check_objects(endpoint, targets, scratch)
            if cut == 0:
                curl(endpoint, extra, scratch, "-X", "PUT")
            payload.write_bytes((f"durable-tail-{cut}\n".encode()) * 1024)
            key = f"{extra}/tail-{cut}"
            curl(endpoint, key, scratch, "-T", str(payload))
            targets.append((key, digest(payload)))
            child.kill()
            child.wait(timeout=20)
            results.append({"trial": f"kill-{cut}", "signal": -child.returncode, "checked_next_open": len(targets)})
    log = probe / "stale-checkpoint.log"
    with flipped(checkpoint, -1, os.SEEK_END):
        with process(args.binary.resolve(), store, log) as (child, port, endpoint):
            wait_live(child, port)
            check_objects(endpoint, targets, scratch)
    if "checkpoint ignored" not in log.read_text():
        raise RuntimeError("damaged checkpoint was not rejected on reopen")
    results.append({"trial": "damaged_checkpoint", "exact_objects": len(targets)})

    log = probe / "corrupt-prefix.log"
    with flipped(journal, 25):
        with process(args.binary.resolve(), store, log) as (child, _, __):
            exit_code = child.wait(timeout=180)
    if exit_code == 0 or "crc mismatch" not in log.read_text():
        raise RuntimeError("journal corruption was not diagnosed on startup")
    results.append({"trial": "corrupt_journal_prefix", "refusal": exit_code})

    last_end = journal.stat().st_size
    with journal.open("ab") as f:
        f.write(b"partial")
        f.flush()
        os.fsync(f.fileno())
    with process(args.binary.resolve(), store, probe / "torn-tail.log") as (child, port, endpoint):
        wait_live(child, port)
        check_objects(endpoint, targets, scratch)
    if journal.stat().st_size != last_end:
        raise RuntimeError("torn tail did not truncate to the last acknowledged state")
    results.append({"trial": "torn_journal_tail", "exact_objects": len(targets)})
    if {p.name: digest(p) for p in (old_journal, old_checkpoint)} != original:
        raise RuntimeError("offline source journal or checkpoint changed")
    with (probe / "results.json").open("x") as stream:
        json.dump({"original_sha256": original, "trials": results}, stream, indent=2)
        stream.write("\n")


if __name__ == "__main__":
    main()
