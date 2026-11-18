#!/usr/bin/env python3
"""G1 ledger: the semantic coverage audit of the retire-everything qualification.
usage: g1_ledger.py OUT.json [--evidence T1_EVIDENCE.json ...]   (from the repository root)

Every root test that asserts on the CDC chunk layout calls g1CDCSpecific(t, class) and runs with CDC authoritative
under the hook. This script (1) assigns each such test the invariants (tags) it exercises, by explicit rules over its
name, and fails on a test no rule explains; (2) collects the tags the authority-mode tests declare with
g1Cover(t, "tag", ...) (the call sits after the assertions of its scenario); (3) fails when a tag required by an
exempt test is declared by no test; (4) with --evidence (the t1run evidence of the authority-native run), fails when
a declaring test did not run and pass. Nothing here names an "equivalent" test: coverage is the set of declarations
in the authority suite, and the evidence says whether the assertions behind them held."""
import glob, json, re, sys

# (regex on the test name without "Test", tags). First match adds its tags; every test must match at least one rule.
RULES = [
 # corruption and repair
 (r"Repair_.*(Concurrent|WorkerCount|HighWorker)", ["repair-concurrent", "repair-peer-exact-stats"]),
 (r"Repair_(Resume|LocalRestart|PeerRestart)", ["repair-resume"]),
 (r"Repair_(Partial|OnePeer|WrongPeer)|RepairFromPeer_Incompatible|BulkRepair_Partial|BulkFetch_", ["repair-partial-honest"]),
 (r"Repair_.*(Shared|Retained)|Fork_Repair", ["repair-shared-digest"]),
 (r"^(CLI_Repair|CLI_VerifyRepair|Repair_|Pack_RepairReplaces|LayoutMatrix_Repair)", ["repair-peer-exact-stats", "missing-component-refused"]),
 (r"^(Verify_|LayoutMatrix_Verify|Reachability_|P3_(DeepVerify|Reachability)|PackCompression_DeepVerify)", ["verify-deep-finds-corruption", "missing-component-refused"]),
 (r"^(GC_Refuses|P1C_MaintenanceRefuses|PackCompression_RepackAborts)", ["gc-refuses-corrupt-record"]),
 (r"^P2_(CachedEntries|ChunkCache)", ["cache-revalidated"]),
 (r"^(P2_Pipelined|StreamingGet_|PackRun_Corruption|PackRun_HTTP)", ["stream-failsafe", "damage-fails-closed"]),
 (r"^Pack_(Payload|Duplicate)", ["damage-fails-closed"]),
 (r"^(Bundle_|BundleDelta_|P1C_BundleExport)", ["export-refuses-corrupt", "repair-peer-exact-stats"]),
 (r"^Restore_Missing", ["restore-missing-no-mutation"]),
 (r"^ReplicateNamespace_", ["damage-is-isolated"]),
 # physical layout: packs, tiers, locality, direct packs, segments
 (r"^(Pack_|PackCompression_|PackRun_|P1B_|DirectPack|Locality_|TierPolicy_|TierRebalance_|Tier_|P1C_Small|P4SmallObject|RepLargeObject|P2_Pipelining|Bundle_ExportIndependent)", ["layout-compact-tier-repack-exact"]),
 (r"CrashPoints|CrashMatrix|Crash_|_Crash", ["maintenance-crash-converges"]),
 (r"^(PackRun_Range|PackRun_Locality|Locality_)", ["range-reads-bounded"]),
 (r"^(Pack_StoreFormatVersionGate|P1C_Format|P1C_OldStore|PackCompression_StoreFormat|Tier_CLI|Tier_Init)", ["format-level-moves-with-records"]),
 # store level
 (r"^(DirectPack_Adaptive|ModelNested|P1C_|PackCompression_StoreFormat|Rep(Managed|Structured|Negative|Smoke|Substrate|Upgrade|Z4Managed|CrashAfterFormat)|Tier_|TierRebalance_DryRun)", ["format-level-moves-with-records", "format-stable-under-maintenance"]),
 # accounting
 (r"^(DeleteObjects_|Prune_|Repack_GC|SnapshotGC_|P3_Repack|IngestCrash)", ["gc-counts-components", "snapshot-pins-after-gc", "maintenance-crash-converges"]),
 (r"^P3_ReachabilityMatches", ["gc-counts-components"]),
 # representation lifecycle that read CDC chunks around the authority
 (r"^Rep(KindLifecycle|Lifecycle|LateCreation|OffMode|SharedChild|SharedVirtual|TempFiles|Structured(Fallback|Ordinary)|Merkle|VirtualComponent|CopyObject|Concurrent)", ["rep-lifecycle-exact", "rep-shared-components"]),
 (r"^Rep(Crash|Managed.*Ordinary|StructuredOrdinary|ModelOrdinary)|^ModelOrdinary", ["rep-crash-publication"]),
 (r"^Rep(Hostile|Git|AnalysisBudgets|CodecAnalysis|SourceCorruption|LiveRootFaults|Z4ModelGGUFAndUnsupported|OptionalFailure)", ["rep-hostile-fallback", "damage-fails-closed"]),
 (r"^RepZ4ModelBase", ["rep-model-base-survives-retirement"]),
 (r"^RepLiveRootFaults|^RepSourceCorruption", ["damage-fails-closed", "missing-component-refused"]),
 # hook class: write paths and caches around the commit
 (r"^(AWSChunked_|StreamingPut_)", ["large-streaming-put-bounded-memory"]),
 (r"^(P4Deferred|P4ReadCache)", ["writes-and-reads-stay-exact-under-migration"]),
 (r"^Rep(BundleBackfill|Codec|KindMatrix|ModesAnd|Z4ModelStreaming)", ["rep-lifecycle-exact", "writes-and-reads-stay-exact-under-migration"]),
 # revive on write
 (r"^(Bundle_PartialOverlap|CASBatch_)", ["revive-on-write"]),
]
# the classes of the g1CDCSpecific labels, for the report
CLASSES = {"corruption", "layout", "storelevel", "accounting", "cdcref", "hook", "revive"}
# always required beyond the exempt tests: the differential and retirement checks
ALWAYS = ["retirement-physical", "differential-reads", "differential-errors", "differential-metadata"]

