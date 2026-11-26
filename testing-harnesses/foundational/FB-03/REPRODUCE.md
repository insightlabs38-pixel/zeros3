# Reproducing FB-03

Go 1.27.0, standard library only, from the repository root on
`research/next`. Times are from a shared 4 vCPU container with 15 GiB; lookup
timings move by up to 1.5x between runs, so compare designs within one run.

## Tests

```sh
go test -count=1 -run 'FB03_' .                      # every FB-03 test, ~30 s
go test -race -count=1 -run 'TestFB03_(Loc|PackFD|Concurrent|Presence|Retired|Maint)|TestLocator' .
go test -count=1 -timeout 110m .                     # full suite, ~28 min
```

`results/ma2_tests_base_7ea1686.txt` is the M-A2 test file run on the FB-02
head, where it fails.

## Ingest (M-A2 failure criterion, M-A8 regression check)

`zeros3_fb03_bench_test.go` uses only pre-FB-03 APIs, so it also runs on the
FB-02 head (`git worktree add ../base 7ea1686`, copy the file in, build both
test binaries). Alternate the order each round, 12 rounds:

```sh
./new.test  -test.run '^$' -test.bench '^BenchmarkFB03Ingest' -test.benchtime 100x
./base.test -test.run '^$' -test.bench '^BenchmarkFB03Ingest' -test.benchtime 100x
```

## M-A8 scale (G-SCALE)

```sh
sh testing-harnesses/foundational/FB-03/ma8_scale.sh /tmp/fb03-out /tmp/fb03-gen   # ~15 min, plus pack generation
```

It builds the test binary once and runs each case of
`zeros3_fb03_scale_test.go` in its own process, appending JSON lines to
`ma8_lookup`, `ma8_open`, `ma8_growth`, `ma8_retire` and `ma8_get.jsonl`.
Digests are `z206Sum(i)`; the open cases first write generated pack
directories (about 100 B per record, 1.5 GB at 16M, kept for reuse). One case
by hand:

```sh
FB03_LOOKUP=16000000 go test -v -count=1 -run '^TestFB03Scale_Lookup$' .
FB03_OPEN_DIR=/tmp/g FB03_N=4000000 FB03_LOADER=lean go test -v -count=1 -run '^TestFB03Scale_Open$' .
FB03_GROWTH=16000000 FB03_K=64 FB03_M=9000000 go test -v -count=1 -run '^TestFB03Scale_Growth$' .
FB03_RETIRE=4000000 go test -v -count=1 -run '^TestFB03Scale_Retire$' .
FB03_GET=2000 go test -v -count=1 -run '^TestFB03Scale_Get$' .
```

Lookup designs over the same records: `one-level` (unfiltered, X2's
baseline), `one-filtered` (what open builds now), `four-unfiltered`,
`four-filtered`, and `policy` (built pack by pack through the level policy,
background merges applied). `ZEROS3_LOCATOR_LEVELS` changes the bound (2-64).

## Checksums

`SHA256SUMS` covers every file under this directory except itself:

```sh
cd testing-harnesses/foundational/FB-03 && sha256sum -c SHA256SUMS
```
