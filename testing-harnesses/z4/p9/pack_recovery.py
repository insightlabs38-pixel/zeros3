#!/usr/bin/env python3
"""Inject a record payload fault into a disposable pack copy and verify S3 behavior."""
import argparse
import json
from pathlib import Path
import shutil
import struct
import subprocess

from scale_recovery import ACCESS, SECRET, check_objects, curl, digest, flipped, process, sample_fixture, wait_live

ENTRY = struct.Struct("<32sQIIB3x")
FOOTER = struct.Struct("<4sHHQQ32sII")


def find_record(store, sha):
    target = bytes.fromhex(sha)
    matches = []
    for pack in sorted((store / "packs").rglob("*.pack")):
        with pack.open("rb") as stream:
            stream.seek(-FOOTER.size, 2)
            stop = stream.tell()
            magic, version, _, count, index, *_ = FOOTER.unpack(stream.read(FOOTER.size))
            if magic != b"ZSPF" or version != 1 or stop - index != ENTRY.size * count:
                raise ValueError(f"invalid pack geometry in {pack}")
            stream.seek(index)
            for stamp, offset, stored, logical, codec in ENTRY.iter_unpack(stream.read(stop - index)):
                if stamp == target:
                    if offset + stored > index or not stored:
                        raise ValueError(f"invalid record span in {pack}")
                    matches.append((pack, offset, stored, logical, codec))
    if len(matches) != 1:
        raise RuntimeError(f"expected one physical record for {sha}, found {len(matches)}")
    return matches[0]


def fault_trial(binary, store, evidence, targets, codec):
    bad, good = targets[:2]
    pack, offset, stored, logical, found = find_record(store, bad[1])
    if found != codec:
        raise RuntimeError(f"expected codec {codec}, got {found}")
    original = digest(pack)
    if pack.stat().st_nlink > 1:
        clone = pack.with_suffix(".fault-copy")
        shutil.copy2(pack, clone)
        clone.replace(pack)
    if pack.stat().st_nlink != 1:
        raise RuntimeError("pack targeted for corruption must be private to the scratch store")
    scratch = evidence / "response.bin"
    with process(binary, store, evidence / "clean.log") as (child, port, endpoint):
        wait_live(child, port)
        check_objects(endpoint, [bad, good], scratch)
    with flipped(pack, offset + stored // 2):
        with process(binary, store, evidence / "injected.log") as (child, port, endpoint):
            wait_live(child, port)
            check_objects(endpoint, [good], scratch)
            status = subprocess.check_output(
                ["curl", "--silent", "--show-error", "--noproxy", "*", "--max-time", "45",
                 "--aws-sigv4", "aws:amz:us-east-1:s3", "--user", f"{ACCESS}:{SECRET}",
                 "--output", str(scratch), "--write-out", "%{response_code}", endpoint + bad[0]],
                text=True,
            )
            if status != "500" or b"<Code>InternalError</Code>" not in scratch.read_bytes():
                raise RuntimeError(f"damaged record was not explicitly diagnosed on GET ({status})")
            (evidence / "refusal.xml").write_bytes(scratch.read_bytes())
    if digest(pack) != original:
        raise RuntimeError("pack did not return to its original SHA-256")
    with process(binary, store, evidence / "restored.log") as (child, port, endpoint):
        wait_live(child, port)
        check_objects(endpoint, [bad, good], scratch)
    return {"codec": codec, "pack_sha256": original, "record_sha256": bad[1],
            "stored_bytes": stored, "logical_bytes": logical, "fault_http_status": int(status),
            "exact_unaffected_and_restored": True}


def compressed_store(binary, evidence):
    store = evidence / "store"
    scratch = evidence / "response.bin"
    targets = []
    with process(binary, store, evidence / "ingest.log") as (child, port, endpoint):
        wait_live(child, port)
        curl(endpoint, "/faults", scratch, "-X", "PUT")
        for key in ("target", "control"):
            payload = evidence / f"{key}.bin"
            payload.write_bytes((key.encode() * 1024)[:4096])
            path = f"/faults/{key}"
            curl(endpoint, path, scratch, "-T", str(payload))
            targets.append((path, digest(payload)))
    with (evidence / "repack.json").open("xb") as result:
        subprocess.run(
            [binary, "repack", "-store", store, "-apply", "-recompress", "-json"],
            stdout=result, check=True,
        )
    return store, targets


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--shadow-store", type=Path, required=True, help="existing disposable scale_recovery.py shadow")
    p.add_argument("--manifest", type=Path, required=True)
    p.add_argument("--out", type=Path, required=True)
    args = p.parse_args()
    binary = args.binary.resolve(strict=True)
    shadow = args.shadow_store.resolve(strict=True)
    if not (shadow.parent / "results.json").is_file() or shadow.name != "store":
        p.error("use the scratch store produced by a successful scale_recovery.py trial")
    args.out.mkdir()
    scale = args.out / "scale"
    scale.mkdir()
    compressed = args.out / "deflate"
    compressed.mkdir()
    tiny, targets = compressed_store(binary, compressed)
    report = {
        "deflate_control_store": fault_trial(binary, tiny, compressed, targets, 1),
        "raw_million_object_shadow": fault_trial(binary, shadow, scale, sample_fixture(args.manifest), 0),
    }
    with (args.out / "results.json").open("x") as out:
        json.dump(report, out, indent=2)
        out.write("\n")


if __name__ == "__main__":
    main()
