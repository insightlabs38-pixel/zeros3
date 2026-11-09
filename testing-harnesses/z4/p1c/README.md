# Z4-P1C evidence: small-object segments

Report: [`../../results/Z4_P1C_SMALL_OBJECT_STORAGE.md`](../../results/Z4_P1C_SMALL_OBJECT_STORAGE.md).
Method = the Z4-P0 driver (`../p0/cmd/p0bench`) unchanged: real server over HTTP, one fresh store and process per repetition, every PUT replayed byte for byte in a fresh process (`verified:true` in every result row). All comparisons are **interleaved A/B on the same instance** (the order of variants alternates every round); historical absolute figures are not used.

| Path | Purpose |
|---|---|
| `FORMAT.md` | byte-level format: segment = pack-v1 prefix, sealing, recovery scan, store level 11 and its rules |
| `DURABILITY_MODEL.md` | written before the publication path: ordering, commit point, failure semantics, recovery state machine, invariants |
| `COMPAT.md` | compatibility matrix (old/new stores and binaries, policy changes, crash during upgrade) with the test or evidence for every row |
| `proto/` | end-to-end prototype used for the architecture decision (`p1cproto`): loose / shared pack / segment, each with and without inline manifests |
| `scripts/proto_matrix.sh` | interleaved prototype matrix; `results/proto_matrix.jsonl`, `results/proto_shards.jsonl` (sharded segments, seal cost, GOMAXPROCS) |
| `scripts/ab_p1c.sh OUT ROUNDS SCENARIOS name=BINDIR[:ENV,..] ...` | interleaved A/B runner for any `p0bench` subcommand (variants alternate order each round) |
| `scripts/scenarios_p1c.txt` | the production matrix (0 B .. 1 GiB, c1..c32, duplicates, revisions, compressible, mixed sizes/readers+writers, multipart) |
| `scripts/report_tables.py FILE A B` | markdown tables (median [min-max], p50/p99, CPU, RSS, files per object) from the JSONL |
| `scripts/scale_p1c.sh` | store scale (1k..1M objects), inodes, open/recovery time, `verify -deep`, `gc`, `repack` |
| `scripts/layout_summary.py` | storage layout actually exercised by the root test suite (`ZEROS3_LAYOUT_MATRIX_OUT`) |
| `results/main_matrix.jsonl` | first full A/B (segment threshold 256 KiB), before the 1 MiB threshold and the final optimisations (kept for the record) |
| `results/final_matrix.jsonl` | the reported A/B: P1B (`5b9bcdd`) vs the final P1C binary |
| `results/*.jsonl` | ablations, GOMAXPROCS 1/2/4, order-swapped large-object checks, scale, strace counts, profiles |
| `results/layout_matrix.jsonl.gz` | per-test layout record of the root suite |

Test code (repository root): `zeros3_layout_test.go` (policy declaration, layout-independent fault injection, coverage record), `zeros3_layout_matrix_test.go` (behaviours qualified on every layout), `zeros3_p1c_test.go`, `zeros3_p1c_crash_test.go` (crash matrix, power-loss state model), `zeros3_p1b_crash_test.go` (real `kill -9` rounds, now with a small-object/1 MiB-segment mode).

Switches (not a tuning interface): `ZEROS3_SMALL_OBJECT_STORAGE=off` (write loose again), `ZEROS3_SEGMENT_TARGET_MIB`, `ZEROS3_SEGMENT_MAX_KIB`, `ZEROS3_INGEST_ABLATE=nochunkpool`.

Reproduce: `sh ../p1b/scripts/build.sh $PWD /tmp/p1c/final`; build `5b9bcdd` the same way into `/tmp/p1c/base`; `scripts/ab_p1c.sh out.jsonl 3 scripts/scenarios_p1c.txt base=/tmp/p1c/base p1c=/tmp/p1c/final`; `python3 scripts/report_tables.py out.jsonl base p1c`.
