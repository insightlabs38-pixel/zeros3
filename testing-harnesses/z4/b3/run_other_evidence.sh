#!/bin/bash
# Git/tar/OCI and columnar evidence. usage: run_other_evidence.sh BIN REPO WORKDIR
set -e
BIN=$1; REPO=$2; W=$3; D=$(dirname "$0"); M="python3 $D/measure.py"; V="python3 $D/verify_replay.py"
mkdir -p $W; cd $W
# ---- Git/tar/OCI: five pinned revisions of this repository, six packagings each ----
python3 $D/make_git_corpus.py $REPO git v0.1.0 v0.1.1 4e470b0 bd05621 44d7d58 > git_corpus.log
ORD="r0_ r1_ r2_ r3_ r4_"; T=""; P=""; ALL=""; FILES=""
for t in $ORD; do for k in tar gz6.tar.gz gz9.tar.gz layer.tar.gz; do f=$(ls git/${t}*.$k); T="$T tar:$f"; ALL="$ALL tar:$f"; FILES="$FILES $f"; done
  for k in nodelta.pack delta.pack; do f=$(ls git/${t}*.$k); P="$P pack:$f"; ALL="$ALL pack:$f"; FILES="$FILES $f"; done
  for f in git/${t}*.loose.*; do P="$P loose:$f"; ALL="$ALL loose:$f"; FILES="$FILES $f"; done; done
rm -rf g_all g_T g_P
$M $BIN files -store g_all $ALL > g_all.log 2>&1
$M $BIN files -store g_T $T > g_T.log 2>&1
$M $BIN files -store g_P $P > g_P.log 2>&1
$BIN cdc $FILES > g_cdc.log 2>&1
rm -rf g_out; $V $BIN g_all g_out $FILES > g_verify.log 2>&1 || true
# ---- columnar: three profiles, eight files each ----
python3 $D/make_columnar_corpus.py col > col_corpus.log 2>&1
for prof in req opt hard; do
  if [ $prof = hard ]; then ARW="arrow:col/$prof.arrow64k.arrow arrow:col/$prof.arrow50k.arrow"; else ARW="arrow:col/$prof.arrow64k.arrow arrow:col/$prof.arrow50k.arrow arrow:col/$prof.arrowrev.arrow"; fi
  PQ="parquet:col/$prof.pq_plain_none_rg100k_v1.parquet parquet:col/$prof.pq_plain_none_rg64k_v2.parquet pqx:col/$prof.pq_default_snappy_dict.parquet pqx:col/$prof.pq_zstd_dict.parquet pqx:col/$prof.pq_plain_snappy.parquet"
  rm -rf c_$prof c_A_$prof c_P_$prof c_out_$prof
  $M $BIN files -store c_$prof $ARW $PQ > c_$prof.log 2>&1
  $BIN files -store c_A_$prof $ARW > c_A_$prof.log 2>&1
  $BIN files -store c_P_$prof $PQ > c_P_$prof.log 2>&1
  $BIN cdc $(echo $ARW $PQ | tr ' ' '\n' | sed 's/^[a-z]*://') > c_cdc_$prof.log 2>&1
  $V $BIN c_$prof c_out_$prof $(echo $ARW $PQ | tr ' ' '\n' | sed 's/^[a-z]*://') > c_verify_$prof.log 2>&1 || true
done
$BIN pq-oracle col/req.pq_default_snappy_dict.parquet col/req.pq_zstd_dict.parquet col/req.pq_plain_snappy.parquet col/req.orc_zlib.orc col/req.orc_none.orc > pq_oracle.log 2>&1
$BIN pq-values -store c_req col/req.pq_default_snappy_dict.parquet col/req.pq_zstd_dict.parquet col/req.pq_plain_snappy.parquet > pq_values.log 2>&1
for f in col/req.pq_default_snappy_dict.parquet col/req.arrow64k.arrow col/req.pq_zstd_dict.parquet; do $BIN ranges -store c_req -orig $f; done > col_ranges.log 2>&1
for f in git/r2_*.layer.tar.gz git/r2_*.nodelta.pack git/r2_*.delta.pack; do $BIN ranges -store g_all -orig $f; done > git_ranges.log 2>&1
echo finished > other_evidence.done
