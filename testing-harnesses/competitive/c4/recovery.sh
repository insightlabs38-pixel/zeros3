#!/usr/bin/env bash
# Bounded disjoint partitions of the existing root suite, with T1 resume.
set -euo pipefail
output=$(realpath -m "${1:?pass a scratch directory}")
scope=${2:-all}
compiler=${C4_GO:-go}
harness=$(cd "$(dirname "$0")" && pwd)
root=$(git -C "$harness" rev-parse --show-toplevel)
export PATH="$(dirname "$(command -v "$compiler")"):$PATH"
export GOMAXPROCS=2
mkdir -p "$output"
if [[ ! -x "$output/t1run" ]]; then
  (cd "$root/testing-harnesses" && "$compiler" build -trimpath -o "$output/t1run" ./harness/t1run)
fi
durability='^(TestB4ServeMaintenanceWorkflow|TestB9GroupFaults|TestB10GroupStaleViews|TestB10GroupedDamagedRecordRefusesGC|TestC2ArchiveFinishProcessKill|TestAuthorityThinImportNamespaceCrashCuts|TestP3_RealKillDuringMaintenance|TestNamespaceCheckpointPublicationCuts|TestNamespaceCheckpointJournalFaultModel)$'
compatibility='^(TestAuthorityOldBinaryRefusesAndLeavesStoreUntouched|TestAuthorityRevertAllLowersFloorAndOldBinaryReopens|TestAuthorityRecoverPeerClosure|TestPack_ParityRestoresExactPackAndRefusesMultipleLoss|TestPack_RepairReplacesCorruptPackedChunkFromPeer)$'
cd "$root"
"$compiler" test . -list '^(Test|Example|Fuzz)' > "$output/catalog.txt"
for name in durability compatibility; do
  if [[ "$scope" == "$name" || "$scope" == all ]]; then
    when=$durability
    if [[ "$name" == compatibility ]]; then
      when=$compatibility
    fi
    "$output/t1run" -root "$root" -mode normal -jobs 1 -test-timeout 75s \
      -run "$when" -cache "$output/cache" -evidence "$output/$name.json"
  fi
done
