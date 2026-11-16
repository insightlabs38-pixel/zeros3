#!/bin/bash
# Interleaved B5 (unmodified main build) vs P7 numeric engine on one family.
# usage: bench_model.sh B5BIN P7BIN CORPUS OUT FAMILY B5POLICY P7POLICY REPS [P7FLAGS...]
# Every run is a fresh process; wall/user/sys from bash `time`, peak RSS from getrusage (printed
# by the tools). Stores are deleted after replay + verification; outputs: OUT/<family>.jsonl + logs.
set -euo pipefail
B5=$1; P7=$2; D=$3; OUT=$4; FAM=$5; B5POL=$6; P7POL=$7; REPS=$8; shift 8; P7FLAGS="$*"
HERE=$(cd "$(dirname "$0")" && pwd); source "$HERE/../../b5/families.sh"
files=$(eval echo \$fam_$FAM); mkdir -p "$OUT"; J="$OUT/$FAM.jsonl"; S="$OUT/store_$FAM"; R="$OUT/replay_$FAM"
TIMEFORMAT='%R %U %S'
run() { # label tool phase cmd...
  local label=$1 tool=$2 phase=$3; shift 3
  local t; t=$( { time "$@" > "$OUT/$label.out" 2> "$OUT/$label.err"; } 2>&1 | tail -1 )
  local rss; rss=$(grep -o '"maxrss_kib":[0-9]*' "$OUT/$label.err" | tail -1 | cut -d: -f2)
  local alloc; alloc=$(grep -o '"total_alloc_mib":[0-9]*' "$OUT/$label.err" | tail -1 | cut -d: -f2)
  local ch; ch=$(grep -o 'charged=[0-9]*' "$OUT/$label.out" | tail -1 | cut -d= -f2 || true)
  read -r real user sys <<< "$t"
  echo "{\"family\":\"$FAM\",\"tool\":\"$tool\",\"phase\":\"$phase\",\"label\":\"$label\",\"real_s\":$real,\"user_s\":$user,\"sys_s\":$sys,\"maxrss_kib\":${rss:-0},\"total_alloc_mib\":${alloc:-0},\"charged\":${ch:-0},\"store_bytes\":$(du -sb "$S" 2>/dev/null | cut -f1 || echo 0)}" >> "$J"
}
verify() { python3 -I - "$1" "$2" <<'PY'
import hashlib, os, sys
bad = n = 0
for f in os.listdir(sys.argv[2]):
    h = hashlib.sha256()
    with open(os.path.join(sys.argv[2], f), 'rb') as fh:
        while (b := fh.read(1 << 22)): h.update(b)
    n += 1; bad += h.hexdigest() != f
print('{"verify":"%s","states":%d,"bad":%d}' % (sys.argv[1], n, bad)); sys.exit(1 if bad else 0)
PY
}
for i in $(seq 1 $REPS); do
  if [ "$B5POL" != "-" ]; then
    rm -rf "$S" "$R"
    run b5_enc_${FAM}_$i b5 encode $B5 family -policy $B5POL -rangeseg 65536 -store "$S" $files
    (cd / && run b5_dec_${FAM}_$i b5 replay $B5 replay -store "$S" -out "$R")
    verify b5_$i "$R" >> "$J"
  fi
  rm -rf "$S" "$R"
  run p7_enc_${FAM}_$i p7 encode $P7 family -policy $P7POL -store "$S" $P7FLAGS $files
  (cd / && run p7_dec_${FAM}_$i p7 replay $P7 replay -store "$S" -out "$R")
  verify p7_$i "$R" >> "$J"
  rm -rf "$R"
  (cd / && run p7_dec4_${FAM}_$i p7 replay-4w $P7 replay -store "$S" -workers 4)
  if [ "$i" = 1 ]; then
    last=$(echo $files | tr ' ' '\n' | tail -1)
    $P7 ranges -store "$S" -orig "$last" > "$OUT/p7_ranges_$FAM.jsonl"
    cp "$S/manifest.json" "$OUT/p7_manifest_$FAM.json"
  fi
  rm -rf "$S"
done
