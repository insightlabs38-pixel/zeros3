#!/bin/bash
# Interleaved A/B PUT matrix using the P0 driver (one fresh store + process per repetition).
# Usage: ab.sh OUT.jsonl ROUNDS SCENARIO_FILE VARIANT=BINDIR [VARIANT=BINDIR ...]
# Each BINDIR holds zeros3, zeros3-prof, p0bench. Each round runs every scenario once per variant, alternating.
# SCENARIO_FILE lines: label|p0bench put args   (see scenarios_*.txt)
set -u
OUT=$1; ROUNDS=$2; SC=$3; shift 3
WORK=${P1A_WORK:-/tmp/claude-0/p1a/work}; mkdir -p "$WORK"
DRV=$(echo "$1" | cut -d= -f2)/p0bench
for r in $(seq 1 "$ROUNDS"); do
  while IFS='|' read -r label args; do
    [ -z "$label" ] && continue; case "$label" in \#*) continue;; esac
    # per-scenario minimum rounds: "label@N" runs only in the first N rounds
    case "$label" in *@*) n=${label##*@}; label=${label%@*}; [ "$r" -gt "$n" ] && continue;; esac
    for v in "$@"; do
      name=${v%%=*}; dir=${v#*=}
      P0_NOTE="variant=$name round=$r" "$DRV" put -label "$label" -out "$OUT" -work "$WORK" \
        -bin "$dir/zeros3" -profbin "$dir/zeros3-prof" -reps 1 $args >/dev/null 2>>"$OUT.err" || { echo "FAILED $label $name" >&2; }
    done
  done < "$SC"
done
echo AB_DONE
