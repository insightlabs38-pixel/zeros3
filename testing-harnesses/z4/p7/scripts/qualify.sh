#!/bin/bash
# usage: qualify.sh OUTDIR   (run from the repository root)
# P7 qualification: format, vet, build, tests of every harness package that depends on a package
# P7 touched, race runs, short fuzz runs, mechanical extraction + rehearsal, production-unchanged check.
set -uo pipefail
O=$(realpath -m "$1"); mkdir -p "$O"; Z=testing-harnesses/z3; L="$O/qualify.log"; : > "$L"; : > "$O/summary.txt"; fail=0
BASE=${P7_BASE:-d2029bfb36072bcc7a46715870942f5ddf460e3e}
CHANGED="qcore numcore mdelta2 brotliz3 lz4z3 sstz3 parquetz3 sqfsz3"
CMDS="cmd/z4p7 cmd/z4b5 cmd/z4b6 cmd/z3parquet"
step() { local n="$1"; shift; local t0=$SECONDS; echo "### $n: $*" >> "$L"
  if "$@" >> "$L" 2>&1; then echo "PASS $n ($((SECONDS-t0))s)" | tee -a "$O/summary.txt"; else echo "FAIL $n" | tee -a "$O/summary.txt"; fail=1; fi; }
fmtcheck() { local out; out=$(cd $Z && gofmt -l $CHANGED $CMDS); [ -z "$out" ] || { echo "$out"; return 1; }; }
# every package of the harness module whose dependency closure contains a changed package
RDEPS=$(cd $Z && go list -f '{{.ImportPath}} {{join .Deps " "}}' ./... | awk -v c="$CHANGED" 'BEGIN{n=split(c,a," ")} {for(i=1;i<=n;i++){p="zeros3-z3/"a[i]; if($1==p||index(" "$0" "," "p" ")){print $1; next}}}' | sed 's|^zeros3-z3|.|' | tr '\n' ' ')
echo "packages under test: $RDEPS" >> "$L"
step gofmt fmtcheck
step vet go -C $Z vet $RDEPS
step build go -C $Z build -o /dev/null ./cmd/z4p7
step test go -C $Z test -count=1 $RDEPS
step race go -C $Z test -count=1 -race ./numcore ./lz4z3 ./brotliz3
for t in numcore:FuzzOpenObject numcore:FuzzPatches brotliz3:FuzzDecode brotliz3:FuzzEncodeDecode lz4z3:FuzzDecode lz4z3:FuzzRecipe lz4z3:FuzzFrames; do
  step "fuzz-$t" go -C $Z test ./${t%%:*} -run '^$' -fuzz "^${t##*:}\$" -fuzztime ${P7_FUZZTIME:-20s}
done
prod_unchanged() { local out; out=$(git diff --name-only $BASE HEAD | grep -v '^testing-harnesses/'); [ -z "$out" ] || { echo "$out"; return 1; }; git diff --quiet HEAD -- zeros3.go go.mod; }
step production-unchanged prod_unchanged
step production-build go build -o /dev/null .
step extraction bash testing-harnesses/z4/p7/scripts/extract_all.sh "$O/extraction"
for k in numeric hard; do
  python3 -I -c "import json,sys; d=json.load(open(sys.argv[1])); print(sys.argv[2], d['class'], 'declarations', d['audit']['selected_declarations'], 'names', d['package_level_names'], 'vectors', d['vector_lines'], 'mismatches', d['vector_mismatches'], 'fidelity_failures', d['fidelity_failures'], 'rehearsal_ok', d['rehearsal_ok'])" "$O/extraction/$k/extraction.json" $k >> "$O/summary.txt" 2>/dev/null
  grep -h "REHEARSAL" "$O/extraction/$k/rehearsal.txt" >> "$O/summary.txt" 2>/dev/null
done
cat "$O/extraction/production_unchanged.txt" >> "$O/summary.txt" 2>/dev/null
echo "qualification $([ $fail = 0 ] && echo PASSED || echo FAILED)" | tee -a "$O/summary.txt"
exit $fail
