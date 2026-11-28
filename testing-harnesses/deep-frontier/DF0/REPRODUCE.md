# Reproduce DF0

Use Go 1.27 on Linux amd64, two Go scheduler workers and a disposable
local store. The timed runs use a two-CPU cgroup and Linux overlay with
4 KiB allocation blocks. Set `TMPDIR` to a scratch filesystem with
space for the corpus, seed stores and trial clones. Keep the host idle.

```bash
export ZEROS3_DF0_FIXTURES=/path/to/df0-inputs
export ZEROS3_GF0_FIXTURES=/path/to/original-gf-fixtures
export ZEROS3_GF2_OLD_BINARY=/path/to/pre-gf2-zeros3
export ZEROS3_DF0_PROFILE=/path/to/scratch/profiles
export GOMAXPROCS=2
h=testing-harnesses/deep-frontier/DF0
python3 "$h/fetch.py"
"$h/reproduce.sh" original
"$h/reproduce.sh" validation
"$h/reproduce.sh" scopes
"$h/reproduce.sh" profiles
"$h/reproduce.sh" safety
```

Stage the inherited inputs as in [GF0](../../grand-frontier/GF0/REPRODUCE.md)
and [GF2](../../grand-frontier/GF2/REPRODUCE.md). The historical build used
clean `1d4f36a210a0bd543762073d249deae848604592` for older-reader refusal.
Set `ZEROS3_DF0_GO` if Go 1.27 is outside PATH. Native race checks use
the local worktree dependencies supported by the Go race runtime.

The `confirmation` mode requires the same preregistered conditions.
Keep nginx outside future DF1 and DF2 development and tuning. The
checkout running the confirmation should have the full harness frozen.

Use the output lines starting with `DF0` for new measurements.
Original lines retain the `GF7`, `GF7AUTH` and `GF8` labels. Output
latencies are nanoseconds. First-request rows include `mincore`
residency for data files immediately after targeted advice and before
open. Metadata and file indexes can become warm during reopen. This
does not approximate a hardware power cycle. Copying and priming happen
before timed work. Store-local warm trials start from an exact whole read.

```bash
go tool pprof -top "$ZEROS3_DF0_PROFILE"/sparse.cpu
go tool pprof -top "$ZEROS3_DF0_PROFILE"/whole.cpu
go tool trace -pprof=syscall "$ZEROS3_DF0_PROFILE"/mixed.trace > /tmp/df0-syscall.pprof
go tool trace -pprof=sync "$ZEROS3_DF0_PROFILE"/mixed.trace > /tmp/df0-sync.pprof
go tool trace -pprof=sched "$ZEROS3_DF0_PROFILE"/mixed.trace > /tmp/df0-sched.pprof
```

CPU profiles sample live signed requests, including client and server work.
Trace wait durations sum across goroutines and are not additive with CPU
or an individual request's wall clock. No system-wide cache dropping is used.

Convert a tagged run's JSONL payloads to latency summaries with
`python3 tail_summary.py results/<run>.jsonl`. It uses completion order,
circular blocks of eight, 400 fixed-seed resamples and nearest-rank
quantiles. The interval is descriptive for this sampled workload.
A p99 interval requires 300 samples in an individual trial and class.
The IID tail-hit calculation is explanatory and does not assume that
concurrent measurements are independent.
