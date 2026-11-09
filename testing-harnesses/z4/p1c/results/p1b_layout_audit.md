# P1B layout-dependent tests: disposition under P1C

P1B hid the production default from the root suite behind a global threshold override. The 76 top-level tests that failed once it was removed are audited here (reproduced with `ZEROS3_TEST_MEDIUM_PACK=1` on `5b9bcdd`: 76 top-level, 155 incl. subtests). Disposition after P1C:

* **30 layout-independent**: the behaviour (detect missing/corrupt chunk, repair, restore, GC refusal, verify) is unchanged; the fault is now injected by `removeChunkAnyLayout` / `corruptChunkAnyLayout` into whichever representation holds the chunk, so they run on the production default (segments / packs) **and are independently qualified on loose files** by `TestLayoutMatrix_*`.
* **46 explicit loose**: the assertion is about the loose representation itself (counts loose files, reads or rewrites a chunk file by path, converts loose chunks with compact/repack, exact store-format levels). They call `useLooseLayout(t, reason)`; the reason is recorded in the layout matrix. The production-default counterparts are listed per reason in the P1C report.

## Layout-independent

* `TestVerify_DetectsMissingChunk`
* `TestVerify_DetectsCorruptChunkOnlyUnderDeep`
* `TestReachability_DetectsCorruptionAmongLiveRoots`
* `TestRestore_MissingHistoricalChunk_NoPartialMutation`
* `TestRepair_MissingChunkDetected`
* `TestRepair_CorruptChunkContentMismatchDetected`
* `TestRepair_MultipleCorruptChunksDetected`
* `TestRepair_SharedCorruptDigestAffectsAllReferencingObjects`
* `TestRepair_MissingChunkRestoredEndToEnd`
* `TestRepair_CorruptExistingChunkAtomicallyReplaced`
* `TestRepair_RestartThenGetExact`
* `TestRepair_VersionsMetadataAndETagUnchanged`
* `TestRepair_StatsExactAccounting`
* `TestRepair_PartialRepairPeerMissingSomeChunksReportsHonestly`
* `TestRepair_ResumeAfterPartialRunOnlyFetchesRemaining`
* `TestRepair_LocalRestartResumesNaturally`
* `TestRepair_PeerRestartBetweenAttempts`
* `TestRepair_ConcurrentGetOnUnrelatedObjectDuringRepair`
* `TestRepair_RetainedHistoricalVersionChunkRepaired`
* `TestRepair_MultipartInProgressPartChunkRepaired`
* `TestCLI_Repair_EndToEndSmoke`
* `TestCLI_VerifyRepairFrom_OneCommandDetectRepairReverify`
* `TestCLI_Repair_RealProcessKillThenResume`
* `TestFork_RepairComposesAcrossSharedCorruptChunk`
* `TestRepair_ManyCorruptChunks_WorkerCountsProduceIdenticalStats`
* `TestRepair_ManyMissingChunks_Concurrent`
* `TestRepair_MixedCorruptAndMissing_Concurrent`
* `TestRepair_OnePeerFailureAmongManySuccesses_Concurrent`
* `TestRepair_WrongPeerBytesAmongMany_Concurrent`
* `TestRepair_HighWorkerCount_RaceAndCorrectness`

## Explicit loose (with reason)

| test | reason |
|---|---|
| `TestRepCrashMatrix` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepOptionalFailureNeverFailsPut` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepLiveRootFaultsFailClosed` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepFailureAfterOutputStartsDoesNotPanic` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepVirtualComponentDescriptor` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepSourceCorruptionClassified` | derived-representation fixture inspects and removes loose chunk files |
| `TestRepUpgradeLevelsDoNotMoveUntilFirstRoot` | derived-representation fixture inspects and removes loose chunk files |
| `TestGC_RefusesOnCorruptLiveRoot_K7` | removes and later restores the loose chunk file by path |
| `TestGC_AbortedMultipartBecomesCollectible` | counts or lists loose chunk files / loose gc granularity |
| `TestPrune_SnapshotSurvivesPruneThenGC` | counts or lists loose chunk files / loose gc granularity |
| `TestPrune_SharedAndRestoredRootsStayPinned` | counts or lists loose chunk files / loose gc granularity |
| `TestRepair_WrongLengthChunkDetectedAsCorrupt` | asserts on the loose chunk file itself (length, mtime/identity) |
| `TestRepair_HealthyChunkNeverRewritten` | asserts on the loose chunk file itself (length, mtime/identity) |
| `TestFork_LargeMultiChunkObject_ZeroPayload` | counts or lists loose chunk files / loose gc granularity |
| `TestFork_DestinationEditedAfterFork_SourceUnchanged_OnlyNewChunksAdded` | counts or lists loose chunk files / loose gc granularity |
| `TestConditionalPut_IfMatch_SpeculativeCASBecomesGCCollectibleAfterFailure` | counts or lists loose chunk files / loose gc granularity |
| `TestIngestCrash_Matrix` | exercises the loose publication and crash points |
| `TestStreamingGet_HeadAndConditionalsSkipPayload` | exercises the loose publication and crash points |
| `TestStreamingGet_Corruption` | exercises the loose publication and crash points |
| `TestPack_PhysicalMatrix` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_MixedManifestSpansLooseAndPacked` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_CompactPreservesLogicalState` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_DryRunAndZeroCandidatesWriteNothing` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_StoreFormatVersionGate` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_GCNeverTouchesPacks` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_StatsAndVerifyReportPhysicalLayout` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_CompactCrashPoints` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPack_CompactCLI` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_NoPacksAndLooseOnlyStores` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_DuplicatePackedLocations` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_LooseCopyCountsAsSurvivingCopy` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_CrashPoints` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_AbortsWhenASourcePackIsUnusable` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_AbortsOnCorruptLiveSource` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_AbortsWhenReplacementFailsValidation` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestRepack_ConflictingPackedCopiesBlockDestructiveWork` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_AdaptivePolicyByDataClass` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_MixedObjectReadsAndRanges` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_StoreFormatBoundary` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_DeepVerifyDetectsCompressedPayloadCorruption` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_CompactOffWritesRawAndRerunIsStable` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_PhysicalAccounting` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_RepackSelectionUsesPhysicalBytes` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_RepackMatrix` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_RepackAbortsOnDamagedCompressedData` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
| `TestPackCompression_CrashPoints` | fixture is loose chunks that compact/repack convert (the packed default is covered by the direct-pack and TestLayoutMatrix tests) |
