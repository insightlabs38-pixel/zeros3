"""Measure incremental filesystem snapshots and verified full restores."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import resource
import shutil
import subprocess
import time

from codec_corpus import FAMILIES as CODEC_FAMILIES
from corpus import FAMILIES
from external_corpus import FAMILIES as EXTERNAL_FAMILIES
from local_s3 import allocation

SYSTEMS = ("restic", "borg", "kopia")


def digest_tree(path):
    return {p.relative_to(path).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in path.rglob("*") if p.is_file()}


def run_test(system, work, run, family, compression):
    run.mkdir(parents=True)
    store = run / "store"
    source = run / "source"
    target = run / "restore"
    cache = run / "cache"
    env = {**os.environ, "GOMAXPROCS": "2", "TMPDIR": str(run)}
    env.update(RESTIC_REPOSITORY=str(store), RESTIC_PASSWORD="disposable-c1",
               RESTIC_CACHE_DIR=str(cache), BORG_PASSPHRASE="disposable-c1",
               BORG_BASE_DIR=str(cache), BORG_HOST_ID="c1@local",
               BORG_RELOCATED_REPO_ACCESS_IS_OK="yes", KOPIA_PASSWORD="disposable-c1",
               KOPIA_CONFIG_PATH=str(run / "kopia.config"), KOPIA_CACHE_DIRECTORY=str(cache),
               KOPIA_LOG_DIR=str(run / "kopia-log"))
    binary = str(work / "bin" / system)

    def command(*args, cwd=run):
        began = time.perf_counter()
        before = resource.getrusage(resource.RUSAGE_CHILDREN)
        process = subprocess.run([binary, *args], cwd=cwd, env=env, timeout=180,
                                 capture_output=True, text=True)
        elapsed = time.perf_counter() - began
        after = resource.getrusage(resource.RUSAGE_CHILDREN)
        if process.returncode:
            raise RuntimeError(f"{system} {list(args)[:2]} exit {process.returncode}: "
                               f"{process.stderr[-1100:]}")
        return process.stdout, {
            "seconds": elapsed, "cpu_seconds": after.ru_utime + after.ru_stime
            - before.ru_utime - before.ru_stime,
            "allocated_bytes": allocation(store) if store.exists() else 0,
            "cache_allocated_bytes": allocation(cache) if cache.exists() else 0,
        }

    if system == "restic":
        _, metrics = command("init")
    elif system == "borg":
        _, metrics = command("init", "--encryption", "repokey-blake2", str(store))
    else:
        _, metrics = command("repository", "create", "filesystem", f"--path={store}")
        command("maintenance", "set", "--enable-quick=false", "--enable-full=false")
        if compression:
            command("policy", "set", "--global", f"--compression={compression}")
    yield {"phase": "init", **metrics}

    snapshots = []
    expected = []
    families = FAMILIES if family == "combined" else [family]
    for rev in range(3):
        if source.exists():
            shutil.rmtree(source)
        source.mkdir()
        for name in families:
            shutil.copytree(work / "corpus" / name / str(rev), source / name, copy_function=shutil.copy)
        expected.append(digest_tree(source))
        logical = sum(p.stat().st_size for p in source.rglob("*") if p.is_file())
        if system == "restic":
            text, metrics = command("backup", "--json", "--compression", "auto", str(source))
            current = next(json.loads(l)["snapshot_id"] for l in text.splitlines()
                           if json.loads(l).get("message_type") == "summary")
        elif system == "borg":
            current = f"r{rev}"
            _, metrics = command("create", "--compression", "lz4", f"{store}::{current}", "source")
        else:
            text, metrics = command("snapshot", "create", "--json", str(source))
            record = json.loads(text)
            current = (record["id"], record["rootEntry"]["obj"])
        snapshots.append(current)
        yield {"phase": "ingest", "revision": rev, "logical_bytes": logical, **metrics}

    if system == "restic":
        _, metrics = command("prune")
    elif system == "borg":
        _, metrics = command("compact", str(store))
    else:
        _, metrics = command("maintenance", "run", "--full")
    yield {"phase": "maintenance", **metrics}

    for rev, snap in enumerate(snapshots):
        if system == "restic":
            _, metrics = command("restore", snap, "--target", str(target))
            files = target / source.relative_to("/")
        elif system == "borg":
            target.mkdir()
            _, metrics = command("extract", f"{store}::{snap}", cwd=target)
            files = target / "source"
        else:
            _, metrics = command("snapshot", "restore", snap[1], str(target))
            files = target
        if digest_tree(files) != expected[rev]:
            raise ValueError(f"restore mismatch at revision {rev}")
        yield {"phase": "restore", "revision": rev, "verified_files": len(expected[rev]), **metrics}
        shutil.rmtree(target)

    if system == "restic":
        _, metrics = command("forget", snapshots[0], "--prune")
    elif system == "borg":
        command("delete", f"{store}::{snapshots[0]}")
        _, metrics = command("compact", str(store))
    else:
        command("snapshot", "delete", snapshots[0][0], "--delete")
        _, metrics = command("maintenance", "run", "--full")
    yield {"phase": "prune", **metrics}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("work", type=Path)
    parser.add_argument("result", type=Path)
    parser.add_argument("--systems", nargs="+", choices=SYSTEMS, default=SYSTEMS)
    parser.add_argument("--families", nargs="+", choices=(*FAMILIES, *EXTERNAL_FAMILIES, *CODEC_FAMILIES, "combined"), default=["combined"])
    parser.add_argument("--kopia-compression", choices=("zstd-fastest",))
    parser.add_argument("--repetitions", type=int, choices=(1, 2, 3), default=3)
    args = parser.parse_args()
    with args.result.open("x", buffering=1) as out:
        for rep in range(args.repetitions):
            for system in (*args.systems[rep:], *args.systems[:rep]):
                for family in args.families:
                    ids = {"system": system, "repetition": rep, "family": family}
                    if system == "kopia" and args.kopia_compression:
                        ids["profile"] = args.kopia_compression
                    run = args.work / "runs" / f"{args.result.stem}-{rep}-{system}-{family}"
                    try:
                        for row in run_test(system, args.work, run, family, args.kopia_compression):
                            print(json.dumps({**ids, **row}), file=out)
                    except Exception as error:
                        print(json.dumps({**ids, "phase": "error", "error": str(error)}), file=out)
                        print(f"{run.name}: {error}", flush=True)
                    finally:
                        if run.exists():
                            shutil.rmtree(run)


if __name__ == "__main__":
    main()
