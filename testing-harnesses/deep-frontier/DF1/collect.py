"""Collect completed DF1 native trials and their exact source identities."""
import hashlib
import json
from pathlib import Path
import sys


def collect(root):
    digest = {
        arm: hashlib.sha256((root / f"{arm}.go").read_bytes()).hexdigest()
        for arm in ("parent", "candidate")
    }
    pairs = {}
    for path in sorted(root.glob("*-*-*-*.txt")):
        family, clients, trial, arm = path.stem.rsplit("-", 3)
        lines = path.read_text().splitlines()
        assert "PASS" in lines and not any("--- SKIP" in line or "--- FAIL" in line for line in lines), path
        prepared = [json.loads(line.removeprefix("DF0PREP ")) for line in lines if line.startswith("DF0PREP ")]
        measurements = [json.loads(line.removeprefix("DF1SIGNED ")) for line in lines if line.startswith("DF1SIGNED ")]
        assert len(prepared) == len(measurements) == 1, path
        store, row = prepared[0], measurements[0]
        assert store["status"] == "measured", path
        assert row["family"] == family and row["clients"] == int(clients) and row["round"] == int(trial), path
        row.update({
            "arm": arm,
            "source_sha256": digest[arm],
            "allocated_bytes": store["allocated_bytes"],
            "pack_allocated_bytes": store["pack_allocated_bytes"],
            "prepare_cpu_ns": store["prepare_cpu_ns"],
        })
        pairs.setdefault((family, clients, trial), {})[arm] = row
    assert pairs
    for arms in pairs.values():
        assert set(arms) == {"parent", "candidate"}
        assert arms["parent"]["pack_allocated_bytes"] == arms["candidate"]["pack_allocated_bytes"]
        assert arms["parent"]["allocated_bytes"] == arms["candidate"]["allocated_bytes"]
        for row in arms.values():
            print(json.dumps(row, sort_keys=True))


if __name__ == "__main__":
    collect(Path(sys.argv[1]))
