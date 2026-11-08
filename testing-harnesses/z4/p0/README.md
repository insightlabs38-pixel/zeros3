# Z4-P0 performance baseline: harness and reproduction

Research/benchmark infrastructure only. Production `zeros3.go`, root `go.mod` and every store/wire format are unchanged.
Report: [`../../results/Z4_P0_PERFORMANCE_BASELINE.md`](../../results/Z4_P0_PERFORMANCE_BASELINE.md). Generated tables: `results/P0_TABLES.md`.

## Layout
| Path | Purpose |
|---|---|
| `cmd/p0bench/` | stdlib-only driver: deterministic content (random-access, revisable), SigV4 client, server lifecycle, `/proc`+rusage+block-device accounting, byte-exact verification, JSONL. Subcommands: `env calib put get mixed cli mkstore readstore verifystore startup` |
| `overlay/zeros3_p0prof.go` | profiling hook compiled **into the unmodified source via `go build -overlay`** (CPU in 500 ms segments, heap/alloc/mutex/block, runtime metrics, optional trace). Never written into the tree. |
| `build.sh`, `build_research.sh` | build plain + profiling binaries of production and of the z3 research drivers |
| `scripts/run_*.sh` | scenario matrices: `put get scale maint maint_prof prof reps reps_prof research integrated b2` |
| `scripts/fetch_corpus.sh`, `fetch_models.py`, `make_structured.py` | corpus acquisition/derivation (pinned + hash checked; nothing committed) |
| `scripts/rmeasure.py` | one-child measurement wrapper for research binaries (wall, user/sys, ru_maxrss, IO, block-device delta) |
| `scripts/summarize.py`, `maint_table.py`, `reps_table.py`, `research_table.py`, `report_tables.py` | aggregation (median [min-max], CV, instability flag) |
| `results/*.jsonl` | raw machine-readable results (one line per repetition, including failures) |
| `results/prof/*.txt` | compact pprof text summaries (raw `.pprof` stays in scratch) |
| `BOTTLENECK_REGISTER.md` | traceable register (IDs referenced by the report) |

## Reproduce (fresh checkout, Go 1.27, Linux, root for `drop_caches`)
```sh
testing-harnesses/z4/p0/build.sh $PWD /tmp/p0/bin                      # production: plain + overlay
testing-harnesses/z4/p0/build_research.sh $PWD /tmp/p0/rbin            # research drivers
go -C testing-harnesses/z4 build -o /tmp/p0/bin/p0bench ./p0/cmd/p0bench
P0_BIN=/tmp/p0/bin P0_WORK=/tmp/p0/work /tmp/p0/bin/p0bench env > env.json; /tmp/p0/bin/p0bench calib
testing-harnesses/z4/p0/scripts/run_put.sh; run_get.sh; run_scale.sh; run_maint.sh objs|bytes|hist   # production, no external data
testing-harnesses/z4/p0/scripts/run_prof.sh; run_maint_prof.sh                                         # profiles
testing-harnesses/z4/p0/scripts/fetch_corpus.sh /tmp/p0/corpus all    # ~5 GB: Hugging Face (pinned), gnu.org, sqlite.org, nuget, F-Droid; pip venv for pyarrow/rocksdict/zstandard
testing-harnesses/z4/p0/scripts/run_reps.sh /tmp/p0/corpus; run_reps_prof.sh /tmp/p0/corpus; run_integrated.sh /tmp/p0/corpus
testing-harnesses/z4/p0/scripts/run_research.sh b1|b3|b4|b5|b6 /tmp/p0/corpus /tmp/p0/rbin
git worktree add --detach /tmp/p0/b2wt origin/z4/representation-authority    # draft #97, never modified
testing-harnesses/z4/p0/scripts/run_b2.sh /tmp/p0/corpus /tmp/p0/b2wt
python3 testing-harnesses/z4/p0/scripts/report_tables.py
```
Paths default to `/tmp/claude-0/...` in `scripts/lib.sh`; override `P0_BIN`, `P0_WORK`, `P0_RES`.
Every PUT/GET result is byte-verified (SHA-256 or range bytes vs the deterministic generator); PUT scenarios additionally
replay all objects in a fresh server process. A mismatch aborts the scenario (fail-closed) and the run is not recorded as valid.
`scripts/lib.sh` supports `P0_FROM=<label>` to resume a matrix.
