# Qualification steps (sourced by qualify.sh). Every name in MANDATORY must PASS on the final source.
MANDATORY=(gofmt vet vet_harness t1run_negative_controls qualify_negative_controls deps_stdlib_only go_mod_unchanged build_reproducible toc_source toc_tests
  g1_ledger_current identity_check root_normal root_race authority_hook authority_native
  fuzz_FuzzXDeltaEnvelope fuzz_FuzzAuthRecord fuzz_FuzzAuthWindowRecord persistence_dense five_generations prodshadow
  scale_1m large_3x256mib large_3x1gib large_3x2gib real_models zn7_decode_w1 zn7_decode_w2 zn7_decode_w4
  z3_producers g1_coverage_executed economics_pqchain conventional_root conventional_race t1_equivalence)

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
# the research producers against their oracles: IQ3_S/K-quant vectors, 562 streaming-Brotli scenarios for libbrotli 1.1.0 and 1.2.0, real streams
step z3_producers go -C testing-harnesses/z3 test -count=1 -timeout 60m ./brotliz3 ./qcore ./numcore ./parquetz3 ./thriftcompactz3
# the exemptions of the retire-everything run are explained by invariants, each declared by an authority-mode test that passed in authority_native
step g1_coverage_executed bash -c "python3 -I $C1/g1_ledger.py $S/g1_final.json --evidence $O/t1-authority_native.json && diff -q $S/g1_final.json $C1/results/g1_ledger.json"
# the independent, conventional runs and the comparison with the optimized runner (outcomes per test)
step conventional_root bash -c "env -u ZEROS3_C1_MODELS -u ZEROS3_C1_PQCHAIN go test -count=1 -json -timeout 120m ./... > $O/conventional_root.json"
step conventional_race bash -c "env -u ZEROS3_C1_MODELS -u ZEROS3_C1_PQCHAIN go test -race -count=1 -json -timeout 240m ./... > $O/conventional_race.json"
step t1_equivalence python3 -I $T1/equivalence.py $O/conventional_root.json $O/t1-root_normal.json $O/conventional_race.json $O/t1-root_race.json
step scale_1m bash -c "set -o pipefail; $S/t1run -need-disk $S=13500 && TINY=900000 $C1/run_scale.sh $S/scale 100000 | tee $O/scale.txt"
step large_3x256mib bash -c "set -o pipefail; $S/t1run -need-disk $S=3000 && $C1/run_large.sh $S/large256 268435456 | tee $O/large256.txt"
step large_3x1gib bash -c "set -o pipefail; $S/t1run -need-disk $S=8000 && $C1/run_large.sh $S/large | tee $O/large.txt"
step large_3x2gib bash -c "set -o pipefail; $S/t1run -need-disk $S=14000 && $C1/run_large.sh $S/large2g 2147483648 | tee $O/large2g.txt"
if [ -n "${ZEROS3_C1_PQCHAIN:-}" ]; then
  step economics_pqchain env ZEROS3_C1_PQCHAIN="$ZEROS3_C1_PQCHAIN" ZEROS3_C1_PQCHAIN_OUT="$O/economics_pqchain.json" go test -count=1 -run TestAuthorityHardCorpusEconomics -v -timeout 120m .
else
  skip economics_pqchain "ZEROS3_C1_PQCHAIN not set (generate with testing-harnesses/z4/c1/gen_parquet_chain.py)"
fi
if [ -n "${ZEROS3_C1_MODELS:-}" ]; then
  step real_models env ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" go test -count=1 -run TestAuthorityZN7RealModels -v -timeout 90m .
  for w in 1 2 4; do step zn7_decode_w$w env ZEROS3_ZN7_WORKERS=$w ZEROS3_C1_MODELS="$ZEROS3_C1_MODELS" ZEROS3_C1_DECODE_BENCH=1 go test -count=1 -run TestAuthorityZN7DecodeScaling -v -timeout 60m .; done
else
  skip real_models "ZEROS3_C1_MODELS not set (pinned model files not present)"
  skip zn7_decode "ZEROS3_C1_MODELS not set"
fi
}
