"""Compare simultaneous G5 migrations against identical cloned fixture bytes."""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import sys
import time

root, testbin, baseline, candidate, size = sys.argv[1:]
root = Path(root)
root.mkdir()
seed = root / "fixture"
seed.mkdir()
base_env = dict(os.environ, GOMAXPROCS="2", ZEROS3_C1_LARGE_SIZE=size)

def run(command, log, env=base_env):
    with log.open("xb") as f:
        subprocess.run(command, env=env, stdout=f, stderr=subprocess.STDOUT, check=True)

fixture_run = "^TestAuthorityLargeFixture$"
fixture_check = "^TestAuthorityLargeCheck$"
run([testbin, "-test.run="+fixture_run, "-test.v"], root / "fixture.log",
    dict(base_env, ZEROS3_C1_LARGE_DIR=str(seed)))
assert "--- PASS: TestAuthorityLargeFixture " in (root / "fixture.log").read_text()

def rss(pid):
    try:
        for line in (Path("/proc") / str(pid) / "status").read_text().splitlines():
            if line.startswith("VmRSS:"):
                return int(line.split()[1])
    except (OSError, ProcessLookupError):
        pass
    return 0

for label, binary in (("baseline", baseline), ("candidate", candidate)):
    target = root / label
    target.mkdir()
    dirs = [target / "a", target / "b"]
    for directory in dirs:
        subprocess.run(["cp", "-a", "--reflink=auto", seed, directory], check=True)
    with ExitStack() as files:
        children = {}
        peak = 0
        resource = {}
        started = time.monotonic()
        try:
            for directory in dirs:
                stream = files.enter_context((target / f"{directory.name}-migrate.json").open("xb"))
                command = [binary, "authority", "migrate", "-apply", "-max-depth", "2", "-archive", "-store", directory]
                p = subprocess.Popen(command, env=base_env, stdout=stream, stderr=subprocess.STDOUT)
                children[p.pid] = (p, directory.name)
            while children:
                peak = max(peak, sum(rss(pid) for pid in children))
                for pid, (p, name) in list(children.items()):
                    done, status, usage = os.wait4(pid, os.WNOHANG)
                    if done:
                        p.returncode = os.waitstatus_to_exitcode(status)
                        resource[name] = dict(rc=p.returncode, peak_rss_kib=usage.ru_maxrss,
                                              cpu_s=usage.ru_utime+usage.ru_stime,
                                              wall_s=time.monotonic()-started)
                        del children[pid]
                time.sleep(0.05)
        finally:
            for p, _ in children.values():
                p.kill()
                p.wait()
        (target / "migration-resources.json").write_text(json.dumps(
            dict(processes=resource, concurrent_sampled_peak_rss_kib=peak), indent=2) + "\n")
        if any(r["rc"] for r in resource.values()):
            raise SystemExit(f"{label} migration failed")
    for directory in dirs:
        prefix = directory.name
        for phase, args in (
            ("gc1", ["gc", "-apply"]),
            ("repack", ["repack", "-apply"]),
            ("compact", ["compact"]),
            ("gc2", ["gc", "-apply"]),
        ):
            run([binary, *args, "-store", directory], target / f"{prefix}-{phase}.log")
        run(["du", "-sb", directory], target / f"{prefix}-physical.tsv")
        check_log = target / f"{prefix}-hash-check.log"
        run([testbin, "-test.run="+fixture_check, "-test.v"], check_log,
            dict(base_env, ZEROS3_C1_LARGE_DIR=str(directory), ZEROS3_C1_LARGE_CHECK="1"))
        assert "--- PASS: TestAuthorityLargeCheck " in check_log.read_text()
        run([binary, "verify", "-store", directory, "-deep"], target / f"{prefix}-deep.log")
    print(label, json.dumps(dict(processes=resource, concurrent_sampled_peak_rss_kib=peak)), flush=True)
