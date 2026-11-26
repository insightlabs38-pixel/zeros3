#!/usr/bin/env bash
set -euo pipefail
root=$(realpath "${1:?pass the landing worktree}")
output=$(realpath -m "${2:?pass a scratch directory}")
compiler=${C19_GO:-go}
export GOMAXPROCS=2
mkdir -p "$output"
cd "$root"
git rev-parse HEAD > "$output/source.txt"

tests='^(TestC1(3PresetPack|6NamedBase|6DerivedBase|7FusedIngest|8PackDictionary)|TestFB02_SnapshotSession(DescribeEqualsFresh|RestoreDecodesOnce|DeletedMidRestore|CorruptDescriptor|MissingManifest|Bounds|OldServer)|TestFB03_(PresenceWaitsForPublication|ConcurrentSinksLinearizable|PackFDCache(Concurrent|Retirement|SealAndMove)|Loc(OpenEquivalence|BackgroundMerge)|Maint(IsolatesCorruptRecord|CrashDuringIsolatedPass|AuthorityDamageIsReference))|TestAuthorityThinImportNamespaceCrashCuts|TestBulkReplicate_ThroughStandardReverseProxy|TestSnapshotRestore_MetadataAndContentTypePreserved)'
races='^(TestFB02_SnapshotSession(DeletedMidRestore|OldServer)|TestFB03_(ConcurrentSinksLinearizable|PackFDCacheConcurrent|LocBackgroundMerge)|TestC16NamedBaseReplication|TestC18PackDictionaryRepair)$'
"$compiler" test . -count=1 -timeout=2m -v -run "$tests" > "$output/tests.txt" 2>&1
"$compiler" test -race . -count=1 -timeout=3m -v -run "$races" > "$output/race.txt" 2>&1
