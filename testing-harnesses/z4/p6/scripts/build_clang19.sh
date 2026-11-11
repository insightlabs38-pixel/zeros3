#!/bin/bash
# usage: build_clang19.sh B4WORK   -- second toolchain for the LLVM lane: the six pinned SQLite amalgamations
# (already unpacked by z4/b4/build_corpus.sh) compiled with clang-19 using the same flags as the clang-18 corpus.
set -euo pipefail
W=$1; mkdir -p "$W/bc19"
for v in 3450100 3450200 3450300 3460000 3460100 3490100; do
  for o in O2 O0; do
    [ "$o" = O0 ] && [ "$v" = 3450200 -o "$v" = 3460000 ] && continue
    (cd "$W/sq/$v/sqlite-amalgamation-$v" && clang-19 -target x86_64-unknown-linux-gnu -$o -g0 -fno-ident \
      -DSQLITE_THREADSAFE=1 -DSQLITE_OMIT_LOAD_EXTENSION=1 -DSQLITE_DEFAULT_MEMSTATUS=0 \
      -emit-llvm -c sqlite3.c -o "$W/bc19/sqlite-$v-$o.bc")
  done
done
sha256sum "$W"/bc/*.bc "$W"/bc19/*.bc | sed "s#$W/##" > "$W/bc_pins.txt"
