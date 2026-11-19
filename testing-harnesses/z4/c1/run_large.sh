#!/bin/bash
# G5: 1 GiB object and a related history through the real CLI in fresh processes.
# usage: run_large.sh WORKDIR [SIZE_BYTES]   (run from the repository root)
set -euo pipefail
W=$(realpath -m "$1"); SIZE=${2:-1073741824}; mkdir -p "$W"
export ZEROS3_C1_LARGE_DIR=$W/store ZEROS3_C1_LARGE_SIZE=$SIZE
go build -o "$W/zeros3" .
go test -c -o "$W/zeros3.test" .
rm -rf "$W/store"; mkdir -p "$W/store"
"$W/zeros3.test" -test.run TestAuthorityLargeFixture -test.v 2>&1 | grep -E 'put|PASS|FAIL' | tee "$W/fixture.txt"
du -sb "$W/store" | tee "$W/cdc_bytes.txt"
R=$(dirname "$0")/rss.py
for step in "migrate -apply -max-depth 2 -archive" "status"; do
  python3 "$R" "${step%% *}" "$W/out_${step%% *}.json" "$W/zeros3" authority $step -store "$W/store" | tee -a "$W/rss.txt"
done
for c in "gc -apply" "repack -apply" "compact" "gc -apply"; do
  python3 "$R" "${c%% *}" "$W/out_${c%% *}.txt" "$W/zeros3" $c -store "$W/store" | tee -a "$W/rss.txt"
done
du -sb "$W/store" | tee "$W/final_bytes.txt"
ZEROS3_C1_LARGE_CHECK=1 "$W/zeros3.test" -test.run TestAuthorityLargeCheck -test.v 2>&1 | grep -E 'exact|ranges|PASS|FAIL' | tee "$W/check.txt"
python3 "$R" verify "$W/verify.txt" "$W/zeros3" verify -store "$W/store" -deep | tee -a "$W/rss.txt"; tail -3 "$W/verify.txt"
rm -rf "$W/store"
