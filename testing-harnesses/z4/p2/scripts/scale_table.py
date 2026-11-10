#!/usr/bin/env python3
"""scale_table.py FILE.jsonl : cache-budget x store-size table from scale_p2.sh output."""
import json, collections, statistics as st, sys
rows = collections.defaultdict(list); stats = {}
for l in open(sys.argv[1]):
    r = json.loads(l)
    if r.get('scenario') == 'cache_stats':
        stats[r['note']] = r['stats'][-1]; continue
    if 'lat' not in r or not r['label'].startswith('scale_'): continue
    rows[r['note']].append(r)
print('| store | caches (manifest/chunk MiB) | ops/s | p50 ms | p99 ms | CPU us/req | RSS MiB | manifest entries | manifest MiB | manifest hit | evictions | chunk hit |')
print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|')
for note, rs in rows.items():
    s = stats.get(note, {}); m = s.get('manifest', {}); c = s.get('chunk', {})
    f = dict(x.split('=') for x in note.split())
    hr = m.get('Hits', 0) / max(1, m.get('Hits', 0) + m.get('Misses', 0))
    ch = c.get('Hits', 0) / max(1, c.get('Hits', 0) + c.get('Misses', 0))
    n = rs[0]['params'].get('nreq', 20000) if 'params' in rs[0] else 20000
    print('| %s x %s | %s/%s | %.0f | %.3f | %.2f | %.0f | %.0f | %d | %.1f | %.2f | %d | %.2f |' % (
        f['objs'], f['size'], f['mcache'], f['ccache'], st.median(20000 / r['wall_s'] for r in rs), st.median(r['lat']['p50_ms'] for r in rs),
        st.median(r['lat']['p99_ms'] for r in rs), 1e6 * st.median(r['srv_cpu_s'] for r in rs) / 20000, max(r['srv_hwm_mib'] for r in rs),
        m.get('Entries', 0), m.get('Bytes', 0) / 1048576, hr, m.get('Evictions', 0), ch))
