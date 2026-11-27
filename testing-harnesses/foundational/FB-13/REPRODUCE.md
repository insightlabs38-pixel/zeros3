# Reproduce FB-13

Use Go 1.27 with the same bounded two-core environment. The native
harness accepts a JSON fixture manifest with an array of
`{"name":"workload","paths":["/local/v0","/local/v1"]}` records.
The checksummed X9 tree tar series is from the same F6 fixture extraction
used by FB-05. TypeScript and HTTPX are from the C12 corpus. SQLite,
Parquet and independent entropy are from the C14 final corpus. Use all
listed revisions and the last X9 revision alone for the standalone
row. Hashes appear in the results.

```sh
GOMAXPROCS=2 ZEROS3_FB13_SERIES=/path/to/series.json \
  go test -v -count=1 -timeout=120s -run '^(TestFB13NativeChunkEconomics|TestFB13FrozenCDCControl)$' .
GOMAXPROCS=2 ZEROS3_FB13_X9=/path/to/f6/corpus/tree-series \
  go test -v -count=1 -timeout=120s -run '^(TestFB13X9RetentionReplay|TestFB13LocalizedRebaseRecovery)$' .
```

For an existing disposable store with eligible history

```sh
zeros3 stats -store STORE -json
zeros3 authority rebase -store STORE -bucket BUCKET -key KEY -max-read-amp 12
zeros3 authority rebase -store STORE -bucket BUCKET -key KEY -max-read-amp 12 -apply
zeros3 verify -store STORE -deep
```

After apply, run the same offline GC, compressed repack and compact
sequence as the native test to measure final allocated bytes. Without
that sequence the report describes extents and estimated rebuild work,
while obsolete bytes still occupy physical packs.
