#!/usr/bin/env python3
"""Representation table: per kind, CDC(off) vs shadow vs prefer for PUT wall/CPU/RSS/physical size and GET/range.
usage: reps_table.py results/reps.jsonl"""
import json, sys, re, statistics as st, collections
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
g = collections.defaultdict(list)
for r in rows:
    g[r['label']].append(r)
def med(v, f):
    x = [f(r) for r in v if f(r) is not None]
    return st.median(x) if x else None
kinds = sorted({re.match(r'(k\d+_[a-z0-9_]+?)_(put|get|r4k|r1m)_', k).group(1) for k in g if re.match(r'(k\d+_[a-z0-9_]+?)_(put|get|r4k|r1m)_', k)})
print('| kind | mode | logical MiB | PUT s (all files) | PUT MiB/s | srv CPU s | peak RSS MiB | phys/logical | whole GET MiB/s | 4K range ms p50 | 1M range ms p50 | verified |')
print('|' + '---|' * 12)
for k in kinds:
    for m in ['off', 'shadow', 'prefer']:
        p, w, a, b = (g.get(f'{k}_{t}_{m}') for t in ['put', 'get', 'r4k', 'r1m'])
        if not p: continue
        lg = med(p, lambda r: r['logical_bytes'] / 2**20)
        ok = all(r.get('verified', True) is not False for v in (p, w, a, b) if v for r in v)
        f = lambda x: '-' if x is None else (f'{x:.2f}' if x < 100 else f'{x:.0f}')
        print(f"| {k} | {m} | {f(lg)} | {f(med(p, lambda r: r['wall_s']))} | {f(med(p, lambda r: r['mib_s']))} | {f(med(p, lambda r: r['srv_cpu_s']))} | {f(med(p, lambda r: r['srv_peak_rss_mib']))} | {f(med(p, lambda r: r['store']['Total'] / r['logical_bytes']))} | {f(med(w, lambda r: r['mib_s']) if w else None)} | {f(med(a, lambda r: r['lat']['p50_ms']) if a else None)} | {f(med(b, lambda r: r['lat']['p50_ms']) if b else None)} | {'Y' if ok else 'N'} |")
