"""Summarize only complete, verified benchmark trials from C1 JSONL."""
from collections import defaultdict
import json
from pathlib import Path
from statistics import median

MIB = 1 << 20
ROOT = Path(__file__).parent / "results"


def load(name):
    path = ROOT / (name + ".jsonl")
    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


def spread(values):
    return f"{median(values):.2f} [{min(values):.2f}, {max(values):.2f}]"


def requests():
    trials = defaultdict(list)
    all_rows = load("s3-requests") + load("minio-requests")
    for row in load("seaweed-8-volumes"):
        all_rows.append({**row, "system": "seaweed (8 volumes)"})
    for row in all_rows:
        if row["phase"] != "requests" or row["status"] != "verified":
            continue
        trials[row["system"], row["operation"], row["concurrency"]].append(row)
    print("## Loopback request samples\n")
    print("Median [minimum, maximum] across repetitions. Rates count verified operations.")
    print("GET timing includes body read and both SHA-256 computations. The LIST lane has 20 calls per run.\n")
    print("| System | Operation | Clients | Runs | Operations/s | p50 ms | p95 ms | p99 ms |")
    print("|---|---|---:|---:|---:|---:|---:|---:|")
    for (system, op, concurrency), rows in sorted(trials.items()):
        rate = spread([r["operations"] / r["seconds"] for r in rows])
        p50 = spread([r["p50_ms"] for r in rows])
        p95 = spread([r["p95_ms"] for r in rows])
        p99 = spread([r["p99_ms"] for r in rows]) if rows[0]["p99_ms"] is not None else "—"
        print(f"| {system} | {op} | {concurrency} | {len(rows)} | {rate} | {p50} | {p95} | {p99} |")


def histories():
    trials = defaultdict(list)
    for name in ("backup-combined", "s3-history-combined", "s3-history-families",
                 "backup-families", "backup-kopia-zstd", "minio-history",
                 "s3-codecs", "backup-codecs"):
        for row in load(name):
            trials[row["system"], row["family"], row.get("profile", "snapshot"),
                   row["repetition"]].append(row)
    groups = defaultdict(list)
    for (system, family, profile, _), rows in trials.items():
        ingest = [r for r in rows if r["phase"] == "ingest"]
        restore = [r for r in rows if r["phase"] == "restore"]
        if len(ingest) != 3 or len(restore) != 3 or any(r["phase"] == "error" for r in rows):
            continue
        maintenance = next((r for r in rows if r["phase"] == "maintenance"), None)
        groups[(family, system, profile)].append({
            "logical": sum(r["logical_bytes"] for r in ingest) / MIB,
            "ingest": sum(r["seconds"] for r in ingest),
            "restore": sum(r["seconds"] for r in restore),
            "pre": ingest[-1]["allocated_bytes"] / MIB,
            "post": (maintenance or ingest[-1])["allocated_bytes"] / MIB,
            "maint": maintenance["seconds"] if maintenance else None,
        })
    print("\n## Three-revision history samples\n")
    print("Allocation uses 512-byte stat block counts, including directories and indexes.")
    print("Backup timing includes CLI startup and destination writes. S3 restore is a verified in-memory GET.\n")
    print("| Family | System | Profile | Runs | Logical MiB | Pre-maint. MiB | Retained MiB | Ingest s | Maint. s | Restore s |")
    print("|---|---|---|---:|---:|---:|---:|---:|---:|---:|")
    for (family, system, profile), rows in sorted(groups.items()):
        cells = [spread([r[k] for r in rows]) if rows[0][k] is not None else "—"
                 for k in ("logical", "pre", "post", "ingest", "maint", "restore")]
        print(f"| {family} | {system} | {profile} | {len(rows)} | " + " | ".join(cells) + " |")


def xet():
    for k, title in (("xet-local", "OpenXet loopback"),
                     ("xet-local-repeat", "OpenXet loopback repeat"),
                     ("xet-local-no-chunk-cache", "OpenXet without the client chunk cache"),
                     ("xet-hosted", "Hugging Face hosted")):
        rows = load(k)
        if not rows:
            continue
        print(f"\n## {title}\n")
        print("Interface counters include protocol overhead and other traffic on the measured interface.")
        print("They do not report CAS allocation or exact per-request payload bytes.\n")
        print("| Resource | Operation | Case | Status | s | Logical MiB | TX KiB | RX KiB |")
        print("|---|---|---|---|---:|---:|---:|---:|")
        for row in rows:
            if "seconds" in row:
                print(f"| {row['resource']} | {row['phase']} | {row['case'] or '—'} | "
                      f"{row.get('http_status') or row['status']} | {row['seconds']:.3f} | {row['logical_bytes'] / MIB:.3f} | "
                      f"{row.get('interface_tx_bytes', 0) / 1024:.1f} | "
                      f"{row.get('interface_rx_bytes', 0) / 1024:.1f} |")


if __name__ == "__main__":
    print("# C1 benchmark tables\n")
    requests()
    histories()
    xet()
