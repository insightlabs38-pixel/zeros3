"""Print the B10 tables from raw rows: python3 tables.py results/*.jsonl results/*.jsonl.gz"""
import gzip, json, statistics as st, sys

rows = []
for p in sys.argv[1:]:
    op = gzip.open if p.endswith('.gz') else open
    with op(p, 'rt') as f:
        rows += [json.loads(l) for l in f if l.strip()]
lanes = {}
for r in rows:
    if r.get('kind') == 'lane':
        lanes[(r['family'], r['lane'])] = r  # the last row of a lane wins (reruns)
MB = lambda b: '%.2f' % (b / 1e6) if b is not None else ''
rep = lambda r: (r or {}).get('report') or {}
pl = lambda r: rep(r).get('planned', {}).get('bytes')
mm = lambda r: ((r or {}).get('materialized_model') or {}).get('bytes')
fams = []
for f, _ in lanes:
    if f not in fams:
        fams.append(f)
cols = ['cdc', 'b9-archive', 'b9x-archive', 'b10-archive', 'b10-all-archive', 'b10-nonest-archive', 'b10-nopage-archive', 'b10-nodelta-archive']
print('## Allocated MB after optimize + gc + repack + compact (plan = materialized model: =, else the model MB)\n')
print('| family | ' + ' | '.join(c.replace('-archive', '') for c in cols) + ' | b10/b9x | splice: units, kept, refused, recipe MB |')
print('|---|' + '---:|' * len(cols) + '---:|---|')
for f in fams:
    cells = []
    for c in cols:
        r = lanes.get((f, c))
        if not r:
            cells.append('')
            continue
        ok = r.get('verify_ok') and (c == 'cdc' or pl(r) == mm(r))
        cells.append(MB(r['allocated']) + ('' if ok else ' (!%s)' % MB(mm(r))))
    a, b = lanes.get((f, 'b9x-archive')), lanes.get((f, 'b10-archive'))
    ratio = '%.3f' % (b['allocated'] / a['allocated']) if a and b else ''
    sp = rep(b).get('splice') or {}
    spl = '%d+%d, %d, %d, %s' % (sp.get('children', 0), sp.get('pages', 0), sp.get('kept', 0), sp.get('refused', 0), MB(sp.get('recipe_bytes', 0))) if sp else ''
    print(f'| {f} | ' + ' | '.join(cells) + f' | {ratio} | {spl} |')

print('\n## Read-priced profiles (allocated MB; kind-16 records kept)\n')
print('| family | b9x-mixed | b10-mixed | b9x-hot | b10-hot |')
print('|---|---:|---:|---:|---:|')
for f in fams:
    cells = []
    for c in ['b9x-mixed', 'b10-mixed', 'b9x-hot', 'b10-hot']:
        r = lanes.get((f, c))
        n = sum(1 for ch in rep(r).get('changes') or [] if ch.get('to') == 'splice') if r else 0
        cells.append('%s (%d)' % (MB(r['allocated']), n) if r else '')
    if any(cells):
        print(f'| {f} | ' + ' | '.join(cells) + ' |')

print('\n## Optimize cost (b10 archive vs b9x archive)\n')
print('| family | wall s b9x/b10 | cpu s | rss MiB | splice analysis s | evals | written MB |')
print('|---|---|---|---|---:|---|---|')
for f in fams:
    a, b = lanes.get((f, 'b9x-archive')), lanes.get((f, 'b10-archive'))
    if not (a and b):
        continue
    g = lambda r, k: r.get(k) or 0
    print(f'| {f} | {g(a,"migrate_wall_s"):.1f}/{g(b,"migrate_wall_s"):.1f} | {g(a,"migrate_cpu_s"):.1f}/{g(b,"migrate_cpu_s"):.1f} | '
          f'{g(a,"migrate_rss_mib"):.0f}/{g(b,"migrate_rss_mib"):.0f} | {(rep(b).get("splice") or {}).get("seconds", 0):.1f} | '
          f'{rep(a).get("evals")}/{rep(b).get("evals")} | {MB(g(a,"migrate_written"))}/{MB(g(b,"migrate_written"))} |')

reads = [r for r in rows if r.get('kind') == 'read']
if reads:
    print('\n## Reads (median ms over objects and offsets; cold = page cache and process caches dropped; model = optimizer)\n')
    print('| family | lane | cold 4 KiB | model 4 KiB | warm 4 KiB | cold 64 KiB | cold 1 MiB | whole | disk reads 4 KiB | CPU ms 4 KiB |')
    print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|')
    by = {}
    for r in reads:
        by.setdefault((r['family'], r['lane']), []).append(r)
    for (f, l), rs in sorted(by.items()):
        def med(mode, key=lambda x: x['row']['WallUS'] / 1e3):
            v = [key(x) for x in rs if x['row']['Mode'] == mode and key(x) is not None]
            return '%.2f' % st.median(v) if v else ''
        print(f'| {f} | {l} | {med("cold4k")} | {med("cold4k", lambda x: (x.get("model_us") or 0) / 1e3)} | {med("warm4k")} | {med("cold64k")} | '
              f'{med("cold1m")} | {med("whole")} | {med("cold4k", lambda x: x["row"]["DiskReads"])} | {med("cold4k", lambda x: x["row"]["CPUUS"] / 1e3)} |')
conc = [r for r in rows if r.get('kind') == 'concurrent']
if conc:
    print('\n## Concurrent readers (closed loop)\n')
    print('| family | lane | order | reads/s | p50 ms | p95 ms | p99 ms | CPU s |')
    print('|---|---|---|---:|---:|---:|---:|---:|')
    for r in conc:
        g = lambda k: r.get(k) or 0
        print(f'| {r["family"]} | {r["lane"]} | {r.get("order")} | {g("reads") / max(g("seconds"), 1e-9):.0f} | {g("p50_us") / 1e3:.2f} | {g("p95_us") / 1e3:.2f} | {g("p99_us") / 1e3:.2f} | {g("cpu_s"):.1f} |')
cache = [r for r in rows if r.get('kind') == 'cache']
if cache:
    print('\n## Decoded-window cache (4 KiB reads/s, mean of both orders; off vs 64 MiB)\n')
    print('| family | lane | workload | off | 64 MiB | ratio |')
    print('|---|---|---|---:|---:|---:|')
    agg = {}
    for r in cache:
        agg.setdefault((r['family'], r['lane'], r['workload'], r['budget']), []).append(r['reads_per_s'])
    for (f, l, w, b), v in sorted(agg.items()):
        if b:
            continue
        on = agg.get((f, l, w, 64 << 20))
        off, onm = st.mean(v), st.mean(on) if on else 0
        print(f'| {f} | {l} | {w} | {off:.0f} | {onm:.0f} | {onm / off:.2f} |')
