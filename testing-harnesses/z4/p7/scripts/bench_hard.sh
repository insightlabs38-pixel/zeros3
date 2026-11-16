set -e
O=/root/p7/out/hard; mkdir -p $O; V=/home/user/zeros3/testing-harnesses/z4/b6/verify_fresh.sh; TIMEFORMAT='%R real %U user %S sys'
{ time $V bin/z4b6 parquet tmp/vf $(find pq/brotli -name db.parquet | sort) ; } > $O/fresh_parquet_brotli.txt 2>&1
for i in 1 2; do for b in z3sst-bin z3sst-new; do
  rm -rf tmp/perfstore; { time bin/$b perf -corpus sst/corpus-lz4hc -prod rocksdb -store tmp/perfstore ; } > $O/sst_perf_lz4hc_${b}_$i.txt 2>&1
done; done
for b in z3sst-bin z3sst-new; do rm -rf tmp/perfstore; { time bin/$b perf -corpus sst/corpus-lz4 -prod rocksdb -store tmp/perfstore ; } > $O/sst_perf_lz4_${b}.txt 2>&1; done
rm -rf tmp/perfstore
{ time $V bin/z4b6 sst tmp/vf $(find sst/corpus-lz4hc/rocksdb/chain -name '*.sst' | sort) ; } > $O/fresh_sst_lz4hc.txt 2>&1
{ time $V bin/z4b6 sst tmp/vf $(find sst/corpus-lz4/rocksdb/chain sst/corpus-lz4/rocksdb/variants -name '*.sst' | sort) ; } > $O/fresh_sst_lz4.txt 2>&1
{ time $V bin/z4b6 parquet tmp/vf $(find pq/lz4 -name db.parquet | sort) ; } > $O/fresh_parquet_lz4.txt 2>&1
echo done > $O/done
