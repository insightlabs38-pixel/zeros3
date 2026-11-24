"""Print the B9 tables from raw rows: python3 tables.py results/*.jsonl results/*.jsonl.gz"""
import collections, gzip, json, statistics as st, sys

rows = []
for p in sys.argv[1:]:
    op = gzip.open if p.endswith('.gz') else open
    with op(p, 'rt') as f:
        rows += [json.loads(l) for l in f if l.strip()]
lanes, recs = {}, {}
for r in rows:
    if r.get('kind') == 'lane':
        lanes[(r['family'], r['lane'])] = r  # the last row of a lane wins (reruns)
    if r.get('kind') == 'records':
        recs[(r['family'], r['lane'])] = r
MB = lambda b: '%.2f' % (b / 1e6) if b is not None else ''
pl = lambda r: ((r or {}).get('report') or {}).get('planned', {}).get('bytes')
mm = lambda r: ((r or {}).get('materialized_model') or {}).get('bytes')

print('## A. Small-file storage (MB): model units (plan, materialized) and allocated store bytes\n')
print('| family | profile | b8 plan | b8m model | b9 materialized | modeled | realized | survives | b8 alloc | b9 alloc | alloc change | deltas b8/b9 | B/record b8/b9 | verify |')
print('|---|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---|---|---|')
for f in ['cu-files', 'go-files', 'synth', 'rnd-files']:
    for prof in ['archive', 'mixed']:
        b8, m, b9 = (lanes.get((f, x + '-' + prof)) for x in ('b8', 'b8m', 'b9'))
        if not (b8 and b9):
            continue
        base = mm(b8)
        mod, rea = (1 - pl(m) / base) if m and base else 0, (1 - mm(b9) / base) if base else 0
        surv = 'n/a' if mod < 0.005 else ('yes' if rea >= 0.75 * mod else 'no')
        d8, d9 = (((x.get('report') or {}).get('planned') or {}).get('deltas') for x in (b8, b9))
        r8, r9 = recs.get((f, 'b8-' + prof), {}), recs.get((f, 'b9-' + prof), {})
        bpr = '/'.join('%.0f' % x['alloc_per_record'] if x.get('alloc_per_record') else '-' for x in (r8, r9))
        ok = all(x.get('verify_ok') for x in (b8, b9)) and pl(b9) == mm(b9)
        print(f'| {f} | {prof} | {MB(base)} | {MB(pl(m))} | {MB(mm(b9))} | {-mod:+.1%} | {-rea:+.1%} | {surv} | '
              f'{MB(b8["allocated"])} | {MB(b9["allocated"])} | {b9["allocated"] / b8["allocated"] - 1:+.1%} | {d8}/{d9} | {bpr} | {"ok" if ok else "FAIL"} |')

print('\n## A. Record store (b8 record files vs b9 groups; lookups: page cache dropped, index kept / rebuilt)\n')
print('| family | lane | records | record bytes | file alloc | groups | group alloc | cold us p50 | open us p50 | warm us p50 | rebuild ms |')
print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
for (f, l), r in sorted(recs.items()):
    if r.get('records'):
        print(f'| {f} | {l} | {r["records"]} | {r["record_bytes"]} | {r["file_alloc"]} | {r["groups"]} | {r["group_alloc"]} | '
              f'{r.get("lookup_cold_us_p50", "")} | {r.get("lookup_open_us_p50", "")} | {r.get("lookup_warm_us_p50", "")} | {r["index_rebuild_cold_ms"]:.2f} |')
for r in rows:
    if r.get('kind') == 'append':
        print(f'\nappend grouped={r["grouped"]}: {r["publish_per_s"]:.0f} publishes/s (p50 {r["publish_p50_ms"]:.2f} ms, p99 {r["publish_p99_ms"]:.2f}), '
              f'{r["written"]} B written, bodies {r["bodies_alloc"]} B allocated, retire {r["retire_s"]:.3f} s, compact {r["compact_s"]:.3f} s')

