#!/bin/bash
# Interleaved A/B runner for any p0bench subcommand (copied from the Z4-P1C runner; the work dir default differs).
# Usage: ab_p3.sh OUT.jsonl ROUNDS SCENARIO_FILE VARIANT=BINDIR [VARIANT=BINDIR ...]
# SCENARIO_FILE lines: label[@maxrounds]|<p0bench subcommand and args>   (see scenarios_p1c.txt)
# BINDIR holds zeros3, zeros3-prof, p0bench. One fresh store + process per repetition; every PUT is replayed byte-for-byte.
set -u
OUT=$1; ROUNDS=$2; SC=$3; shift 3
WORK=${P3_WORK:-/tmp/claude-0/p3/work}; mkdir -p "$WORK"
VARS=("$@")
DRV=$(echo "${VARS[0]}" | cut -d= -f2 | cut -d: -f1)/p0bench
for r in $(seq 1 "$ROUNDS"); do
  while IFS='|' read -r label args; do
    [ -z "$label" ] && continue; case "$label" in \#*) continue;; esac
    case "$label" in *@*) n=${label##*@}; label=${label%@*}; [ "$r" -gt "$n" ] && continue;; esac
    # alternate the variant order every round to expose first/second-position bias
    vs=("${VARS[@]}"); if [ $((r % 2)) -eq 0 ]; then vs=(); for ((i=${#VARS[@]}-1; i>=0; i--)); do vs+=("${VARS[$i]}"); done; fi
    read -ra A <<< "$args"; sub=${A[0]}; rest=("${A[@]:1}")
    for v in "${vs[@]}"; do
      name=${v%%=*}; dir=${v#*=}; envs=""
      case "$dir" in *:*) envs=${dir#*:}; dir=${dir%%:*};; esac   # name=BINDIR[:KEY=VAL,KEY=VAL] (server environment)
      P0_NOTE="variant=$name round=$r" "$DRV" "$sub" -label "$label" -out "$OUT" -work "$WORK" \
        -bin "$dir/zeros3" -profbin "$dir/zeros3-prof" -reps 1 ${envs:+-env "$envs"} "${rest[@]}" >/dev/null 2>>"$OUT.err" || echo "FAILED $label $name" >&2
    done
  done < "$SC"
done
echo AB_DONE
