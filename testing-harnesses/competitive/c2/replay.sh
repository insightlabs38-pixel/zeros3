#!/usr/bin/env bash
# Replay C2 with an owned work directory and the C1 Python dependencies.
set -euo pipefail
work=$(realpath -m "${1:?usage: replay.sh WORK C1_CORPUS [core|external|requests|tests]}")
corpus=$(realpath "${2:?pass a verified C1 corpus directory}")
lane=${3:-core}
py=${C2_PYTHON:-python3}
go_compiler=${C2_GO:-go}
cd "$(dirname "$0")"
c2=$PWD
repo=$(git rev-parse --show-toplevel)
mkdir -p "$work"/{source,bin,results,cache}
export GOMAXPROCS=2 GOCACHE="$work/cache"
git show dcefbcb5c2539c74a0d33bfc0d6e4a380f671743:zeros3.go > "$work/source/zeros3.go"
git show dcefbcb5c2539c74a0d33bfc0d6e4a380f671743:go.mod > "$work/source/go.mod"
(cd "$work/source" && "$go_compiler" build -o "$work/bin/b10" .)
(cd "$repo" && "$go_compiler" build -o "$work/bin/c2" .)
run() {
  binary=$1
  data=$2
  label=$3
  shift 3
  "$py" "$c2/frontier.py" "$work/bin/$binary" "$data" "$work/$label" "$work/results/$label.jsonl" "$@"
}
case "$lane" in
  core)
    "$py" ../c1/corpus_stats.py "$corpus" ../c1/results/{corpus,external-corpus,codec-corpus}.json > "$work/c1-fixtures.json"
    families=(lodash parquet sst tensor combined)
    run b10 "$corpus" b10-primary --families "${families[@]}" --dry-plan --reads --reoptimize
    run b10 "$corpus" b10-finalized --families "${families[@]}" --full --reads --reoptimize
    run c2 "$corpus" c2-finalized --families "${families[@]}" coreutils entropy --full --lanes c1 --reads --reoptimize --memo
    run c2 "$corpus" c2-budget-final --families combined --full --lanes budget
    for repeat in 1 2; do
      run b10 "$corpus" "b10-parquet-$repeat" --families parquet --full --lanes c1 --reads
      run c2 "$corpus" "c2-parquet-$repeat" --families parquet --full --lanes c1 --reads
    done
    "$py" "$c2/summary.py" "$work/results" > "$work/results/summary.json"
    ;;
  external)
    "$py" inputs.py "$work/external" "$work/downloads" "$work/external-inputs.json"
    for family in coreutils-files go-files rubocop typescript django urllib3; do
      run b10 "$work/external" "b10-$family" --families "$family" --full --lanes c1
      run c2 "$work/external" "c2-$family" --families "$family" --full --lanes c1
    done
    ;;
  requests)
    for trial in b10:1 c2:1 c2:2 b10:2 b10:3 c2:3; do
      build=${trial%:*}
      repetition=${trial#*:}
      suffix="-$repetition"
      if [ "$repetition" = 1 ]; then suffix=; fi
      mkdir -p "$work/s3-$build"
      if [ ! -e "$work/s3-$build/zeros3" ]; then
        ln -s "$work/bin/$build" "$work/s3-$build/zeros3"
      fi
      "$py" ../c1/local_s3.py "$work/s3-$build" "$work/results/$build-default-s3$suffix.jsonl" \
        --lane requests --systems zeros3 --repetitions 1
    done
    run b10 "$corpus" b10-concurrent --families parquet --full --lanes c1 --concurrent-reads
    run c2 "$corpus" c2-concurrent --families parquet --full --lanes c1 --concurrent-reads
    ;;
  tests)
    cd "$repo"
    "$go_compiler" test -race -run 'TestC2|TestB10SpliceConcurrentReads|TestB10SpliceWindowCache' -count=1 -timeout 12m .
    "$go_compiler" test -count=1 -timeout 40m .
    ;;
  *) printf 'Unknown lane %s\n' "$lane" >&2; exit 2 ;;
esac
