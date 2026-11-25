"""Prepare bounded neutral histories with immutable identities."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import random
import shutil


def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def stage(root, family, version, name, source, decompress=False):
    path = root / family / str(version) / name
    path.parent.mkdir(parents=True, exist_ok=True)
    if decompress:
        with gzip.open(source, "rb") as f, path.open("wb") as out:
            shutil.copyfileobj(f, out, length=1 << 20)
    else:
        shutil.copyfile(source, path)


def vm(root, version):
    path = root / "volume" / str(version) / "volume.raw"
    path.parent.mkdir(parents=True)
    rng = random.Random(1212)
    with path.open("wb") as f:
        f.truncate(8 << 20)
        for page in range(1984):
            if rng.randrange(5) > 1:
                continue
            data = bytearray(rng.randbytes(4096))
            if version and page % 7 == 0:
                for pos in range(11, len(data), 79):
                    data[pos] ^= version
            if version == 2 and page >= 1024:
                page += 12
            f.seek(page * 4096)
            f.write(data)


def events(root, version):
    path = root / "events" / str(version) / "events.jsonl"
    path.parent.mkdir(parents=True)
    groups = list(range(48 + version))
    if version == 2:
        groups[8:20] = reversed(groups[8:20])
    with path.open("w") as f:
        for batch in groups:
            for i in range(256):
                row = {"sequence": batch * 256 + i, "region": f"zone-{i%6}",
                       "status": 429 if i % 23 == version else 200,
                       "sensor": 302 + i % 89, "event": f"event-{batch%17}"}
                f.write(json.dumps(row, separators=(",", ":")) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("root", "c1", "c5", "manifest"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    root = args.root
    root.mkdir(parents=True)
    for family, name in {"sqlite": "snapshot.sqlite", "native": "fixture.elf",
                         "tensor": "weights.safetensors", "shift": "payload.bin",
                         "entropy": "payload.bin"}.items():
        for version in range(3):
            stage(root, family, version, name, args.c1 / family / str(version) / name)
    for version in range(3):
        package = args.c5 / "typescript" / str(version) / "package.tgz"
        stage(root, "typescript-gzip", version, "package.tgz", package)
        stage(root, "typescript-tar", version, "package.tar", package, decompress=True)
        vm(root, version)
        events(root, version)
    for version in range(2):
        source = args.c5 / "httpx" / str(version) / "source.tar.gz"
        stage(root, "httpx-heldout", version, "source.tar", source, decompress=True)
    result = [{"path": p.relative_to(root).as_posix(), "bytes": p.stat().st_size,
               "sha256": digest(p)} for p in sorted(root.rglob("*")) if p.is_file()]
    if args.manifest.exists():
        if json.loads(args.manifest.read_text()) != result:
            raise ValueError("C12 staged fixtures differ from the frozen manifest")
    else:
        args.manifest.write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    main()
