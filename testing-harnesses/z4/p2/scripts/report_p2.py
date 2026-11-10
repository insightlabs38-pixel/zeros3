#!/usr/bin/env python3
"""report_p2.py FILE.jsonl A B [cell-prefix ...] : markdown A-vs-B tables from interleaved p0bench `get` phases.

Rows are `label/phase` cells; every cell is the median over the interleaved rounds (min-max in brackets for the
headline metric). Throughput is ops/s for ranges/HEAD and MiB/s for whole-object phases; `x` is B/A for throughput.
CPU is server CPU-seconds per request (user+sys). Amplification is bytes the server read (rchar) per byte served.
"""
import json, sys, collections, statistics as st

f, a, b = sys.argv[1:4]
prefixes = sys.argv[4:]
d = collections.defaultdict(list)
for line in open(f):
    line = line.strip()
    if not line:
        continue
    r = json.loads(line)
    if 'note' not in r or 'lat' not in r:
        continue
    v = r['note'].split()[0].split('=')[1]
    d[(r['label'], v)].append(r)


def fmt(v):
    return '%.0f' % v if v >= 100 else '%.1f' % v if v >= 10 else '%.2f' % v


def med(rs, g):
    xs = [g(r) for r in rs]
    return st.median(xs)


def unit(r):
    return ('MiB/s', 'mib_s') if r['params'].get('mode') == 'whole' else ('ops/s', 'ops_s')


labels = []
for k in d:
    if k[0] not in labels:
        labels.append(k[0])
rows = []
for lab in labels:
    if prefixes and not any(lab.startswith(p) for p in prefixes):
        continue
    x, y = d.get((lab, a)), d.get((lab, b))
    if not x or not y:
        continue
    u, key = unit(x[0])
    n = x[0]['params'].get('nreq', 1) or 1
    thr = lambda z: med(z, lambda r: r[key])
    rng = lambda z: '%s [%s-%s]' % (fmt(thr(z)), fmt(min(r[key] for r in z)), fmt(max(r[key] for r in z)))
    lat = lambda z, k: med(z, lambda r: r['lat'][k])
    cpu = lambda z: med(z, lambda r: r['srv_cpu_s'] / n) * 1e6
    # HEAD serves no body: report server bytes read per request instead of per served byte
    amp = (lambda z: med(z, lambda r: r['srv_rchar'] / n)) if x[0]['params'].get('mode') == 'head' else (lambda z: med(z, lambda r: r['read_amp_rchar']))
    rss = lambda z: max(r.get('srv_hwm_mib', 0) for z_ in [z] for r in z_)
    ok = all(r.get('verified') for r in x + y)
    rows.append((lab, u, rng(x), rng(y), thr(y) / thr(x), lat(x, 'p50_ms'), lat(y, 'p50_ms'), lat(x, 'p99_ms'), lat(y, 'p99_ms'),
                 cpu(x), cpu(y), amp(x), amp(y), rss(x), rss(y), len(x), ok))



print('| cell | unit | %s | %s | x | p50 ms %s->%s | p99 ms %s->%s | CPU us/req %s->%s | read amp %s->%s | RSS MiB | rounds | verified |' % ((a, b) + (a, b) * 4))
print('|---|---|---:|---:|---:|---|---|---|---|---|---|---|')
for r in rows:
    print('| %s | %s | %s | %s | %.2fx | %.3g->%.3g | %.3g->%.3g | %.0f->%.0f | %.1f->%.1f | %.0f->%.0f | %d | %s |' % r)
