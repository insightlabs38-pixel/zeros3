#!/bin/sh
# Z3-23/24 external tool setup. Everything is built/installed OUTSIDE Git under
# $Z3_TOOLS (default /root/z3cache) and cached; re-running is a no-op when present.
# Pinned: zstd/squashfs-tools from Ubuntu 24.04 apt (zstd 1.5.5, mksquashfs 4.6.1),
# erofs-utils v1.8.10 (source, adds zstd), composefs HEAD ec2573a (1.0.8), nydus v2.3.6.
set -eu
T=${Z3_TOOLS:-/root/z3cache}
mkdir -p "$T/src" "$T/tools" "$T/nydus"
command -v zstd >/dev/null && command -v mksquashfs >/dev/null || apt-get install -y zstd squashfs-tools attr
if [ ! -x "$T/tools/bin/mkfs.erofs" ]; then
  apt-get install -y autoconf automake libtool libzstd-dev liblz4-dev liblzma-dev libdeflate-dev uuid-dev
  git clone -q --depth 1 --branch v1.8.10 https://github.com/erofs/erofs-utils.git "$T/src/erofs-utils"
  (cd "$T/src/erofs-utils" && ./autogen.sh && ./configure --prefix="$T/tools" --with-libzstd --with-libdeflate --disable-fuse && make -j4 && make install)
fi
if [ ! -x "$T/tools/bin/mkcomposefs" ]; then
  apt-get install -y meson ninja-build pkg-config libssl-dev libfuse3-dev
  git clone -q --depth 1 https://github.com/containers/composefs.git "$T/src/composefs"
  (cd "$T/src/composefs" && git checkout -q ec2573a0f68f548ae91f3e10acc990a08f9122dc 2>/dev/null || true
   meson setup "$T/build-cfs" -Dprefix="$T/tools" -Dfuse=disabled -Dman=disabled && ninja -C "$T/build-cfs" install)
fi
if [ ! -x "$T/nydus/nydus-static/nydus-image" ]; then
  curl -sSL -o "$T/nydus/nydus.tgz" https://github.com/dragonflyoss/nydus/releases/download/v2.3.6/nydus-static-v2.3.6-linux-amd64.tgz
  echo "456b65c1b918be87271e21e627597d1ae2de29cc91a7bc545f4f5c507e30b6c9  $T/nydus/nydus.tgz" | sha256sum -c -
  tar -xzf "$T/nydus/nydus.tgz" -C "$T/nydus"
fi
(cd "$(dirname "$0")" && go build -o "$T/z3cprod" .)
echo "tools ready in $T"
