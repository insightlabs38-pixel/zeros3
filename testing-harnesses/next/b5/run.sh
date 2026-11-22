#!/bin/bash
# B5 reproduction: corpus, fixtures, matched reads, concurrent readers, maintenance under load, migration.
# usage: run.sh WORK   (needs root for drop_caches; ~1 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); R="$ROOT/testing-harnesses/next/b5/results"
C=$W/corpus
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" tars
bash "$ROOT/testing-harnesses/z4/p0/scripts/fetch_corpus.sh" "$C" git
mkdir -p "$C/ts"; for v in 5.6.2 5.6.3 5.7.2; do [ -f "$C/ts/typescript-$v.tgz" ] || curl -sSfL -o "$C/ts/typescript-$v.tgz" "https://registry.npmjs.org/typescript/-/typescript-$v.tgz"; done
python3 -I "$ROOT/testing-harnesses/next/b5/make_inputs.py" "$C"
(cd "$C" && sha256sum -c "$ROOT/testing-harnesses/next/b5/corpus.sha256" --quiet)
cd "$ROOT"
T="-tags b5locality,b1rangeexperiment -count=1 -timeout 4h"
ZEROS3_B2_CORPUS=$C ZEROS3_B1_DIR=$W/b1 go test $T -run '^TestB1Fixtures$' .
ZEROS3_B5_FX=$W/fx ZEROS3_B2_CORPUS=$C ZEROS3_B1_DIR=$W/b1 ZEROS3_B5_OUT=$R/repack-cost.jsonl go test $T -run '^TestB5BuildFixtures$' .
for fam in coreutils-zw-mixed coreutils-xd-multi coreutils-cdc sqlite ts git gz random; do
  F=$W/fx/$fam; cfg="base=$F/base:p0,baseP=$F/base,blk=$F/blk:p0,blkP=$F/blk"
  [ "$fam" = coreutils-zw-mixed ] && cfg="$cfg,rbP=$F/rb"
  ZEROS3_B5_CONFIGS=$cfg ZEROS3_B5_N=32 ZEROS3_B5_COLD=1 ZEROS3_B5_LABEL=$fam ZEROS3_B5_OUT=$R/reads-$fam.jsonl go test $T -run '^TestB5ReadBench$' .
  ZEROS3_B5_CONFIGS="base=$F/base:p0,blkP=$F/blk" ZEROS3_B5_LABEL=$fam ZEROS3_B5_OUT=$R/concurrent.jsonl go test $T -run '^TestB5ConcurrentBench$' .
done
ZEROS3_B5_MAINTSTORE=$W/fx/coreutils-zw-mixed/base ZEROS3_B5_OUT=$R/maint-under-load.jsonl go test $T -run '^TestB5MaintUnderLoad$' .
# old binary: git worktree add $W/old b169eda; cp testing-harnesses/next/b5/baseline_probe_test.go.txt $W/old/zeros3_b5_baseline_test.go
#   (cd $W/old && ZEROS3_B5_STORES=zw=$W/fx/coreutils-zw-mixed/base,cdc=$W/fx/coreutils-cdc/base ZEROS3_B5_N=32 ZEROS3_B5_OUT=$R/reads-oldbinary.jsonl \
#      go test -tags b5baseline,b1rangeexperiment -run TestB5BaselineReads .)
#   ZEROS3_B5_MIGRATE=$W/b1/coreutils/cdc: TestB5BaselineMigrate (old) vs TestB5MigrateProfile (this tree)
python3 "$ROOT/testing-harnesses/next/b5/tables.py" "$R"
