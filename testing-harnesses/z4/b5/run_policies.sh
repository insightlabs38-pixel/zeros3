#!/bin/bash
# usage: run_policies.sh BIN OUTDIR FAMILY [POLICY...]
# For each policy: encode the family, replay every state in a FRESH process from the store alone, hash independently
# in Python, then delete the (large) store. Any inexact state or error makes the script exit nonzero.
set -euo pipefail
BIN=$1; OUT=$2; FAM=$3; shift 3; HERE=$(cd "$(dirname "$0")" && pwd); source "$HERE/families.sh"
files=$(eval echo \$fam_$FAM); mkdir -p "$OUT/$FAM"
pols=${*:-"direct first prev sel2 lin2 pool3"}
[ -s "$OUT/$FAM/cdc.jsonl" ] || $BIN cdc $files > "$OUT/$FAM/cdc.jsonl"
for p in $pols; do
  extra=""; q=$p
  case $p in pool3seg) extra="-seg"; q=pool3;; prev-nonarrow) extra="-narrow=false"; q=prev;; direct-nonarrow) extra="-narrow=false"; q=direct;; esac
  [ -s "$OUT/$FAM/$p.verify.json" ] && continue   # resumable: finished policies are kept
  S="$OUT/$FAM/store_$p"; rm -rf "$S" "$OUT/$FAM/replay_$p"
  $BIN family -policy $q $extra -store "$S" -json "$OUT/$FAM/$p.json" $files > "$OUT/$FAM/$p.log"
  (cd / && $BIN replay -store "$S" -out "$OUT/$FAM/replay_$p") > "$OUT/$FAM/$p.replay.jsonl"
  python3 -I "$HERE/verify_replay.py" "$S" "$OUT/$FAM/replay_$p" > "$OUT/$FAM/$p.verify.json"
  rm -rf "$S" "$OUT/$FAM/replay_$p"
done
echo "$FAM done" > "$OUT/$FAM/policies.done"
