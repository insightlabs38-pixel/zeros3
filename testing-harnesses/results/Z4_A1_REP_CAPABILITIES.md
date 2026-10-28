# Z4 A1 representation capabilities — active checkpoint

A0 merged in #90, main 3daab82. A1 is active, not READY.
No production change yet; private candidates reuse immutable draft sources
mechanically, changing only supported store-format ceiling and root publication.

Design A has a measured counterexample: I1 doctor retires valid kinds 8–12.
Design C cannot protect existing I1 with a capability file it ignores; it still
needs a format floor and would introduce a second mechanism.
Preselected Design B candidate: cumulative floors 7=Model, 8=SST/Parquet,
9=managed. Earlier Z4 binaries must refuse later tranches. Root traversal must
include nested derived kinds, not just the outer Concat header. Unsupported
kinds/ceilings reject before format change or root publication. Floors persist
after retirement; ordinary creation remains off by default.

Required matrix: pre-I0, I0, I1C and new-kind candidates; exact GET/ranges,
stats, verification/deep, GC dry/apply, compact/repack, tiers, full/delta bundles,
sync/replicate, doctor/retire, history/snapshot prune, and capability crash edges.
Remote sync/replicate clients operate through a compatible server and cannot
be treated as direct store openers. Distinguish lock-owner metadata from durable
payload/namespace/root mutation. Invalid CLI flags never count as safe refusal.

A1 will not enable a future floor in main before its decoder tranche is ready;
otherwise an intermediate binary would accept a store it cannot understand.
A2/A3/A4 must carry the proven publication contract and earn their ceilings.
