#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${ZEROS3_DF0_FIXTURES:?Set the pinned release directory described in DF0/REPRODUCE.md}"
go_bin=${ZEROS3_SAT_GO:-go}
scratch=$1
phase=$2
mkdir -p "$scratch"
scratch=$(cd "$scratch" && pwd)
export GOMAXPROCS=${GOMAXPROCS:-2}
export ZEROS3_SAT_FAMILY=curl
"$go_bin" test -c -o "$scratch/replay.test" .
run() {
    local label=$1
    shift
    env "$@" "$scratch/replay.test" -test.v -test.timeout=90s -test.run '^TestDFSatSigned$' > "$scratch/$label.txt"
}

case "$phase" in
  diagnose)
    for spec in 2:0 8:0 32:0 32:1 8:1 2:1; do
        IFS=: read -r clients round <<< "$spec"
        ZEROS3_DF1_FAMILY=curl ZEROS3_DF1_CLIENTS=$clients ZEROS3_DF1_COUNT=240 ZEROS3_DF1_ROUND=$round \
          "$scratch/replay.test" -test.v -test.timeout=1m -test.run '^TestDF1SignedReplay$' > "$scratch/repro-$clients-$round.txt"
    done
    for clients in 2 8 32; do
        run "get-$clients" ZEROS3_SAT_CLIENTS="$clients" ZEROS3_SAT_COUNT=320
    done
    for clients in 2 32; do
        run "profile-$clients" ZEROS3_SAT_CLIENTS="$clients" ZEROS3_SAT_COUNT=96 \
            ZEROS3_SAT_OBSERVE=1 ZEROS3_SAT_TRACE=1 ZEROS3_SAT_PROFILE="$scratch/profile-$clients"
    done
    run evicted ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=96 ZEROS3_SAT_COLD=1 ZEROS3_SAT_OBSERVE=1
    run one-core GOMAXPROCS=1 ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=96
    ;;
  compare)
    for round in 0 1; do
        arms=(baseline gate merge)
        if [[ $round == 1 ]]; then arms=(merge gate baseline); fi
        for clients in 2 8 32; do
            for mode in "${arms[@]}"; do
                run "arch-$mode-$clients-$round" ZEROS3_SAT_CLIENTS="$clients" ZEROS3_SAT_COUNT=320 \
                    ZEROS3_SAT_ROUND="$round" ZEROS3_SAT_MODE="$mode"
            done
        done
    done
    ;;
  controls)
    for mode in baseline gate merge; do
        run "control-aliases-$mode" ZEROS3_SAT_MODE="$mode" ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=96 ZEROS3_SAT_ALIASES=1
        run "control-diverse-$mode" ZEROS3_SAT_MODE="$mode" ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=96 ZEROS3_SAT_DIVERSE=1
    done
    for mode in baseline merge; do
        run "control-ranges-$mode" ZEROS3_SAT_MODE="$mode" ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=160 ZEROS3_SAT_WORKLOAD=ranges
        run "control-puts-$mode" ZEROS3_SAT_MODE="$mode" ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=96 ZEROS3_SAT_WORKLOAD=put
        run "control-cold-$mode" ZEROS3_SAT_MODE="$mode" ZEROS3_SAT_CLIENTS=32 ZEROS3_SAT_COUNT=32 ZEROS3_SAT_COLD=1 ZEROS3_SAT_OBSERVE=1
    done
    for clients in 2 8 32; do
        run "fifo-$clients" ZEROS3_SAT_MODE=fifo ZEROS3_SAT_CLIENTS="$clients" ZEROS3_SAT_COUNT=320
    done
    ;;
  confirm)
    : "${ZEROS3_SAT_FROZEN:?Confirm the development choices are frozen before accessing nginx}"
    test "$ZEROS3_SAT_FROZEN" = 1
    for pair in 0 1; do
        for round in 0 1; do
            arms=(baseline merge)
            if [[ $round == 1 ]]; then arms=(merge baseline); fi
            for clients in 2 8 32; do
                for mode in "${arms[@]}"; do
                    run "confirm-$mode-$clients-$round-$pair" ZEROS3_SAT_FAMILY=nginx ZEROS3_SAT_MODE="$mode" \
                        ZEROS3_SAT_CLIENTS="$clients" ZEROS3_SAT_COUNT=320 ZEROS3_SAT_ROUND="$round" ZEROS3_SAT_PAIR="$pair"
                done
            done
        done
    done
    ;;
  qualify)
    : "${ZEROS3_GF2_OLD_BINARY:?Set a pre-GF2 executable for refusal checks}"
    native='TestDFSat(FlightCancellationAndCapacity|ChannelAdmissionCancellation|ReplayContextBeforeFetch|SignedScopeAndCorruption)|TestGF7(TranslatedAuthority|PublicationCuts|TranslatedControl)|TestGF8CompiledSnapshotRepair|TestDF0GenericRepairDiscovery|TestFBJ2Grouped(Replay|SyncFailure)'
    "$go_bin" test -v -race -count=1 -timeout=2m -run "^($native)$" .
    ZEROS3_SAT_MODE=merge ZEROS3_SAT_CLIENTS=8 ZEROS3_SAT_COUNT=32         "$go_bin" test -v -race -count=1 -timeout=2m -run '^TestDFSatSigned$' .
    for f in FuzzGF7TranslatedFrame FuzzGF8TranslateIndex; do
        printf 'DF2SATFUZZ %s\n' "$f"
        "$go_bin" test -run '^$' -fuzz "^$f$" -fuzztime=6000x -parallel=2 .
    done
    ;;
  *) printf '%s\n' 'Usage: run.sh scratch-dir {diagnose|compare|controls|confirm|qualify}' >&2; exit 2;;
esac
