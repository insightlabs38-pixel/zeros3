#!/bin/bash
# run_final.sh BASEBIN FINALBIN : the full P1A measurement set against two build dirs (see build.sh).
# Writes results/*.jsonl and results/prof/*.txt next to this script's parent.
set -u
BASE=$1; FIN=$2
H=$(cd "$(dirname "$0")" && pwd); R=$H/../results; mkdir -p "$R/prof"
AB=$H/ab.sh
echo "== matrix"; $AB "$R/final_matrix.jsonl" 4 "$H/scenarios_final.txt" base="$BASE" final="$FIN"
echo "== GOMAXPROCS sweep"
V=()
for n in 1 2 4; do
  V+=("base_p$n=$($H/mkenv.sh base_p$n "$BASE" "GOMAXPROCS=$n")" "final_p$n=$($H/mkenv.sh final_p$n "$FIN" "GOMAXPROCS=$n")")
done
$AB "$R/final_gomaxprocs.jsonl" 3 "$H/scenarios_sweep.txt" "${V[@]}"
echo "== ablation"
A=()
for a in nopipe syncpublish syncwrite fullverify noearlysync; do A+=("abl_$a=$($H/mkenv.sh abl_$a "$FIN" "ZEROS3_INGEST_ABLATE=$a")"); done
head -3 "$H/scenarios_sweep.txt" > /tmp/claude-0/p1a/abl_sc.txt; echo "put_1g_c1@3|-objs 1 -size 1gib -conc 1" >> /tmp/claude-0/p1a/abl_sc.txt
$AB "$R/final_ablation.jsonl" 4 /tmp/claude-0/p1a/abl_sc.txt base="$BASE" final="$FIN" "${A[@]}"
echo "== profiles"
W=${P1A_WORK:-/tmp/claude-0/p1a/work}
for v in base final; do
  d=$BASE; [ $v = final ] && d=$FIN
  for sc in "prof_64m_dp:-objs 8 -size 64mib -conc 1" "prof_1g:-objs 1 -size 1gib -conc 1" "prof_64m_dp_c4:-objs 8 -size 64mib -conc 4"; do
    l=${sc%%:*}; a=${sc#*:}; rm -rf "$W/prof/${v}_$l-r0"
    $d/p0bench put -label "${v}_$l" -work "$W" -bin "$d/zeros3" -profbin "$d/zeros3-prof" -prof full -reps 1 -noverify $a >/dev/null 2>&1
    $H/../../p0/scripts/profsum.sh "$W/prof/${v}_$l-r0" "$R/prof/${v}_$l.txt"
  done
done
echo FINAL_DONE
