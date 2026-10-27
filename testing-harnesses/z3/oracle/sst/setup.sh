#!/bin/sh
# Z3-30 producers: pinned toolchains OUTSIDE Git. Nothing here is a runtime dependency of the Z3 module.
#   LevelDB 1.23  (Ubuntu libleveldb-dev 1.23-5build1 + libsnappy 1.1.10, via plyvel 1.5.1)
#   RocksDB 9.8.4 (bundled in rocksdict 0.3.29)
#   Pebble  v1.1.5 (Go module, nested module pebblegen; FormatMajorVersion = FormatNewest -> table format Pebblev4)
set -e
R="${Z3_SST_ROOT:-$HOME/.cache/z3-sst}"
mkdir -p "$R"
apt-get install -y libleveldb-dev libsnappy-dev >/dev/null
python3 -m venv "$R/venv"
"$R/venv/bin/pip" install -q plyvel==1.5.1 rocksdict==0.3.29
(cd "$(dirname "$0")/pebblegen" && GOFLAGS=-mod=mod go build -o "$R/pebblegen-bin" .)
# workload: deterministic operation logs shared by every producer
(cd "$(dirname "$0")/../../cmd/z3sst" && go run . gen-ops "$R/ops")
# corpus
"$R/venv/bin/python" "$(dirname "$0")/gen_sst.py" leveldb "$R/ops" "$R/corpus" "$R/work"
"$R/venv/bin/python" "$(dirname "$0")/gen_sst.py" rocksdb "$R/ops" "$R/corpus" "$R/work"
"$R/pebblegen-bin" "$R/ops" "$R/corpus" "$R/work"
