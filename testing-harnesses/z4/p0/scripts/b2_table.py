#!/usr/bin/env python3
"""B2 (draft #97) physical/time/read table from results/b2_econ*.jsonl (medians over repeated rows). usage: b2_table.py FILE..."""
import json, sys, statistics as st, collections
R = collections.defaultdict(lambda: collections.defaultdict(list))
base = {}
for f in sys.argv[1:]:
    for l in open(f):
        r = json.loads(l)
        k = (r.get('family'), r.get('scenario'))
        if 'phys' in r and r.get('scenario') == 'cdc': base.setdefault(r['family'], []).append(r['phys'])
        if 'migrate_s' in r:
            for m in ['migrate_s', 'migrate_rss_mib', 'peak_during_migration', 'gc_s', 'repack_s', 'phys']: R[k][m].append(r[m])
        if r.get('reads') == 'cold': R[k]['cold_whole'].append(r['stats']['WholeMBps']); R[k]['cold_4k_us'].append(r['stats']['RangeUs']['4K']); R[k]['amp_4k'].append(r['stats']['RangeAmp']['4K'])
        if r.get('reads') == 'warm-cache256MiB': R[k]['warm_whole'].append(r['stats']['WholeMBps'])
        if r.get('reads') in ('cold', 'warm') and r.get('scenario') == 'cdc': R[k]['cold_whole' if r['reads'] == 'cold' else 'warm_whole'].append(r['stats']['WholeMBps'])
        if r.get('reads') == 'cold' and r.get('scenario') == 'cdc': pass
        if r.get('recovery') and 'deep_verify_s' in r: R[k]['deep_verify_s'].append(r['deep_verify_s'])
        if r.get('scn') in ('cdc', 'cdc+additive-rep'): pass
print('| family | variant | phys/CDC | migrate s | migrate RSS MiB | peak during migration (x CDC) | gc s | repack s | cold whole MB/s | cold 4K us | 4K amp | n |')
print('|' + '---|' * 12)
f = lambda v, p=2: '-' if not v else f'{st.median(v):.{p}f}'
for (fam, sc), d in sorted(R.items(), key=lambda x: (str(x[0][0]), str(x[0][1]))):
    if fam not in base: continue
    b = st.median(base[fam])
    if 'migrate_s' not in d and sc != 'cdc': continue
    print(f"| {fam} | {sc} | {('1.000' if sc=='cdc' else f(d.get('phys') and [x/b for x in d['phys']],3))} | {f(d.get('migrate_s'),1)} | {f(d.get('migrate_rss_mib'),0)} | {('-' if not d.get('peak_during_migration') else f'{st.median(d['peak_during_migration'])/b:.2f}')} | {f(d.get('gc_s'),1)} | {f(d.get('repack_s'),1)} | {f(d.get('cold_whole'),0)} | {f(d.get('cold_4k_us'),0)} | {f(d.get('amp_4k'),0)} | {len(d.get('migrate_s', d.get('cold_whole', [])))} |")
