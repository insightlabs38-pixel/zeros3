"""Combine paired default-S3 timing by width and operation."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
import random
from statistics import median


def metrics(rows):
    fields = ("ops_per_second", "p95_ms", "p99_ms", "server_cpu_seconds",
              "client_cpu_seconds", "disk_read_bytes", "disk_write_bytes", "server_peak_rss_bytes")
    return {field: median(row[field] for row in rows) for field in fields}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", type=Path, nargs="+")
    args = parser.parse_args()
    groups = defaultdict(lambda: defaultdict(dict))
    for path in args.paths:
        for row in map(json.loads, path.read_text().splitlines()):
            groups[row["width"], row["operation"]][row["pair"]][row["build"]] = row
    for (width, operation), pairs in sorted(groups.items()):
        ordered = list(pairs.values())
        ratios = [p["c2"]["ops_per_second"] / p["b10"]["ops_per_second"] - 1 for p in ordered]
        rng = random.Random(947)
        sample = sorted(median(rng.choices(ratios, k=len(ratios))) for _ in range(10000))
        print(json.dumps({"width": width, "operation": operation, "paired_change": median(ratios),
                          "paired_changes": ratios, "bootstrap_95": [sample[249], sample[9749]],
                          "b10": metrics([p["b10"] for p in ordered]), "c2": metrics([p["c2"] for p in ordered])}))


if __name__ == "__main__":
    main()
