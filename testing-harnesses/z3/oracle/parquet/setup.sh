#!/bin/sh
# Z3-31 producers: pinned Python venv OUTSIDE Git. Nothing here is a runtime dependency of the Z3 module.
set -e
V="${Z3_PQ_VENV:-$HOME/.cache/z3-parquet/venv}"
python3 -m venv "$V"
"$V/bin/pip" install -q numpy==2.5.4 pyarrow==18.1.0 duckdb==2.0.0
echo "$V/bin/python"
