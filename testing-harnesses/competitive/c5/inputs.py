"""Stage bounded bridge fixtures and freeze independent public inputs."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
from urllib.request import urlopen

BRIDGE = ("coreutils", "parquet", "sst", "entropy")
MODELS = ("mobilenet_v2-b0353104.pth", "mobilenet_v2-7ebf99e0.pth")
HTTPX = ("0.28.0", "0.28.1")


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def fetch(url, target, expected, limit):
    target.parent.mkdir(parents=True, exist_ok=True)
    if not target.exists():
        pending = target.with_suffix(".part")
        try:
            with urlopen(url, timeout=30) as response, pending.open("wb") as out:
                size = 0
                while block := response.read(1 << 20):
                    size += len(block)
                    if size > limit:
                        raise ValueError(f"input exceeds its byte budget: {target.name}")
                    out.write(block)
            if not digest(pending).startswith(expected):
                raise ValueError(f"producer checksum differs: {target.name}")
            pending.rename(target)
        finally:
            pending.unlink(missing_ok=True)
    if target.stat().st_size > limit or not digest(target).startswith(expected):
        raise ValueError(f"staged input differs: {target.name}")
    return url


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("bridge", type=Path)
    parser.add_argument("releases", type=Path)
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    args.root.mkdir(parents=True, exist_ok=True)
    urls = {}
    for family in BRIDGE:
        shutil.copytree(args.bridge / family, args.root / family, dirs_exist_ok=True)
    for i, ver in enumerate(("5.6.2", "5.6.3", "5.7.2")):
        name = f"typescript-{ver}.tgz"
        target = args.root / "typescript" / str(i) / "package.tgz"
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(args.releases / name, target)
        urls[target] = f"https://registry.npmjs.org/typescript/-/{name}"
    for i, name in enumerate(MODELS):
        target = args.root / "mobilenet" / str(i) / "weights.pth"
        urls[target] = fetch(f"https://download.pytorch.org/models/{name}", target,
                             name.split("-")[1].split(".")[0], 32 << 20)
    for i, version in enumerate(HTTPX):
        with urlopen(f"https://pypi.org/pypi/httpx/{version}/json", timeout=20) as response:
            release = json.load(response)
        archive = next(f for f in release["urls"] if f["packagetype"] == "sdist")
        target = args.root / "httpx" / str(i) / "source.tar.gz"
        urls[target] = fetch(archive["url"], target, archive["digests"]["sha256"], 1 << 20)
    rows = [{"path": p.relative_to(args.root).as_posix(), "sha256": digest(p),
             "bytes": p.stat().st_size, "url": urls.get(p)}
            for p in sorted(args.root.rglob("*")) if p.is_file()]
    if args.manifest.exists():
        frozen = json.loads(args.manifest.read_text())
        if rows != frozen:
            raise ValueError("inputs differ from the frozen manifest")
    else:
        args.manifest.write_text(json.dumps(rows, indent=2) + "\n")


if __name__ == "__main__":
    main()
