"""Compare paired exact-read rows with per-operation CPU and read accounting."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
from statistics import median


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", nargs="+", type=Path)
    parser.add_argument("--families", nargs="+")
    parser.add_argument("--lanes", nargs="+")
    parser.add_argument("--sizes", nargs="+", type=int)
    args = parser.parse_args()

    groups = defaultdict(dict)
    for path in args.results:
        for line in path.read_text().splitlines():
            row = json.loads(line)
            if row["stage"] != "read":
                continue
            family, lane, size = (row[k] for k in ("family", "lane", "range_bytes"))
            if ((args.families and family not in args.families)
                    or (args.lanes and lane not in args.lanes)
                    or (args.sizes and size not in args.sizes)):
                continue
            key = (family, lane, size, row["width"], row["mode"])
            groups[key][path, row["pair"], row["build"]] = row

    print("family lane range_B clients phase pairs allocated_B "
          "c9_p50_ms c10_p50_ms c9_p95_ms c10_p95_ms "
          "c9_cpu_ms/op c10_cpu_ms/op c9_read_B/op c10_read_B/op")
    for key, rows in sorted(groups.items()):
        pairs = [(base, rows[path, idx, "c10"])
                 for (path, idx, build), base in rows.items()
                 if build == "c9" and (path, idx, "c10") in rows]
        if not pairs:
            continue
        for base, trial in pairs:
            if any(base[f] != trial[f] for f in ("allocated", "logical_bytes", "operations")):
                raise ValueError(f"incomparable pair {key}")
        print(*key, len(pairs), pairs[0][0]["allocated"],
              *(f"{median(fn(pair[i]) for pair in pairs):.2f}"
                for fn in (lambda r: r["p50_ms"], lambda r: r["p95_ms"],
                           lambda r: 1000 * r["server_cpu_seconds"] / r["operations"],
                           lambda r: r["process_read_bytes"] / r["operations"])
                for i in (0, 1)))


if __name__ == "__main__":
    main()
