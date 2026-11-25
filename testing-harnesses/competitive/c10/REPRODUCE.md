# Reproduce C10

Build C9 at `3cf73e37205c98525686fbf470756dc4a2e46c66` and C10 with
Go 1.27.0. Use the C1 Python environment with the existing boto3
requirements. The harness binds disposable signed S3 endpoints to loopback.

Stage C5's TypeScript, Parquet, SST, entropy and HTTPX families. Use the C1
generated `package` family as `zip`. Place the C2 RuboCop 1.66.0 and 1.66.1
public gems at `rubygems/0/rubocop-1.66.0.gem` and
`rubygems/1/rubocop-1.66.1.gem`. Check each path against `fixtures.json`.
The C1, C2 and C5 producer scripts and their frozen manifests specify source
versions, archive digests and deterministic generators.

From this directory, with absolute paths for `B9`, `B10`, `CORPUS` and
`WORK` and the C1 Python interpreter at `P`:

```bash
families=(typescript rubygems zip parquet sst entropy httpx)
args=(--baseline "$B9" --work "$WORK/stores" --corpus "$CORPUS")
"$P" read_frontier.py "${args[@]}" --prepare --families "${families[@]}" \
  --results "$WORK/prepare.jsonl"
export ZEROS3_GZIP_SEQUENTIAL=1
"$P" read_frontier.py "${args[@]}" --candidate "$B10" \
  --families typescript --lanes cdc canonical --samples 24 --repeats 2 \
  --results "$WORK/typescript-core.jsonl"
"$P" read_frontier.py "${args[@]}" --candidate "$B10" \
  --families typescript --lanes deep --samples 12 --full-samples 6 --repeats 2 \
  --results "$WORK/typescript-deep.jsonl"
"$P" read_frontier.py "${args[@]}" --candidate "$B10" \
  --families rubygems --samples 16 --full-samples 8 --repeats 2 \
  --results "$WORK/rubygems.jsonl"
"$P" read_frontier.py "${args[@]}" --candidate "$B10" \
  --families zip parquet sst entropy httpx --samples 12 --full-samples 6 --repeats 1 \
  --results "$WORK/controls.jsonl"
"$P" read_frontier.py "${args[@]}" --candidate "$B10" \
  --families typescript --lanes deep --sizes 4096 65536 --widths 4 \
  --samples 120 --repeats 1 --results "$WORK/sparse-tail.jsonl"
./profile.sh "$WORK/c10-profile" "$WORK/stores/typescript/deep/store" \
  1/package.tgz worktree
```

Reuse that standalone profiling directory to run the race check against the
already finalized HTTPX lane.

```bash
(cd "$WORK/c10-profile" &&
  C10_STORE="$WORK/stores/httpx/canonical/store" C10_KEY=0/source.tar.gz \
  C10_SOURCE="$CORPUS/httpx/0/source.tar.gz" \
  GOMAXPROCS=2 go test -race -count=1 -timeout=45s -run '^TestC10ConcurrentRead$')
```

Profile C9 by passing its commit as the last argument. The baseline ignores
the sequential flag. To reproduce the early-exit ablation, apply
`frontier-stop.patch` with `git apply --unidiff-zero` to the C10 replay
checkpoint in a disposable checkout. Build the patched binary and compare
it to unpatched C10, with the sequential flag enabled in both processes.
Use only the TypeScript deep lane with 3 whole reads per phase, 1 client,
and 2 pairs. The ablation JSONL labels `c9` and `c10` then refer to those two
experimental builds.
