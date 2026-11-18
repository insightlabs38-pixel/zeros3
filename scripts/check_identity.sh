#!/bin/sh
# check_identity.sh -- commit identity and message hygiene for new commits.
#
#   scripts/check_identity.sh BASE        every commit in BASE..HEAD: author and committer must be the project
#                                         identity, the message at most 10 lines and free of attribution trailers
#   scripts/check_identity.sh install     core.hooksPath -> scripts/git-hooks (pre-commit and commit-msg run the same checks)
#   scripts/check_identity.sh pre         check the identity git would use for the next commit (hook entry point)
#   scripts/check_identity.sh msg FILE    check a commit message file (hook entry point)
#
# The allowed identity is the single line of scripts/identity.allow. Commits already in BASE are not judged.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
allow=$(head -n1 "$root/scripts/identity.allow")
bad='^(Co-Authored-By|Claude-Session|Signed-off-by: *(Claude|Anthropic))|Generated with|claude\.ai/code|Claude Code|noreply@anthropic\.com'

msgcheck() { # stdin: message
	m=$(cat)
	n=$(printf '%s\n' "$m" | grep -vc '^#' || true)
	[ "$n" -le 10 ] || { echo "message has $n lines (limit 10)"; return 1; }
	if printf '%s\n' "$m" | grep -Eiq "$bad"; then echo "message carries an attribution trailer or tool footer"; return 1; fi
}

case "${1:-}" in
install) git -C "$root" config core.hooksPath scripts/git-hooks; echo "hooks installed (core.hooksPath=scripts/git-hooks)"; exit 0 ;;
pre)
	for v in GIT_AUTHOR_IDENT GIT_COMMITTER_IDENT; do
		id=$(git var $v | sed 's/ [0-9]* [+-][0-9]*$//')
		[ "$id" = "$allow" ] || { echo "$v is '$id', want '$allow'"; exit 1; }
	done
	exit 0 ;;
msg) msgcheck < "$2" || exit 1; exit 0 ;;
"") echo "usage: check_identity.sh BASE | install | pre | msg FILE" >&2; exit 2 ;;
esac

base=$1; fail=0; n=0
for c in $(git -C "$root" rev-list --reverse "$base..HEAD"); do
	n=$((n + 1))
	a=$(git -C "$root" log -1 --format='%an <%ae>' "$c"); k=$(git -C "$root" log -1 --format='%cn <%ce>' "$c")
	[ "$a" = "$allow" ] || { echo "$c author '$a'"; fail=1; }
	[ "$k" = "$allow" ] || { echo "$c committer '$k'"; fail=1; }
	git -C "$root" log -1 --format=%B "$c" | msgcheck >/tmp/ci.$$ 2>&1 || { echo "$c: $(cat /tmp/ci.$$)"; fail=1; }
done
rm -f /tmp/ci.$$
[ $fail -eq 0 ] && echo "identity ok: $n commits in $base..HEAD by $allow"
exit $fail
