#!/usr/bin/env python3
"""Markdown tables for the Z4-P5 report from the raw JSONL in ../results. usage: report_tables.py RESULTS_DIR"""
import json, sys, collections, os
R = sys.argv[1]
def rows(name):
    p = os.path.join(R, name)
    return [json.loads(l) for l in open(p)] if os.path.exists(p) else []

def match_table():
    rs = rows('match_quality.jsonl')
    fam = collections.OrderedDict()
    for r in rs: fam.setdefault(r['family'], collections.OrderedDict()).setdefault(r['matcher'], []).append(r)
    print('| family | matcher | envelope bytes | vs best B1 | literal MiB | MiB/s (target, incl. index build) | speedup vs same-family B1 adaptive |')
    print('|---|---|---:|---:|---:|---:|---:|')
    for f, ms in fam.items():
        b1 = {m: sum(x['envelope'] for x in v) for m, v in ms.items() if m.startswith('b1-')}
        best = min(b1.values()); ad = ms['b1-adaptive']
        adsec = sum(x['sec'] for x in ad)
        for m, v in ms.items():
            env = sum(x['envelope'] for x in v); sec = sum(x['sec'] for x in v); tb = sum(x['target'] for x in v)
            print(f"| {f} | {m} | {env} | {env/best:.3f} | {sum(x['lit_bytes'] for x in v)/2**20:.2f} | {tb/sec/2**20:.0f} | {adsec/sec:.1f}x |")

def select_table():
    rs = rows('select.jsonl')
    agg = collections.defaultdict(lambda: collections.defaultdict(float))
    for r in rs:
        a = agg[(r['family'], r['policy'])]
        a['n'] += 1; a['sec'] += r['sec']; a['ex'] += r['exact_encoded']; a['c'] += r['candidates']; a['w'] += r['wrong_choice']
        a['size'] += r['size']; a['orc'] += r['oracle_size']; a['mx'] = max(a['mx'], r['regret'])
    print('| family | policy | targets | wall s | exact encodings | wrong choice | byte regret (total / worst target) |')
    print('|---|---|---:|---:|---:|---:|---:|')
    for (f, p), a in sorted(agg.items()):
        print(f"| {f} | {p} | {int(a['n'])} | {a['sec']:.1f} | {int(a['ex'])}/{int(a['c'])} | {int(a['w'])} | {a['size']/a['orc']-1:+.4f} / {a['mx']:.4f} |")

def b2_table(*names):
    cdc = {}
    allr = []
    for n in names: allr += rows(n)
    for r in allr:
        if r.get('scenario') == 'cdc' and 'phys' in r: cdc[r['family']] = r['phys']
    print('| family | variant | physical MB | x CDC | migrate s | migrate RSS MiB | peak x CDC | forms |')
    print('|---|---|---:|---:|---:|---:|---:|---|')
    for r in allr:
        if 'phys' in r and r['scenario'] != 'cdc' and r.get('migrate_s') is not None:
            c = cdc[r['family']]; pk = r.get('peak_during_migration')
            print(f"| {r['family']} | {r['scenario']} | {r['phys']/1e6:.2f} | {r['phys']/c:.3f} | {r['migrate_s']:.1f} | {r.get('migrate_rss_mib',0):.0f} | {(pk/c if pk else 0):.2f} | {r.get('forms','')} |")

if __name__ == '__main__':
    print('## match'); match_table(); print(); print('## select'); select_table(); print(); print('## b2'); b2_table('b2_draft_baseline.jsonl', 'b2_p5_ab.jsonl', 'b2_p5_ab2.jsonl', 'b2_p5_ab2_snappy.jsonl')
