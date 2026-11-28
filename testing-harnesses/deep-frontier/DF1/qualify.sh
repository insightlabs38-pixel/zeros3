#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
: "${ZEROS3_DF0_FIXTURES:?Set the pinned Lua and curl fixture directory}"
: "${ZEROS3_GF2_OLD_BINARY:?Set the real pre-GF2 reader executable}"
test -x "$ZEROS3_GF2_OLD_BINARY"
export GOMAXPROCS=${GOMAXPROCS:-2}
go_bin=${ZEROS3_DF1_GO:-go}
core='TestDF1(CanonicalVarints|CanonicalVarintNoAllocation|BitWriterEquivalent|PackedTokensEquivalent|HuffmanRefillEquivalent)'
authority='TestGF2NativeAuthority|TestGF7(TranslatedControl|TranslatedAuthority|PublicationCuts)|TestGF8(CompiledSnapshotRepair|BitPatchBoundaries)|TestDF0GenericRepairDiscovery'
codec='TestRepCodec(LabWireFidelity|NestedProduction|UnsupportedFallsBack|SeekableRanges|SeekIndexIntegrity)|TestRepIRMatchLengthBoundaries|TestAuthority(OptInCodecReplay|SeekableCodecReplay|CycleRejected)'
journal='TestFBJ2Grouped(Replay|SyncFailure|FeatureGate|DamageRefuses)'
"$go_bin" test -v -count=1 -timeout=2m -run "^($core|$authority|$codec|$journal)$" .
"$go_bin" test -v -race -count=1 -timeout=2m -run "^($core|$authority|TestAuthorityConcurrentRangedReadsSharedCaches|$journal)$" .
ZEROS3_DF1_FAMILY=curl ZEROS3_DF1_CLIENTS=8 ZEROS3_DF1_COUNT=96 ZEROS3_DF1_ROUND=0 \
    "$go_bin" test -v -race -count=1 -timeout=2m -run '^TestDF1SignedReplay$' .
for fuzzer in FuzzDF1CanonicalVarintCompatibility FuzzGF2FrameProgram FuzzGF7TranslatedFrame FuzzGF8TranslateIndex FuzzRepCodecDecode; do
    printf 'DF1FUZZ %s\n' "$fuzzer"
    "$go_bin" test -run '^$' -fuzz "^$fuzzer$" -fuzztime=6000x -parallel=2 .
done
