"""Replay exact page prototypes and verify their independent consumers."""
import argparse
import io
import json
import math
import os
from pathlib import Path
import random
import sys
import tempfile
import time
import zlib

from atlas import Atlas, BLK, SPAN, encode
from fixtures import digest, inspect_fs, query, sha

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c3"))
from transfer import run


class Reads:
    def __init__(self, handle):
        self.handle = handle
        self.total = 0

    def read(self, n):
        data = self.handle.read(n)
        self.total += len(data)
        return data

    def seek(self, *args):
        return self.handle.seek(*args)

    def tell(self):
        return self.handle.tell()


def ranges(program, basis, target):
    data = target.read_bytes()
    rng = random.Random(1414)
    times = []
    with program.open("rb") as prog, basis.open("rb") as base:
        p, b = Reads(prog), Reads(base)
        reader = Atlas(p, b)
        open_base, open_program = b.total, p.total
        for _ in range(128):
            at = rng.randrange(len(data)-BLK+1)
            start = time.perf_counter_ns()
            got = b"".join(reader.blocks(at, at+BLK))
            times.append(time.perf_counter_ns()-start)
            if got != data[at:at+BLK]:
                raise ValueError(f"page range differs at {at}")
    times.sort()
    return {"samples": len(times), "base_verify_bytes": open_base, "program_open_bytes": open_program,
            "base_read_bytes": b.total-open_base, "program_read_bytes": p.total-open_program,
            **{f"p{p}_ns": times[math.ceil(p*len(times)/100)-1] for p in (50, 95, 99)}}


def faults(program, basis):
    valid = program.read_bytes()
    base = basis.read_bytes()
    reader = Atlas(io.BytesIO(valid), io.BytesIO(base))
    row = len(reader.rows)//2
    offset, size, _ = reader.rows[row]
    altered = bytearray(valid)
    rng = random.Random(14341)
    altered[offset+rng.randrange(size)] ^= 32

    def fails(probe):
        try:
            probe()
        except (ValueError, zlib.error):
            return True
        raise ValueError("invalid atlas program or basis was accepted")

    def check_packet():
        reader = Atlas(io.BytesIO(altered), io.BytesIO(base))
        return next(reader.blocks(row*SPAN, min(reader.size, (row+1)*SPAN)))

    changed_base = bytes([base[0]^1])+base[1:]
    return {"frame_corruption_rejected": fails(check_packet),
            "truncated_table_rejected": fails(lambda: Atlas(io.BytesIO(valid[:65]), io.BytesIO(base))),
            "wrong_base_rejected": fails(lambda: Atlas(io.BytesIO(valid), io.BytesIO(changed_base)))}


def consumer(case, revision, output, args, work, proof):
    if case == "sqlite":
        row = proof["sqlite"]["versions"][revision]
        expected = {key: row[key] for key in ["rows", "query_sha256", "schema_sha256"]}
        observed = query(output)
        if observed != expected:
            raise ValueError("restored SQLite application state differs")
        return observed
    row = proof["volume"][revision]
    return {"verified_files": inspect_fs(output, row["offset"], row["sample"], work, args.fs_tools),
            "filesystem_offset": row["offset"]}


def atlas_trial(args, work, case, pair, mode, base, target, proof):
    prog, restored = work/"program", work/"restored"
    prog.unlink(missing_ok=True)
    restored.unlink(missing_ok=True)
    cli = [sys.executable, Path(__file__).with_name("atlas.py")]
    env = dict(os.environ)
    build = run(cli[0], env, [cli[1], "encode", base, target, prog, "--mode", mode], 60)
    restore = run(cli[0], env, [cli[1], "restore", base, restored, prog,
                               *(["--sparse"] if case == "volume" else [])], 60)
    sha256 = digest(restored)
    if sha256 != digest(target):
        raise ValueError(f"restored {case} digest differs")
    restore.pop("stdout")
    return {"family": case, "pair": pair, "mode": mode, "program_bytes": prog.stat().st_size,
            "program_allocation": prog.stat().st_blocks*512,
            "restored_allocation": restored.stat().st_blocks*512, "restored_sha256": sha256,
            "encode": build, "decode": restore, "ranges": ranges(prog, base, target),
            "consumer": consumer(case, pair+1, restored, args, work, proof),
            "faults": faults(prog, base)}


def wal_trial(args, work, proof):
    src = args.application / "wal"
    path = work / "wal"
    path.mkdir()
    db = path / "snapshot.sqlite"
    wal = path / "snapshot.sqlite-wal"
    base = args.corpus / "sqlite" / "0" / "snapshot.sqlite"
    known = proof["sqlite"]["wal"]
    payload, _ = encode(base.read_bytes(), (src/db.name).read_bytes(), "sqlite")
    with base.open("rb") as prior, db.open("xb") as out:
        Atlas(io.BytesIO(payload), prior).restore(out, False)
    # Package the WAL itself as a self-contained coded member. The 4 KiB
    # zero base is a fixed public input and carries no user data.
    raw = (src/wal.name).read_bytes()
    wal_program, _ = encode(bytes(BLK), raw, "runs")
    with wal.open("xb") as out:
        Atlas(io.BytesIO(wal_program), io.BytesIO(bytes(BLK))).restore(out, False)
    if digest(db) != known["database_sha256"] or digest(wal) != known["wal_sha256"]:
        raise ValueError("restored SQLite WAL pair differs")
    observed = query(db, replay_wal=True)
    if any(value != known[key] for key, value in observed.items()):
        raise ValueError("restored WAL replay differs")
    return {"family": "sqlite-wal-pair", "database_sha256": digest(db), "wal_sha256": sha(raw),
            "wal_bytes": len(raw), "program_bytes": len(payload)+len(wal_program), "consumer": observed}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("corpus", "application", "fixture_manifest", "work", "fs_tools", "results"):
        p.add_argument("--"+name.replace("_", "-"), type=lambda name: Path(name).resolve(), required=True)
    args = p.parse_args()
    args.work.mkdir(parents=True, exist_ok=True)
    proof = json.loads(args.fixture_manifest.read_text())
    with tempfile.TemporaryDirectory(dir=args.work) as scratch, args.results.open("x") as out:
        work = Path(scratch)
        for case, name in (("sqlite", "snapshot.sqlite"), ("volume", "volume.raw")):
            for pair in (0, 1):
                base, target = (args.corpus/case/str(rev)/name for rev in (pair, pair+1))
                methods = ["runs", "xor"] + (["sqlite"] if case == "sqlite" else [])
                for mode in methods[::1 if pair == 0 else -1]:
                    row = atlas_trial(args, work, case, pair, mode, base, target, proof)
                    print(json.dumps(row), file=out, flush=True)
        print(json.dumps(wal_trial(args, work, proof)), file=out, flush=True)


if __name__ == "__main__":
    main()
