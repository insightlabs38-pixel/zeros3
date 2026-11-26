#!/usr/bin/env bash
# FB-01 compat runner (G-COMPAT). A store written by one build is read, then
# extended, by the other, both ways round, over the S3 API:
#   - every acknowledged object reads back byte for byte, deletes stay deleted;
#   - a clean journal is opened unchanged (same SHA-256) by the other build;
#   - `zeros3 journal inspect` on the new build calls it clean.
# Usage, from the repository root:
#   testing-harnesses/foundational/FB-01/compat.sh OLD_BINARY [NEW_BINARY]
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
KEY=AKIAZEROS3EXAMPLE01
SECRET=zeros3exampleSecretKeyForM1TestingOnly01
PORT=$((20000 + RANDOM % 20000))
URL=http://127.0.0.1:$PORT
FAILS=0
PAIR=
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
jsha() { sha256sum "$1/journal/visibility.log" | cut -d' ' -f1; }
put() { # key file
	s3 -f -o /dev/null -X PUT --data-binary "@$2" "$URL/b/$1" && sha256sum <"$2" | cut -d' ' -f1 >"$W/want/$1"
}
del() { s3 -f -o /dev/null -X DELETE "$URL/b/$1" && rm -f "$W/want/$1" && touch "$W/gone/$1"; }
writes() { # prefix count
	local i f
	for i in $(seq "$2"); do
		f=$W/body
		head -c $(((i * 7919) % 300000 + 1)) /dev/urandom >"$f"
		put "$1$i" "$f" || return 1
	done
	head -c 1000 /dev/urandom >"$W/body" && put "${1}1" "$W/body" && put "${1}2" "$W/body" || return 1
	del "${1}3" && del "${1}4"
}
readback() { # -> sets READ_OK/READ_DETAIL
	local k n=0 bad=0
	for k in $(ls "$W/want"); do
		n=$((n + 1))
		[ "$(s3 -f "$URL/b/$k" | sha256sum | cut -d' ' -f1)" = "$(cat "$W/want/$k")" ] || bad=$((bad + 1))
	done
	for k in $(ls "$W/gone"); do
		[ "$(s3 -o /dev/null -w '%{http_code}' "$URL/b/$k")" = 404 ] || bad=$((bad + 1))
	done
	READ_DETAIL="$n live objects, $(ls "$W/gone" | wc -l) deleted, $bad wrong"
	READ_OK=0; [ "$bad" = 0 ] && [ "$n" -gt 0 ] && READ_OK=1
}
run_pair() { # writer reader label
	PAIR=$3
	local store=$W/store-$RANDOM
	rm -rf "$W/want" "$W/gone"; mkdir -p "$W/want" "$W/gone"
	serve "$1" "$store" || { note start 0 "writer did not start"; return; }
	ok=0; s3 -f -o /dev/null -X PUT "$URL/b" && writes a 24 && ok=1
	note "writer acknowledged" $ok "bucket b, 24 puts, 2 overwrites, 2 deletes"
	stop
	local before; before=$(jsha "$store")
	if [ "$2" = "$NEW" ] || [ "$1" = "$NEW" ]; then
		"$NEW" journal inspect -store "$store" >"$W/inspect" 2>&1
		ok=0; [ $? = 0 ] && grep -q 'verdict: clean' "$W/inspect" && ok=1
		note "new build inspects clean" $ok "$(grep verdict "$W/inspect")"
	fi
	serve "$2" "$store" || { note "reader opens" 0 "$(tail -3 "$W/serve.log")"; return; }
	readback
	note "reader reads every object" $READ_OK "$READ_DETAIL"
	stop
	local after; after=$(jsha "$store")
	note "journal opened unchanged" $([ "$before" = "$after" ] && echo 1 || echo 0) "sha256 ${before:0:16} -> ${after:0:16}"
	ok=0; serve "$2" "$store" && writes c 8 && ok=1
	note "reader extends the store" $ok "8 puts, 2 overwrites, 2 deletes"
	stop
	serve "$1" "$store" || { note "writer reopens" 0 "$(tail -3 "$W/serve.log")"; return; }
	readback
	note "writer reads the other build's frames" $READ_OK "$READ_DETAIL"
	stop
}
run_pair "$OLD" "$NEW" "old->new $(basename "$OLD")"
run_pair "$NEW" "$OLD" "new->old $(basename "$OLD")"
[ "$FAILS" = 0 ] && echo "compat: all checks passed" || echo "compat: $FAILS checks failed"
[ "$FAILS" = 0 ]
