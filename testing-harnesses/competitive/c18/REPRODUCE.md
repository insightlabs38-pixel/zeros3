# Reproduce C18

Build the current Go 1.27.0 engine and control `220a336`. Use the
verified C13 wide-panel preset store as the seed. Recreate it with
C13's `preset_bench.py` and the frozen wide fixture list. The repair
driver clones that seed and changes only those disposable copies.

```bash
common=(--binary "$C18_BINARY" --baseline "$C17_BINARY" --seed "$C13_PRESET_STORE"
        --manifest "$HARNESS/../c13/wide-fixtures.jsonl")
"$PYTHON" "$HARNESS/repair.py" "${common[@]}" --work "$WORK/repair" \
    --results "$WORK/repair.jsonl"
"$PYTHON" "$HARNESS/repair.py" "${common[@]}" --dictionary "$C13_PRESET_FILE" \
    --lanes forced-repack --work "$WORK/forced" --results "$WORK/forced.jsonl"
"$PYTHON" "$HARNESS/../c4/operations.py" --binary "$C18_BINARY" --sdk "$AWS_SDK_GO_FIXTURE" \
    --restic "$RESTIC" --scratch "$WORK/operations" --results "$WORK/operations-amd64.jsonl"
C4_GO="$GO" C4_PYTHON="$PYTHON" bash "$HARNESS/../c4/arm64.sh" "$WORK/arm64" "$QEMU"
```

C4's native SDK fixture uses the official Go SDK v2 and the operations
driver uses Boto3. The Restic lane is a local S3 backup and restore. The
ARM smoke uses qemu-aarch64 10.0.13 and a static Linux/arm64 executable.
Repeat the isolated repair on that target using the emitted wrapper
and matching `ZEROS3_C4_QEMU` and `ZEROS3_C4_ARM64` variables.

```bash
go test -run '^(TestC18|TestC13PresetPack|TestFB01R(Kill|WatermarkWriteFailure)|TestAuthoritySimilarityPublicationRecovery|TestC16DerivedBaseImportCuts|TestAuthorityRecoverPeerClosure)' .
go test -race -run '^(TestC18|TestC13PresetPackAdmissionAndReopen|TestAuthoritySimilarityPublicationRecovery)' .
```

`repair.jsonl` prices end-to-end repair and deep verification separately
from the 96 signed reads in each phase. Its header and footer identities
are the same expected pack SHA-256 before the injected bit flip and
after targeted repair. `output_blocks` are process counters. The fault
suite tests corruption outside the record and real process cuts on
either side of the repair rename.
