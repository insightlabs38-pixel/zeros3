# Reproduce concurrent saturation

Use Linux amd64, Go 1.27 and disposable local storage. The recorded
host uses a two-core cgroup, GOMAXPROCS 2 and 4 KiB filesystem blocks.
Set up exact curl and nginx bytes with [the DF0 fixture tool](../DF0/REPRODUCE.md).
Run on an idle host and keep the nginx partition out of development.

```bash
export ZEROS3_DF0_FIXTURES=/path/to/df0-inputs
export ZEROS3_GF2_OLD_BINARY=/path/to/pre-gf2-executable
export GOMAXPROCS=2
h=testing-harnesses/deep-frontier/DF2-SATURATION
"$h/run.sh" /tmp/sat-diagnose diagnose
"$h/run.sh" /tmp/sat-compare compare
"$h/run.sh" /tmp/sat-controls controls
"$h/run.sh" /tmp/sat-qualification qualify
ZEROS3_SAT_FROZEN=1 "$h/run.sh" /tmp/sat-confirm confirm
python3 "$h/collect.py" /tmp/sat-compare/arch-*.txt > /tmp/sat-compare.jsonl
```

The read-only native mode serves the pinned DF1 representation. The
signed client hashes every response with streaming SHA-256 and checks
status and length. Record hashes, source paths and order are in the
pinned DF0 manifest. The original DF1 mixed workload is reproduced
separately. Comparisons preserve the same physical allocation within
each fixture and scope. Main trials are closed-loop warm GETs with
opposite lane order. The secondary PUT writer verifies every acknowledged
object by exact readback. Controls are bounded diagnostic runs.

CPU is measured across server and client goroutines in one process.
The cgroup deltas reflect the whole cgroup. Peak heap objects and RSS
are sampled every 30 ms. The runtime scheduler histogram is an
approximate snapshot of sampled runnable delays. The kernel read
counter and verified record bytes describe different layers. Eviction
uses mincore before the first burst and does not repeatedly drop
the page cache for later requests.

Use the separately saved profiled lanes for attribution. Trace profiles
measure aggregate delays across goroutines. For example:

```bash
go tool pprof -top /tmp/sat-diagnose/profile-32/cpu.pprof
go tool pprof -top /tmp/sat-diagnose/profile-32/mutex.pprof
go tool trace -pprof=sched /tmp/sat-diagnose/profile-32/schedule.trace > /tmp/sat-runnable.pprof
go tool pprof -top /tmp/sat-runnable.pprof
```
