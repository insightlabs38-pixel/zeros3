"""Compute per-trial latency summaries and descriptive block-bootstrap intervals."""
import argparse
import json
import math
from pathlib import Path
import random


def quantile(samples, p):
    ordered = sorted(samples)
    return ordered[max(0, math.ceil(p * len(ordered)) - 1)]


def block_bootstrap(samples, p, seed=463901):
    rng = random.Random(seed)
    size = min(8, len(samples))
    estimates = []
    for _ in range(400):
        replay = []
        while len(replay) < len(samples):
            start = rng.randrange(len(samples))
            replay.extend(samples[(start + j) % len(samples)] for j in range(size))
        estimates.append(quantile(replay[:len(samples)], p))
    return [quantile(estimates, 0.025), quantile(estimates, 0.975)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("rows", nargs="+", type=Path)
    parser.add_argument("--case", default="", help="Restrict output to matching case names")
    args = parser.parse_args()
    for path in args.rows:
        for line in path.read_text().splitlines():
            row = json.loads(line)
            if "latency_ns" not in row:
                continue
            case = row.get("case", row.get("profile", ""))
            if isinstance(case, list):
                case = "/".join(case)
            if args.case not in case:
                continue
            for name, samples in sorted(row["latency_ns"].items()):
                n = len(samples)
                receipt = {
                    "case": case,
                    "lane": row.get("lane"),
                    "clients": row.get("clients"),
                    "operation": name,
                    "round": row.get("round"),
                    "cold": row.get("cold", False),
                    "samples": n,
                    "p50_ns": quantile(samples, 0.50),
                    "p95_ns": quantile(samples, 0.95),
                    "p99_ns": quantile(samples, 0.99),
                    "iid_chance_of_sampling_above_p99": 1 - 0.99 ** n,
                }
                if n >= 32:
                    receipt["descriptive_p95_block_ci_ns"] = block_bootstrap(samples, 0.95)
                if n >= 300:
                    receipt["descriptive_p99_block_ci_ns"] = block_bootstrap(samples, 0.99)
                print(json.dumps(receipt, sort_keys=True))


if __name__ == "__main__":
    main()
