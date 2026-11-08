#!/usr/bin/env python3
"""Median wall/CPU/RSS per research label. usage: research_table.py results/research_b4.jsonl"""
import json, sys, collections, statistics as st
g = collections.OrderedDict()
for l in open(sys.argv[1]):
    if l.strip():
        r = json.loads(l); g.setdefault(r['label'], []).append(r)
print('| label | n | ok | wall s (min-max) | user s | sys s | peak RSS MiB | dev read MiB | dev write MiB | note |'); print('|' + '---|' * 10)
for k, v in g.items():
    w = [x['wall_s'] for x in v]
    print(f"| {k} | {len(v)} | {'Y' if all(x['exit_ok'] for x in v) else '**N**'} | {st.median(w):.2f} ({min(w):.2f}-{max(w):.2f}) | {st.median([x['ru_user_s'] for x in v]):.1f} | {st.median([x['ru_sys_s'] for x in v]):.1f} | {st.median([x['ru_maxrss_mib'] for x in v]):.0f} | {st.median([x['dev_read_bytes'] for x in v])/2**20:.0f} | {st.median([x['dev_write_bytes'] for x in v])/2**20:.0f} | {'conc' if v[0].get('note') else 'quiet'} |")
