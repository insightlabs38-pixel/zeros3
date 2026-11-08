#!/bin/sh
# Shared settings for P0 runners. Override via environment.
P0_ROOT=${P0_ROOT:-$(cd "$(dirname "$0")/../../../.." && pwd)}
P0_BIN=${P0_BIN:-/tmp/claude-0/bin}
P0_WORK=${P0_WORK:-/tmp/claude-0/p0work}
P0_RES=${P0_RES:-$P0_ROOT/testing-harnesses/z4/p0/results}
P0=$P0_BIN/p0bench
mkdir -p "$P0_WORK" "$P0_RES"
# P0_FROM=label resumes a matrix at that label (earlier labels skipped, their results kept)
skip() { [ -n "${P0_FROM:-}" ] && [ "${P0_STARTED:-0}" != 1 ] && { if [ "$1" = "$P0_FROM" ]; then P0_STARTED=1; return 1; fi; return 0; }; return 1; }
# run LABEL FILE.jsonl args... : one scenario, results appended, failure aborts the script
putrun()  { l=$1; f=$2; shift 2; skip "$l" && return 0; echo "== $l"; $P0 put   -label "$l" -out "$P0_RES/$f" -work "$P0_WORK" -bin "$P0_BIN/zeros3" -profbin "$P0_BIN/zeros3-prof" "$@" >/dev/null || { echo "FAILED $l" >&2; exit 1; }; }
getrun()  { l=$1; f=$2; shift 2; skip "$l" && return 0; echo "== $l"; $P0 get   -label "$l" -out "$P0_RES/$f" -work "$P0_WORK" -bin "$P0_BIN/zeros3" -profbin "$P0_BIN/zeros3-prof" "$@" >/dev/null || { echo "FAILED $l" >&2; exit 1; }; }
mixrun()  { l=$1; f=$2; shift 2; skip "$l" && return 0; echo "== $l"; $P0 mixed -label "$l" -out "$P0_RES/$f" -work "$P0_WORK" -bin "$P0_BIN/zeros3" -profbin "$P0_BIN/zeros3-prof" "$@" >/dev/null || { echo "FAILED $l" >&2; exit 1; }; }
clirun()  { l=$1; f=$2; shift 2; skip "$l" && return 0; echo "== $l"; $P0 cli   -label "$l" -out "$P0_RES/$f" -work "$P0_WORK" -bin "$P0_BIN/zeros3" -profbin "$P0_BIN/zeros3-prof" "$@" >/dev/null || { echo "FAILED $l" >&2; exit 1; }; }
