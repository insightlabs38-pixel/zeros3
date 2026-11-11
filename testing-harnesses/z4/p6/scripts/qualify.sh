#!/bin/bash
# usage: qualify.sh B4CORPUS   (from the repository root) -- the P6 research qualification suite, one log.
set -uo pipefail
Z=testing-harnesses/z3; C=${1:-}
run() { echo "### $*"; "$@" 2>&1 | tail -${TAILN:-6}; echo "exit=${PIPESTATUS[0]}"; echo; }
echo "### gofmt"; gofmt -l $Z/semcore $Z/semcas $Z/graphcore $Z/graphnorm $Z/cmd/z4p6 $Z/cmd/p6extract $Z/cmd/p6alias $Z/dcore $Z/simdelta; echo "(empty = clean)"
run go -C $Z vet ./semcore ./semcas ./graphcore ./graphnorm ./dcore ./simdelta ./cmd/z4p6 ./cmd/z4b3 ./cmd/z4b4 ./cmd/p6extract ./cmd/p6alias
run go -C $Z build ./...
run go -C $Z test -count=1 ./semcore ./semcas ./graphcore ./graphnorm ./dcore ./simdelta
[ -n "$C" ] && B4_CORPUS=$C run go -C $Z test -count=1 ./graphcore
run go -C $Z test -race -count=1 ./semcore ./graphcore ./dcore
for f in FuzzParseRecord FuzzParseRecipe FuzzDetectAt; do run go -C $Z test ./semcore -run '^$' -fuzz "^$f\$" -fuzztime 15s; done
for f in FuzzVM FuzzBitstream FuzzRecipe FuzzPDBandDEX; do run go -C $Z test ./graphcore -run '^$' -fuzz "^$f\$" -fuzztime 15s; done
echo "### production source unchanged"; git diff --stat ded8d04 -- zeros3.go go.mod 'zeros3*_test.go' | tail -2; echo "(empty = unchanged)"
