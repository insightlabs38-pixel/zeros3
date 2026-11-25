"""Recompute matched transfer summaries from raw HTTP counts."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
from statistics import median


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", type=Path, nargs="+")
    args = parser.parse_args()
    for path in args.paths:
        groups = defaultdict(list)
        for line in path.read_text().splitlines():
            row = json.loads(line)
            groups[row["family"], row["state"], row["lane"], row.get("rate_bytes", 0)].append(row)
        for (family, state, lane, rate), rows in sorted(groups.items()):
            def wire(row, role):
                metric = row[f"{role}_http_bytes"]
                return metric["client_to_server"] + metric["server_to_client"]
            print(json.dumps({
                "trace": path.name, "family": family, "knowledge": state, "lane": lane,
                "rate_bytes": rate, "trials": len(rows),
                "http_bytes": median(wire(r, "source") + wire(r, "destination") for r in rows),
                "receiver_http_bytes": median(wire(r, "destination") for r in rows),
                "seconds": median(r["seconds"] for r in rows),
                "server_cpu_seconds": median(r["sender"]["cpu_seconds"] + r["receiver"]["cpu_seconds"] for r in rows),
                "allocated_before_finalization": median(r["allocated_before_finalization"] for r in rows),
            }))


if __name__ == "__main__":
    main()
