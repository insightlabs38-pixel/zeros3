#!/bin/bash
# usage: run_policies.sh BIN OUTDIR FAMILY [POLICY...]   Fails (nonzero) if any policy run is inexact or errors.
set -euo pipefail
BIN=$1; OUT=$2; FAM=$3; shift 3; cd "$(dirname "$0")"; source ./families.sh
files=$(eval echo \$fam_$FAM); mkdir -p "$OUT/$FAM"
pols=${*:-"direct first prev sel2 lin2 pool3"}
$BIN cdc $files > "$OUT/$FAM/cdc.jsonl"
for p in $pols; do
  extra=""; [ "$p" = "pool3seg" ] && { extra="-seg"; q=pool3; } || q=$p
  rm -rf "$OUT/$FAM/store_$p"
  $BIN family -policy $q $extra -store "$OUT/$FAM/store_$p" -json "$OUT/$FAM/$p.json" $files > "$OUT/$FAM/$p.log"
done
echo "$FAM done" > "$OUT/$FAM/policies.done"
