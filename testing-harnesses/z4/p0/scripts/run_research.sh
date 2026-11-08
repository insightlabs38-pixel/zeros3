#!/bin/bash
# Research engines B1,B3,B4,B5,B6 under the same measurement harness (workstreams F-H). Sequential, never concurrent.
# usage: run_research.sh ENGINE CORPUS_DIR RBIN_DIR   ENGINE = b1|b3|b4|b5|b6
set -uo pipefail
E=${1:?engine}; C=$(realpath "$2"); B=$(realpath "$3")
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../../../.." && pwd)
RES=$ROOT/testing-harnesses/z4/p0/results; W=${P0_WORK:-/tmp/claude-0/p0work}/r_$E; PR=${P0_WORK:-/tmp/claude-0/p0work}/prof
mkdir -p $W $PR $RES/prof; OUT=$RES/research_$E.jsonl
M() { python3 $HERE/rmeasure.py --out $OUT "$@"; }          # M --label L [--reps N] [--prof DIR] [--cache cold] -- cmd...
V=$ROOT/testing-harnesses/z4/b3/verify_replay.py
sumprof() { for d in $PR/$1-r*; do [ -d $d ] && $HERE/profsum.sh $d $RES/prof/$(basename $d).txt; done; }
case $E in
b3)
  S=$C/models/smol5; fwd="$S/model.safetensors $S/f16.gguf $S/q8_0.gguf $S/model_fp16.onnx $S/model.onnx"
  M --label b3_cdc_baseline_smol5 --reps 3 --stdout $W/cdc.out -- $B/z4b3 cdc $fwd
  for mode in identity semantic; do
    M --label b3_ingest_smol5_$mode --reps 1 --pre "rm -rf $W/s_$mode" --stdout $W/in_$mode.out -- $B/z4b3 ingest -store $W/s_$mode -mode $mode $fwd
  done
  M --label b3_ingest_smol5_full --reps 3 --pre "rm -rf $W/s_full" --stdout $W/in_full.out -- $B/z4b3 ingest -store $W/s_full -mode full $fwd
  M --label b3_ingest_smol5_full_PROF --reps 1 --prof $PR/b3_ingest_smol5_full --pre "rm -rf $W/s_pf" --stdout $W/in_pf.out -- $B/z4b3-prof ingest -store $W/s_pf -mode full $fwd; sumprof b3_ingest_smol5_full
  rm -rf $W/s_pf
  # fresh-process replay + independent hash (verify_replay runs replay in its own process)
  M --label b3_replay_verify_smol5 --reps 1 --stdout $W/vr.out -- python3 -I $V $B/z4b3 $W/s_full $W/out_full $fwd
  M --label b3_replay_PROF --reps 1 --prof $PR/b3_replay --pre "rm -rf $W/out_pf" --cwd / -- $B/z4b3-prof replay -store $W/s_full -out $W/out_pf; sumprof b3_replay
  rm -rf $W/out_full $W/out_pf
  for f in $fwd; do M --label b3_ranges_$(basename $f) --reps 1 --stdout $W/rg.out -- $B/z4b3 ranges -store $W/s_full -orig $f; done
  # small tensor lane and Git/tar/columnar lanes
  P=$C/models/pythia14m; M --label b3_ingest_pythia14m_full --reps 3 --pre "rm -rf $W/p_full" -- $B/z4b3 ingest -store $W/p_full -mode full $P/step1000.safetensors $P/step2000.safetensors $P/step3000.safetensors
  M --label b3_files_tar_x3 --reps 3 --pre "rm -rf $W/f_tar" -- $B/z4b3 files -store $W/f_tar tar:$C/src/coreutils-9.5.tar tar:$C/src/coreutils-9.6.tar tar:$C/src/coreutils-9.7.tar
  M --label b3_files_targz_deflate --reps 1 --pre "rm -rf $W/f_gz" -- $B/z4b3 files -store $W/f_gz tar:$C/gz/coreutils-9.7.l6.tar.gz tar:$C/gz/coreutils-9.7.l9.tar.gz
  M --label b3_files_targz_PROF --reps 1 --prof $PR/b3_targz --pre "rm -rf $W/f_gzp" -- $B/z4b3-prof files -store $W/f_gzp tar:$C/gz/coreutils-9.7.l6.tar.gz; sumprof b3_targz
  M --label b3_files_pack --reps 3 --pre "rm -rf $W/f_pack" -- $B/z4b3 files -store $W/f_pack pack:$C/git/h0.pack pack:$C/git/h1.pack pack:$C/git/h2.pack
  M --label b3_files_parquet --reps 3 --pre "rm -rf $W/f_pq" -- $B/z4b3 files -store $W/f_pq pqx:$C/parquet/zstd/c0.parquet pqx:$C/parquet/zstd/c1.parquet pqx:$C/parquet/zstd/c2.parquet pqx:$C/parquet/zstd/c3.parquet
  M --label b3_files_parquet_PROF --reps 1 --prof $PR/b3_pq --pre "rm -rf $W/f_pqp" -- $B/z4b3-prof files -store $W/f_pqp pqx:$C/parquet/zstd/c0.parquet pqx:$C/parquet/zstd/c1.parquet; sumprof b3_pq
  ;;
