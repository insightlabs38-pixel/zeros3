"""Obtain and verify the exact registered ZeroS3 DF0 release bytes."""
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess


def record(stream):
    digest = hashlib.sha256()
    size = 0
    for data in iter(lambda: stream.read(1 << 20), b""):
        size += len(data)
        digest.update(data)
    return size, digest.hexdigest()


def main():
    root = Path(os.environ["ZEROS3_DF0_FIXTURES"])
    root.mkdir(parents=True, exist_ok=True)
    corpus = Path(__file__).with_name("corpus.json")
    for family in json.loads(corpus.read_text()):
        for pin in family["files"]:
            path = root / pin["file"]
            candidate = path
            if not path.exists():
                candidate = path.with_suffix(path.suffix + ".download")
                subprocess.run(
                    ["curl", "--fail", "--silent", "--show-error", "--location",
                     "--proto", "=https", "--max-time", "90", "--output", candidate,
                     pin["url"]],
                    check=True,
                )
            with candidate.open("rb") as inp:
                actual = record(inp)
            if actual != (pin["bytes"], pin["sha256"]):
                raise ValueError(f"compressed bytes differ for {candidate}")
            with gzip.open(candidate, "rb") as inp:
                actual = record(inp)
            if actual != (pin["tar_bytes"], pin["tar_sha256"]):
                raise ValueError(f"inflated bytes differ for {candidate}")
            if candidate != path:
                candidate.replace(path)
            print(f"{family['split']} {family['name']} {path.name} verified")


if __name__ == "__main__":
    main()
