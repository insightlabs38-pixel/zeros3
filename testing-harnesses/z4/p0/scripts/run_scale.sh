#!/bin/sh
# Scaling probes: read latency vs object size (chunk count) on the real server.
. "$(dirname "$0")/lib.sh"
F=scale_range.jsonl
for s in 1mib 4mib 16mib 64mib 256mib 1gib; do
  getrun scale_range4k_$s $F -objs 1 -size $s -mode range -rsize 4kib -nreq 3000 -conc 1 -cache warm -reps 3
  getrun scale_range4k_loose_$s $F -objs 1 -size $s -mode range -rsize 4kib -nreq 3000 -conc 1 -cache warm -reps 3 -env ZEROS3_DIRECT_PACK_MIB=0
  getrun scale_whole_ttfb_$s $F -objs 1 -size $s -mode whole -nreq 6 -conc 1 -cache warm -reps 3
done
echo SCALE_DONE
