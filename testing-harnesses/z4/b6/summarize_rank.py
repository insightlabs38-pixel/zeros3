#!/usr/bin/env python3
"""Tabulate z4b6rank JSON (cumulative retained cost) per family/view/chunker. usage: summarize_rank.py rank.json..."""
import json, sys
for p in sys.argv[1:]:
    rows = json.load(open(p))
    print("##", p)
    print("| family | view | whole | cdc16/64/256 | cdc1/4/16 | cdc.25/1/4 | raw bytes (all versions) |")
    print("|---|---|---:|---:|---:|---:|---:|")
    t = {}
    for r in rows: t.setdefault((r["Family"], r["View"]), {})[r["Chunker"]] = r
    for (f, v), c in sorted(t.items()):
        g = lambda k: "%.3f" % (c[k]["Pack"] / 1e6) if k in c else "-"
        print("| %s | %s | %s | %s | %s | %s | %.1f |" % (f, v, g("whole"), g("cdc16/64/256"), g("cdc1/4/16"), g("cdc256/1K/4K"), c["whole"]["Raw"] / 1e6))
