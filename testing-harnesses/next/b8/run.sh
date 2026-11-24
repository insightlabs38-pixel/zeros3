#!/bin/bash
# B8 reproduction: corpus (published files + make_inputs.py), CDC fixtures, materialized lanes (archive first:
# its plans do not depend on the read model), cold reads (calibration), mixed/hot lanes, concurrency.
# usage: run.sh WORK   (needs root for drop_caches; ~6 GB of scratch)
set -euo pipefail
W=$(realpath -m "${1:?work dir}"); mkdir -p "$W"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd); R="$ROOT/testing-harnesses/next/b8/results"; mkdir -p "$R"
C=$W/corpus; mkdir -p "$C/ts" "$C/src" "$C/django" "$C/lone"
for v in 5.6.2 5.6.3 5.7.2; do [ -f "$C/ts/typescript-$v.tgz" ] || curl -sSfL -o "$C/ts/typescript-$v.tgz" "https://registry.npmjs.org/typescript/-/typescript-$v.tgz"; done
for v in 9.5 9.6 9.7; do for x in gz xz; do [ -f "$C/src/coreutils-$v.tar.$x" ] || curl -sSfL -o "$C/src/coreutils-$v.tar.$x" "https://ftp.gnu.org/gnu/coreutils/coreutils-$v.tar.$x"; done; done
for v in 5.1.1 5.1.2 5.1.3; do
  pip download -q --no-deps --no-binary :all: -d "$C/django" "django==$v"; pip download -q --no-deps --only-binary :all: -d "$C/django" "django==$v"
done
pip download -q --no-deps --no-binary :all: -d "$C/lone" requests==2.32.3; pip download -q --no-deps --only-binary :all: -d "$C/lone" requests==2.32.3
cp -n "$C/src/coreutils-9.5.tar.xz" "$C/lone/"
python3 -I "$ROOT/testing-harnesses/next/b8/make_inputs.py" "$C"   # needs python-zstandard 0.25.0
python3 -I - "$C" <<'PY'   # B6's per-file Coreutils history (src/ and lib/ members >= 4 KiB), for the B8-G model
import os, sys, tarfile
C = sys.argv[1]
for v in ['9.5', '9.6', '9.7']:
    d = f'{C}/cu/{v}'
    if os.path.exists(d + '/.done'):
        continue
    with tarfile.open(f'{C}/src/coreutils-{v}.tar') as t:
        for m in t:
            if m.isfile() and any(m.name.startswith(f'coreutils-{v}/{p}') for p in ['src/', 'lib/']):
                b = t.extractfile(m).read()
                if len(b) >= 4096:
                    p = os.path.join(d, m.name.split('/', 1)[1]); os.makedirs(os.path.dirname(p), exist_ok=True); open(p, 'wb').write(b)
    open(d + '/.done', 'w').close()
PY
(cd "$C" && sha256sum -c "$ROOT/testing-harnesses/next/b8/corpus.sha256" --quiet)
cd "$ROOT"
T="-tags b8bench,b6bench,b5locality,b1rangeexperiment -count=1 -timeout 8h"
E="ZEROS3_B6_FX=$W/fx ZEROS3_B6_CORPUS=$C"
env $E go test $T -run '^TestB8Build$' .
# exact-DEFLATE residuals of the producers (B8-A)
B8_FILES=$C/ts/typescript-5.6.2.tgz,$C/django/Django-5.1.1.tar.gz,$C/django/Django-5.1.1-py3-none-any.whl,$C/src/coreutils-9.7.tar.gz \
  go test $T -run '^TestB8IRProbe$' -v . | grep -E 'gzip size|zip members' > "$R/irprobe.txt"
python3 -I "$ROOT/testing-harnesses/next/b8/oracle.py" "$C" > "$R/oracle.jsonl"
POS=ts,django,cu-gz,wrap,ts-mix
A=b7-archive,b7r-archive,b7rg-archive,b8s-archive,b8-archive
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=$POS ZEROS3_B6_LANES=cdc,$A,b8-nodelta-archive go test $T -run '^TestB8Lanes$' .
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=django ZEROS3_B6_LANES=b8-nozip-archive go test $T -run '^TestB8Lanes$' .
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=random,lone,zst19 ZEROS3_B6_LANES=cdc,b7-archive,b7r-archive,b8-archive go test $T -run '^TestB8Lanes$' .
# cold reads through the production reader (archive stores): calibrate the replay terms of the read model
env $E ZEROS3_B6_OUT=$R/reads.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_1M=1 ZEROS3_B6_FAMILIES=ts,django,cu-gz,wrap \
  ZEROS3_B6_LANES=cdc,b7rg-archive,b8-archive go test $T -run '^TestB8Reads$' .
# mixed and hot profiles (calibrated model)
M=b7-mixed,b7r-mixed,b7rg-mixed,b8s-mixed,b8-mixed
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=$POS ZEROS3_B6_LANES=$M go test $T -run '^TestB8Lanes$' .
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=ts,django ZEROS3_B6_LANES=b7rg-hot,b8-hot go test $T -run '^TestB8Lanes$' .
env $E ZEROS3_B6_OUT=$R/lanes.jsonl ZEROS3_B6_FAMILIES=random,lone,zst19 ZEROS3_B6_LANES=b7-mixed,b8-mixed go test $T -run '^TestB8Lanes$' .
env $E ZEROS3_B6_OUT=$R/reads-mixed.jsonl ZEROS3_B5_COLD=1 ZEROS3_B6_N=8 ZEROS3_B6_1M=1 ZEROS3_B6_FAMILIES=ts,django,cu-gz \
  ZEROS3_B6_LANES=b7rg-mixed,b8-mixed go test $T -run '^TestB8Reads$' .
env $E ZEROS3_B6_OUT=$R/concurrent.jsonl ZEROS3_B6_FAMILIES=ts,django ZEROS3_B6_LANES=cdc,b7rg-archive,b8-archive go test $T -run '^TestB8Concurrent$' .
ZEROS3_B6_OUT=$R/scale.jsonl go test $T -run '^TestB8Scale$' .
env $E ZEROS3_B6_FAMILIES=cu-files go test $T -run '^TestB6Build$' .
env $E ZEROS3_B6_OUT=$R/recgroup.jsonl ZEROS3_B6_FAMILIES=cu-files go test $T -run '^TestB8RecordGroup$' .
gzip -9f "$R/reads.jsonl" "$R/reads-mixed.jsonl"
python3 "$ROOT/testing-harnesses/next/b8/tables.py" "$R"/*.jsonl "$R"/*.jsonl.gz > "$R/tables.md"
