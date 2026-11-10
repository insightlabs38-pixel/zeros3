#!/usr/bin/env python3
"""Aggregate the P4 JSONL rows into markdown tables.
usage: report_tables.py BASELINE_OFF_SHADOW.jsonl BASE_PREFER.jsonl RESDIR   (RESDIR holds after*.jsonl, mix_*.jsonl)"""
import json, sys, os, collections

def load(path):
    d = collections.defaultdict(dict)
    if not os.path.exists(path):
        return d
    for l in open(path):
        r = json.loads(l)
        d[r['Label'].split('/')[1]][r['Mode']] = r
    return d

base = load(sys.argv[1]); base_pref = load(sys.argv[2]); res = sys.argv[3]
after = load(f'{res}/after.jsonl'); defer = load(f'{res}/after_deferred.jsonl')
kinds = sorted(k for k in base if not k.startswith('k99'))
f0 = lambda x: f'{x:,.0f}'
f1 = lambda x: f'{x:,.1f}'
out = []
def table(title, head, rows):
    out.append(f'\n### {title}\n'); out.append('| ' + ' | '.join(head) + ' |'); out.append('|' + '|'.join('---' if i == 0 else '---:' for i in range(len(head))) + '|')
    for r in rows: out.append('| ' + ' | '.join(r) + ' |')

rows = []
for k in kinds:
    o, b, a, d = after[k]['off'], base[k]['shadow'], after[k]['shadow'], defer[k]['shadow']
    rows.append([k, f0(o['PutMS']), f0(b['PutMS']), f0(a['PutMS']), f0(d['PutMS']), f0(d['PutMS'] + d['QuiesceMS']), f'{b["PutMS"]/max(d["PutMS"],1):.0f}x'])
table('PUT latency (ms), serial, whole object set per kind', ['kind', 'off', 'inline before', 'inline after', 'deferred ack', 'deferred available', 'ack speedup vs before'], rows)

rows = []
for k in kinds:
    o, b, a, d = after[k]['off'], base[k]['shadow'], after[k]['shadow'], defer[k]['shadow']
    rows.append([k, f1(o['Size'] / 1e6), f0(o['CPUMS']), f0(b['CPUMS']), f0(a['CPUMS']), f0(d['CPUMS']), f"{b['RSSMB']:.0f}/{a['RSSMB']:.0f}/{d['RSSMB']:.0f}", f"{b['AllocMB']:.0f}/{a['AllocMB']:.0f}"])
table('CPU ms (user+sys, whole process incl. background), peak RSS MiB, allocation MiB', ['kind', 'MB', 'off', 'inline before', 'inline after', 'deferred', 'RSS b/a/d', 'alloc MiB b/a'], rows)

rows = []
for k in kinds:
    b, a = base[k]['shadow'], after[k]['shadow']
    rows.append([k, f0(b['AnalyzeMS']), f0(a['AnalyzeMS']), f0(b['PublMS']), f0(a['PublMS']), f0(b['ProofMS']), f0(a['ProofMS']), f0(b['RootMS'] + b['ReachMS']), f0(a['RootMS'] + a['ReachMS'])])
table('Build phases (ms, summed over objects): before / after', ['kind', 'analyze b', 'a', 'publish b', 'a', 'proof b', 'a', 'reach+root b', 'a'], rows)

rows = []
for k in kinds:
    a, o = after[k]['shadow'], after[k]['off']
    rows.append([k, str(a['Built']), str(a['Components']), str(a.get('CompUnique', 0)), f0(a.get('CompMedian', 0)), f1(a.get('CompBytes', 0) / 1e6), str(a['StoreFiles']), f1(o['StoreBytes'] / 1e6), f1(a['StoreBytes'] / 1e6), f"{a['StoreBytes']/max(o['StoreBytes'],1):.2f}x", f1(a.get('GCBytes', 0) / 1e6), f1(a.get('RepackB', 0) / 1e6)])
table('Component economics and storage (shadow, inline after)', ['kind', 'roots', 'components', 'unique', 'median B', 'unique MB', 'files', 'off MB', 'shadow MB', 'amp', 'after gc MB', 'after repack MB'], rows)

rows = []
for k in kinds:
    o, b, a = after[k]['off'], base[k]['shadow'], after[k]['shadow']
    rows.append([k, f0(o['DeepMS']), f0(b['DeepMS']), f0(a['DeepMS']), f"{b['DeepMS']/max(a['DeepMS'],1):.1f}x", f0(a.get('GCMS', 0)), f0(a.get('RepackMS', 0)), f0(a.get('ReopenDeepMS', 0)), str(a.get('ReopenOK'))])
table('Maintenance (ms): verify -deep and offline gc / repack on the shadow store', ['kind', 'deep off', 'deep before', 'deep after', 'gain', 'gc -apply', 'repack', 'deep after gc+repack', 'ok'], rows)

rows = []
for k in kinds:
    o = after[k]['off']; bp = base_pref.get(k, {}).get('prefer'); ap = after[k]['prefer']
    if not bp: continue
    rows.append([k, f0(o['R4kUS']), f0(bp['R4kUS']), f0(ap['R4kUS']), f0(o['R64kUS']), f0(bp['R64kUS']), f0(ap['R64kUS']), f0(o['R1mUS']), f0(bp['R1mUS']), f0(ap['R1mUS']), str(ap['RepReads']) + '/' + str(ap['Fallbacks'])])
table('Range reads µs/request: CDC (off) / prefer before / prefer after (reads that fall back are counted)', ['kind', '4K off', 'b', 'a', '64K off', 'b', 'a', '1M off', 'b', 'a', 'rep reads/fallbacks'], rows)

for name, files in (('Representative mix, 4 concurrent PUTs', ('mix_base.jsonl', 'mix_after.jsonl', 'mix_deferred.jsonl')), ('Representative mix, serial', ('mix_base_serial.jsonl', 'mix_after_serial.jsonl', None))):
    rows = []
    for lab, f in zip(('before', 'after', 'deferred'), files):
        if not f or not os.path.exists(f'{res}/{f}'): continue
        for l in open(f'{res}/{f}'):
            r = json.loads(l)
            rows.append([lab, r['Mode'], f0(r['PutMS']), f1(r['PutMBs']), f0(r['QuiesceMS']), f0(r['CPUMS']), f0(r['RSSMB']), f0(r['StoreBytes'] / 1e6), str(r['Built']), str(r['VerifyOK'])])
    table(name, ['code', 'mode', 'PUT wall ms', 'MB/s', 'then wait ms', 'CPU ms', 'RSS MiB', 'store MB', 'built', 'verify ok'], rows)
open(f'{res}/P4_TABLES.md', 'w').write('\n'.join(out) + '\n')
print('\n'.join(out))
