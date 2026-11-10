#!/bin/bash
# Mechanical-portability check: re-extract the codec declarations from the lab sources with the AST/type-aware
# extractor (z3/cmd/prodcodecport) in each of its modes and require every extracted declaration to be present in
# the production zeros3.go, identical after gofmt/whitespace normalisation. Exit status 1 on any difference.
# usage: portcheck.sh [OUTDIR]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../../../.." && pwd); OUT=${1:-$(mktemp -d)}
mkdir -p "$OUT"; rc=0
for mode in "" managed git; do
  name=${mode:-codec}
  ( cd "$ROOT/testing-harnesses/z3" && go run ./cmd/prodcodecport . "$OUT/$name" $mode ) | sed "s/^/[$name] extract: /"
  ( cd "$ROOT/testing-harnesses/z4" && go run ./p4/cmd/portcheck "$OUT/$name.decls" "$ROOT/zeros3.go" ) | sed "s/^/[$name] /" || rc=1
done
exit $rc
