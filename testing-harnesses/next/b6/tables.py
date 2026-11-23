#!/usr/bin/env python3
"""B6 tables from the campaign rows. usage: tables.py ROWS.jsonl[.gz] ...
storage, migration, paired reads (geometric mean of per-read wall ratios), model fit, plans."""
import gzip, json, math, sys
from collections import defaultdict

rows = []
for p in sys.argv[1:]:
    op = gzip.open if p.endswith('.gz') else open
    with op(p, 'rt') as f:
        rows += [json.loads(l) for l in f if l.strip()]
lanes = defaultdict(dict)
for r in rows:
    if r['kind'] == 'lane':
        lanes[r['family']][r['lane']] = r
ORDER = ['cdc', 'local', 'local-archive', 'seq-loc', 'global', 'global-loc', 'global-archive', 'global-hot', 'reopt', 'reopt-archive', 'global-loc3', 'global-archive3']
fams = [f for f in ['coreutils', 'cu-files', 'go', 'sqlite', 'bundle', 'git', 'ts', 'random'] if f in lanes]

def med(x):
    x = sorted(x)
    return x[len(x) // 2] if x else float('nan')

def q(x, p):
    x = sorted(x)
    return x[min(len(x) - 1, int(p * len(x)))] if x else float('nan')

def gm(x):
    x = [v for v in x if v > 0]
    return math.exp(sum(map(math.log, x)) / len(x)) if x else float('nan')

print('## Storage after gc + repack + compact (allocated bytes; ratio to cdc and to local)')
print('| family | ' + ' | '.join(l for l in ORDER) + ' |')
print('|---|' + '---:|' * len(ORDER))
for f in fams:
    c = lanes[f].get('cdc', {}).get('allocated')
    lo = lanes[f].get('local', {}).get('allocated')
    cells = []
    for l in ORDER:
        r = lanes[f].get(l)
        if not r:
            cells.append('')
            continue
        a = r['allocated']
        cells.append(f"{a/1e6:.2f} MB ({a/c:.3f}/{a/lo:.3f})" if c and lo else f"{a/1e6:.2f}")
    print(f'| {f} | ' + ' | '.join(cells) + ' |')

print('\n## Plan estimate vs materialized store (closure bytes, model units)')
print('| family | lane | planned | model of materialized | measured retained | measured/planned | verify |')
print('|---|---|---:|---:|---:|---:|---|')
for f in fams:
    for l in ORDER:
        r = lanes[f].get(l)
        if not r or not isinstance(r.get('report'), dict) or 'planned' not in r['report']:
            continue
        p = r['report']['planned']['bytes']
        mm = r.get('materialized_model', {}).get('bytes', 0)
        m = r['retained_model_units']
        print(f"| {f} | {l} | {p:,} | {mm:,} | {m:,} | {m/p:.3f} | {r['verify_ok']} |")

print('\n## Migration cost')
print('| family | lane | wall s | CPU s | written MB | peak allocated MB | RSS MiB | applied | bridges | failed | candidates s | search s | encodes |')
print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
for f in fams:
    for l in ORDER:
        r = lanes[f].get(l)
        if not r or 'migrate_wall_s' not in r:
            continue
        rep = r.get('report') or {}
        ap = rep.get('applied', rep.get('Admitted', ''))
        print(f"| {f} | {l} | {r['migrate_wall_s']:.1f} | {r['migrate_cpu_s']:.1f} | {r['migrate_written']/1e6:.1f} | {r['peak_allocated']/1e6:.1f} | {r['migrate_rss_mib']:.0f} | {ap} | {rep.get('bridges','')} | {rep.get('failed','')} | {rep.get('candidate_seconds',0):.1f} | {rep.get('search_seconds',0):.2f} | {rep.get('encodes','')} |")

reads = defaultdict(lambda: defaultdict(list))  # (family, mode) -> lane -> rows
for r in rows:
    if r['kind'] == 'read':
        reads[(r['family'], r['row']['Mode'])][r['lane']].append(r)

def paired(f, mode, lane, ref):
    a = {(x['sha'], x['row']['Off'], i): x['row']['WallUS'] for i, x in enumerate(reads[(f, mode)][lane])}
    b = {(x['sha'], x['row']['Off'], i): x['row']['WallUS'] for i, x in enumerate(reads[(f, mode)][ref])}
    return gm([a[k] / b[k] for k in a if k in b and b[k] > 0])

for mode in ['cold4k', 'cold64k', 'whole', 'warm4k']:
    print(f'\n## {mode}: p50 / p95 / p99 µs, paired ratio vs local and vs cdc, median device reads, records, decoded KB')
    print('| family | lane | p50 | p95 | p99 | /local | /cdc | dev reads | records | decoded KB | depth |')
    print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
    for f in fams:
        for l in ORDER:
            rs = reads[(f, mode)].get(l)
            if not rs:
                continue
            w = [x['row']['WallUS'] for x in rs]
            print(f"| {f} | {l} | {med(w):.0f} | {q(w,.95):.0f} | {q(w,.99):.0f} | {paired(f,mode,l,'local'):.2f} | {paired(f,mode,l,'cdc'):.2f} | "
                  f"{med([x['row']['DiskReads'] for x in rs]):.0f} | {med([x['row']['Records'] for x in rs]):.0f} | {med([x['row']['LogicalBytes'] for x in rs])/1e3:.0f} | "
                  f"{max(x.get('depth', 0) for x in rs)} |")

print('\n## Read model fit (cold reads, every lane): wall µs ~ a*records + b*decoded bytes + c')
for mode in ['cold4k', 'cold64k']:
    X, Y, P = [], [], []
    for f in fams:
        for l, rs in reads[(f, mode)].items():
            for x in rs:
                if 'model_us' in x:
                    X.append((x['row']['Records'], x['row']['LogicalBytes'], 1.0))
                    Y.append(x['row']['WallUS'])
                    P.append(x['model_us'])
    if len(X) < 10:
        continue
    # normal equations, 3x3
    A = [[sum(a[i] * a[j] for a in X) for j in range(3)] for i in range(3)]
    bvec = [sum(a[i] * y for a, y in zip(X, Y)) for i in range(3)]
    def solve(A, b):
        n = len(b); M = [row[:] + [b[i]] for i, row in enumerate(A)]
        for c in range(n):
            p = max(range(c, n), key=lambda r: abs(M[r][c])); M[c], M[p] = M[p], M[c]
            for r in range(n):
                if r != c:
                    k = M[r][c] / M[c][c]
                    M[r] = [x - k * y for x, y in zip(M[r], M[c])]
        return [M[i][n] / M[i][i] for i in range(n)]
    co = solve(A, bvec)
    fit = [sum(c * v for c, v in zip(co, a)) for a in X]
    my = sum(Y) / len(Y)
    r2 = 1 - sum((y - f) ** 2 for y, f in zip(Y, fit)) / sum((y - my) ** 2 for y in Y)
    mp = sum(P) / len(P)
    r2m = 1 - sum((y - p) ** 2 for y, p in zip(Y, P)) / sum((y - my) ** 2 for y in Y)
    corr = sum((y - my) * (p - mp) for y, p in zip(Y, P)) / math.sqrt(sum((y - my) ** 2 for y in Y) * sum((p - mp) ** 2 for p in P))
    print(f"* {mode}: n={len(Y)}, fit a={co[0]:.1f} µs/record, b={co[1]*1e3:.2f} µs/KB, c={co[2]:.0f} µs, R²={r2:.3f}; "
          f"model as configured: mean {mp:.0f} vs measured {my:.0f} µs, corr {corr:.3f}, R² {r2m:.3f}")

print('\n## Plans on identical candidates (model): sequential rule vs global vs oracle (bytes, modeled sparse µs)')
print('| family | lane | cdc | sequential rule | global | oracle | global/sequential | partitions | largest | evals |')
print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|')
for f in fams:
    for l in ORDER:
        r = lanes[f].get(l)
        rep = (r or {}).get('report') or {}
        if 'planned' not in rep:
            continue
        o = rep.get('oracle') or {}
        lo, pl = rep['local'], rep['planned']
        print(f"| {f} | {l} | {rep['cdc_only']['bytes']:,} | {lo['bytes']:,} / {lo['sparse_us']:.0f} | {pl['bytes']:,} / {pl['sparse_us']:.0f} | "
              f"{o.get('bytes','-'):,} / {o.get('sparse_us',0):.0f} | {pl['bytes']/lo['bytes']:.3f} | {rep['partitions']} | {rep['largest_partition']} | {rep['evals']} |"
              if o else
              f"| {f} | {l} | {rep['cdc_only']['bytes']:,} | {lo['bytes']:,} / {lo['sparse_us']:.0f} | {pl['bytes']:,} / {pl['sparse_us']:.0f} | - | {pl['bytes']/lo['bytes']:.3f} | {rep['partitions']} | {rep['largest_partition']} | {rep['evals']} |")

print('\n## Search ablations (global-loc lane, same candidates and weight): objective / bytes')
for f in fams:
    rep = (lanes[f].get('global-loc') or {}).get('report') or {}
    if rep.get('planned'):
        parts = [f"sequential {rep['local']['objective']:,.0f} / {rep['local']['bytes']:,}"]
        parts += [f"{a['name']} {a['objective']:,.0f} / {a['bytes']:,}" for a in rep.get('ablations') or []]
        parts.append(f"global {rep['planned']['objective']:,.0f} / {rep['planned']['bytes']:,}")
        if rep.get('oracle'):
            parts.append(f"oracle {rep['oracle']['objective']:,.0f} ({rep.get('oracle_partitions')} partitions)")
        print(f"* {f}: " + '; '.join(parts))

print('\n## Frontier (global-loc lane sweep: read weight -> bytes / modeled sparse µs / max depth / deltas)')
for f in fams:
    rep = (lanes[f].get('global-loc') or {}).get('report') or {}
    if rep.get('frontier'):
        print(f"* {f}: " + '; '.join(f"{x['name']}: {x['bytes']/1e6:.2f} MB / {x['sparse_us']:.0f} µs / d{x['max_depth']} / {x['deltas']}" for x in rep['frontier']))

conc = [r for r in rows if r['kind'] == 'concurrent']
if conc:
    print('\n## Concurrent readers (8 closed-loop, 75% 4 KiB / 25% 64 KiB): reads/s, p50/p95/p99 µs, CPU s')
    print('| family | lane | order | reads/s | p50 | p95 | p99 | CPU s |')
    print('|---|---|---|---:|---:|---:|---:|---:|')
    for r in conc:
        print(f"| {r['family']} | {r['lane']} | {r['order']} | {r['reads']/r['seconds']:.0f} | {r['p50_us']} | {r['p95_us']} | {r['p99_us']} | {r['cpu_s']:.1f} |")
sc = [r for r in rows if r['kind'] == 'scale']
if sc:
    print('\n## Planning scale (synthetic 4-version histories with shared boilerplate)')
    print('| contents | options | partitions | largest | encodes | candidates s | search s | evals | RSS MiB | cdc | sequential | global |')
    print('|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
    for r in sc:
        print(f"| {r['contents']} | {r['options']} | {r['partitions']} | {r['largest']} | {r['encodes']} | {r['candidate_s']:.1f} | {r['search_s']:.2f} | {r['evals']} | {r['rss_mib']:.0f} | {r['cdc']:,} | {r['local']:,} | {r['planned']:,} |")
