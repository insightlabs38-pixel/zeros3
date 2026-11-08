#!/usr/bin/env python3 -I
"""Write a perturbed copy of the B7 candidate series for robustness reruns.
Usage: b7_perturb_series.py in.json out.json MODE SEED   (MODE: noise|heavy|bias|invert)
noise : lognormal sd .10 on measured rep numbers, .25 on modeled ones (independent per scheme/stage)
heavy : sd .10 measured, heavy tail on modeled (lognormal sd .5, 10% of entries x/÷ up to 2.2)
bias  : every non-CDC scheme +15% on inc and std (systematically optimistic costs)
invert: first-sight (stage 0) of every non-CDC scheme +30% and later stages +10% (lock-in pessimism)"""
import json, math, random, sys
src, dst, mode, seed = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
rnd = random.Random(seed)
s = json.load(open(src))
def f(x, src_tag, i):
    if mode == "noise": sd = 0.10 if src_tag.startswith("measured") else 0.25; return x * math.exp(rnd.gauss(0, sd))
    if mode == "heavy":
        sd = 0.10 if src_tag.startswith("measured") else 0.5
        m = math.exp(rnd.gauss(0, sd))
        if not src_tag.startswith("measured") and rnd.random() < 0.10: m *= rnd.choice([2.2, 1/2.2])
        return x * m
    if mode == "bias": return x * 1.15
    if mode == "invert": return x * (1.30 if i == 0 else 1.10)
    raise SystemExit("mode")
for h in s["histories"]:
    for sc in h["schemes"]:
        if sc["kind"] == "cdc": continue
        inc, std = sc["inc"], sc["std"]
        ni, ns = [], []
        for i, (a, b) in enumerate(zip(inc, std)):
            b2 = max(1, int(f(b, sc["std_src"], i)))
            a2 = b2 if a == b else min(b2, max(1, int(f(a, sc["inc_src"], i))))  # loader requires inc <= std
            ni.append(a2); ns.append(b2)
        sc["inc"], sc["std"] = ni, ns
json.dump(s, open(dst, "w"))
