#!/usr/bin/env python3 -I
"""G1 failure ledger: `go test -json` of the retire-everything run -> one record per failing top-level test.
usage: g1_ledger.py RUN.json STACKS.txt OUT.json   (STACKS.txt: ZEROS3_G1_STACKS output of the same run)
Categories (cause):
  layout      the test asserts the CDC physical layout (loose/packs/tiers/format level/locality): intentional contract of the CDC store
  corruption  the test damages/deletes a chunk that the hook retired: it targets a copy no reader consults any more
  cdcref      the test reads the CDC chunks as the reference for a logical result (representation/codec lifecycle tests)
  hookside    side effect of the retire-everything hook itself (format level raised by the body, heap of the hook, order of hook calls)
Each record names the equivalent authority-backed test that proves the same logical guarantee after real retirement."""
import json, re, sys, collections

EQ_C = "TestAuthorityRetiredEquivalents (component damage after physical retirement) + TestAuthorityRetireEverythingSubprocess"
EQ_R = "TestAuthorityRepFormAcrossStructuralKinds + TestAuthorityRetireEverythingSubprocess (representation/body read through the production reader)"
EQ_L = "original test, unchanged, in default mode (CDC contract) + TestAuthorityRetireEverythingSubprocess (physical state after real retirement)"
# (cause, test-name regex, message regex or None, expected behaviour, equivalent authority-backed proof)
RULES = [
 ("hook", r"BoundedMemory", None, "heap growth bounded while streaming a large body", "TestAuthorityLargeCheck + run_large.sh (RSS of independent processes)"),
 ("corruption", r"^Test(Repair_|RepairFromPeer|CLI_Repair|CLI_VerifyRepairFrom|BulkRepair|Pack_RepairReplaces|LayoutMatrix_Repair|Fork_Repair)", None, "a damaged/missing chunk is found and repaired from a peer; stats exact", "TestAuthorityRetiredEquivalents/peer repair + AuthorityPeerAssistedComponentRepair + subprocess repair"),
 ("corruption", r"^Test(Verify_|Reachability_Detects|LayoutMatrix_Verify|P3_DeepVerify|P3_ReachabilityMatchesReference_Damage|GC_Refuses|P1C_MaintenanceRefuses|PackCompression_(DeepVerify|RepackAborts)|Restore_Missing|P1C_BundleExport|Bundle_ExportIsDeterministic)", None, "damage to a live chunk is detected and refuses destructive maintenance/export", "TestAuthorityRetiredEquivalents/missing, corrupt component"),
 ("corruption", r"^Test(StreamingGet_Corruption|PackRun_(HTTPCorruption|CorruptionFalls)|P2_(PipelinedStreams|ChunkCacheNever|CachedEntries)|Pack_(PayloadAndRecord|DuplicateCopies)|ReplicateNamespace_SourceChunkCorrupt|BulkFetch_ServerSide|BundleDelta_PayloadRepairs|Bundle_CorruptExisting)", None, "damaged bytes never reach a client or destination", "TestAuthorityRetiredEquivalents/corrupt component (never wrong bytes) + CorruptComponentFailsClosed"),
 ("storelevel", r"", r"format|FORMAT|floor|level|version \d|Model floor|capability", "store level moves only with the first record of a kind", "TestAuthorityFiveGenerationMatrix + RevertAllLowersFloor + TestAuthorityOldBinaryRefuses (levels 10/12 recorded by FORMAT.json)"),
 ("cdcref", r"^Test(Rep|Model)", r"CDC: chunk read failed|CDC:|chunk read failed|descriptor chunk missing|object not in source|CDC fallback", "the CDC chunk list is read as the reference for a representation result", EQ_R),
 ("hook", r"^TestRep(ModesAndFallbackCounters|BundleBackfill|CodecNested|CodecUnsupported)|^TestP4(DeferredBuilds|ReadCache)", None, "representation counters assume CDC-served reads", "the body, not the sidecar root, serves the read: TestAuthorityRepFormAcrossStructuralKinds"),
 ("accounting", r"^Test(Prune_|DeleteObjects_|IngestCrash|SnapshotGC|P3_ReachabilityMatchesReference_Healthy|Repack_GC|P3_Repack)", None, "gc/reachability counters count CDC chunks of body-owned objects", "TestAuthorityRetiredEquivalents/snapshots pin retired content + BaseRetentionAfterNamespacePruning"),
 ("revive", r"^TestCASBatch|^TestBundle_PartialOverlap", None, "dedup against a chunk that no longer exists writes it again (revive-on-write, by design)", "TestAuthorityRetireEverythingSubprocess (re-PUT, sync and import of retired content stay exact)"),
 ("layout", r"^Test(Pack|DirectPack|Locality|Tier|P1B|P1C|P4Small|P2_Pipelining|PackCompression|RepLargeObject|Bundle_ExportIndependent|Repack)", None, "CDC physical layout (loose/packs/tiers/locality/compression)", EQ_L),
]
OVERRIDE = {
 "TestRepKindMatrixRoundTrip": ("hook", "deep verify of a representation store whose sidecar roots still reference the CDC chunks the hook hid", "the hook retires chunks that additive representation roots keep live; gc never would: TestAuthorityRepFormAcrossStructuralKinds"),
 "TestRepZ4ModelStreamingModelLifecycle": ("hook", "derivation hop counts read through the representation root", "the body serves the read, so no derivation hop: TestAuthorityRepFormAcrossStructuralKinds"),
}
# production call chains that touched a hidden chunk (ZEROS3_G1_STACKS) and why each is safe
PATH_RULES = [
 (r"casStatOrVirtual|casReadOrVirtual|authAvailable", "virtual-aware", "presence/read falls back to the authoritative body of a live object that lists the chunk"),
 (r"packSink|segSink|casBatch|casPublishClaimed|bundleImporter|handleBulkUpload|storeRepSource|ingestStream|repBuilder|casStat$", "write-side probe (revive-on-write)", "a retired chunk reads as absent, so the writer stores it again; never relies on a virtual copy for a CDC-authoritative manifest"),
 (r"handleSyncNegotiate", "virtual-aware", "negotiation counts a virtual chunk as present; handleSyncCommit materializes it before publishing a CDC manifest"),
 (r"checkChunk|verifyWholeObjects|repairFindings|computeReach", "maintenance, authority-aware at the root", "chunk checks run only for chunks of CDC-authoritative roots or authoritative components; the hook also hid chunks of additive representation roots, which gc would keep"),
 (r"manifestReader", "hook ordering artifact", "a CDC-authoritative manifest read a chunk the hook hid because another manifest sharing it committed first; physical retirement happens only in offline gc after liveness is recomputed (TestAuthorityRetireEverythingSubprocess)"),
 (r"casReadH|casReadExcluding|casRead", "repair/pack tests reading CDC directly", "tests that read or corrupt a CDC chunk on purpose (ledger: corruption/layout)"),
]
def main(run, stacks, out):
    fails, outp = {}, collections.defaultdict(list)
    ran = 0
    for l in open(run):
        try: e = json.loads(l)
        except Exception: continue
        t = e.get("Test")
        if not t: continue
        if e["Action"] == "output": outp[t].append(e["Output"])
        elif e["Action"] in ("pass", "fail", "skip") and "/" not in t: ran += 1
        if e["Action"] == "fail": fails[t] = 1
    leaf = [t for t in fails if not any(o != t and o.startswith(t + "/") for o in fails)]
    tops = collections.OrderedDict()
    for t in sorted(leaf): tops.setdefault(t.split("/")[0], []).append(t)
    stk = []
    for l in open(stacks):
        m = re.match(r"\s*(\d+)\s+(.*?)\s+\[(.*)\]\s*$", l)
        if m: stk.append((int(m.group(1)), m.group(2), m.group(3)))
    recs = []
    for top, subs in tops.items():
        msgs = [x.strip() for t in subs for o in outp[t] for x in o.splitlines() if re.search(r"_test\.go:\d+:", x)]
        first = msgs[0][:240] if msgs else ""
        key = top + " " + first
        cause, exp, eq = "other", "", ""
        for c, nrx, mrx, ex, e in RULES:
            if re.search(nrx, top) and (mrx is None or re.search(mrx, first)):
                cause, exp, eq = c, ex, e; break
        if top in OVERRIDE: cause, exp, eq = OVERRIDE[top]
        st = [s[1] for s in stk if top in s[2]][:2]
        recs.append({"test": top, "subtests_failed": len(subs), "cause": cause, "stack": st, "expected": exp, "observed": first, "equivalent": eq})
    paths = []
    for n, chain, tests in stk:
        why = next(((v, w) for rx, v, w in PATH_RULES if re.search(rx, chain)), ("unclassified", ""))
        paths.append({"hits": n, "chain": chain, "tests": tests, "verdict": why[0], "reason": why[1]})
    json.dump({"tests_run": ran, "failing_top_level": len(recs), "causes": dict(collections.Counter(r["cause"] for r in recs)), "records": recs, "retired_chunk_paths": paths}, open(out, "w"), indent=1)
    print("unclassified paths:", [p["chain"] for p in paths if p["verdict"] == "unclassified"])
    print(len(recs), collections.Counter(r["cause"] for r in recs))
main(*sys.argv[1:4])
