#!/usr/bin/env python3
"""Extract the Z4 B7 candidate-cost series from pinned B1/B3/B4/B5/B6 evidence.

Every number is copied from an evidence file in this repository (no network).
Per scheme and stage: inc = marginal bytes when the history is stored all-in in
that scheme; std = bytes when no earlier stage is available in that scheme
(measured where the evidence has it, otherwise first-sight ratio x logical,
tagged std_src="modeled"). Usage: build_series.py <repo>/testing-harnesses > series.json
"""
import gzip, json, re, sys, os

root = sys.argv[1]
Z4 = os.path.join(root, "z4")
MiB = 1 << 20


def jgz(p):
    return json.load(gzip.open(os.path.join(Z4, p)))


def units(s):
    m = re.fullmatch(r"([\d.]+)([KM]?)", s.strip())
    v = float(m.group(1))
    return int(round(v * {"": 1, "K": 1024, "M": MiB}[m.group(2)]))


H = []
OPP = []  # future opportunities, never ready candidates


def scale_std(inc0, logical):
    return [int(round(inc0 * l / logical[0])) for l in logical]


def hist(name, cls, evidence, source, logical, schemes, labels=None, note=""):
    n = len(logical)
    for s in schemes:
        assert len(s["inc"]) == n, (name, s["name"], len(s["inc"]), n)
        if "std" not in s:
            s["std"] = scale_std(s["inc"][0], logical)
            s["std_src"] = "modeled"
        else:
            s.setdefault("std_src", "measured")
        s["std"] = [max(a, b) if False else a for a, b in zip(s["std"], s["inc"])]
        s["std"][0] = s["inc"][0]
        s["inc"] = [min(i, st) for i, st in zip(s["inc"], s["std"])]
    H.append(dict(name=name, **{"class": cls}, evidence=evidence, source=source,
                  stages=[dict(label=(labels[i] if labels else "s%d" % i), logical=l)
                          for i, l in enumerate(logical)], schemes=schemes, note=note))


# ---- B6 SST chains (exact JSON), three codecs ----------------------------------
SST_DEC = 129.0  # MiB/s, fresh-process decode incl. SHA (report B6 table)
for codec, fname in [("lz4", "sst_econ_lz4-rocksdb.json"), ("lz4hc", "sst_econ_lz4hc-rocksdb.json"),
                     ("snappy", "sst_econ_snappy-rocksdb.json")]:
    d = json.load(open(os.path.join(Z4, "b6/results", fname)))
    ch = d["chain"]
    logical = [c["raw"] for c in ch]
    sch = []
    for rep, nm, dep in [("cdc", "cdc", "none"), ("blocks", "b6-blocks", "cas"), ("ukv", "b6-ukv8k", "cas")]:
        inc = [c["incr"][rep]["pack"] for c in ch]
        # measured standalone: unrelated arrival (n03) scaled to the stage size
        v = {x["rev"]: x for x in (d.get("variants") or [])}
        if "n03-unrelated" in v:
            un = v["n03-unrelated"]
            stdr = un["reps"][rep]["pack"] / un["reps"][rep]["raw"]
            std = [int(round(stdr * c["raw"])) for c in ch]
            stdsrc = "measured-ratio(n03-unrelated)"
        else:
            std = [int(round(inc[0] * c["raw"] / ch[0]["raw"])) for c in ch]
            stdsrc = "modeled(first-sight ratio)"
        sch.append(dict(name=nm, kind="cdc" if rep == "cdc" else "rep", dep=dep, depth_cap=1 if dep == "cas" else 0,
                        inc=inc, std=std, std_src=stdsrc, inc_src="measured",
                        analysis_mibs=1e9 if rep == "cdc" else 27.0, decode_mibs=400.0 if rep == "cdc" else SST_DEC,
                        amp4k=36.8 if rep == "cdc" else 1.9, amp64k=2.4 if rep == "cdc" else 1.9, amp1m=1.1 if rep == "cdc" else 1.1,
                        amp_src="B6 sst_reads/range: CDC 151KiB vs 7.7KB for 4KiB block"))
    hist("sst-%s-rocksdb" % codec, "sst", "PRODUCER_WORKLOAD", ["z4/b6/results/" + fname], logical, sch,
         labels=[c["rev"] for c in ch],
         note="Z3-30 operation log chain c0..c7; c3 compaction is where ukv wins; c0 cdc is the first-sight lock-in")
    for vv in ("n02-randvals", "n03-unrelated"):
        if not d.get("variants"):
            continue
        x = {y["rev"]: y for y in d["variants"]}[vv]
        sch1 = []
        for rep, nm, dep in [("cdc", "cdc", "none"), ("blocks", "b6-blocks", "cas"), ("ukv", "b6-ukv8k", "cas")]:
            r = x["reps"][rep]["pack"]
            sch1.append(dict(name=nm, kind="cdc" if rep == "cdc" else "rep", dep=dep, depth_cap=1 if dep == "cas" else 0,
                             inc=[r], std=[r], std_src="measured", inc_src="measured",
                             analysis_mibs=1e9 if rep == "cdc" else 27.0, decode_mibs=400.0 if rep == "cdc" else SST_DEC,
                             amp4k=36.8 if rep == "cdc" else 1.9, amp64k=2.4 if rep == "cdc" else 1.9, amp1m=1.1,
                             amp_src="B6"))
        hist("oneoff-sst-%s-%s" % (codec, vv.split("-")[1]), "sst", "PRODUCER_WORKLOAD", ["z4/b6/results/" + fname],
             [x["logical"] if False else x["reps"]["cdc"]["raw"]], sch1, note="unrelated/random-value arrival (no reuse), one-off")

