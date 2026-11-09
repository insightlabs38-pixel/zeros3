#!/usr/bin/env python3
"""make_tables.py RESULTS_DIR : generates P1C_TABLES.md (all tables of the report) from the raw JSONL/text evidence."""
import json, collections, statistics as st, sys, os, re, subprocess
R = sys.argv[1]
out = []
def P(*a): out.append(' '.join(str(x) for x in a))
def load(f):
    p = os.path.join(R, f)
    if not os.path.exists(p): return []
    rows = []
    for l in open(p):
        l = l.strip()
        if l:
            try: rows.append(json.loads(l))
            except Exception: pass
    return rows
def var(r): return r.get('note', 'variant=?').split()[0].split('=')[1]
def med(x, g):
    v = [g(r) for r in x if g(r) is not None]
    return st.median(v) if v else float('nan')
def tp(r):
    k = 'ops_s' if 'ops_s' in r and any(s in r['label'] for s in ('_0b_', '_1b_', '_1k_', '_4k_', '_16k_', '_64k_', '_128k_', '_255k_', '_256k_', '_512k_')) else ('mib_s' if 'mib_s' in r else 'ops_s')
    return k

# ---- main matrix ----
P('# P1C generated tables\n')
def ab(f, a, b, title):
    rows = load(f)
    if not rows: return
    d = collections.defaultdict(list)
    for r in rows: d[(r['label'], var(r))].append(r)
    P('\n## %s (%s)\n' % (title, f))
    P('| cell | %s | %s | x | p50 ms | p90 ms | p99 ms | srv CPU-ms/obj | RSS MiB | files/obj | phys/logical |' % (a, b))
    P('|---|---:|---:|---:|---|---|---|---|---|---|---|')
    mixed = []
    labs = []
    for k in d:
        if k[0] not in labs: labs.append(k[0])
    for lab in labs:
        x, y = d.get((lab, a)), d.get((lab, b))
        if not x or not y: continue
        if x[0].get('scenario') == 'mixed': mixed.append(lab); continue
        k = tp(x[0]); u = 'ops/s' if k == 'ops_s' else 'MiB/s'
        n = x[0].get('params', {}).get('objs', 1) or 1
        fx = lambda z: '%.0f [%.0f-%.0f]' % (med(z, lambda r: r[k]), min(r[k] for r in z), max(r[k] for r in z))
        lat = lambda z, q: med(z, lambda r: r.get('lat', {}).get(q))
        cpu = lambda z: 1000 * med(z, lambda r: r.get('srv_cpu_s')) / n
        files = lambda z: med(z, lambda r: r.get('store', {}).get('Files')) / n
        P('| %s (%s) | %s | %s | %.2fx | %.1f->%.1f | %.1f->%.1f | %.1f->%.1f | %.2f->%.2f | %.0f->%.0f | %.2f->%.2f | %.2f->%.2f |' % (
            lab, u, fx(x), fx(y), med(y, lambda r: r[k]) / med(x, lambda r: r[k]),
            lat(x, 'p50_ms'), lat(y, 'p50_ms'), lat(x, 'p90_ms'), lat(y, 'p90_ms'), lat(x, 'p99_ms'), lat(y, 'p99_ms'), cpu(x), cpu(y),
            max(r.get('srv_hwm_mib', 0) for r in x), max(r.get('srv_hwm_mib', 0) for r in y), files(x), files(y),
            med(x, lambda r: r.get('phys_per_logical')), med(y, lambda r: r.get('phys_per_logical'))))
    for lab in mixed:
        x, y = d[(lab, a)], d[(lab, b)]
        P('\n| mixed cell | reads/s | writes/s | read p99 ms | write p99 ms |\n|---|---|---|---|---|')
        P('| %s | %.0f->%.0f (%.2fx) | %.0f->%.0f (%.2fx) | %.1f->%.1f | %.1f->%.1f |' % (lab,
            med(x, lambda r: r['reads_s']), med(y, lambda r: r['reads_s']), med(y, lambda r: r['reads_s']) / med(x, lambda r: r['reads_s']),
            med(x, lambda r: r['writes_s']), med(y, lambda r: r['writes_s']), med(y, lambda r: r['writes_s']) / med(x, lambda r: r['writes_s']),
            med(x, lambda r: r['read_lat']['p99_ms']), med(y, lambda r: r['read_lat']['p99_ms']), med(x, lambda r: r['write_lat']['p99_ms']), med(y, lambda r: r['write_lat']['p99_ms'])))
ab('final_matrix.jsonl', 'base', 'p1c', 'Main matrix: P1B (5b9bcdd) vs P1C, 3 interleaved rounds')
ab('big_order.jsonl', 'base', 'p1c', 'Large objects, 5 rounds with alternating order')

# ---- ablation ----
rows = load('ablation.jsonl')
if rows:
    d = collections.defaultdict(list)
    for r in rows: d[(r['label'], var(r))].append(r)
    P('\n## Ablation (ablation.jsonl): ops/s, median of 3 interleaved rounds\n')
    names = [('base', 'P1B'), ('p1c_segoff', 'P1C code, segments off (loose, pool on)'), ('p1c_nopool', 'P1C, chunker pool off'), ('p1c', 'P1C (all)')]
    labs = sorted({k[0] for k in d}, key=lambda s: (int(re.search(r'put_(\d+)', s).group(1)) if re.search(r'put_(\d+)', s) else 0))
    P('| variant | ' + ' | '.join(labs) + ' |'); P('|---|' + '---:|' * len(labs))
    for v, desc in names:
        P('| %s | %s |' % (desc, ' | '.join('%.0f' % med(d[(l, v)], lambda r: r[tp(r)]) if d.get((l, v)) else '-' for l in labs)))

