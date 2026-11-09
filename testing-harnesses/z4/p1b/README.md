# Z4-P1B evidence: durable publication and small/medium-object ingest

Report: [`../../results/Z4_P1B_DURABLE_PUBLICATION.md`](../../results/Z4_P1B_DURABLE_PUBLICATION.md). Driver, content generator and byte-exact
verification are the unchanged Z4-P0 `p0bench` (`../p0/`): every repetition is a fresh store and process and every PUT is replayed in a
fresh process and compared byte for byte.

| Path | Purpose |
|---|---|
| `DURABILITY_MODEL.md` | ordering of one PUT, what each durability state means, which steps may be shared/reordered, failure semantics, proof sketch for the per-digest claims |
| `P1C_PROPOSAL.md` | storage-layout proposal (log-structured segments that seal into pack-v1) with prototype evidence |
| `pubsim/` | design-comparison simulator for the exact filesystem operations of a small PUT (lock mode, dir-fsync sharing, journal group commit, chain overlap, syncfs, staging shards, segment layout) |
| `scripts/build.sh ROOT OUT` | plain + P0-overlay-profiled production binaries and `p0bench` |
| `scripts/ab.sh OUT.jsonl ROUNDS SCENARIOS name=dir...` | interleaved A/B runner (each round runs every scenario once per variant) |
| `scripts/scenarios_p1b.txt` | the scenario matrix (4 KiB .. 64 MiB, multipart, duplicates, revisions) |
| `scripts/audit_order.py`, `scripts/run_audit.sh` | durability-ordering audit of a real `strace -y` of the server (R1 staged-file fsync before rename, R2 directory fsync before the dependent journal frame, R3 parent fsync before use of a new shard directory, R4 journal fsync) |
| `scripts/syscount.sh` | per-object syscall counts from a full server trace |
| `scripts/report_tables.py` | markdown tables from the JSONL |
| `results/matrix_base_vs_final.jsonl` | first full cut (before the commit mutex) vs baseline |
| `results/final_matrix.jsonl` | release-candidate binary vs baseline, 3 interleaved rounds |
| `results/ablation_loose.jsonl`, `gomaxprocs.jsonl`, `big_order_{ab,ba}.jsonl`, `mixed_sizes_put.jsonl`, `mixed_put_get.jsonl` | ablations, GOMAXPROCS 1/2/4, order-swapped 64 MiB/1 GiB regression check, mixed workloads |
| `results/pack_threshold_matrix.jsonl`, `pack_first_probe.jsonl` | medium-object pack threshold experiments |
| `results/rejected_tmp_sharding.jsonl` | rejected alternative: sharded staging directory |
| `results/audit_summary.txt` | ordering-audit verdicts, baseline control and negative controls |
| `results/syscalls_per_object.txt`, `old_binary_compat.txt`, `layout_dependent_tests_under_medium_default.txt` | syscall counts; baseline binary reading/verifying a P1B store; the 76 root-suite failures under the new default that assume loose chunks |
| `results/prof/` | CPU/mutex/block summaries at 4 KiB c8 (baseline and first cut) |
| `results/P1B_TABLES.md` | generated tables |

Test code: `zeros3_p1b_test.go` (primitives, group commit, medium packs), `zeros3_p1b_crash_test.go` (real-process SIGKILL rounds and a deterministic
crash matrix), `zeros3_p1b_bench_test.go` (opt-in populated-store measurement, `ZEROS3_P1B_BENCH=1`).

Benchmark-only environment switches (not a supported tuning interface): `ZEROS3_PUB_ABLATE=nocoalesce,nodircache,serialcommit,nooverlap`,
`ZEROS3_DIRECT_PACK_KIB`, `ZEROS3_DIRECT_PACK_FLUSH_KIB`. `ZEROS3_DIRECT_PACK_MIB=0` (pre-existing) disables direct packs.

Reproduce: `scripts/build.sh $PWD /tmp/p1b/final`; build the baseline (`b220973`) the same way into `/tmp/p1b/base`; then
`scripts/ab.sh out.jsonl 3 scripts/scenarios_p1b.txt base=/tmp/p1b/base final=/tmp/p1b/final` and `python3 scripts/report_tables.py results`.
