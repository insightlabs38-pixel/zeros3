#!/usr/bin/env python3
"""B3 namespace measurements: fresh-process probes of matched old (B2 head) and new builds on one synthetic store.
usage: measure.py WORK OUT.jsonl [TRIALS]
WORK holds old.test and new.test (go test -c of each tree with zeros3_b3_scale_test.go), zeros3-old, zeros3-new, and
fx1m (journal-only fixture: ZEROS3_B3_BUILD=fx1m ZEROS3_B3_N=1000000 new.test -test.run TestB3BuildFixture)."""
import json, os, shutil, subprocess, sys, time

work, out = sys.argv[1], sys.argv[2]
trials = int(sys.argv[3]) if len(sys.argv) > 3 else 5
rows = []

def emit(**kw):
    rows.append(kw)
    print(json.dumps(kw), flush=True)

def probe(binary, store, mode, extra=None):
    env = dict(os.environ, ZEROS3_B3_PROBE=store, ZEROS3_B3_MODE=mode, **(extra or {}))
    res = subprocess.run([os.path.join(work, binary), "-test.run", "^TestB3Probe$"], cwd=work, env=env, capture_output=True, text=True)
    for line in res.stdout.splitlines():
        if line.startswith("B3RESULT "):
            return json.loads(line[9:])
    raise SystemExit("probe failed: " + res.stdout[-400:] + res.stderr[-400:])

def fresh(src, dst):
    shutil.rmtree(os.path.join(work, dst), ignore_errors=True)
    shutil.copytree(os.path.join(work, src), os.path.join(work, dst))

# publication / rebuild time (full journal replay + write), one store copy per build
for build, flags in (("old", ["-compact-history"]), ("new", [])):
    fresh("fx1m", "fx-" + build)
    t = time.time()
    subprocess.run([os.path.join(work, "zeros3-" + build), "namespace-checkpoint", "-store", os.path.join(work, "fx-" + build)] + flags, check=True, capture_output=True)
    emit(kind="publish", build=build, seconds=round(time.time() - t, 2))
    # the same store with 20,000 more acknowledged operations after the checkpoint
    fresh("fx-" + build, "fx-%s-suf" % build)
    subprocess.run([os.path.join(work, build + ".test"), "-test.run", "^TestB3AppendFixture$"], cwd=work, check=True, capture_output=True,
                   env=dict(os.environ, ZEROS3_B3_APPEND=os.path.join(work, "fx-%s-suf" % build), ZEROS3_B3_SUFFIX="20000"))
for build in ("old", "new"):
    for scenario, store, mode, extra in (("checkpoint", "fx-" + build, "checkpoint", None),
                                         ("checkpoint+suffix", "fx-%s-suf" % build, "checkpoint", None),
                                         ("journal", "fx-" + build, "journal", None),
                                         ("mutate", "fx-" + build, "checkpoint", {"ZEROS3_B3_MUTATE": "1"})):
        for i in range(trials if scenario != "mutate" else 2):
            emit(kind="probe", build=build, scenario=scenario, trial=i, **probe(build + ".test", store, mode, extra))
with open(out, "w") as f:
    for r in rows:
        f.write(json.dumps(r) + "\n")
