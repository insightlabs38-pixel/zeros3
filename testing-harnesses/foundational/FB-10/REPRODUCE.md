# Reproduce FB-10

Run Go 1.27 on a local filesystem. The bounded test takes seconds
after build on a two-core Linux runner.

```sh
GOMAXPROCS=2 ZEROS3_TEST_PRE_FB10=/path/to/prior-J2-binary go test -v \
  -run '^(TestFB10JournalBin|TestFB09LegacyCopy|TestFBJ2Grouped|TestFB04ConcurrentRootLinearizability)' \
  -bench '^BenchmarkFB10JournalBin$' -benchtime=150ms -count=1 -timeout=100s .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB10JournalBinPut$' \
  -fuzztime=7s -parallel=2 .
```

Start a disposable J2 store with `ZEROS3_JOURNAL_FORMAT=j2`. On its
next exclusive writer open, add `serve -binary-journal`. The durable
`journalbin` marker enables old-reader refusal. With the flag removed,
the current binary reads the mixed store and emits legacy JSON for new
writes. A current-only store can be copied separately with FB-09's
legacy-copy path. The activation involves no prior-journal retirement.
