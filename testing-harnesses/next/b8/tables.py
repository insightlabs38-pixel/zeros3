"""Print the B8 tables from raw JSONL rows: python3 tables.py results/*.jsonl"""
import collections, gzip, json, statistics as st, sys

rows = []
for p in sys.argv[1:]:
    op = gzip.open if p.endswith('.gz') else open
    with op(p, 'rt') as f:
        rows += [json.loads(l) for l in f if l.strip()]

lanes = {}
for r in rows:
    if r.get('kind') == 'lane':
        lanes[(r['family'], r['lane'])] = r  # the last row of a lane wins (reruns)
fams = list(dict.fromkeys(f for f, _ in lanes))
MB = lambda b: '%.2f' % (b / 1e6) if b is not None else ''


def alloc(f, l):
    r = lanes.get((f, l))
    return r['allocated'] if r else None


for prof in ['archive', 'mixed', 'hot']:
    cfgs = ['b7', 'b7r', 'b7rg', 'b8s', 'b8', 'b8-nodelta', 'b8-nozip']
    have = [c for c in cfgs if any((f, c + '-' + prof) in lanes for f in fams)]
    if not have:
        continue
    print(f'\n## Allocated store bytes (MB) after optimize + gc + repack + compact, {prof}\n')
    print('| family | cdc | ' + ' | '.join(have) + ' | b8 / pre-B8 best | b8 / b7rg | verify | plan = model |')
    print('|---|' + '---:|' * (len(have) + 3) + '---|---|')
    for f in fams:
        if not any((f, c + '-' + prof) in lanes for c in have):
            continue
        vals = [alloc(f, c + '-' + prof) for c in have]
        pre = [v for v in (alloc(f, 'b7-' + prof), alloc(f, 'b7r-' + prof)) if v]
        b8, rg = alloc(f, 'b8-' + prof), alloc(f, 'b7rg-' + prof)
        ok = all(lanes[(f, c + '-' + prof)].get('verify_ok') for c in have if (f, c + '-' + prof) in lanes)
        exact = all(lanes[k]['report']['planned']['bytes'] == lanes[k].get('materialized_model', {}).get('bytes')
                    for k in ((f, c + '-' + prof) for c in have) if k in lanes)
        print(f'| {f} | {MB(alloc(f, "cdc"))} | ' + ' | '.join(MB(v) for v in vals) +
              f' | {"%.3f" % (b8 / min(pre)) if b8 and pre else ""} | {"%.3f" % (b8 / rg) if b8 and rg else ""} | {ok} | {exact} |')

print('\n## Canonical analysis (b8 archive)\n')
print('| family | containers | rejected | streams | shared | plain MB | recipe MB | analysis s | planned deltas / reps / pins / cdc |')
print('|---|---:|---:|---:|---:|---:|---:|---:|---|')
for f in fams:
    r = lanes.get((f, 'b8-archive'))
    if not r or not r['report'].get('canon'):
        continue
    c, p = r['report']['canon'], r['report']['planned']
    print(f'| {f} | {c["containers"]} | {c["rejected"]} | {c["streams"]} | {c["shared"]} | {MB(c["plain_bytes"])} | '
          f'{MB(c["recipe_bytes"])} | {c["seconds"]:.1f} | {p["deltas"]} / {p["reps"]} / {p["pins"]} / {p["cdc"]} |')

print('\n## Migration cost (archive)\n')
print('| family | lane | wall s | CPU s | written MB | peak allocated MB | RSS MiB | reclaim s |')
print('|---|---|---:|---:|---:|---:|---:|---:|')
for (f, l), r in lanes.items():
    if not l.endswith('-archive') or r.get('migrate_wall_s') is None:
        continue
    print(f'| {f} | {l} | {r["migrate_wall_s"]:.1f} | {r["migrate_cpu_s"]:.1f} | {MB(r["migrate_written"])} | {MB(r["peak_allocated"])} | '
          f'{r["migrate_rss_mib"]:.0f} | {r["reclaim_s"]:.1f} |')

reads = collections.defaultdict(list)
for r in rows:
    if r.get('kind') == 'read':
        reads[(r['family'], r['lane'], r['row']['Mode'])].append(r)
if reads:
    print('\n## Cold reads through the production reader (medians; model = optimizer read model)\n')
    print('| family | lane | mode | n | wall ms | CPU ms | device reads | model ms | forms |')
    print('|---|---|---|---:|---:|---:|---:|---:|---|')
    for k in sorted(reads):
        v = reads[k]
        mk = 'model_whole_us' if k[2] == 'whole' else 'model_us'
        forms = collections.Counter(x.get('form') for x in v)
        print(f'| {k[0]} | {k[1]} | {k[2]} | {len(v)} | {st.median(x["row"]["WallUS"] for x in v) / 1e3:.2f} | '
              f'{st.median(x["row"]["CPUUS"] for x in v) / 1e3:.2f} | {st.median(x["row"]["DiskReads"] for x in v):.0f} | '
              f'{st.median(x.get(mk) or 0 for x in v) / 1e3:.2f} | {", ".join(f"{a}:{b}" for a, b in forms.items())} |')

conc = [r for r in rows if r.get('kind') == 'concurrent']
if conc:
    print('\n## Concurrent readers\n')
    print('| family | lane | order | reads/s | p50 µs | p95 µs | p99 µs | CPU s |')
    print('|---|---|---|---:|---:|---:|---:|---:|')
    for r in conc:
        print(f'| {r["family"]} | {r["lane"]} | {r.get("order", "")} | {r.get("reads", 0) / max(r.get("seconds", 1), 1):.0f} | {r.get("p50_us", 0):.0f} | '
              f'{r.get("p95_us", 0):.0f} | {r.get("p99_us", 0):.0f} | {r.get("cpu_s", 0):.1f} |')

scale = [r for r in rows if r.get('kind') == 'scale']
if scale:
    print('\n## Scale (synthetic histories, planning only)\n')
    keys = [k for k in scale[0] if k != 'kind']
    print('| ' + ' | '.join(keys) + ' |')
    print('|' + '---:|' * len(keys))
    for r in scale:
        print('| ' + ' | '.join(str(round(r[k], 3)) if isinstance(r[k], float) else str(r[k]) for k in keys) + ' |')
