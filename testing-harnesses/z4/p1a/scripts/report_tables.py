#!/usr/bin/env python3
"""report_tables.py : markdown tables (base vs final) from results/*.jsonl -> stdout."""
import json, collections, statistics as st, os
R = os.path.join(os.path.dirname(__file__), '..', 'results')
def load(f):
    d = collections.defaultdict(list)
    for l in open(os.path.join(R, f)):
        r = json.loads(l); v = r['note'].split()[0].split('=')[1]
        d[(r['label'], v)].append(r)
    return d
def med(rs, k): return st.median(k(r) for r in rs)
gib = lambda r: r['logical_bytes'] / 2**30
def row(d, lab, a='base', b='final'):
    A, B = d[(lab, a)], d[(lab, b)]
    ta, tb = med(A, lambda r: r['mib_s']), med(B, lambda r: r['mib_s'])
    ca, cb = med(A, lambda r: r['srv_cpu_s'] / gib(r)), med(B, lambda r: r['srv_cpu_s'] / gib(r))
    return '| %s | %.0f [%.0f-%.0f] | %.0f [%.0f-%.0f] | %.2fx | %.1f -> %.1f | %.2f -> %.2f | %.0f -> %.0f | %.0f -> %.0f |' % (
        lab, ta, min(r['mib_s'] for r in A), max(r['mib_s'] for r in A), tb, min(r['mib_s'] for r in B), max(r['mib_s'] for r in B), tb / ta,
        med(A, lambda r: r['srv_cpu_cores']), med(B, lambda r: r['srv_cpu_cores']), ca, cb,
        max(r['srv_hwm_mib'] for r in A), max(r['srv_hwm_mib'] for r in B), med(A, lambda r: r['lat']['p50_ms']), med(B, lambda r: r['lat']['p50_ms']))
hdr = '| scenario | base MiB/s | final MiB/s | speedup | cores | CPU-s/GiB | peak RSS MiB | p50 ms |\n|---|---:|---:|---:|---:|---:|---:|---:|'
m = load('final_matrix.jsonl')
print('## Matrix (interleaved, medians [min-max], n in final_matrix.jsonl)\n' + hdr)
for lab in sorted({k[0] for k in m}): print(row(m, lab))
g = load('final_gomaxprocs.jsonl')
print('\n## GOMAXPROCS sweep\n' + hdr.replace('scenario', 'scenario/procs'))
for lab in sorted({k[0] for k in g}):
    for n in (1, 2, 4): print(row(g, lab, 'base_p%d' % n, 'final_p%d' % n).replace('| %s |' % lab, '| %s p=%d |' % (lab, n), 1))
a = load('final_ablation.jsonl')
print('\n## Ablation of the final build (one mechanism off), MiB/s median [min-max]\n| scenario | base | final | no pipeline | sync pack writer | sync publish | full verify | no early fsync |\n|---|---:|---:|---:|---:|---:|---:|---:|')
for lab in sorted({k[0] for k in a}):
    cells = []
    for v in ('base', 'final', 'abl_nopipe', 'abl_syncwrite', 'abl_syncpublish', 'abl_fullverify', 'abl_noearlysync'):
        rs = a[(lab, v)]; cells.append('%.0f [%.0f-%.0f]' % (med(rs, lambda r: r['mib_s']), min(r['mib_s'] for r in rs), max(r['mib_s'] for r in rs)))
    print('| %s | %s |' % (lab, ' | '.join(cells)))
