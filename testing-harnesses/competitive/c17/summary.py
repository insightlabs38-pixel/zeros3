"""Recompute paired C17 optimization, verification and transfer metrics."""
import argparse
from collections import defaultdict
import json
from pathlib import Path
from statistics import median


def load(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def trend(data, fields):
    return {f: median(r[f] for r in data) for f in fields}


def summarize(args):
    optim = load(args.optimizer)
    expected = {(r["family"], k): spec["sha256"] for r in optim if r["stage"] == "ingest"
                for k, spec in r["paths"].items()}
    planners = defaultdict(list)
    plans = defaultdict(set)
    stores = defaultdict(set)
    for r in optim:
        family = r["family"]
        if r["stage"] == "optimize":
            key = (family, r["profile"], r["lane"])
            changes = r["report"].get("changes", [])
            identity = tuple(sorted((c["sha256"], c["to"], tuple(c.get("bases", []))) for c in changes))
            plans[(family, r["profile"], r["repeat"])].add(
                (r["report"]["applied"], r["report"]["planned"]["bytes"], identity))
            cand = r["report"].get("canon") or {}
            planners[key].append({**r, "apply_seconds": r["report"]["apply_seconds"],
                                  "gc_pause_ms": r["gc"]["pause_total_ms"],
                                  "cached_recipes": cand.get("retained_hits", 0)})
        elif r["stage"] == "read":
            for object_key, data in r["reads"].items():
                if data["sha256"] != expected[(family, object_key)]:
                    raise ValueError(f"{family} does not verify after finalization")
            stores[(family, r["profile"], r["lane"])].add(r["retained"])
    if any(len(m) != 1 for m in plans.values()):
        raise ValueError("retained canonical recipe changed the optimizer plan")
    saved = []
    for key, rows in sorted(planners.items()):
        family, profile, lane = key
        perf = trend(rows, ("seconds", "cpu_seconds", "apply_seconds", "peak_rss_bytes", "scratch_peak_bytes",
                            "gc_pause_ms", "output_blocks", "cached_recipes"))
        saved.append({"family": family, "profile": profile, "lane": lane,
                      "retained": sorted(stores[key]), "trials": len(rows), **perf})

    transports = defaultdict(list)
    verification = set()
    for workload, path in (("idle", args.native), ("foreground", args.foreground)):
        for r in load(path):
            key = (workload, r["profile"], r["receiver_build"])
            verification.add(r["object_sha256"])
            wire = sum(r[side][direction] for side in ("source_http_bytes", "destination_http_bytes")
                       for direction in ("client_to_server", "server_to_client"))
            receiver = r["receiver"]
            transports[key].append({"wire_bytes": wire, "seconds": r["seconds"],
                "retained": r["finalization"]["allocated"], "receiver_cpu": receiver["cpu_seconds"],
                "receiver_rss": receiver["process_peak_rss_bytes"], "receiver_reads": receiver["logical_read_bytes"],
                "receiver_writes": receiver["physical_write_bytes"], "gc_pause_ms": r["server_gc"]["pause_total_ms"],
                "foreground_p99_ms": r.get("foreground", {}).get("p99_ms", 0)})
    if len(verification) != 1 or expected[("typescript", "target")] not in verification:
        raise ValueError("native receiver did not reproduce the frozen update")
    received = [{"workload": key[0], "profile": key[1], "lane": key[2], "trials": len(rows),
                 **trend(rows, rows[0])} for key, rows in sorted(transports.items())]
    return {"optimizer": saved, "transfer": received, "typeScript_sha256": verification.pop()}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("optimizer", "native", "foreground", "results"):
        p.add_argument("--"+name, type=Path, required=True)
    args = p.parse_args()
    with args.results.open("x") as out:
        json.dump(summarize(args), out, indent=2, sort_keys=True)
        out.write("\n")


if __name__ == "__main__":
    main()
