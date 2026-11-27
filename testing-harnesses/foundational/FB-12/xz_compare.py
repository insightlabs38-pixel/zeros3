"""Compare per-record xz -9e with the production DEFLATE fallback."""

import argparse
import hashlib
import json
import lzma
from pathlib import Path
import resource
import struct
import tempfile
import time
import zlib


def read_records(path):
    data = memoryview(path.read_bytes())
    magic, max_chunk, header, footer, record, index, target = struct.unpack_from("<4s6I", data)
    assert magic == b"ZRC1" and max_chunk == 256 << 10
    cursor = 28
    while cursor < len(data):
        raw_n, deflate_n, blocked_n, deflate_kind, _ = struct.unpack_from("<3I2B", data, cursor)
        cursor += 14
        raw = data[cursor : cursor + raw_n]
        deflate = data[cursor + raw_n : cursor + raw_n + deflate_n]
        cursor += raw_n + deflate_n + blocked_n
        yield raw, deflate, deflate_kind, (header, footer, record, index, target)
    assert cursor == len(data)


def cpu_ns():
    usage = resource.getrusage(resource.RUSAGE_SELF)
    return int((usage.ru_utime + usage.ru_stime) * 1e9)


def pack_size(stored, framing):
    header, footer, record, index, target = framing
    apparent = allocated = packs = 0
    cursor = 0
    with tempfile.TemporaryDirectory(prefix="fb12-xz-") as work:
        while cursor < len(stored):
            start = cursor
            size = header + footer
            while cursor < len(stored):
                add = len(stored[cursor][0]) + record + index
                if size + add > target and cursor > start:
                    break
                size += add
                cursor += 1
            path = Path(work) / str(packs)
            with path.open("wb") as out:
                out.write(bytes(header))
                for body, _ in stored[start:cursor]:
                    out.write(bytes(record))
                    out.write(body)
                out.write(bytes(footer + index * (cursor - start)))
            info = path.stat()
            assert info.st_size == size
            apparent += info.st_size
            allocated += info.st_blocks * 512
            packs += 1
    return apparent, allocated, packs


def measure(path, rounds):
    rows = list(read_records(path))
    wanted = [hashlib.sha256(row[0]).digest() for row in rows]
    chosen = []
    selected = 0
    start, cpu = time.perf_counter_ns(), cpu_ns()
    for raw, deflate, codec, geometry in rows:
        raw_len = len(raw)
        coded = lzma.compress(raw, preset=9 | lzma.PRESET_EXTREME)
        if len(coded) < len(deflate) and len(coded) <= raw_len - max(1, raw_len // 16):
            chosen.append((coded, 6))
            selected += 1
        else:
            chosen.append((deflate, codec))
    encode_wall, encode_cpu = time.perf_counter_ns() - start, cpu_ns() - cpu
    pack, allocated, packs = pack_size(chosen, geometry)
    cpu_samples, wall_samples = [], []
    for _ in range(rounds):
        start, cpu = time.perf_counter_ns(), cpu_ns()
        for (payload, codec), (raw, _, _, _), digest in zip(chosen, rows, wanted, strict=True):
            if codec == 6:
                restored = lzma.decompress(payload)
            elif codec == 1:
                restored = zlib.decompress(payload, wbits=-15)
            else:
                restored = payload
            assert restored == raw and hashlib.sha256(restored).digest() == digest
        wall_samples.append(time.perf_counter_ns() - start)
        cpu_samples.append(cpu_ns() - cpu)
    return dict(
        corpus=path.stem, lane="xz-9e", records=len(rows),
        native_logical_bytes=sum(len(row[0]) for row in rows),
        selected_range_records=selected,
        stored_payload_bytes=sum(len(row[0]) for row in chosen),
        pack_bytes=pack, allocated_bytes=allocated, packs=packs,
        encode_wall_ns=encode_wall, encode_cpu_ns=encode_cpu,
        decode_wall_ns=wall_samples, decode_cpu_ns=cpu_samples,
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=Path)
    parser.add_argument("--rounds", type=int, default=3)
    args = parser.parse_args()
    for file in sorted(args.input.glob("*.zrc")):
        print(json.dumps(measure(file, args.rounds), sort_keys=True), flush=True)
