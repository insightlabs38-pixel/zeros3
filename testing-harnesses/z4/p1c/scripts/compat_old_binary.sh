#!/bin/bash
# compat_old_binary.sh BASEBIN FINALBIN : the P1B binary (levels 1..9) against stores written by P1C.
set -u
BASE=$1; FIN=$2; W=${P1C_WORK:-/tmp/claude-0/p1c/work}/compat; rm -rf $W; mkdir -p $W
mk() { # mk NAME BIN [ENV]
  "$2/p0bench" mkstore -label c -work $W -bin "$2/zeros3" -profbin "$2/zeros3-prof" -dir $W/$1 -objs 300 -size 8kib -conc 8 ${3:+-env "$3"} >/dev/null 2>&1
}
lvl() { python3 -c "import json;print(json.load(open('$W/$1/FORMAT.json'))['store_format_version'])"; }
run() { "$BASE/zeros3" "$@" 2>&1 | tail -2 | tr '\n' ' '; echo "[exit ${PIPESTATUS[0]}]"; }
echo "# old binary = $(sha256sum $BASE/zeros3 | cut -c1-12) (origin/main 5b9bcdd), new binary = $(sha256sum $FIN/zeros3 | cut -c1-12)"
mk old_layout "$BASE"; echo "1. store written by the OLD binary: level $(lvl old_layout); new binary verify:"; "$FIN/zeros3" verify -store $W/old_layout 2>&1 | tail -1; echo "   level after the new binary only opened it: $(lvl old_layout)"
mk seg_default "$FIN"; echo "2. store written by the NEW binary, default policy: level $(lvl seg_default); segments=$(ls $W/seg_default/segments 2>/dev/null | wc -l); old binary verify:"; run verify -store $W/seg_default
mk seg_off "$FIN" ZEROS3_SMALL_OBJECT_STORAGE=off; echo "3. store written by the NEW binary, ZEROS3_SMALL_OBJECT_STORAGE=off: level $(lvl seg_off); segments dir: $(ls $W/seg_off/segments 2>/dev/null | wc -l) entries; old binary verify:"; run verify -store $W/seg_off
echo "4. level-11 store (2) read by the new binary with the policy off:"; ZEROS3_SMALL_OBJECT_STORAGE=off "$FIN/zeros3" verify -store $W/seg_default -deep 2>&1 | tail -1
echo "5. old binary on the level-11 store, stats / gc dry-run (must refuse, must not modify):"; before=$(find $W/seg_default -type f | xargs sha256sum | sha256sum | cut -c1-12); run stats -store $W/seg_default; run gc -store $W/seg_default; after=$(find $W/seg_default -type f | xargs sha256sum | sha256sum | cut -c1-12); echo "   store tree hash before/after: $before / $after"
rm -rf $W
