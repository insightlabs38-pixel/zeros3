# FB-01: reproduce

Run from the repository root. Each command appends JSONL to `$FB01_RESULTS`.

## Old builds (the defects)

The harness was committed before the fix, on top of 2337f48:

```sh
git worktree add /tmp/fb01-old be82fc0
cd /tmp/fb01-old
FB01_EXPECT_DEFECT=1 FB01_BUILD=2337f48 FB01_RESULTS=/tmp/repro_2337f48.jsonl \
  go test -count=1 -run 'TestFB01Repro' .
```

`main` has no v4 checkpoint, so only F6-JNL-01 runs there:

```sh
git worktree add /tmp/fb01-main 2ac9cab
git -C /tmp/fb01-main show be82fc0:zeros3_fb01_test.go > /tmp/fb01-main/zeros3_fb01_test.go
cd /tmp/fb01-main
FB01_EXPECT_DEFECT=1 FB01_BUILD=main-2ac9cab FB01_RESULTS=/tmp/repro_main.jsonl \
  go test -count=1 -run 'TestFB01ReproF6JNL01' .
```

Without `FB01_EXPECT_DEFECT=1` these tests fail on the old code by design.

## New build

```sh
FB01_BUILD=$(git rev-parse --short HEAD) FB01_RESULTS=$PWD/fb01.jsonl \
  go test -count=1 -run 'TestFB01Repro|TestFB01FlipSweep|TestFB01ConflictingCheckpoint' .
go test -count=1 -run 'TestFB01' .                       # all FB-01 tests, under a minute
go test -race -count=1 -run 'TestFB01SharedOpenDuringWrites|TestFB01PartialMultiframeGroup|TestFB01WriterRollback' .
FB01_X14_TRIALS=300 FB01_RESULTS=$PWD/x14.jsonl go test -count=1 -run 'TestFB01X14Engine$' -v .
go test -run '^$' -fuzz '^FuzzFB01JournalScan$' -fuzztime 30s .
go test -run '^$' -fuzz '^FuzzFB01CheckpointReaders$' -fuzztime 30s .
go test -short -count=1 -timeout 60m .                   # about 16 minutes here
```

X14 seeds are fixed (bases 1000-1005, scenario seed 37), but frame order inside
a forced group follows goroutine scheduling, so counts can move by a few trials
between runs; open times vary.

## Compatibility

Build each old binary from its commit (`go build -o zeros3-OLD .` in a worktree),
then:

```sh
FB01_RESULTS=$PWD/compat.jsonl testing-harnesses/foundational/FB-01/compat.sh /path/zeros3-OLD
```

It needs `curl` with `--aws-sigv4` and passes explicit credentials to `serve`,
so `AWS_*` variables in the environment do not interfere.

## Binaries measured (sha256)

```
b260208d182b08652f43d7ededd431125309df8fca2951c4881099d1952711d7  zeros3 at 2337f48
9490d10b2d347ef08ef9c598022ac31e88ad43dc4e8d1d74b70b02a6a11f89e0  zeros3 at main 2ac9cab
18444ac3a0ce801408152ffd67d3c4a616348a3800378eeda5ff09357456ab04  zeros3 at d2727f4
```

Go 1.27.0 linux/amd64. Binary hashes depend on the toolchain and the VCS stamp;
the journal hashes in the JSONL rows identify each fixture before and after an open.

## Using the commands on a damaged store

```sh
zeros3 journal inspect -store DIR          # exit 1 when damaged; -json for the report
zeros3 journal salvage -store DIR -cut OFFSET -i-understand-data-loss
```