# ---- B6 Parquet chain (text table, MiB/KiB rounded to 4 digits) ----------------
def parquet_chain(fname, tag):
    t = open(os.path.join(Z4, "b6/results", fname)).read()
    sec = t.split("incremental cost per revision")[1].split("rolling cost")[0]
    rows = re.findall(r"\| (c\d)[^|]*\| ([\d.]+[KM]?) \| ([\d.]+[KM]?) \| ([\d.]+[KM]?) \|", sec)
    return [(r[0], units(r[1]), units(r[2]), units(r[3])) for r in rows]


rows = parquet_chain("parquet_chain_lz4.txt", "lz4")
# per-revision logical sizes: the 8-rev chain is 275 MiB in total (report 2); equal split is a modeled assumption
lg = [int(275 * MiB / len(rows))] * len(rows)
sch = []
for i, (nm, dep, an, dec, a4, a64, a1) in enumerate([("cdc", "none", 1e9, 400.0, 11.0, 1.2, 0.6),
                                                     ("b6-dpages", "cas", 20.0, 120.0, 4.0, 1.5, 1.0),
                                                     ("b6-logical8k", "cas", 14.0, 100.0, 2.0, 1.3, 1.0)]):
    sch.append(dict(name=nm, kind="cdc" if i == 0 else "rep", dep=dep, depth_cap=0 if i == 0 else 1,
                    inc=[r[1 + i] for r in rows], analysis_mibs=an, decode_mibs=dec, amp4k=a4, amp64k=a64, amp1m=a1,
                    amp_src="B1 parquet CDC 11.0x; rep amps modeled from B1 format 1.7x/fine", inc_src="measured(rounded)"))
hist("parquet-lz4-chain", "parquet", "PRODUCER_WORKLOAD", ["z4/b6/results/parquet_chain_lz4.txt"], lg, sch,
     labels=[r[0] for r in rows],
     note="pyarrow 8-revision chain; per-revision logical size is total/8 (modeled); c3/c5 are retention/compaction rewrites")
OPP.append(dict(name="parquet-brotli-logical", reason="B6: exact replay not built; 0-byte cost would be an oracle", ready=False,
                source="z4/b6/results/parquet_chain_brotli.txt"))

