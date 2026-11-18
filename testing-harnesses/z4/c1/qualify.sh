#!/bin/bash
# Z4-C1 final qualification of the current tree. usage: qualify.sh OUTDIR [SCRATCH]   (from the repository root)
# Every step runs to completion and records its exit code and wall time in OUTDIR/summary.tsv; nothing aborts the run.
set -uo pipefail
O=$(realpath -m "$1"); S=$(realpath -m "${2:-$1/scratch}"); mkdir -p "$O" "$S"
ROOT=$(pwd); C1=testing-harnesses/z4/c1
: > "$O/summary.tsv"
step() { local name=$1; shift; local t=$(date +%s); "$@" > "$O/$name.log" 2>&1; local rc=$?; printf '%s\trc=%s\t%ss\n' "$name" "$rc" "$(( $(date +%s) - t ))" | tee -a "$O/summary.tsv"; }
step gofmt test -z "$(gofmt -l *.go)"
step vet go vet ./...
step build_reproducible sh scripts/reproducible_build.sh
step go_mod_unchanged git diff --exit-code origin/main -- go.mod go.sum
step toc_source bash -c 'cd testing-harnesses && go run ./harness/toc_check -file ../zeros3.go'
step toc_tests bash -c 'cd testing-harnesses && go run ./harness/toc_check -file ../zeros3_test.go'
step clean_checkout bash -c "rm -rf $S/clean && git clone -q --local $ROOT $S/clean && cd $S/clean && git checkout -q \$(git -C $ROOT rev-parse HEAD) && go build ./... && go vet . && test -z \"\$(git status --porcelain)\""
step root_suite go test -count=1 -timeout 180m .
step authority_short bash -c 'go test -count=1 -run "TestAuthority|FuzzAuth|TestXDelta" -v . | grep -E "^(--- |ok|FAIL|PASS)" | sort | uniq -c'
step persistence_dense env ZEROS3_PL_CUTS=300 go test -count=1 -run TestAuthorityPersistenceLossModel -timeout 120m .
step five_generations env ZEROS3_C1_MATRIX=1 go test -count=1 -run TestAuthorityFiveGenerationMatrix -timeout 60m .
step crash_loops go test -count=1 -run 'TestAuthorityCrashMatrix|TestAuthorityRepeatedCrashLoops|TestAuthorityRepairCrashQualification' -timeout 60m .
for f in FuzzXDeltaEnvelope FuzzAuthRecord FuzzAuthWindowRecord; do step fuzz_$f go test -run '^$' -fuzz "^$f\$" -fuzztime 40s .; done
step g1_retire_everything_hook bash -c "ZEROS3_G1_RETIRE_ALL=1 ZEROS3_G1_STACKS=$O/g1_stacks.txt go test -skip TestAuthority -json -timeout 120m . > $O/g1_run.json 2> $O/g1_run.err; true"
step g1_ledger python3 -I $C1/g1_ledger.py "$O/g1_run.json" "$O/g1_stacks.txt" "$O/g1_ledger.json"
step prodshadow go -C testing-harnesses/z3 test ./prodshadow ./numcore ./qcore -count=1
step scale_1m bash -c "TINY=900000 $C1/run_scale.sh $S/scale 100000 | tee $O/scale.txt"
step large_3x256mib bash -c "$C1/run_large.sh $S/large256 268435456 | tee $O/large256.txt"
step large_3x1gib bash -c "$C1/run_large.sh $S/large | tee $O/large.txt"
step large_3x2gib_control bash -c "$C1/run_large.sh $S/large2g 2147483648 | tee $O/large2g.txt"
step real_models env ZEROS3_C1_MODELS="${ZEROS3_C1_MODELS:-$S/models}" go test -count=1 -run TestAuthorityZN7RealModels -v -timeout 90m .
step race_suite go test -race -count=1 -timeout 360m .
echo "---- summary"; cat "$O/summary.tsv"
