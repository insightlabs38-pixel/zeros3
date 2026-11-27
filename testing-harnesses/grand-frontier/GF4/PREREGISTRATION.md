# GF4 workload and maintenance policy

Compare static CDC, static archive and archive with a fixed hot set,
an LRU materialization policy and a cost-aware hysteretic controller.
Use a small offline dynamic program as an exact future-aware comparator
under the same capacity and rewrite constraints. Freeze a finite objective
that prices read service time, live hot allocation and materialization.
Keep complete SLO misses and byte counters available beside the objective.

Replay stable small hot sets, rotations, unexpected concentration,
archive/restore bursts, retention pruning, maintenance with replication,
and write/memory pressure. Separate native measured costs from generated
trace conditions and model assumptions. Estimate online demand using only
completed observation windows. Reject expensive copies whose expected
gain does not repay construction within the lease. Test forecast mistakes,
repetition, pressure, no-gain regimes, and exact sparse data under
concurrent native foreground requests.

Advance opt-in whole-engine work only if the measured admission and
foreground-tail tradeoffs improve on the best static policy or a simple
cache. A projected win alone is insufficient.
