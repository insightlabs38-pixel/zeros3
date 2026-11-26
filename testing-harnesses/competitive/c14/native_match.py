"""Compare native XD matchers on exact volume and SQLite histories."""
import argparse
import json
import os
from pathlib import Path
import sys
import tempfile

from fixtures import digest, inspect_fs, query

harness = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(harness/"c3"))
from transfer import run


def measurements(args, proof, work, base, target, pair, label, tune):
    program, output = work/"delta", work/"output"
    program.unlink(missing_ok=True)
    output.unlink(missing_ok=True)
    env = {**os.environ, "GOMAXPROCS": "2", "C12_METHOD": "xd",
           "C12_BASE": str(base), "C12_TARGET": str(target),
           "C12_PROGRAM": str(program), "C12_OUTPUT": str(output)}

    def invoke(name, action=None):
        if action:
            env["C12_ACTION"] = action
        return run(args.lab, env, ["-test.run", "^"+name+"$", "-test.v", "-test.timeout", "25s"], 30)

    profile = {}
    if tune is None:
        encode = invoke("TestC12Process", "encode")
    else:
        env.update(C14_BASE=str(base), C14_TARGET=str(target), C14_PROGRAM=str(program))
        if isinstance(tune, str):
            env.update(C14_SQL_MODE=tune)
            encode = invoke("TestC14NativeSQLiteEncode")
        else:
            gap, bits, frame, hole_copy = tune
            env.update(C14_GAP=str(gap), C14_SAMPLE=str(bits), C14_FRAME=str(frame), C14_HOLE_COPY=str(hole_copy))
            encode = invoke("TestC14NativeSparseEncode")
        profile = json.loads(next(line[4:] for line in encode["stdout"].splitlines() if line.startswith("C14 ")))
    decode = invoke("TestC12Process", "decode")
    ranged = invoke("TestC12Process", "ranges")
    ranged.update(json.loads(next(line[4:] for line in ranged["stdout"].splitlines() if line.startswith("C12 "))))
    for row in (encode, decode, ranged):
        row.pop("stdout")
    want = proof["sha256"]
    if digest(output) != want or digest(target) != want:
        raise ValueError("native sparse proof differs")
    if "offset" in proof:
        consumer = {"filesystem_files": inspect_fs(output, proof["offset"], proof["sample"], work, args.fs_tools)}
    else:
        consumer = query(output)
        if any(value != proof[key] for key, value in consumer.items()):
            raise ValueError("native SQLite replay differs")
    return {"pair": pair, "lane": label, "settings": tune, "target_sha256": want,
            "allocated": program.stat().st_blocks*512, "program_bytes": program.stat().st_size,
            "engine": profile, "build": encode, "decode": decode, "ranges": ranged, "consumer": consumer}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("lab", "corpus", "fixture_manifest", "work", "fs_tools", "results"):
        p.add_argument("--"+name.replace("_", "-"), type=lambda name: Path(name).resolve(), required=True)
    p.add_argument("--family", choices=("volume", "volume-incremental", "sqlite"), default="volume")
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    proof = json.loads(args.fixture_manifest.read_text())
    lanes = [("stock", None), ("sparse", (32768, 0, 1048576, 32<<20)),
             ("thin", (32768, 2, 1048576, 32<<20)),
             ("bounded", (32768, 0, 262144, 262144)),
             ("bounded-thin", (32768, 2, 262144, 262144)),
             ("short-holes", (8192, 0, 262144, 262144))]
    filename, history = "volume.raw", proof[args.family]
    if args.family == "sqlite":
        lanes = [("stock", None), ("page-match", "plain"), ("overflow-guided", "overflow")]
        filename, history = "snapshot.sqlite", history["versions"]
    with tempfile.TemporaryDirectory(dir=args.work) as scratch, args.results.open("x") as out:
        for i in (0, 1):
            base, target = (args.corpus/args.family/str(rev)/filename for rev in (i, i+1))
            for label, tuning in lanes[::1 if i == 0 else -1]:
                row = measurements(args, history[i+1], Path(scratch), base, target, i, label, tuning)
                print(json.dumps({"family": args.family, **row}), file=out, flush=True)


if __name__ == "__main__":
    main()
