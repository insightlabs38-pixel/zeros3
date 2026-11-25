#!/usr/bin/env bash
# Cross-build and run the operational smoke under an installed QEMU user emulator.
set -euo pipefail
work=$(realpath -m "${1:?pass a scratch directory}")
export ZEROS3_C4_QEMU=$(realpath "${2:?pass a qemu-aarch64 executable}")
harness=$(cd "$(dirname "$0")" && pwd)
root=$(git -C "$harness" rev-parse --show-toplevel)
mkdir -p "$work"
export ZEROS3_C4_ARM64="$work/zeros3-arm64"
(cd "$root" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOMAXPROCS=2 \
  "${C4_GO:-go}" build -trimpath -o "$ZEROS3_C4_ARM64" .)
runner="$work/arm64-run"
cat > "$runner" <<'RUNNER'
#!/bin/sh
exec "$ZEROS3_C4_QEMU" "$ZEROS3_C4_ARM64" "$@"
RUNNER
chmod +x "$runner"
"${C4_PYTHON:-python3}" "$harness/operations.py" --binary "$runner" \
  --scratch "$work" --results "$work/operations-arm64.jsonl"
