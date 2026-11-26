# C15 · Index size, churn and freshness

Freeze 29cb2d5 as the native control. C7 proved that a bounded target can
recover useful bases from verified posting runs, but every run pays a
256 KiB directory. Small append runs and new generations pay this
overhead even with a single live addition. Analyze adaptive radix width,
sparse full-width directories and an unindexed small-run layout under
the same 64-bit features and newest-candidate budget.

Prefer a compatible reader plus a smaller derived run when it preserves
exact posting order, recall and resource limits. Ensure a previous
binary falls back safely when it cannot open a new derived run. Price
full immutable verification, incremental catalog writes, query reads,
foreground work and retained allocation together. Keep authority content,
the namespace journal and table formats unchanged.

Exercise 64 and 1,024 unrelated objects, a long real revision history,
a compressed cross-format control and larger metadata-only scale.
Use C7's query and namespace fixtures as comparators. Verify scope,
tombstones, copy/multipart/snapshot churn, compaction across format
generations, corruption fallback and cut/restart around publication.
Report when whole-catalog or journal verification dominates.
