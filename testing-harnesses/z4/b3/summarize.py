#!/usr/bin/env python3
"""Compact the raw run logs of run_tensor_evidence.sh (WORK) and run_other_evidence.sh (WORK2) into one JSON document."""
import glob, json, os, sys
w1, w2 = sys.argv[1], sys.argv[2]
def jl(p):
    out = []
    if not os.path.exists(p): return out
    for l in open(p):
        l = l.strip()
        if l.startswith('{'):
            try: out.append(json.loads(l))
            except Exception: pass
    return out
def ingest(p):
    rows = jl(p); meas = next((r['measure'] for r in rows if 'measure' in r), None)
    files = [r for r in rows if 'Name' in r or ('file' in r and 'cum_total' in r)]
    res = []
    for r in files:
        res.append({'file': r.get('Name') or r.get('file'), 'size': r.get('Size') or r.get('size'), 'how': r.get('How'), 'how_bytes': r.get('HowBytes'),
                    'new_blob_bytes': r.get('NewBlobBytes', r.get('new_blob_bytes')), 'recipe_bytes': r.get('RecipeBytes', r.get('recipe_bytes')),
                    'cum_total': r.get('Total', r.get('cum_total')), 'search_s': round(r['Search'] / 1e9, 2) if 'Search' in r else None})
    return {'files': res, 'measure': meas, 'final_total': res[-1]['cum_total'] if res else None}
def cdc(p):
    rows = [r for r in jl(p) if 'prod_cum' in r]
    return {'raw': sum(r['size'] for r in rows), 'prod': rows[-1]['prod_cum'] if rows else None, 'fine': rows[-1]['fine_cum'] if rows else None}
ev = {}
for fam, pre in (('smol', 'smol'), ('pythia', 'py')):
    d = {}
    for m in ('identity', 'semantic', 'full'):
        d[m] = ingest(f'{w1}/{pre}_fwd_{m}.log' if fam == 'smol' else f'{w1}/py_{m}.log')
    d['cdc'] = cdc(f'{w1}/{pre}_cdc.log')
    ev[fam] = d
ev['smol']['reverse_online'] = ingest(f'{w1}/smol_rev.log')
ev['smol']['reverse_optimized'] = jl(f'{w1}/smol_rev_opt.log')
for k in ('smol_verify_fwd', 'smol_verify_rev_opt', 'py_verify'):
    ev[k] = open(f'{w1}/{k}.log').read().strip().splitlines()[-1:] if os.path.exists(f'{w1}/{k}.log') else None
ev['smol']['stats'] = jl(f'{w1}/smol_stats.log'); ev['pythia']['stats'] = jl(f'{w1}/py_stats.log')
ev['smol']['ranges'] = jl(f'{w1}/smol_ranges.log'); ev['pythia']['ranges'] = jl(f'{w1}/py_ranges.log')
ev['b1_sample'] = {n: jl(f'{w1}/{n}.log') for n in ('smol_b1', 'py_b1')}
g = {'all': ingest(f'{w2}/g_all.log'), 'tar_family': ingest(f'{w2}/g_T.log'), 'git_family': ingest(f'{w2}/g_P.log'), 'cdc': cdc(f'{w2}/g_cdc.log'),
     'verify': open(f'{w2}/g_verify.log').read().strip().splitlines()[-1:], 'ranges': jl(f'{w2}/git_ranges.log')}
ev['git'] = g
c = {}
for prof in ('req', 'opt', 'hard'):
    c[prof] = {'joint': ingest(f'{w2}/c_{prof}.log'), 'arrow_only': ingest(f'{w2}/c_A_{prof}.log'), 'parquet_only': ingest(f'{w2}/c_P_{prof}.log'),
               'cdc': cdc(f'{w2}/c_cdc_{prof}.log'), 'verify': open(f'{w2}/c_verify_{prof}.log').read().strip().splitlines()[-1:]}
c['pq_oracle'] = jl(f'{w2}/pq_oracle.log'); c['pq_values'] = jl(f'{w2}/pq_values.log'); c['ranges'] = jl(f'{w2}/col_ranges.log')
c['manifest'] = json.load(open(f'{w2}/col/manifest.json'))
ev['columnar'] = c
json.dump(ev, sys.stdout, separators=(',', ':'))
