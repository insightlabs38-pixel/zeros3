"""Reduce final comparable request and transfer evidence."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
from statistics import median


def rows(path):
    return list(map(json.loads, path.read_text().splitlines()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", type=Path)
    args = parser.parse_args()
    path = args.results
    data = {}
    groups = defaultdict(list)
    for file in sorted(path.glob("requests*.jsonl")):
        trace = rows(file)
        invalid = {(x["system"], x["repetition"]) for x in trace
                   if x["phase"] == "error" or x.get("issues") or x.get("status") == "invalid"}
        for row in trace:
            if (row["system"], row["repetition"]) in invalid:
                continue
            if row.get("concurrency") == 4 and row["phase"] == "requests":
                groups[row["system"], row["operation"]].append(row)
    data["requests_4_clients"] = {
        f"{system}/{op}": {"trials": len(samples),
                           "ops_per_second": median(x["operations"] / x["seconds"] for x in samples),
                           "cpu_seconds": median(x["cpu_seconds"] for x in samples),
                           "p99_ms": median(x["p99_ms"] for x in samples) if samples[0]["p99_ms"] else None}
        for (system, op), samples in sorted(groups.items())
    }
    lanes = defaultdict(list)
    for r in rows(path / "transfer.jsonl"):
        if r.get("phase") == "transfer":
            lanes[r["lane"]].append(r)
    data["transfer"] = {name: {
        "trials": len(rs),
        "seconds": median(r["seconds"] for r in rs),
        "server_cpu_seconds": median(r["sender"]["cpu_seconds"] + r["receiver"]["cpu_seconds"] for r in rs),
        "http_stream_bytes": median(sum(r[key][direction]
            for key in ("source_http_bytes", "destination_http_bytes")
            for direction in ("client_to_server", "server_to_client")) for r in rs),
    } for name, rs in lanes.items()}
    data["workflows"] = {f'{r["workflow"]}/{r["phase"]}': r for r in rows(path / "workflows.jsonl")
                         if r["phase"] in ("full_archive", "repeat_repack", "bundle_restore", "deep_verify")}
    print(json.dumps(data, indent=2))


if __name__ == "__main__":
    main()
