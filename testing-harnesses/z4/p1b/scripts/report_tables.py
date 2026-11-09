#!/usr/bin/env python3
"""report_tables.py RESULTS_DIR : markdown tables (median [min-max]) from the P1B JSONL evidence."""
import json, collections, statistics as st, sys, os
R = sys.argv[1]
def load(f):
    d = collections.defaultdict(list)
    for l in open(os.path.join(R, f)):
        r = json.loads(l)
        v = r.get('note', 'variant=?').split()[0].split('=')[1]
        d[(r['label'], v)].append(r)
    return d
def med(x, f): return st.median(f(a) for a in x)
def tput(lab): return 'ops_s' if any(s in lab for s in ('_4k_', '_16k_', '_64k_')) else 'mib_s'
def table(f, a, b, title):
    d = load(f); print('\n### %s (%s)\n' % (title, f))
    print('| cell | %s | %s | x | p50 ms | p99 ms | srv cores | peak RSS MiB | verified |' % (a, b)); print('|---|---:|---:|---:|---|---|---|---|---|')
    for lab in sorted({k[0] for k in d}):
        x, y = d.get((lab, a)), d.get((lab, b))
        if not x or not y: continue
        k = tput(lab + '_'); unit = 'ops/s' if k == 'ops_s' else 'MiB/s'
        fx = lambda z: '%.0f [%.0f-%.0f]' % (med(z, lambda r: r[k]), min(r[k] for r in z), max(r[k] for r in z))
        print('| %s (%s) | %s | %s | %.2fx | %.1f / %.1f | %.1f / %.1f | %.2f / %.2f | %.0f / %.0f | %s |' % (
            lab, unit, fx(x), fx(y), med(y, lambda r: r[k]) / med(x, lambda r: r[k]),
            med(x, lambda r: r['lat']['p50_ms']), med(y, lambda r: r['lat']['p50_ms']),
            med(x, lambda r: r['lat']['p99_ms']), med(y, lambda r: r['lat']['p99_ms']),
            med(x, lambda r: r['srv_cpu_cores']), med(y, lambda r: r['srv_cpu_cores']),
            max(r['srv_hwm_mib'] for r in x), max(r['srv_hwm_mib'] for r in y),
            all(r.get('verified') in (True, None) for r in x + y)))
table('matrix_base_vs_final.jsonl', 'base', 'final', 'Main matrix, baseline vs first full cut (3 interleaved rounds)')
table('mixed_sizes_put.jsonl', 'base', 'final2', 'Mixed object sizes 4k:50 64k:30 1m:15 16m:5')
for f in ('big_order_ab.jsonl', 'big_order_ba.jsonl'):
    table(f, 'base', 'final2', 'Large-object regression check, order ' + ('base,final' if 'ab' in f else 'final,base'))
d = load('gomaxprocs.jsonl'); print('\n### GOMAXPROCS sweep (gomaxprocs.jsonl)\n'); print('| cell | 1 base -> final | 2 base -> final | 4 base -> final |'); print('|---|---|---|---|')
for lab in sorted({k[0] for k in d}):
    k = tput(lab + '_'); cells = []
    for g in ('1', '2', '4'):
        x, y = d.get((lab, 'base' + g)), d.get((lab, 'fin' + g))
        cells.append('%.0f -> %.0f (%.2fx)' % (med(x, lambda r: r[k]), med(y, lambda r: r[k]), med(y, lambda r: r[k]) / med(x, lambda r: r[k])))
    print('| %s | %s |' % (lab, ' | '.join(cells)))
d = load('ablation_loose.jsonl'); print('\n### Ablation of single mechanisms, loose path only (ZEROS3_DIRECT_PACK_MIB=0; ablation_loose.jsonl)\n')
names = [('base', 'baseline binary'), ('L_none', 'P1B, all four mechanisms off'), ('L_noovl', '- manifest/chunk overlap off'), ('L_serial', '- group commit off'),
         ('L_nodir', '- shard-dir cache off'), ('L_nocoal', '- dir-fsync coalescing off'), ('L', 'P1B loose path (all on)')]
print('| variant | ' + ' | '.join(sorted({k[0] for k in d})) + ' |'); print('|---|' + '---|' * len({k[0] for k in d}))
for v, desc in names:
    row = []
    for lab in sorted({k[0] for k in d}):
        x = d.get((lab, v)); k = tput(lab + '_'); row.append('%.0f' % med(x, lambda r: r[k]) if x else '-')
    print('| %s | %s |' % (desc, ' | '.join(row)))
d = load('pack_threshold_matrix.jsonl'); print('\n### Direct-pack threshold probe (pack_threshold_matrix.jsonl): loose vs pack from 1 MiB (pkA) vs from 256 KiB (pkB)\n')
print('| cell | loose | pkA (1 MiB) | pkB (256 KiB) |'); print('|---|---:|---:|---:|')
for lab in sorted({k[0] for k in d}):
    k = tput(lab + '_'); print('| %s | %s |' % (lab, ' | '.join('%.0f' % med(d[(lab, v)], lambda r: r[k]) if (lab, v) in d else '-' for v in ('loose', 'pkA', 'pkB'))))
d = collections.defaultdict(list)
for l in open(os.path.join(R, 'mixed_put_get.jsonl')):
    r = json.loads(l); d[(r['label'], r['note'].split()[0].split('=')[1])].append(r)
print('\n### Mixed PUT/GET (mixed_put_get.jsonl; 10 s windows, 3 rounds; final = first cut, final2 = commit mutex)\n'); print('| workload | variant | reads/s | read p50/p99 ms | writes/s | write p50/p99 ms |'); print('|---|---|---:|---|---:|---|')
for (lab, v), x in sorted(d.items()):
    print('| %s | %s | %.0f | %.2f / %.2f | %.0f | %.1f / %.1f |' % (lab, v, med(x, lambda r: r['reads_s']), med(x, lambda r: r['read_lat']['p50_ms']), med(x, lambda r: r['read_lat']['p99_ms']), med(x, lambda r: r['writes_s']), med(x, lambda r: r['write_lat']['p50_ms']), med(x, lambda r: r['write_lat']['p99_ms'])))
