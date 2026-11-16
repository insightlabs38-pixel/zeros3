#!/usr/bin/env python3
"""Summary of a quantized-collection run (bench_quant_collection.sh output dir). usage: coll_summary.py DIR"""
import json, re, sys
D = sys.argv[1]
cdc = [json.loads(l) for l in open(D + '/cdc.txt') if l.startswith('{"file"')]
logical = sum(r['raw'] for r in cdc); cdc_total = cdc[-1]['prod_cum']
rows = []
for l in open(D + '/derived.enc.txt'):
    m = re.match(r'(\S+)\s+raw\s+(\d+) object\s+(\d+) .*?([\d.]+)s .* qpatch (\d+)/(\d+)', l)
    if m: rows.append((m[1], int(m[2]), int(m[3]), float(m[4]), int(m[5]), int(m[6])))
tot = int(re.search(r'charged=(\d+)', open(D + '/derived.enc.txt').read())[1])
direct = int(re.search(r'charged=(\d+)', open(D + '/direct.enc.txt').read())[1])
fl, bw, qf = rows[:3], rows[3:16], rows[16:]
mb = lambda b: b / 1e6
print(f'files {len(rows)} logical {mb(logical):.1f} MB  CDC {mb(cdc_total):.1f} MB  direct(no bases) {mb(direct):.1f} MB  derived {mb(tot):.2f} MB  ratio {tot/cdc_total:.3f}')
print(f'float roots: base {mb(fl[0][2]):.1f} + instruct {mb(fl[1][2]):.1f} + f16.gguf {mb(fl[2][2]):.2f} MB')
iq3 = [r for r in bw if r[0].startswith('IQ3')]
print(f'bartowski 13 files: {mb(sum(r[2] for r in bw)):.2f} MB (IQ3_S opaque part: {mb(sum(r[2] for r in iq3)):.2f} MB in {len(iq3)} files; others {sum(r[2] for r in bw if r not in iq3)/ (len(bw)-len(iq3)) /1e3:.1f} KB each), raw {mb(sum(r[1] for r in bw)):.0f} MB')
print(f'QuantFactory 14 files: {mb(sum(r[2] for r in qf)):.2f} MB ({sum(r[2] for r in qf)/len(qf)/1e3:.1f} KB each), raw {mb(sum(r[1] for r in qf)):.0f} MB')
print(f'patched blocks {sum(r[4] for r in rows)} of {sum(r[5] for r in rows)}; encode seconds bw {sum(r[3] for r in bw):.0f} qf {sum(r[3] for r in qf):.0f}')
enc = open(D + '/derived.enc.txt').read(); dec = open(D + '/derived.dec.txt').read()
print('encode', re.findall(r'"maxrss_kib":\d+|"user_s":[\d.]+', enc)[-2:], re.findall(r'real\s+\S+', enc)[-1:])
print('replay', re.findall(r'real\s+\S+|user\s+\S+', dec)[-2:], re.findall(r'"maxrss_kib":\d+', dec)[-1:], re.findall(r'verify states.*', dec))
d = {json.loads(l)['sha256']: json.loads(l) for l in dec.splitlines() if l.startswith('{"sha256"')}
q = [v for v in d.values() if v['bytes'] < 200e6]
print(f'quant replay MiB/s min {min(v["mib_s"] for v in q)} median {sorted(v["mib_s"] for v in q)[len(q)//2]} max {max(v["mib_s"] for v in q)}')
try: print(open(D + '/derived.ranges.jsonl').read().strip()); print(open(D + '/derived.store_bytes.txt').read().strip())
except OSError: pass
