#!/bin/sh
# Opt-in: fetch and build the pinned preflate-rs oracle outside Git.
# Usage: oracle/preflate/setup.sh   -> prints the z3preflate binary path.
# Env: Z3_CACHE (default ${XDG_CACHE_HOME:-$HOME/.cache}/z3-oracle)
set -eu
REPO=https://github.com/microsoft/preflate-rs
COMMIT=3bcd33441849a8a25d2b26077128f4e3a88d071b   # tag v0.7.6, Apache-2.0
here=$(cd "$(dirname "$0")" && pwd)
cache=${Z3_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/z3-oracle}
mkdir -p "$cache/preflate"
cd "$cache/preflate"
if [ ! -d src/.git ]; then git clone --quiet "$REPO" src; fi
git -C src fetch --quiet origin "$COMMIT" 2>/dev/null || true
git -C src checkout --quiet "$COMMIT"
[ "$(git -C src rev-parse HEAD)" = "$COMMIT" ] || { echo "pin mismatch" >&2; exit 1; }
rm -rf wrap && mkdir -p wrap/src
cp "$here/Cargo.toml" wrap/Cargo.toml && cp "$here/src/main.rs" wrap/src/main.rs
cp src/Cargo.lock wrap/Cargo.lock
sed -i 's#\.\./src/preflate#../src/preflate#' wrap/Cargo.toml
(cd wrap && cargo build --release --quiet)
echo "$cache/preflate/wrap/target/release/z3preflate"
