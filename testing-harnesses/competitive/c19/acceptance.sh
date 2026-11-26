#!/usr/bin/env bash
set -euo pipefail
output=$(realpath -m "${1:?pass a scratch directory}")
harness=$(cd "$(dirname "$0")" && pwd)
root=$(git -C "$harness" rev-parse --show-toplevel)
compiler=${C19_GO:-go}
export GOMAXPROCS=2 PATH="$(dirname "$(command -v "$compiler")"):$PATH"
mkdir -p "$output"
cd "$root"
head=$(git rev-parse HEAD)
if [[ ! -x "$output/root.test" || ! -f "$output/build.sha" || $(cat "$output/build.sha") != "$head" ]] ||
    ! git diff --quiet HEAD -- '*.go'; then
  "$compiler" vet . > "$output/vet.txt" 2>&1
  "$compiler" test -c -o "$output/root.test" .
  printf '%s\n' "$head" > "$output/build.sha"
fi
"$output/root.test" -test.list '^Test' > "$output/catalog.txt"

partition() {
  local name=$1 selection=$2
  rg "$selection" "$output/catalog.txt" > "$output/$name.selected"
  "$output/root.test" -test.v -test.timeout=2m -test.run "$selection" > "$output/$name.txt" 2>&1
}

partition batch '^TestC1[0-8]'
partition archive '^Test(B8(CanonicalCrossFormat|OnlineMaintenance|ConcurrentReads|ZipExact)|B9OldBinaryRefuses|C2(ArchiveFinishProcessKill|StructuredDeltaBasesUseNativeRanges|SpliceWindowReuseAndExactFailure)|AuthorityRetireEverythingSubprocess)$'
partition dependency '^Test(Authority(ClosureSync.*|CrossKeyHints|ThinImportNamespaceCrashCuts|RecoverPeerClosure|BundleExportToOldBinary|OldBinaryRefusesAndLeavesStoreUntouched|RevertAllLowersFloorAndOldBinaryReopens)|BulkReplicate_ThroughStandardReverseProxy|Sync_(Discovery_.*|ResumeAfterPartialPriorUpload))$'
partition index '^TestAuthoritySimilarity'
partition pack '^Test(Pack_(ParityRestoresExactPackAndRefusesMultipleLoss|RepairReplacesCorruptPackedChunkFromPeer|PayloadAndRecordHeaderDamageFailSafely)|Repack_KeepUnchangedChecksReplacement|B10(GroupStaleViews|GroupedDamagedRecordRefusesGC)|B9GroupFaults|P3_RealKillDuringMaintenance)$'
partition journal '^Test(NamespaceCheckpoint(PublicationCuts|JournalFaultModel)|FB01R(Kill|KillWithoutWatermark|InterruptedGroupMatrix|TailDiscardInterrupted|DegradedViews)|FB02_(ChecksumCheckValues|CRC64NVMEMatchesBitwiseReference|HeaderChecksumMatrix|ChecksumHeaderRules|TrailerModes|TrailerAWSDocumentationVector|MultipartChecksums|DeleteObjectsBodyChecksum|BulkRequiresSignedPayload|PeerResponsesAreBounded|PartRecordBoundary))$'
partition s3 '^Test(HeadObject_.*|GetObject_.*|Range_.*|CopyObject_MetadataDirective.*|Conditional(Put|Get)HTTP_.*|SnapshotRestore_MetadataAndContentTypePreserved)$'

cat "$output"/*.selected | sort > "$output/selected.txt"
if [[ -n $(uniq -d "$output/selected.txt") ]]; then
  exit 1
fi
rg '^Test' "$output/catalog.txt" | sort | comm -23 - "$output/selected.txt" > "$output/unselected.txt"
"$compiler" test -race . -count=1 -timeout=3m -v \
  -run '^(TestC1(1(BackgroundYield|GzipProofCheckpoint)|3PresetPackAdmissionAndReopen|5SimilarityRunGeometry|6NamedBaseReplication|7FusedIngestKeepsFailedPacksPrivate|8PackDictionaryRepair)|TestAuthoritySimilarity(QueryBoundsResidentDirectories|HotPostingsAcrossRuns))$' \
  > "$output/race.txt" 2>&1
