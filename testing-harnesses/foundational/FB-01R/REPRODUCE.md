# FB-01R: reproduce

Run from the repository root. Each command appends JSONL to `$FB01_RESULTS`.
Everything runs locally; there is no CI.

```sh
B=$(git rev-parse --short HEAD)
go test -count=1 -run 'FB01R|Watermark' .                      # all FB-01R tests, under a minute
FB01_X14_TRIALS=300 FB01_BUILD=$B FB01_RESULTS=$PWD/x14r_engine.jsonl \
  go test -count=1 -run 'TestFB01RX14Engine$' -v .              # about 2 minutes
FB01_X14_TRIALS=300 FB01_BUILD=$B FB01_RESULTS=$PWD/design_b.jsonl \
  go test -count=1 -run 'TestFB01RDesignB' -v .
FB01R_KILL_ROUNDS=30 FB01_BUILD=$B FB01_RESULTS=$PWD/kill.jsonl \
  go test -count=1 -run 'TestFB01RKill$|TestFB01RKillWithoutWatermark' -v .
FB01R_MEASURE=1 FB01_BUILD=$B FB01_RESULTS=$PWD/measure.jsonl \
  go test -count=1 -run 'TestFB01RMeasure$' -v .                # needs strace for the syscall rows
go test -run '^$' -fuzz '^FuzzFB01RWatermarkSlot$' -fuzztime 60s .
go test -run '^$' -fuzz '^FuzzFB01RJudgeTail$' -fuzztime 60s .
go test -run '^$' -fuzz '^FuzzFB01RDesignBRecover$' -fuzztime 60s .
go test -count=1 -timeout 110m .                               # 27-29 minutes here
```

X14-R uses FB-01's seeds, so its trials are the same stores and cuts; the
"restart" columns simulate another boot through `testBootID`. Frame order
inside a forced group follows goroutine scheduling, so counts can move by a few
trials between runs. Timings depend on the machine and vary by up to 15%
between runs here.

## Compatibility

Build each older binary in a worktree (`go build -o zeros3-OLD .`), then:

```sh
FB01_RESULTS=$PWD/compat.jsonl testing-harnesses/foundational/FB-01R/compat.sh /path/zeros3-OLD /path/zeros3-NEW
```

It runs FB-01's runner unchanged, then the stale-watermark checks. It needs
`curl` with `--aws-sigv4` and `python3`.

## Binaries measured (sha256)

```
e119d31f18cdabb3c9f9708d7beb77527aa2c5583ff524980637635ebe516b33  zeros3 at 2ac9cab (main)
559e419dba058f01399854153d2b104f521c036fbad19cf31252d2259df89ca7  zeros3 at 4b66f16 (FB-01)
b7b6d952d692f2e50b06b922d1127d744797894ddef9dffde02ea341531b17ed  zeros3 at 609374b (this build)
```
