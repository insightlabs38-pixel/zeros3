"""Prototype exact page/run deltas with a bounded, verified seek table."""
import argparse
from bisect import bisect_left
from collections import Counter, defaultdict
from hashlib import sha256
from pathlib import Path
import struct
import zlib

from fixtures import BLK


SPAN = 16 * BLK
LIMIT = 32 << 20
MAGIC = b"ZC14PG01"
HEADER = struct.Struct("<8sQQ32s32s")
ROW = struct.Struct("<I32s")
INTEGER = struct.Struct("<I")
ZERO = bytes(BLK)


def pages(data):
    return [data[at:at+BLK] for at in range(0, len(data), BLK)]


def varint(page, at):
    value = 0
    for width in range(9):
        byte = page[at+width]
        if width == 8:
            return value << 8 | byte, at+width+1
        value = value << 7 | byte & 127
        if byte < 128:
            return value, at+width+1
    raise ValueError("SQLite varint")


def rowids(page, number):
    head = 0 if number else 100
    if len(page) != BLK or page[head] != 0x0d:
        return ()
    n = int.from_bytes(page[head+3:head+5], "big")
    end = head+8+2*n
    first = int.from_bytes(page[head+5:head+7], "big")
    if not n or end > first or first >= BLK:
        return ()
    try:
        pointers = (int.from_bytes(page[p:p+2], "big") for p in range(head+8, end, 2))
        rows = []
        for pointer in pointers:
            if not first <= pointer <= BLK-2:
                return ()
            _, key = varint(page, pointer)
            value, _ = varint(page, key)
            rows.append(value)
    except IndexError:
        return ()
    if rows != sorted(set(rows)):
        return ()
    return tuple(rows)


def xor(old, new):
    return (int.from_bytes(old, "little") ^ int.from_bytes(new, "little")).to_bytes(len(new), "little")


