#!/bin/sh
# Re-runs every measurement quoted by Z4_B6_HARD_CODECS.md against already generated corpora and writes
# results/*.txt. set -e: any exactness failure (inexact round trip, mismatched sha256) aborts with a
# non-zero status. Corpus roots are arguments so the script has no hidden state.
# usage: run_evidence.sh BIN_DIR(z3sst-bin,z4b6,z4b6rank,z3parquet) SST_ROOT PQ_ROOT SQ_ROOT WIMX_ROOT W2_ROOT FRAMES_DIR BLOCKS_DIR SCRATCH
set -eu
B=$1; SST=$2; PQ=$3; SQ=$4; WIMX=$5; W2=$6; FR=$7; BL=$8; SCR=$9
R="$(cd "$(dirname "$0")" && pwd)/results"; mkdir -p "$R" "$SCR"
V="$(dirname "$0")/verify_fresh.sh"
par() { # run commands (given as strings) in parallel; fail if any fails
  pids=""; for c in "$@"; do sh -c "$c" & pids="$pids $!"; done
  for p in $pids; do wait "$p"; done
}
if [ -z "${RESUME:-}" ]; then
par \
 "$B/z3sst-bin rt $SST/corpus-lz4/rocksdb/chain $SST/corpus-lz4/rocksdb/variants > $R/sst_rt_lz4.txt 2>&1" \
 "$B/z3sst-bin rt $SST/corpus-lz4hc/rocksdb/chain > $R/sst_rt_lz4hc.txt 2>&1" \
 "$B/z3sst-bin econ -corpus $SST/corpus-lz4 -prod rocksdb -reps cdc,blocks,ukv -creps cdc,blocks,ukv -json $R/sst_econ_lz4 > $R/sst_econ_lz4.txt 2>/dev/null"
par \
 "$B/z3sst-bin econ -corpus $SST/corpus-snappy -prod rocksdb -reps cdc,blocks,ukv -creps cdc,blocks,ukv -json $R/sst_econ_snappy > $R/sst_econ_snappy.txt 2>/dev/null" \
 "$B/z3sst-bin econ -corpus $SST/corpus-lz4hc -prod rocksdb -variants=false -creps cdc,blocks,ukv -json $R/sst_econ_lz4hc > $R/sst_econ_lz4hc.txt 2>/dev/null"
fi # RESUME=1 skips the long round-trip/economics blocks above when their results/ files exist
"$B/z3sst-bin" perf -corpus $SST/corpus-lz4 -prod rocksdb -store $SCR/perfstore > $R/sst_perf_lz4.txt 2>&1
"$B/z3sst-bin" reads -corpus $SST/corpus-lz4 -prod rocksdb > $R/sst_reads_lz4.txt 2>&1 || { cat $R/sst_reads_lz4.txt >&2; exit 1; }
for c in lz4 snappy brotli; do
  Z3_STRUCT_CACHE=$PQ/$c "$B/z3parquet" chain -fam pyarrow-chain -reps cdc,dpages,logical > $R/parquet_chain_$c.txt 2>&1
done
Z3_STRUCT_CACHE=$PQ/lz4 "$B/z3parquet" audit -fam pyarrow-chain > $R/parquet_audit_lz4.txt 2>&1
Z3_STRUCT_CACHE=$PQ/lz4 "$B/z3parquet" rt -fam pyarrow-chain > $R/parquet_rt_lz4.txt 2>&1
"$B/z4b6" sqfs $SQ three_lz4 typescript_lz4 three_lz4hc typescript_lz4hc > $R/sqfs_econ.txt 2>&1
"$B/z4b6" lz4frames $FR/*.lz4 > $R/lz4_frames.txt 2>&1
"$B/z4b6" lz4blocks $BL/lz4blocks $BL/pierrec > $R/lz4_matrix.txt 2>&1
"$B/z4b6rank" $SQ json > $R/rank_squashfs.json 2>/dev/null
"$B/z4b6rank" $WIMX json win7pe > $R/rank_wim_win7pe.json 2>/dev/null
"$B/z4b6rank" $W2 json > $R/rank_woff2.json 2>/dev/null
python3 "$(dirname "$0")/wimtable.py" $(ls $WIMX/../wim/w7/x_*/BOOT.WIM) > $R/wim_native_resources.txt
python3 "$(dirname "$0")/brotli_probe.py" $W2/../../b6cache > $R/brotli_probe.txt
$V $B/z4b6 sst $SCR $(find $SST/corpus-lz4/rocksdb/chain $SST/corpus-lz4/rocksdb/variants -name '*.sst' | sort) > $R/fresh_sst_lz4.txt
$V $B/z4b6 sst $SCR $(find $SST/corpus-lz4hc/rocksdb/chain -name '*.sst' | sort) > $R/fresh_sst_lz4hc.txt
$V $B/z4b6 parquet $SCR $(find $PQ/lz4 -name db.parquet | sort) > $R/fresh_parquet_lz4.txt
$V $B/z4b6 sqfs $SCR $(find $SQ/*_lz4 $SQ/*_lz4hc -name image.sqsh | sort) > $R/fresh_sqfs_lz4.txt
echo "evidence complete"
