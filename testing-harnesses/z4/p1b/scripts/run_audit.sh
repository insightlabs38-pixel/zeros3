#!/bin/bash
# run_audit.sh BIN OUTDIR MODE
#   MODE: seq-loose | seq-medium | conc-loose | conc-medium
# Runs the server under strace -f, performs PUTs through p0bench, then audits the
# syscall trace's durability ordering with audit_order.py.
set -eu
BIN=${1:?server binary}; OUT=${2:?out dir}; MODE=${3:?mode}
HERE=$(cd "$(dirname "$0")" && pwd)
BENCH=${P0BENCH:-$(dirname "$BIN")/p0bench}
mkdir -p "$OUT"; STORE=$OUT/store.$MODE; rm -rf "$STORE"
ENV=""; CONC=1; EXTRA=""
case $MODE in
  seq-loose)    ENV="ZEROS3_DIRECT_PACK_MIB=0"; CONC=1 ;;
  seq-medium)   CONC=1 ;;
  conc-loose)   ENV="ZEROS3_DIRECT_PACK_MIB=0"; CONC=8 ;;
  conc-medium)  CONC=8 ;;
esac
# p0bench starts its own server; wrap the binary with strace instead.
W=$OUT/wrap.$MODE; mkdir -p "$W"
{ echo '#!/bin/sh'; for kv in $ENV; do echo "export $kv"; done
  echo "exec strace -A -y -f -ttt -s 8192 -e trace=openat,open,creat,close,write,pwrite64,fsync,fdatasync,rename,renameat,renameat2,mkdir,mkdirat -o $OUT/trace.$MODE \"$BIN\" \"\$@\""
} > "$W/zeros3"; chmod +x "$W/zeros3"; cp "$W/zeros3" "$W/zeros3-prof"; cp "$BENCH" "$W/p0bench"
rm -f "$OUT/trace.$MODE"
for spec in "4kib 40" "64kib 24" "700kib 12" "3mib 6"; do
  set -- $spec
  "$W/p0bench" put -label audit -work "$OUT/work" -bin "$W/zeros3" -profbin "$W/zeros3-prof" -reps 1 \
     -objs "$2" -size "$1" -conc $CONC -noverify >/dev/null 2>&1 || true
done
sleep 1
python3 "$HERE/audit_order.py" "$OUT/trace.$MODE" "$OUT/work" $([ "$CONC" = 1 ] && echo --strict-global) || true
