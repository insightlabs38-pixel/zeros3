#!/bin/bash
# Final qualification of the checked-out tree. usage: qualify.sh OUTDIR [SCRATCH]   (from the repository root)
#
# Steps come from $QUALIFY_STEPS (default z4/t1/steps.sh), which calls step/skip and lists the MANDATORY names.
# summary.tsv gets one row per attempt, each stamped with the source fingerprint (qual_summary.py). The verdict
# is computed from the LAST attempt of every mandatory step on the CURRENT fingerprint: a failed or interrupted
# step, a skipped or missing mandatory step, or rows from another source never count as a pass, and the script
# exits nonzero unless every mandatory step passed. Earlier failed attempts stay in the record.
# QUALIFY_ONLY=REGEXP reruns just the matching steps (rows are appended); QUALIFY_FRESH=1 starts the record over.
set -uo pipefail
set -m
O=$(realpath -m "$1"); S=$(realpath -m "${2:-$1/scratch}"); mkdir -p "$O" "$S"
ROOT=$(pwd); T1=testing-harnesses/z4/t1; C1=testing-harnesses/z4/c1
STEPS=${QUALIFY_STEPS:-$ROOT/$T1/steps.sh}
SUM="python3 -I $ROOT/$T1/qual_summary.py"
FP=$($SUM fingerprint) || exit 2
[ -n "${QUALIFY_FRESH:-}" ] && rm -f "$O/summary.tsv"
[ -s "$O/summary.tsv" ] || printf 'step\tstatus\trc\tseconds\tnote\tfingerprint\tfinished\tcommand\n' > "$O/summary.tsv"
export VALIDATE_LOG_DIR="$S/validate"; mkdir -p "$VALIDATE_LOG_DIR"
CURSTEP=; CHILD=
row() { printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" "$FP" "$(date -u +%FT%TZ)" "$6" >> "$O/summary.tsv"; echo "$1 $2 rc=$3 ${4}s $5"; }

finish() { local rc=$?; trap - EXIT INT TERM; $SUM report "$O" "$O/mandatory.txt"; local prc=$?; [ $rc -ne 0 ] && exit $rc; exit $prc; }
interrupted() {
  trap - INT TERM
  [ -n "$CHILD" ] && kill -TERM -- "-$CHILD" 2>/dev/null; sleep 1; [ -n "$CHILD" ] && kill -KILL -- "-$CHILD" 2>/dev/null
  [ -n "$CURSTEP" ] && row "$CURSTEP" INTERRUPTED 130 0 "qualification interrupted" ""
  exit 130
}
trap finish EXIT
trap interrupted INT TERM

# step NAME CMD...: run to completion, log to NAME.log, record the real exit status of CMD
step() {
  local name=$1; shift
  [[ $name =~ ${QUALIFY_ONLY:-.} ]] || return 0
  CURSTEP=$name; local t=$SECONDS
  ( "$@" ) > "$O/$name.log" 2>&1 &
  CHILD=$!; wait $CHILD; local rc=$?; CHILD=
  local st=PASS note=
  [ $rc -ne 0 ] && st=FAIL
  # an out-of-space condition anywhere in the log is a failure even when a pipeline hid the exit code
  if grep -q 'no space left on device' "$O/$name.log"; then st=FAIL; note="ENOSPC in the log"; [ $rc -eq 0 ] && rc=28; fi
  row "$name" "$st" "$rc" "$((SECONDS - t))" "$note" "$*"
  CURSTEP=
}
skip() { [[ $1 =~ ${QUALIFY_ONLY:-.} ]] || return 0; row "$1" SKIP - 0 "$2" ""; }
# need PATH MIB: refuse to start an expensive step when the filesystem cannot hold it
need() { "$S/t1run" -need-disk "$1=$2"; }

MANDATORY=()
(cd testing-harnesses && go build -o "$S/t1run" ./harness/t1run) || exit 2
# shellcheck disable=SC1090
. "$(realpath "$STEPS")"   # defines MANDATORY and run_steps
printf '%s\n' "${MANDATORY[@]}" > "$O/mandatory.txt"
run_steps
exit 0   # the verdict is the exit status of finish, not of the last step
