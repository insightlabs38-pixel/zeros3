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

## Integrated measures

Create SCRATCH on the measured filesystem and pin the C19 urllib3
2.2.2 archive from its input manifest. Use the same Go compiler and
GOMAXPROCS=2 in every lane. The signed loopback server and client
share the two-core quota. Foreground reads are run concurrently with
writes, and the raw rows record their sample counts. The profile
adds sampling overhead and is separate evidence.

```sh
export TMPDIR="$SCRATCH" GOMAXPROCS=2 FBJ2_ARCHIVE="$ARCHIVE"
go test -c -o "$SCRATCH/j2.test" .
ZEROS3_JOURNAL_FORMAT=j2 FB01R_KILL_ROUNDS=1 FB01_RESULTS="$SCRATCH/kill.jsonl" \
  "$SCRATCH/j2.test" -test.count=1 -test.timeout=75s -test.v -test.run '^TestFB01RKill$'
"$SCRATCH/j2.test" -test.count=1 -test.timeout=3m -test.v -test.run '^TestFBJ2SignedEngine$'
FBJ2_PROFILE_MODES=lazy,j2 "$SCRATCH/j2.test" -test.count=1 -test.timeout=2m \
  -test.v -test.run '^TestFBJ2SignedEngine$' -test.cpuprofile "$SCRATCH/cpu.pb"
go test -race -count=1 -timeout=90s \
  -run '^(TestFBJ2Grouped.*|TestFBJ2IncompleteWatermarkEvidence|TestFB01RInterruptedGroupMatrix)$' .
go test -run '^$' -fuzz '^FuzzFBJ2GroupedDecode$' -fuzztime=5s -parallel=2 .
go build -o "$SCRATCH/zeros3-j2" .
"$PYTHON" testing-harnesses/foundational/FB-J2/compat.py --new "$SCRATCH/zeros3-j2" \
  --old "$C19_BASELINE" --work "$SCRATCH/compat"
```

C19 baseline is 519f53b. The initial engine traces precede the bounded
writer encoding scratch. The final code is the optimized variant.
To repeat the initial ablation, build a disposable copy with the
encoding scratch reuse disabled, leaving the wire format unchanged.
