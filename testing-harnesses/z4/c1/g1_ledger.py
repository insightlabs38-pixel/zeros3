#!/usr/bin/env python3
"""G1 ledger of the retire-everything qualification, generated from the sources (no failing run involved).
usage: g1_ledger.py OUT.json   (from the repository root)
Every root test that asserts on the CDC chunk layout calls g1CDCSpecific(t, class); under the hook it runs with CDC
authoritative. EQUIV names the authority-mode tests that cover each class; the authority stage requires them to pass."""
import glob, json, re, sys
EQUIV = {
 "corruption": ["TestAuthorityRetiredEquivalents", "TestAuthorityCorruptComponentFailsClosedNeverWrongBytes", "TestAuthorityPeerAssistedComponentRepair", "TestAuthorityRepairPaths", "TestAuthorityRepairCrashQualification", "TestAuthorityGCFailsClosedOnMissingBase"],
 "layout": ["TestAuthorityEquivPhysicalLayouts", "TestAuthorityMixedLifecycle", "TestAuthorityDeltaLifecycleAndReclaim"],
 "storelevel": ["TestAuthorityEquivStoreLevel", "TestAuthorityOldBinaryRefusesAndLeavesStoreUntouched", "TestAuthorityRevertAllLowersFloorAndOldBinaryReopens"],
 "accounting": ["TestAuthorityEquivAccounting", "TestAuthorityBaseRetentionAfterNamespacePruning"],
 "cdcref": ["TestAuthorityRepFormAcrossStructuralKinds", "TestAuthorityPlannerRepGroupsExact", "TestAuthorityZN7ModelHistory"],
 "hook": ["TestAuthorityLargeCheck", "TestAuthorityRepFormAcrossStructuralKinds"],
 "revive": ["TestAuthorityCopyAndRestoreOfBodyOnlyContent", "TestAuthorityNativeClosureTransferAndInterruption"],
}
tests = {}
for f in sorted(glob.glob("zeros3*_test.go")):
    cur = None
    for line in open(f):
        m = re.match(r"func (Test\w+)\(", line)
        if m: cur = m.group(1)
        m = re.search(r'g1CDCSpecific\(t, "(\w+)"\)', line)
        if m and cur: tests[cur] = m.group(1)
by = {}
for t, c in tests.items(): by.setdefault(c, []).append(t)
out = {"cdc_specific_tests": len(tests), "classes": {c: {"tests": sorted(v), "authority_equivalents": EQUIV[c]} for c, v in sorted(by.items())}}
json.dump(out, open(sys.argv[1], "w"), indent=1)
print({c: len(v) for c, v in by.items()}, len(tests))
