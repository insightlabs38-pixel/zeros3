"""Run P8 measurements and publish rows only after the entire run succeeds."""

import argparse
from collections import Counter
import json
import math
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
from tempfile import TemporaryDirectory


def csv_list(value):
    entries = value.split(",")
    if any(not entry.strip() for entry in entries) or len(set(entries)) != len(entries):
        raise argparse.ArgumentTypeError("expected a comma-separated list of distinct values")
    return entries


def positive(value):
    number = int(value)
    if number < 1:
        raise argparse.ArgumentTypeError("must be positive")
    return number


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("bin", type=Path)
    parser.add_argument("outdir", type=Path)
    parser.add_argument("tag")
    modes = parser.add_subparsers(dest="mode", required=True)
    for mode, option, default in (
        ("migrate", "variants", 3),
        ("matcher", "matchers", None),
        ("rep", None, 2),
    ):
        command = modes.add_parser(mode)
        command.add_argument("families" if option else "files", type=csv_list)
        if option:
            command.add_argument(option, type=csv_list)
        if default is not None:
            command.add_argument("reps", nargs="?", type=positive, default=default)
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_-][A-Za-z0-9_.-]*", args.tag):
        parser.error("invalid output tag")
    if not args.bin.is_file() or not os.access(args.bin, os.X_OK):
        parser.error("BIN must be an executable file")
    if args.mode != "rep":
        if any(not re.fullmatch(r"[a-z0-9-]+", family) for family in args.families):
            parser.error("invalid corpus family")
        options, allowed = (
            (args.variants, {"delta", "archive", "rep"})
            if args.mode == "migrate"
            else (args.matchers, {"xd", "dw"})
        )
        if set(options) - allowed:
            parser.error(f"expected values from {', '.join(sorted(allowed))}")
    else:
        for filename in args.files:
            path = Path(filename)
            if not path.is_file() or not path.stat().st_size or not os.access(path, os.R_OK):
                parser.error(f"unreadable or empty input: {path}")
    return args


def invalid_constant(value):
    raise ValueError(f"invalid JSON constant {value}")


def validate(path, args, family):
    raw = path.read_bytes()
    if not raw or not raw.endswith(b"\n"):
        raise ValueError("missing or unterminated benchmark rows")
    seen = Counter()
    matcher_pairs = set()
    for line in raw.splitlines():
        row = json.loads(line, parse_constant=invalid_constant)
        if not isinstance(row, dict) or row.get("p8") != args.mode or row.get("exact") is not True:
            raise ValueError("unexpected mode or failed exactness")
        if family is not None and row.get("family") != family:
            raise ValueError("unexpected corpus family")
        if args.mode == "matcher":
            pair = (row["base"], row["target"])
            if not all(isinstance(value, str) and value for value in pair):
                raise ValueError("missing matcher input identity")
            matcher_pairs.add(pair)
            key = (*pair, row["matcher"])
            fields = ("tgt_mib", "match_ms", "mibps", "ops", "envelope")
        else:
            if type(row.get("rep")) is not int:
                raise ValueError("invalid repetition number")
            if args.mode == "migrate":
                key = (row["variant"], row["rep"])
                fields = ("logical", "objects", "cdc_phys", "wall_s", "cpu_s", "rss_mib", "phys_after")
                if row["logical"] <= 0 or row["objects"] < 2:
                    raise ValueError("incomplete migration corpus")
            else:
                key = (row["file"], row["rep"])
                fields = ("bytes", "build_s", "cpu_s", "rss_mib", "rep_phys", "replay_s")
                if row.get("err") != "<nil>" or row["bytes"] <= 0:
                    raise ValueError("failed representation build")
        if any(type(row.get(field)) not in (int, float) or not math.isfinite(row[field]) for field in fields):
            raise ValueError("missing or invalid measurement field")
        seen[key] += 1

    if args.mode == "matcher":
        expected = Counter((*pair, m) for pair in matcher_pairs for m in args.matchers)
    elif args.mode == "migrate":
        expected = Counter((variant, r) for variant in args.variants for r in range(args.reps))
    else:
        expected = Counter((Path(f).name, r) for f in args.files for r in range(args.reps))
    if seen != expected:
        raise ValueError("missing, duplicate, or unexpected benchmark rows")
    return raw


