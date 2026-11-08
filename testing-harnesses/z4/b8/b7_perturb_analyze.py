#!/usr/bin/env python3 -I
"""Summarize B7 robustness reruns (rows_*.json.gz produced by z4b7 -mode eval on perturbed series).
Usage: b7_perturb_analyze.py LABEL=rows.json.gz ...   Compares on scenarios S1,S2 only (the reruns' common subset)."""
import gzip, json, sys, collections, statistics as st
SIMPLE = ["always-cdc", "greedy", "premium-cap", "expected-value"]
FOLDS = ["archive", "managed", "model", "parquet", "sst"]
POL = ["auto", "class-prior", "lifetime", "expected-value", "premium-cap", "greedy", "combined", "combined-no-defer", "cons-group"]
for arg in sys.argv[1:]:
    label, path = arg.split("=", 1)
    R = [r for r in json.load(gzip.open(path))["Rows"] if r["Scenario"][:2] in ("S1", "S2")]
    W = collections.defaultdict(dict)
    for r in R: W[(r["Book"], r["Fold"], r["Scenario"], r["Seed"])][r["Policy"]] = r["Cost"][r["Book"]]
    print(f"\n=== {label}: {len(R)} rows (S1,S2; seed 11)")
    for book in ("A_storage", "B_io_heavy"):
        def fm(f, p):
            v = [w[p] / w["always-cdc"] for (b, ff, s, sd), w in W.items() if b == book and ff == f and p in w]
            return st.mean(v) if v else float("nan")
        means = {p: st.mean(fm(f, p) for f in FOLDS) for p in POL}
        rank = sorted(means, key=means.get)
        wins = 0; imps = []
        for f in FOLDS:
            base = min(fm(f, p) for p in SIMPLE); a = fm(f, "auto"); wins += a < base; imps.append((base - a) / base)
        cw = sum(fm(f, "combined") < min(fm(f, p) for p in SIMPLE) for f in FOLDS)
        print(f"{book}: mean cost/CDC " + " ".join(f"{p}={means[p]:.3f}" for p in POL))
        print(f"   rank: {' < '.join(rank[:6])}; auto beats best-simple on {wins}/5 folds, mean imp {st.mean(imps):+.3f}; combined wins {cw}/5; worst auto fold {max(fm(f,'auto') for f in FOLDS):.3f}")
