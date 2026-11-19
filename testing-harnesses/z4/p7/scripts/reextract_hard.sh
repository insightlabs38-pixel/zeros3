#!/bin/bash
# Regenerate the extracted Brotli/LZ4 body of zeros3.go section 16i from testing-harnesses/z3/{brotliz3,lz4z3}.
# usage: reextract_hard.sh WORKDIR   (from the repository root; rewrites zeros3.go in place, then run srcmap.py)
# The extractor needs the production source as it was before any extraction (a9be919^); the extracted body
# (from "type hcBrotliz3_bitReader" to the banner of section 17) replaces the current one. The adapter code
# that precedes it (dictionary, hcBrotliEnsureDict) is hand-written and stays.
set -euo pipefail
W=$(realpath -m "$1"); mkdir -p "$W/pristine"
git show a9be919^:zeros3.go > "$W/pristine/zeros3.go"; git show a9be919^:go.mod > "$W/pristine/go.mod"
Z=testing-harnesses/z3; ZABS=$(realpath $Z)
go -C $Z build -o "$W/p6extract" ./cmd/p6extract
roots() { cat "$@" | grep -o "@[a-z0-9]*\.[A-Za-z0-9_]*@" | tr -d '@' | sort -u | tr '\n' ',' | sed 's/,$//'; }
( cd $Z && P6_Z3=$ZABS "$W/p6extract" -module . -modpath zeros3-z3 -pkgs brotliz3,lz4z3 -roots "$(roots brotliz3/vectors.go.txt brotliz3/rehearsal.go.txt)" \
    -prod "$W/pristine/zeros3.go" -work "$W/hard" -prefix hc -vectors brotliz3/vectors.go.txt -rehearsal brotliz3/rehearsal.go.txt ) | tail -3
diff "$W/pristine/zeros3.go" "$W/hard/integration/zeros3.go" | grep '^>' | sed 's/^> //' > "$W/added.txt" || true
python3 -I - "$W/added.txt" <<'PY'
import sys
z = open('zeros3.go').read().split('\n')
start = [i for i, l in enumerate(z) if l.startswith('type hcBrotliz3_bitReader struct {')][0]
life = [i for i, l in enumerate(z) if l.startswith('// 17. Lifecycle / main')][0]
end = life - 1
while not z[end].startswith('// ====='): end -= 1
new = open(sys.argv[1]).read().split('\n')
k = [j for j, l in enumerate(new) if l.startswith('// ===== extracted by p6extract')][0]
body = new[k + 1:]
while body and body[0] == '': body = body[1:]
while body and body[-1] == '': body = body[:-1]
open('zeros3.go', 'w').write('\n'.join(z[:start] + body + [''] + z[end:]))
PY
gofmt -l zeros3.go
