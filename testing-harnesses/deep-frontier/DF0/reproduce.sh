#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
export GOMAXPROCS="${GOMAXPROCS:-2}"
go_bin=${ZEROS3_DF0_GO:-go}
grand="$PWD/testing-harnesses/grand-frontier"

case "${1:-}" in
  original)
    : "${ZEROS3_GF0_FIXTURES:?Set the inherited fixture root}"
    ZEROS3_GF0_CORPUS="$grand/GF0/corpus.json" "$go_bin" test -v -count=1 -timeout=4m \
      -run '^(TestGF7(NativeTranslationPanel|SignedAuthorityPanel)|TestGF8SignedMixed)$' .
    ZEROS3_GF0_CORPUS="$grand/GF2/heldout.json" "$go_bin" test -v -count=1 -timeout=2m \
      -run '^TestGF7(NativeTranslationPanel|SignedAuthorityPanel)$/^typescript-gzip-adjacent$' .
    ;;
  validation)
    : "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture root}"
    for history in adjacent-1 adjacent-2 adjacent-3 retained-series; do
      ZEROS3_DF0_SPLIT=validation "$go_bin" test -v -count=1 -timeout=4m \
        -run "^TestDF0Independent$/^curl$/^${history}$" .
    done
    ZEROS3_DF0_SPLIT=validation "$go_bin" test -v -count=1 -timeout=4m \
      -run '^TestDF0Independent$/^lua$' .
    ;;
  scopes)
    : "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture root}"
    ZEROS3_DF0_SPLIT=scope "$go_bin" test -v -count=1 -timeout=3m -run '^TestDF0Independent$' .
    "$go_bin" test -v -count=1 -timeout=80s -run '^TestDF0Diagnostics$' .
    ;;
  confirmation)
    : "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture root}"
    ZEROS3_DF0_SPLIT=blind "$go_bin" test -v -count=1 -timeout=6m -run '^TestDF0Independent$' .
    ;;
  profiles)
    : "${ZEROS3_DF0_FIXTURES:?Set the pinned DF0 fixture root}"
    : "${ZEROS3_DF0_PROFILE:?Set the scratch profile directory}"
    "$go_bin" test -v -count=1 -timeout=7m -run '^TestDF0LoadAndProfile$' .
    "$go_bin" test -v -count=1 -timeout=4m -run '^TestDF0TailSamples$' .
    ;;
  safety)
    : "${ZEROS3_GF2_OLD_BINARY:?Set a pre-GF2 executable}"
    focused='^(TestDF0(GenericRepairDiscovery|Diagnostics|LiveReadCostProbe)|TestGF7(TranslatedAuthority|PublicationCuts)|TestGF8(CompiledSnapshotRepair|BitPatchBoundaries))$'
    "$go_bin" test -v -race -count=1 -timeout=2m -run "$focused" .
    for fuzz in FuzzGF2FrameProgram FuzzGF7TranslatedFrame FuzzGF8TranslateIndex; do
      "$go_bin" test -run '^$' -fuzz "^$fuzz$" -fuzztime=6000x -parallel=2 .
    done
    ;;
  *)
    printf '%s\n' 'Usage: reproduce.sh {original|validation|scopes|profiles|confirmation|safety}' >&2
    exit 2
    ;;
esac
