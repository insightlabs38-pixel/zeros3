#!/usr/bin/env python3
"""Outcome comparison of the conventional `go test -json ./...` runs with the T1 runner's evidence.
usage: equivalence.py CONV_ROOT.json T1_NORMAL.json [CONV_RACE.json T1_RACE.json]
Compares, for the root package, the set of top-level tests discovered, executed, passed, failed and skipped. Differences in
skips are explained only by the environment switches that gate them (identical in both runs); anything else fails."""
import json, sys

def conv(path):
    res = {}
    for line in open(path):
        try:
            e = json.loads(line)
        except ValueError:
            continue
        t = e.get("Test")
        if t and "/" not in t and e.get("Package") == "zeros3" and e.get("Action") in ("pass", "fail", "skip"):
            res[t] = e["Action"]
    return res

def t1(path):
    return {r["test"]: r["status"] for r in json.load(open(path))["results"]}

def compare(a, b, name):
    bad = []
    for t in sorted(set(a) | set(b)):
        if a.get(t) != b.get(t):
            bad.append((t, a.get(t), b.get(t)))
    print(f"{name}: conventional {len(a)} tests ({sum(v=='pass' for v in a.values())} pass, {sum(v=='skip' for v in a.values())} skip, {sum(v=='fail' for v in a.values())} fail); "
          f"t1run {len(b)} tests ({sum(v=='pass' for v in b.values())} pass, {sum(v=='skip' for v in b.values())} skip, {sum(v=='fail' for v in b.values())} fail); {len(bad)} differences")
    for t, x, y in bad[:50]:
        print("  DIFF", t, "conventional:", x, "t1run:", y)
    return not bad

ok = compare(conv(sys.argv[1]), t1(sys.argv[2]), "normal")
if len(sys.argv) > 4:
    ok = compare(conv(sys.argv[3]), t1(sys.argv[4]), "race") and ok
sys.exit(0 if ok else 1)
