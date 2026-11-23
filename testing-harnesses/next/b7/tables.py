#!/usr/bin/env python3
"""B7 tables from the campaign rows. usage: tables.py ROWS.jsonl[.gz] ...
frontier (recall, benefit), graph-level model comparison, lanes (allocated bytes, cost), paired reads
(geometric mean of per-read wall ratios), concurrency, density, chunking, scale."""
import gzip, json, math, sys
from collections import defaultdict

rows = []
for p in sys.argv[1:]:
    op = gzip.open if p.endswith('.gz') else open
    with op(p, 'rt') as f:
        rows += [json.loads(l) for l in f if l.strip()]
by = defaultdict(list)
for r in rows:
    by[r['kind']].append(r)
FAMS = ['cu-files', 'go-files', 'vendor', 'bundle', 'coreutils', 'go', 'sqlite', 'tensors', 'git', 'ts', 'random', 'unrelated']

def gm(x):
    x = [v for v in x if v > 0]
    return math.exp(sum(map(math.log, x)) / len(x)) if x else float('nan')

def forder(rs):
    return sorted(rs, key=lambda r: FAMS.index(r['family']) if r['family'] in FAMS else 99)

if by['frontier']:
    print('## Discovery frontier (estimate level; oracle = exhaustive pairwise matching)')
    print('| family | targets | useful | variant | recall@1 | recall@2 | recall@4 | recall@8 | benefit@1 | benefit@4 | false pos top-4 | index KiB | rank+verify s | oracle pair extra | multi est. extra |')
    print('|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
    for r in forder(by['frontier']):
        for d, x in r.items():
            if not isinstance(x, dict) or 'recall@1' not in x:
                continue
            st = x['stats']
            extra = r['b6_refine_extra'] if d == 'sketch' else r['b7_multi_extra'] if d == 'region' else ''
            print(f"| {r['family']} | {r['targets']} | {r['useful_targets']} | {d} | {x['recall@1']:.3f} | {x['recall@2']:.3f} | {x['recall@4']:.3f} | {x['recall@8']:.3f} | "
                  f"{x['benefit@1']:.4f} | {x['benefit@4']:.4f} | {x['false_pos_top4']:.3f} | {st['index_bytes']>>10} | {st['rank_seconds']:.1f} | "
                  f"{r['oracle_pair_extra']/max(1,r['oracle_single_saving']):.4f} | {'' if extra == '' else f"{extra/max(1, r['oracle_single_saving']):.4f}"} |")

for kind, title in [('graph', 'Graph level, archive, model only (oracle candidate sets; locality {0})'), ('graph-alg', 'Graph level, model only, B6 search vs B7 discovery')]:
    if not by[kind]:
        continue
    g = defaultdict(dict)
    for r in by[kind]:
        g[r['family']][r['config']] = r
    cfgs = []
    for r in by[kind]:
        if r['config'] not in cfgs:
            cfgs.append(r['config'])
    print(f'\n## {title}: planned closure bytes (ratio to b6), deltas')
    print('| family | ' + ' | '.join(cfgs) + ' |')
    print('|---|' + '---:|' * len(cfgs))
    for f in [f for f in FAMS if f in g]:
        b = g[f].get('b6', {}).get('planned', {}).get('bytes')
        cells = []
        for c in cfgs:
            r = g[f].get(c)
            if not r:
                cells.append('')
                continue
            p = r['planned']
            cells.append(f"{p['bytes']/1e6:.3f} MB ({p['bytes']/b:.4f}) {p['deltas']}d" if b else f"{p['bytes']/1e6:.3f}")
        print(f'| {f} | ' + ' | '.join(cells) + ' |')

lanes = defaultdict(dict)
for r in by['lane']:
    lanes[r['family']][r['lane']] = r
LORDER = ['cdc', 'b6-archive', 'b7-archive', 'b6-mixed', 'b7-mixed']
if lanes:
    print('\n## Materialized storage (allocated bytes after gc + repack + compact)')
    print('| family | cdc | b6 archive | b7 archive | b7/b6 | b6 mixed | b7 mixed | b7/b6 | verify |')
    print('|---|---:|---:|---:|---:|---:|---:|---:|---|')
    for f in [f for f in FAMS if f in lanes]:
        L = lanes[f]
        a = lambda l: L.get(l, {}).get('allocated')
        cell = lambda l: f"{a(l)/1e6:.2f}" if a(l) else ''
        rat = lambda x, y: f"{a(x)/a(y):.3f}" if a(x) and a(y) else ''
        ok = all(L[l].get('verify_ok') for l in L)
        print(f"| {f} | {cell('cdc')} | {cell('b6-archive')} | {cell('b7-archive')} | {rat('b7-archive','b6-archive')} | {cell('b6-mixed')} | {cell('b7-mixed')} | {rat('b7-mixed','b6-mixed')} | {ok} |")
    print('\n## Plan vs materialized, migration cost')
    print('| family | lane | planned | model of materialized | equal | deltas | multi-base | wall s | CPU s | written MB | peak alloc MB | RSS MiB | candidates s | discovery s (extract/rank/verify) | encodes |')
    print('|---|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|')
    for f in [f for f in FAMS if f in lanes]:
        for l in LORDER[1:]:
            r = lanes[f].get(l)
            if not r or not isinstance(r.get('report'), dict):
                continue
            rep = r['report']
            p = rep['planned']['bytes']
            mm = r.get('materialized_model', {}).get('bytes', 0)
            multi = sum(1 for c in rep.get('changes') or [] if len(c.get('bases') or []) > 1)
            d = rep.get('discovery') or {}
            ds = f"{d.get('extract_seconds',0):.1f}/{d.get('rank_seconds',0):.1f}/{d.get('verify_seconds',0):.1f}"
            print(f"| {f} | {l} | {p:,} | {mm:,} | {p == mm} | {rep['planned']['deltas']} | {multi} | {r['migrate_wall_s']:.1f} | {r['migrate_cpu_s']:.1f} | "
                  f"{r['migrate_written']/1e6:.1f} | {r['peak_allocated']/1e6:.1f} | {r['migrate_rss_mib']:.0f} | {rep.get('candidate_seconds',0):.1f} | {ds} | {rep.get('encodes',0)} |")

if by['read']:
    print('\n## Paired cold reads: geometric mean of per-read wall ratios (n pairs)')
    reads = defaultdict(dict)
    for r in by['read']:
        k = (r['family'], r['row']['Mode'], r['sha'], r['row']['Off'], r['row']['Len'])
        reads[k][r['lane']] = r['row']['WallUS']
    pairs = [('b7-archive', 'b6-archive'), ('b7-mixed', 'b6-mixed'), ('b7-archive', 'cdc'), ('b7-mixed', 'cdc')]
    modes = sorted({k[1] for k in reads})
    print('| family | pair | ' + ' | '.join(modes) + ' |')
    print('|---|---|' + '---:|' * len(modes))
    for f in [f for f in FAMS if any(k[0] == f for k in reads)]:
        for a, b in pairs:
            cells = []
            for m in modes:
                rs = [v[a] / v[b] for k, v in reads.items() if k[0] == f and k[1] == m and a in v and b in v and v[b] > 0]
                cells.append(f"{gm(rs):.2f} ({len(rs)})" if rs else '')
            if any(cells):
                print(f'| {f} | {a}/{b} | ' + ' | '.join(cells) + ' |')

if by['read']:
    print('\n## Physical work per cold read (medians: device read requests, decoded bytes modeled)')
    print('| family | lane | 4 KiB device reads | 4 KiB decoded KB | 64 KiB device reads | whole ms |')
    print('|---|---|---:|---:|---:|---:|')
    med = lambda x: sorted(x)[len(x) // 2] if x else float('nan')
    cells = defaultdict(lambda: defaultdict(list))
    for r in by['read']:
        cells[(r['family'], r['lane'])][r['row']['Mode']].append(r)
    for (f, l), m in sorted(cells.items(), key=lambda kv: (FAMS.index(kv[0][0]) if kv[0][0] in FAMS else 99, kv[0][1])):
        c4, c64, wh = m.get('cold4k', []), m.get('cold64k', []), m.get('whole', [])
        print(f"| {f} | {l} | {med([r['row']['DiskReads'] for r in c4])} | {med([r['model_decoded'] for r in c4])/1e3:.0f} | "
              f"{med([r['row']['DiskReads'] for r in c64])} | {med([r['row']['WallUS'] for r in wh])/1e3:.1f} |")

if by['concurrent']:
    print('\n## Concurrent readers (8 closed-loop, 75% 4 KiB / 25% 64 KiB, cold start; both lane orders)')
    print('| family | lane | order | reads/s | p50 µs | p95 µs | p99 µs | CPU s |')
    print('|---|---|---|---:|---:|---:|---:|---:|')
    for r in forder(by['concurrent']):
        print(f"| {r['family']} | {r['lane']} | {r['order']} first | {r['reads']/r['seconds']:,.0f} | {r['p50_us']} | {r['p95_us']} | {r['p99_us']} | {r['cpu_s']:.1f} |")

if by['density']:
    print('\n## COPY-anchor density on large bases (packed envelope bytes, best of locality 0/256)')
    print('| family | pair | anchors | build s | loc0 MB | loc256 MB | best |')
    print('|---|---:|---:|---:|---:|---:|---:|')
    for r in forder(by['density']):
        b = min(r['loc0']['phys'], r['loc256']['phys'])
        print(f"| {r['family']} | {r['pair']} | {r['anchors']:,} | {r['build_s']:.2f} | {r['loc0']['phys']/1e6:.3f} | {r['loc256']['phys']/1e6:.3f} | {b/1e6:.3f} |")

if by['chunking']:
    print('\n## Chunk boundaries as similarity features: bytes of version i+1 found in version i')
    print('| family | pair | matcher copied | CDC v1 (64 KiB) | gear 1 KiB | FastCDC 1 KiB | windows (est.) | mean / CV gear | mean / CV FastCDC | ns/B CDC v1 / FastCDC / windows |')
    print('|---|---:|---:|---:|---:|---:|---:|---|---|---|')
    for r in forder(by['chunking']):
        T = r['target_bytes']
        s = lambda k: f"{r[k]['stable']/T:.3f}"
        print(f"| {r['family']} | {r['pair']} | {r['matcher_copied']/T:.3f} | {s('cdcv1')} | {s('gear')} | {s('fastcdc')} | {r['windows']['stable_est']/T:.3f} | "
              f"{r['gear']['mean']:.0f} / {r['gear']['cv']:.2f} | {r['fastcdc']['mean']:.0f} / {r['fastcdc']['cv']:.2f} | "
              f"{r['cdcv1']['ns_per_byte']:.2f} / {r['fastcdc']['ns_per_byte']:.2f} / {r['windows']['ns_per_byte']:.2f} |")

if by['scale']:
    print('\n## Retrieval at scale (synthetic, 30% shared boilerplate, 3 versions per file)')
    print('| contents | sketch build s | sketch rank s | sketch recall@4 | region build s | region rank s | region recall@4 | region index MiB |')
    print('|---:|---:|---:|---:|---:|---:|---:|---:|')
    for r in by['scale']:
        a, b = r['sketch'], r['region']
        print(f"| {r['contents']} | {a['build_s']:.2f} | {a['rank_s']:.2f} | {a['recall_prev@4']:.3f} | {b['build_s']:.2f} | {b['rank_s']:.2f} | {b['recall_prev@4']:.3f} | {b['index_bytes']/2**20:.1f} |")
