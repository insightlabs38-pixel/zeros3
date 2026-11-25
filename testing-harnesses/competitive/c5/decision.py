"""Reduce the independent comparison and price explicit sensitivity scenarios."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
from statistics import median


def rows(path):
    with path.open() as trace:
        return list(map(json.loads, trace))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", type=Path)
    args = parser.parse_args()
    root = args.results
    storage = defaultdict(dict)
    for engine in ("c2", "current"):
        stages = defaultdict(dict)
        for row in rows(root / f"{engine}.jsonl"):
            stages[row["family"], row["lane"]][row["stage"]] = row
        for (family, lane), per_stage in stages.items():
            variant = per_stage["final-gc"]
            storage[family][f"{engine}-{lane}"] = {
                "allocated": variant["allocated"], "operations_verified": per_stage["restore"]["objects_verified"],
                "ingest_cpu_seconds": per_stage["ingest"].get("ingest_server_cpu_seconds"),
                "maintenance_cpu_seconds": sum(per_stage[s]["cpu_seconds"] for s in
                    ("apply", "gc", "repack", "compact", "final-gc") if s in per_stage),
                "reads": per_stage["reads"]["latency"],
            }
    for row in rows(root / "peers.jsonl"):
        variant = storage[row["family"]].setdefault(row["system"], {})
        if row["stage"] == "restore":
            variant.update(allocated=row["allocated"], operations_verified=row["verified"], reads=row["latency"])
        else:
            variant["ingest_cpu_seconds"] = row["server_cpu_seconds"]
    request = defaultdict(lambda: defaultdict(list))
    for row in rows(root / "requests.jsonl"):
        if row.get("phase") == "requests" and row.get("status") == "verified" and row.get("concurrency") == 4:
            request[row["system"]][row["operation"]].append(row)
    metrics = {system: {op: {
        "operations_per_second": median(x["operations"] / x["seconds"] for x in values),
        "median_p99_ms": median(x["p99_ms"] for x in values) if values[0]["p99_ms"] else None,
        "median_cpu_seconds": median(x["cpu_seconds"] for x in values),
    } for op, values in by_op.items()} for system, by_op in request.items()}
    sensitivity = {}
    for family, values in storage.items():
        plain, archive = values["current-cdc"], values["current-c1"]
        saving = plain["allocated"] - archive["allocated"]
        cpu = archive["maintenance_cpu_seconds"] - plain["maintenance_cpu_seconds"]
        assumptions = ((0.005, 0.02), (0.025, 0.05), (0.10, 0.15))
        sensitivity[family] = {
            "bytes_saved": saving, "extra_maintenance_cpu_seconds": cpu,
            "cost_cases": [{"storage_usd_per_gib_month": disk, "cpu_usd_per_core_hour": compute,
                            "payback_months": max(0, cpu) / 3600 * compute / (saving / (1 << 30) * disk)
                            if saving > 0 else None} for disk, compute in assumptions],
        }
    print(json.dumps({"storage": storage, "requests_4_clients": metrics, "sensitivity": sensitivity}, indent=2))


if __name__ == "__main__":
    main()
