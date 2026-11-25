# Reproduce C13

Use Go 1.27.0, the C12 production baseline `dcf5191`, C1's Python
environment, and the verified C12 fixture tree. Extract the pinned Python
zstandard wheel in scratch for the independent archive comparisons.
The pack laboratory and the S3 benchmark use separate scratch roots.

```bash
mkdir -p "$WORK"/{wheels,vendor}
"$PYTHON" -m pip download --only-binary=:all: --no-deps -d "$WORK/wheels" zstandard==0.25.0
(cd "$WORK/wheels" && sha256sum -c "$HARNESS/tools.sha256")
"$PYTHON" -m pip install --no-index --no-deps --find-links "$WORK/wheels" --target "$WORK/vendor" zstandard==0.25.0
export PYTHONPATH="$WORK/vendor"
"$PYTHON" "$HARNESS/fixtures.py" --root "$WORK/corpus" --c12 "$C12_CORPUS" --manifest "$HARNESS/fixtures.jsonl"
args=(--corpus "$WORK/corpus" --manifest "$HARNESS/fixtures.jsonl")
for scope in main heldout; do
  "$PYTHON" "$HARNESS/benchmark.py" "${args[@]}" --scope "$scope" --work "$WORK/final-packs" \
    --lanes deflate prefix zstd trained block block8 --results "$WORK/$scope.jsonl"
  "$PYTHON" "$HARNESS/benchmark.py" "${args[@]}" --scope "$scope" --work "$WORK/paid-packs" \
    --lanes prefix prefix-paid trained trained-paid --results "$WORK/$scope-paid.jsonl"
done
"$PYTHON" "$HARNESS/faults.py" "${args[@]}" --work "$WORK/faults"
native=(--baseline "$C12_BINARY" --work "$WORK/stores" --corpus "$WORK/corpus" --families main heldout)
"$PYTHON" "$HARNESS/native.py" "${native[@]}" --prepare --results "$WORK/native-prepare.jsonl"
"$PYTHON" "$HARNESS/native.py" "${native[@]}" --results "$WORK/native-read.jsonl"
```

The first six-mode comparison used the code at `7ff48fe`. Its pack formats
and allocations are also reproducible using the final writer. The paid
comparison re-encodes ungated controls with the same writer version.
