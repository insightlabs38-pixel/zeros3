#!/bin/bash
# run_final3.sh : synthetic locator scale (memory / build time / lookup latency) for 100k .. 10M records
set -u
H=$(cd "$(dirname "$0")" && pwd); R=$H/../results; ROOT=$(cd "$H/../../../.." && pwd)
( cd $ROOT && ZEROS3_LOCATOR_SCALE=100000,1000000,10000000 go test -count=1 -run TestLocatorScale -v . 2>&1 | grep -E "records|ns|MiB|bytes|ok|FAIL|---" ) > $R/locator_scale.txt
