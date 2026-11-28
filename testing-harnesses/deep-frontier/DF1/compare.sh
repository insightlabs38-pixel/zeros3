#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture directory}"
export GOMAXPROCS=${GOMAXPROCS:-2}
go_bin=${ZEROS3_DF1_GO:-go}
scratch=$1
parent=$2
candidate=$3
mkdir -p "$scratch"
scratch=$(cd "$scratch" && pwd)
for arm in parent candidate; do
    ref=$parent
    if [[ $arm == candidate ]]; then ref=$candidate; fi
    if [[ $ref == worktree ]]; then
        cp zeros3.go "$scratch/$arm.go"
    else
        git show "$ref:zeros3.go" > "$scratch/$arm.go"
    fi
    python3 - "$PWD/zeros3.go" "$scratch/$arm.go" > "$scratch/$arm.overlay.json" <<'PY'
import json, sys
print(json.dumps({"Replace": {sys.argv[1]: sys.argv[2]}}))
PY
    "$go_bin" test -overlay "$scratch/$arm.overlay.json" -c -o "$scratch/$arm.test" .
    "$scratch/$arm.test" -test.run '^$' -test.bench '^BenchmarkDF1CanonicalVarint$' \
        -test.benchtime 200ms -test.count 5 > "$scratch/$arm.micro.txt"
done
for round in 0 1; do
    arms=(parent candidate)
    if [[ $round == 1 ]]; then arms=(candidate parent); fi
    cases=(lua:2:1536 curl:2:240)
    if [[ ${ZEROS3_DF1_SWEEP:-0} == 1 ]]; then
        cases+=(curl:1:144 curl:8:240 curl:32:240)
    fi
    for spec in "${cases[@]}"; do
        IFS=: read -r family clients count <<< "$spec"
        for arm in "${arms[@]}"; do
            ZEROS3_DF1_FAMILY=$family ZEROS3_DF1_CLIENTS=$clients ZEROS3_DF1_COUNT=$count ZEROS3_DF1_ROUND=$round \
                "$scratch/$arm.test" -test.v -test.count=1 -test.timeout=90s -test.run '^TestDF1SignedReplay$' \
                > "$scratch/$family-$clients-$round-$arm.txt"
        done
    done
done
