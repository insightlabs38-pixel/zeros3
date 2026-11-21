#!/usr/bin/env python3
"""Summarize completed and failed observations without changing a prepared pilot."""
import argparse
import datetime as dt
import json
from pathlib import Path

MIN_DAYS = 30
MIN_SAVING = 0.15
MIN_RATIO = 0.5
READ_PHASES = ("first-process-read", "repeat-whole")


def load(path):
    return json.loads(path.read_text())


def summarize(run):
    identity = load(run / "source.json")
    fixture = load(run / "ingest.json")
    samples = sorted(p for p in (run / "samples").iterdir() if p.is_dir())
    passes, failures = [], []
    for sample in samples:
        if (sample / "failure.json").is_file():
            failures.append({"sample": sample.name, **load(sample / "failure.json")})
            continue
        try:
            observation = load(sample / "observation.json")
            start = dt.datetime.fromisoformat(observation["start"])
            finish = dt.datetime.fromisoformat(observation["finish"])
            if start > finish:
                raise ValueError("observation finish predates its start")
            requests = 0
            for mode in ("cdc", "cold"):
                verify = load(sample / mode / "verify.out")
                if any(verify[name] for name in ("missing", "corrupt", "invalid")) or not verify["journal_ok"]:
                    raise ValueError(f"{mode} deep verification failed")
                with (sample / mode / "requests.jsonl").open() as stream:
                    served = [json.loads(line) for line in stream]
                by_key = {a["key"]: set() for a in fixture}
                for row in served:
                    if not row["exact"] or row["http"] != (206 if row["range"] else 200):
                        raise ValueError(f"{mode} {row['key']} response was inexact")
                    by_key[row["key"]].add(row["phase"])
                if any(not set(READ_PHASES).issubset(phases) for phases in by_key.values()):
                    raise ValueError(f"{mode} did not replay every retained revision")
                requests += len(served)
            if observation["errors"] != 0:
                raise ValueError("observation recorded an error")
            if not set(READ_PHASES).issubset(observation["whole_ratio"]):
                raise ValueError("missing required whole-read comparisons")
        except (OSError, KeyError, ValueError) as exc:
            failures.append({"sample": sample.name, "reason": str(exc)})
            continue
        passes.append({
            "start": start, "finish": finish, "exact_http_checks": requests,
            "allocated_bytes": observation["allocated_bytes"],
            "allocated_saving": observation["allocated_saving"],
            "whole_ratio": observation["whole_ratio"],
        })

    windows = sorted(passes, key=lambda row: row["start"])
    span = (max(r["finish"] for r in windows) - windows[0]["start"]) if windows else dt.timedelta()
    gaps = [(b["start"] - a["finish"]).total_seconds() for a, b in zip(windows, windows[1:])]
    allocated = [r["allocated_saving"] for r in windows]
    throughputs = {p: min((row["whole_ratio"][p] for row in windows), default=None) for p in READ_PHASES}
    economic = bool(allocated) and min(allocated) >= MIN_SAVING and min(throughputs.values()) >= MIN_RATIO
    return {
        "identity": identity, "samples_complete": len(windows), "samples_failed_or_incomplete": failures,
        "observed_days": span.total_seconds() / 86400, "largest_sample_gap_s": max(0, *gaps) if gaps else None,
        "exact_http_checks": sum(r["exact_http_checks"] for r in windows),
        "allocation_saving_range": [min(allocated), max(allocated)] if allocated else None,
        "min_whole_ratio": throughputs, "economic_gates": "PASS" if economic else "FAIL" if windows else "UNMEASURED",
        "duration_gate": "PASS" if span.days >= MIN_DAYS else "PENDING 30-DAY RUN",
        "overall": "PASS" if economic and span.days >= MIN_DAYS and not failures else "INCOMPLETE",
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("prepared_run", type=Path)
    args = p.parse_args()
    print(json.dumps(summarize(args.prepared_run), indent=2))


if __name__ == "__main__":
    main()
