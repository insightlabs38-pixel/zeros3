#!/usr/bin/env python3 -I
"""B8: B2's measured reverse-base/hybrid physical rows vs B7's modeled hybrid (B7 report section 3 values) per family.
Usage: reverse_base.py ../b7/b2_reference.json"""
import json, sys
d = json.load(open(sys.argv[1] if len(sys.argv) > 1 else "testing-harnesses/z4/b7/b2_reference.json"))["families"]
# B7 model hot-hybrid / B2 hot-hybrid pairs as printed in Z4_B7_PLANNER2.md section 3 (model value only used here)
B7_MODEL = {"clr-short": .82, "dex": 1.47, "parquet": 1.70, "src-tar-short": .93, "git": 1.55}
print(f"{'family':14s} {'CDC MB':>7s} {'archive-best':>12s} {'newest-order':>12s} {'hot-hybrid':>10s} {'B7 model hybrid':>15s} {'hybrid/archive saving':>22s} {'decision same?':>14s}")
for fam, rows in d.items():
    if "cdc" not in rows or fam == "unrelated": continue
    c = rows["cdc"]["phys"]
    arch = min(r["phys"] for k, r in rows.items() if k.startswith("auth-archive") or k == "auth-delta-only") / c
    newest = rows["auth-archive-newest"]["phys"] / c if "auth-archive-newest" in rows else float("nan")
    hot = rows["auth-hot-hybrid"]["phys"] / c if "auth-hot-hybrid" in rows else float("nan")
    m = B7_MODEL.get(fam)
    ins = (1 - hot) / (1 - arch) if arch < 1 else float("nan")
    same = "-" if m is None else ("yes" if (m < 1) == (hot < 0.995) else "NO (tie)" if hot >= .995 and m > 1 else "NO")
    print(f"{fam:14s} {c/1e6:7.2f} {arch:12.3f} {newest:12.3f} {hot:10.3f} {'' if m is None else format(m, '.2f'):>15s} {ins:22.2f} {same:>14s}")
