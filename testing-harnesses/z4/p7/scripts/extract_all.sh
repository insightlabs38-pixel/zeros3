#!/bin/bash
# usage: extract_all.sh OUTDIR   (run from the repository root)
# Mechanical extraction of the P7 cores into a TEMPORARY copy of the production zeros3.go with the P6
# extractor (type-aware reachability, collision-checked renaming, compile, original-vs-extracted
# differential vectors, engine rehearsal). Nothing is written into the repository.
#   numeric : numcore + qcore (+ dcore for header deltas)   -> ZN7 model container, ggml quantizers
#   hard    : brotliz3 + lz4z3                              -> exact Brotli / LZ4 replay
set -euo pipefail
O=$(realpath -m "$1"); mkdir -p "$O"; Z=testing-harnesses/z3; Z3ABS=$(realpath $Z)
go -C $Z build -o "$O/p6extract" ./cmd/p6extract
sha_before=$(sha256sum zeros3.go go.mod | sha256sum | cut -c1-16)
roots() { cat "$@" | grep -o "@[a-z0-9]*\.[A-Za-z0-9_]*@" | tr -d '@' | sort -u | tr '\n' ',' | sed 's/,$//'; }
( cd $Z && P6_Z3=$Z3ABS "$O/p6extract" -module . -modpath zeros3-z3 -pkgs numcore,qcore -roots "$(roots numcore/vectors.go.txt numcore/rehearsal.go.txt)" \
    -prod ../../zeros3.go -work "$O/numeric" -prefix nc -vectors numcore/vectors.go.txt -rehearsal numcore/rehearsal.go.txt ) | tee "$O/extract_numeric.txt"
( cd $Z && P6_Z3=$Z3ABS "$O/p6extract" -module . -modpath zeros3-z3 -pkgs brotliz3,lz4z3 -roots "$(roots brotliz3/vectors.go.txt brotliz3/rehearsal.go.txt)" \
    -prod ../../zeros3.go -work "$O/hard" -prefix hc -vectors brotliz3/vectors.go.txt -rehearsal brotliz3/rehearsal.go.txt ) | tee "$O/extract_hard.txt"
rm -rf $Z/cmd/p6vec_tmp
sha_after=$(sha256sum zeros3.go go.mod | sha256sum | cut -c1-16)
echo "production zeros3.go+go.mod digest before=$sha_before after=$sha_after" | tee "$O/production_unchanged.txt"
[ "$sha_before" = "$sha_after" ]
diff <(cat go.mod) "$O/numeric/integration/go.mod" && diff <(cat go.mod) "$O/hard/integration/go.mod" && echo "go.mod unchanged in both integrations" | tee -a "$O/production_unchanged.txt"
