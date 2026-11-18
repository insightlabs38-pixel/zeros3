# Z4-T1 test execution

`harness/t1run` (module `testing-harnesses`) builds the root test binary and the production binary once per
source revision and runs the tests as isolated processes under CPU, memory and scratch-space budgets, longest first.

| Command | |
|---|---|
| `scripts/validate.sh fast` | gofmt, vet, source/test maps, tests recorded as costing at most 1 s |
| `scripts/validate.sh test` / `race` / `authority` / `authority-race` | the full root suite in that mode |
| `t1run -mode normal -run REGEXP` | any subset (affected subsystem) |
| `t1run -need-disk PATH=MIB` | exit 3 unless the filesystem has that much free space (used before the multi-GiB qualification steps) |
| `qualify.sh OUT [SCRATCH]` | final qualification of the tree: `summary.tsv` (every attempt), `qualification.json` (the verdict) |

## What a pass means

A test passes only when its process also ended cleanly: exit 0, the final `PASS` line, a result line for every selected
test and none for others, no failed subtest under a passing parent. A nonzero exit, a signal, a timeout or a crash after
every test reported PASS (TestMain teardown, a hang) fails the whole process; a crash in a batch fails the test that was
running and reruns the rest one process each. Skips are reported, never reused, and `-allow-skip REGEXP` turns any other
skip into a failure. An empty selection is an error, an interrupted run exits 130 and kills the process groups.
`harness/t1run/negative_test.go` drives every one of these cases against a throwaway module.

## Cache

Results are cached per test under `sha256(runner version, test binary, mode, flags, timeout, environment that matters
(ZEROS3_*, GO*, TZ, LANG), GOOS/GOARCH/CPUs, fixture files)`. Fixtures are the directories of every `testing-harnesses/...`
path the tests mention plus `testdata`. An entry is published by rename after an fsync, carries its identity, and is read
back only if it is a complete pass for exactly this identity. Concurrent invocations serialize their builds on a file lock.
`-fresh` ignores the cache; `-update-costs` rewrites `costs-*.json` (wall, CPU, RSS and scratch of every test).

## Qualification

`qualify.sh` runs the steps of `steps.sh`. The verdict is computed from the last attempt of every mandatory step on the
current source fingerprint (`qual_summary.py fingerprint`: every tracked or unignored file except `results/` directories, so
committing the evidence does not change what it certifies); a failed, interrupted, skipped or missing mandatory step, or
rows recorded for other sources, make the script exit nonzero. Earlier failed attempts and invalidated rows stay in
`qualification.json`. `QUALIFY_ONLY=REGEXP` reruns selected steps and appends. `qualify_test.sh` is the negative controls.

Timings measured on 4 vCPU / 15 GiB: `results/speedup.json`.