b4)
  BC=$C/b4/bc; PD=$C/b4/pdb; Q=$C/q6
  o2="$BC/sqlite-3450100-O2.bc $BC/sqlite-3450200-O2.bc $BC/sqlite-3450300-O2.bc $BC/sqlite-3460000-O2.bc $BC/sqlite-3460100-O2.bc $BC/sqlite-3490100-O2.bc"
  M --label b4_llvm_sqlite_O2x6 --reps 3 --stdout $W/llvm.out -- $B/z4b4 llvm $o2
  M --label b4_llvm_PROF --reps 1 --prof $PR/b4_llvm --stdout $W/llvm_p.out -- $B/z4b4-prof llvm $o2; sumprof b4_llvm
  M --label b4_perf_sqlite_3490100 --reps 3 --stdout $W/perf.out -- $B/z4b4 perf $BC/sqlite-3490100-O2.bc
  M --label b4_perf_PROF --reps 1 --prof $PR/b4_perf --stdout $W/perf_p.out -- $B/z4b4-prof perf $BC/sqlite-3490100-O2.bc; sumprof b4_perf
  M --label b4_pdb_newtonsoftx5 --reps 3 --stdout $W/pdb.out -- $B/z4b4 pdb $PD/12.0.3-netstandard2.0.pdb $PD/13.0.1-netstandard2.0.pdb $PD/13.0.2-netstandard2.0.pdb $PD/13.0.3-netstandard2.0.pdb $PD/13.0.4-netstandard2.0.pdb
  M --label b4_pdb_PROF --reps 1 --prof $PR/b4_pdb -- $B/z4b4-prof pdb $PD/12.0.3-netstandard2.0.pdb $PD/13.0.1-netstandard2.0.pdb $PD/13.0.4-netstandard2.0.pdb; sumprof b4_pdb
  M --label b4_dex_fdroidx3 --reps 3 -- $B/z4b4 dex $Q/fdroid-apk/*.dex
  M --label b4_clr_newtonsoftx5 --reps 3 -- $B/z4b4 clr $Q/clr/*.dll
  rm -rf $W/ls $W/lo; mkdir -p $W/ls
  M --label b4_llvm_store --reps 1 -- $B/z4b4 llvm-store $W/ls $o2
  M --label b4_llvm_replay --reps 1 --cwd / -- $B/z4b4 llvm-replay $W/ls $W/lo
  M --label b4_verify_replay --reps 1 -- python3 -I $ROOT/testing-harnesses/z4/b4/verify_replay.py $W/ls $W/lo $o2
  rm -rf $W/lo
  ;;
b5)
  P=$C/models/pythia70m; S=$C/models/smollm2
  cb() { n=$1; shift; M --label b5_coderbench_$n --reps 3 --stdout $W/cb_$n.out -- $B/z4b5 coderbench -json $W/cb_$n.json -label $n "$@"; }
  cb pythia70m_16000_vs_4000 $P/step16000.safetensors $P/step4000.safetensors
  cb smollm2_instruct_vs_base $S/instruct.safetensors $S/base.safetensors
  cb smollm2_shakespeare_vs_base $S/shakespeare.safetensors $S/base.safetensors
  cb smollm2_unrelated_v1_vs_base $S/smollm_v1.safetensors $S/base.safetensors
  M --label b5_coderbench_PROF --reps 1 --prof $PR/b5_coder --stdout $W/cb_p.out -- $B/z4b5-prof coderbench -json $W/cb_p.json -label prof $S/instruct.safetensors $S/base.safetensors; sumprof b5_coder
  files="$S/base.safetensors $S/sft_only.safetensors $S/instruct.safetensors $S/shakespeare.safetensors $S/smollm_v1.safetensors"
  M --label b5_family_smollm2_pool3 --reps 2 --pre "rm -rf $W/fam" --stdout $W/fam.out -- $B/z4b5 family -policy pool3 -rangeseg 65536 -store $W/fam $files
  M --label b5_family_PROF --reps 1 --prof $PR/b5_family --pre "rm -rf $W/famp" -- $B/z4b5-prof family -policy pool3 -rangeseg 65536 -store $W/famp $files; sumprof b5_family
  rm -rf $W/famp
  M --label b5_replay_smollm2 --reps 2 --pre "rm -rf $W/fam_out" --cwd / -- $B/z4b5 replay -store $W/fam -out $W/fam_out
  M --label b5_verify_replay --reps 1 -- python3 -I $ROOT/testing-harnesses/z4/b5/verify_replay.py $W/fam $W/fam_out
  rm -rf $W/fam_out
  M --label b5_ranges_instruct --reps 1 --stdout $W/rg.out -- $B/z4b5 ranges -store $W/fam -orig $S/instruct.safetensors -n 60
  M --label b5_family_pythia70m_prev --reps 2 --pre "rm -rf $W/fam2" -- $B/z4b5 family -policy prev -rangeseg 65536 -store $W/fam2 $P/step1000.safetensors $P/step4000.safetensors $P/step16000.safetensors
  rm -rf $W/fam $W/fam2
  ;;
b6)
  cd $ROOT/testing-harnesses/z3
  M --label b6_lz4z3_gobench --reps 3 --stdout $W/gobench.out -- go test ./lz4z3 -run '^$' -bench . -benchmem -count 1
  M --label b6_lz4z3_gobench_PROF --reps 1 --stdout $W/gobench_p.out -- go test ./lz4z3 -run '^$' -bench . -benchmem -count 1 -cpuprofile $W/lz4z3.cpu -memprofile $W/lz4z3.mem
  go tool pprof -top -cum -nodecount=30 $W/lz4z3.cpu > $RES/prof/b6_lz4z3_gobench.txt 2>/dev/null
  go tool pprof -top -nodecount=15 -sample_index=alloc_space $W/lz4z3.mem >> $RES/prof/b6_lz4z3_gobench.txt 2>/dev/null
  for s in $C/sst/lz4/c0.sst $C/sst/lz4/c1.sst; do :; done
  F=$ROOT/testing-harnesses/z4/b6/verify_fresh.sh
  M --label b6_sst_lz4_fresh --reps 3 --stdout $W/sst.out -- bash $F $B/z4b6 sst $W/sst_scratch $C/sst/lz4/c0.sst $C/sst/lz4/c1.sst $C/sst/lz4/c2.sst $C/sst/lz4/c3.sst
  M --label b6_sst_snappy_fresh --reps 3 --stdout $W/sst2.out -- bash $F $B/z4b6 sst $W/sst_scratch2 $C/sst/snappy/c0.sst $C/sst/snappy/c1.sst $C/sst/snappy/c2.sst $C/sst/snappy/c3.sst
  M --label b6_sst_lz4_PROF --reps 1 --prof $PR/b6_sst -- bash $F $B/z4b6-prof sst $W/sst_scratch3 $C/sst/lz4/c0.sst $C/sst/lz4/c1.sst; sumprof b6_sst
  if ls $C/parquet/lz4/*.parquet >/dev/null 2>&1; then
    M --label b6_parquet_lz4raw_fresh --reps 3 --stdout $W/pq.out -- bash $F $B/z4b6 parquet $W/pq_scratch $C/parquet/lz4/c0.parquet $C/parquet/lz4/c1.parquet $C/parquet/lz4/c2.parquet $C/parquet/lz4/c3.parquet
    M --label b6_parquet_snappy_fresh --reps 3 --stdout $W/pq2.out -- bash $F $B/z4b6 parquet $W/pq_scratch2 $C/parquet/snappy/c0.parquet $C/parquet/snappy/c1.parquet $C/parquet/snappy/c2.parquet $C/parquet/snappy/c3.parquet
    M --label b6_parquet_lz4_PROF --reps 1 --prof $PR/b6_pq -- bash $F $B/z4b6-prof parquet $W/pq_scratch3 $C/parquet/lz4/c0.parquet $C/parquet/lz4/c1.parquet; sumprof b6_pq
  fi
  ;;
b1)
  # families -> one manifest per family, as b1/run.py does
  python3 - "$C" "$W" <<'PY'
import json, sys, glob, os
C, W = sys.argv[1:3]
g = lambda p: sorted(glob.glob(os.path.join(C, p)))
fams = [dict(Name='source-tar', Kind='REAL_RELEASE', Paths=g('src/coreutils-9.*.tar')),
        dict(Name='model-pythia14m', Kind='REAL_CHECKPOINT', Paths=g('models/pythia14m/*.safetensors')),
        dict(Name='clr-newtonsoft', Kind='REAL_RELEASE', Paths=g('q6/clr/*.dll')),
        dict(Name='dex-fdroid', Kind='REAL_RELEASE', Paths=g('q6/fdroid-apk/*.dex')),
        dict(Name='sqlite-states', Kind='PRODUCER_WORKLOAD', Paths=g('sqlite/v*.db')),
        dict(Name='parquet-zstd', Kind='PRODUCER_WORKLOAD', Paths=g('parquet/zstd/c*.parquet')),
        dict(Name='rocksdb-sst-zstd', Kind='PRODUCER_WORKLOAD', Paths=g('sst/zstd/c*.sst')),
        dict(Name='git-packs', Kind='REAL_HISTORY', Paths=g('git/h*.pack')),
        dict(Name='bitcode-sqlite', Kind='REAL_RELEASE_SOURCE_COMPILED', Paths=g('b4/bc/sqlite-*-O2.bc'))]
os.makedirs(W + '/stores', exist_ok=True)
for f in fams:
    json.dump([f], open(f"{W}/{f['Name']}.manifest.json", 'w'))
print('\n'.join(f['Name'] for f in fams if f['Paths']))
PY
  for f in $(python3 -c "import glob,os;print(' '.join(sorted(os.path.basename(p)[:-14] for p in glob.glob('$W/*.manifest.json'))))"); do
    M --label b1_cdc_$f --reps 3 --pre "rm -rf $W/stores/$f-cdc" --stdout $W/b1_$f.jsonl -- $B/z4similarity -manifest $W/$f.manifest.json -out $W/stores -mode cdc
    M --label b1_replay_$f --reps 1 --cwd / --stdout $W/b1_replay_$f.jsonl -- $B/z4similarity -replay $W/stores/$f-cdc
  done
  M --label b1_PROF_clr --reps 1 --prof $PR/b1_clr --pre "rm -rf $W/stores/clr-newtonsoft-cdc" -- $B/z4similarity-prof -manifest $W/clr-newtonsoft.manifest.json -out $W/stores -mode cdc; sumprof b1_clr
  M --label b1_PROF_tar --reps 1 --prof $PR/b1_tar --pre "rm -rf $W/stores/source-tar-cdc" -- $B/z4similarity-prof -manifest $W/source-tar.manifest.json -out $W/stores -mode cdc; sumprof b1_tar
  M --label b1_PROF_sqlite --reps 1 --prof $PR/b1_sqlite --pre "rm -rf $W/stores/sqlite-states-cdc" -- $B/z4similarity-prof -manifest $W/sqlite-states.manifest.json -out $W/stores -mode cdc; sumprof b1_sqlite
  for n in 100000 1000000 10000000; do M --label b1_index_scale_$n --reps 3 --stdout $W/scale_$n.out -- $B/z4similarity -scale $n; done
  M --label b1_hash_index --reps 3 -- $B/z4similarity -hash-index -scale 1000000
  M --label b1_index_scale_PROF --reps 1 --prof $PR/b1_scale -- $B/z4similarity-prof -scale 1000000; sumprof b1_scale
  M --label b1_quality_clr --reps 1 --stdout $W/q.out -- $B/z4similarity -quality -manifest $W/clr-newtonsoft.manifest.json
  M --label b1_baseline_ranges_clr --reps 1 -- $B/z4similarity -baseline-ranges -manifest $W/clr-newtonsoft.manifest.json
  for c in lz4 zstd; do :; done
  ;;
esac
echo RESEARCH_${E}_DONE
