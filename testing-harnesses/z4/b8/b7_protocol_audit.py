#!/usr/bin/env python3 -I
"""B8 audit of the B7 preregistered promotion rule, recomputed from rows.json.gz.
Usage: b7_protocol_audit.py [rows.json.gz] [protocol.json]"""
import gzip, json, sys, statistics as st, collections, random
rows_p = sys.argv[1] if len(sys.argv) > 1 else "testing-harnesses/z4/b7/rows.json.gz"
proto_p = sys.argv[2] if len(sys.argv) > 2 else "testing-harnesses/z4/b7/protocol.json"
R = json.load(gzip.open(rows_p))["Rows"]
P = json.load(open(proto_p))
SIMPLE = ["always-cdc", "greedy", "premium-cap", "expected-value"]  # protocol's simple baselines
FOLDS = ["archive", "managed", "model", "parquet", "sst"]
# world key -> policy -> cost
W = collections.defaultdict(dict)
for r in R:
    W[(r["Book"], r["Fold"], r["Scenario"], r["Seed"])][r["Policy"]] = r["Cost"][r["Book"]]
def foldmean(book, fold, pol):
    v = [w[pol] / w["always-cdc"] for (b, f, s, sd), w in W.items() if b == book and f == fold and pol in w]
    return st.mean(v) if v else float("nan")
def verdict(pol, books=("A_storage", "B_io_heavy"), verbose=True):
    out = {}
    ok_all = True
    for b in books:
        wins = 0; imps = []; worst = 0
        for f in FOLDS:
            base = min(foldmean(b, f, p) for p in SIMPLE)
            m = foldmean(b, f, pol)
            wins += m < base
            imps.append((base - m) / base)
            worst = max(worst, m)
        out[b] = dict(wins=wins, mean_improvement=st.mean(imps), worst_vs_cdc=worst, per_fold_imp=[round(x, 3) for x in imps])
        ok_all &= wins >= 4 and st.mean(imps) >= 0.05 and worst <= 1.03
    nwin = {b: out[b]["wins"] for b in books}
    if ok_all: v = "PROMOTE"
    elif all(nwin[b] >= 2 for b in books): v = "SPECIALIZED(>=2 folds in both books)"
    elif any(nwin[b] >= 2 for b in books): v = "SPECIALIZED-in-one-book-only"
    else: v = "NOT_JUSTIFIED"
    return v, out
print("registered rule:", P["promotion_rule"])
for pol in ["combined", "auto", "class-prior", "lifetime", "combined-no-defer", "cons-group"]:
    v, o = verdict(pol)
    print(f"\n[{pol}] => {v}")
    for b, x in o.items(): print("  ", b, json.dumps(x))
# sensitivity: drop each fold, recompute mean improvement of auto vs best simple (fold-mean level)
print("\nauto: mean per-fold improvement over best simple, dropping one fold")
for b in ("A_storage", "B_io_heavy"):
    imp = {}
    for f in FOLDS:
        base = min(foldmean(b, f, p) for p in SIMPLE); imp[f] = (base - foldmean(b, f, "auto")) / base
    print(b, {f: round(x, 3) for f, x in imp.items()}, "all", round(st.mean(imp.values()), 4),
          "drop:", {f: round(st.mean(v for k, v in imp.items() if k != f), 4) for f in FOLDS})
# scenario-cluster bootstrap (resample fold x scenario clusters, seeds averaged) of auto vs best-simple-per-fold
random.seed(1)
for b in ("A_storage", "B_io_heavy"):
    cl = collections.defaultdict(lambda: collections.defaultdict(list))
    for (bb, f, s, sd), w in W.items():
        if bb != b: continue
        for p in SIMPLE + ["auto"]: cl[(f, s)][p].append(w[p] / w["always-cdc"])
    cm = {k: {p: st.mean(v) for p, v in d.items()} for k, d in cl.items()}
    keys = list(cm)
    def stat(sample):
        # per fold: best simple chosen in-sample (hindsight, biased against auto) vs auto
        imps = []
        for f in FOLDS:
            ks = [k for k in sample if k[0] == f]
            if not ks: continue
            base = min(st.mean(cm[k][p] for k in ks) for p in SIMPLE)
            imps.append((base - st.mean(cm[k]["auto"] for k in ks)) / base)
        return st.mean(imps)
    bs = sorted(stat([random.choice(keys) for _ in keys]) for _ in range(2000))
    print(b, "cluster-bootstrap (fold x scenario) mean improvement 95%CI:", round(bs[50], 4), round(bs[1950], 4), "point", round(stat(keys), 4))
# when does auto lose to CDC at world level
for b in ("A_storage", "B_io_heavy"):
    L = [(k, w["auto"] / w["always-cdc"]) for k, w in W.items() if k[0] == b]
    print(b, "auto worlds worse than CDC:", sum(1 for _, x in L if x > 1.0), "of", len(L), "worst", round(max(x for _, x in L), 3))
