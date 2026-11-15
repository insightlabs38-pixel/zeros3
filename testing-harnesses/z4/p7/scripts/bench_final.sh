#!/bin/bash
# usage: bench_final.sh ROOT   (ROOT holds bin/ corpus/ sst/ pq/ tmp/ as described in README.md)
# Re-runs the headline measurements with binaries built from the final tree, on an otherwise idle machine:
# fresh-process exact reconstruction (independent sha256sum), SST analysis/replay timing, parallel
# Parquet-Brotli replay, interleaved B5 vs P7 on Pythia-14M. Output: ROOT/out/final/.
set -euo pipefail
R=$1; HERE=$(cd "$(dirname "$0")" && pwd); Z=$HERE/../../../z3; O=$R/out/final; mkdir -p $O; cd $R
V=$HERE/../../b6/verify_fresh.sh; TIMEFORMAT='%R real %U user %S sys'
for c in z4p7 z4b6 z3parquet; do go -C $Z build -o $R/bin/$c ./cmd/$c; done; go -C $Z build -o $R/bin/z3sst-new ./cmd/z3sst
git -C $Z rev-parse HEAD > $O/revision.txt
{ time $V bin/z4b6 parquet tmp/vf $(find pq/brotli -name db.parquet | sort) ; } > $O/fresh_parquet_brotli.txt 2>&1
{ time $V bin/z4b6 parquet tmp/vf $(find pq/lz4 -name db.parquet | sort) ; } > $O/fresh_parquet_lz4.txt 2>&1
{ time $V bin/z4b6 sst tmp/vf $(find sst/corpus-lz4hc/rocksdb/chain -name '*.sst' | sort) ; } > $O/fresh_sst_lz4hc.txt 2>&1
{ time $V bin/z4b6 sst tmp/vf $(find sst/corpus-lz4/rocksdb/chain sst/corpus-lz4/rocksdb/variants -name '*.sst' | sort) ; } > $O/fresh_sst_lz4.txt 2>&1
for c in lz4hc lz4; do rm -rf tmp/perfstore; { time bin/z3sst-new perf -corpus sst/corpus-$c -prod rocksdb -store tmp/perfstore ; } > $O/sst_perf_${c}.txt 2>&1; done; rm -rf tmp/perfstore
# Parquet-Brotli: analysis once, then replay with 1 / 2 / 4 page workers (two repetitions each)
F=pq/brotli/parquet/pyarrow-chain/c0/db.parquet; rm -rf tmp/pqstore
{ time bin/z3parquet perf -phase encode -file $F -store tmp/pqstore ; } > $O/pq_brotli_encode.txt 2>&1
for i in 1 2; do for w in 1 2 4; do { time bin/z3parquet perf -phase decode -workers $w -file $F -store tmp/pqstore ; } > $O/pq_brotli_decode_w${w}_$i.txt 2>&1; done; done; rm -rf tmp/pqstore
rm -f $O/pythia14m.jsonl; bash $HERE/bench_model.sh $R/bin/z4b5-main $R/bin/z4p7 $R/corpus $O pythia14m prev prev 3
echo done > $O/done
