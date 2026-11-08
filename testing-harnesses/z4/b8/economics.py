#!/usr/bin/env python3 -I
"""B8 common-economics model. Every input row carries a provenance tag:
 P = physical store bytes (real packs, after gc/repack) on this code;  S = standalone research representation (B1/B3/B4/B5/B6, modelled CDC baseline)
 M = production-kind lab model (not physically measured);  X = interpolated bound (midpoint/derived, not measured).
Ratios are bytes of the *whole retained history* divided by production CDC bytes of the same history. Nothing below is a field estimate."""
import itertools, sys
# class: (cons, central, opt) store ratio for the archived share, for each option, plus hot-hybrid ratio for option B/C, provenance
# Option B = B2-style narrow authority over *existing* production kinds + exact delta. Option C = B plus B3/B4/B5/B6 forms behind the same authority.
# realization factor standalone->physical measured on this tree: DEX .487->.489 (1.00), CLR format .898->.901 (1.00), tar .546->.499 (.91), CLR hybrid .655->.713 (1.09), Git .61-.64->.82 (1.3)
REAL = (1.35, 1.10, 1.00)   # conservative / central / optimistic realization of a standalone ratio
CLS = {
 # name: dict(B=(c,m,o), Bhot=(c,m,o) or None, Cstand=(c,m,o) standalone ratio or None, tag)
 "managed histories (CLR/DEX)": dict(B=(0.88, 0.66, 0.49), Bhot=(0.97, 0.87, 0.76), C=(0.88, 0.66, 0.49), tag="B:P (CLR .71-.82, DEX .49-.88); C adds nothing measured (DEX bespoke beats generic)"),
 "LLVM/PDB build histories":      dict(B=(0.90, 0.80, 0.65), Bhot=(1.0, 0.9, 0.8), C=(0.69, 0.51, 0.41), tag="B:X delta only (B1 LLVM .66-.78 generic); C:S B4 .51/.41 x REAL"),
 "source tar / Git histories":    dict(B=(0.82, 0.65, 0.50), Bhot=(1.0, 0.85, 0.73), C=(0.82, 0.60, 0.50), tag="B:P tar .499 Git .819; C:S B3 packagings only when multi-packaging"),
 "DB SST histories":              dict(B=(0.70, 0.50, 0.31), Bhot=(0.9, 0.7, 0.5), C=(0.40, 0.31, 0.31), tag="B:S/M (kind 9 .66-.74, ukv chain .31); C:S B6 LZ4 .31"),
 "columnar (Parquet)":            dict(B=(0.90, 0.65, 0.40), Bhot=(1.0, 0.9, 0.7), C=(0.60, 0.42, 0.40), tag="B:P .897 synthetic / S .40; C:S B6 LZ4_RAW .42"),
 "model checkpoints (natural)":   dict(B=(0.89, 0.78, 0.67), Bhot=(1.0, 0.95, 0.9), C=(0.84, 0.70, 0.57), tag="B:M lab Model8 .67-.89; C:S B5 .57-.73 (narrowing is Pythia-F32 specific)"),
 "model multi-format/quantized":  dict(B=(0.85, 0.78, 0.74), Bhot=(1.0, 0.95, 0.9), C=(0.60, 0.30, 0.24), tag="B:M GGUF-only .74-.78; C:S B3 .24, B5 .28-.41"),
 "unrelated / one-off":           dict(B=(1.0, 1.0, 1.0), Bhot=(1.0, 1.0, 1.0), C=(1.0, 1.0, 1.0), tag="P: nothing admitted = 1.00x"),
}
MIX = {
 "artifact/CI registry": {"managed histories (CLR/DEX)": .20, "LLVM/PDB build histories": .10, "source tar / Git histories": .25, "DB SST histories": .05, "columnar (Parquet)": .05, "model checkpoints (natural)": 0, "model multi-format/quantized": 0, "unrelated / one-off": .35},
 "ML model hub":         {"managed histories (CLR/DEX)": 0, "LLVM/PDB build histories": 0, "source tar / Git histories": .02, "DB SST histories": 0, "columnar (Parquet)": .10, "model checkpoints (natural)": .35, "model multi-format/quantized": .25, "unrelated / one-off": .28},
 "general S3 (mostly unrelated)": {"managed histories (CLR/DEX)": .01, "LLVM/PDB build histories": 0, "source tar / Git histories": .03, "DB SST histories": .01, "columnar (Parquet)": .03, "model checkpoints (natural)": 0, "model multi-format/quantized": 0, "unrelated / one-off": .92},
}
def blended(mix, opt, scen, hot):
    # scen 0=conservative 1=central 2=optimistic ; hot = fraction of versioned data that must stay hot (hybrid ratio)
    tot = 0
    for c, w in mix.items():
        d = CLS[c]
        if opt == "A": r = 1.0
        elif opt == "B": r = (1 - hot) * d["B"][scen] + hot * d["Bhot"][scen]
        elif opt == "C":
            c_r = min(d["B"][scen], d["C"][scen] * (REAL[scen] if d["C"] != d["B"] else 1.0)) if d["C"] != d["B"] else d["B"][scen]
            r = (1 - hot) * c_r + hot * d["Bhot"][scen]
        tot += w * r
    return tot
print("Blended physical store bytes / CDC  (cons | central | opt), hot share of versioned data h")
print("HYPOTHETICAL mixes x measured/standalone per-class ratios (see CLS tags); not a field estimate\n")
for h in (0.0, 0.5, 1.0):
    print(f"--- hot share h={h}")
    for m, mix in MIX.items():
        row = []
        for o in "ABC":
            row.append(f"{o}: " + " | ".join(f"{blended(mix, o, s, h):.3f}" for s in (0, 1, 2)))
        print(f"{m:32s} " + "   ".join(row))
print("\nBreak-even: share of store that must be in versioned/reusable classes for a total saving >= 5% (option B central, archive-only):")
avg = sum(CLS[c]["B"][1] for c in CLS if c != "unrelated / one-off") / (len(CLS) - 1)
print(f"  mean central per-class ratio over reusable classes = {avg:.3f} -> saving per reusable byte {1-avg:.2f}; share needed = {0.05/(1-avg):.2f}")
print("\nAnalysis-CPU break-even: a representation pays its encode CPU if saved_fraction*storage$ > CPU time*cpu$ (per GB, 12 months)")
books = {"A_storage": dict(st=0.023, cpu=0.04), "B_io_heavy": dict(st=0.0045, cpu=0.08)}
enc = {"NRC/FPE model coder (B5, 10-24 MiB/s)": 10.5, "B6 SST/Parquet analyze (27 MiB/s)": 27, "B4 LLVM encode (7.1 MiB/s)": 7.1, "B4 LLVM per-function proof (2.5 MiB/s)": 2.5,
       "B5 hybrid low-rank ladder (0.6-3.7 MiB/s)": 1.5, "B3 exact-DEFLATE git/tar analysis (1.1 MB/s)": 1.1, "B1 PXR (2.8 MiB/s)": 2.8, "LZ4HC SST analyze (6-10 MiB/s)": 8}
for b, p in books.items():
    print(f"  {b}: storage ${p['st']}/GB-mo, cpu ${p['cpu']}/core-h")
    for n, r in enc.items():
        t_h = 1024 / r / 3600
        cpu = t_h * p["cpu"]
        need = cpu / (12 * p["st"])
        print(f"    {n:46s} CPU ${cpu:.5f}/GB  -> break-even saved fraction {100*need:5.1f}% of the encoded bytes")
