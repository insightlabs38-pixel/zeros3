#!/usr/bin/env python3
"""Summarize B1 range-bench rows: tables.py measurements.jsonl [label ...]
Prints, per label/layout/depth/mode: mean and median logical CAS bytes, mean reconstructed-frame and
program bytes, median wall/CPU/alloc of the instrumented read and median signed-GET-path latency."""
import json, sys, statistics as st, collections
rows = collections.defaultdict(list)
labels = sys.argv[2:]
for line in open(sys.argv[1]):
    r = json.loads(line)
    if labels and r["label"] not in labels:
        continue
    x = r["row"]
    fam = "coreutils" if x["Family"] == "coreutils" else "go-tools"
    rows[(r["label"], fam, x["Layout"], x["Depth"], x["Mode"])].append(x)
print("| label | family | layout | depth | mode | n | CAS mean | CAS median | frames decoded | programs | wall med ms | CPU med ms | alloc med | GET med ms |")
print("|---|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
for k, v in rows.items():
    m = lambda f: st.median([x[f] for x in v])
    a = lambda f: sum(x[f] for x in v) // len(v)
    print("| %s | %s | %s | %d | %s | %d | %d | %d | %d | %d | %.1f | %.1f | %d | %.1f |" % (*k, len(v), a("Fetched"), m("Fetched"),
          a("WindowBytes"), a("ProgBytes"), m("WallUS") / 1e3, m("CPUUS") / 1e3, m("AllocBytes"), m("GetUS") / 1e3))