print('\n## Allocated MB (B: executables, C stores)\n')
print('| family | cdc | b8c | b9n | b9n / b8c | verify |')
print('|---|---:|---:|---:|---:|---|')
for f in ['elf', 'pe', 'ts', 'django']:
    c, b, n = (lanes.get((f, x)) for x in ('cdc', 'b8c-archive', 'b9n-archive'))
    if b and n:
        print(f'| {f} | {MB(c["allocated"]) if c else ""} | {MB(b["allocated"])} | {MB(n["allocated"])} | {n["allocated"] / b["allocated"]:.3f} | '
              f'{"ok" if b.get("verify_ok") and n.get("verify_ok") else "FAIL"} |')

reads = collections.defaultdict(list)
for r in rows:
    if r.get('kind') == 'read':
        reads[(r['family'], r['lane'], r['row']['Mode'])].append(r['row'])
if reads:
    print('\n## Reads (median wall ms / CPU ms, every read checked byte for byte)\n')
    modes = ['cold4k', 'cold64k', 'cold1m', 'whole']
    print('| family | lane | ' + ' | '.join(modes) + ' |')
    print('|---|---|' + '---:|' * len(modes))
    for f, l in sorted({(f, l) for f, l, _ in reads}):
        cells = []
        for m in modes:
            xs = reads.get((f, l, m))
            cells.append('%.2f / %.2f' % (st.median(x['WallUS'] for x in xs) / 1e3, st.median(x['CPUUS'] for x in xs) / 1e3) if xs else '')
        print(f'| {f} | {l} | ' + ' | '.join(cells) + ' |')

conc = [r for r in rows if r.get('kind') == 'concurrent']
if conc:
    print('\n## Concurrent readers (8, 10 s)\n\n| family | lane | order | reads/s | p50 us | p99 us | CPU s |\n|---|---|---|---:|---:|---:|---:|')
    for r in conc:
        print(f'| {r["family"]} | {r["lane"]} | {r["order"]} | {r["reads"] / r["seconds"]:.0f} | {r["p50_us"]} | {r["p99_us"]} | {r["cpu_s"]:.1f} |')

cache = [r for r in rows if r.get('kind') == 'cache']
if cache:
    print('\n## C. Decoded-window cache: off vs 64 MiB (mean of two orders); sweep at 8 readers (Zipf)\n')
    print('| family | admission | window KiB | workload | req/s off | req/s on | on/off | p50 ms off/on | p99 ms off/on | hit rate | cache MB |')
    print('|---|---|---:|---|---:|---:|---:|---|---|---:|---:|')
    g = collections.defaultdict(list)
    for r in cache:
        g[(r['family'], r.get('admission', 'first-touch'), r['window_kib'], r['workload'], r['budget_mib'])].append(r)
    m = lambda xs, k: st.mean(x[k] for x in xs)
    for (f, adm, w, wl, b), on in g.items():
        off = g.get((f, adm, w, wl, 0))
        if b != 64 or not off:
            continue
        print(f'| {f} | {adm} | {w} | {wl} | {m(off, "rps"):.0f} | {m(on, "rps"):.0f} | {m(on, "rps") / m(off, "rps"):.2f} | '
              f'{m(off, "p50_ms"):.2f}/{m(on, "p50_ms"):.2f} | {m(off, "p99_ms"):.0f}/{m(on, "p99_ms"):.0f} | {m(on, "hit_rate") if "hit_rate" in on[0] else 0:.2f} | {m(on, "cache_bytes") / 1e6:.1f} |')
    for (f, adm, w, wl, b), xs in g.items():
        if wl == 'zipf-8' and b in (16, 256):
            print(f'sweep {f} {adm} window {w} KiB budget {b} MiB: {m(xs, "rps"):.0f} req/s, hit rate {m(xs, "hit_rate") if "hit_rate" in xs[0] else 0:.2f}')
