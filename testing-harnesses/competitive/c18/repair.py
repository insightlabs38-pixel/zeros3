"""Compare local preset repair with ordinary re-encoding on a disposable pack."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from hashlib import sha256
import json
import os
from pathlib import Path
import random
import shutil
import struct
import subprocess
import sys

harness = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(harness/sub) for sub in ("c1", "c3")]
from local_s3 import measure
from transfer import BUCKET, KEY, SECRET, allocated, file_digest, run, serve


def preset_records(store):
    for path in sorted((store/"packs").glob("*.pack")):
        with path.open("rb") as f:
            f.seek(-64, 2)
            foot = f.read(64)
            count, index = struct.unpack_from("<QQ", foot, 8)
            if not 0 < count < 100000:
                raise ValueError("unexpected repair pilot pack record count")
            f.seek(index)
            first = f.read(52)
            if first[48] == 5:
                f.seek(index+(count-1)*52)
                last = f.read(52)
                if last[48] != 5:
                    raise ValueError("backup context missing")
                a, b = (struct.unpack_from("<Q", data, 32)[0] for data in (first, last))
                return path, a, b
    raise ValueError("the frozen pilot store has no pack-local preset")


def flip(path, offset):
    with path.open("r+b") as f:
        f.seek(offset)
        previous = f.read(1)
        f.seek(offset)
        f.write(bytes([previous[0]^64]))
        f.flush()
        os.fsync(f.fileno())


def observe(args, store, log, env, check_lock=False):
    rng = random.Random(18183)
    exams = rng.sample(args.entries, 96)
    with serve(args.binary, store, log, env) as (_, api, _):
        def read(row):
            with api.get_object(Bucket=BUCKET, Key=row["key"])["Body"] as body:
                data = body.read()
            if len(data) != row["bytes"] or sha256(data).hexdigest() != row["sha256"]:
                raise ValueError(f"repaired object differs at {row['key']}")
            return len(data)
        if check_lock:
            proc = subprocess.run([args.binary, "doctor", "-repair-pack-dictionaries", "-store", store],
                                  env=env, text=True, capture_output=True, timeout=10)
            if not proc.returncode or "exclusive access" not in proc.stderr:
                raise ValueError("repair did not respect the serving lock")
        with ThreadPoolExecutor(max_workers=4) as pool:
            return measure(pool, [(r,) for r in exams], read)


def operation(binary, env, store, *args):
    result = run(binary, env, [*args, "-store", str(store)], 60, separate_stderr=True)
    result["report"] = json.loads(result.pop("stdout"))
    return result


def trial(args, env, pair, lane, emit):
    root = args.work/f"{pair}-{lane}"
    root.mkdir()
    store = root/"store"
    shutil.copytree(args.seed, store)
    path, primary, backup = preset_records(store)
    original = file_digest(path)
    copied = path.stat().st_size
    flip(path, (primary if pair % 2 == 0 else backup)+305)
    broken = file_digest(path)
    if broken == original:
        raise ValueError("fault injection did not change the pack")
    old = subprocess.run([args.baseline, "verify", "-deep", "-json", "-store", store],
                         env=env, capture_output=True, text=True, timeout=20)
    check = subprocess.run([args.binary, "verify", "-deep", "-json", "-store", store],
                           env=env, capture_output=True, text=True, timeout=20)
    if check.returncode == 0 or "preset context" not in check.stdout:
        raise ValueError("new deep verify did not report the damaged context")
    before = observe(args, store, root/"before.log", env, check_lock=lane == "targeted")
    if lane == "targeted":
        steps = [operation(args.binary, env, store, "doctor", "-deep", "-json", "-repair-pack-dictionaries")]
        report = steps[0]["report"]["pack_dictionary_repairs"]
        if len(report) != 1 or report[0]["rewritten_bytes"] != copied or file_digest(path) != original:
            raise ValueError("targeted repair changed the original pack identity")
    else:
        extra = ("-preset-dictionary", str(args.dictionary)) if lane == "forced-repack" else ()
        steps = [operation(args.baseline, env, store, *step) for step in (
            ("repack", "-apply", "-recompress", "-max-live-percent", "100", "-json", *extra),
            ("gc", "-apply", "-json"),
            ("verify", "-deep", "-json"),
        )]
    health = subprocess.run([args.binary, "verify", "-deep", "-json", "-store", store],
                            env=env, capture_output=True, text=True, timeout=20)
    if lane != "repack" and health.returncode:
        raise ValueError(f"{lane} did not fully restore the store: {health.stdout}")
    final = observe(args, store, root/"after.log", env)
    emit({"pair": pair, "lane": lane, "damaged_copy": "primary" if pair % 2 == 0 else "backup",
          "expected_pack_sha256": original, "faulted_pack_sha256": broken,
          "previous_verify_detected": old.returncode != 0,
          "current_verify_issues": json.loads(check.stdout)["issues"],
          "healthy_after": health.returncode == 0,
          "original_pack_preserved": path.is_file() and file_digest(path)==original,
          "allocated": allocated(store), "operations": steps, "before": before, "after": final,
          "seconds": sum(m["seconds"] for m in steps), "cpu_seconds": sum(m["cpu_seconds"] for m in steps),
          "output_blocks": sum(m["output_blocks"] for m in steps)})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("binary", "baseline", "seed", "manifest", "work", "results"):
        parser.add_argument("--"+arg, type=lambda name: Path(name).resolve(), required=True)
    parser.add_argument("--dictionary", type=Path)
    parser.add_argument("--lanes", nargs="+", choices=("targeted", "repack", "forced-repack"),
                        default=("targeted", "repack"))
    args = parser.parse_args()
    if "forced-repack" in args.lanes and args.dictionary is None:
        parser.error("forced repack requires the original preset file")
    args.work.mkdir(parents=True)
    args.entries = [r for r in map(json.loads, args.manifest.read_text().splitlines()) if r["scope"] == "wide"]
    env = {**os.environ, "AWS_ACCESS_KEY_ID": KEY, "AWS_SECRET_ACCESS_KEY": SECRET, "GOMAXPROCS": "2"}
    with args.results.open("x") as out:
        for pair in (0, 1):
            for lane in args.lanes[::1 if pair == 0 else -1]:
                trial(args, env, pair, lane, lambda row: print(json.dumps(row), file=out, flush=True))


if __name__ == "__main__":
    main()