# ---- gomaxprocs ----
rows = load('gomaxprocs.jsonl')
if rows:
    d = collections.defaultdict(list)
    for r in rows: d[(r['label'], var(r))].append(r)
    P('\n## GOMAXPROCS 1 / 2 / 4 (gomaxprocs.jsonl): base -> P1C\n')
    P('| cell | 1 | 2 | 4 |'); P('|---|---|---|---|')
    for lab in sorted({k[0] for k in d}):
        cells = []
        for g in ('1', '2', '4'):
            x, y = d.get((lab, 'base' + g)), d.get((lab, 'p1c' + g))
            if not x or not y: cells.append('-'); continue
            k = tp(x[0]); cells.append('%.0f -> %.0f (%.2fx)' % (med(x, lambda r: r[k]), med(y, lambda r: r[k]), med(y, lambda r: r[k]) / med(x, lambda r: r[k])))
        P('| %s | %s |' % (lab, ' | '.join(cells)))

# ---- scale ----
rows = load('scale.jsonl')
if rows:
    P('\n## Scale (scale.jsonl)\n')
    cfg = collections.OrderedDict()
    for r in rows:
        note = r.get('note', '')
        m = re.search(r'variant=(\S+) objs=(\d+) delfrac=(\S+)', note)
        if not m: continue
        cfg.setdefault((m.group(1), int(m.group(2)), m.group(3)), []).append(r)
    P('| variant | objects | delfrac | populate s | files | dirs | du MiB | open warm s | open cold s | open RSS MiB | verify -deep s | gc s | gc -apply s | repack -apply s | files after |')
    P('|---|---:|---:|---:|---:|---:|---:|---|---|---|---|---|---|---|---|')
    for (v, n, df), rs in cfg.items():
        g = lambda sc, key: [r.get(key) for r in rs if r.get('scenario') == sc and r.get(key) is not None]
        mk = [r for r in rs if r.get('scenario') == 'mkstore']
        ino = [r for r in rs if r.get('scenario') == 'inodes']
        ino2 = [r for r in rs if r.get('scenario') == 'inodes_after_maintenance']
        sw = [r for r in rs if r.get('scenario') == 'startup' and r.get('cache') == 'warm']
        sc = [r for r in rs if r.get('scenario') == 'startup' and r.get('cache') == 'cold']
        cli = lambda name: [r['wall_s'] for r in rs if r.get('scenario') == 'cli' and r.get('label', '').startswith('cli_' + name + '_')]
        f = lambda x: '%.2f' % st.median(x) if x else '-'
        P('| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |' % (v, n, df,
            '%.0f' % mk[0]['populate_s'] if mk else '-', ino[0]['files'] if ino else '-', ino[0]['dirs'] if ino else '-', '%.0f' % (ino[0]['du_kib'] / 1024) if ino else '-',
            f([r['ready_s'] for r in sw]), f([r['ready_s'] for r in sc]), '%.0f' % max(r['rss_mib'] for r in sw) if sw else '-',
            f(cli('verify__deep')), f(cli('gc')), f(cli('gc__apply')), f(cli('repack__apply')), ino2[0]['files'] if ino2 else '-'))

# ---- prototype delay ----
rows = load('proto_delay.jsonl')
if rows:
    d = collections.defaultdict(list)
    for r in rows: d[(r['C'], r['Note'].split()[0])].append(r)
    P('\n## Fixed batching window vs natural batching (prototype, 4 KiB, proto_delay.jsonl)\n')
    P('| concurrency | window | ops/s | p50 ms | p99 ms | avg group |'); P('|---|---|---:|---:|---:|---:|')
    for (c, w), rs in sorted(d.items()):
        P('| %d | %s | %.0f | %.2f | %.2f | %.1f |' % (c, w.replace('delay=', ''), med(rs, lambda r: r['OpsPerSec']), med(rs, lambda r: r['P50']), med(rs, lambda r: r['P99']), med(rs, lambda r: r['AvgGroup'])))

rows = load('churn.jsonl')
if rows:
    P('\n## Space reclamation under churn (churn.jsonl): 100,000 x 4 KiB objects, a fraction deleted, history pruned\n')
    P('| variant | deleted | stage | files | store MiB | wall s |'); P('|---|---|---|---:|---:|---:|')
    d = collections.OrderedDict()
    for r in rows:
        m = re.search(r'variant=(\S+) objs=(\d+) delfrac=(\S+)', r.get('note', ''))
        if not m: continue
        d.setdefault((m.group(1), m.group(3)), []).append(r)
    for (v, df), rs in d.items():
        wall = {r['label']: r['wall_s'] for r in rs if r.get('scenario') == 'cli'}
        for r in rs:
            if r.get('scenario') == 'churn':
                w = {'pruned': wall.get('churn_prune'), 'after_gc': wall.get('churn_gc'), 'after_repack': wall.get('churn_repack')}.get(r['stage'])
                P('| %s | %s | %s | %d | %.0f | %s |' % (v, df, r['stage'], r['files'], r['du_kib'] / 1024, '%.2f' % w if w else '-'))

for f, title in (('syscalls_per_object.txt', 'Syscalls per object (strace -c -f, whole server lifetime)'), ('store_layer_bench.txt', 'Store layer, no HTTP (in-process, loose vs segments)'), ('locator_scale.txt', 'Locator scale (synthetic)')):
    p = os.path.join(R, f)
    if os.path.exists(p):
        P('\n## %s (%s)\n\n```' % (title, f)); P(open(p).read().rstrip()); P('```')
open(os.path.join(R, 'P1C_TABLES.md'), 'w').write('\n'.join(out) + '\n')
print('wrote P1C_TABLES.md', len(out), 'lines')
