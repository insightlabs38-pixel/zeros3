# Reproduce C15

Use Go 1.27.0 and the C1 Python environment. Build the current engine and
the fixed-run control at `29cb2d5`. The C7 report uses the same indexed,
one-target search. C15 isolates the index change between adjacent
binaries. Public TypeScript input identities come from the C12 manifest.
The case seal is SHA-256 over sorted UTF-8 keys with a zero separator,
the eight-byte little-endian length, and binary SHA-256 of each object.

```bash
"$PYTHON" "$HARNESS/layouts.py" --results "$WORK/layouts.jsonl"
"$PYTHON" "$HARNESS/campaign.py" --baseline "$C14_BINARY" --candidate "$C15_BINARY" \
  --corpus "$C5_CORPUS" --c12 "$C12_CORPUS" \
  --work "$WORK/store-trials" --results "$WORK/campaign.jsonl"
go test -run '^(TestC15SimilarityRunGeometry|TestAuthoritySimilarity)' -count=1 .
go test -race -run '^(TestC15SimilarityRunGeometry|TestAuthoritySimilarity)' -count=1 .
```

Add `go_runs_test.go.txt` as a `*_test.go` overlay in a scratch Go test
build. Run `ZEROS3_C15_RUNS=1 ./c15.test -test.run '^TestC15PostingRunPanel$' -test.v`
with `GOMAXPROCS=2`. Extract lines with the `C15RUN ` prefix. Files are
fsynced before verification. The fixed lane uses the independent ZSR1
fixture writer, so its write time is descriptive. Every trial measures
positioned reads and checks all 768 results against expected ordered IDs.

```bash
"$PYTHON" "$HARNESS/../c7/namespace.py" --root-tests "$C15_LAB" --zeros3 "$C15_BINARY" \
  --scratch "$WORK/namespace" --counts 1024 65536 262144 --results "$WORK/namespace.jsonl"
```

The campaign rows distinguish packed bodies from the complete index
directory across generations. Allocation deduplicates hardlinked runs
and includes catalogs, pointers and directories. Foreground rows label
read-only command overlap and the stale fence check. The metadata-only
probe has no object payloads.
