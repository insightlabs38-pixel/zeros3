#!/bin/bash
# B10 reproduction: corpus (published files + make_inputs.py), CDC fixtures, materialized lanes, reads,
# concurrency and the decoded-window cache. usage: run.sh WORK   (root for drop_caches; ~4 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); B=$ROOT/testing-harnesses/next/b10; R=$B/results; mkdir -p "$R"
C=$W/corpus; mkdir -p "$C"/{gems,maven,oci,ts,ctlgems,src}
get() { [ -f "$C/$1" ] || curl -sSfL -o "$C/$1" "$2"; }
for v in 1.66.0 1.66.1 1.67.0 1.68.0 1.69.0; do get gems/rubocop-$v.gem https://rubygems.org/downloads/rubocop-$v.gem; done
for v in 7.2.0 7.2.1 7.2.2 8.0.0; do get gems/activerecord-$v.gem https://rubygems.org/downloads/activerecord-$v.gem; done
for v in 3.9.6 3.9.7 3.9.8 3.9.9; do for x in tar.gz zip; do
  get maven/apache-maven-$v-bin.$x https://archive.apache.org/dist/maven/maven-3/$v/binaries/apache-maven-$v-bin.$x; done; done
for v in 5.6.2 5.6.3 5.7.2; do get ts/typescript-$v.tgz https://registry.npmjs.org/typescript/-/typescript-$v.tgz; done
for g in rake-13.2.1 rack-3.1.8 thor-1.3.2 nokogiri-1.16.7 puma-6.4.3; do get ctlgems/$g.gem https://rubygems.org/downloads/$g.gem; done
[ -f "$C/oci/python-3.12.8-slim.layer2.tar.gz" ] || FETCH=$C/oci LAYER=2 python3 -I "$B/fetch_oci.py" library/python 3.12.6-slim 3.12.7-slim 3.12.8-slim
[ -d "$C/src/owid" ] || git clone -q --filter=blob:none --no-checkout https://github.com/owid/co2-data "$C/src/owid"
python3 -I "$B/make_inputs.py" "$C"     # pyarrow 18.1.0, rocksdict 0.3.29
(cd "$C" && sha256sum -c "$B/corpus.sha256" --quiet)
cd "$ROOT"
T="-tags b10bench,b6bench,b5locality,b1rangeexperiment -count=1 -timeout 8h"
export ZEROS3_B6_FX=$W/fx ZEROS3_B6_CORPUS=$C
go test $T -run '^TestB10Build$' .
ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_LANES=cdc,b9-archive,b9x-archive,b10-archive,b10-all-archive,b10-nodelta-archive,b9x-mixed,b10-mixed,b9x-hot,b10-hot \
  go test $T -run '^TestB10Lanes$' .
F=gems-rubocop,maven,pq-snappy,pq-zstd,sst-snappy
ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_1M=1 ZEROS3_B6_FAMILIES=$F \
  ZEROS3_B6_LANES=cdc,b9x-archive,b10-archive,b10-mixed go test $T -run '^TestB10Reads$' .
ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=gems-rubocop,pq-snappy,sst-snappy ZEROS3_B6_LANES=b9x-archive,b10-archive go test $T -run '^TestB10Concurrent$' .
ZEROS3_B6_OUT=$R/cache.jsonl ZEROS3_B6_FAMILIES=gems-rubocop,pq-snappy,sst-snappy go test $T -run '^TestB10Cache$' .
gzip -9f "$R/reads.jsonl"
python3 "$B/tables.py" "$R"/lanes.jsonl "$R"/reads.jsonl.gz "$R"/concurrent.jsonl "$R"/cache.jsonl > "$R/tables.md"
