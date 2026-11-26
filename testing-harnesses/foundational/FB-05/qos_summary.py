"""Compute pooled and median-pair p99 over matching pass windows."""
from collections import defaultdict
import gzip
import json
from pathlib import Path
import statistics
import sys


def p99(values):
    return sorted(values)[int(.99 * len(values))]


def summarize(path):
    pairs = defaultdict(dict)
    opener = gzip.open if path.suffix == ".gz" else open
    with opener(path, "rt") as f:
        for line in f:
            row = json.loads(line)
            pairs[row["pair"]][row["mode"]] = row
    modes = sorted({mode for pair in pairs.values() for mode in pair} - {"idle"})
    for mode in modes:
        for method in ("GET", "HEAD"):
            pooled = defaultdict(list)
            per_pair = []
            elapsed, pace = [], []
            truncated = 0
            for pair in pairs.values():
                if "idle" not in pair or mode not in pair:
                    continue
                lo, hi = pair[mode]["pass_window_s"]
                truncated += hi > len(pair[mode]["reads"][method]) * .01
                successes = {}
                for k in ("idle", mode):
                    values = [r[1] for r in pair[k]["reads"][method] if lo <= r[0] <= hi]
                    pooled[k].extend(values)
                    successes[k] = p99(values)
                per_pair.append(successes)
                elapsed.append((hi - lo) * 1000)
                pace.append(pair[mode]["pass"].get("pace_us", 0) / 1000)
            yield {"run": path.name, "mode": mode, "method": method, "pairs": len(per_pair),
                   "truncated_pass_windows": truncated,
                   "median_pass_ms": round(statistics.median(elapsed), 3),
                   "median_pace_ms": round(statistics.median(pace), 3),
                   "samples": {k: len(v) for k, v in pooled.items()},
                   "pooled_p99_ms": {k: round(p99(v), 3) for k, v in pooled.items()},
                   "median_pair_p99_ms": {
                       k: round(statistics.median(r[k] for r in per_pair), 3) for k in pooled}}


if __name__ == "__main__":
    for filename in sys.argv[1:]:
        for row in summarize(Path(filename)):
            print(json.dumps(row))
