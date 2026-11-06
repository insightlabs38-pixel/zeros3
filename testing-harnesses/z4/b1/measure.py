#!/usr/bin/env python3
"""One child process per invocation; RSS is the OS child high-water mark."""
import json,resource,subprocess,sys,time
start=time.monotonic();p=subprocess.run(sys.argv[1:]);r=resource.getrusage(resource.RUSAGE_CHILDREN)
print(json.dumps(dict(command=sys.argv[1:],exit=p.returncode,seconds=time.monotonic()-start,peak_rss_kib=r.ru_maxrss,user_seconds=r.ru_utime,system_seconds=r.ru_stime)),file=sys.stderr);sys.exit(p.returncode)
