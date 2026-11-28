"""Descriptive tail intervals over the recorded completion order."""
import json
import math
from pathlib import Path
import random
import sys


def nearest(values, quantile):
    return values[math.ceil(len(values) * quantile) - 1]


for name in sys.argv[1:]:
    for line in Path(name).read_text().splitlines():
        row = json.loads(line)
        for kind, values in row["latency_ns"].items():
            if len(values) < 300:
                continue
            size = min(len(values), max(8, row["clients"]))
            rng = random.Random(44191)
            q95, q99 = [], []
            for _ in range(200):
                trial = []
                while len(trial) < len(values):
                    start = rng.randrange(len(values))
                    trial.extend(values[(start + i) % len(values)] for i in range(size))
                order = sorted(trial[:len(values)])
                q95.append(nearest(order, .95))
                q99.append(nearest(order, .99))
            q95.sort()
            q99.sort()
            print(json.dumps({
                "experiment": row["experiment"],
                "kind": kind,
                "observations": len(values),
                "block": size,
                "p95_descriptive_interval_ns": [nearest(q95, .025), nearest(q95, .975)],
                "p99_descriptive_interval_ns": [nearest(q99, .025), nearest(q99, .975)],
            }, sort_keys=True))
