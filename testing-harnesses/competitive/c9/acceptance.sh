#!/usr/bin/env bash
# Check the final engine with compact relevant partitions.
set -euo pipefail
work=$(realpath -m "${1:?pass final verification scratch}")
harness=$(cd "$(dirname "$0")" && pwd)
root=$(git -C "$harness" rev-parse --show-toplevel)
compiler=${C9_GO:-go}
export GOMAXPROCS=2
mkdir -p "$work"
(
  cd "$root"
  "$compiler" vet .
  "$compiler" test . -run '^(TestAuthority(ClosureSyncImportRejectsBadPayload|ClosureSyncReplication|CrossKeyHints|ThinImportNamespaceCrashCuts|SimilarityOneTargetUsesPriorGeneration)|TestRepack_KeepUnchangedChecksReplacement|TestC2(ArchiveFinishProcessKill|StructuredDeltaBasesUseNativeRanges|SpliceWindowReuseAndExactFailure)|TestSync_ResumeAfterPartialPriorUpload|TestBulkReplicate_ThroughStandardReverseProxy)$' -count=1
)
C4_GO="$compiler" "$harness/../c4/recovery.sh" "$work/root-partitions"
if [[ -n ${C9_QEMU:-} ]]; then
  C4_GO="$compiler" C4_PYTHON=${C9_PYTHON:-python3} "$harness/../c4/arm64.sh" "$work/arm64" "$C9_QEMU"
fi
