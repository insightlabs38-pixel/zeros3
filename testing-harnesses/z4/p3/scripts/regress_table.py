#!/usr/bin/env python3
"""regress_table.py FILE.jsonl base new : median [min-max] throughput per scenario/phase and the ratio new/base."""
import json, sys, collections, statistics
f, a, b = sys.argv[1:4]
g = collections.defaultdict(lambda: collections.defaultdict(list))
for l in open(f):
    r = json.loads(l)
    v = r.get('note', '').split('variant=')[-1].split()[0]
    key = (r['label'], r.get('phase') or r.get('name') or '')
    big = r['label'].startswith(('put_1m', 'put_64m'))
    val = r.get('mib_s') if big else r.get('ops_s')
    if val is None:
        val = r.get('mib_s') or r.get('ops_s')
    if val is None or not r.get('verified', True):
        continue
    g[key][v].append((val, r.get('lat', {}).get('p99_ms')))
print(f"| scenario | phase | unit | {a} | {b} | {b}/{a} | p99 ms {a} -> {b} |")
print("|---|---|---|---:|---:|---:|---|")
for key, vs in g.items():
    if a not in vs or b not in vs:
        continue
    unit = 'MiB/s' if key[0].startswith(('put_1m', 'put_64m')) else 'ops/s'
    fa = [x[0] for x in vs[a]]; fb = [x[0] for x in vs[b]]
    pa = [x[1] for x in vs[a] if x[1]]; pb = [x[1] for x in vs[b] if x[1]]
    fmt = lambda L: f"{statistics.median(L):.0f} [{min(L):.0f}-{max(L):.0f}]"
    p = f"{statistics.median(pa):.1f} -> {statistics.median(pb):.1f}" if pa and pb else ""
    print(f"| {key[0]} | {key[1]} | {unit} | {fmt(fa)} | {fmt(fb)} | {statistics.median(fb)/statistics.median(fa):.3f} | {p} |")
