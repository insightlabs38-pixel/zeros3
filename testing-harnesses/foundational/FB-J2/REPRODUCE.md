# Reproduce FB-J2 preflight

Use Go 1.27.0 with two Go workers. Tests use disposable local stores,
and the boot transition is modeled. The recorded workspace has a
two-core CPU quota, 8 GiB RAM and overlay storage.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=2m -v \
  -run '^(TestFBJ2IncompleteWatermarkEvidence|TestFB01R(WatermarkEncoding|WatermarkLifecycle|InterruptedGroupMatrix|DegradedViews|TailDiscardInterrupted|WatermarkWriteFailure))$' .
```

Historical performance, durability controls and the original Design B
are in [FB-01R](../FB-01R/REPRODUCE.md).

## Prototype comparison

The model matrix is table-driven and uses an explicit count of durable
groups independent of what the parser observes. The microbenchmark
uses three trial rotations and 256 300-byte groups per lane and width.
Its call counts exclude initialization and close. Its scan latency is
for a journal already in memory. Use a path on the target filesystem
for TMPDIR. This workspace has tmpfs at /tmp and overlay at /workspace.

```sh
mkdir -p "$SCRATCH"
TMPDIR="$SCRATCH" GOMAXPROCS=2 FBJ2_REPORT=1 FBJ2_BENCH=1 \
  go test -count=1 -timeout=3m -v \
  -run '^(TestFBJ2PrototypeRecovery|TestFBJ2JournalMicro)$' . > "$SCRATCH/run.txt"
sed -n 's/^FBJ2 //p' "$SCRATCH/run.txt"
```

`prototype-overlay.jsonl` uses overlay and the optimized scanner.
`prototype-tmpfs.jsonl` uses the same scanner on tmpfs.
`prototype-pilot.jsonl` preserves the initial tmpfs measurements with
the per-group replay reader allocation.
