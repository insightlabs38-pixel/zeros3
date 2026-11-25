# Reproduce C11

Build C10 at `dd1af6b`, the admission-time policy at `d725316`, and the C11 final policy with
Go 1.27.0. Use C1's Python environment and the C2 public TypeScript 5.6.2
and 5.6.3 archives. Fixture SHA-256 values are in `fixtures.json`.
The experiment uses disposable loopback endpoints and local credentials.

From this directory, assign absolute paths for the two binaries, the
C2 download directory and a scratch directory. Keep the CPU quota fixed
for a comparison. The process-level control changes `GOMAXPROCS`
consistently across client and servers.

```bash
args=(--baseline "$BASE" --candidate "$CANDIDATE" --work "$WORK/stores" --downloads "$DOWNLOADS")
"$PYTHON" qos.py "${args[@]}" --prepare --results "$WORK/unused.jsonl"
"$PYTHON" qos.py "${args[@]}" --events mixed --widths 1 4 8 16 \
  --lanes idle incumbent fixed adaptive --repeats 2 --deep-verify \
  --check-leases --results "$WORK/mixed.jsonl"
"$PYTHON" qos.py "${args[@]}" --events import maintenance gc restore --widths 4 8 \
  --lanes incumbent adaptive --repeats 2 --deep-verify --results "$WORK/separate.jsonl"
"$PYTHON" qos.py "${args[@]}" --events mixed --widths 4 8 --cpus 1 \
  --lanes incumbent adaptive --repeats 2 --deep-verify --results "$WORK/one-worker.jsonl"
"$PYTHON" qos.py "${args[@]}" --events none --widths 4 8 \
  --lanes incumbent adaptive --repeats 2 --results "$WORK/idle-policy.jsonl"
```

For separate scheduler and collection diagnostics use `--runtime-trace`
on a single mixed pair with `--widths 4`. Its stderr lives under the
named per-trial scratch directories. Exclude the trace run from timing
aggregates. The receiver's deep verification follows the timed interval.

Point `CANDIDATE` to the admission-time binary for the commands above.
Point it to the final binary and rebuild the argument array for these
paired measurements of dynamic proof scheduling.

```bash
"$PYTHON" qos.py "${args[@]}" --events mixed --widths 1 4 8 16 \
  --lanes incumbent adaptive --repeats 2 --deep-verify --check-leases \
  --results "$WORK/adaptive-mixed.jsonl"
"$PYTHON" qos.py "${args[@]}" --events import --widths 4 8 \
  --lanes incumbent adaptive --repeats 2 --deep-verify --results "$WORK/adaptive-import.jsonl"
"$PYTHON" qos.py "${args[@]}" --events mixed --widths 4 8 --cpus 1 \
  --lanes incumbent adaptive --repeats 2 --deep-verify --results "$WORK/adaptive-one-worker.jsonl"
```
