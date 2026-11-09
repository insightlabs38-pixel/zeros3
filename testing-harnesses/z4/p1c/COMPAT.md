# Z4-P1C compatibility matrix

"New" = this branch; "old" = `origin/main` `5b9bcdd` (P1B, store levels 1..9). Every row marked *test* is executed by the named test; rows marked *evidence* were run with the real baseline binary (`results/compat_old_binary.txt`).

| # | situation | expected | covered by |
|---|---|---|---|
| 1 | old store (loose chunks, manifest files, level 1) opened by new | opens, reads everything, level unchanged | test `TestP1C_OldStoreAndPolicyChangesCoexist` |
| 2 | old store, first small PUT by new | tier roots created if the store is below level 5, `FORMAT.json` -> 11, `segments/` created, then the segment file | test `TestP1C_Format_FirstSmallWriteRaisesLevelPolicyOffDoesNot`, crash rows below |
| 3 | mixed loose / medium pack / segment store reopened by new | all three readable, `verify -deep` OK, `gc` keeps all | test `TestP1C_OldStoreAndPolicyChangesCoexist` |
| 4 | new store reopened by new, clean close | small segment re-adopted (no tiny pack per restart) | test `TestP1C_SmallPutUsesSegmentAndSurvivesRestart` |
| 5 | new store reopened by new, unclean (no close / SIGKILL) | torn tail cut and quarantined, acknowledged data intact | tests `TestP1C_RecoveryCutsTornTailsAndKeepsAcknowledgedRecords`, `TestP1B_RealKill_*/segments-small-objects` |
| 6 | crash between `FORMAT.json` rewrite and first segment file | valid level-11 store without segments | test `TestP1C_CrashMatrix_Segments` (boundaries `seg-created`, `seg-dir-synced`), `TestP1C_CrashStateModel` (`new-segment-dirent-lost`) |
| 7 | crash while creating the first segment (file or directory entry not durable) | file removed by recovery or absent; nothing acknowledged depended on it | same |
| 8 | old binary opens a level-11 store | **refuses** (`unsupported store format version 11`) -- an explicit compatibility cost | evidence (`compat_old_binary.txt`) |
| 9 | old binary opens a store that new only *opened* (never wrote a small object) | opens, level unchanged | evidence |
| 10 | level 10 (reserved, draft #97) store opened by new | refuses (`supportedStoreFormat(10)` is false); level 11 does not imply 10 | test `TestP1C_Format_*` |
| 11 | policy turned off (`ZEROS3_SMALL_OBJECT_STORAGE=off`) after segments exist | segments and inline manifests readable, recovered, sealed, maintained; new writes loose; level stays 11 | test `TestP1C_OldStoreAndPolicyChangesCoexist` |
| 12 | policy off from the start | level never raised, no `segments/`, no `segments.lock` taken for writing | test `TestP1C_Format_*/policy-off` |
| 13 | sealed segment vs pack-v1 | byte-valid pack-v1: `verifyPackFile` passes, name = body SHA-256 | test `TestP1C_RolloverSealsIntoVerifiablePacks` |
| 14 | crash during upgrade or at any seal boundary | reopens; no acknowledged byte lost; both names present resolved | tests `TestP1C_CrashMatrix_Segments`, `TestP1C_CrashStateModel` |
| 15 | read-only openers (`stats`, `verify`, `doctor`, `versions`, `repair`, bundle export) while a server writes | never truncate, adopt, seal or create a segment; index the durable prefix | test `TestP1C_SharedOpenerNeverDisturbsALiveWriter` |
| 16 | manifests above 64 KiB | stay `manifests/<uuid>.json` files; bundle import also writes files | test `TestP1C_LargeManifestStaysAFile`, bundle round-trip tests |
| 17 | bundles / sync / replication / peer repair | work through the CAS abstraction (`casRead`, `readVerifiedManifest`); imported chunks and manifests are loose files, which every reader accepts | full root suite, harness groups `s3`, `sync`, `client` |
| 18 | tiering | a sealed segment is an ordinary pack: `movePack`, `rebalance` carry inline manifest records with their pack | test `TestP1C_TierMoveCarriesInlineManifests` |
| 19 | B2 representation-authority draft (#97, level 10) | untouched; its level is not consumed; a store written by both would need a merge-time decision on the level ordering | design note in the report |

Format-floor rule used: the level is raised **immediately before the first segment file is created**, after the tier roots that any level above 5 implies; never lowered; never raised for stores that never create a segment.
