"""Exercise grouped and legacy stores with two actual engine builds."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "competitive" / "c3"))
from transfer import BUCKET, KEY, SECRET, serve


def digest(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("new", "old", "work"):
        parser.add_argument(f"--{name}", type=Path, required=True)
    args = parser.parse_args()
    args.work.mkdir(parents=True)
    env = dict(os.environ, AWS_ACCESS_KEY_ID=KEY, AWS_SECRET_ACCESS_KEY=SECRET, GOMAXPROCS="2")
    payload = b"journal compatibility exact bytes"
    for mode in ("legacy", "grouped"):
        store = args.work/mode
        log = args.work/f"{mode}.log"
        first = args.old if mode == "legacy" else args.new
        initial = dict(env, ZEROS3_JOURNAL_FORMAT="j2" if mode == "grouped" else "")
        with serve(first, store, log, initial) as (_, client, _):
            client.create_bucket(Bucket=BUCKET)
            client.put_object(Bucket=BUCKET, Key="compat", Body=payload, Metadata={"tag": mode})
        journal = store/"journal"/"visibility.log"
        before = digest(journal)
        if mode == "grouped":
            prior = subprocess.run([args.old, "stats", "-store", store, "-json"], env=env,
                                   capture_output=True, text=True, timeout=10)
            if prior.returncode == 0 or "journalgroup" not in prior.stderr:
                raise ValueError(f"old build did not refuse grouped format: {prior.stderr}")
        if digest(journal) != before:
            raise ValueError("the refused reader changed the journal")
        with serve(args.new, store, log, dict(env, ZEROS3_JOURNAL_FORMAT="")) as (_, client, _):
            response = client.get_object(Bucket=BUCKET, Key="compat")
            with response["Body"] as body:
                if body.read() != payload or {k.lower(): v for k, v in response["Metadata"].items()} != {"tag": mode}:
                    raise ValueError("new reader changed object bytes or metadata")
        if digest(journal) != before:
            raise ValueError("a read-only request changed the journal")
        print(json.dumps({"mode": mode, "old_sha256": digest(args.old), "new_sha256": digest(args.new),
                          "old_refused": mode == "grouped", "new_exact": True, "journal_sha256": before}))


if __name__ == "__main__":
    main()
