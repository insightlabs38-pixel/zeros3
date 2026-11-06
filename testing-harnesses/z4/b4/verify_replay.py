#!/usr/bin/env python3
"""Independent check: usage verify_replay.py STORE_DIR REPLAY_DIR ORIGINALS_DIR
Hashes every replayed revision with hashlib and compares it with the MANIFEST (written at
store time) and with the original file's SHA-256. Does not import any Go code."""
import hashlib, os, sys
store, out, orig = sys.argv[1:4]
ok = True
for line in open(os.path.join(store, 'MANIFEST')):
    rev, sha, size, name = line.split()
    got = hashlib.sha256(open(os.path.join(out, rev), 'rb').read()).hexdigest()
    src = hashlib.sha256(open(os.path.join(orig, name), 'rb').read()).hexdigest()
    good = got == sha == src and os.path.getsize(os.path.join(out, rev)) == int(size)
    ok &= good
    print(rev, name, 'MATCH' if good else 'MISMATCH')
sys.exit(0 if ok else 1)
