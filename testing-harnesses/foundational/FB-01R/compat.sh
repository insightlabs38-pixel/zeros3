#!/usr/bin/env bash
# FB-01R compat runner: the ack watermark against older builds.
#   1. FB-01's runner unchanged (both directions, every object read back, a
#      clean journal opened unchanged by the other build);
#   2. a stale watermark: the new build writes, an older build extends the
#      store (it ignores journal/visibility.ack), then the new build inspects
#      it clean, reads every object and moves its watermark to the new end.
# Usage, from the repository root:
#   testing-harnesses/foundational/FB-01R/compat.sh OLD_BINARY [NEW_BINARY]
# Writes one JSON line per check to $FB01_RESULTS when set; exits 1 on a failure.
set -uo pipefail
OLD=${1:?usage: compat.sh OLD_BINARY [NEW_BINARY]}
NEW=${2:-}
W=$(mktemp -d)
PID=
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$W"; }
trap cleanup EXIT
if [ -z "$NEW" ]; then
	go build -o "$W/zeros3-new" . || exit 1
	NEW=$W/zeros3-new
fi
"$(dirname "$0")/../FB-01/compat.sh" "$OLD" "$NEW"
BASE=$?

KEY=AKIAZEROS3EXAMPLE01
SECRET=zeros3exampleSecretKeyForM1TestingOnly01
PORT=$((20000 + RANDOM % 20000))
URL=http://127.0.0.1:$PORT
FAILS=0
PAIR="stale watermark $(basename "$OLD")"
s3() { curl -sS --aws-sigv4 "aws:amz:us-east-1:s3" --user "$KEY:$SECRET" "$@"; }
note() { # check ok detail
	local res=PASS
	[ "$2" = 1 ] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%-5s %-28s %s\n' "$res" "$PAIR" "$1: $3"
	[ -n "${FB01_RESULTS:-}" ] && printf '{"kind":"compat","pair":"%s","check":"%s","result":"%s","detail":"%s"}\n' \
		"$PAIR" "$1" "$res" "$3" >>"$FB01_RESULTS"
}
serve() { # binary store
	"$1" serve -store "$2" -addr "127.0.0.1:$PORT" -access-key "$KEY" -secret-key "$SECRET" -region us-east-1 >>"$W/serve.log" 2>&1 &
	PID=$!
	for _ in $(seq 100); do
		s3 -o /dev/null "$URL/" 2>/dev/null && return 0
		sleep 0.1
	done
	echo "server did not start"; tail -5 "$W/serve.log"; return 1
}
stop() { kill -TERM "$PID"; wait "$PID" 2>/dev/null; PID=; }
put() { s3 -f -o /dev/null -X PUT --data-binary "@$2" "$URL/b/$1" && sha256sum <"$2" | cut -d' ' -f1 >"$W/want/$1"; }
writes() { # prefix count
	local i
	for i in $(seq "$2"); do
		head -c $(((i * 7919) % 200000 + 1)) /dev/urandom >"$W/body"
		put "$1$i" "$W/body" || return 1
	done
}
readback() {
	local k n=0 bad=0
	for k in $(ls "$W/want"); do
		n=$((n + 1))
		[ "$(s3 -f "$URL/b/$k" | sha256sum | cut -d' ' -f1)" = "$(cat "$W/want/$k")" ] || bad=$((bad + 1))
	done
	READ_DETAIL="$n objects, $bad wrong"
	READ_OK=0; [ "$bad" = 0 ] && [ "$n" -gt 0 ] && READ_OK=1
}
wm() { # store field -> value from journal inspect -json
	"$NEW" journal inspect -store "$1" -json 2>/dev/null | python3 -c "import json,sys; r=json.load(sys.stdin); print($2)"
}
store=$W/store
mkdir -p "$W/want"
ok=0; serve "$NEW" "$store" && s3 -f -o /dev/null -X PUT "$URL/b" && writes a 12 && ok=1
note "new build writes" $ok "12 puts"
stop
s1=$(wm "$store" "r['watermark']['offset']")
e1=$(wm "$store" "r['valid_end']")
note "watermark at the end after a clean stop" $([ "$s1" = "$e1" ] && echo 1 || echo 0) "watermark $s1, journal $e1"
ok=0; serve "$OLD" "$store" && writes c 8 && ok=1
note "older build extends the store" $ok "8 puts"
stop
s2=$(wm "$store" "r['watermark']['offset']")
e2=$(wm "$store" "r['valid_end']")
v2=$(wm "$store" "r['verdict']")
note "new build inspects the stale watermark" $([ "$s2" = "$s1" ] && [ "$e2" -gt "$s2" ] && [[ "$v2" == clean* ]] && echo 1 || echo 0) \
	"watermark $s2 behind journal $e2; $v2"
serve "$NEW" "$store" || { note "new build reopens" 0 "$(tail -3 "$W/serve.log")"; exit 1; }
readback
note "new build reads every object" $READ_OK "$READ_DETAIL"
ok=0; writes d 4 && ok=1
stop
s3v=$(wm "$store" "r['watermark']['offset']")
e3=$(wm "$store" "r['valid_end']")
note "new build moves the watermark to the end" $([ "$ok" = 1 ] && [ "$s3v" = "$e3" ] && echo 1 || echo 0) "watermark $s3v, journal $e3"
[ "$FAILS" = 0 ] && echo "compat (watermark): all checks passed" || echo "compat (watermark): $FAILS checks failed"
[ "$FAILS" = 0 ] && [ "$BASE" = 0 ]
