#!/bin/bash
# G3: >=100k objects and >=1M chunk references; each maintenance command in a fresh process (wall, RSS).
# usage: run_scale.sh WORKDIR [OBJECTS]   (from the repository root)
set -euo pipefail
W=$(realpath -m "$1"); N=${2:-100000}; mkdir -p "$W"
export ZEROS3_C1_SCALE_DIR=$W/store ZEROS3_C1_SCALE_OBJECTS=$N ZEROS3_C1_SCALE_PINS=1
go build -o "$W/zeros3" . && go test -c -o "$W/zeros3.test" .
rm -rf "$W/store"; mkdir -p "$W/store"
"$W/zeros3.test" -test.run TestAuthorityScaleFixture -test.v -test.timeout 3h 2>&1 | grep -E 'written|pin records|PASS|FAIL' | tee "$W/fixture.txt"
du -sb "$W/store" | tee "$W/store_bytes.txt"
run() { n=$1; shift; /usr/bin/time -f "$n wall=%es rss=%MKB" -o "$W/t_$n.txt" "$W/zeros3" "$@" > "$W/o_$n.txt" 2>&1 || true; cat "$W/t_$n.txt"; }
run stats stats -store "$W/store"
run verify verify -store "$W/store"
run verify_deep verify -store "$W/store" -deep
run gc_dry gc -store "$W/store"
run migrate_sample authority migrate -apply -store "$W/store" -bucket b -prefix h/0000 -max-depth 2
run status authority status -store "$W/store"
run verify_after verify -store "$W/store"
run gc_apply gc -store "$W/store" -apply
run repack repack -store "$W/store" -apply
run verify_final verify -store "$W/store" -deep
cp -a "$W/store" "$W/store_revert"
run revert_all authority revert -all -store "$W/store_revert"
run verify_reverted verify -store "$W/store_revert"
