"""Freeze a larger independent public source fragment panel."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile


def segments(tar, suffixes):
    for member in tar:
        if not member.isfile() or not member.name.endswith(suffixes):
            continue
        with tar.extractfile(member) as f:
            while data := f.read(3072):
                if len(data) >= 768:
                    yield member.name, data


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("typescript", "rubocop", "corpus", "manifest"):
        p.add_argument("--"+name, type=Path, required=True)
    args = p.parse_args()
    root = args.corpus / "wide"
    root.mkdir(parents=True)
    rows, seen = [], set()

    def stage(cohort, values, cap):
        count = 0
        for source, data in values:
            digest = hashlib.sha256(data).hexdigest()
            if digest in seen:
                continue
            seen.add(digest)
            split = "train" if count % 8 == 0 else "eval"
            key = f"{cohort}/{split}/{count:05}.bin"
            path = root/key
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
            rows.append({"scope": "wide", "key": key, "bytes": len(data),
                         "sha256": digest, "source_member": source})
            count += 1
            if count == cap:
                break

    with tarfile.open(args.typescript) as archive:
        stage("typescript", segments(archive, (".ts", ".js")), 3750)
    with tarfile.open(args.rubocop) as gem:
        member = gem.getmember("data.tar.gz")
        if member.size > 8 << 20:
            raise ValueError("RubyGems source exceeded input limit")
        with gem.extractfile(member) as data:
            outer = io.BytesIO(data.read())
        with tarfile.open(fileobj=outer, mode="r:gz") as archive:
            stage("rubocop", segments(archive, (".rb", ".md", ".yml")), 1750)
    if sum(row["bytes"] for row in rows) < 10 << 20:
        raise ValueError("wide panel lacks enough independent fragments")
    frozen = "\n".join(json.dumps(r, sort_keys=True) for r in rows)+"\n"
    if args.manifest.exists() and args.manifest.read_text() != frozen:
        raise ValueError("wide input changed")
    args.manifest.write_text(frozen)


if __name__ == "__main__":
    main()
