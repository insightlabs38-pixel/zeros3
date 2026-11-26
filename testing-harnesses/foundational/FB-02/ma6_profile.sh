#!/bin/sh
# FB-02 M-A6: restore time, peak RSS and index heap, fresh against session.
# Usage: sh testing-harnesses/foundational/FB-02/ma6_profile.sh OUTDIR
# One configuration per process, so each VmHWM is that run's own.
set -eu
out=${1:?usage: ma6_profile.sh OUTDIR}
mkdir -p "$out"
bin="$out/zeros3.test"
go test -c -o "$bin" .
run() { FB02_PROFILE="$1" "$bin" -test.count=1 -test.v -test.run '^TestFB02SnapshotProfile$' | grep '^FB02PROFILE' >> "$out/ma6_profile.jsonl"; }
: > "$out/ma6_profile.jsonl"
for n in 1000 10000 100000; do run "mode=session n=$n case=heap"; done
for n in 1000 10000; do
  for r in 1 2 3; do run "mode=session n=$n case=ok"; run "mode=fresh n=$n case=ok"; done
  for c in deleted corrupt; do run "mode=session n=$n case=$c"; run "mode=fresh n=$n case=$c"; done
done
for m in session fresh; do run "mode=$m n=100000 case=describe"; done
"$bin" -test.run '^$' -test.bench '^BenchmarkFB02Snapshot' -test.benchtime 20x -test.count 5 > "$out/ma6_bench.txt"
rm -f "$bin"
