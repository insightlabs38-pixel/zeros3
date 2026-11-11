#!/usr/bin/env python3 -I
"""Independent exactness check: replay a store in a FRESH process (no original files readable by
the replayer by construction: it only gets the store directory), then hash every replayed output
and every original with hashlib and compare. usage: verify_replay.py BIN STORE OUTDIR FILES...
Prints one JSON line per original and a final summary; exit 1 on any mismatch."""
import hashlib, json, os, resource, shutil, subprocess, sys, time

def sha(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        while True:
            b = f.read(1 << 22)
            if not b:
                break
            h.update(b)
    return h.hexdigest()

binary, store, out, files = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
shutil.rmtree(out, ignore_errors=True)
t = time.time()
p = subprocess.run([binary, 'replay', '-store', store, '-out', out], capture_output=True, text=True)
ru = resource.getrusage(resource.RUSAGE_CHILDREN)
wall = time.time() - t
if p.returncode != 0:
    print(json.dumps({'replay_failed': p.stderr[-500:]})); sys.exit(1)
bad = 0
for f in files:
    want = sha(f)
    got_path = os.path.join(out, want)
    ok = os.path.exists(got_path) and sha(got_path) == want and os.path.getsize(got_path) == os.path.getsize(f)
    bad += not ok
    print(json.dumps({'file': os.path.basename(f), 'sha256': want, 'exact': ok}))
print(json.dumps({'summary': {'files': len(files), 'mismatches': bad, 'replay_wall_s': round(wall, 2),
                              'replay_max_rss_mib': round(ru.ru_maxrss / 1024, 1)}}))
shutil.rmtree(out, ignore_errors=True)
sys.exit(1 if bad else 0)
