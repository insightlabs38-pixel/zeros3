# Opt-in persistent candidate discovery

The offline migration flag `-indexed-discovery` allows the existing planner
to see similarity postings that were unavailable from its in-memory sketch
set. Each returned ID still resolves to a verified root in the requested
scope and the same bucket as the target. The existing eligibility, physical
pricing and exact admission checks apply. An unavailable or invalid index
leaves the existing planner available.

A local S3 fixture used three distinct keys in a single bucket. The base was
1,550,000 deterministic bytes from Python `Random(593418).randbytes`. The
second key replaced offsets [620000, 740000) with `Random(22).randbytes` and
became authoritative before the third key was uploaded. The third key
replaced offsets [1100000, 1110000) in the second with
`Random(223).randbytes`. Without opt-in discovery, the final plan kept the
target in CDC. With discovery enabled, two extra candidates were found and the
planner used the second key as a depth-2 base. Its plan reported 10,361 added
bytes. The migration used archive policy, one base per delta and no rep or ZN7
alternatives.

After applying the indexed plan, GC and repack completed, deep verification
passed, and a separate server returned the exact target SHA-256. Another
fixture with the entire derived index moved away produced the same plans as
the default planner and reported fallback. The logs cover functional behavior
on controlled fixtures. Real-history recall, physical savings on comparable
stores and scale remain open.

The archived CLI replay predates the catalog-read and hot-run lookup
optimizations. Focused race tests on the later implementation cover bucket
and prefix scope, transparent lookup failure, and compaction-equivalent
hot-feature results.
