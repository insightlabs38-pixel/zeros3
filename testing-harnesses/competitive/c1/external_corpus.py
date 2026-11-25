"""Fetch compact, public release histories for the C1 extension."""
import argparse
import hashlib
import json
from pathlib import Path
from urllib.request import urlopen

SOURCES = ("cat.c", "cp.c", "copy.c", "ls.c", "sort.c", "wc.c")
FAMILIES = ("coreutils", "lodash")


def inputs():
    for rev, tag in enumerate(("v9.3", "v9.4", "v9.5")):
        for filename in SOURCES:
            yield f"coreutils/{rev}/src/{filename}", (
                f"https://raw.githubusercontent.com/coreutils/coreutils/{tag}/src/{filename}")
    for rev, version in enumerate(("4.17.19", "4.17.20", "4.17.21")):
        yield f"lodash/{rev}/lodash.tgz", f"https://registry.npmjs.org/lodash/-/lodash-{version}.tgz"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("manifest", type=Path)
    args = parser.parse_args()
    recorded = {}
    if args.manifest.exists():
        recorded = {row["path"]: row for row in json.loads(args.manifest.read_text())}
    rows = []
    for relative, url in inputs():
        path = args.root / relative
        if path.exists():
            content = path.read_bytes()
        else:
            with urlopen(url, timeout=20) as response:
                content = response.read(2 * 1024 * 1024 + 1)
        if len(content) > 2 * 1024 * 1024:
            raise ValueError(f"unexpected size: {relative}")
        row = {"path": relative, "url": url, "bytes": len(content),
               "sha256": hashlib.sha256(content).hexdigest()}
        if recorded and row != recorded.get(relative):
            raise ValueError(f"manifest mismatch: {relative}")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        rows.append(row)
    args.manifest.write_text(json.dumps(rows, indent=2) + "\n")


if __name__ == "__main__":
    main()
