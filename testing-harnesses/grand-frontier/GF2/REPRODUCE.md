# Reproduce GF2 compiled candidates

Set the two GF0 fixture variables from
[GF0 reproduction](../GF0/REPRODUCE.md). This pass used the same machine,
Go version and 4 KiB overlay filesystem as GF0.

```bash
GOMAXPROCS=2 go test -v -count=1 -timeout=3m -run '^TestGF2NativePatchPanel$' .
ZEROS3_GF2_TUNE=1 GOMAXPROCS=2 go test -v -count=1 -timeout=3m -run '^TestGF2NativePatchPanel$/^typescript-gzip$' .
ZEROS3_GF2_RELATIVE=tune GOMAXPROCS=2 go test -v -count=1 -timeout=4m -run '^TestGF2NativePatchPanel$' .
GOMAXPROCS=2 go test -v -count=1 -timeout=4m -run '^TestGF2(FastWitness|WitnessPanel|BudgetedOverlay|ShiftedProgram)$' .
ZEROS3_GF0_CORPUS="$PWD/testing-harnesses/grand-frontier/GF2/heldout.json" \
  ZEROS3_GF2_RELATIVE=fixed ZEROS3_GF2_HELDOUT=1 GOMAXPROCS=2 \
  go test -v -count=1 -timeout=3m -run '^TestGF2NativePatchPanel$' .
GOMAXPROCS=2 go test -race -count=1 -timeout=2m -run '^TestGF2(FastWitness|ShiftedProgram)$' \
  -fuzz '^FuzzGF2FrameProgram$' -fuzztime=5000x -parallel=2 .
```

Capture `GF2PATCH`, `GF2WITNESS` and `GF2OVERLAY` result lines
after removing their prefixes. The controls use generated tar ordering
and Go gzip coding levels, with deterministic names and timestamps.
The selected held-out TypeScript pair shares its base release with the
development series and has an unseen gzip target. urllib3 is a separate
family. The 32 KiB patch lab samples each target after reopen, with
ambient Linux caches. Witness range timing includes checkpoint lookup
and sparse native plaintext reads. It assumes the witness is in memory.
