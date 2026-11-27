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

For multipart reference pages, use the same J2 setup with
`serve -multipart-pages`. The writer persists the separate
`journalpartpages` marker. Existing inline part records still replay.
The test requires only one physical part payload and uses generated
repeated references for the 10,000-part case.

```sh
GOMAXPROCS=2 ZEROS3_TEST_PARTS_SCALE=1 go test -run '^TestFB10PartPage' \
  -v -count=1 -timeout=120s .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB10PartPage$' \
  -fuzztime=6s -parallel=2 .
```

`journal cursor -store PATH` prints the verified chain position.
Retain that cursor outside the store's failure domain and pass it to
`journal cursor -store PATH -from CURSOR.json` to check the same
lineage. The command is read-only and does not change recovery policy.

For the cursor and X14 cases, run `go test -run '^TestFB10JournalCursor' .`.
For the signed repeat, point `FB10_SIGNED_ARCHIVE` at the pinned C19
`urllib3-2.2.2.tar.gz` (SHA-256
`dd505485549a7a552833da5e6063639d0d177c04f23bc3864e41e5dc5f612168`).
Run `GOMAXPROCS=2 go test -v -run '^TestFB10JournalSignedMixed$' .`
and retain the `FBJ2 ` JSON lines. The local runner had a two-CPU
cgroup and workspace overlay. Reopen measurements use ambient cache.
