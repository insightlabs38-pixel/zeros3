# Reproduce GF0

Place the fixtures at the paths relative to a local scratch root in
[corpus.json](corpus.json). The ordered paths in each family are revisions.
The harness verifies their exact size and SHA-256. Use Go 1.27.0 and a local
disposable Linux filesystem with enough free space for two test stores.

- Stage TypeScript and HTTPX with the pins and gzip inflation in
  [C12 reproduction](../../competitive/c12/REPRODUCE.md) and
  [C12 frozen manifest](../../competitive/c12/fixtures.json).
- Place the two original urllib3 release archives from
  [C19 public fixture URLs](../../competitive/c19/inputs.json).
- Use the SQLite, Parquet, sparse volume, typed tensors and entropy in the
  [C14 fixture manifest](../../competitive/c14/fixtures.json). The pinned
  recipes and tools are in [C14 reproduction](../../competitive/c14/REPRODUCE.md).
  Verify the actual checksums before comparing runs on regenerated databases,
  volumes or metadata-sensitive artifacts.

```bash
export ZEROS3_GF0_FIXTURES=/path/to/scratch
export ZEROS3_GF0_CORPUS="$PWD/testing-harnesses/grand-frontier/GF0/corpus.json"
GOMAXPROCS=2 go test -v -count=1 -timeout=4m -run '^TestGF0NativeBaseline$' . | tee gf0.log
sed -n 's/^GF0 //p' gf0.log > gf0.jsonl
```

A single family is available via `-run '^TestGF0NativeBaseline$/^sqlite$'`.
Keep the timing run idle. The two passes share the OS page cache. Reopen and
same-offset repeat timings have distinct fields in the raw rows.
