#!/usr/bin/env python3
"""Fresh-process replay proof: run `z4b3 replay` on STORE, then independently hash every output and every original.
usage: verify_replay.py BINARY STORE OUTDIR ORIGINAL..."""
import hashlib, json, os, subprocess, sys
binary, store, out, files = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
os.makedirs(out, exist_ok=True)
subprocess.run([binary, 'replay', '-store', store, '-out', out], check=True, stdout=subprocess.DEVNULL)
def sha(p):
    h = hashlib.sha256()
    with open(p, 'rb') as f:
        while (b := f.read(1 << 22)):
            h.update(b)
    return h.hexdigest()
ok = bad = missing = 0
for f in files:
    s = sha(f)
    o = os.path.join(out, s)
    if not os.path.exists(o):
        missing += 1; print('MISSING', f); continue
    if sha(o) == s and os.path.getsize(o) == os.path.getsize(f):
        ok += 1
    else:
        bad += 1; print('DIFF', f)
print(json.dumps({'originals': len(files), 'exact': ok, 'diff': bad, 'missing': missing}))
sys.exit(1 if bad or missing else 0)