def child_run(command, env, log, timeout):
    with log.open("xb") as output, subprocess.Popen(
        command, env=env, stdout=output, stderr=subprocess.STDOUT, start_new_session=True
    ) as child:
        try:
            status = child.wait(timeout)
        except BaseException:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()
            raise
    if status:
        raise ValueError(f"child exited with status {status}")
    if not log.read_bytes().splitlines()[-1:] == [b"PASS"]:
        raise ValueError("missing Go test PASS trailer")


class Interrupted(Exception):
    pass


def interrupted(signum, _frame):
    raise Interrupted(signum)


def main():
    args = parse_args()
    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, interrupted)
    try:
        timeout = positive(os.environ.get("ZEROS3_P8_TIMEOUT_SECONDS", "10800"))
        base_env = os.environ.copy()
        base_env.pop("ZEROS3_P8_FAMILY", None)
        base_env.pop("ZEROS3_P8_PROF", None)
        corpus = Path(base_env.get("ZEROS3_B2_CORPUS") or "/home/user/p8corpus/econ")
        if args.mode != "rep" and not corpus.is_dir():
            raise ValueError(f"corpus directory does not exist: {corpus}")
        base_env["ZEROS3_B2_CORPUS"] = str(corpus)
        args.outdir.mkdir(parents=True, exist_ok=True)
        logs = args.outdir / args.tag
        final = args.outdir / f"{args.tag}.jsonl"
        if os.path.lexists(final):
            raise ValueError(f"output already exists: {final}")
        logs.mkdir()
        with TemporaryDirectory(prefix=f".{args.tag}.", dir=args.outdir) as scratch:
            staged = Path(scratch) / "all.jsonl"
            failed = False
            for index, family in enumerate(args.families if args.mode != "rep" else [None]):
                rows = Path(scratch) / f"rows-{index}.jsonl"
                env = dict(base_env, ZEROS3_B2_OUT=str(rows))
                command = [str(args.bin.resolve()), "-test.count=1", "-test.timeout=3h"]
                if args.mode == "migrate":
                    env.update(ZEROS3_P8_FAMILY=family, ZEROS3_P8_VARIANTS=",".join(args.variants),
                               ZEROS3_P8_REPS=str(args.reps), ZEROS3_P8_PROF=str(logs))
                    command += ["-test.run=^TestP8Migration$"]
                    log = logs / f"{family}.log"
                elif args.mode == "matcher":
                    env.update(ZEROS3_P8_FAMILY=family, ZEROS3_P8_MATCHERS=",".join(args.matchers))
                    command += ["-test.run=^TestP8Matcher$", "-test.v"]
                    log = logs / f"matcher-{family}.log"
                else:
                    env.update(ZEROS3_P8_FILES=",".join(args.files), ZEROS3_P8_REPS=str(args.reps))
                    command += ["-test.run=^TestP8Rep$", "-test.v", "-test.cpuprofile", str(logs / "rep.pprof")]
                    log = logs / "rep.log"
                try:
                    child_run(command, env, log, timeout)
                    with staged.open("ab") as output:
                        output.write(validate(rows, args, family))
                except (OSError, ValueError, KeyError, TypeError, OverflowError, subprocess.TimeoutExpired) as exc:
                    print(f"{log}: {exc}", file=sys.stderr)
                    failed = True
            if failed:
                return 1
            os.chmod(staged, stat.S_IRUSR | stat.S_IWUSR | stat.S_IRGRP | stat.S_IROTH)
            os.link(staged, final)
    except Interrupted as exc:
        return 128 + exc.args[0]
    except (OSError, ValueError, argparse.ArgumentTypeError) as exc:
        print(exc, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
