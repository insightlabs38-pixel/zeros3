#!/bin/bash
# usage: build_corpus.sh WORKDIR   (needs clang, wasm-ld, python3, curl; GitHub is not required)
set -euo pipefail
W=$(realpath -m "${1:?workdir}"); mkdir -p "$W"/{bc,pdb,sq,ctl}; cd "$W"
declare -A SHA=( [3450100]=5592243caf28b2cdef41e6ab58d25d653dfc53deded8450eb66072c929f030c4
 [3450200]=65230414820d43a6d1445d1d98cfe57e8eb9f7ac0d6a96ad6932e0647cce51db
 [3450300]=ea170e73e447703e8359308ca2e4366a3ae0c4304a8665896f068c736781c651
 [3460000]=712a7d09d2a22652fb06a49af516e051979a3984adb067da86760e60ed51a7f5
 [3460100]=77823cb110929c2bcb0f5d48e4833b5c59a8a6e40cdea3936b99e199dbbe5784
 [3490100]=6cebd1d8403fc58c30e93939b246f3e6e58d0765a5cd50546f16c00fd805d2c3 )
for v in "${!SHA[@]}"; do
  y=2024; [ "$v" = 3490100 ] && y=2025
  curl -sSf -o s$v.zip https://www.sqlite.org/$y/sqlite-amalgamation-$v.zip
  echo "${SHA[$v]}  s$v.zip" | sha256sum -c -
  unzip -qo s$v.zip -d sq/$v
  for o in O2 O0; do
    [ "$o" = O0 ] && [ "$v" = 3450200 -o "$v" = 3460000 ] && continue
    (cd sq/$v/sqlite-amalgamation-$v && clang -target x86_64-unknown-linux-gnu -$o -g0 -fno-ident \
      -DSQLITE_THREADSAFE=1 -DSQLITE_OMIT_LOAD_EXTENSION=1 -DSQLITE_DEFAULT_MEMSTATUS=0 \
      -emit-llvm -c sqlite3.c -o "$W/bc/sqlite-$v-$o.bc")
  done
done
# Portable PDBs: official Newtonsoft.Json symbol packages (netstandard2.0 member)
for v in 12.0.3 13.0.1 13.0.2 13.0.3 13.0.4; do
  curl -sSfL -o pdb/$v.snupkg https://www.nuget.org/api/v2/symbolpackage/Newtonsoft.Json/$v
  python3 -I - "$v" <<'PY'
import zipfile,sys
v=sys.argv[1]
open(f'pdb/{v}-netstandard2.0.pdb','wb').write(zipfile.ZipFile(f'pdb/{v}.snupkg').read('lib/netstandard2.0/Newtonsoft.Json.pdb'))
PY
done
# controlled C histories and their wasm counterparts
cp "$(dirname "$0")/gen_controlled.py" ctl/gen.py; (cd ctl && python3 gen.py && for v in base early late type global const rename restored; do
  cp $v.c input.c; clang -target x86_64-unknown-linux-gnu -O2 -g0 -fno-ident -emit-llvm -c input.c -o $v.bc; done)
mkdir -p ctl/wasm; for v in base early late type global const rename restored; do
  sed -e 's/#include <string.h>/extern unsigned long strlen(const char *);/' -e 's/#include <stdio.h>/extern int printf(const char *, ...);/' -e 's/#include <stdlib.h>//' ctl/$v.c > ctl/wasm/input.c
  (cd ctl/wasm && clang --target=wasm32-unknown-unknown -O2 -nostdlib -ffreestanding -c input.c -o $v.o && wasm-ld --no-entry --export-all --allow-undefined -o $v.wasm $v.o); done
echo "corpus ready in $W (DEX/CLR inputs: scripts/q6_fetch_frontier_inputs.py CACHE z3)"
