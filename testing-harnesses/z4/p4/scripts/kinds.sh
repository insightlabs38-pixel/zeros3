# Kind -> object list for the P4 benchmarks (sourced). C = corpus dir (see ../../p0/scripts/fetch_corpus.sh, make_structured.py).
declare -A K
K[k01_tar]="$C/src/coreutils-9.5.tar,$C/src/coreutils-9.6.tar,$C/src/coreutils-9.7.tar"
K[k02_car]="$C/q6/car/sample-v1.car.car,$C/q6/car/sample-wrapped-v2.car.car,gen:car:6000"
K[k03_sqlite]="$C/sqlite/v0.db,$C/sqlite/v1.db"
K[k04_git]="$C/git/h0.pack,$C/git/h1.pack,$C/git/h2.pack"
K[k05_merkle]="gen:merkle:60000"
K[k06_gzip]="$C/gz/coreutils-9.7.l6.tar.gz"
K[k07_zstd]="$C/zstd/coreutils-9.7.l3.tar.zst"
K[k08_model]="$C/models/pythia14m/step1000.safetensors,$C/models/pythia14m/step2000.safetensors"
K[k09_sst_snappy]="$C/sst/snappy/c0.sst,$C/sst/snappy/c1.sst"
K[k09_sst_zstd]="$C/sst/zstd/c0.sst"
K[k10_parquet_snappy]="$C/parquet/snappy/c0.parquet"
K[k10_parquet_zstd]="$C/parquet/zstd/c0.parquet,$C/parquet/zstd/c1.parquet"
K[k11_dex]="$C/q6/fdroid-apk/2.0.0.dex,$C/q6/fdroid-apk/2.0.1.dex"
K[k12_clr]="$C/q6/clr/13.0.1.dll,$C/q6/clr/13.0.2.dll,$C/q6/clr/13.0.3.dll,$C/q6/clr/13.0.4.dll"
# a representative mix of every kind in one run (use with CONC=4)
K[k99_mix]="$C/src/coreutils-9.7.tar,gen:car:6000,$C/sqlite/v0.db,$C/git/h0.pack,gen:merkle:60000,$C/gz/coreutils-9.7.l6.tar.gz,$C/zstd/coreutils-9.7.l3.tar.zst,$C/models/pythia14m/step1000.safetensors,$C/sst/snappy/c0.sst,$C/parquet/zstd/c0.parquet,$C/q6/fdroid-apk/2.0.0.dex,$C/q6/clr/13.0.4.dll"
