#!/usr/bin/env python3
"""layout_summary.py MATRIX.jsonl [SUITE.json] : which storage layout the root test suite actually exercised (ZEROS3_LAYOUT_MATRIX_OUT)."""
import json, sys, collections
rows = [json.loads(l) for l in open(sys.argv[1])]
passed = set()
if len(sys.argv) > 2:
    for l in open(sys.argv[2]):
        try: e = json.loads(l)
        except Exception: continue
        if e.get('Test') and e['Action'] == 'pass' and '/' not in e['Test']: passed.add(e['Test'])
def cls(r):
    d = set(r.get('declared') or [])
    return 'explicit loose' if d == {'loose'} else ('explicit production' if d == {'production'} else ('loose+production matrix' if d else 'default (production)'))
t = collections.Counter(); k = collections.defaultdict(collections.Counter)
for r in rows:
    c = cls(r); t[c] += 1
    o = set((r.get('observed') or {}))
    for kind in ('segment-records', 'pack', 'loose-chunk', 'manifest-file'):
        if kind in o: k[c][kind] += 1
    if not o: k[c]['(no in-process publication observed: read-only, subprocess or error-path tests)'] += 1
print('root suite: %d top-level tests passed; %d test/subtest names recorded\n' % (len(passed), len(rows)))
print('| declared policy | names | produced segment records | produced packs | produced loose chunks | produced manifest files | nothing observed |')
print('|---|---:|---:|---:|---:|---:|---:|')
for c in ('default (production)', 'explicit production', 'loose+production matrix', 'explicit loose'):
    kk = k[c]
    print('| %s | %d | %d | %d | %d | %d | %d |' % (c, t[c], kk['segment-records'], kk['pack'], kk['loose-chunk'], kk['manifest-file'], kk['(no in-process publication observed: read-only, subprocess or error-path tests)']))