tests = {}
for f in sorted(glob.glob("zeros3*_test.go")):
    cur = None
    for line in open(f):
        m = re.match(r"func (Test\w+)\(", line)
        if m: cur = m.group(1)
        m = re.search(r'g1CDCSpecific\(t, "(\w+)"\)', line)
        if m and cur: tests[cur] = m.group(1)
bad = [c for c in set(tests.values()) if c not in CLASSES]
need, unexplained, by_class = {}, [], {}
for t, c in sorted(tests.items()):
    tags = []
    for rx, tg in RULES:
        if re.search(rx, t[4:]):
            tags += [x for x in tg if x not in tags]
    if not tags:
        unexplained.append(t)
    need[t] = tags
    by_class.setdefault(c, []).append(t)

declared = {}   # tag -> [authority test functions]
for f in sorted(glob.glob("zeros3*_test.go")):
    cur = None
    for line in open(f):
        m = re.match(r"func (Test\w+)\(", line)
        if m: cur = m.group(1)
        for call in re.findall(r"g1Cover\(t, ([^)]*)\)", line):
            for tag in re.findall(r'"([^"]+)"', call):
                declared.setdefault(tag, [])
                if cur and cur not in declared[tag]: declared[tag].append(cur)
required = sorted({tg for v in need.values() for tg in v} | set(ALWAYS))
missing = [t for t in required if t not in declared]

passed = None
if "--evidence" in sys.argv:
    passed = set()
    for ev in sys.argv[sys.argv.index("--evidence") + 1:]:
        for r in json.load(open(ev)).get("results", []):
            if r["status"] == "pass": passed.add(r["test"])
not_run = []
if passed is not None:
    for tag in required:
        if tag in declared and not any(fn in passed for fn in declared[tag]):
            not_run.append(tag)

out = {"cdc_specific_tests": len(tests),
       "classes": {c: {"tests": v, "tags": sorted({tg for t in v for tg in need[t]})} for c, v in sorted(by_class.items())},
       "tests": {t: {"class": tests[t], "tags": need[t]} for t in sorted(tests)},
       "required_tags": required,
       "declared": {t: declared[t] for t in required if t in declared}}
json.dump(out, open(sys.argv[1], "w"), indent=1)
print({c: len(v) for c, v in by_class.items()}, len(tests), "tests;", len(required), "invariants,", len(declared), "declared")
fail = False
for what, items in (("unexplained exempt tests (no rule)", unexplained), ("unknown classes", bad), ("invariants no authority test declares", missing), ("invariants whose declaring tests did not pass in the evidence", not_run)):
    if items:
        fail = True
        print("FAIL", what + ":", items)
sys.exit(1 if fail else 0)
