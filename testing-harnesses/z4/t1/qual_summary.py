#!/usr/bin/env python3
"""Qualification record: source fingerprint and the pass/fail gate over summary.tsv.

  qual_summary.py fingerprint            fingerprint of the tree to be qualified (stdout)
  qual_summary.py report OUT MANDATORY   write OUT/qualification.json, print the result, exit 1 unless every
                                         mandatory step PASSed on the current fingerprint as its last attempt

The fingerprint hashes every tracked or unignored file except result/evidence directories, so committing the
evidence of a run does not change the identity of the code that produced it. Rows recorded for another
fingerprint are listed as invalidated and count for nothing.
"""
import csv, hashlib, json, os, subprocess, sys

COLS = ["step", "status", "rc", "seconds", "note", "fingerprint", "finished", "command"]


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout


def fingerprint():
    files = git("ls-files", "-z", "-c", "-o", "--exclude-standard").split("\0")
    h = hashlib.sha256()
    for f in sorted(set(files)):
        if not f or "/results/" in f or f.startswith("results/") or not os.path.isfile(f):
            continue
        h.update(f.encode() + b"\0")
        with open(f, "rb") as fh:
            for blk in iter(lambda: fh.read(1 << 20), b""):
                h.update(blk)
    return h.hexdigest()


def suite(path):
    d = json.load(open(path))
    keep = ["command", "mode", "go_version", "git_head", "git_tree", "git_dirty_files", "source_hash", "fixture_hash",
            "test_binary_sha256", "production_binary_sha256", "identity", "selected", "fresh", "reused", "passed", "failed",
            "skipped", "not_run", "interrupted", "wall_seconds", "exit"]
    s = {k: d.get(k) for k in keep}
    s["sha256"] = hashlib.sha256(open(path, "rb").read()).hexdigest()
    s["tests"] = [{"test": r["test"], "status": r["status"], "seconds": r["seconds"], "cached": r.get("cached", False)} for r in d.get("results", [])]
    return s


def report(out, mand_file):
    mand = [l.strip() for l in open(mand_file) if l.strip()] if os.path.exists(mand_file) else []
    cur = fingerprint()
    rows = []
    p = os.path.join(out, "summary.tsv")
    if os.path.exists(p):
        rows = list(csv.DictReader(open(p), delimiter="\t"))
    attempts, invalid = {}, []
    for r in rows:
        (attempts.setdefault(r["step"], []) if r.get("fingerprint") == cur else invalid).append(r)
    final = {s: a[-1] for s, a in attempts.items()}
    problems = []
    if not mand:
        problems.append("no mandatory steps defined")
    for m in mand:
        if m not in final:
            problems.append(f"{m}: MISSING (never ran on this source)")
        elif final[m]["status"] != "PASS":
            problems.append(f"{m}: {final[m]['status']} (rc {final[m]['rc']}) {final[m]['note']}")
    suites = {}
    for r in final.values():
        ev = os.path.join(out, f"t1-{r['step']}.json")
        if r["status"] == "PASS" and os.path.exists(ev):
            suites[r["step"]] = suite(ev)
    for name, s in suites.items():
        if s["exit"] != 0 or s["failed"] or s["not_run"] or s["source_hash"] is None:
            problems.append(f"{name}: evidence records exit {s['exit']}, failed {s['failed']}")
    rec = {
        "status": "FAIL" if problems else "PASS",
        "problems": problems,
        "source_fingerprint": cur,
        "git_head": git("rev-parse", "HEAD").strip(),
        "git_tree": git("rev-parse", "HEAD^{tree}").strip(),
        "dirty_files": len([l for l in git("status", "--porcelain").splitlines() if "/results/" not in l]),
        "fingerprint_excludes": "directories named results",
        "mandatory": mand,
        "steps": [final[s] for s in final],
        "failed": [s for s in final if final[s]["status"] == "FAIL"],
        "skipped": [s for s in final if final[s]["status"] == "SKIP"],
        "earlier_attempts": {s: a[:-1] for s, a in attempts.items() if len(a) > 1},
        "invalidated_other_source": invalid,
        "suites": suites,
    }
    json.dump(rec, open(os.path.join(out, "qualification.json"), "w"), indent=1)
    print(f"qualification {rec['status']}: {len([m for m in mand if m in final and final[m]['status'] == 'PASS'])}/{len(mand)} mandatory steps pass on source {cur[:12]}")
    for pr in problems:
        print("  ", pr)
    if invalid:
        print(f"   {len(invalid)} earlier row(s) belong to another source and are invalidated")
    return 1 if problems else 0


if __name__ == "__main__":
    if sys.argv[1] == "fingerprint":
        print(fingerprint())
    elif sys.argv[1] == "report":
        sys.exit(report(sys.argv[2], sys.argv[3]))
    else:
        sys.exit("usage: qual_summary.py fingerprint | report OUT MANDATORY")
