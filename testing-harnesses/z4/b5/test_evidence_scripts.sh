#!/bin/bash
# Regression: B3/B4 reproduction scripts must exit nonzero when a verifier, test or evidence phase fails.
# Uses a python3 PATH shim so the real, unmodified scripts run end to end against stub tools.
set -uo pipefail
Z4=$(cd "$(dirname "$0")/.." && pwd); T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir -p "$T/shim"; cat > "$T/shim/python3" <<'SH'
#!/bin/bash
case "$*" in *verify_replay.py*) [ "${SHIM_FAIL_VERIFY:-0}" = 1 ] && { echo "shim: verifier failure" >&2; exit 1; };; esac
a=("$@"); for i in "${!a[@]}"; do [ "${a[$i]}" = -store ] && mkdir -p "${a[$((i+1))]}"; done
exit 0
SH
chmod +x "$T/shim/python3"
fail=0; check() { # name expected-rc-class(0|nz) actual
  local ok=0; if [ "$2" = 0 ]; then [ "$3" -eq 0 ] && ok=1; else [ "$3" -ne 0 ] && ok=1; fi
  if [ $ok = 1 ]; then echo "PASS $1 (rc=$3)"; else echo "FAIL $1 (want $2, rc=$3)"; fail=1; fi; }
# --- B3 tensor lane
mkdir -p "$T/hf/smol" "$T/hf/pythia" "$T/hf/pythia_ctrl"
for v in 0 1; do rm -rf "$T/w$v"
  PATH="$T/shim:$PATH" SHIM_FAIL_VERIFY=$v bash "$Z4/b3/run_tensor_evidence.sh" /bin/true "$T/hf" "$T/w$v" >/dev/null 2>&1; rc=$?
  if [ $v = 1 ]; then check "b3-tensor failing verifier" nz $rc; [ -e "$T/w$v/tensor_evidence.done" ] && { echo "FAIL done marker written"; fail=1; }
  else check "b3-tensor passing verifier" 0 $rc; fi; done
# --- B3 other lane
mkdir -p "$T/ow/git"; for t in r0_ r1_ r2_ r3_ r4_; do for k in tar gz6.tar.gz gz9.tar.gz layer.tar.gz nodelta.pack delta.pack; do : > "$T/ow/git/${t}x.$k"; done; done
for v in 0 1; do
  PATH="$T/shim:$PATH" SHIM_FAIL_VERIFY=$v bash "$Z4/b3/run_other_evidence.sh" /bin/true /nonexistent "$T/ow" >/dev/null 2>&1; rc=$?
  if [ $v = 1 ]; then check "b3-other failing verifier" nz $rc; else check "b3-other passing verifier" 0 $rc; fi; done
# --- B4 go-test phase: failing test must fail the script; a passing run needs a real ok line
mkdir -p "$T/b4"; printf '#!/bin/bash\necho FAIL\nexit 3\n' > "$T/failtest.sh"; printf '#!/bin/bash\necho ok\n' > "$T/oktest.sh"; chmod +x "$T"/*.sh
B4_GOTEST="$T/failtest.sh" bash "$Z4/b4/run_gotest.sh" "$T/b4" >/dev/null 2>&1; check "b4-gotest failing test" nz $?
B4_GOTEST="$T/oktest.sh" bash "$Z4/b4/run_gotest.sh" "$T/b4" >/dev/null 2>&1; check "b4-gotest passing test" 0 $?
printf '#!/bin/bash\necho nothing\n' > "$T/silent.sh"; chmod +x "$T/silent.sh"
B4_GOTEST="$T/silent.sh" bash "$Z4/b4/run_gotest.sh" "$T/b4" >/dev/null 2>&1; check "b4-gotest no ok line" nz $?
# --- B4 evidence runner with an empty corpus: first evidence phase must fail the run
mkdir -p "$T/b4e"; bash "$Z4/b4/run_evidence.sh" "$T/b4e" >/dev/null 2>&1; check "b4-evidence missing inputs" nz $?
# --- static guard: no suppression idioms remain
if grep -nE '\|\| *true|2>&1 *\| *grep' "$Z4"/b3/run_*_evidence.sh "$Z4"/b4/run_evidence.sh; then echo "FAIL suppression idiom present"; fail=1; else echo "PASS no suppression idioms"; fi
exit $fail
