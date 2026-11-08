#!/bin/sh
# mkenv.sh NAME BINDIR "VAR=VAL ..." -> /tmp/.../v_NAME : variant dir whose zeros3 wrapper exports the given environment.
set -eu
D=${P1A_VARS:-/tmp/claude-0/p1a}/v_$1; mkdir -p "$D"; cp "$2/p0bench" "$D/"
for b in zeros3 zeros3-prof; do
  { printf '#!/bin/sh\n'; for kv in $3; do printf 'export %s\n' "$kv"; done; printf 'exec %s/%s "$@"\n' "$2" "$b"; } > "$D/$b"; chmod +x "$D/$b"
done
echo "$D"
