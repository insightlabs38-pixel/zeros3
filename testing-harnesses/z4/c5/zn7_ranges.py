#!/usr/bin/env python3
"""Measure exact signed ranges on a prepared authoritative model store."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess

ACCESS = "AKIAZEROS3PILOTLOCAL01"
SECRET = "zeros3PilotLocalScratchAuthenticationOnly01"


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--baseline", required=True, type=Path)
    p.add_argument("--candidate", required=True, type=Path)
    p.add_argument("--store", required=True, type=Path)
    p.add_argument("--pilot-helper", required=True, type=Path)
    p.add_argument("--original", type=Path, action="append", required=True)
    p.add_argument("--out", required=True, type=Path)
    a = p.parse_args()
    a.out.mkdir()
    spec = importlib.util.spec_from_file_location("pilot", a.pilot_helper)
    pilot = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(pilot)
    original_digests = {p.name: pilot.digest(p) for p in a.original}
    rows = []

    def get(url, path, size, start, dest):
        cmd = ["curl", "--fail", "--silent", "--show-error", "--aws-sigv4", "aws:amz:us-east-1:s3",
               "--user", f"{ACCESS}:{SECRET}", "--noproxy", "*", "--max-time", "240",
               "--output", str(dest), "--write-out", "%{json}"]
        if size:
            cmd += ["--range", f"{start}-{start + size - 1}"]
        return json.loads(subprocess.check_output([*cmd, url + "/models/" + path], text=True))

    measurements = [("baseline", a.baseline), ("candidate", a.candidate),
                    ("candidate", a.candidate), ("baseline", a.baseline)]
    for trial, (form, binary) in enumerate(measurements):
        stamp = pilot.digest(binary)
        with pilot.serve(binary.resolve(), a.store.resolve(), a.out / f"{trial}-{form}.log") as url:
            for original in a.original:
                size = original.stat().st_size
                target = a.out / "body.bin"
                samples = []
                with original.open("rb") as expected:
                    for off in (4 << 20, size // 5, size // 2, size * 3 // 4, size - 4096):
                        expected.seek(off)
                        check = hashlib.sha256(expected.read(4096)).hexdigest()
                        for request in ("first-range", "repeat-range"):
                            response = get(url, original.name, 4096, off, target)
                            if response["response_code"] != 206 or pilot.digest(target) != check:
                                raise RuntimeError(f"{form} inexact {original.name} range at {off}")
                            samples.append({"phase": request, "offset": off, "seconds": response["time_total"]})
                response = get(url, original.name, 0, 0, target)
                if response["response_code"] != 200 or pilot.digest(target) != original_digests[original.name]:
                    raise RuntimeError(f"{form} inexact whole {original.name}")
                rows.append({"trial": trial, "mode": form, "binary_sha256": stamp, "key": original.name,
                             "sha256": original_digests[original.name], "whole_seconds": response["time_total"],
                             "whole_mib_s": size / (1 << 20) / response["time_total"], "ranges": samples})
                target.unlink()
    (a.out / "report.json").write_text(json.dumps(rows, indent=2) + "\n")


if __name__ == "__main__":
    main()
