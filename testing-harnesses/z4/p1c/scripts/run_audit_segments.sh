#!/bin/bash
# run_audit_segments.sh BIN OUTDIR MODE   (MODE: seq | conc)
# Runs the server under strace -f, performs small-object PUTs through p0bench (1 MiB segments so seals happen),
# then audits the syscall trace with audit_segments.py (plus negative controls).
set -eu
BIN=${1:?server binary}; OUT=${2:?out dir}; MODE=${3:?mode}
HERE=$(cd "$(dirname "$0")" && pwd)
BENCH=${P0BENCH:-$(dirname "$BIN")/p0bench}
mkdir -p "$OUT"; rm -f "$OUT/trace.$MODE"
CONC=1; [ "$MODE" = conc ] && CONC=8
W=$OUT/wrap.$MODE; mkdir -p "$W"
{ echo '#!/bin/sh'; echo 'export ZEROS3_SEGMENT_TARGET_MIB=1'
  echo "exec strace -A -y -f -ttt -s 4096 -e trace=openat,open,creat,close,write,pwrite64,fsync,fdatasync,rename,renameat,renameat2,link,linkat,unlink,unlinkat,mkdir,mkdirat,ftruncate -o $OUT/trace.$MODE \"$BIN\" \"\$@\""
} > "$W/zeros3"; chmod +x "$W/zeros3"; cp "$W/zeros3" "$W/zeros3-prof"; cp "$BENCH" "$W/p0bench"
for spec in "4kib 300" "64kib 120" "700kib 30"; do
  set -- $spec
  "$W/p0bench" put -label audit -work "$OUT/work" -bin "$W/zeros3" -profbin "$W/zeros3-prof" -reps 1 \
     -objs "$2" -size "$1" -conc $CONC -noverify >/dev/null 2>&1 || true
done
sleep 1
STORE="$OUT/work/stores/audit-r0"
echo "# store: $STORE"
python3 "$HERE/audit_segments.py" "$OUT/trace.$MODE" "$STORE" $([ "$MODE" = seq ] && echo --sequential) || true
for c in drop-dirsync drop-seg-fsync drop-pack-dirsync drop-format-sync drop-seal-fsync; do
  echo "# negative control $c (must report violations)"
  python3 "$HERE/audit_segments.py" "$OUT/trace.$MODE" "$STORE" $([ "$MODE" = seq ] && echo --sequential) --control $c | tail -3 || true
done
