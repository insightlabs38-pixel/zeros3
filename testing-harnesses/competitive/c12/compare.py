"""Run exact native and independent delta encoders with one known base."""
import argparse
import json
import os
from pathlib import Path
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c3"))
from transfer import file_digest, run


METHODS = ("xd", "xor", "packed", "xdelta3", "bsdiff", "zstd")
LAB = METHODS[:3]


def allocated(path):
    with path.open("rb") as f:
        os.fsync(f.fileno())
    return path.stat().st_blocks * 512


def native_lab(args, method, env, action):
    return run(args.lab, {**env, "C12_METHOD": method, "C12_ACTION": action},
               ["-test.run", "^TestC12Process$", "-test.v", "-test.timeout", "90s"], 95)


def external(args, method, env, encode, base, target, program, output):
    if method == "xdelta3":
        binary = args.tools / "xdelta3"
        flags = ["-e" if encode else "-d", "-f", "-s", str(base)]
        if encode:
            flags += ["-9", str(target), str(program)]
        else:
            flags += [str(program), str(output)]
    elif method == "bsdiff":
        binary = args.tools / ("bsdiff" if encode else "bspatch")
        flags = [str(base), str(target if encode else output), str(program)]
    else:
        binary = Path("zstd")
        flags = ["--quiet", "--force", f"--patch-from={base}"]
        flags += ["-9", str(target), "-o", str(program)] if encode else ["-d", str(program), "-o", str(output)]
    return run(binary, env, flags, 90)


def measure(args, env, method, family, pair, base, target, work):
    program, output = work / "delta", work / "restored"
    program.unlink(missing_ok=True)
    output.unlink(missing_ok=True)
    specs = {"C12_BASE": str(base), "C12_TARGET": str(target),
             "C12_PROGRAM": str(program), "C12_OUTPUT": str(output)}
    if method in LAB:
        encode = native_lab(args, method, {**env, **specs}, "encode")
        decode = native_lab(args, method, {**env, **specs}, "decode")
        ranged = native_lab(args, method, {**env, **specs}, "ranges")
        spans = next(line[4:] for line in ranged.pop("stdout").splitlines() if line.startswith("C12 "))
        ranged.update(json.loads(spans))
    else:
        encode = external(args, method, env, True, base, target, program, output)
        decode = external(args, method, env, False, base, target, program, output)
        ranged = None
    want = file_digest(target)
    if file_digest(output) != want:
        raise ValueError(f"{family} {method} reconstructed different bytes")
    encode.pop("stdout")
    decode.pop("stdout")
    input_bytes = base.stat().st_size + target.stat().st_size
    encode["cpu_seconds_per_gib"] = encode["cpu_seconds"] * (1 << 30) / input_bytes
    return {"family": family, "base_revision": pair, "target_revision": pair+1, "delta": method,
            "base_sha256": file_digest(base), "target_sha256": want, "input_bytes": input_bytes,
            "base_logical": base.stat().st_size, "base_allocated": allocated(base),
            "target_logical": target.stat().st_size, "target_allocated": allocated(target),
            "patch_logical": program.stat().st_size, "patch_allocated": allocated(program),
            "encode": encode, "decode": decode, "ranges": ranged}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for p in ("lab", "corpus", "tools", "work", "results"):
        parser.add_argument("--" + p, type=Path, required=True)
    parser.add_argument("--families", nargs="+", required=True)
    parser.add_argument("--methods", nargs="+", choices=METHODS, default=METHODS)
    parser.add_argument("--pairs", nargs="+", type=int, default=[0, 1])
    args = parser.parse_args()
    for n, v in vars(args).items():
        if isinstance(v, Path):
            setattr(args, n, v.resolve())
    args.work.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "GOMAXPROCS": "2"}
    with tempfile.TemporaryDirectory(dir=args.work) as directory, args.results.open("x") as out:
        work = Path(directory)
        for family in args.families:
            fixture = args.corpus / family
            for pair in args.pairs:
                files = [sorted((fixture / str(i)).glob("*")) for i in (pair, pair+1)]
                if not files[1] and pair > 0:
                    continue
                if any(len(group) != 1 for group in files):
                    raise ValueError(f"{family} needs exactly one object per revision")
                base, target = (group[0] for group in files)
                methods = args.methods[::-1] if pair % 2 else args.methods
                for method in methods:
                    if method == "bsdiff" and max(base.stat().st_size, target.stat().st_size) > 8 << 20:
                        continue
                    row = measure(args, env, method, family, pair, base, target, work)
                    print(json.dumps(row), file=out, flush=True)


if __name__ == "__main__":
    main()
