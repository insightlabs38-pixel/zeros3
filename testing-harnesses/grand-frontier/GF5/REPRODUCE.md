# Reproduce GF5

Download the three exact public safetensors URLs in [inputs.json](inputs.json)
into a local scratch directory with their listed filenames. Confirm every
SHA-256 before running. No credentials or model execution are needed.

```bash
export ZEROS3_GF5_FIXTURES=/path/to/checkpoints
GOMAXPROCS=2 go test -v -count=1 -timeout=4m \
  -run '^TestGF5(CheckpointIncumbent|CheckpointPanel|LayoutAndDTypeControls|TensorResiduals)$' .
GOMAXPROCS=2 go test -race -count=1 -timeout=90s \
  -run '^TestGF5(TensorResiduals|LayoutAndDTypeControls)$' \
  -fuzz '^FuzzGF5TensorWire$' -fuzztime=3000x -parallel=2 .
```

Collect `GF5PACK` and `GF5INC` lines as JSONL. For the reference codec
use zstd 1.5.7 at `-6 -T1` on every file. Repeat with
`--patch-from=previous.safetensors` for the two descendants and use
the same option during decode. Count all three outputs and check
each decoded file against the exact original digest.
