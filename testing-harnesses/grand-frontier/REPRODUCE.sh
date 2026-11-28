#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."
h="$PWD/testing-harnesses/grand-frontier"
: "${ZEROS3_GF0_FIXTURES:?Set the scratch root described in GF0/REPRODUCE.md}"
: "${ZEROS3_GF5_FIXTURES:?Set the verified checkpoint root described in GF5/REPRODUCE.md}"
export GOMAXPROCS=${GOMAXPROCS:-2}
export ZEROS3_GF0_CORPUS="$h/GF0/corpus.json"
gf_go=${ZEROS3_GF_GO:-go}

recovery='TestFBJ2Grouped(SyncFailure|Replay|FeatureGate|DamageRefuses|CheckpointAheadRefuses)|TestFB16SignedProjectionReplication|TestAuthority(CycleRejected|GraphLimits|CorruptComponentFailsClosedNeverWrongBytes|MigrationCancellationBeforePublication)|TestRepOversizedRootReadBounded|TestFB03_PackFDCache(Unit|Concurrent|Retirement|SealAndMove)'
"$gf_go" test -v -count=1 -timeout=90s \
  -run "^(TestGF7(TranslatedAuthority|PublicationCuts|TranslatedControl)|TestGF8(CompiledSnapshotRepair|BitPatchBoundaries)|$recovery)$" .
"$gf_go" test -v -count=1 -timeout=4m \
  -run '^(TestGF7(SignedAuthorityPanel|NativeTranslationPanel)|TestGF8SignedMixed)$' .
"$gf_go" test -v -count=1 -timeout=4m \
  -run '^(TestGF1(SharedClosureOracle|NativeCatalog|PhysicalCompiledSelection)|TestGF2(NativeAuthority|CrashBoundaries|NativeAuthorityPanel|FastWitness|WitnessPanel|BudgetedOverlay|ShiftedProgram|NativePatchPanel)|TestGF3(NativeDiscoveryOracle|NativePhrasePanel|SmallBankSweep))$' .
"$gf_go" test -v -count=1 -timeout=4m \
  -run '^(TestGF4(NativePolicyReplay|OfflineOptimality|WorkloadReplay)|TestGF5(CheckpointIncumbent|CheckpointPanel|LayoutAndDTypeControls|TensorResiduals)|TestGF6(SignedNegotiation|ShapedSignedNegotiation))$' .

ZEROS3_GF0_CORPUS="$h/GF2/heldout.json" "$gf_go" test -v -count=1 -timeout=2m \
  -run '^TestGF7(SignedAuthorityPanel|NativeTranslationPanel)$/^typescript-gzip-adjacent$' .
ZEROS3_GF0_CORPUS="$h/GF2/heldout.json" ZEROS3_GF2_HELDOUT=1 ZEROS3_GF2_RELATIVE=fixed \
  "$gf_go" test -v -count=1 -timeout=1m -run '^TestGF2NativePatchPanel$' .
