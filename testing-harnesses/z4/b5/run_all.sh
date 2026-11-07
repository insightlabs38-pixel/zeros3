#!/bin/bash
# Run every family, record failures, and fail the campaign if any family fails.
set -uo pipefail
BIN=${1:?binary}; OUT=${2:?output directory}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
: > "$OUT/status.txt"
failed=0
run_family() {
  local family=$1 rc
  shift
  if "$HERE/run_policies.sh" "$BIN" "$OUT" "$family" "$@" > "$OUT/run_$family.log" 2>&1; then
    printf '%s exit=0\n' "$family" >> "$OUT/status.txt"
  else
    rc=$?
    printf '%s exit=%s\n' "$family" "$rc" >> "$OUT/status.txt"
    failed=1
  fi
}
for f in pythia14m gpt2 pythia70m smollm2 smollm2ctl; do
  run_family "$f"
done
run_family qwen05 direct first prev sel2 lin2
if (( failed )); then
  echo FAILED >> "$OUT/status.txt"
  exit 1
fi
echo ALLDONE >> "$OUT/status.txt"
