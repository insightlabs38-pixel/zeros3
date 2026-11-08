#!/bin/bash
# B2 (draft PR #97) profiling in an isolated detached worktree. The draft is never modified or pushed.
# usage: run_b2.sh CORPUS_DIR WORKTREE   (WORKTREE = git worktree of origin/z4/representation-authority)
set -euo pipefail
C=$(realpath "$1"); W=$(realpath "$2"); HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../../../.." && pwd)
OUT=$ROOT/testing-harnesses/z4/p0/results; SC=${P0_WORK:-/tmp/claude-0/p0work}/b2${TAG:-}; mkdir -p $SC $OUT/prof
G=$SC/corpus; rm -rf $G; mkdir -p $G/src $G/clr $G/dex $G/parquet $G/sqlite $G/git
for f in $C/src/coreutils-9.*.tar; do ln -s $f $G/src/; done
for f in $C/q6/clr/*.dll; do ln -s $f $G/clr/; done
i=0; for f in $C/q6/fdroid-apk/*.dex; do mkdir -p $G/dex/$((1019000+i)); ln -s $f $G/dex/$((1019000+i))/classes.dex; i=$((i+1)); done
i=0; for f in $C/parquet/snappy/c*.parquet; do ln -s $f $G/parquet/c$i.parquet; i=$((i+1)); done
for f in $C/sqlite/v*.db; do ln -s $f $G/sqlite/; done
for f in $C/git/h*.pack; do ln -s $f $G/git/; done
cp "$ROOT/testing-harnesses/z4/b8/zeros3_b8_physical_test.go.txt" $W/zeros3_b8_physical_test.go
( cd $W && go vet ./... >/dev/null 2>&1 || true; go test -c -o $SC/b2.test . )
cd $W
for fam in ${FAMILIES:-clr-short src-tar-short dex sqlite git parquet unrelated}; do
  for rep in $(seq 0 $((${REPS:-2}-1))); do   # independent processes per family; rep 0 also CPU/mem/block/mutex profiled
    prof=""; [ $rep = 0 ] && prof="-test.cpuprofile $SC/cpu-$fam.pprof -test.memprofile $SC/mem-$fam.pprof -test.memprofilerate 4096 -test.blockprofile $SC/block-$fam.pprof -test.blockprofilerate 10000 -test.mutexprofile $SC/mutex-$fam.pprof -test.mutexprofilefraction 5"
    rm -f $SC/rm.out
    ZEROS3_B2_CORPUS=$G ZEROS3_B2_OUT=$OUT/b2_econ${TAG:-}.jsonl python3 $HERE/rmeasure.py --label "b2${TAG:-}_${fam}_prof${prof:+1}" --out $OUT/b2_time${TAG:-}.jsonl --stdout $SC/b2-$fam-$rep.log -- \
      $SC/b2.test -test.run "TestB2Economics/$fam\$" -test.count=1 -test.v -test.timeout 3h $prof > /dev/null || { echo "B2 FAILED $fam rep $rep" >&2; }
  done
  [ -f $SC/cpu-$fam.pprof ] && { for k in cpu mem block mutex; do cp $SC/$k-$fam.pprof /tmp/claude-0/p0work/prof/b2${TAG:-}-$k-$fam.pprof 2>/dev/null || true; done; }
done
echo B2_DONE
