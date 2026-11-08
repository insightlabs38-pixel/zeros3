#!/usr/bin/env python3
"""Maintenance ladder table: median wall seconds per op per rung + scaling exponent between first and last rung.
usage: maint_table.py FILE.jsonl PREFIX   (PREFIX like objs / bytes / hist)"""
import json, re, sys, math, statistics as st
f, pre = sys.argv[1], sys.argv[2]
ops = {}; order = []
for l in open(f):
    d = json.loads(l); m = re.match(rf'({pre}\d+)_(.*)', d['label'])
    if not m or not ('wall_s' in d or 'ready_s' in d): continue
    if d.get('exit_ok') is False: m2 = m.group(2) + '(FAILED)'
    else: m2 = m.group(2)
    ops.setdefault(m2, {}).setdefault(m.group(1), []).append(d.get('wall_s', d.get('ready_s')))
rungs = sorted({r for o in ops.values() for r in o}, key=lambda s: int(s[len(pre):]))
xs = [int(r[len(pre):]) for r in rungs]
print('| op | ' + ' | '.join(rungs) + ' | exponent (last/first) |'); print('|---|' + '---|' * (len(rungs) + 1))
for op, v in ops.items():
    cells = [('%.3f' % st.median(v[r])) if r in v else '-' for r in rungs]
    a, b = st.median(v[rungs[0]]) if rungs[0] in v else None, st.median(v[rungs[-1]]) if rungs[-1] in v else None
    e = '%.2f' % (math.log(b / a) / math.log(xs[-1] / xs[0])) if a and b and a > 0 and xs[-1] != xs[0] else '-'
    print(f'| {op} | ' + ' | '.join(cells) + f' | {e} |')
