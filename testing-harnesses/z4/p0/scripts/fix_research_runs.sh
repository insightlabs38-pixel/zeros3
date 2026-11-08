#!/bin/bash
# Re-run the two research steps whose first invocation was a harness argument error (kept in the JSONL as FAILED):
#  b3_replay_PROF (output directory must pre-exist) and b4_verify_replay (3rd arg is an originals DIRECTORY).
C=$(realpath "$1"); B=$(realpath "$2"); HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../../../.." && pwd)
RES=$ROOT/testing-harnesses/z4/p0/results; W=${P0_WORK:-/tmp/claude-0/p0work}; PR=$W/prof
mkdir -p $W/r_b3/out_pf2 $PR $RES/prof
python3 $HERE/rmeasure.py --label b3_replay_PROF_fixed --out $RES/research_b3.jsonl --prof $PR/b3_replay_fixed --cwd / -- $B/z4b3-prof replay -store $W/r_b3/s_full -out $W/r_b3/out_pf2
$HERE/profsum.sh $PR/b3_replay_fixed-r0 $RES/prof/b3_replay_fixed-r0.txt
rm -rf $W/r_b3/out_pf2 $W/r_b4/lo; mkdir -p $W/r_b4/lo
python3 $HERE/rmeasure.py --label b4_llvm_replay_fixed --out $RES/research_b4.jsonl --cwd / -- $B/z4b4 llvm-replay $W/r_b4/ls $W/r_b4/lo
python3 $HERE/rmeasure.py --label b4_verify_replay_fixed --out $RES/research_b4.jsonl -- python3 -I $ROOT/testing-harnesses/z4/b4/verify_replay.py $W/r_b4/ls $W/r_b4/lo $C/b4/bc
