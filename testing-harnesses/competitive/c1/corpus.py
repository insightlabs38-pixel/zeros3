"""Generate fixed synthetic C1 histories and their SHA-256 manifest."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import random
import sqlite3
import struct
import tarfile
import zipfile

MIB = 1 << 20
FAMILIES = (
    "boilerplate", "json", "gzip-control", "source", "native", "package",
    "oci", "sqlite", "tensor", "shift", "repeat", "entropy",
)


def blob(seed, size):
    return random.Random(seed).randbytes(size)


def archive(members, kind):
    out = io.BytesIO()
    if kind == "zip":
        with zipfile.ZipFile(out, "w") as pack:
            for name, data in members.items():
                pack.writestr(zipfile.ZipInfo(name, (2020, 1, 1, 0, 0, 0)), data,
                              compress_type=zipfile.ZIP_DEFLATED, compresslevel=6)
        return out.getvalue()
    with tarfile.open(fileobj=out, mode="w", format=tarfile.USTAR_FORMAT) as pack:
        for name, data in members.items():
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            pack.addfile(entry, io.BytesIO(data))
    return gzip.compress(out.getvalue(), compresslevel=6, mtime=0)


def files(family, rev, native):
    changed = blob(100 + rev, 65536)
    stable = blob(42, 4 * MIB)
    if family == "boilerplate":
        return {f"{i:03}.txt": ((f"{i},{rev if i % 16 == 0 else 0},"
                                  "the service accepted the request\n") * 128).encode()
                for i in range(128)}
    if family == "json":
        return {f"page-{i}.json": json.dumps(
            [{"id": j, "branch": i, "total": rev if j % 127 == 0 else j}
             for j in range(4096)], sort_keys=True).encode() for i in range(8)}
    if family == "gzip-control":
        return {"asset.gz": gzip.compress(blob(230 + rev, 2 * MIB), mtime=0)}
    if family == "source":
        return {f"lib/{i:03}.go": (f"package lib\nconst Revision{i} = "
                                  f"{rev if i % 8 == 0 else 0}\n" +
                                  f"// Function for component {i}\n" * 40).encode()
                for i in range(128)}
    if family == "native":
        return {"fixture.elf": native + b"C1TRAILER" + changed}
    if family in ("package", "oci"):
        members = {f"payload/{i:02}.bin": blob(501 + i, 128 * 1024)
                   for i in range(12)}
        members["payload/changed.bin"] = changed
        return ({"package.zip": archive(members, "zip")} if family == "package"
                else {"layer.tar.gz": archive(members, "tar")})
    if family == "sqlite":
        db = sqlite3.connect(":memory:")
        try:
            db.execute("CREATE TABLE rows (id INTEGER PRIMARY KEY, value BLOB)")
            db.executemany("INSERT INTO rows VALUES (?, ?)",
                           [(i, blob(600 + i + (rev if i % 127 == 0 else 0), 1024))
                            for i in range(2048)])
            return {"snapshot.sqlite": db.serialize()}
        finally:
            db.close()
    if family == "tensor":
        header = json.dumps({"weights": {"dtype": "F32", "shape": [MIB],
                                         "data_offsets": [0, 4 * MIB]}}).encode()
        header += b" " * (-len(header) % 8)
        tensor = bytearray(struct.pack("<4096f", *(i / 4096 for i in range(4096))) * 256)
        tensor[rev * 65536:rev * 65536 + 65536] = changed
        return {"weights.safetensors": struct.pack("<Q", len(header)) + header + tensor}
    if family == "shift":
        return {"payload.bin": stable[:MIB // 2] + changed[:rev * 8192] + stable[MIB // 2:]}
    if family == "repeat":
        return {"payload.bin": stable, "copy.bin": stable}
    if family == "entropy":
        return {"payload.bin": blob(730 + rev, 4 * MIB)}
    raise ValueError(family)


def generate(root, native_path):
    existing = root.exists()
    root.mkdir(parents=True, exist_ok=True)
    native = native_path.read_bytes()
    manifest = {"generator": 1, "native_sha256": hashlib.sha256(native).hexdigest(), "files": []}
    for family in FAMILIES:
        for rev in range(3):
            for name, data in sorted(files(family, rev, native).items()):
                rel = Path(family, str(rev), name)
                path = root / rel
                path.parent.mkdir(parents=True, exist_ok=True)
                if existing and path.exists() and path.read_bytes() != data:
                    raise ValueError(f"existing fixture differs: {rel}")
                path.write_bytes(data)
                manifest["files"].append({
                    "path": rel.as_posix(), "bytes": len(data),
                    "sha256": hashlib.sha256(data).hexdigest(),
                })
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--native", type=Path, required=True)
    args = parser.parse_args()
    args.manifest.write_text(json.dumps(generate(args.root, args.native),
                                       separators=(",", ":")) + "\n")
