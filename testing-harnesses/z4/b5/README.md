# Z4-B5 reproduction

Code: `testing-harnesses/z3/mdelta2` (library), `testing-harnesses/z3/cmd/z4b5` (driver). Research only; production untouched.

1. Corpus (pins + sha256 verified): `python3 fetch.py $D` (`$D` ~16 GB incl. controls); `pins.json` is written there and copied to `pins.json` here.
2. Synthetic controls: `z4b5 controls $D/smollm2/base.safetensors $D/controls`
3. Family policies with fresh-process replay and independent hashing: `./run_all.sh $BIN $OUT` (nonzero exit on any inexact state; see `$OUT/status.txt`)
4. Experiments (all print JSON/text and write `-json` files): `z4b5 explore|sparse|discover|matrix|coderbench|b1|quant-repro|model8|cdc`
5. Regression for the repaired B3/B4 scripts: `./test_evidence_scripts.sh`
6. Tests: `cd testing-harnesses/z3 && go test ./mdelta2 ./cmd/z4b5 && go vet ./mdelta2 ./cmd/z4b5`

`evidence.json.gz` merges the machine-readable outputs; the report is `../../results/Z4_B5_MODEL_DELTA2.md`.
