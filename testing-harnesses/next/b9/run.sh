#!/bin/bash
# B9 reproduction: corpus (published files + make_inputs.py), CDC fixtures, materialized lanes, record-store and
# append measurements (A), executable lanes and probe (B), reads and the decoded-window cache (C).
# usage: run.sh WORK   (needs root for drop_caches; ~6 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); R="$ROOT/testing-harnesses/next/b9/results"; mkdir -p "$R"
C=$W/corpus; mkdir -p "$C/src" "$C/go" "$C/gobin" "$C/ts" "$C/django"
for v in 9.5 9.6 9.7; do [ -f "$C/src/coreutils-$v.tar" ] || { curl -sSfL -o "$C/src/coreutils-$v.tar.xz" "https://ftp.gnu.org/gnu/coreutils/coreutils-$v.tar.xz"; xz -dk "$C/src/coreutils-$v.tar.xz"; }; done
for v in 1.22.0 1.22.6 1.23.0 1.23.4; do
  [ -f "$C/go/go$v.src.tar.gz" ] || curl -sSfL -o "$C/go/go$v.src.tar.gz" "https://go.dev/dl/go$v.src.tar.gz"
  for p in linux-amd64.tar.gz windows-amd64.zip; do [ -f "$C/gobin/go$v.$p" ] || curl -sSfL -o "$C/gobin/go$v.$p" "https://go.dev/dl/go$v.$p"; done
done
for v in 5.6.2 5.6.3 5.7.2; do [ -f "$C/ts/typescript-$v.tgz" ] || curl -sSfL -o "$C/ts/typescript-$v.tgz" "https://registry.npmjs.org/typescript/-/typescript-$v.tgz"; done
for v in 5.1.1 5.1.2 5.1.3; do
  pip download -q --no-deps --no-binary :all: -d "$C/django" "django==$v"; pip download -q --no-deps --only-binary :all: -d "$C/django" "django==$v"
done
(cd "$C" && sha256sum -c "$ROOT/testing-harnesses/next/b9/corpus.sha256" --quiet)
python3 -I "$ROOT/testing-harnesses/next/b9/make_inputs.py" "$C"
cd "$ROOT"
T="-tags b9bench,b6bench,b5locality,b1rangeexperiment -count=1 -timeout 8h"
export ZEROS3_B6_FX=$W/fx ZEROS3_B6_CORPUS=$C
go test $T -run '^TestB9Build$' .
# A: small-file economics (lanes and the record store of each), append/retire, reads and concurrency
ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=cu-files,go-files,rnd-files,synth \
  ZEROS3_B6_LANES=cdc,b8-archive,b8-mixed,b8m-archive,b8m-mixed,b9-archive,b9-mixed go test $T -run '^TestB9Lanes$' .
ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=cu-files,go-files ZEROS3_B6_LANES=b8g-archive go test $T -run '^TestB9Lanes$' .
ZEROS3_B6_OUT=$R/lanes.jsonl go test $T -run '^TestB9Append$' .
ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_FAMILIES=cu-files,go-files \
  ZEROS3_B6_LANES=cdc,b8-archive,b9-archive go test $T -run '^TestB9Reads$' .
ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_FAMILIES=cu-files ZEROS3_B6_LANES=b8g-archive,b8-mixed,b9-mixed go test $T -run '^TestB9Reads$' .
ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=cu-files ZEROS3_B6_LANES=b8-archive,b9-archive go test $T -run '^TestB9Concurrent$' .
ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=cu-files ZEROS3_B6_LANES=b8-archive,b8g-archive,b8-mixed,b9-mixed go test $T -run '^TestB9Concurrent$' .
# B: executables (probe of delta literals by section, lanes, reads)
for fam in elf pe; do for tool in compile link go vet asm cgo; do ext=""; [ $fam = pe ] && ext=.exe
  for x in "" 1; do echo "== $fam $tool$ext pcln-columns=${x:-0}"
    B9_PCLN=$x B9_EXE_DIR=$C/$fam B9_TOOL=$tool$ext go test $T -run '^TestB9ExeProbe$' -v . | grep -E 'delta raw|literals|units'; done
done; done > "$R/exeprobe.txt"
ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=elf,pe,ts,django ZEROS3_B6_LANES=cdc,b8c-archive,b9n-archive go test $T -run '^TestB9Lanes$' .
ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_1M=1 ZEROS3_B6_FAMILIES=elf,pe \
  ZEROS3_B6_LANES=cdc,b8c-archive,b9n-archive go test $T -run '^TestB9Reads$' .
# C: decoded-window cache (canonical TS and Django stores; the grouped Coreutils store as negative control)
ZEROS3_B6_OUT=$R/cache.jsonl ZEROS3_B6_FAMILIES=ts,django go test $T -run '^TestB9Cache$' .
ZEROS3_B6_OUT=$R/cache.jsonl ZEROS3_B9_WINDOW_KIB=16 ZEROS3_B6_FAMILIES=ts,django go test $T -run '^TestB9Cache$' .
ZEROS3_B6_OUT=$R/cache.jsonl ZEROS3_B6_FAMILIES=cu-files ZEROS3_B9_LANE=b9-archive go test $T -run '^TestB9Cache$' .
gzip -9f "$R/reads.jsonl" "$R/lanes.jsonl"
# (cache-v1.jsonl: the first design, admitting a window at its first touch; superseded, kept as a negative result)
python3 "$ROOT/testing-harnesses/next/b9/tables.py" "$R"/*.jsonl "$R"/*.jsonl.gz > "$R/tables.md"
