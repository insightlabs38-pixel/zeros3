# Reproduce C14

The C13 baseline is `2078378` with Go 1.27.0. Use the C1 Python environment,
C5 frozen source artifacts and C12 corpus. SQLite 3.53.1 was provided by
Python. Zstandard was 1.5.7. For e2fsprogs 1.47.2, verify `tools.sha256`
in the directory containing the Debian files and extract each package
into one tool root using `dpkg-deb -x`. The benchmark sets a fixed filesystem
clock. SQLite generates WAL salts at runtime and the WAL fixture verifies
exact bytes against the manifest from the same run.

```bash
"$PYTHON" "$HARNESS/fixtures.py" --root "$WORK/corpus" --app "$WORK/application" \
  --c12 "$C12_CORPUS" --c5 "$C5_CORPUS" --tools "$E2TOOLS" --results "$WORK/fixtures.json"
"$PYTHON" "$HARNESS/panel.py" --corpus "$WORK/corpus" --results "$WORK/panel.jsonl"
common=(--corpus "$WORK/corpus" --fixture-manifest "$WORK/fixtures.json" --fs-tools "$E2TOOLS")
"$PYTHON" "$HARNESS/trial.py" "${common[@]}" --application "$WORK/application" \
  --work "$WORK/page-pilot" --results "$WORK/page-pilot.jsonl"
"$PYTHON" "$HARNESS/../c12/compare.py" --lab "$C12_LAB" --tools "$XDELTA_TOOLS" --corpus "$WORK/corpus" \
  --families sqlite volume volume-compressed tensor-dtypes mobilenet parquet sst entropy events \
  --methods xd anchor-xor xdelta3 zstd --work "$WORK/neutral" --results "$WORK/neutral.jsonl"
```

Build a scratch Go test binary with C12's `lab_test.go.txt` and C14's
`go_atlas_test.go.txt` and `go_sqlite_test.go.txt` enabled as `*_test.go`
files. Go's `-overlay` can add the three files. Build and test at
`GOMAXPROCS=2`. The experimental programs use the unchanged XD decoder.

```bash
for family in sqlite volume volume-incremental; do
  "$PYTHON" "$HARNESS/native_match.py" "${common[@]}" --lab "$C14_LAB" --family "$family" \
    --work "$WORK/match-$family" --results "$WORK/match-$family.jsonl"
done
"$PYTHON" "$HARNESS/native_store.py" "${common[@]}" --binary "$C14_BINARY" --baseline "$C13_BINARY" \
  --work "$WORK/archive" --results "$WORK/archive.jsonl"
"$PYTHON" "$HARNESS/native_store.py" "${common[@]}" --binary "$C14_BINARY" --baseline "$C13_BINARY" \
  --profile mixed --repeats 1 --families volume volume-incremental \
  --work "$WORK/mixed" --results "$WORK/mixed.jsonl"
"$PYTHON" "$HARNESS/restic_snapshot.py" --restic "$RESTIC" --corpus "$WORK/corpus" \
  --work "$WORK/restic" --results "$WORK/restic.jsonl"
go test -run '^(TestC14SparseXD|TestXDeltaRoundTripAndRejection|TestB6(HistoryApplyConverges|InterruptedRecovers)|TestB7OptimizeMultiBase)' .
go test -race -run '^(TestC14SparseXD|TestXDeltaRoundTripAndRejection)' .
```

The input source versions and digests are in the C5 and C12 manifests.
`neutral.jsonl` records one row per method and pair. `native-sparse*.jsonl`
and `native-sqlite.jsonl` record native program length, process metrics and
range reads. Store rows include both trial orders, exact consumer results
and per-stage I/O. Retained allocation describes the state before any
compatibility reversion.
