#!/usr/bin/env python3
"""Run a matched ZeroS3 retained-history pilot and append observations."""
import argparse
import collections
import contextlib
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time

BUCKET = "retained-pilot"
ACCESS = "AKIAZEROS3PILOTLOCAL01"
SECRET = "zeros3PilotLocalScratchAuthenticationOnly01"
STORES = ("cdc", "cold")


def now():
    return dt.datetime.now(dt.timezone.utc)


def save(path, value):
    with path.open("x") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def allocated(path):
    return int(subprocess.check_output(["du", "-s", "-B1", str(path)]).split()[0])


def timed(binary, store, out, name, *args, subcommand=None):
    """Measure each maintenance process and sample the store's allocated bytes."""
    prefix = [name, subcommand] if subcommand else [name]
    command = [binary, *prefix, "-store", store, *args]
    start = time.monotonic()
    before = allocated(store)
    peak = before
    min_free = shutil.disk_usage(store).free
    with (out / f"{name}.out").open("xb") as stdout, (out / f"{name}.err").open("xb") as stderr:
        proc = subprocess.Popen(command, stdout=stdout, stderr=stderr)
        while True:
            pid, status, usage = os.wait4(proc.pid, os.WNOHANG)
            if pid:
                proc.returncode = os.waitstatus_to_exitcode(status)
                break
            peak = max(peak, allocated(store))
            min_free = min(min_free, shutil.disk_usage(store).free)
            time.sleep(0.2)
    after = allocated(store)
    stats = {
        "command": [*prefix, *args], "exit": proc.returncode, "wall_s": time.monotonic() - start,
        "cpu_s": usage.ru_utime + usage.ru_stime, "max_rss_kib": usage.ru_maxrss,
        "allocated_before": before, "allocated_after": after,
        "allocated_peak_sampled": max(peak, after),
        "free_min_sampled": min(min_free, shutil.disk_usage(store).free),
    }
    save(out / f"{name}.timing.json", stats)
    if proc.returncode:
        raise subprocess.CalledProcessError(proc.returncode, command)
    return stats


@contextlib.contextmanager
def serve(binary, store, log):
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    address = f"127.0.0.1:{port}"
    with log.open("xb") as err:
        proc = subprocess.Popen(
            [binary, "serve", "-store", store, "-addr", address, "-access-key", ACCESS, "-secret-key", SECRET],
            stdout=err, stderr=err, env={**os.environ, "ZEROS3_CHUNK_CACHE_MIB": "64"},
        )
        try:
            until = time.monotonic() + 30
            while time.monotonic() < until and proc.poll() is None:
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=0.25):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError(f"ZeroS3 did not start, see {log}")
            yield f"http://{address}"
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=30)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise


def request(url, method, key="", *, upload=None, span=None, output=None):
    route = f"/{BUCKET}" + (f"/{key}" if key else "")
    command = [
        "curl", "--silent", "--show-error", "--fail-with-body", "--max-time", "600",
        "--noproxy", "*", "--aws-sigv4", "aws:amz:us-east-1:s3", "--user", f"{ACCESS}:{SECRET}",
        "--request", method, "--output", str(output or os.devnull), "--write-out", "%{json}",
    ]
    if upload:
        command += ["--upload-file", str(upload)]
    if span:
        command += ["--range", f"{span[0]}-{span[1]}"]
    result = subprocess.run([*command, url + route], capture_output=True, text=True, check=False)
    row = json.loads(result.stdout)
    code = 206 if span else 200
    if result.returncode or row["response_code"] != code:
        raise RuntimeError(f"{method} {route}: HTTP {row['response_code']}, curl {result.returncode}: {result.stderr.strip()}")
    return row


def build(args, run, binary):
    assets = []
    for family, files in args.history:
        if len(files) < 3:
            raise ValueError(f"{family}: at least three chronological versions required")
        for n, filename in enumerate(files):
            path = Path(filename).resolve(strict=True)
            if path.stat().st_size < 4096:
                raise ValueError(f"{path}: input must hold at least one 4 KiB read")
            assets.append({
                "family": family, "ordinal": n, "key": f"{family}/revision-{n}",
                "source": str(path), "sha256": digest(path), "size": path.stat().st_size,
            })
    if not assets:
        raise ValueError("at least one --history FAMILY FILE FILE FILE is required")
    save(run / "fixture.json", assets)
    cdc = run / "cdc"
    with serve(binary, cdc, run / "seed.serve.log") as url:
        request(url, "PUT")
        for asset in assets:
            path = Path(asset["source"])
            request(url, "PUT", asset["key"], upload=path)
            asset["stored_at"] = now().isoformat()
            request(url, "PUT", asset["family"] + "/retained", upload=path)
            asset["retained_put_ack_at"] = now().isoformat()
    save(run / "ingest.json", assets)
    shutil.copytree(cdc, run / "cold")
    for label in STORES:
        store = run / label
        report = run / f"prepare-{label}"
        report.mkdir()
        if label == "cold":
            timed(binary, store, report, "authority", "-tier", "cold", "-apply", "-workers", "1", subcommand="migrate")
        timed(binary, store, report, "gc", "-apply", "-json")
        timed(binary, store, report, "repack", "-apply", "-recompress", "-json")
        timed(binary, store, report, "stats", "-json")
        if label == "cold":
            with (report / "authority-status.json").open("xb") as out:
                subprocess.run([binary, "authority", "status", "-store", store], stdout=out, check=True)


