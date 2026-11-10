#!/bin/bash
# Real-binary store-format refusal matrix. Builds nothing: pass two binaries.
#   formatmatrix.sh MAIN_BINARY DRAFT_BINARY WORKDIR
# For each (binary, FORMAT.json store_format_version) it runs `stats` and
# `verify` and records accept/refuse plus whether the store tree is byte-identical afterwards.
set -u
MAIN=$1; DRAFT=$2; W=$3; rm -rf "$W"; mkdir -p "$W"
echo "# NOTE: levels >= 5 need valid tier roots; where the message is a tier error the version gate PASSED (accepted) and the fixture failed later"
printf '%-8s %-6s %-8s %-10s %s\n' binary level stats verify tree-unchanged
for lv in 1 9 10 11 12 13 99; do
  for name in main draft; do
    bin=$MAIN; [ $name = draft ] && bin=$DRAFT
    d=$W/$name-$lv; mkdir -p "$d"
    "$MAIN" stats -store "$d" >/dev/null 2>&1          # initialise a store with the current binary
    "$MAIN" tier init -store "$d" -tier warm >/dev/null 2>&1; "$MAIN" tier init -store "$d" -tier cold >/dev/null 2>&1  # valid tier roots for levels >= 5
    python3 - "$d/FORMAT.json" "$lv" <<'PY'
import json,sys
p=sys.argv[1]; f=json.load(open(p)); f['store_format_version']=int(sys.argv[2]); json.dump(f,open(p,'w'),indent=2)
PY
    before=$(cd "$d" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum)
    "$bin" stats -store "$d" >/dev/null 2>"$W/err-s"; rs=$?
    "$bin" verify -store "$d" >/dev/null 2>"$W/err-v"; rv=$?
    after=$(cd "$d" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum)
    msg=$(head -c 90 "$W/err-s" | tr '\n' ' ')
    printf '%-8s %-6s %-8s %-10s %s  %s\n' "$name" "$lv" "$([ $rs = 0 ] && echo accept || echo refuse)" "$([ $rv = 0 ] && echo accept || echo refuse)" "$([ "$before" = "$after" ] && echo yes || echo NO)" "$msg"
  done
done
