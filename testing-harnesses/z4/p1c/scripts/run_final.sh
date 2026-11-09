#!/bin/bash
# run_final.sh BASEDIR P1CDIR : the complete final measurement sequence (unattended, ~3 h). Results -> ../results/
set -u
BASE=$1; FIN=$2; H=$(cd "$(dirname "$0")" && pwd); R=$H/../results; S=$H/scenarios
A=$H/ab_p1c.sh
step() { echo "[$(date +%T)] $*"; }
step main matrix;     $A $R/final_matrix.jsonl 3 $H/scenarios_p1c.txt base=$BASE p1c=$FIN
step ablation;        $A $R/ablation.jsonl 3 $H/scenarios_ablation.txt p1c=$FIN p1c_segoff=$FIN:ZEROS3_SMALL_OBJECT_STORAGE=off p1c_nopool=$FIN:ZEROS3_INGEST_ABLATE=nochunkpool base=$BASE
step gomaxprocs;      $A $R/gomaxprocs.jsonl 3 $H/scenarios_gmp.txt base1=$BASE:GOMAXPROCS=1 p1c1=$FIN:GOMAXPROCS=1 base2=$BASE:GOMAXPROCS=2 p1c2=$FIN:GOMAXPROCS=2 base4=$BASE:GOMAXPROCS=4 p1c4=$FIN:GOMAXPROCS=4
step large objects;   $A $R/big_order.jsonl 5 $H/scenarios_big.txt base=$BASE p1c=$FIN
step done
