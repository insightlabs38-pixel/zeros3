"""Failure controls for run.sh, using a deterministic test binary stand-in."""

import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import unittest


DRIVER = Path(__file__).with_name("run.sh")
CHILD = """#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
import time

family = os.environ["ZEROS3_P8_FAMILY"]
control = os.environ["P8_CONTROL"]
row = dict(p8="migrate", family=family, variant="delta", rep=0, logical=100,
           objects=2, cdc_phys=100, phys_after=50, wall_s=1, cpu_s=1, rss_mib=10, exact=True)
out = Path(os.environ["ZEROS3_B2_OUT"])
if control != "empty":
    out.write_text(json.dumps(row) + "\\n")
if control == "test-fail":
    print("--- FAIL: TestP8Migration")
    print("FAIL")
    sys.exit(1)
if control == "nonzero" and family == "git":
    sys.exit(7)
if control == "partial":
    with out.open("a") as stream:
        stream.write('{"p8":')
if control == "incomplete":
    out.write_text(json.dumps({"p8": "migrate", "family": family, "exact": True}) + "\\n")
if control in {"timeout", "interrupt"}:
    Path(os.environ["P8_READY"]).write_text(str(os.getpid()))
    time.sleep(60)
print("PASS")
"""


class DriverControls(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        self.corpus = self.root / "corpus"
        self.corpus.mkdir()
        self.child = self.root / "bench"
        self.child.write_text(CHILD)
        self.child.chmod(0o755)

    def start(self, control, *args, tag="run", timeout="10"):
        env = dict(os.environ, ZEROS3_B2_CORPUS=str(self.corpus), P8_CONTROL=control,
                   ZEROS3_P8_TIMEOUT_SECONDS=timeout, P8_READY=str(self.root / "ready"))
        return subprocess.Popen(
            [DRIVER, self.child, self.root, tag, "migrate", *args],
            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )

    def check(self, control, *args, status=1, tag="run", timeout="10"):
        with self.start(control, *args, tag=tag, timeout=timeout) as proc:
            _, errors = proc.communicate(timeout=15)
            self.assertEqual(proc.returncode, status, errors.decode())
        self.assertEqual((self.root / f"{tag}.jsonl").exists(), status == 0)

    def test_success(self):
        self.check("ok", "sqlite,git", "delta", "1", status=0)
        self.assertEqual(len((self.root / "run.jsonl").read_text().splitlines()), 2)

    def test_failed_test(self):
        self.check("test-fail", "git", "delta", "1")

    def test_late_nonzero(self):
        self.check("nonzero", "sqlite,git,parquet", "delta", "1")
        self.assertTrue((self.root / "run" / "parquet.log").exists())

    def test_missing_corpus(self):
        self.corpus.rmdir()
        self.check("ok", "git", "delta", "1")

    def test_invalid_invocation(self):
        self.check("ok", "git", "invalid", "1", status=2)

    def test_timeout(self):
        self.check("timeout", "git", "delta", "1", timeout="1")
        with self.assertRaises(ProcessLookupError):
            os.kill(int((self.root / "ready").read_text()), 0)

    def test_empty_and_invalid_outputs(self):
        for control, reps in (("empty", "1"), ("partial", "1"), ("incomplete", "1"), ("ok", "2")):
            with self.subTest(control=control):
                self.check(control, "git", "delta", reps, tag=control)

    def test_interrupted_run(self):
        with self.start("interrupt", "git", "delta", "1") as proc:
            try:
                until = time.monotonic() + 5
                while not (self.root / "ready").exists() and time.monotonic() < until:
                    time.sleep(0.01)
                child = int((self.root / "ready").read_text())
            finally:
                proc.send_signal(signal.SIGTERM)
                _, errors = proc.communicate(timeout=5)
            self.assertEqual(proc.returncode, 143, errors.decode())
        self.assertFalse((self.root / "run.jsonl").exists())
        self.assertTrue((self.root / "run" / "git.log").exists())
        with self.assertRaises(ProcessLookupError):
            os.kill(child, 0)


if __name__ == "__main__":
    unittest.main()
