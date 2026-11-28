# Reproduce GF7

Stage the [development files](../GF0/REPRODUCE.md) and
[adjacent held-out gzip](../GF2/REPRODUCE.md). Set
`ZEROS3_GF2_OLD_BINARY` to a pre-GF2 ZeroS3 executable for old-reader
refusal. With the scratch fixture root configured, run from the repo root.

```bash
h="$PWD/testing-harnesses/grand-frontier"
ZEROS3_GF0_CORPUS="$h/GF0/corpus.json" GOMAXPROCS=2 go test -v -count=1 -timeout=2m \
  -run '^TestGF7(TranslatedAuthority|PublicationCuts|NativeTranslationPanel|SignedAuthorityPanel|TranslatedControl)$' .
ZEROS3_GF0_CORPUS="$h/GF2/heldout.json" GOMAXPROCS=2 go test -v -count=1 -timeout=2m \
  -run '^TestGF7(NativeTranslationPanel|SignedAuthorityPanel)$/^typescript-gzip-adjacent$' .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzGF7TranslatedFrame$' -fuzztime=6000x -parallel=2 .
```

`GF7` and `GF7AUTH` emit raw JSON. Allocations are filesystem blocks
after finalization, and latency arrays are p50/p95/p99 nanoseconds. Use
local disposable Linux storage and interpret cold and warm cache figures
within the corresponding harness.
