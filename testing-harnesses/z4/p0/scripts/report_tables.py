#!/usr/bin/env python3
"""Regenerates results/P0_TABLES.md from the committed JSONL (deterministic; no new measurements)."""
import json, subprocess, sys, os, statistics as st, collections, re
R = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'results')
S = os.path.dirname(os.path.abspath(__file__))
def run(*a):
    return subprocess.run([sys.executable, *a], capture_output=True, text=True, cwd=S).stdout
out = []
def sec(t, body): out.append(f'## {t}\n\n{body}\n')
sec('PUT matrix (put.jsonl)', run('summarize.py', f'{R}/put.jsonl'))
sec('GET / range matrix (get.jsonl)', run('summarize.py', f'{R}/get.jsonl'))
if os.path.exists(f'{R}/mixed.jsonl'): sec('Mixed GET+PUT (mixed.jsonl)', run('summarize.py', f'{R}/mixed.jsonl'))
sec('Read latency vs object size (scale_range.jsonl)', run('summarize.py', f'{R}/scale_range.jsonl'))
for pre, f in [('objs', 'maint_objs.jsonl'), ('bytes', 'maint_bytes.jsonl'), ('hist', 'maint_hist.jsonl')]:
    if os.path.exists(f'{R}/{f}'): sec(f'Maintenance ladder: {pre} (median wall s)', run('maint_table.py', f'{R}/{f}', pre))
if os.path.exists(f'{R}/reps.jsonl'): sec('Production representations (reps.jsonl)', run('reps_table.py', f'{R}/reps.jsonl'))
open(f'{R}/P0_TABLES.md', 'w').write('# P0 generated tables (do not edit; regenerate with scripts/report_tables.py)\n\n' + '\n'.join(out))
print('wrote', f'{R}/P0_TABLES.md')
