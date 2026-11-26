"""Verify replayable old-reader refusal against a repaired preset store."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess


def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def tree(root):
    return {p.relative_to(root).as_posix(): digest(p)
            for p in sorted(root.rglob("*")) if p.is_file()}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("frozen", "old", "fixture", "copy", "results"):
        p.add_argument("--"+name, type=Path, required=True)
    args = p.parse_args()
    shutil.copytree(args.fixture, args.copy)
    env = {**os.environ, "GOMAXPROCS": "2"}
    before = tree(args.copy)
    events = []
    for name, binary, want in (("c9-refusal", args.old, False), ("c19-verifies", args.frozen, True)):
        proc = subprocess.run([binary, "verify", "-store", args.copy, "-deep", "-json"],
                              env=env, capture_output=True, text=True, timeout=30)
        same = before == tree(args.copy)
        if (proc.returncode == 0) != want or not same:
            raise ValueError(f"unexpected behavior in {name}: {proc.stderr}")
        if not want and "packlocaldict" not in proc.stderr:
            raise ValueError("old engine did not identify unsupported dictionary storage")
        events.append({"phase": name, "status": proc.returncode, "untouched": same,
                       "binary_sha256": digest(binary), "store_files": len(before),
                       "format_sha256": before["FORMAT.json"], "stderr": proc.stderr,
                       "report": json.loads(proc.stdout) if want else None})
    args.results.write_text("".join(json.dumps(x)+"\n" for x in events))


if __name__ == "__main__":
    main()
