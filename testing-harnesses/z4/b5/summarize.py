#!/usr/bin/env python3
"""Merge machine-readable B5 outputs into evidence.json.gz and print the cumulative-storage table.
usage: summarize.py OUT EV QUANTDIR EVIDENCE.json.gz"""
import glob, gzip, json, os, sys
out, ev, evidence = sys.argv[1], sys.argv[2], sys.argv[4]
def load(p):
    try: return json.load(open(p))
    except Exception: return None
res = {'families': {}, 'experiments': {}}
for fam in sorted(os.listdir(out)):
    d = os.path.join(out, fam)
    if not os.path.isdir(d): continue
    f = {'policies': {}}
    cdc = [json.loads(l) for l in open(os.path.join(d, 'cdc.jsonl'))] if os.path.exists(os.path.join(d, 'cdc.jsonl')) else []
    if cdc:
        f['raw'] = sum(c['raw'] for c in cdc); f['cdc_prod'] = cdc[-1]['prod_cum']; f['cdc_fine'] = cdc[-1]['fine_cum']
        f['cdc_prod_per_state'] = [c['prod_inc'] for c in cdc]
    for p in glob.glob(os.path.join(d, '*.json')):
        n = os.path.basename(p)[:-5]
        if n.endswith('.verify'): continue
        j = load(p)
        v = load(os.path.join(d, n + '.verify.json'))
        if j and 'total' in j and 'states' in j:
            f['policies'][n] = {'total': j['total'], 'per_state': [s['charged'] for s in j['states']], 'max_depth': max(s['max_depth'] for s in j['states']),
                                'enc_secs': sum(s['enc_secs'] for s in j['states']), 'exact_in_process': all(s['exact'] for s in j['states']),
                                'fresh_process_exact': (v or {}).get('exact'), 'fresh_process_bad': (v or {}).get('bad'), 'states': len(j['states'])}
            rp = os.path.join(d, n + '.replay.jsonl')
            if os.path.exists(rp):
                rr = [json.loads(l) for l in open(rp) if l.startswith('{')]
                f['policies'][n]['replay_ms'] = [r['ms'] for r in rr]; f['policies'][n]['replay_store_read'] = [r['store_read'] for r in rr]
    for k, pre in (('model8', 'model8_'), ('b1', 'b1_')):
        j = load(os.path.join(ev, f'{pre}{fam}.json'))
        if j: f[k] = j
    m = load(os.path.join(ev, f'matrix_{fam}.json'))
    if m: f['matrix'] = m
    res['families'][fam] = f
for p in sorted(glob.glob(os.path.join(ev, '*.json'))):
    n = os.path.basename(p)[:-5]
    if n.startswith(('model8_', 'b1_', 'matrix_')): continue
    res['experiments'][n] = load(p)
qd = sys.argv[3]
res['quant'] = {os.path.basename(p)[:-5]: load(p) for p in glob.glob(os.path.join(qd, '*.json'))}
res['quant_text'] = {os.path.basename(p): open(p).read() for p in glob.glob(os.path.join(qd, '*.txt'))}
res['quant_verify'] = {os.path.basename(p): load(p) for p in glob.glob(os.path.join(qd, '*.verify.json'))}
for p in glob.glob(os.path.join(qd, 'cdc_*.jsonl')):
    res['quant']['cdc_' + os.path.basename(p)[4:-6]] = [json.loads(l) for l in open(p)]
res['perf'] = {os.path.basename(p): open(p).read() for p in glob.glob(os.path.join(out, 'perf', '*')) if os.path.isfile(p) and os.path.getsize(p) < 300000}
res['crossdtype'] = load(os.path.join(out, 'crossdtype', 'f32_to_f16.json'))
res['text'] = {os.path.basename(p): open(p).read() for p in glob.glob(os.path.join(ev, '*.txt')) if os.path.getsize(p) < 100000}
res['status'] = open(os.path.join(out, 'status.txt')).read()
with gzip.open(evidence, 'wt') as g: json.dump(res, g)
print('%-11s %9s %9s %9s %9s %9s %9s %9s %9s %9s %9s' % ('family', 'raw MB', 'CDCprod', 'CDCfine', 'Model8', 'B1gen', 'B1pxr', 'direct', 'first', 'prev', 'best'))
for fam, f in res['families'].items():
    if 'cdc_prod' not in f: continue
    c = f['cdc_prod']
    def r(x): return '%9.3f' % (x / c) if x else '%9s' % '-'
    pol = f['policies']; best = min([v['total'] for v in pol.values()] or [0])
    print('%-11s %9.1f %9.1f %9s %s %s %s %s %s %s %s' % (fam, f['raw'] / 1e6, c / 1e6, '%.3f' % (f['cdc_fine'] / c),
          r((f.get('model8') or {}).get('model8_total', 0)), r((f.get('b1') or {}).get('total', {}).get('Generic', 0)), r((f.get('b1') or {}).get('total', {}).get('PXRControl', 0)),
          r(pol.get('direct', {}).get('total', 0)), r(pol.get('first', {}).get('total', 0)), r(pol.get('prev', {}).get('total', 0)), r(best)))
