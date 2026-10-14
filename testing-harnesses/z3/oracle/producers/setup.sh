#!/bin/sh
# Opt-in: build the extra DEFLATE producers used by `z3corpus extreal` outside Git.
#   python venv with zlib-ng, ISA-L (isal), libdeflate (deflate), zopfli, safetensors, numpy
#   z3miniz: miniz_oxide 0.8.0 behind a stdin/stdout CLI
# Prints the environment to export:  Z3_PYTHON=...  Z3_MINIZ=...
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cache=${Z3_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/z3-oracle}
mkdir -p "$cache/producers"
cd "$cache/producers"
[ -d venv ] || python3 -m venv venv
venv/bin/pip install -q zlib-ng isal deflate zopfli safetensors numpy
rm -rf miniz && mkdir -p miniz/src
cp "$here/miniz/Cargo.toml" "$here/miniz/Cargo.lock" miniz/ && cp "$here/miniz/src/main.rs" miniz/src/main.rs
(cd miniz && cargo build --release --quiet)
echo "export Z3_PYTHON=$cache/producers/venv/bin/python"
echo "export Z3_MINIZ=$cache/producers/miniz/target/release/z3miniz"
