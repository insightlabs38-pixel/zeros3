#!/usr/bin/env python3
"""Least-squares fit of the B6 read model from "feature" rows (TestB6Refit). usage: fit.py FEATURES.jsonl"""
import json, sys, math
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
def solve(X, Y):
    n = len(X[0]); A = [[sum(a[i]*a[j] for a in X) for j in range(n)] for i in range(n)]
    b = [sum(a[i]*y for a, y in zip(X, Y)) for i in range(n)]
    M = [A[i][:] + [b[i]] for i in range(n)]
    for c in range(n):
        p = max(range(c, n), key=lambda r: abs(M[r][c])); M[c], M[p] = M[p], M[c]
        for r in range(n):
            if r != c:
                k = M[r][c] / M[c][c]; M[r] = [x - k*y for x, y in zip(M[r], M[c])]
    return [M[i][n] / M[i][i] for i in range(n)]
def r2(X, Y, co):
    f = [sum(c*v for c, v in zip(co, a)) for a in X]; m = sum(Y)/len(Y)
    return 1 - sum((y-v)**2 for y, v in zip(Y, f)) / sum((y-m)**2 for y in Y)
for r in rows:
    r['raw_all'] = r['raw'] + r['raw_b']
for mode, feats in [('cold4k', ['one', 'nodes', 'records', 'inflated', 'records_b', 'inflated_b', 'raw_all']), ('cold64k', ['one', 'nodes', 'records', 'inflated', 'records_b', 'inflated_b', 'raw_all']),
                    ('whole', ['one', 'whole_records', 'whole_inflated', 'depthbytes'])]:
    rs = [r for r in rows if r['mode'] == mode]
    if mode == 'whole':
        for r in rs: r['depthbytes'] = r['size'] * r['depth']
    for r in rows: r['one'] = 1
    X = [[r[f] for f in feats] for r in rs]; Y = [r['wall_us'] for r in rs]
    if len(rs) < len(feats) + 2:
        continue
    co = solve(X, Y)
    print(f"{mode}: n={len(rs)} " + ', '.join(f"{f}={c:.4g}" for f, c in zip(feats, co)) + f"  R²={r2(X, Y, co):.3f}")
    if mode.startswith('cold'):
        both = [r for r in rows if r['mode'] in ('cold4k', 'cold64k')]
        X2 = [[r[f] for f in feats] for r in both]; Y2 = [r['wall_us'] for r in both]
        co2 = solve(X2, Y2)
        if mode == 'cold64k':
            print(f"cold (4K+64K): n={len(both)} " + ', '.join(f"{f}={c:.4g}" for f, c in zip(feats, co2)) + f"  R²={r2(X2, Y2, co2):.3f}")
