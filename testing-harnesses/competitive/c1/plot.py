"""Render retained allocation versus timing by storage interface."""
from collections import defaultdict
from pathlib import Path
from statistics import median

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

from summarize import MIB, load


def points():
    trials = defaultdict(list)
    for name in ("s3-history-combined", "minio-history", "backup-combined", "backup-kopia-zstd"):
        for row in load(name):
            trials[row["system"], row.get("profile", "snapshot"), row["repetition"]].append(row)
    groups = defaultdict(list)
    for (system, profile, _), rows in trials.items():
        writes = [r for r in rows if r["phase"] == "ingest"]
        reads = [r for r in rows if r["phase"] == "restore"]
        if len(writes) != 3 or len(reads) != 3 or any(r["phase"] == "error" for r in rows):
            continue
        maint = next((r for r in rows if r["phase"] == "maintenance"), None)
        groups[system, profile].append((
            (maint or writes[-1])["allocated_bytes"] / MIB,
            sum(r["seconds"] for r in writes) + (maint["seconds"] if maint else 0),
            sum(r["seconds"] for r in reads),
        ))
    return groups


def main():
    groups = points()
    fig, axes = plt.subplots(2, 2, figsize=(11, 8), layout="constrained")
    backups = {"restic", "borg", "kopia"}
    for index, (axis_row, group) in enumerate(zip(axes, ("S3 objects", "Filesystem snapshots"))):
        eligible = [(system, profile, rows) for (system, profile), rows in sorted(groups.items())
                    if (system in backups) == bool(index)]
        for col, (ax, title) in enumerate(zip(axis_row, ("Ingest and maintenance", "Verified restoration"))):
            for system, profile, rows in eligible:
                values = list(zip(*rows))
                x, y = median(values[col + 1]), median(values[0])
                label = system if profile in ("plain", "snapshot") and system != "zeros3" else f"{system} {profile}"
                ax.errorbar(x, y, xerr=[[x - min(values[col + 1])], [max(values[col + 1]) - x]],
                            yerr=[[y - min(values[0])], [max(values[0]) - y]],
                            marker="o", capsize=3, label=label)
            ax.set(title=f"{group} · {title}", xlabel="Elapsed seconds", ylabel="Retained MiB")
            ax.grid(alpha=.2)
            ax.margins(x=.15, y=.16)
            if col == 0 and eligible:
                ax.legend(fontsize=8, loc="best")
    fig.suptitle("Three revisions of the 89.8 MiB synthetic history\n"
                 "Medians and observed ranges by storage interface", fontsize=14)
    fig.savefig(Path(__file__).parent / "results" / "history-tradeoffs.png", dpi=140)


if __name__ == "__main__":
    main()
