# C1/T1 completion ledger

Implementation state of each mandatory item. The verdict is `results/qualification.json` (written by `qualify.sh`); this table says where each item lives and which test or step covers it.

| Item | Where | Covered by |
|---|---|---|
| 1A t1run exit-status handling | `harness/t1run` (`judge`) | `negative_test.go`: crash, timeout, signal, launch failure, malformed trailer, duplicates, subtest failure, interrupt, empty selection, cache identity |
| 1B qualify.sh verdict | `qualify.sh`, `qual_summary.py` | `qualify_test.sh` (failed/skipped/missing/stale-source controls); fingerprint = tree without `results/` |
| 1C cache and scheduler audit | `harness/t1run/{cache,sched}.go` | atomic identity-checked entries, fixtures hashed, skips never reused, disk/mem/CPU budgets, `-need-disk` |
| 1D equivalence | `equivalence.py` | steps `conventional_root`, `conventional_race`, `t1_equivalence`: per-test outcomes of `go test ./...` and `-race` equal the runner's |
| 2 IQ3_S | `zeros3.go` sections 16g/16h, `z3/qcore` | byte-exact vs ggml `ggml-quants.c` (conformance vectors, IQ3_M/IQ3_XS real models); ZN7 DType 21 derive + `zn7iq3s` feature, old-binary refusal test |
| 3 streaming Brotli | section 16i, `z3/brotliz3` | 562 scenarios vs libbrotli 1.1.0 and 1.2.0; Parquet recipe v3 (tag `0xB5`), `hardstream` feature |
| 4A economics | `TestAuthorityHardCorpusEconomics`, `c1/results/economics_pqchain.*` | step `economics_pqchain` |
| 4B read-cost planner | `AuthPolicy.Tier`/`NoReadCost`, `authTierLimits` | `TestAuthorityTierRefusalTable`, `TestAuthorityTierGateOnHardCodecs` |
| 4C parallel page replay | `repPQdecodeRange` | `TestRepParquetParallelReplayIsExact`, race suite, economics 1/2/4 workers |
| 4D format compatibility | features `zn7`, `zn7iq3s`, `hardstream`, `hardcodec` | `TestAuthorityStreamAndIQ3FeaturesRefuseOlderBuilds` (import/snapshot/replication/maintenance/repair/revert) |
| 5 G1 retirement audit | `zeros3_g1_test.go`, `c1/g1_ledger.py` | steps `g1_ledger_current`, `g1_coverage_executed`, `authority_hook` |
| 6 fixtures, G3, runner speed | `zeros3_fixture_test.go`, `eachLiveManifest`, `AuthorityMigrate`, `costs-*.json` | `TestFixtureImageEqualsBuildingInPlace`; step `scale_1m` (`c1/results/scale_1m_g3_optimized.txt`) |