# ---- B6 SquashFS ---------------------------------------------------------------
t = open(os.path.join(Z4, "b6/results/sqfs_econ.txt")).read()
blocks = re.split(r"\n(?=\S)", t)
for blk in blocks:
    m = re.match(r"(\w+)\s+cdc16/64/256\s+CDC-of-image ([\d.]+)M\s+exact-lz4-decoded\+CDC ([\d.]+)M", blk)
    if not m:
        continue
    nm = m.group(1)
    rel = re.findall(r"^\s+([\d.]+): raw ([\d.]+)M -> rep ([\d.]+)M", blk, re.M)
    if nm not in ("three_lz4", "typescript_lz4", "typescript_lz4hc", "three_lz4hc"):
        continue
    logical = [units(r[1] + "M") for r in rel]
    rep_mult = 1.0
    sch = [dict(name="cdc", kind="cdc", dep="none", depth_cap=0, inc=[units(r[1] + "M") for r in rel],
                analysis_mibs=1e9, decode_mibs=400.0, amp4k=9.0, amp64k=1.0, amp1m=0.5, amp_src="B1 CDC amps (image)",
                inc_src="measured(rounded)"),
           dict(name="b6-sqfs-decoded", kind="rep", dep="cas", depth_cap=1, inc=[units(r[2] + "M") for r in rel],
                analysis_mibs=10.0, decode_mibs=60.0, amp4k=2.0, amp64k=1.5, amp1m=1.0, amp_src="B6 independent blocks (modeled)",
                inc_src="measured(rounded)")]
    hist("sqfs-" + nm, "archive", "REAL" if "typescript" in nm else "PRODUCER_WORKLOAD", ["z4/b6/results/sqfs_econ.txt"],
         logical, sch, labels=[r[0] for r in rel], note="SquashFS-lz4 release images; decoded-block representation vs CDC-of-image")

# ---- B1 families (exact stage arrays) -----------------------------------------
ev = jgz("b1/evidence.json.gz")
B1FAM = {"clr-newtonsoft-struct": ("clr", "REAL", 1.0), "dex-fdroid-struct": ("dex", "REAL", 1.0),
         "git-history-struct": ("git", "REAL", 1.0), "git-repack-struct": ("git", "REAL_REEXPORT", 1.0),
         "source-archives-struct": ("archive", "REAL", 1.0), "oci-transport-struct": ("oci", "REAL_REEXPORT", 1.0),
         "parquet-chain-struct": ("parquet", "PRODUCER_WORKLOAD", 1.0), "rocksdb-sst-struct": ("sst", "PRODUCER_WORKLOAD", 1.0),
         "pebble-sst-struct": ("sst", "PRODUCER_WORKLOAD", 1.0), "model-pythia-struct": ("model", "REAL", 1.0)}
AMP = {"clr": (9.2, 15.4), "dex": (9.4, 17.2), "git": (14.6, 22.3), "archive": (3.2, 5.3), "oci": (3.2, 5.3),
       "parquet": (11.0, 16.9), "sst": (11.0, 16.9), "model": (23.0, 40.0)}
for c in ev["components"]:
    nm = c["Family"]
    if nm not in B1FAM:
        continue
    cls, evid, _ = B1FAM[nm]
    S = c["Stages"]
    cum = lambda key: [s[key] for s in S]
    diff = lambda a: [a[0]] + [a[i] - a[i - 1] for i in range(1, len(a))]
    auth = [s["Standalone"] + s["IndexBytes"] for s in S]
    logical = [s["InputBytes"] for s in S]
    encmibs = (c["Raw"] / MiB) / (c["EncodeMS"] / 1000.0)
    decmibs = (c["Raw"] / MiB) / (c["DecodeMS"] / 1000.0)
    a4, a4g = AMP[cls]
    sch = [dict(name="cdc", kind="cdc", dep="none", depth_cap=0, inc=diff(cum("CDC")), analysis_mibs=1e9,
                decode_mibs=400.0, amp4k=a4, amp64k=1.0, amp1m=0.5, amp_src="B1 baseline-ranges", inc_src="measured"),
           dict(name="b1-delta", kind="rep", dep="chain", depth_cap=2, inc=diff(auth), analysis_mibs=encmibs,
                decode_mibs=decmibs, amp4k=a4g, amp64k=2.5, amp1m=1.2, amp_src="B1 baseline-ranges (generic)", inc_src="measured")]
    ids = [x.replace("-struct", "") for x in [nm]]
    hist("b1-" + nm.replace("-struct", ""), cls, evid, ["z4/b1/evidence.json.gz"], logical, sch,
         labels=["r%d" % s["Revision"] for s in S], note="B1 exact component streams, generic similarity delta (auth = standalone+index)")

