"""Recompute C19 scoreboard medians and observed ranges from raw rows."""
import argparse
import json
from pathlib import Path
from statistics import median


def load(root, name):
    return [json.loads(line) for line in (root/f"{name}.jsonl").read_text().splitlines()]


def metric(values):
    values = [x for x in values if x is not None]
    if not values:
        return None
    return {"median": median(values), "min": min(values), "max": max(values), "n": len(values)}


def aggregate(rows, dimensions, measures):
    groups = {}
    for row in rows:
        label = tuple(row.get(name) for name in dimensions)
        groups.setdefault(label, []).append(row)
    return [{**dict(zip(dimensions, key)), **{
        name: metric(getter(row) for row in group) for name, getter in measures.items()}}
        for key, group in groups.items()]


def fields(*names):
    return {name: lambda row, name=name: row.get(name) for name in names}


def nested(*keys):
    def get(row):
        for name in keys:
            if row is None:
                break
            row = row.get(name)
        return row
    return get


def wire(row, side):
    leg = row[f"{side}_http_bytes"]
    return leg["client_to_server"]+leg["server_to_client"]


def summarize(root):
    controls = load(root, "retention")
    restore = [row for row in controls if row.get("portable")]
    def transport(name):
        rows = load(root, name)
        return aggregate((r for r in rows if r["stage"] == "transfer"),
                         ("family", "prepared_source", "state", "shape", "lane"), {
            **fields("seconds"), "source_wire": lambda r: wire(r, "source"),
            "destination_wire": lambda r: wire(r, "destination"),
            "total_wire": lambda r: wire(r, "source")+wire(r, "destination"),
            "receiver_rss": nested("receiver", "process_peak_rss_bytes"),
            "receiver_cpu": nested("receiver", "cpu_seconds"),
            "sender_cpu": nested("sender", "cpu_seconds"),
            "client_cpu": nested("client", "cpu_seconds"),
            "receiver_allocated": nested("finalization", "allocated")})

    summaries = {
        "general": aggregate(load(root, "general"), ("engine", "phase", "range_bytes", "group"), fields(
            "seconds", "p50_ms", "p95_ms", "p99_ms", "restart_seconds", "server_cpu_seconds",
            "server_rss_bytes", "immutable_allocated", "finalized_allocated", "server_disk_write_bytes",
            "metadata_present", "verified_objects")),
        "retention": aggregate((r for r in controls if "lane" in r), ("family", "lane"), {
            **fields("allocated"),
            "pruned": nested("prune", "allocated"),
            "ingest_seconds": nested("ingest", "seconds"),
            "ingest_write_bytes": nested("ingest", "server", "physical_write_bytes"),
            "optimize_seconds": nested("optimize", "seconds"),
            "optimize_cpu": nested("optimize", "cpu_seconds"),
            "optimize_rss": nested("optimize", "peak_rss_bytes"),
            "optimized": nested("optimize", "report", "applied"),
            "range_p95_ms": lambda r: r["reads"][0]["p95_ms"],
            "whole_p50_ms": lambda r: r["reads"][1]["p50_ms"]}),
        "portable": aggregate(restore, ("family",), {
            "bundle_bytes": nested("portable", "bundle_bytes"),
            "export_seconds": nested("portable", "export", "seconds"),
            "import_seconds": nested("portable", "import_c9", "seconds"),
            "restore_seconds": nested("portable", "restore_c9", "seconds")}),
        "qos": aggregate(load(root, "qos"), ("event", "lane"), {
            "p99_ms": nested("foreground", "result", "p99_ms"),
            "import_wall_seconds": lambda r: (r["background"]["import"]["end"]-
                                              r["background"]["import"]["start"]) if "import" in r["background"] else None,
            "allocated": nested("allocated")}),
        "transport": transport("transport"),
        "transport_merged": transport("transport-merged"),
        "openxet": aggregate(load(root, "openxet"), ("resource", "phase", "case"), fields(
            "seconds", "interface_rx_bytes", "interface_tx_bytes", "client_cpu_seconds", "server_allocated_bytes")),
        "backup": aggregate((r for r in load(root, "backup") if r["phase"] != "error"),
                            ("family", "system", "phase", "revision"), fields(
            "seconds", "cpu_seconds", "allocated_bytes", "cache_allocated_bytes", "verified_files")),
        "codec": aggregate(load(root, "codec"), ("family", "base_revision", "delta"), {
            **fields("patch_logical", "patch_allocated"), "encode_cpu": nested("encode", "cpu_seconds"),
            "decode_cpu": nested("decode", "cpu_seconds")}),
    }
    return summaries


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("raw", type=Path)
    args = p.parse_args()
    for panel, rows in summarize(args.raw).items():
        for row in rows:
            print(json.dumps({"scoreboard": panel, **{k: v for k, v in row.items() if v is not None}}))


if __name__ == "__main__":
    main()
