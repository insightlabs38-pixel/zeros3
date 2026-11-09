#!/bin/bash
# syscount.sh BIN OUTDIR LABEL SIZE OBJS CONC : per-object syscall counts (strace -c -f) of the server for one PUT scenario.
set -eu
BIN=${1:?}; OUT=${2:?}; LABEL=${3:?}; SIZE=${4:?}; OBJS=${5:?}; CONC=${6:?}
mkdir -p "$OUT"; W=$OUT/wrap.$LABEL; mkdir -p "$W"; rm -f "$OUT/sc.$LABEL"
BENCH=${P0BENCH:-$(dirname "$BIN")/p0bench}
{ echo '#!/bin/sh'; echo "exec strace -A -f -ttt -e trace=openat,open,fsync,fdatasync,mkdir,mkdirat,rename,renameat,renameat2,write,pwrite64,unlink,unlinkat,newfstatat,stat,close -o $OUT/sc.$LABEL \"$BIN\" \"\$@\""; } > "$W/zeros3"; chmod +x "$W/zeros3"; cp "$W/zeros3" "$W/zeros3-prof"; cp "$BENCH" "$W/p0bench"
"$W/p0bench" put -label sc -work "$OUT/work" -bin "$W/zeros3" -profbin "$W/zeros3-prof" -reps 1 -objs "$OBJS" -size "$SIZE" -conc "$CONC" -noverify > /dev/null 2>&1 || true
sleep 1
python3 - "$OUT/sc.$LABEL" "$OBJS" <<'PY'
import sys,re,collections
n=int(sys.argv[2]); c=collections.Counter()
for l in open(sys.argv[1],errors='replace'):
    m=re.match(r'^\d+\s+[\d.]+\s+(\w+)\(',l)
    if m: c[m.group(1)]+=1
print('per-object syscall counts (%d objects; whole server lifetime incl. startup and bucket creation)'%n)
for k in ('fsync','openat','mkdir','mkdirat','rename','renameat2','renameat','pwrite64','write','unlinkat','unlink','newfstatat','stat','close'):
    if c[k]: print('  %-10s %8.2f'%(k,c[k]/n))
PY
