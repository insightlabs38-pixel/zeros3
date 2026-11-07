#!/bin/bash
# usage: run_gotest.sh WORKDIR. Runs the B4 real-data go tests; exits nonzero on any test failure.
# B4_GOTEST overrides the test command (used only by the b5 failure-propagation regression).
set -euo pipefail
W=$(realpath "$1"); Z=$(cd "$(dirname "$0")/../../z3" && pwd); cd "$Z"
gotest() { go test ./graphnorm -v -run 'RealFunctionGraphMatching|MatchRetention|Renumbering|BitFlips|PDBLineOracle|TableStability'; }
# no pipe: filter only after the exit code is checked
rc=0; B4_CORPUS=$W ${B4_GOTEST:-gotest} > "$W/gotest.log" 2>&1 || rc=$?
grep -E "^\s+[a-z_]+\.go|^(---|ok|FAIL)" "$W/gotest.log" || true
[ $rc -eq 0 ] || { echo "FATAL: go test exited $rc" >&2; exit $rc; }
grep -q '^ok' "$W/gotest.log" || { echo "FATAL: go test reported no ok line" >&2; exit 1; }
