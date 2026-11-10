#!/bin/bash
# usage: run_kinds.sh TESTBIN LABEL OUT.jsonl [modes...]   env: C=corpus dir, W=work dir (store on disk), KINDS=filter regexp
# One process per (kind, mode): rusage/RSS belong to the run alone. Every object is read back byte-exactly and Verify -deep'd.
set -u
BIN=$1; LABEL=$2; OUT=$3; shift 3; MODES=${*:-off shadow prefer}
HERE=$(cd "$(dirname "$0")" && pwd); . "$HERE/kinds.sh"
W=${W:-/tmp/p4work}; mkdir -p "$W"
for k in $(echo "${!K[@]}" | tr ' ' '\n' | sort); do
  [[ -n "${KINDS:-}" && ! $k =~ $KINDS ]] && continue
  for m in $MODES; do
    sync; echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
    ZEROS3_P4_FILES="${K[$k]}" ZEROS3_P4_MODE=$m ZEROS3_P4_DIR="$W/store-$k-$m" ZEROS3_P4_LABEL="$LABEL/$k" ZEROS3_P4_OUT="$OUT" ZEROS3_P4_CONC=${CONC:-1} \
      "$BIN" -test.run 'TestP4Probe$' -test.count=1 -test.timeout 2h > "$W/log-$k-$m.txt" 2>&1 || echo "FAILED $k $m (see $W/log-$k-$m.txt)"
    rm -rf "$W/store-$k-$m"
  done
done
