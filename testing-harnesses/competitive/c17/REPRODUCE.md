# Reproduce C17

Use Go 1.27.0, C1's Python environment and a two-CPU quota. Build the C16
snapshot at `2337f48`, the integrated control at `20f13bc` and the C17
engine. The integrated paired lanes share the same foundational journal
and checksum code. Tool `strace` collects syscall counts in its own run.
The original source identities come from C2, C1 and C14.

```bash
common=(--baseline "$CONTROL" --candidate "$CANDIDATE" --downloads "$C2_DOWNLOADS"
        --c1 "$C1_CORPUS" --c14 "$C14_CORPUS")
"$PYTHON" "$HARNESS/../c8/stream.py" --baseline "$C16" --candidate "$C16" \
    --corpus "$C1_CORPUS" --downloads "$C2_DOWNLOADS" --cases typescript \
    --strace "$STRACE" --scratch "$WORK/c8-replay" --results "$WORK/c8-trace.jsonl"
"$PYTHON" "$HARNESS/pipeline.py" "${common[@]}" --phase optimize \
    --families typescript sqlite entropy --strace "$STRACE" --trace \
    --work "$WORK/trace" --results "$WORK/traced.jsonl"
"$PYTHON" "$HARNESS/pipeline.py" "${common[@]}" --phase optimize \
    --families typescript sqlite entropy --resources tight oversubscribed --repeats 2 \
    --work "$WORK/optimize" --results "$WORK/optimizer.jsonl"
"$PYTHON" "$HARNESS/pipeline.py" "${common[@]}" --phase transfer --prepared "$PREPARED_SOURCE" \
    --resources tight oversubscribed --repeats 2 --work "$WORK/native" --results "$WORK/native.jsonl"
"$PYTHON" "$HARNESS/pipeline.py" "${common[@]}" --phase transfer --prepared "$PREPARED_SOURCE" \
    --fg --repeats 2 --work "$WORK/foreground" --results "$WORK/foreground.jsonl"
"$PYTHON" "$HARNESS/summary.py" --optimizer "$WORK/optimizer.jsonl" --native "$WORK/native.jsonl" \
    --foreground "$WORK/foreground.jsonl" --results "$WORK/summary.json"
```

The prepared source is the exact lean TypeScript source from the C16
runner. Preserve it until transfer trials finish. The optimizer runner
seeds ordinary signed S3 inputs once per family, clones the same physical
seed per lane, then records optimizer, GC, repack, compact, verify and
signed GETs. Optimizer JSON and GC traces are captured separately.

```bash
go test -run '^(TestC17|TestC16|TestB8(CanonicalCrossFormat|RecordShape)|TestDirectPackLife_BodyReadFailureCleansStagingKeepsPublishedPacks|TestP1A_PackPublishFailureFailsRequestAndCleansUp)' .
go test -race -run '^(TestC17|TestC16DerivedBaseImportCuts|TestC16NamedBaseReplication)' .
```

The optimizer record contains candidate and apply CPU, peak sampled
filesystem scratch and I/O units. GC pauses are parsed from Go runtime
logs. Trace write counts include runtime logging. Native transfer rows
include combined HTTP bytes, receiver process metrics, exact whole
identity and physical finalization. The foreground lane issues 512
bounded signed operations on the receiving store.
