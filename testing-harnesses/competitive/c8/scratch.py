"""Probe bounded decoded-stream scratch with independently addressable compressed blocks."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import random
import statistics
import tempfile
import time
import zlib

BLOCK = 256 << 10
MAX_DECODED = 128 << 20


def construct(source, raw, packed):
    blocks = []
    original = hashlib.sha256()
    original_size = 0
    plain_cpu = packed_cpu = 0.
    with source as inp, raw.open("wb") as plain, packed.open("wb") as encoded:
        while data := inp.read(BLOCK):
            original.update(data)
            original_size += len(data)
            if original_size > MAX_DECODED:
                raise ValueError("fixture exceeds the decoded scratch budget")
            start = time.process_time()
            plain.write(data)
            plain_cpu += time.process_time() - start
            start = time.process_time()
            code = zlib.compress(data, 1)
            compressed = len(code) < len(data)
            payload = code if compressed else data
            blocks.append((encoded.tell(), len(payload), compressed))
            encoded.write(payload)
            packed_cpu += time.process_time() - start
        started = time.process_time()
        plain.flush()
        plain_cpu += time.process_time() - started
        started = time.process_time()
        encoded.flush()
        packed_cpu += time.process_time() - started
    return original_size, original.hexdigest(), blocks, plain_cpu, packed_cpu


def read_chunk(stream, entry):
    at, size, compressed = entry
    stream.seek(at)
    data = stream.read(size)
    if len(data) != size:
        raise ValueError("truncated scratch block")
    return zlib.decompress(data) if compressed else data


def analyze(source, decoded, directory, sequence):
    plain, blocks = directory / "plain", directory / "blocks"
    size, digest, index, raw_cpu, block_cpu = construct(
        gzip.open(source, "rb") if decoded else source.open("rb"), plain, blocks)
    exact = hashlib.sha256()
    with blocks.open("rb") as coded:
        for entry in index:
            exact.update(read_chunk(coded, entry))
    if exact.hexdigest() != digest:
        raise ValueError("decoded scratch identity mismatch")
    offsets = [random.Random(k + 144 + sequence).randrange(max(1, size - 4096 + 1)) for k in range(200)]
    ns_raw, ns_blocks = [], []
    with plain.open("rb") as raw, blocks.open("rb") as coded:
        for offset in offsets:
            start = time.perf_counter_ns()
            raw.seek(offset)
            reference = raw.read(4096)
            ns_raw.append(time.perf_counter_ns() - start)
            first, inner = divmod(offset, BLOCK)
            start = time.perf_counter_ns()
            result = read_chunk(coded, index[first])[inner:inner + 4096]
            if len(result) < 4096 and first + 1 < len(index):
                result += read_chunk(coded, index[first+1])[:4096-len(result)]
            ns_blocks.append(time.perf_counter_ns() - start)
            if result != reference:
                raise ValueError(f"scratch range differs at {offset}")
    # Charge a fixed representation for each in-memory offset/length/codec entry.
    return {"fixture": source.name, "mode": "gzip-decoded" if decoded else "opaque",
            "sequence": sequence, "logical_bytes": size, "sha256": digest,
            "raw_allocated": plain.stat().st_blocks * 512,
            "block_allocated": blocks.stat().st_blocks * 512,
            "minimum_index_bytes": len(index) * 16, "block_bytes": BLOCK,
            "raw_write_cpu_seconds": raw_cpu, "block_write_cpu_seconds": block_cpu,
            "raw_range_p50_us": statistics.median(ns_raw) / 1000,
            "block_range_p50_us": statistics.median(ns_blocks) / 1000,
            "raw_range_p95_us": sorted(ns_raw)[189] / 1000,
            "block_range_p95_us": sorted(ns_blocks)[189] / 1000,
            "verified_ranges": len(offsets)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gzip", type=Path, required=True)
    parser.add_argument("--opaque", type=Path, required=True)
    parser.add_argument("--scratch", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    args.scratch.mkdir(parents=True, exist_ok=True)
    with args.results.open("x") as output:
        for seq in range(3):
            for path, decoded in ((args.gzip, True), (args.opaque, False)):
                with tempfile.TemporaryDirectory(dir=args.scratch) as tmp:
                    print(json.dumps(analyze(path, decoded, Path(tmp), seq)), file=output, flush=True)


if __name__ == "__main__":
    main()
