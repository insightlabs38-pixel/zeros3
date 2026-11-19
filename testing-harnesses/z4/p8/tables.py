#!/usr/bin/env python3
"""Markdown tables from the P8 jsonl rows. usage: tables.py migrate BASE.jsonl NEW.jsonl | rep BASE NEW | matcher FILE"""
import json, sys, statistics as st, collections

def rows(f, kind):
    out = collections.defaultdict(list)
    for l in open(f):
        r = json.loads(l)
        if r.get('p8') == kind:
            out[r.get('family') or r.get('file')].append(r)
    return out

def med(v, k): return st.median(x[k] for x in v)
def spread(v, k): return "%.2f [%.2f-%.2f]" % (med(v, k), min(x[k] for x in v), max(x[k] for x in v))

mode = sys.argv[1]
if mode == 'migrate':
    b, n = rows(sys.argv[2], 'migrate'), rows(sys.argv[3], 'migrate')
    print('| family | base wall s | P8 wall s | speedup | base CPU s | P8 CPU s | RSS MiB | alloc MiB | source MiB decoded | physical bytes after gc/repack | exact |')
    print('|---|---|---|---|---|---|---|---|---|---|---|')
    for f in n:
        if f not in b: continue
        x, y = b[f], n[f]
        print('| %s | %s | %s | %.2fx | %.1f | %.1f | %.0f → %.0f | %.0f → %.0f | %.0f → %.0f | %d → %d (%+.2f%%) | %s |' % (
            f, spread(x, 'wall_s'), spread(y, 'wall_s'), med(x, 'wall_s') / med(y, 'wall_s'), med(x, 'cpu_s'), med(y, 'cpu_s'),
            med(x, 'rss_mib'), med(y, 'rss_mib'), med(x, 'alloc_mib'), med(y, 'alloc_mib'), med(x, 'src_mib'), med(y, 'decoded_mib') if 'decoded_mib' in y[0] else med(y, 'src_mib'),
            x[0]['phys_after'], y[0]['phys_after'], 100.0 * (y[0]['phys_after'] / x[0]['phys_after'] - 1), all(r['exact'] for r in x + y)))
elif mode == 'rep':
    b, n = rows(sys.argv[2], 'rep'), rows(sys.argv[3], 'rep')
    print('| file | base build s | P8 build s | speedup | base replay s | P8 replay s | speedup | base CPU s | P8 CPU s | alloc MiB | RSS MiB | exact |')
    print('|---|---|---|---|---|---|---|---|---|---|---|---|')
    for f in n:
        x, y = b[f], n[f]
        print('| %s | %s | %s | %.2fx | %.2f | %.2f | %.2fx | %.1f | %.1f | %.0f → %.0f | %.0f → %.0f | %s |' % (f, spread(x, 'build_s'), spread(y, 'build_s'), med(x, 'build_s') / med(y, 'build_s'), med(x, 'replay_s'), med(y, 'replay_s'), med(x, 'replay_s') / med(y, 'replay_s'),
            med(x, 'cpu_s'), med(y, 'cpu_s'), med(x, 'alloc_mib'), med(y, 'alloc_mib'), med(x, 'rss_mib'), med(y, 'rss_mib'), all(r['exact'] for r in x + y)))
elif mode == 'matcher':
    d = collections.defaultdict(dict)
    for l in open(sys.argv[2]):
        r = json.loads(l)
        if r.get('p8') == 'matcher': d[(r['family'], r['base'], r['target'])][r['matcher']] = r
    print('| family | pair | xd MiB/s | dw MiB/s | xd envelope | dw envelope | exact |')
    print('|---|---|---|---|---|---|---|')
    for k, v in d.items():
        if 'xd' in v and 'dw' in v:
            print('| %s | %s→%s | %.0f | %.0f | %d | %d (%+.1f%%) | %s |' % (k[0], k[1], k[2], v['xd']['mibps'], v['dw']['mibps'], v['xd']['envelope'], v['dw']['envelope'], 100.0 * (v['dw']['envelope'] / v['xd']['envelope'] - 1), v['xd']['exact'] and v['dw']['exact']))
