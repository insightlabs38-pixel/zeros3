#!/usr/bin/env python3
"""Aggregate P0 JSONL into medians/spread tables. usage: summarize.py FILE.jsonl [--md] [--prefix P] [--unstable]
Spread = min..max over independent repetitions (fresh store + fresh process each). cv% = stdev/mean of the headline metric.
Instability flag: cv% > 10 or max/min > 1.25."""
import json, sys, statistics as st, collections, re

def load(path):
    rows = []
    for l in open(path):
        l = l.strip()
        if l:
            rows.append(json.loads(l))
    return rows

def g(r, path, d=None):
    for k in path.split('.'):
        if not isinstance(r, dict) or k not in r:
            return d
        r = r[k]
    return r

def stats(v):
    v = [x for x in v if x is not None]
    if not v:
        return None
    m = st.mean(v)
    return dict(n=len(v), med=st.median(v), lo=min(v), hi=max(v), cv=(st.pstdev(v) / m * 100 if m else 0))

def fmt(s, p=1):
    if s is None:
        return '-'
    return f"{s['med']:.{p}f} [{s['lo']:.{p}f}-{s['hi']:.{p}f}]"

def main():
    path = sys.argv[1]
    prefix = ''
    if '--prefix' in sys.argv:
        prefix = sys.argv[sys.argv.index('--prefix') + 1]
    rows = [r for r in load(path) if r.get('label', '').startswith(prefix)]
    grp = collections.OrderedDict()
    for r in rows:
        grp.setdefault(r['label'], []).append(r)
    kind = rows[0].get('scenario', '') if rows else ''
    hdr = ['label', 'n', 'MiB/s', 'ops/s', 'p50 ms', 'p99 ms', 'ttfb p50', 'srv cores', 'peak RSS MiB', 'phys/logical', 'ok', 'cv%']
    print('| ' + ' | '.join(hdr) + ' |')
    print('|' + '---|' * len(hdr))
    for lab, rs in grp.items():
        mib = stats([r.get('mib_s') for r in rs])
        ops = stats([r.get('ops_s') for r in rs])
        p50 = stats([g(r, 'lat.p50_ms') or g(r, 'read_lat.p50_ms') for r in rs])
        p99 = stats([g(r, 'lat.p99_ms') or g(r, 'read_lat.p99_ms') for r in rs])
        tt = stats([g(r, 'ttfb.p50_ms') for r in rs])
        cpu = stats([r.get('srv_cpu_cores') for r in rs])
        rss = stats([r.get('srv_peak_rss_mib') for r in rs])
        ph = stats([r.get('phys_per_logical') for r in rs])
        ok = all(r.get('verified', True) is not False for r in rs)
        cvv = mib['cv'] if mib else 0
        flag = ' **UNSTABLE**' if mib and (mib['cv'] > 10 or mib['hi'] / max(mib['lo'], 1e-9) > 1.25) else ''
        print(f"| {lab} | {len(rs)} | {fmt(mib)} | {fmt(ops,0)} | {fmt(p50,2)} | {fmt(p99,2)} | {fmt(tt,2)} | {fmt(cpu,2)} | {fmt(rss,0)} | {fmt(ph,3)} | {'Y' if ok else 'N'} | {cvv:.0f}{flag} |")

main()