def plan(base, target, mode):
    locations = defaultdict(list)
    identity = defaultdict(list)
    for i, data in enumerate(base):
        if data == ZERO[:len(data)]:
            continue
        locations[sha256(data).digest()].append(i)
        if mode == "sqlite":
            for key in rowids(data, i):
                if len(identity[key]) < 4:
                    identity[key].append(i)

    exact = {}
    anchors = []
    for i, data in enumerate(target):
        if data == ZERO[:len(data)]:
            continue
        options = locations.get(sha256(data).digest(), ())
        if options:
            place = bisect_left(options, i)
            choices = options[max(0, place-1):place+1]
            source = min(choices, key=lambda j: abs(j-i))
            if data == base[source]:
                exact[i] = source
                if len(options) == 1:
                    anchors.append(i)
    drift = Counter(exact[i]-i for i in anchors)
    common = [d for d, n in drift.most_common(3) if d and n >= 2]

    for i, data in enumerate(target):
        if data == ZERO[:len(data)]:
            yield 0, 0, b""
            continue
        if i in exact:
            yield 1, exact[i], b""
            continue
        best = (2, 0, data)
        if mode != "runs":
            pos = bisect_left(anchors, i)
            nearby = [exact[j]-j for j in anchors[max(0, pos-1):pos+1] if abs(j-i) <= 16]
            keys = rowids(data, i) if mode == "sqlite" else ()
            votes = Counter(j for key in keys for j in identity.get(key, ()))
            candidates = dict.fromkeys([*(j for j, _ in votes.most_common(3)), i,
                                        *(i+d for d in dict.fromkeys(nearby+common))])
            price = len(zlib.compress(data, 6))
            for source in candidates:
                if not 0 <= source < len(base) or len(base[source]) != len(data):
                    continue
                previous = base[source]
                equal = sum(previous[k] == data[k] for k in range(0, len(data), 67))
                if equal < max(2, len(data)//268):
                    continue
                residual = zlib.compress(xor(previous, data), 6)
                cost = len(residual) + 6
                if cost < price:
                    best = (3, source, residual)
                    price = cost
        yield best


def frame(ops):
    payload = bytearray()
    i = 0
    while i < len(ops):
        kind, source, data = ops[i]
        end = i+1
        while kind != 3 and end < len(ops) and ops[end][0] == kind:
            if kind == 1 and ops[end][1] != source+end-i:
                break
            end += 1
        payload.extend((kind, end-i))
        if kind in (1, 3):
            payload.extend(INTEGER.pack(source))
        if kind == 2:
            for item in ops[i:end]:
                payload.extend(item[2])
        elif kind == 3:
            payload.extend(struct.pack("<H", len(data)))
            payload.extend(data)
        i = end
    return b"\x01"+zlib.compress(payload, 6)


def encode(base, target, mode):
    if not 0 < len(base) <= LIMIT or not 0 < len(target) <= LIMIT:
        raise ValueError("atlas byte budget")
    operations = list(plan(pages(base), pages(target), mode))
    output = bytearray(HEADER.pack(MAGIC, len(base), len(target),
                                   sha256(base).digest(), sha256(target).digest()))
    frames = []
    for at in range(0, len(target), SPAN):
        source_page = at//BLK
        original = target[at:at+SPAN]
        packet = frame(operations[source_page:source_page+SPAN//BLK])
        literal = b"\x00"+zlib.compress(original, 6)
        if len(literal) < len(packet):
            packet = literal
        output.extend(ROW.pack(len(packet), sha256(original).digest()))
        frames.append(packet)
    return bytes(output)+b"".join(frames), Counter(code for code, _, _ in operations)


def inflate(data, maximum):
    stream = zlib.decompressobj()
    out = stream.decompress(data, maximum+1)
    if len(out) > maximum or not stream.eof or stream.unused_data or stream.unconsumed_tail:
        raise ValueError("invalid or oversized atlas frame")
    return out


def unpack(payload, base, base_bytes, size):
    if payload[0] == 0:
        return inflate(payload[1:], size)
    if payload[0] != 1:
        raise ValueError("invalid atlas frame tag")
    p = memoryview(inflate(payload[1:], size+16*(2+6)))
    out = bytearray()
    while p:
        if len(p) < 2:
            raise ValueError("truncated atlas command")
        kind, count = p[:2]
        p = p[2:]
        length = min(count*BLK, size-len(out))
        if not count or length <= (count-1)*BLK or kind > 3 or kind == 3 and count != 1:
            raise ValueError("invalid atlas command")
        if kind in (1, 3):
            if len(p) < 4:
                raise ValueError("truncated atlas source")
            offset = INTEGER.unpack_from(p)[0]*BLK
            p = p[4:]
            if offset+length > base_bytes:
                raise ValueError("atlas source outside checked base")
            base.seek(offset)
            old = base.read(length)
            if len(old) != length:
                raise ValueError("short atlas source")
        if kind == 0:
            out.extend(bytes(length))
        elif kind == 1:
            out.extend(old)
        elif kind == 2:
            if len(p) < length:
                raise ValueError("truncated atlas literals")
            out.extend(p[:length])
            p = p[length:]
        else:
            if len(p) < 2:
                raise ValueError("truncated atlas residual header")
            n = struct.unpack_from("<H", p)[0]
            p = p[2:]
            if not n or len(p) < n:
                raise ValueError("truncated atlas residual")
            residual = inflate(p[:n], length)
            if len(residual) != length:
                raise ValueError("short atlas residual")
            out.extend(xor(old, residual))
            p = p[n:]
    return bytes(out)


class Atlas:
    def __init__(self, stream, base):
        raw = stream.read(HEADER.size)
        if len(raw) != HEADER.size:
            raise ValueError("atlas header truncated")
        magic, self.base_size, self.size, base_hash, self.digest = HEADER.unpack(raw)
        if magic != MAGIC or not 0 < self.size <= LIMIT or not 0 < self.base_size <= LIMIT:
            raise ValueError("atlas header invalid")
        hash_base = sha256()
        self.base_verification_bytes = 0
        while data := base.read(1 << 20):
            self.base_verification_bytes += len(data)
            if self.base_verification_bytes > self.base_size:
                raise ValueError("atlas base larger than sealed identity")
            hash_base.update(data)
        if self.base_verification_bytes != self.base_size or hash_base.digest() != base_hash:
            raise ValueError("atlas base digest differs")
        n = (self.size+SPAN-1)//SPAN
        header_size = HEADER.size+n*ROW.size
        table = stream.read(n*ROW.size)
        if len(table) != n*ROW.size:
            raise ValueError("atlas table truncated")
        self.rows = []
        offset = header_size
        for length, checksum in ROW.iter_unpack(table):
            if not 1 < length <= SPAN+128:
                raise ValueError("atlas frame exceeds bounds")
            self.rows.append((offset, length, checksum))
            offset += length
        stream.seek(0, 2)
        if stream.tell() != offset:
            raise ValueError("atlas file size differs")
        self.stream = stream
        self.base = base

    def blocks(self, start, end):
        if not 0 <= start < end <= self.size:
            raise ValueError("atlas range outside target")
        for index in range(start//SPAN, (end-1)//SPAN+1):
            at = index*SPAN
            offset, length, checksum = self.rows[index]
            self.stream.seek(offset)
            payload = self.stream.read(length)
            if len(payload) != length:
                raise ValueError("short atlas frame")
            n = min(SPAN, self.size-at)
            data = unpack(payload, self.base, self.base_size, n)
            if len(data) != n or sha256(data).digest() != checksum:
                raise ValueError("atlas frame digest differs")
            yield data[max(0, start-at):min(end-at, n)]

    def restore(self, out, sparse):
        result = sha256()
        for data in self.blocks(0, self.size):
            result.update(data)
            if not sparse:
                out.write(data)
                continue
            for page in pages(data):
                if page == ZERO[:len(page)]:
                    out.seek(len(page), 1)
                else:
                    out.write(page)
        out.truncate(self.size)
        if result.digest() != self.digest:
            raise ValueError("atlas whole digest differs")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=("encode", "restore"))
    for name in ("base", "target", "program"):
        p.add_argument(name, type=Path)
    p.add_argument("--mode", choices=("runs", "xor", "sqlite"), default="xor")
    p.add_argument("--sparse", action="store_true")
    args = p.parse_args()
    if args.action == "encode":
        payload, counts = encode(args.base.read_bytes(), args.target.read_bytes(), args.mode)
        args.program.write_bytes(payload)
        print(dict(counts))
    else:
        with args.base.open("rb") as base, args.program.open("rb") as program:
            reader = Atlas(program, base)
            with args.target.open("xb") as output:
                reader.restore(output, args.sparse)


if __name__ == "__main__":
    main()
