"""Measure byte-identical XD re-encoding through the real optimizer apply path."""
import argparse
import json
import os
from pathlib import Path
import random
import shutil
import sys

here = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(here/"c3"))
from transfer import BUCKET, KEY, SECRET, allocated, put, run, serve, verified


FAMILIES = ("shift", "sqlite", "volume", "events", "typescript-tar", "httpx-heldout", "entropy",
            "mixed-edits", "windowed", "windowed-churn", "budget-stress")
GENERATED = ("mixed-edits", "windowed", "windowed-churn", "budget-stress")
POLICIES = {"control": [], "fast": ["-fast-reencode"], "retained": ["-retain-delta-plans"]}


def edits(root):
    stable = random.Random(12811).randbytes(2 << 20)
    for i in range(24):
        rng = random.Random(12827+i)
        at = (3 + i % 10) * (128 << 10)
        if i % 4 == 0:
            data = stable
        elif i % 4 == 1:
            data = stable[:at] + rng.randbytes(8192) + stable[at:]
        elif i % 4 == 2:
            data = stable[:at] + stable[at+12288:]
        else:
            data = stable[:at] + rng.randbytes(6144) + stable[at+4096:(at+524288)] + rng.randbytes(4096) + stable[at+528384:]
        path = root / str(i).zfill(2) / "payload.bin"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)


def windowed(root, churn=False):
    stable = random.Random(12913).randbytes(33 << 20)
    if churn:
        revised = bytearray(stable)
        for at in range(1729, len(revised), 64 << 10):
            revised[at] ^= 0x5a
    else:
        edit = random.Random(12917).randbytes(11 << 10)
        revised = stable[:3 << 20] + edit + stable[3 << 20:]
    for revision, data in enumerate((stable, revised)):
        path = root / str(revision) / "data.bin"
        path.parent.mkdir(parents=True)
        path.write_bytes(data)


def budget_stress(root):
    stable = random.Random(12924).randbytes(2 << 20)
    offset, span = 896 << 10, 192 << 10
    for revision in range(20):
        data = stable[:offset] + random.Random(12925+revision).randbytes(span) + stable[offset+span:]
        path = root / str(revision).zfill(2) / "payload.bin"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)


def files(args, family):
    root = (args.work if family in GENERATED else args.corpus) / family
    return {str(p.relative_to(root)): p for p in sorted(root.rglob("*")) if p.is_file()}


def source(args, env, family):
    work = args.work / "seeds" / family
    work.mkdir(parents=True)
    with serve(args.binary, work / "store", work / "source.log", env) as (_, client, _):
        client.create_bucket(Bucket=BUCKET)
        for key, path in files(args, family).items():
            put(client, key, path)
    return work / "store"


def trial(args, env, family, seed, lane, pair):
    root = args.work / f"trial-{family}-{pair}-{lane}"
    root.mkdir()
    store = root / "store"
    shutil.copytree(seed, store)
    command = ["authority", "optimize", "-apply", "-record-groups", "-profile", "archive",
               "-order", "oldest", "-max-depth", "2", "-max-bases", "1", "-min-saving", ".10",
               "-store", str(store)]
    command.extend(POLICIES[lane])
    measured = run(args.binary, env, command, 180)
    report = json.loads(measured.pop("stdout"))
    if report["failed"]:
        raise ValueError(f"optimizer apply failed: {report['changes']}")
    for flags in (["gc", "-apply", "-json"], ["repack", "-apply", "-recompress", "-max-live-percent", "100", "-json"],
                  ["compact", "-pack-size-mib", "1", "-json"], ["gc", "-apply", "-json"],
                  ["verify", "-deep", "-json"]):
        run(args.binary, env, [*flags, "-store", str(store)], 90)
    exact = {}
    with serve(args.binary, store, root/"receiver.log", env) as (_, client, _):
        for key, path in files(args, family).items():
            exact[key] = verified(client, key, path)
    row = {"family": family, "pair": pair, "lane": lane, "allocated": allocated(store),
           "identity": exact, "measure": measured, "optimizer": report}
    shutil.rmtree(store)
    return row


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for key in ("binary", "work", "corpus", "results"):
        p.add_argument("--"+key, type=lambda x: Path(x).resolve(), required=True)
    p.add_argument("--families", nargs="+", choices=FAMILIES, default=FAMILIES)
    p.add_argument("--lanes", nargs="+", choices=POLICIES, default=list(POLICIES))
    p.add_argument("--repeats", type=int, default=2)
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    if "mixed-edits" in args.families:
        edits(args.work/"mixed-edits")
    if "windowed" in args.families:
        windowed(args.work/"windowed")
    if "windowed-churn" in args.families:
        windowed(args.work/"windowed-churn", churn=True)
    if "budget-stress" in args.families:
        budget_stress(args.work/"budget-stress")
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for family in args.families:
            seed = source(args, env, family)
            for pair in range(args.repeats):
                lanes = args.lanes if pair % 2 == 0 else args.lanes[::-1]
                observed = []
                for lane in lanes:
                    row = trial(args, env, family, seed, lane, pair)
                    observed.append(row)
                    print(json.dumps(row), file=out, flush=True)
                control = observed[0]
                for row in observed[1:]:
                    if (control["allocated"], control["identity"], control["optimizer"].get("changes", [])) != (
                            row["allocated"], row["identity"], row["optimizer"].get("changes", [])):
                        raise ValueError("apply differed from the incumbent plan or store")


if __name__ == "__main__":
    main()
