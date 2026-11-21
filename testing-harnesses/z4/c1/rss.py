#!/usr/bin/env python3
"""usage: rss.py LABEL OUTFILE CMD...  runs CMD (stdout+stderr to OUTFILE), prints 'LABEL wall=Ns rss=MKB' (peak RSS of the child tree)."""
import resource, subprocess, sys, time
label, out = sys.argv[1], sys.argv[2]
t = time.time()
with open(out, "w") as f:
    rc = subprocess.call(sys.argv[3:], stdout=f, stderr=subprocess.STDOUT)
ru = resource.getrusage(resource.RUSAGE_CHILDREN)
print(f"{label} rc={rc} wall={time.time()-t:.2f}s rss={ru.ru_maxrss}KB")
sys.exit(rc if rc >= 0 else 128 - rc)
