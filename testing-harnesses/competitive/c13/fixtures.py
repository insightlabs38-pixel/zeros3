"""Materialize small object cohorts from versioned public and synthetic inputs."""
import argparse
import hashlib
import json
from pathlib import Path
import random
import tarfile

TRAIN, EVAL = 48, 80


def parts(data, span):
    return [data[i:i+span] for i in range(0, len(data), span) if len(data[i:i+span]) >= span//4]


def source(archive, count, seed):
    blocks = []
    with tarfile.open(archive) as tar:
        for member in tar:
            name = member.name
            if (member.isfile() and name.endswith((".ts", ".js", ".py", ".md", ".json"))
                    and 512 <= member.size <= 4 << 20):
                with tar.extractfile(member) as f:
                    blocks.extend(parts(f.read(), 2048))
    return random.Random(seed).sample(blocks, count)


def event_files(path, count):
    lines = path.read_bytes().splitlines(keepends=True)
    return [b"".join(lines[i*24:(i+1)*24]) for i in range(count)]


def pages(path, count, seed):
    base = path.read_bytes()
    numbers = random.Random(seed).sample(range(len(base) // 4096), count)
    return [base[i*4096:(i+1)*4096] for i in numbers]


def configs(count, epoch):
    return [json.dumps({"node": i, "region": f"region-{i%8}", "epoch": epoch,
                        "storage": {"tier": "warm", "enabled": bool(i%3), "retries": 3},
                        "routes": [f"/api/{i//8}/item/{r}" for r in range(11)],
                        "annotations": {f"input-{n}": f"worker:{n%8}" for n in range(17)}},
                       indent=2, sort_keys=True).encode() for i in range(count)]


def model_metadata(count, epoch):
    return [json.dumps({"shard": i, "format": "safetensors", "epoch": epoch,
                        "layers": [{"name": f"layer.{i%12}.{n}.weight", "dtype": "BF16",
                                    "shape": [256, 768], "start": n*393216,
                                    "end": (n+1)*393216} for n in range(18)]},
                       separators=(",", ":")).encode() for i in range(count)]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("root", "c12", "manifest"):
        p.add_argument("--" + n, type=Path, required=True)
    args = p.parse_args()
    args.root.mkdir(parents=True)
    manifest = []
    for epoch, split, count in ((0, "train", TRAIN), (1, "eval", EVAL)):
        fixture = args.c12
        groups = {
            "events": event_files(fixture / "events" / str(epoch) / "events.jsonl", count),
            "config": configs(count, epoch),
            "release": source(fixture / "typescript-tar" / str(epoch) / "package.tar", count, 1302),
            "sqlite": pages(fixture / "sqlite" / str(epoch) / "snapshot.sqlite", count, 1305),
            "volume": pages(fixture / "volume" / str(epoch) / "volume.raw", count, 1307),
            "model": model_metadata(count, epoch),
            "entropy": [random.Random(13_000+epoch*100+i).randbytes(3072) for i in range(count)],
        }
        held = source(fixture / "httpx-heldout" / str(epoch) / "source.tar", count, 1311)
        for scope, cohorts in (("main", groups), ("heldout", {"httpx": held})):
            for family, values in sorted(cohorts.items()):
                for i, data in enumerate(values):
                    key = f"{family}/{split}/{i:04}.bin"
                    path = args.root / scope / key
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(data)
                    manifest.append({"scope": scope, "key": key, "bytes": len(data),
                                     "sha256": hashlib.sha256(data).hexdigest()})
    frozen = "\n".join(json.dumps(r, sort_keys=True) for r in manifest)+"\n"
    if args.manifest.exists() and args.manifest.read_text() != frozen:
        raise ValueError("C13 input identity changed")
    args.manifest.write_text(frozen)


if __name__ == "__main__":
    main()
