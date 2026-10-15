#!/bin/sh
# Z3-18 producers: pinned Python venv OUTSIDE Git. Nothing here is a runtime dependency of the Z3 module.
# libleveldb-dev is needed to build plyvel (apt-get install libleveldb-dev).
set -e
V="${Z3_STRUCT_VENV:-$HOME/.cache/z3-realworld/structured-venv}"
python3 -m venv "$V"
"$V/bin/pip" install -q numpy==2.5.4 pyarrow==18.1.0 zarr==2.18.4 numcodecs==0.15.1 plyvel==1.5.1 rocksdict==0.3.29
echo "$V/bin/python"
