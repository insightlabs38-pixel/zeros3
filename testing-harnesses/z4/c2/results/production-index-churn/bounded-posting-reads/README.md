# Bounded reads for hot posting lists

A verified 88-posting bucket now takes one ReadAt. A dense 1,002-posting
bucket takes at most 11 ReadAt calls to return the newest 64 matching
IDs. The focused tests cover neighboring and absent features in the
same bucket, tombstones across runs, compaction, and corruption.

The production benchmark used the same published 26,624-object fixture
and its four hottest features on both builds. Both returned 64
candidates per lookup. Allocation fell from 1,824 bytes and five
allocations per call to 1,808 bytes and four allocations. The median
wall time was 236 µs before and 335 µs after, with substantial spread
during the running scale qualification. This pair shows an allocation
reduction and an observed latency regression under variable load.
