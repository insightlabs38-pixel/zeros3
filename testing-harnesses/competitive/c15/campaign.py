"""Benchmark alternate index layouts inside the same native object-store workflow."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from hashlib import sha256
import json
import os
from pathlib import Path
import random
import shutil
import subprocess
import sys
from threading import Event
import time

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/path) for path in ("c7", "c3", "c2", "c1")]
from incremental import FINISH, OPT, input_rows, verify_reads, write
from frontier import inventory
from local_s3 import measure
from transfer import BUCKET, KEY, SECRET, run, serve


def case_input(args, family, work):
    if family == "mixed-format":
        rels = ("typescript-gzip/0/package.tgz", "typescript-tar/1/package.tar",
                "typescript-gzip/2/package.tgz")
        return {f"revision/{i:03}": (args.c12/rel).read_bytes() for i, rel in enumerate(rels)}, {}
    if family == "edits32":
        rng = random.Random(15151)
        body = rng.randbytes(256<<10)
        rows = {}
        for index in range(32):
            rows[f"revision/{index:03}"] = body
            at = rng.randrange(len(body)-(8<<10))
            body = body[:at]+rng.randbytes(8192)+body[at+8192:]
        return rows, {}
    noise = int(family[6:])
    return input_rows(args, "binary", work), {
        f"unrelated/{i:05}": random.Random(10000+i).randbytes(4096) for i in range(noise)
    }


def flags_for(family):
    if family == "mixed-format":
        return [f for f in OPT if f != "-no-rep"]+["-canonical", "-codec-replay"]
    return OPT


def measurements(binary, env, store, flags, targets=()):
    row = run(binary, env, [*flags, "-store", str(store), *targets], 120)
    row["report"] = json.loads(row.pop("stdout"))
    all_bytes = inventory(store)
    row["index_allocated"] = all_bytes.get("similarity-index", 0)
    row["total_allocated"] = sum(all_bytes.values())
    row["retained_excluding_index"] = row["total_allocated"]-row["index_allocated"]
    if "directory" in row["report"] and "runs" in row["report"]:
        current = Path(row["report"]["directory"])
        row["run_physical_sizes"] = [(current/r["name"]).stat().st_blocks*512 for r in row["report"]["runs"]]
        row["run_logical_sizes"] = [(current/r["name"]).stat().st_size for r in row["report"]["runs"]]
    return row


def online(binary, baseline, env, root, store, paths):
    with serve(binary, store, root/"online.log", env) as (_, api, _):
        older = subprocess.run([baseline, "authority", "index-status", "-store", store],
                               env=env, text=True, capture_output=True, timeout=10)
        if not older.returncode or "store" not in older.stderr:
            raise ValueError("old offline index status did not respect the active writer")
        keys = sorted(paths)
        requests = [(keys[i % len(keys)],) for i in range(360)]

        def get(key):
            with api.get_object(Bucket=BUCKET, Key=key, Range="bytes=0-4095")["Body"] as source:
                got = source.read()
            if got != paths[key][:4096]:
                raise ValueError(f"online signed read differs at {key}")
            return len(got)

        def interrogate(started):
            times = []
            started.set()
            for _ in range(12):
                before = time.monotonic()
                report = run(binary, env, ["authority", "index-status", "-store", str(store)], 20)
                info = json.loads(report["stdout"])
                if not info["objects"]:
                    raise ValueError("online status did not inspect an active index")
                times.append(time.monotonic()-before)
            return times

        with ThreadPoolExecutor(max_workers=4) as pool, ThreadPoolExecutor(max_workers=1) as status:
            plain = measure(pool, requests, get)
            flag = Event()
            due = status.submit(interrogate, flag)
            if not flag.wait(5):
                raise ValueError("online query worker did not start")
            concurrent = measure(pool, requests, get)
            durations = due.result()
        api.put_object(Bucket=BUCKET, Key="live-after-fence", Body=b"new visible content")
        stale = subprocess.run([binary, "authority", "index-status", "-store", store],
                               env=env, text=True, capture_output=True, timeout=10)
        if stale.returncode == 0 or "stale" not in stale.stderr:
            raise ValueError("strict shared status accepted a stale generation")
        return {"baseline_get": plain, "index_status_get": concurrent, "index_status_seconds": durations,
                "old_status_fail": older.returncode, "new_status_stale": stale.returncode}


def trial(args, env, family, lane, root, store, binary, writes, expected, emit):
    def action(stage, options, targets=(), **tags):
        row = measurements(binary, env, store, options, targets)
        emit({"family": family, "lane": lane, "stage": stage, **tags, **row})
        return row

    action("index-build", ["authority", "index-build", "-apply"])
    for i, (key, data) in enumerate(writes[:-1]):
        write(binary, store, root/"serve.log", env, {key: data})
        action("index-edit-refresh", ["authority", "index-refresh", "-apply"], edit=i+1)
    key, data = writes[-1]
    write(binary, store, root/"serve.log", env, {key: data})
    report = action("latest", [*flags_for(family), "-max-targets", "1", "-indexed-discovery"])
    for label, flags in FINISH:
        action(label, flags)
    verified = action("verify", ["verify", "-deep", "-json"])
    count = verify_reads(binary, store, root/"serve.log", env, expected)
    emit({"family": family, "lane": lane, "stage": "exact", "latest_sha256": sha256(data).hexdigest(),
          "verified_objects": count, "plan_bytes": report["report"]["planned"]["bytes"],
          "affected": report["report"]["applied"], "retained_excluding_index": verified["retained_excluding_index"],
          "total_allocated": verified["total_allocated"], "index_allocated": verified["index_allocated"]})
    action("index-append", ["authority", "index-refresh", "-apply"])
    if family != "mixed-format":
        victim = "unrelated/00000" if family.startswith("binary") else "revision/000"
        with serve(binary, store, root/"serve.log", env) as (_, api, _):
            api.delete_object(Bucket=BUCKET, Key=victim)
        action("prune-namespace", ["versions", "prune", "-apply", "-json", "-bucket", BUCKET,
                                   "-prefix", victim, "-keep-last", "0"])
        action("index-tombstone", ["authority", "index-refresh", "-apply"])
    status = action("index-status", ["authority", "index-status"])
    action("index-inventory", ["authority", "index-inspect"])
    old = action("prune-dry-run", ["authority", "index-prune"])["report"].get("candidates", [])
    if old:
        action("prune-inactive", ["authority", "index-prune", "-apply"], old)
    if lane == "compact" and family == "binary64":
        old_read = verify_reads(args.baseline, store, root/"legacy.log", env,
                                {k: v for k, v in expected.items() if k != victim})
        fallback_opts = [f for f in flags_for(family) if f != "-apply"]
        fallback = subprocess.run([args.baseline, *fallback_opts, "-max-targets", "1", "-indexed-discovery",
                                   "-store", store], env=env, check=True, text=True, capture_output=True, timeout=40)
        if not json.loads(fallback.stdout)["discovery"].get("index_fallback"):
            raise ValueError("older engine did not fall back on a new compact run")
        all_vis = {k: v for k, v in expected.items() if k != victim}
        rows = online(binary, args.baseline, env, root, store, all_vis)
        emit({"family": family, "lane": lane, "stage": "online", "legacy_exact": old_read,
              **rows})
    if lane == "control":
        # Try a compact append to a cloned legacy active generation.
        candidate = root/"mixed-generation"
        shutil.copytree(store, candidate)
        mixed_body = random.Random(15159).randbytes(32768)
        write(args.candidate, candidate, root/"mixed.log", env, {"mixed-generation/append": mixed_body})
        appended = measurements(args.candidate, env, candidate, ["authority", "index-refresh", "-apply"])
        gen = Path(appended["report"]["directory"])
        formats = sorted({(gen/r["name"]).read_bytes()[:4].decode() for r in appended["report"]["runs"]})
        if formats != ["ZSR1", "ZSR2"] or appended["report"]["generation"] != status["report"]["generation"]:
            raise ValueError(f"old run was rebuilt rather than reused: {formats}")
        emit({"family": family, "lane": "mixed-generation", "stage": "append",
              "formats": formats, **appended})


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for n in ("baseline", "candidate", "corpus", "c12", "work", "results"):
        p.add_argument("--"+n, type=lambda name: Path(name).resolve(), required=True)
    p.add_argument("--families", nargs="+", choices=("binary64", "binary1024", "edits32", "mixed-format"),
                   default=("binary64", "binary1024", "edits32", "mixed-format"))
    args = p.parse_args()
    args.work.mkdir(parents=True)
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET,
           "AWS_REGION": "us-east-1", "GOMAXPROCS": "2"}
    engines = {"control": args.baseline, "compact": args.candidate}
    with args.results.open("x") as out:
        for switch, family in enumerate(args.families):
            root = args.work/family
            root.mkdir()
            history, noise = case_input(args, family, root)
            prefix = 8 if family == "edits32" else len(history)-1
            old = dict(list(history.items())[:prefix])
            future = list(history.items())[prefix:]
            seed = root/"seed"
            write(args.baseline, seed, root/"server.log", env, {**noise, **old}, True)
            prep = [*flags_for(family), "-max-targets", "4"]
            measurements(args.baseline, env, seed, prep)
            for _, flags in FINISH:
                measurements(args.baseline, env, seed, flags)
            for lane in tuple(engines)[::1 if switch % 2 == 0 else -1]:
                d = root/lane
                d.mkdir()
                store = d/"store"
                shutil.copytree(seed, store)
                trial(args, env, family, lane, d, store, engines[lane], future, {**noise, **history},
                      lambda row: print(json.dumps(row), file=out, flush=True))


if __name__ == "__main__":
    main()
