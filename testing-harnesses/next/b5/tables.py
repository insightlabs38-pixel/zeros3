#!/usr/bin/env python3
"""Summarize B5 raw rows (results/*.jsonl) as the README tables.
usage: tables.py RESULTS_DIR [--detail]
Paired ratio: for every (object, mode, offset) the configuration's wall time over the baseline's ("base",
old layout, prefetch off) on the same read; the geometric mean of those ratios is reported."""
import json, sys, glob, os, math, statistics as S, collections
R = sys.argv[1]
detail = '--detail' in sys.argv
def rows(p):
    return [json.loads(l) for f in sorted(glob.glob(os.path.join(R, p))) for l in open(f)]
def q(v, p):
    v = sorted(v); return v[min(len(v) - 1, int(p * len(v)))] if v else 0
def gm(v):
    return math.exp(sum(math.log(x) for x in v) / len(v)) if v else float('nan')
order = ['base', 'baseP', 'blk', 'blkP', 'rbP']
print('## Reads (wall µs; ratio = paired geometric mean vs base)\n')
print('| family | mode | ' + ' | '.join(f'{c} p50 / p95 / ratio' for c in order) + ' |')
print('|---|---|' + '---|' * len(order))
phys = []
for f in sorted(glob.glob(os.path.join(R, 'reads-*.jsonl'))):
    rs = [json.loads(l) for l in open(f)]
    fam = rs[0]['Label']
    by = collections.defaultdict(lambda: collections.defaultdict(list))
    for r in rs:
        by[(r['Key'], r['Mode'], r['Off'], r['Len'])][r['Store']].append(r)
    modes = collections.defaultdict(lambda: collections.defaultdict(list))
    for (k, m, o, l), d in by.items():
        if 'base' not in d:
            continue
        b = S.median([x['WallUS'] for x in d['base']])
        for c, v in d.items():
            modes[m][c].append((S.median([x['WallUS'] for x in v]), b, v))
    for m in sorted(modes):
        cells = []
        for c in order:
            v = modes[m].get(c)
            if not v:
                cells.append('')
                continue
            w = [x[0] for x in v]
            cells.append(f'{q(w,.5):.0f} / {q(w,.95):.0f} / {gm([x[0]/x[1] for x in v if x[1]>0]):.2f}')
            allr = [r for x in v for r in x[2]]
            phys.append((fam, m, c, len(w), S.median([r['Records'] for r in allr]), S.median([r['StoredBytes'] for r in allr]),
                         S.median([r['LogicalBytes'] for r in allr]), S.median([r['DiskReads'] for r in allr]), S.median([r['DiskBytes'] for r in allr]),
                         S.median([r['DecodeNS'] / 1000 for r in allr]), S.median([r['ReadNS'] / 1000 for r in allr]), S.median([r['CPUUS'] for r in allr]),
                         S.median([r['AllocBytes'] for r in allr]), q([x[0] for x in v], .99)))
        print(f'| {fam} | {m} | ' + ' | '.join(cells) + ' |')
print('\n## Physical work per read (medians)\n')
print('| family | mode | config | n | records | stored B | decoded B | device reads | device B | decode µs | pread µs | CPU µs | alloc B | p99 µs |')
print('|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
for p in phys:
    if detail or p[1] in ('cold4k', 'cold64k', 'whole'):
        print('| %s | %s | %s | %d | %.1f | %.0f | %.0f | %.1f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f |' % p)
c = rows('concurrent.jsonl')
if c:
    print('\n## Concurrent mixed-object readers (8 readers, 15 s, 75% 4 KiB / 25% 64 KiB, cold start)\n')
    print('| family | config | reads/s | p50 µs | p95 µs | p99 µs | CPU s | device reads | device MB |')
    print('|---|---|---:|---:|---:|---:|---:|---:|---:|')
    for r in c:
        print(f"| {r['label']} | {r['config']} | {r['reads_per_s']:.0f} | {r['p50_us']} | {r['p95_us']} | {r['p99_us']} | {r['cpu_s']:.1f} | {r['disk_reads']} | {r['disk_bytes']/1e6:.1f} |")
rc = rows('repack-cost.jsonl')
if rc:
    print('\n## Storage and rewrite cost (offline repack of the whole store)\n')
    print('| family | variant | allocated before | after | Δ | rewrite wall s | CPU s | peak RSS MiB | bytes written | recon: windows now → planned, cross-cluster, relaid |')
    print('|---|---|---:|---:|---:|---:|---:|---:|---:|---|')
    for r in rc:
        x = r.get('recon') or {}
        rec = f"{x.get('windows_per_probe_now',0):.2f} → {x.get('windows_per_probe_planned',0):.2f}, {x.get('cross_cluster_share',0):.2f}, {x.get('families_relaid',0)}" if x else ''
        print(f"| {r['family']} | {r['variant']} | {r['allocated_base']:,} | {r['allocated']:,} | {100*(r['allocated']/r['allocated_base']-1):+.2f}% | {r['wall_s']:.2f} | {r['cpu_s']:.2f} | {r['peak_rss_mib']:.0f} | {r['bytes_written']:,} | {rec} |")
mu = rows('maint-under-load.jsonl')
if mu:
    print('\n## Online maintenance under load (4 readers of 4 KiB ranges, warm page cache)\n')
    for r in mu:
        if r['kind'] == 'maint-phase':
            print(f"* {r['phase']}: {r['reads']} reads, p50 {r['p50_us']} µs, p95 {r['p95_us']}, p99 {r['p99_us']}, max {r['max_us']}")
        elif r['kind'] == 'maint-pass':
            x = r.get('recon') or {}
            print(f"* pass {r['pass']}: {r['wall_ms']} ms, process CPU {r['process_cpu_s']:.1f} s, {r['packs_written']} packs / {r['bytes_written']:,} B written, {r['packs_retired']} retired / {r['bytes_reclaimed']:,} B reclaimed, relaid {x.get('families_relaid')}")
        elif r['kind'] == 'maint-idle-passes':
            print(f"* {r['passes']} further passes found nothing to do (relayout plan memoized)")
        elif r['kind'] == 'maint-store':
            print(f"* allocated {r['allocated_before']:,} → {r['allocated_after']:,} B")
mg = rows('migration.jsonl')
if mg:
    print('\n## Migration (Coreutils 9.5→9.7, depth 2, 1 MiB windows)\n')
    print('| binary | run | wall s | CPU s | peak RSS MiB | single-record decodes | MB | run decodes | MB |')
    print('|---|---:|---:|---:|---:|---:|---:|---:|---:|')
    for r in mg:
        print(f"| {r['binary']} | {r.get('run','')} | {r['wall_s']} | {r['cpu_s']} | {r.get('peak_rss_mib','')} | {r.get('single_record_decodes','')} | {r.get('single_record_mb','')} | {r.get('run_decodes','')} | {r.get('run_mb','')} |")
ob = rows('reads-oldbinary.jsonl')
if ob:
    print('\n## Old binary (b169eda) on the same base stores and offsets (separate process, not interleaved)\n')
    g = collections.defaultdict(list)
    for r in ob:
        g[(r['Store'], r['Key'][:14], r['Mode'])].append(r['WallUS'])
    for k, v in sorted(g.items()):
        print(f'* {k[0]} {k[1]} {k[2]}: p50 {q(v,.5)} µs, p95 {q(v,.95)} (n={len(v)})')