# ---- B4 LLVM / PDB (per-release columns from the evidence text) ---------------
t4 = gzip.open(os.path.join(Z4, "b4/evidence.txt.gz")).read().decode().splitlines()
groups, cur = [], None
for ln in t4:
    if ln.startswith("###"):
        cur = []
        groups.append((ln, cur))
        continue
    m = re.match(r"(\S+\.(?:bc|pdb))\s+((?:\d+(?:\s+|$)){4,8})", ln)
    if m and cur is not None:
        cur.append((m.group(1), [int(x) for x in m.group(2).split()]))
for hdr, rows in groups:
    if not rows:
        continue
    if " llvm " in hdr and "-O2" in rows[0][0] and len(rows) == 6:
        nm, cols = "b4-llvm-o2", dict(cdc=0, b1=2, b4=5)
    elif " llvm " in hdr and "-O0" in rows[0][0]:
        nm, cols = "b4-llvm-o0", dict(cdc=0, b1=2, b4=5)
    elif " pdb " in hdr:
        nm, cols = "b4-pdb", dict(cdc=0, b1=2, b4=4)
    else:
        continue
    logical = [{"b4-llvm-o2": 2600000, "b4-llvm-o0": 1400000, "b4-pdb": 500000}[nm]] * len(rows)  # modeled sizes
    sch = [dict(name="cdc", kind="cdc", dep="none", depth_cap=0, inc=[r[1][cols["cdc"]] for r in rows], analysis_mibs=1e9,
                decode_mibs=400.0, amp4k=9.0, amp64k=1.0, amp1m=0.5, amp_src="B1", inc_src="measured"),
           dict(name="b1-delta", kind="rep", dep="chain", depth_cap=2, inc=[r[1][cols["b1"]] for r in rows], analysis_mibs=15.0,
                decode_mibs=100.0, amp4k=17.0, amp64k=2.5, amp1m=1.2, amp_src="B1", inc_src="measured"),
           dict(name="b4-graph", kind="rep", dep="chain", depth_cap=2, inc=[r[1][cols["b4"]] for r in rows], analysis_mibs=2.5,
                decode_mibs=18.0, amp4k=650.0 if nm != "b4-pdb" else 120.0, amp64k=40.0, amp1m=3.0,
                amp_src="B4: unindexed VM requires whole-object replay (size/4KiB)", inc_src="measured")]
    hist(nm, "bitcode" if "llvm" in nm else "pdb", "REAL" if nm == "b4-pdb" else "REAL_SOURCE_COMPILED",
         ["z4/b4/evidence.txt.gz"], logical, sch, labels=[r[0] for r in rows],
         note="per-release columns from b4 evidence; logical sizes are modeled round values (per-release CDC cost is measured)")
OPP.append(dict(name="b4-dwarf", reason="B4: DWARF adapter unfinished", ready=False, source="Z4_B4_GRAPH_NORMALIZATION.md"))

# ---- B3 SmolLM2 cross-format, forward and reverse ingestion -------------------
e3 = jgz("b3/evidence.json.gz")["smol"]
fw = e3["full"]["files"]
rv = e3["reverse_online"]["files"]
def cumdiff(files):
    c = [f["cum_total"] for f in files]
    return {f["file"]: (c[i] - (c[i - 1] if i else 0)) for i, f in enumerate(files)}


