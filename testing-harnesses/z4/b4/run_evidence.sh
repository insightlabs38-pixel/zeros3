#!/bin/bash
set -euo pipefail
# usage: run_evidence.sh WORKDIR (after build_corpus.sh and q6_fetch_frontier_inputs.py into WORKDIR/q6)
W=$(realpath "$1"); Z=$(cd "$(dirname "$0")/../../z3" && pwd); cd "$Z"; go build -o "$W/z4b4" ./cmd/z4b4
B=$W/bc; C=$W/ctl; P=$W/pdb; Q=$W/q6
run() { echo "### $*"; "$@"; echo; }
{
run "$W/z4b4" llvm $B/sqlite-3450100-O2.bc $B/sqlite-3450200-O2.bc $B/sqlite-3450300-O2.bc $B/sqlite-3460000-O2.bc $B/sqlite-3460100-O2.bc $B/sqlite-3490100-O2.bc
run "$W/z4b4" llvm $B/sqlite-3450100-O0.bc $B/sqlite-3450300-O0.bc $B/sqlite-3460100-O0.bc $B/sqlite-3490100-O0.bc
run "$W/z4b4" llvm $C/base.bc $C/early.bc $C/late.bc $C/type.bc $C/global.bc $C/const.bc $C/rename.bc $C/restored.bc
run "$W/z4b4" policy 3 $B/sqlite-3450100-O2.bc $B/sqlite-3450200-O2.bc $B/sqlite-3450300-O2.bc $B/sqlite-3460000-O2.bc $B/sqlite-3460100-O2.bc $B/sqlite-3490100-O2.bc
run "$W/z4b4" pdb $P/12.0.3-netstandard2.0.pdb $P/13.0.1-netstandard2.0.pdb $P/13.0.2-netstandard2.0.pdb $P/13.0.3-netstandard2.0.pdb $P/13.0.4-netstandard2.0.pdb
run "$W/z4b4" dex $Q/fdroid-apk/2.0.0.dex $Q/fdroid-apk/2.0.1.dex $Q/fdroid-apk/2.0.2.dex
run "$W/z4b4" clr $Q/clr/12.0.3.dll $Q/clr/13.0.1.dll $Q/clr/13.0.2.dll $Q/clr/13.0.3.dll $Q/clr/13.0.4.dll
run "$W/z4b4" wasm $C/wasm/base.wasm $C/wasm/early.wasm $C/wasm/late.wasm $C/wasm/type.wasm $C/wasm/global.wasm $C/wasm/const.wasm $C/wasm/rename.wasm $C/wasm/restored.wasm
run "$W/z4b4" perf $B/sqlite-3490100-O2.bc
mkdir -p $W/ctlroot; ln -sfn $B $W/ctlroot/bc
echo "### go test (real graph matching, retention, fixtures)"
"$(dirname "$0")/run_gotest.sh" "$W"
} 2>&1
