#!/bin/bash
# Final qualification of the checked-out tree. usage: qualify.sh OUTDIR [SCRATCH]   (from the repository root)
# Every step runs to completion; summary.tsv has one row per attempt (status, exit code, seconds, note) and
# summary.json carries the revision, the tree hash, whether the working tree was clean, the last attempt of
# every step and the earlier failed attempts. A step that is not run is recorded SKIP with its reason; nothing
# is reported PASS unless it ran on this tree. QUALIFY_ONLY=REGEXP reruns just the matching steps and appends.
set -uo pipefail
O=$(realpath -m "$1"); S=$(realpath -m "${2:-$1/scratch}"); mkdir -p "$O" "$S"
ROOT=$(pwd); T1=testing-harnesses/z4/t1; C1=testing-harnesses/z4/c1
HEAD_SHA=$(git rev-parse HEAD); TREE=$(git rev-parse 'HEAD^{tree}'); DIRTY=$(git status --porcelain | wc -l)
[ -s "$O/summary.tsv" ] && [ -n "${QUALIFY_ONLY:-}" ] || printf 'step\tstatus\trc\tseconds\tnote\n' > "$O/summary.tsv"
step() { local name=$1; shift; [[ $name =~ ${QUALIFY_ONLY:-.} ]] || return 0; local t=$(date +%s); "$@" > "$O/$name.log" 2>&1; local rc=$?
  local st=PASS; [ $rc -ne 0 ] && st=FAIL
  printf '%s\t%s\t%s\t%s\t\n' "$name" "$st" "$rc" "$(( $(date +%s) - t ))" | tee -a "$O/summary.tsv"; }
skip() { [[ $1 =~ ${QUALIFY_ONLY:-.} ]] || return 0; printf '%s\tSKIP\t-\t0\t%s\n' "$1" "$2" | tee -a "$O/summary.tsv"; }
export VALIDATE_LOG_DIR="$S/validate"; mkdir -p "$VALIDATE_LOG_DIR"
(cd testing-harnesses && go build -o "$S/t1run" ./harness/t1run) || exit 2
# the real-model test has its own steps below: it must not ride along in the timed suites
t1() { local mode=$1; shift; env -u ZEROS3_C1_MODELS "$S/t1run" -mode "$mode" -fresh -v -cache "$S/t1cache" -evidence "$O/t1-$mode.json" "$@"; }
authskip='^(TestAuthority|FuzzAuth|FuzzXDelta|TestXDelta)'

git rev-parse -q --verify v0.1.1 >/dev/null || git fetch -q origin refs/tags/v0.1.1:refs/tags/v0.1.1  # five_generations builds the v0.1.1 tag
step gofmt test -z "$(gofmt -l . testing-harnesses)"
step vet go vet ./...
step vet_harness bash -c 'cd testing-harnesses && go vet ./...'
step deps_stdlib_only bash -c '! grep -Eq "^\s*require|^\s*replace" go.mod && test -z "$(go list -deps -f "{{if not .Standard}}{{.ImportPath}}{{end}}" . | grep -v "^zeros3$")" && test ! -e vendor'
step go_mod_unchanged git diff --exit-code origin/main -- go.mod go.sum
step build_reproducible sh scripts/reproducible_build.sh
step toc_source bash -c 'cd testing-harnesses && go run ./harness/toc_check -file ../zeros3.go'
step toc_tests bash -c 'cd testing-harnesses && go run ./harness/toc_check -file ../zeros3_test.go'
step g1_ledger_current bash -c "python3 -I $C1/g1_ledger.py $S/g1_ledger.json && diff -q $S/g1_ledger.json $C1/results/g1_ledger.json"
step identity_check bash scripts/check_identity.sh origin/main
step root_normal t1 normal
step root_race t1 race
step authority_hook t1 authority -skip "$authskip"
step authority_native t1 normal -run "$authskip"
for f in FuzzXDeltaEnvelope FuzzAuthRecord FuzzAuthWindowRecord; do step fuzz_$f go test -run '^$' -fuzz "^$f\$" -fuzztime 40s .; done
step persistence_dense env ZEROS3_PL_CUTS=300 go test -count=1 -run TestAuthorityPersistenceLossModel -timeout 120m .
step five_generations env ZEROS3_C1_MATRIX=1 go test -count=1 -run TestAuthorityFiveGenerationMatrix -timeout 60m .
step prodshadow go -C testing-harnesses/z3 test ./prodshadow ./numcore ./qcore -count=1
step scale_1m bash -c "set -o pipefail; TINY=900000 $C1/run_scale.sh $S/scale 100000 | tee $O/scale.txt"
step large_3x256mib bash -c "set -o pipefail; $C1/run_large.sh $S/large256 268435456 | tee $O/large256.txt"
step large_3x1gib bash -c "set -o pipefail; $C1/run_large.sh $S/large | tee $O/large.txt"
step large_3x2gib bash -c "set -o pipefail; $C1/run_large.sh $S/large2g 2147483648 | tee $O/large2g.txt"
if [ -n "${ZEROS3_C1_MODELS:-}" ]; then
  step real_models env ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" go test -count=1 -run TestAuthorityZN7RealModels -v -timeout 90m .
  for w in 1 2 4; do step zn7_decode_w$w env ZEROS3_ZN7_WORKERS=$w ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" ZEROS3_C1_DECODE_BENCH=1 go test -count=1 -run TestAuthorityZN7DecodeScaling -v -timeout 60m .; done
else
  skip real_models "ZEROS3_C1_MODELS not set (pinned model files not present)"
  skip zn7_decode "ZEROS3_C1_MODELS not set"
fi
python3 - "$O" "$HEAD_SHA" "$TREE" "$DIRTY" <<'E'
import csv, json, sys
o, head, tree, dirty = sys.argv[1:5]
rows = list(csv.DictReader(open(o + "/summary.tsv"), delimiter="\t"))
last, hist = {}, {}
for r in rows:
    if r["step"] in last:
        hist.setdefault(r["step"], []).append(last[r["step"]])
    last[r["step"]] = r
steps = list(last.values())
json.dump({"head": head, "tree": tree, "dirty_files": int(dirty), "steps": steps,
           "failed": [r["step"] for r in steps if r["status"] == "FAIL"], "skipped": [r["step"] for r in steps if r["status"] == "SKIP"],
           "earlier_attempts": hist}, open(o + "/summary.json", "w"), indent=1)
print("---- summary"); print(open(o + "/summary.tsv").read())
E
