#!/usr/bin/env python3
"""Independent fresh-process proof: hash every replayed output and every original with Python (no Go).
usage: verify_replay.py STORE OUTDIR   (STORE/manifest.json lists original paths, sha256, sizes). Exit 1 on any mismatch."""
import hashlib, json, os, sys
store, out = sys.argv[1], sys.argv[2]
man = json.load(open(os.path.join(store, 'manifest.json')))
def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        while (b := f.read(1 << 22)):
            h.update(b)
    return h.hexdigest()
ok = bad = 0
for e in man:
    o = os.path.join(out, e['sha256'])
    if not os.path.exists(o) or os.path.getsize(o) != e['size'] or sha(o) != sha(e['path']) or sha(o) != e['sha256']:
        bad += 1; print('MISMATCH', e['path'])
    else:
        ok += 1
print(json.dumps({'store': store, 'states': len(man), 'exact': ok, 'bad': bad}))
sys.exit(1 if bad else 0)
