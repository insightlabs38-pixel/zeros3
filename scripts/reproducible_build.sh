#!/bin/sh
# reproducible_build.sh — proves ZeroS3's release build is byte-for-byte
# reproducible.
#
# Builds zeros3.go twice, from two independent copies of the source tree
# at two different absolute paths, into two different output locations,
# using the exact frozen release flags, then compares SHA-256 hashes.
# Exits nonzero if they differ.
#
# This script is not part of the shipped implementation (zeros3.go
# remains the sole implementation source file); it exists only to make
# the reproducibility proof a single repeatable command instead of a
# hand-typed procedure.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

src_a="$work/src-a"
src_b="$work/src-b"
mkdir -p "$src_a" "$src_b" "$work/out-a" "$work/out-b"
cp "$repo_root/zeros3.go" "$repo_root/go.mod" "$src_a/"
cp "$repo_root/zeros3.go" "$repo_root/go.mod" "$src_b/"

build() {
	# $1 = source dir, $2 = output binary path
	(
		cd "$1"
		CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o "$2" zeros3.go
	)
}

echo "Go toolchain: $(go version)"
echo "Building copy A ($src_a) -> $work/out-a/zeros3"
build "$src_a" "$work/out-a/zeros3"
echo "Building copy B ($src_b) -> $work/out-b/zeros3"
build "$src_b" "$work/out-b/zeros3"

sha_a=$(sha256sum "$work/out-a/zeros3" | cut -d' ' -f1)
sha_b=$(sha256sum "$work/out-b/zeros3" | cut -d' ' -f1)

echo ""
echo "SHA-256 (copy A): $sha_a"
echo "SHA-256 (copy B): $sha_b"

if [ "$sha_a" != "$sha_b" ]; then
	echo ""
	echo "REPRODUCIBILITY FAILED: hashes differ."
	echo "go version -m (A):"
	go version -m "$work/out-a/zeros3"
	echo "go version -m (B):"
	go version -m "$work/out-b/zeros3"
	exit 1
fi

echo ""
echo "REPRODUCIBLE: both builds are byte-for-byte identical."
