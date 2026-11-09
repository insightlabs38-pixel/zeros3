#!/usr/bin/env python3
"""report_tables.py FILE.jsonl A B [title] : markdown A-vs-B table (median [min-max] over interleaved rounds) from p0bench JSONL."""
import json, collections, statistics as st, sys
f, a, b = sys.argv[1:4]
title = sys.argv[4] if len(sys.argv) > 4 else f
d = collections.defaultdict(list)
for l in open(f):
    l = l.strip()
    if not l: continue
    r = json.loads(l)
    v = r.get('note', 'variant=?').split()[0].split('=')[1]
    d[(r['label'], v)].append(r)
def med(x, g): return st.median(g(r) for r in x)
def unit(r):
    if r.get('scenario') == 'get': return ('ops_s' if 'ops_s' in r else 'mib_s')
    return 'ops_s' if any(s in r['label'] for s in ('_0b_', '_1b_', '_1k_', '_4k_', '_16k_', '_64k_', '_128k_', '_255k_', '_256k_', '_512k_')) else 'mib_s'
print('\n### %s\n' % title)
print('| cell | %s | %s | x | p50 ms %s->%s | p99 ms %s->%s | srv CPU-s/obj %s->%s | RSS MiB | files/obj | phys/logical | verified |' % (a, b, a, b, a, b, a, b))
print('|---|---:|---:|---:|---|---|---|---|---|---|---|')
mixed = []
labels = []
for k in d:
    if k[0] not in labels: labels.append(k[0])
for lab in labels:
    x, y = d.get((lab, a)), d.get((lab, b))
    if not x or not y: continue
    if x[0].get('scenario') == 'mixed':
        mixed.append(lab); continue
    k = unit(x[0]); u = 'ops/s' if k == 'ops_s' else 'MiB/s'
    if k not in x[0]: k = 'mib_s'; u = 'MiB/s'
    n = x[0]['params'].get('objs', 1) if 'params' in x[0] else 1
    fx = lambda z: '%.0f [%.0f-%.0f]' % (med(z, lambda r: r[k]), min(r[k] for r in z), max(r[k] for r in z))
    def lat(z, key):
        try: return med(z, lambda r: r['lat'][key])
        except Exception: return float('nan')
    cpu = lambda z: med(z, lambda r: r.get('srv_cpu_s', float('nan'))) / max(1, n)
    files = lambda z: med(z, lambda r: r.get('store', {}).get('Files', float('nan'))) / max(1, n)
    print('| %s (%s) | %s | %s | %.2fx | %.1f->%.1f | %.1f->%.1f | %.4f->%.4f | %.0f->%.0f | %.2f->%.2f | %.2f->%.2f | %s |' % (
        lab, u, fx(x), fx(y), med(y, lambda r: r[k]) / med(x, lambda r: r[k]),
        lat(x, 'p50_ms'), lat(y, 'p50_ms'), lat(x, 'p99_ms'), lat(y, 'p99_ms'), cpu(x), cpu(y),
        max(r.get('srv_hwm_mib', 0) for r in x), max(r.get('srv_hwm_mib', 0) for r in y), files(x), files(y),
        med(x, lambda r: r.get('phys_per_logical', float('nan'))), med(y, lambda r: r.get('phys_per_logical', float('nan'))),
        all(r.get('verified') in (True, None) for r in x + y)))

if mixed:
    print('\n| mixed cell | reads/s %s->%s | writes/s %s->%s | read p99 ms | write p99 ms |' % (a, b, a, b)); print('|---|---|---|---|---|')
    for lab in mixed:
        x, y = d[(lab, a)], d[(lab, b)]
        print('| %s | %.0f->%.0f (%.2fx) | %.0f->%.0f (%.2fx) | %.1f->%.1f | %.1f->%.1f |' % (lab,
            med(x, lambda r: r['reads_s']), med(y, lambda r: r['reads_s']), med(y, lambda r: r['reads_s']) / med(x, lambda r: r['reads_s']),
            med(x, lambda r: r['writes_s']), med(y, lambda r: r['writes_s']), med(y, lambda r: r['writes_s']) / med(x, lambda r: r['writes_s']),
            med(x, lambda r: r['read_lat']['p99_ms']), med(y, lambda r: r['read_lat']['p99_ms']),
            med(x, lambda r: r['write_lat']['p99_ms']), med(y, lambda r: r['write_lat']['p99_ms'])))
