# Reproduce the GF1 core

Set `ZEROS3_GF0_FIXTURES` and `ZEROS3_GF0_CORPUS` as in
[GF0 reproduction](../GF0/REPRODUCE.md). The toy oracle runs without fixtures.

```bash
GOMAXPROCS=2 go test -v -count=1 -timeout=2m -run '^TestGF1' . | tee gf1.log
sed -n 's/^GF1 //p' gf1.log > gf1.jsonl
```

The native catalog experiment builds current normalized archive policies
with sparse files and XD, then restricts alternate exact programs to the
oldest direct parent. The harness omits structural/canonical alternatives in
this catalog. Use GF0 rows for their fully materialized comparison. Release
fixtures are checked by SHA before ingestion.
