#!/bin/bash
# run_final4.sh BASEDIR P1CDIR : quiet rerun of the main matrix (5 rounds), churn, old-binary compatibility, forced-production harness run.
set -u
BASE=$1; FIN=$2; H=$(cd "$(dirname "$0")" && pwd); R=$H/../results; ROOT=$(cd "$H/../../../.." && pwd)
step() { echo "[$(date +%T)] $*"; }
mv $R/final_matrix.jsonl $R/final_matrix_run1_overlapped.jsonl; mv $R/final_matrix.jsonl.err $R/final_matrix_run1_overlapped.jsonl.err 2>/dev/null
step quiet main matrix; $H/ab_p1c.sh $R/final_matrix.jsonl 5 $H/scenarios_p1c.txt base=$BASE p1c=$FIN
step churn; : > $R/churn.jsonl
for df in 0.5 0.9; do for v in base final; do d=$BASE; [ $v = final ] && d=$FIN; $H/churn_p1c.sh $v $d $R/churn.jsonl 100000 $df; done; done
step compat; $H/compat_old_binary.sh $BASE $FIN > $R/compat_old_binary.txt 2>&1
step forced production harness run
( cd $ROOT/testing-harnesses && go run ./runner -group s3,sync,client,apps -bin $FIN/zeros3 -force-production > $R/harness_forced_production.txt 2>&1 ); grep -E "^(PASS|FAIL) " $R/harness_forced_production.txt > $R/harness_forced_production_summary.txt
step done