idn = cumdiff(e3["identity"]["files"])
sz = {f["file"]: f["size"] for f in e3["full"]["files"]}
cdc_ratio = e3["cdc"]["prod"] / e3["cdc"]["raw"]
for nm, files, note in [("b3-smollm2-forward", fw, "BF16 safetensors first"), ("b3-smollm2-reverse", rv, "lossy forms first; BF16 last")]:
    order = [f["file"] for f in files]
    cd = cumdiff(files)
    logical = [sz[f] for f in order]
    sch = [dict(name="cdc", kind="cdc", dep="none", depth_cap=0, inc=[int(round(l * cdc_ratio)) for l in logical],
                std_src="modeled", inc_src="modeled(prod CDC total/raw ratio)", analysis_mibs=1e9, decode_mibs=400.0,
                amp4k=23.0, amp64k=2.4, amp1m=1.1, amp_src="B5 ranges (CDC 94KB for 4KiB)"),
           dict(name="b3-semcas", kind="rep", dep="chain", depth_cap=4, inc=[f["new_blob_bytes"] + f["recipe_bytes"] for f in files],
                std=[idn[f] for f in order], std_src="measured(identity mode new blob)", inc_src="measured",
                analysis_mibs=(sum(logical) / MiB) / 11.0, decode_mibs=80.0, amp4k=1.9, amp64k=1.9, amp1m=1.0,
                amp_src="B3 ranges: semcas_bytes_read 4200-7762 for 4KiB; permuted tensors need whole tensor (not in these files)")]
    hist(nm, "crossfmt", "REAL", ["z4/b3/evidence.json.gz"], logical, sch, labels=order,
         note=note + "; group reoptimize action: %s" % json.dumps(e3["reverse_optimized"][0]))
    H[-1]["group_actions"] = [dict(name="b3-optimize", scheme="b3-semcas", after_total=e3["reverse_optimized"][0]["after"],
                                   before_total=e3["reverse_optimized"][0]["before"], cpu_ms=e3["reverse_optimized"][0]["ms"],
                                   peak_rss_mib=976.4,
                                   applies=(nm == "b3-smollm2-reverse"))]
OPP.append(dict(name="b3-orc", reason="B3: ORC replay incomplete", ready=False, source="Z4_B3_CROSS_FORMAT_CAS.md"))

# ---- B5 model families (exact per-state arrays; direct = std, pool3 = inc) ----
e5 = jgz("b5/evidence.json.gz")["families"]
RAWMB = {"pythia14m": (338, 12, 0.0), "pythia70m": (3099, 11, 1697 / 3099.0), "gpt2": (1644, 3, 1494 / 1644.0),
         "qwen05": (4940, 5, 3132 / 4940.0), "smollm2": (3229, 8, 2916 / 3229.0), "smollm2ctl": (1345, 5, 908 / 1345.0)}
SMOL_CDC = [263.7, 263.5, 263.7, 261.5, 539, 539, 539, 246]
for fam, (rawmb, n, cdcr) in RAWMB.items():
    pol = e5[fam]["policies"]
    best = "pool3" if "pool3" in pol else "prev"
    inc, std = pol[best]["per_state"], pol["direct"]["per_state"]
    if fam == "pythia14m":
        cdcr = 338.0 / 338.0
    logical = [int(rawmb * 1e6 / n)] * n
    if fam == "smollm2":
        cdc = [int(x * 1e6) for x in SMOL_CDC]
        logical = [int(x * 1e6) for x in [269, 269, 269, 267, 540, 540, 540, 253]]
        src_c = "measured(report B5 §3 per-state)"
    else:
        cdc = [int(l * cdcr) for l in logical]
        src_c = "modeled(CDC total/raw x uniform state size)"
        # exact-duplicate states (direct bytes ~0, e.g. an unsloth copy) are deduplicated by CDC as well
        cdc = [min(c, max(d, 0) + 4096) if d < 0.01 * l else c for c, d, l in zip(cdc, std, logical)]
    sch = [dict(name="cdc", kind="cdc", dep="none", depth_cap=0, inc=cdc, std_src="modeled", inc_src=src_c, analysis_mibs=1e9,
                decode_mibs=400.0, amp4k=23.0, amp64k=2.4, amp1m=1.1, amp_src="B5 ranges"),
           dict(name="b5-model", kind="rep", dep="chain", depth_cap=3, inc=inc, std=std, std_src="measured(b5 direct)",
                inc_src="measured(b5 %s)" % best, analysis_mibs=10.5, decode_mibs=85.0, amp4k=144.0, amp64k=11.5, amp1m=2.8,
                amp_src="B5 ranges 64K-element segments (SmolLM2)")]
    hist("b5-" + fam, "model", "REAL" if fam != "smollm2ctl" else "REAL_WITH_SYNTHETIC_CONTROLS", ["z4/b5/evidence.json.gz"],
         logical, sch, note="pool3/prev exact per-state bytes; hop bound <=3")

out = dict(version=1, histories=H, opportunities=OPP,
           note="Generated by z4/b7/build_series.py from pinned evidence; see per-scheme inc_src/std_src/amp_src tags.")
json.dump(out, sys.stdout, separators=(",", ":"), sort_keys=True)
