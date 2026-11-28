#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture directory}"
export GOMAXPROCS=${GOMAXPROCS:-2}
go_bin=${ZEROS3_DF1_GO:-go}
scratch=$(cd "$1" && pwd)
mkdir -p "$scratch/extra"
export ZEROS3_DF1_FAMILY=curl

run() {
    local arm=$1 name=$2
    shift 2
    env "$@" "$scratch/$arm.test" -test.v -test.count=1 -test.timeout=1m \
        -test.run '^TestDF1SignedReplay$' > "$scratch/extra/$name-$arm.txt"
}

for round in 0 1; do
    arms=(parent candidate)
    if [[ $round == 1 ]]; then arms=(candidate parent); fi
    export ZEROS3_DF1_ROUND=$round
    for probe in range4 range64 whole; do
        for arm in "${arms[@]}"; do
            run "$arm" "cold-$probe-$round" ZEROS3_DF1_CLIENTS=1 ZEROS3_DF1_COUNT=1 \
                ZEROS3_DF1_PROBE="$probe" ZEROS3_DF1_COLD=1
        done
    done
    for arm in "${arms[@]}"; do
        run "$arm" "maintenance-$round" ZEROS3_DF1_CLIENTS=8 ZEROS3_DF1_COUNT=240 ZEROS3_DF1_MAINT=1
    done
done

for probe in range4 whole; do
    count=1024
    if [[ $probe == whole ]]; then count=64; fi
    for arm in parent candidate; do
        path="$scratch/extra/$probe-$arm.cpu"
        run "$arm" "profile-$probe" ZEROS3_DF1_CLIENTS=2 ZEROS3_DF1_COUNT=$count \
            ZEROS3_DF1_PROBE="$probe" ZEROS3_DF1_CPUPROFILE="$path"
        "$go_bin" tool pprof -top -nodecount=20 "$path" > "$scratch/extra/$probe-$arm.top"
    done
done
