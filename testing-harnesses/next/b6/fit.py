#!/usr/bin/env python3
"""Least-squares fit of the B6 read model from "feature" rows (TestB6Refit). usage: fit.py FEATURES.jsonl[.gz]
Prints the combined cold fit (the defaults in rgDefaultModel), the rejected own/COPY split, and the whole-read fit."""
import gzip, json, sys
op = gzip.open if sys.argv[1].endswith('.gz') else open
rows = [json.loads(l) for l in op(sys.argv[1], 'rt') if l.strip()]
for r in rows:
    r['one'] = 1
    r['R'], r['I'], r['W'] = r['records'] + r['records_b'], r['inflated'] + r['inflated_b'], r['raw'] + r['raw_b']
    r['depthbytes'] = r['size'] * r['depth']
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
def fit(name, rs, feats):
    X = [[r[f] for f in feats] for r in rs]; Y = [r['wall_us'] for r in rs]
    co = solve(X, Y); f = [sum(c*v for c, v in zip(co, a)) for a in X]; m = sum(Y)/len(Y)
    r2 = 1 - sum((y-v)**2 for y, v in zip(Y, f)) / sum((y-m)**2 for y in Y)
    print(f"{name}: n={len(rs)} " + ', '.join(f"{k}={c:.4g}" for k, c in zip(feats, co)) + f"  R²={r2:.3f}")
cold = [r for r in rows if r['mode'] in ('cold4k', 'cold64k')]
fit('cold, combined (defaults: Fixed, Node, Rec, Byte, RawByte)', cold, ['one', 'nodes', 'R', 'I', 'W'])
fit('cold, own/COPY split (rejected)', cold, ['one', 'nodes', 'records', 'inflated', 'records_b', 'inflated_b', 'W'])
fit('whole (WRec, WByte, WCopy)', [r for r in rows if r['mode'] == 'whole'], ['one', 'whole_records', 'whole_inflated', 'depthbytes'])
