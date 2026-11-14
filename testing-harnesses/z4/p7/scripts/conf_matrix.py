#!/usr/bin/env python3
"""Producer-conformance matrix from quantconf JSONL rows. usage: conf_matrix.py FILE.jsonl..."""
import collections, json, sys
agg = collections.OrderedDict()
for p in sys.argv[1:]:
    for l in open(p):
        r = json.loads(l)
        d, f = r['file'].split('/')
        prod = d if d in ('bw', 'qf') else d + ':' + f.split('_')[0]
        a = agg.setdefault((prod, r['type'], r['variant']), [set(), 0, 0, 0, 0, 0, 0])
        a[0].add(r['file'])
        for i, k in enumerate(('tensors', 'exact_tensors', 'blocks', 'exact_blocks', 'bytes', 'unported')): a[i + 1] += r.get(k, 0)
print('|producer|type|variant|files|tensors exact/all|blocks exact/all|%|MB|')
print('|---|---|---|---|---|---|---|---|')
for (prod, typ, var), a in sorted(agg.items()):
    if var == 'unported':
        print(f'|{prod}|{typ}|not ported|{len(a[0])}|0/{a[6]}|-|-|{a[5]/1e6:.1f}|'); continue
    print(f'|{prod}|{typ}|{var}|{len(a[0])}|{a[2]}/{a[1]}|{a[4]}/{a[3]}|{100*a[4]/max(a[3],1):.4f}|{a[5]/1e6:.1f}|')
