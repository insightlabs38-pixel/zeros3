# Reproduce the integrated Grand Frontier suite

Use Go 1.27 on Linux with disposable local storage. Stage the exact
[development and baseline corpus](../GF0/REPRODUCE.md),
[third TypeScript release](../GF2/REPRODUCE.md) and
[three training checkpoints](../GF5/REPRODUCE.md). Each suite checks the
inputs against its fixture manifest. Direct Go tests can skip when an
optional fixture is missing. Confirm there are no skipped expected
families in the replay log.

```bash
export ZEROS3_GF0_FIXTURES=/path/to/fixture-root
export ZEROS3_GF5_FIXTURES=/path/to/checkpoint-root
export ZEROS3_GF2_OLD_BINARY=/path/to/pre-GF2-zeros3
set -o pipefail
testing-harnesses/grand-frontier/REPRODUCE.sh | tee grand-frontier.log
```

The script runs bounded test groups, including the adjacent held-out
signed and native stores. The full historical baseline can be recreated
separately. Setting the pre-GF2 executable exercises the real old reader. The
recorded run used a clean Go 1.27 build from
`1d4f36a210a0bd543762073d249deae848604592`.
Test logs report bytes, CPU nanoseconds and p50/p95/p99 latency arrays.
Keep the host idle and compare allocations independently of timings.

```bash
GOMAXPROCS=2 go test -race -count=1 -timeout=2m \
  -run '^(TestGF7(TranslatedAuthority|PublicationCuts)|TestGF8(CompiledSnapshotRepair|BitPatchBoundaries)|TestGF6SignedNegotiation|TestFB16SignedProjectionReplication|TestFBJ2Grouped(SyncFailure|Replay)|TestAuthority(CycleRejected|GraphLimits|CorruptComponentFailsClosedNeverWrongBytes|MigrationCancellationBeforePublication)|TestRepOversizedRootReadBounded|TestFB03_PackFDCache(Unit|Concurrent|Retirement|SealAndMove))$' .
GOMAXPROCS=2 go test -race -count=1 -timeout=2m \
  -run '^TestGF8SignedMixed$/^0$/^(cdc|compiled|translate)$' .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzGF8TranslateIndex$' -fuzztime=6000x -parallel=2 .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzGF7TranslatedFrame$' -fuzztime=6000x -parallel=2 .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzGF2FrameProgram$' -fuzztime=6000x -parallel=2 .
```

The timing run measured a two-worker quota, 4 KiB Linux overlay blocks
and local signed HTTP. Reopens clear store-local state with OS page
caches still available. Rate and latency shaping is local to GF6.
Process cuts use SIGKILL. Physical disk loss, physical power loss,
OS-wide descriptor starvation, NVMe and production WAN remain outside
the measurement.

The standalone zstd matched-input control requires Python 3 and the
zstd CLI. It reconstructs and verifies both whole gzip files and emits
physical file blocks and encode/decode wall and CPU nanoseconds.

```bash
python3 testing-harnesses/grand-frontier/GF8/zstd_control.py
```
