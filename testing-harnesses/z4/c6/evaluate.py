#!/usr/bin/env python3
"""Compare fixed admission rules on held-out archives and measured store pairs."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import statistics

STATIC = ("always-cdc", "greedy", "premium-cap", "late-bootstrap")
COUNTERS = ("FinalPhys", "Peak", "Pinned", "ByteDays", "Served", "Logical", "Decode",
            "AnalysisMSSync", "AnalysisMSBg", "MigrateMS", "HotViol", "MaxDepth",
            "MaxFanout", "MaxBlast", "Failed")


def archived_folds(path):
    with gzip.open(path, "rt") as stream:
        archived = json.load(stream)["Rows"]
    groups = {}
    for row in archived:
        key = tuple(row[f] for f in ("Book", "Fold", "Scenario", "Seed"))
        if row["Policy"] in groups.setdefault(key, {}):
            raise ValueError(f"duplicate policy in archive group {key}")
        groups[key][row["Policy"]] = row
    matched = {k: v for k, v in groups.items() if all(name in v for name in STATIC)}
    report = []
    for book, held in sorted({key[:2] for key in matched}):
        train = [v for (b, fold, _, _), v in matched.items() if b == book and fold != held]
        test = [v for (b, fold, _, _), v in matched.items() if b == book and fold == held]
        if not train or not test:
            raise ValueError(f"{book}/{held} lacks an independent fold")
        score = lambda policy: sum(v[policy]["Cost"][book] for v in train)
        chosen = min(STATIC, key=score)
        total = sum(v[chosen]["Cost"][book] for v in test)
        best_static = sum(min(v[p]["Cost"][book] for p in STATIC) for v in test)
        best_recorded = sum(min(r["Cost"][book] for r in v.values()) for v in test)
        exact = [v[chosen] for v in test if v[chosen]["OracleExact"] and book in (v[chosen]["OracleCost"] or {})]
        chosen_rows = [v[chosen] for v in test]
        report.append({
            "price_book": book, "held_out": held, "rule": chosen,
            "training_runs": len(train), "holdout_runs": len(test),
            "cost_vs_cdc": total / sum(v["always-cdc"]["Cost"][book] for v in test),
            "cost_vs_best_static_per_run": total / best_static,
            "cost_vs_best_recorded_per_run": total / best_recorded,
            "exact_hindsight_runs": len(exact),
            "cost_vs_exact_hindsight": (
                sum(r["Cost"][book] for r in exact) / sum(r["OracleCost"][book] for r in exact)
                if exact else None
            ),
            "median_archive_counters": {c: statistics.median(r[c] for r in chosen_rows) for c in COUNTERS},
        })
    return {"groups": len(groups), "matched_groups": len(matched), "folds": report}


def verified(row):
    return row["journal_ok"] and not any(row[field] for field in ("missing", "corrupt", "invalid"))


def cold_pair(path, parquet_cdc, parquet_candidate, wire_path):
    observed = json.loads(path.read_text())
    read_ratio = min(r for run in observed["samples"] for r in run["whole_ratio"].values())
    migrated = observed["migration"]
    cold = observed["samples"][0]["allocated_bytes"]
    pq_base = json.loads(parquet_cdc.read_text())
    pq_hard = json.loads(parquet_candidate.read_text())
    wire = json.loads(wire_path.read_text())["trials"]
    cases = [
        {
            "fold": "mixed_retained_histories", "scope": "three Coreutils and Pythia families",
            "cdc_bytes": cold["cdc"], "candidate_bytes": cold["cold"], "min_whole_ratio": read_ratio,
            "exact": all(verified(s["verify"]) and all(r["exact"] for r in s["requests"])
                         for run in observed["samples"] for s in run["stores"].values()),
            "sampled_read_requests": sum(len(s["requests"]) for run in observed["samples"] for s in run["stores"].values()),
            "migration_cpu_s": migrated["cpu_s"], "peak_migration_bytes": migrated["allocated_peak_sampled"],
            "peak_migration_rss_bytes": migrated["max_rss_kib"] << 10,
            "depth": max((r.get("depth", 0) for r in observed["admission"]["Plans"]), default=0),
            "unmeasured": ["shared-base repair latency", "field access rates"],
        },
        {
            "fold": "parquet_brotli", "scope": "two independently pinned Parquet revisions",
            "cdc_bytes": pq_base["physical_file_bytes"], "candidate_bytes": pq_hard["physical_file_bytes"],
            "min_whole_ratio": pq_hard["whole_mib_s"] / pq_base["whole_mib_s"],
            "exact": True, "sampled_read_requests": len(pq_hard["range_ms"]) + 2,
            "range_median_ms": statistics.median(pq_hard["range_ms"]),
            "cdc_range_median_ms": statistics.median(pq_base["range_ms"]),
            "migration_wall_s": pq_hard["migration_s"],
            "unmeasured": ["field access rates", "migration CPU", "peak physical headroom", "peer repair latency"],
        },
        {
            "fold": "coreutils_relay", "scope": "two Coreutils gzip releases already represented in the mixed fold",
            "cdc_bytes": min(r["allocated_after_gc_repack"] for r in wire if r["mode"] == "cdc"),
            "candidate_bytes": min(r["allocated_after_gc_repack"] for r in wire if r["mode"] == "native"),
            "exact": all(verified(r["deep_verify"]) for r in wire),
            "min_whole_ratio": None,
            "successful_sampled_replications": len(wire),
            "seed_recovery": "peer authority recovered and verified the base before each trial",
            "unmeasured": ["whole-read throughput ratio", "field access rates", "server migration CPU", "base fanout scale"],
            "dependent_fold": "mixed_retained_histories",
        }
    ]
    for row in cases:
        saving = 1 - row["candidate_bytes"] / row["cdc_bytes"]
        row["physical_saving"] = saving
        row["best_measured_by_bytes"] = "candidate" if saving > 0 and row["exact"] else "cdc"
        if not row["exact"]:
            raise ValueError(f"inexact measurement in {row['fold']}")
        reasons = []
        if saving < .10:
            reasons.append("retained-byte gain below 10%")
        if row["min_whole_ratio"] is None:
            reasons.append("whole-read throughput ratio unmeasured")
        elif row["min_whole_ratio"] < .5:
            reasons.append("whole-read throughput below 0.5x CDC")
        row["quality_reasons"] = reasons
        row["fixed_rule_with_operator_opt_in"] = "cdc" if reasons else "candidate"
        row["default"] = "cdc"
    return cases


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--archived", required=True, type=Path)
    p.add_argument("--retained-pilot", required=True, type=Path)
    p.add_argument("--parquet-cdc", required=True, type=Path)
    p.add_argument("--parquet-range", required=True, type=Path)
    p.add_argument("--coreutils-relay", required=True, type=Path)
    p.add_argument("--out", required=True, type=Path)
    args = p.parse_args()
    inputs = {key: value for key, value in vars(args).items() if key != "out"}
    sources = {}
    for key, path in inputs.items():
        with path.open("rb") as stream:
            sources[key] = {"file": path.name, "sha256": hashlib.file_digest(stream, "sha256").hexdigest()}
    result = {
        "sources": sources,
        "historical_replay": archived_folds(args.archived),
        "measured_fixtures": cold_pair(args.retained_pilot, args.parquet_cdc, args.parquet_range, args.coreutils_relay),
        "policy": {"minimum_saving": .10, "minimum_hybrid_whole_ratio": .50, "operator_opt_in": True},
    }
    with args.out.open("x") as stream:
        json.dump(result, stream, indent=2, sort_keys=True)
        stream.write("\n")


if __name__ == "__main__":
    main()
