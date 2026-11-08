#!/bin/sh
# profsum.sh PROFDIR OUT.txt : compact text summary of a P0 overlay profile dir.
# CPU = merge of 500 ms segments. Raw .pprof stays in scratch (not committed).
set -eu
D=${1:?profile dir}; OUT=${2:?out file}
{
 echo "# profile dir: $(basename "$D")"
 if ls "$D"/cpu-*.pprof >/dev/null 2>&1; then
  echo "## CPU flat (top 28)"; go tool pprof -top -nodecount=28 "$D"/cpu-*.pprof 2>/dev/null | sed -n '4,40p'
  echo "## CPU cumulative (top 45)"; go tool pprof -top -cum -nodecount=45 "$D"/cpu-*.pprof 2>/dev/null | sed -n '5,55p'
 fi
 for p in allocs; do [ -f "$D/$p.pprof" ] && { echo "## alloc_space (top 18)"; go tool pprof -top -nodecount=18 -sample_index=alloc_space "$D/$p.pprof" 2>/dev/null | sed -n '5,28p'; echo "## alloc_objects (top 12)"; go tool pprof -top -nodecount=12 -sample_index=alloc_objects "$D/$p.pprof" 2>/dev/null | sed -n '5,20p'; }; done
 [ -f "$D/mutex.pprof" ] && { echo "## mutex delay (top 12)"; go tool pprof -top -nodecount=12 "$D/mutex.pprof" 2>/dev/null | sed -n '4,18p'; }
 [ -f "$D/block.pprof" ] && { echo "## block delay (top 15)"; go tool pprof -top -nodecount=15 "$D/block.pprof" 2>/dev/null | sed -n '4,22p'; }
 [ -f "$D/metrics.json" ] && { echo "## runtime metrics"; cat "$D/metrics.json"; }
} > "$OUT"
