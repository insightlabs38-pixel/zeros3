# C19 evidence

`general.jsonl` identifies engine, reversed trial, operation and object
cohort. Read rows include verified user metadata. Restart rows describe
a fresh process with the same filesystem. `qos.jsonl` includes the timed
overlap of signed foreground load, authority import, online maintenance
and restore. Intervals are relative to foreground trial start.

`retention.jsonl` has fixture rows with portable input SHA-256 values and
consumer digests. Trial rows cover ingest, planner, each finalization
phase, physical allocation, signed reads and exact verification. Pair
zero includes an independent bundle import and restore on C9 for each
integrated history. Volume pruning verifies the surviving objects.
`backup.jsonl` holds independent encrypted filesystem repository
measurements and exact digest-tree restores.

`transport.jsonl` separates one-time source preparation from transfer.
Wire bytes sum client-to-server and server-to-client counters on both
legs. `transport-merged.jsonl` repeats the exact and ordinary-prior
cases after the later FB-02 merge. `openxet.jsonl` reports that lane's
interface counters and keeps client caches explicit. `codec.jsonl`
records patch size and exact full decode against the same source bytes.

Allocation is in physical bytes. Linux process CPU is in seconds.
Signed request quantiles are in milliseconds. Recompute ranges and
medians with `../summary.py`. `summary.jsonl` contains metric medians,
observed extrema and sample counts for each scoreboard row. The
acceptance logs and catalogs identify the frozen root partitions.
Merged integration logs cover peer equivalence. Landing logs cover
the later restore-session and pack locator changes as well.
