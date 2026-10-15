#!/bin/sh
# Builds the optional Git diff-delta oracle (research only, not a dependency).
# Fetches Git's diff-delta.c and delta.h at the pinned tag into ${Z3_CACHE:-~/.cache/z3-oracle}/gitdelta,
# verifies their SHA-256, compiles them with wrap.c, and prints the binary path.
# Git's sources are GPL-2.0 and are never committed or linked into the Z3 module.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
dir=${Z3_CACHE:-$HOME/.cache/z3-oracle}/gitdelta
tag=${Z3_GIT_TAG:-v2.43.0}
mkdir -p "$dir"
fetch() { # name sha256
	[ -f "$dir/$1" ] || curl -fsS -o "$dir/$1" "https://raw.githubusercontent.com/git/git/$tag/$1"
	echo "$2  $dir/$1" | sha256sum -c - >/dev/null || { echo "sha256 mismatch for $1" >&2; rm -f "$dir/$1"; exit 1; }
}
if [ "$tag" = v2.43.0 ]; then
	fetch diff-delta.c 5d4113046ea2eb026ef4ac474955ae20d4ea3c76455f082baad845418f769213
	fetch delta.h 7655d4d6d9f22c90dc1c67a94e95b5541b508ea7a4b1668eaa235f94edb375ea
else # exploratory build of another tag: not hash-pinned, recorded by the caller
	dir=$dir-$tag; mkdir -p "$dir"
	for f in diff-delta.c delta.h; do [ -f "$dir/$f" ] || curl -fsS -o "$dir/$f" "https://raw.githubusercontent.com/git/git/$tag/$f"; done
	sha256sum "$dir/diff-delta.c" >&2
fi
cc -O2 -std=gnu99 -I"$here" -I"$dir" -o "$dir/gitdelta" "$here/wrap.c" "$dir/diff-delta.c"
echo "$dir/gitdelta"
