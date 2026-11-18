# Z4-T1 test execution

`harness/t1run` (module `testing-harnesses`) builds the root test binary and the production binary once per
source revision and runs the tests as isolated processes under CPU and memory budgets, longest first.

| Command | |
|---|---|
| `scripts/validate.sh fast` | gofmt, vet, tests recorded as costing at most 1 s |
| `scripts/validate.sh test` / `race` / `authority` / `authority-race` | the full root suite in that mode |
| `t1run -mode normal -run REGEXP` | any subset (affected subsystem) |
| `qualify.sh OUT [SCRATCH]` | final qualification of the tree, one row per step in `summary.tsv` / `summary.json` |

Results are cached per test under the identity `sha256(test binary, mode, environment)`: a source edit changes the
binary and invalidates everything, an unrelated failing shard does not disturb the others (`-fresh` ignores the cache).
`costs-{normal,race}.json` hold the measured wall time, CPU time and peak RSS of every test (`-update-costs`
rewrites them from a complete fresh passing run); they only order and pack the work. Each process gets its own
`TMPDIR`, children are killed with the process group, historical binaries are built once into a persistent cache.
Evidence: `t1run -evidence FILE`. Timings measured on 4 vCPU / 15 GiB: `results/speedup.json`.