def versions(binary, store, out, assets):
    for family in sorted({a["family"] for a in assets}):
        rows = json.loads(subprocess.check_output(
            [binary, "versions", "-store", store, "-bucket", BUCKET, "-key", family + "/retained", "-json"],
        ))
        expected = sum(a["family"] == family for a in assets)
        if len(rows) != expected or sum(row["status"] == "current" for row in rows) != 1:
            raise RuntimeError(f"{family}: found {len(rows)} versions, expected {expected}")
        save(out / f"versions-{family}.json", rows)


def sample(run, binary):
    started = now()
    samples = run / "samples"
    samples.mkdir(exist_ok=True)
    index = len(list(samples.iterdir()))
    output = samples / started.strftime("%Y%m%dT%H%M%S.%fZ")
    output.mkdir()
    assets = json.loads((run / "ingest.json").read_text())
    size = {}
    throughput = {}
    try:
        latest_keys = {}
        for asset in assets:
            if digest(Path(asset["source"])) != asset["sha256"]:
                raise RuntimeError(f"fixture changed: {asset['source']}")
            latest_keys[asset["family"]] = asset["key"]
        for label in STORES[::1 if index % 2 == 0 else -1]:
            store = run / label
            out = output / label
            out.mkdir()
            size[label] = allocated(store)
            by_key = collections.Counter()
            whole = collections.Counter()
            frequencies = collections.Counter()
            with (out / "requests.jsonl").open("x", buffering=1) as requests:
                with serve(binary, store, out / "serve.log") as url:
                    body = out / "body.tmp"
                    for asset in assets:
                        key = asset["key"]
                        latest = key == latest_keys[asset["family"]]
                        offset = max(0, asset["size"] - 8192)
                        reads = [("first-process-read", None), ("repeat-whole", None)]
                        reads += [("repeat-range", (offset, offset + 4095))] * (8 if latest else 1)
                        for phase, span in reads:
                            before = now()
                            row = request(url, "GET", key, span=span, output=body)
                            if span:
                                with Path(asset["source"]).open("rb") as original:
                                    original.seek(span[0])
                                    expected = hashlib.sha256(original.read(span[1] - span[0] + 1)).hexdigest()
                            else:
                                expected = asset["sha256"]
                            actual = digest(body)
                            requests.write(json.dumps({
                                "at": before.isoformat(), "key": key, "phase": phase, "range": span,
                                "age_since_ingest_s": (before - dt.datetime.fromisoformat(asset["stored_at"])).total_seconds(),
                                "bytes": row["size_download"], "http": row["response_code"], "seconds": row["time_total"],
                                "first_byte_s": row["time_starttransfer"], "exact": actual == expected,
                            }) + "\n")
                            if actual != expected:
                                raise RuntimeError(f"{label} {key} {phase}: expected {expected}, got {actual}")
                            body.unlink()
                            frequencies[key] += 1
                            if not span:
                                whole[phase] += row["size_download"]
                                by_key[phase] += row["time_total"]
            throughput[label] = {phase: whole[phase] / elapsed for phase, elapsed in by_key.items()}
            save(out / "frequencies.json", frequencies)
            versions(binary, store, out, assets)
            timed(binary, store, out, "verify", "-deep", "-json")
            timed(binary, store, out, "stats", "-json")
        save(output / "observation.json", {
            "start": started.isoformat(), "finish": now().isoformat(), "errors": 0, "allocated_bytes": size,
            "allocated_saving": 1 - size["cold"] / size["cdc"],
            "whole_bytes_per_second": throughput,
            "whole_ratio": {p: throughput["cold"][p] / throughput["cdc"][p] for p in throughput["cdc"]},
            "extended_observation": "PENDING 30-DAY RUN",
        })
    except Exception as exc:
        save(output / "failure.json", {"at": now().isoformat(), "errors": 1, "reason": str(exc)})
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=["prepare", "sample"])
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--run-dir", required=True, type=Path)
    parser.add_argument("--history", nargs="+", action="append", default=[], metavar="FAMILY_OR_FILE")
    args = parser.parse_args()
    if args.mode == "sample" and args.history:
        parser.error("sample reads the existing fixture, pass --history to prepare")
    families = set()
    for family, *names in args.history:
        if not family.isascii() or not family.replace("-", "").isalnum() or family.lower() != family or family in families:
            parser.error(f"invalid or duplicate family: {family}")
        families.add(family)
    args.history = [(family, paths) for family, *paths in args.history]
    binary = args.binary.resolve(strict=True)
    run = args.run_dir.resolve()
    binary_sha = digest(binary)
    if args.mode == "prepare":
        run.mkdir()
        save(run / "source.json", {"binary": str(binary), "binary_sha256": binary_sha, "harness_sha256": digest(Path(__file__))})
        try:
            build(args, run, binary)
        except Exception as exc:
            save(run / "prepare-failure.json", {"at": now().isoformat(), "reason": str(exc)})
            raise
        save(run / "prepared.json", {"at": now().isoformat()})
    if not (run / "prepared.json").is_file():
        parser.error("the run has no successful preparation")
    identity = json.loads((run / "source.json").read_text())
    if binary_sha != identity["binary_sha256"] or digest(Path(__file__)) != identity["harness_sha256"]:
        parser.error("the binary or harness digest differs from preparation, start a separate run")
    sample(run, binary)


if __name__ == "__main__":
    main()
