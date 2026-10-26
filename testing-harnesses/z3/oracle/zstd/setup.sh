#!/bin/sh
# Z3-25 external reference zstd builds, kept OUTSIDE Git under $Z3_TOOLS (default
# /root/z3cache) and cached; re-running is a no-op when present. Pinned release
# tarballs (facebook/zstd v1.5.5 and v1.5.7) are verified by SHA-256. Each build is
# installed as $Z3_TOOLS/tools/zstd-<version>/zstd; the system zstd (1.5.5 from apt, the
# Z3-23 producer) is left alone. The reference binaries are differential oracles and
# producers only: nothing in zstdz3 calls them.
set -eu
T=${Z3_TOOLS:-/root/z3cache}
mkdir -p "$T/src" "$T/tools"
build() {
  v=$1; sum=$2
  [ -x "$T/tools/zstd-$v/zstd" ] && return 0
  [ -f "$T/src/zstd-$v.tar.gz" ] || curl -sSL -o "$T/src/zstd-$v.tar.gz" "https://github.com/facebook/zstd/releases/download/v$v/zstd-$v.tar.gz"
  echo "$sum  $T/src/zstd-$v.tar.gz" | sha256sum -c -
  tar -xzf "$T/src/zstd-$v.tar.gz" -C "$T/src"
  make -C "$T/src/zstd-$v/programs" -j4 zstd >/dev/null
  mkdir -p "$T/tools/zstd-$v"
  cp "$T/src/zstd-$v/programs/zstd" "$T/tools/zstd-$v/zstd"
}
build 1.5.5 9c4396cc829cfae319a6e2615202e82aad41372073482fce286fac78646d3ee4
build 1.5.7 eb33e51f49a15e023950cd7825ca74a4a2b43db8354825ac24fc1b7ee09e6fa3
# independent producer: klauspost/compress via the containers oracle module
(cd "$(dirname "$0")/../containers" && go build -o "$T/z3cprod" .)
for v in 1.5.5 1.5.7; do "$T/tools/zstd-$v/zstd" --version; done
