#!/usr/bin/env python3
"""summarize.py FILE... : per (scenario, variant) median [min-max] MiB/s, CPU-s/GiB, cores, peak RSS, p50 latency."""
import json, collections, statistics as st, sys
d = collections.defaultdict(list)
for f in sys.argv[1:]:
    for l in open(f):
        r = json.loads(l)
        v = r.get('note', 'variant=?').split()[0].split('=')[1]
        d[(r['label'], v)].append((r['mib_s'], r['srv_cpu_s'] / (r['logical_bytes'] / 2**30), r['srv_hwm_mib'], r['srv_cpu_cores'], r['lat']['p50_ms'], r.get('verified')))
for (lab, v), x in sorted(d.items()):
    print('%-22s %-14s MiB/s %6.1f [%6.1f-%6.1f]  cpu-s/GiB %6.2f  cores %.2f  hwm %3.0f  p50 %7.1f ms  n=%d verified=%s' % (
        lab, v, st.median(a[0] for a in x), min(a[0] for a in x), max(a[0] for a in x), st.median(a[1] for a in x),
        st.median(a[3] for a in x), max(a[2] for a in x), st.median(a[4] for a in x), len(x), all(a[5] for a in x)))
