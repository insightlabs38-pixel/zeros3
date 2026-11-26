"""Test the prototype bytes with the native decoder and actual store machinery."""
import argparse
import json
import os
from pathlib import Path
import shutil
import sys
from types import SimpleNamespace

from benchmark import fixtures
from cohort import publish

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c10"))
from read_frontier import replay
from transfer import KEY, SECRET, allocated, run, serve, verified


def probe(args, env, scope, emit):
    entries = fixtures(args.corpus, args.manifest, scope)
    modes = ("deflate", "prefix", "prefix-paid", "block8", "block")
    for lane in modes:
        root = args.work / scope / lane
        root.mkdir(parents=True)
        geometry = publish(root, scope, lane, entries)
        for budget in args.cache_budgets:
            for repeat in range(3):
                tested = run(args.lab, {**env, "C13_PACK": str(root), "C13_SCOPE": scope,
                                        "C13_CORPUS": str(args.corpus), "C13_CACHE": str(budget)},
                             ["-test.run", "^TestC13NativeCohortReads$", "-test.v", "-test.timeout", "40s"], 45)
                for line in tested["stdout"].splitlines():
                    if line.startswith("C13READ "):
                        emit({"lane": lane, "repeat": repeat, "geometry": geometry, **json.loads(line[8:])})


def press(args, env, scope, emit):
    files = {key: args.corpus / scope / key for key in fixtures(args.corpus, args.manifest, scope)}
    lanes = {"control": (0, False), "native32": (32 << 10, False),
             "native8": (8 << 10, False), "priced32": (32 << 10, True)}
    for lane, (cap, gate) in lanes.items():
        work = args.work / scope / lane
        store = work / "store"
        work.mkdir(parents=True)
        shutil.copytree(args.seeds / scope / "cdc" / "store", store)
        planner = {}
        if cap:
            planner = run(args.lab, {**env, "C13_STORE": str(store), "C13_INPUT": str(args.corpus/scope),
                                     "C13_CAP": str(cap), "C13_GATE": str(int(gate))},
                          ["-test.run", "^TestC13NativeStoreGroups$", "-test.v", "-test.timeout", "90s"], 95)
            payload = next(line[9:] for line in planner.pop("stdout").splitlines() if line.startswith("C13GROUP "))
            planner.update(json.loads(payload))
        for flags in (["gc", "-apply", "-json"], ["repack", "-apply", "-recompress", "-max-live-percent", "100", "-json"],
                      ["compact", "-pack-size-mib", "1", "-json"], ["gc", "-apply", "-json"],
                      ["verify", "-deep", "-json"]):
            run(args.binary, env, [*flags, "-store", str(store)], 60)
        identity = {}
        with serve(args.binary, store, work/"server.log", env) as (_, client, _):
            for key, source in files.items():
                identity[key] = verified(client, key, source)
        (work / "fixtures.json").write_text(json.dumps(identity))
        emit({"scope": scope, "lane": lane, "stage": "verified", "objects": len(identity),
              "allocated": allocated(store), "planner": planner})
    measured = SimpleNamespace(corpus=args.corpus, work=args.work, lanes=list(lanes), baseline=args.binary,
                               candidate=None, repeats=1, sizes=[4096], widths=[1, 8],
                               samples=192, full_samples=192)

    def output(row):
        row["build"] = "native-cohort-feasibility"
        emit(row)
    replay(measured, env, scope, output)


def geometry(args, env, scope, emit):
    args.work.mkdir(parents=True, exist_ok=True)
    for repeat in range(3):
        caps = (0, 8192, 32768) if repeat % 2 == 0 else (32768, 8192, 0)
        for cap in caps:
            path = args.work / f"{scope}-{repeat}-{cap}.pack"
            result = run(args.lab, {**env, "C13_SHADOW": str(path), "C13_INPUT": str(args.corpus/scope),
                                    "C13_CAP": str(cap)},
                         ["-test.run", "^TestC13ShadowPack$", "-test.v", "-test.timeout", "40s"], 45)
            for line in result["stdout"].splitlines():
                if line.startswith("C13SHADOW "):
                    emit({"scope": scope, "repeat": repeat, "allocated_bytes": path.stat().st_blocks*512,
                          **json.loads(line[10:])})


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("work", "corpus", "manifest", "lab", "binary", "seeds", "results"):
        p.add_argument("--"+n, type=lambda name: Path(name).resolve(), required=n not in ("binary", "seeds"))
    phases = {"reads": probe, "stores": press, "geometry": geometry}
    p.add_argument("--phase", choices=phases, required=True)
    p.add_argument("--scopes", choices=("main", "heldout", "wide"), nargs="+", default=("main", "heldout"))
    p.add_argument("--cache-budgets", type=int, nargs="+", default=[0])
    args = p.parse_args()
    if args.phase == "stores" and (args.binary is None or args.seeds is None):
        p.error("stores needs --binary and --seeds")
    if args.phase == "stores" and "wide" in args.scopes:
        p.error("the wide panel is for the isolated reader and pack geometry")
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        def emit(row):
            print(json.dumps(row), file=out, flush=True)
        for scope in args.scopes:
            phases[args.phase](args, env, scope, emit)


if __name__ == "__main__":
    main()
