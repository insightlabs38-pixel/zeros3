#!/usr/bin/env python3
"""tables.py RESDIR : markdown tables for the Z4-P3 report from the raw JSONL in RESDIR (maint_base/new, maint_1m, churn_*)."""
import json, sys, collections, statistics, os
d = sys.argv[1]
def load(f):
    g = collections.defaultdict(list)
    p = os.path.join(d, f)
    if not os.path.exists(p):
        return g
    for l in open(p):
        r = json.loads(l); g[r['label']].append(r)
    return g
med = lambda L, k: statistics.median([x[k] if k != 'cpu' else x['ru_user_s'] + x['ru_sys_s'] for x in L])

def maint_table(fixtures, base='maint_base.jsonl', new='maint_new.jsonl'):
    b, n = load(base), load(new)
    print("| fixture | op | base wall s | new wall s | speedup | base CPU s | new CPU s | base RSS MiB | new RSS MiB |")
    print("|---|---|---:|---:|---:|---:|---:|---:|---:|")
    for fx in fixtures:
        for k in b:
            if not k.startswith(fx + '_') or k not in n:
                continue
            op = k[len(fx) + 1:]
            bw, nw = med(b[k], 'wall_s'), med(n[k], 'wall_s')
            print(f"| {fx} | {op} | {bw:.2f} | {nw:.2f} | {bw/nw:.2f}x | {med(b[k],'cpu'):.2f} | {med(n[k],'cpu'):.2f} | {med(b[k],'ru_maxrss_mib'):.0f} | {med(n[k],'ru_maxrss_mib'):.0f} |")

def million():
    g = load('maint_1m.jsonl')
    ops = []
    for k in g:
        op = k.split('_', 2)[2]
        if op not in ops: ops.append(op)
    print("| op (1,000,000 objects) | base wall s | new wall s | speedup | base CPU s | new CPU s | base RSS MiB | new RSS MiB |")
    print("|---|---:|---:|---:|---:|---:|---:|---:|")
    for op in ops:
        b, n = g.get('s1m_base_' + op), g.get('s1m_new_' + op)
        if not b or not n: continue
        print(f"| {op} | {med(b,'wall_s'):.1f} | {med(n,'wall_s'):.1f} | {med(b,'wall_s')/med(n,'wall_s'):.2f}x | {med(b,'cpu'):.0f} | {med(n,'cpu'):.0f} | {med(b,'ru_maxrss_mib'):.0f} | {med(n,'ru_maxrss_mib'):.0f} |")

def churn(f):
    p = os.path.join(d, f)
    if not os.path.exists(p): return
    arms = collections.OrderedDict()
    for l in open(p):
        r = json.loads(l)
        note = r.get('note', '')
        kv = dict(x.split('=') for x in note.split())
        key = (int(kv['dead']), kv['mode'], kv['arm'], kv['bin'])
        a = arms.setdefault(key, {'wall': 0, 'cpu': 0, 'wr': 0, 'before': r['store_before']['Total'], 'after': r['store_after']['Total']})
        a['wall'] += r['wall_s']; a['cpu'] += r['ru_user_s'] + r['ru_sys_s']; a['after'] = r['store_after']['Total']; a['wr'] += r.get('sampled_wchar', 0)
    print("| dead | deletion | arm | store MB before | after | reclaimed MB | written MB | write amp. | base wall s | new wall s | base CPU s | new CPU s |")
    print("|---:|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for (dead, mode, arm, b), a in arms.items():
        if b != 'new': continue
        o = arms.get((dead, mode, arm, 'base'))
        rec = (a['before'] - a['after']) / 1e6
        wr = a['wr'] / 1e6
        wa = f"{wr/rec:.1f}" if rec > 1 else "-"
        print(f"| {dead}% | {mode} | {arm} | {a['before']/1e6:.0f} | {a['after']/1e6:.0f} | {rec:.0f} | {wr:.0f} | {wa} | {o['wall']:.2f} | {a['wall']:.2f} | {o['cpu']:.1f} | {a['cpu']:.1f} |")

if __name__ == '__main__':
    print("## small/mixed fixtures"); maint_table(['s1k', 's10k', 'loose10k', 'hist', 'mix', 's100k'])
    print("\n## 1M"); million()
    print("\n## churn s100kc"); churn('churn_s100kc.jsonl')
