"""Recreate the original C1 SST bytes with a sparse producer-metadata fixup."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import struct
import sys
from tempfile import TemporaryDirectory
import zlib

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c1"))
from codec_corpus import sst

HEADER = struct.Struct("<II")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def difference(original, regenerated):
    positions = set()
    for content in regenerated:
        if len(content) != len(original):
            raise ValueError("producer length changed")
        positions.update(i for i, (a, b) in enumerate(zip(original, content)) if a != b)
    runs = []
    for pos in sorted(positions):
        start, end = max(0, pos-7), min(len(original), pos+8)
        if runs and runs[-1][1] >= start:
            runs[-1][1] = end
        else:
            runs.append([start, end])
    raw = b"".join(HEADER.pack(a, b-a) + original[a:b] for a, b in runs)
    return base64.b64encode(zlib.compress(raw, 9)).decode()


def repair(data, record):
    content = bytearray(data)
    if len(content) != record["size"]:
        raise ValueError("producer length changed")
    raw = zlib.decompress(base64.b64decode(record["fixups"], validate=True))
    offset = previous = 0
    while offset < len(raw):
        start, size = HEADER.unpack_from(raw, offset)
        offset += HEADER.size
        if start < previous or not size or start + size > len(content) or offset + size > len(raw):
            raise ValueError("invalid sparse fixup")
        content[start:start+size] = raw[offset:offset+size]
        offset += size
        previous = start + size
    if digest(content) != record["sha256"]:
        raise ValueError("restored bytes disagree with C1 SHA-256")
    return content


def produce(path, revision):
    sst(path, revision)
    data = path.read_bytes()
    path.unlink()
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("corpus", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--capture", action="store_true")
    args = parser.parse_args()
    if args.capture:
        rows = []
    else:
        rows = json.loads(args.manifest.read_text())
    with TemporaryDirectory(dir=args.manifest.resolve().parent if args.capture else args.corpus) as tmp:
        for rev in range(3):
            path = args.corpus / "sst" / str(rev) / "snapshot.sst"
            probe = Path(tmp) / "probe.sst"
            if args.capture:
                original = path.read_bytes()
                replacements = [produce(probe, rev) for _ in range(4)]
                rows.append({"revision": rev, "size": len(original), "sha256": digest(original),
                             "fixups": difference(original, replacements)})
            record = rows[rev]
            content = repair(produce(probe, rev), record)
            if not args.capture:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(content)
    if args.capture:
        with args.manifest.open("x") as out:
            json.dump(rows, out, separators=(",", ":"))
            out.write("\n")


if __name__ == "__main__":
    main()
