#!/bin/bash
# syscount.sh BIN OUTDIR LABEL SIZE OBJS CONC : per-object syscall counts (strace -c -f) of the server for one PUT scenario.
set -eu
BIN=${1:?}; OUT=${2:?}; LABEL=${3:?}; SIZE=${4:?}; OBJS=${5:?}; CONC=${6:?}
mkdir -p "$OUT"; W=$OUT/wrap.$LABEL; mkdir -p "$W"; rm -f "$OUT/sc.$LABEL"
BENCH=${P0BENCH:-$(dirname "$BIN")/p0bench}
{ echo '#!/bin/sh'; echo "exec strace -f -c -S name -o $OUT/sc.$LABEL \"$BIN\" \"\$@\""; } > "$W/zeros3"; chmod +x "$W/zeros3"; cp "$W/zeros3" "$W/zeros3-prof"; cp "$BENCH" "$W/p0bench"
"$W/p0bench" put -label sc -work "$OUT/work" -bin "$W/zeros3" -profbin "$W/zeros3-prof" -reps 1 -objs "$OBJS" -size "$SIZE" -conc "$CONC" -noverify > /dev/null 2>&1 || true
sleep 1
python3 - "$OUT/sc.$LABEL" "$OBJS" <<'PY'
import sys,re
n=int(sys.argv[2]); want=('fsync','openat','mkdir','rename','write','pwrite64','pread64','read','close','fdatasync','unlink','unlinkat','newfstatat','stat','futex','epoll_pwait')
print('%-10s per-object syscall counts (%d objects)'%('',n))
for l in open(sys.argv[1]):
    m=re.match(r'\s*[\d.]+\s+[\d.]+\s+\d+\s+(\d+)\s+(\d+)?\s*(\w+)$',l.strip()) or re.match(r'\s*[\d.]+\s+[\d.]+\s+\d+\s+(\d+)\s+(\w+)$',l.strip())
    if not m: continue
    name=m.group(m.lastindex); calls=int(m.group(1))
    if name in want: print('  %-12s %8.2f'%(name,calls/n))
PY
