# C7 · Incremental candidate discovery

The existing disk index has checksummed posting runs, a catalog, staged
publication, tombstones and append/refresh reconciliation. The per-object
migrator can query it. The global optimizer's target limit also limits its
in-memory base pool, so a one-target pass can miss an earlier version.
Hypothesis: a verified posting probe can restore same-bucket base recall
over existing live content, preserving target bounds and the full planner's
retained-byte quality on a version-shaped workload.

Compare a full optimize, a one-target pass, the C2 completed-pass memo on
unchanged input, and a one-target pass with the disk index on a new revision.
Measure index opening, append refresh, deletion reconciliation, candidate
quality, exact finalization, CPU and allocated bytes. Use unrelated objects
as a negative control. Sample 1k, 10k and 100k namespace entries using
existing journal-only probes. Keep the measured index scale distinct from
that metadata-only curve.

Require a verified store and journal-prefix identity, bounded per-feature
postings, current live graph membership, matching size and bucket, and
independent encoder/proof checks. Preserve the existing durable index and
root publication. Missing or corrupt hints must leave local discovery and
ordinary object reads available.
