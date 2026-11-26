"""Measure actual C19 native and ordinary transfers by receiver readiness."""
import argparse
import json
import os
from pathlib import Path
import shutil
import sys
from types import SimpleNamespace

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/"c3"), str(harness/"c16")]
from frontier import LEAN, SHAPES, inputs
from transfer import BUCKET, KEY, SECRET, file_digest, put, run, serve, stage, trial, verified

PRIMARY = (
    ("loopback", "empty", ("s3", "replicate", "closure-cross")),
    ("loopback", "exact", ("s3", "replicate", "closure-cross")),
    ("loopback", "different-key", ("s3", "replicate", "closure-derived")),
    ("constrained", "empty", ("s3", "replicate", "closure-cross")),
    ("constrained", "different-key", ("s3", "replicate", "closure-derived")),
    ("constrained", "different-authority", ("replicate", "closure-cross")),
)
INTEGRATION = (
    ("loopback", "exact", ("replicate", "closure-cross")),
    ("constrained", "different-key", ("s3", "replicate", "closure-derived")),
)
ORDINARY = (("loopback", "different-key", ("s3", "replicate", "closure-derived")),)
FAMILY_CONTROL = (("loopback", "different-key", ("s3", "replicate", "closure-cross")),)


def test(args, env, root, family, preparation, matrix, repetitions, emit):
    root.mkdir()
    prior, target = inputs(args, family)
    pins = {r["family"]: r for r in map(json.loads, (harness/"c3/inputs.jsonl").read_text().splitlines())}
    shas = {"prior_sha256": file_digest(prior), "target_sha256": file_digest(target)}
    if any(sha != pins[family][name] for name, sha in shas.items()):
        raise ValueError(f"{family} transfer fixture differs from C3 source lock")
    source_store = root/"source"
    log = root/"source.log"
    with serve(args.binary, source_store, log, env) as (_, api, _):
        api.create_bucket(Bucket=BUCKET)
        put(api, "prior", prior)
        put(api, "target", target, {"scene": "c3"})
    source_optimization = None
    if preparation:
        source_optimization = run(args.binary, {**env, "TMPDIR": str(root)},
                                  [*LEAN, "-fast-reencode", "-retain-canonical-plans",
                                   "-store", str(source_store)], 90)
        source_optimization["report"] = json.loads(source_optimization.pop("stdout"))
    emit({"stage": "fixture", "family": family, "prepared_source": preparation,
          **shas, "source_optimization": source_optimization})
    with serve(args.binary, source_store, log, env) as source:
        verified(source[1], "prior", prior)
        verified(source[1], "target", target, {"scene": "c3"})
        for state in sorted({state for _, state, _ in matrix}):
            work = root/state
            work.mkdir()
            stage(args, env, work, source, state, prior, target)
        for rep in range(repetitions):
            for shape, state, lanes in matrix[::1 if rep % 2 == 0 else -1]:
                rate, delay = SHAPES[shape]
                options = SimpleNamespace(**vars(args), request_delay=delay, rate_bytes=rate,
                                          model_gzip=False, finalize=True)
                for lane in lanes[::1 if rep % 2 == 0 else -1]:
                    work = root/f"{shape}-{state}-{lane}-{rep}"
                    work.mkdir()
                    shutil.copytree(root/state/"destination", work/"destination")
                    row = trial(options, env, work, source, family, state, lane, prior, target)
                    emit({"stage": "transfer", "prepared_source": preparation, "shape": shape,
                          "repeat": rep, **row})
                    shutil.rmtree(work/"destination")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "downloads", "c1", "work", "results"):
        p.add_argument("--"+name, type=lambda x: Path(x).resolve(), required=True)
    p.add_argument("--integration-smoke", action="store_true")
    args = p.parse_args()
    args.receiver_binary = args.binary
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2", "ZEROS3_GZIP_SEQUENTIAL": "1"}
    args.work.mkdir(parents=True)
    with args.results.open("x") as out:
        def emit(row):
            print(json.dumps(row), file=out, flush=True)
        primary = INTEGRATION if args.integration_smoke else PRIMARY
        test(args, env, args.work/"typescript-prepared", "typescript", True, primary, 2, emit)
        if not args.integration_smoke:
            test(args, env, args.work/"typescript-ordinary", "typescript", False, ORDINARY, 1, emit)
            for family in ("shift", "entropy"):
                test(args, env, args.work/family, family, False, FAMILY_CONTROL, 1, emit)


if __name__ == "__main__":
    main()
