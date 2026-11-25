"""Verify and summarize content overlap and zlib compressibility by revision."""
import argparse
from collections import defaultdict
import hashlib
import json
from pathlib import Path
import zlib

BLOCK = 65536


def summarize(root, manifests):
    revisions = defaultdict(list)
    for manifest in manifests:
        records = json.loads(manifest.read_text())
        files = records["files"] if isinstance(records, dict) else records
        for row in files:
            relative = Path(row["path"])
            family, revision = relative.parts[:2]
            content = (root / relative).read_bytes()
            if len(content) != row["bytes"] or hashlib.sha256(content).hexdigest() != row["sha256"]:
                raise ValueError(f"manifest verification failed: {relative}")
            revisions[family, int(revision)].append((row["sha256"], content))
    result = []
    previous = {}
    for (family, revision), objects in sorted(revisions.items()):
        unique = {digest for digest, _ in objects}
        blocks = {
            hashlib.sha256(data[i:i + BLOCK]).digest()
            for _, data in objects for i in range(0, len(data), BLOCK)
        }
        prior = previous.get(family)
        row = {"family": family, "revision": revision, "files": len(objects),
               "logical_bytes": sum(len(data) for _, data in objects),
               "zlib_level_6_bytes": sum(len(zlib.compress(data, 6)) for _, data in objects),
               "unique_file_hashes": len(unique)}
        if prior is not None:
            earlier_hashes, earlier_blocks = prior
            row["unchanged_or_reused_file_bytes"] = sum(len(data) for digest, data in objects
                                                        if digest in earlier_hashes)
            row["aligned_64k_block_jaccard"] = len(earlier_blocks & blocks) / len(earlier_blocks | blocks)
        previous[family] = unique, blocks
        result.append(row)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("manifests", nargs="+", type=Path)
    args = parser.parse_args()
    print(json.dumps(summarize(args.root, args.manifests), indent=2))
