# Reproduce DF1

Use Go 1.27, Linux amd64, and a disposable filesystem with 4 KiB blocks.
The reported machine uses a two-core quota and `GOMAXPROCS=2`. Stage the
original curl and Lua releases from [DF0](../DF0/REPRODUCE.md). The
measurement harness accepts those two families and loads only the first
release pair in each. The native bodies are rebuilt for each trial.

Set the old executable to a clean pre-GF2 build. The recorded run used
`1d4f36a210a0bd543762073d249deae848604592`. Use an idle host for timing.
The compare script builds each supplied source revision with the
checkout's identical research tests via Go overlays.

```bash
export ZEROS3_DF0_FIXTURES=/path/to/df0-inputs
export ZEROS3_GF2_OLD_BINARY=/path/to/pre-gf2-zeros3
export GOMAXPROCS=2
h=testing-harnesses/deep-frontier/DF1
"$h/compare.sh" /tmp/df1-varint d0112fb 4aa1dca
"$h/compare.sh" /tmp/df1-fused 4aa1dca cdfaee0
"$h/compare.sh" /tmp/df1-decoder cdfaee0 e250d76
ZEROS3_DF1_SWEEP=1 "$h/compare.sh" /tmp/df1-combined d0112fb e250d76
"$h/extra.sh" /tmp/df1-combined
"$h/qualify.sh"
python3 "$h/collect.py" /tmp/df1-combined > /tmp/df1-combined.jsonl
```

The wider-store experiment is [reproduced from a saved patch](WRITER.md).
Use `compare.sh` with `worktree` as the candidate after applying the
patch to the varint-only source in a disposable checkout. The collect
script verifies that the native final allocated blocks and pack blocks
match across each comparison. Profiles, cold first reads and isolated
maintenance runs are separate from warm timed comparisons.

Process CPU excludes prep and post-run readback of acknowledged PUTs.
The timed workers verify every signed GET byte.
Peak server memory is outside the scope of the process counters.
The memory and GC snapshots are at request-window boundaries.
The total allocation and GC fields include HTTP, verifier readback
and any Go runtime activity in that process. Latency includes HTTP.
Individual signed request samples are retained. Small highest ranks
and two orders give descriptive variation for this host.

The cold probe records mincore before opening each scratch clone.
Repeated cold trials are a small sample. Profile CPU is Go sampled
CPU across the signed process. Maintenance runs use a disposable
partially live pack, an independently verified carry, and the real
online rewrite path. Qualification groups are bounded and do not
use the historic broad replay suites.
