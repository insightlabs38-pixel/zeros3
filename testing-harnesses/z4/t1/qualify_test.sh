#!/bin/bash
# Negative controls for qualify.sh: stub steps drive every verdict the gate must reach. usage: qualify_test.sh (from anywhere in the repo)
set -u
cd "$(git rev-parse --show-toplevel)"
Q=$(pwd)/testing-harnesses/z4/t1; W=$(mktemp -d); trap 'rm -rf "$W"' EXIT; fails=0
cat > "$W/steps.sh" <<'S'
MANDATORY=(a b c)
run_steps() { step a bash -c "exit ${A_RC:-0}"; step b bash -c "exit ${B_RC:-0}"; step c bash -c "${C_CMD:-true}"; [ -n "${SKIP_C:-}" ] && skip c "not available"; }
S
q() { QUALIFY_STEPS="$W/steps.sh" bash "$Q/qualify.sh" "$W/out" "$W/s" > "$W/log" 2>&1; echo $?; }
# want: 0 = must pass; N = must exit exactly N (1 = the gate refused; an infrastructure error such as 127 never counts)
expect() { local what=$1 want=$2 got=$3
  if [ "$want" = "$got" ]; then echo "ok   $what (exit $got)"; else echo "FAIL $what: exit $got, wanted $want"; sed 's/^/     /' "$W/log" | tail -8; fails=$((fails+1)); fi; }
fresh() { rm -rf "$W/out"; }

mkdir "$W/bin"
cat > "$W/bin/git" <<'G'
#!/bin/sh
if [ "$1" = ls-files ]; then
  echo "injected source inventory failure" >&2
  exit 9
fi
exec "$T1_REAL_GIT" "$@"
G
chmod +x "$W/bin/git"
fresh; expect "a failed source inventory stops qualification" 2 "$(T1_REAL_GIT="$(command -v git)" PATH="$W/bin:$PATH" q)"
if [ -e "$W/out/summary.tsv" ]; then
  echo "FAIL qualification recorded steps without a source fingerprint"
  fails=$((fails+1))
fi

fresh; expect "all steps pass" 0 "$(q)"
fresh; expect "one failed step" 1 "$(A_RC=1 q)"
fresh; expect "two failed steps" 1 "$(A_RC=1 B_RC=2 q)"
fresh; A_RC=1 q > /dev/null; expect "failed step then legitimate rerun passes" 0 "$(QUALIFY_ONLY='^a$' q)"
grep -q 'earlier_attempts' "$W/out/qualification.json" && python3 -I -c "import json,sys; d=json.load(open('$W/out/qualification.json')); sys.exit(0 if d['earlier_attempts'].get('a') and d['earlier_attempts']['a'][0]['status']=='FAIL' else 1)" && echo "ok   the failed attempt stays in the record" || { echo "FAIL failed attempt vanished"; fails=$((fails+1)); }
fresh; q > /dev/null; expect "an earlier pass is masked by a later failure" 1 "$(A_RC=1 QUALIFY_ONLY='^a$' q)"
fresh; expect "a mandatory step that is only skipped" 1 "$(C_CMD=true SKIP_C=1 QUALIFY_ONLY='^c$' q)"
fresh; expect "a mandatory step that never ran" 1 "$(QUALIFY_ONLY='^(a|b)$' q)"
# interruption: signal the running qualification while a step sleeps
interrupt() { fresh; C_CMD='sleep 60' QUALIFY_STEPS="$W/steps.sh" python3 -I -c '
import subprocess, signal, sys, time
p = subprocess.Popen(["bash", sys.argv[1], sys.argv[2], sys.argv[3]], stdout=open(sys.argv[4], "w"), stderr=subprocess.STDOUT,
                     preexec_fn=lambda: signal.signal(signal.SIGINT, signal.SIG_DFL))
time.sleep(4); p.send_signal(getattr(signal, "SIG" + sys.argv[5])); print(p.wait())' "$Q/qualify.sh" "$W/out" "$W/s" "$W/log" "$1"; }
expect "SIGTERM during a step" 130 "$(interrupt TERM)"
grep -q INTERRUPTED "$W/out/summary.tsv" && echo "ok   the interruption is recorded" || { echo "FAIL interruption not recorded"; fails=$((fails+1)); }
expect "SIGINT during a step" 130 "$(interrupt INT)"
fresh; q > /dev/null; sed -i 's/\t[0-9a-f]\{64\}\t/\t0000\t/' "$W/out/summary.tsv"
expect "rows from another source are invalidated" 1 "$(QUALIFY_ONLY='^a$' q)"
fresh; expect "a failing step hidden behind a pipe is still a failure" 1 "$(C_CMD='set -o pipefail; false | cat' q)"
fresh; expect "ENOSPC in a log" 1 "$(C_CMD='echo write: no space left on device; true' q)"
[ $fails -eq 0 ] && echo "qualify negative controls: all passed" || { echo "qualify negative controls: $fails failed"; exit 1; }
