#!/bin/bash
# Fetch/derive the P0 corpora into $1 (scratch; never committed). All downloads are pinned+hash-checked by the
# reused scripts, or hashed into $1/MANIFEST.sha256 on first fetch (derived artifacts are deterministic).
# usage: fetch_corpus.sh CORPUS_DIR [tars|q6|models|git|sqlite|bitcode|py|all]
set -euo pipefail
C=$(realpath -m "${1:?corpus dir}"); mkdir -p "$C"; shift
WHAT=${1:-all}
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../../../.." && pwd)
want() { [ "$WHAT" = all ] || [ "$WHAT" = "$1" ]; }
if want tars; then   # real source-archive history: coreutils 9.5/9.6/9.7 (kind 1 tar, kind 6 gzip, B1/B2)
  mkdir -p "$C/src"
  for v in 9.5 9.6 9.7; do
    [ -f "$C/src/coreutils-$v.tar" ] || { curl -sSfL -o "$C/src/coreutils-$v.tar.xz" "https://ftp.gnu.org/gnu/coreutils/coreutils-$v.tar.xz"; xz -dk "$C/src/coreutils-$v.tar.xz"; }
    [ -f "$C/src/coreutils-$v.tar.gz" ] || curl -sSfL -o "$C/src/coreutils-$v.tar.gz" "https://ftp.gnu.org/gnu/coreutils/coreutils-$v.tar.gz"
  done
fi
if want q6; then     # Pythia-14m, F-Droid DEX, CAR, Newtonsoft CLR (pinned sources in z3/realworld/*.json)
  python3 "$ROOT/testing-harnesses/scripts/q6_fetch_frontier_inputs.py" "$C/q6" "$ROOT/testing-harnesses/z3"
fi
if want models; then python3 "$HERE/fetch_models.py" "$C/models"; fi
if want git; then    # hiredis history: three pack exports (kind 4 Git / B1)
  mkdir -p "$C/git"
  [ -d "$C/hiredis.git" ] || git clone -q --bare https://github.com/redis/hiredis "$C/hiredis.git"
  i=0
  for pin in 39de5267c092859b4cab4bdf79081e9634b70e39 c14775b4e48334e0262c9f168887578f4a368b5d 60e5075d4ac77424809f855ba3e398df7aacefe8; do
    [ -f "$C/git/h$i.pack" ] || echo "$pin" | git -C "$C/hiredis.git" pack-objects --stdout --revs --window=10 --depth=50 > "$C/git/h$i.pack"
    i=$((i+1))
  done
fi
if want bitcode; then bash "$ROOT/testing-harnesses/z4/b4/build_corpus.sh" "$C/b4"; fi
if want py; then     # SQLite/Parquet/SST/zstd producers (pip, in a scratch venv; never a production dependency)
  python3 -m venv "$C/venv"; "$C/venv/bin/pip" -q install pyarrow zstandard rocksdict numpy
  "$C/venv/bin/python" "$HERE/make_structured.py" "$C"
fi
( cd "$C" && find . -type f ! -name MANIFEST.sha256 ! -path './venv/*' ! -path './hiredis.git/*' -print0 | sort -z | xargs -0 sha256sum > MANIFEST.sha256 )
echo "corpus ready: $C ($(wc -l < "$C/MANIFEST.sha256") files)"
