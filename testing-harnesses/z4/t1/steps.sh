# Qualification steps (sourced by qualify.sh). Every name in MANDATORY must PASS on the final source.
MANDATORY=(gofmt vet vet_harness t1run_negative_controls qualify_negative_controls deps_stdlib_only go_mod_unchanged build_reproducible toc_source toc_tests
  g1_ledger_current identity_check root_normal root_race authority_hook authority_native
  fuzz_FuzzXDeltaEnvelope fuzz_FuzzAuthRecord fuzz_FuzzAuthWindowRecord persistence_dense five_generations prodshadow
  scale_1m large_3x256mib large_3x1gib large_3x2gib real_models zn7_decode_w1 zn7_decode_w2 zn7_decode_w4)

# the real-model test has its own steps below: it must not ride along in the timed suites
t1() { local mode=$1; shift; env -u ZEROS3_C1_MODELS "$S/t1run" -mode "$mode" -fresh -v -allow-skip "$allowskip" -cache "$S/t1cache" -evidence "$O/t1-$CURSTEP.json" "$@"; }
# tests that skip unless an environment switch selects them; each one that matters has its own mandatory step below
allowskip='^(TestAuthority(FiveGenerationMatrix|Large(Check|Fixture|Profile)|ScaleFixture|ZN7DecodeScaling|ZN7RealModels)|TestB2(Economics|RepBreakdown)|TestLocalityBench_(CompactOverhead|RunWindows)|TestLocatorScale|TestP1B_PopulatedStoreThroughput|TestP1C_(RecoveryLatency|StoreLayerPutThroughput)|TestP3_(ChurnFixture|HistoryFixture|MaintHelper)|TestP4(Interference|Probe)|TestRep(CodecEconomics|CodecLargeStreaming|GitRealReexports|Measure|OCIRealRegistryComposition))$'
authskip='^(TestAuthority|FuzzAuth|FuzzXDelta|TestXDelta)'

run_steps() {
git rev-parse -q --verify v0.1.1 >/dev/null || git fetch -q origin refs/tags/v0.1.1:refs/tags/v0.1.1  # five_generations builds the v0.1.1 tag
step gofmt test -z "$(gofmt -l . testing-harnesses)"
step vet go vet ./...
step vet_harness bash -c 'cd testing-harnesses && go vet ./...'
step t1run_negative_controls bash -c 'cd testing-harnesses && go test -count=1 ./harness/t1run'
step qualify_negative_controls bash testing-harnesses/z4/t1/qualify_test.sh
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
step scale_1m bash -c "set -o pipefail; $S/t1run -need-disk $S=26000 && TINY=900000 $C1/run_scale.sh $S/scale 100000 | tee $O/scale.txt"
step large_3x256mib bash -c "set -o pipefail; $S/t1run -need-disk $S=3000 && $C1/run_large.sh $S/large256 268435456 | tee $O/large256.txt"
step large_3x1gib bash -c "set -o pipefail; $S/t1run -need-disk $S=8000 && $C1/run_large.sh $S/large | tee $O/large.txt"
step large_3x2gib bash -c "set -o pipefail; $S/t1run -need-disk $S=14000 && $C1/run_large.sh $S/large2g 2147483648 | tee $O/large2g.txt"
if [ -n "${ZEROS3_C1_MODELS:-}" ]; then
  step real_models env ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" go test -count=1 -run TestAuthorityZN7RealModels -v -timeout 90m .
  for w in 1 2 4; do step zn7_decode_w$w env ZEROS3_ZN7_WORKERS=$w ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" ZEROS3_C1_DECODE_BENCH=1 go test -count=1 -run TestAuthorityZN7DecodeScaling -v -timeout 60m .; done
else
  skip real_models "ZEROS3_C1_MODELS not set (pinned model files not present)"
  skip zn7_decode "ZEROS3_C1_MODELS not set"
fi
}
