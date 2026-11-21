"""Medians of measurements.jsonl.gz by label, lane, key and mode: wall and CPU ms, logical CAS, plaintext, units."""
import gzip, json, statistics, sys
from collections import defaultdict

rows = defaultdict(list)
for line in gzip.open(sys.argv[1] if len(sys.argv) > 1 else 'measurements.jsonl.gz', 'rt'):
    r = json.loads(line)
    rows[(r['Label'], r['Lane'], r['Key'], r['Form'], r['Mode'])].append(r)
print('label lane key form mode n wall_ms cpu_ms cas_kb plain_kb units')
for k, v in sorted(rows.items()):
    m = lambda f: statistics.median(f(r) for r in v)
    print(*k, len(v), round(m(lambda r: r['WallUS']) / 1e3, 1), round(m(lambda r: r['CPUUS']) / 1e3, 1),
          round(m(lambda r: r['CASBytes']) / 1e3), round(m(lambda r: r['Codec']['PlainBytes']) / 1e3), m(lambda r: r['Codec']['Units']))
