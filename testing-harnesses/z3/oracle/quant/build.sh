#!/bin/sh
# usage: build.sh LLAMA_SRC OUTDIR   (LLAMA_SRC = llama.cpp checkout, pinned tag b3991 in z4/p7/README.md)
# Builds the oracle under several floating-point code generation settings (producer build variation).
set -eu
S=$1; O=$2; H=$(cd "$(dirname "$0")" && pwd); mkdir -p "$O"
I="-I$S/ggml/include -I$S/ggml/src"
b() { n=$1; shift; gcc -w -DNDEBUG "$@" $I -o "$O/qoracle-$n" "$H/qoracle.c" "$S/ggml/src/ggml-quants.c" "$S/ggml/src/ggml-aarch64.c" -lm; }
b o2 -O2
b o3 -O3
b o3-avx2-fma -O3 -mavx2 -mfma -mf16c
b o3-avx2-nocontract -O3 -mavx2 -mfma -mf16c -ffp-contract=off
b o0 -O0
