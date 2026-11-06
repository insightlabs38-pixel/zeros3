#!/usr/bin/env python3
"""Run a command as an isolated child; print its stdout and a final JSON line with wall time and peak RSS."""
import json, resource, subprocess, sys, time
t = time.time()
p = subprocess.run(sys.argv[1:], stdout=sys.stdout, stderr=sys.stderr)
ru = resource.getrusage(resource.RUSAGE_CHILDREN)
print(json.dumps({"measure": {"wall_s": round(time.time() - t, 3), "max_rss_mib": round(ru.ru_maxrss / 1024, 1),
      "user_s": round(ru.ru_utime, 2), "sys_s": round(ru.ru_stime, 2), "rc": p.returncode}}))
sys.exit(p.returncode)
