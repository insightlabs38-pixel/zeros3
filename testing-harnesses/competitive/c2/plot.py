"""Plot the finalized Parquet read comparison and composite apply tradeoff."""
import json

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

from summary import RESULTS, load, row


def main():
    data = json.loads((RESULTS / "summary.json").read_text())
    fig, (reads, frontier) = plt.subplots(1, 2, figsize=(10, 4.5), layout="constrained")
    for build, name, color in (("b10", "Finalized B10", "#2362a2"),
                               ("c2", "Finalized C2", "#c35a20")):
        values = data[f"parquet_read_{build}"]
        samples = [values[k] for k in ("4096/0", "65536/0", "1048576/0", "0/0")]
        points = [s["median_run_p50_ms"] for s in samples]
        lower = [p - min(s["run_p50_ms"]) for p, s in zip(points, samples)]
        upper = [max(s["run_p50_ms"]) - p for p, s in zip(points, samples)]
        reads.errorbar(range(4), points, yerr=[lower, upper],
                       color=color, marker="o", capsize=4, label=name)
    reads.set_xticks(range(4), ["4 KiB", "64 KiB", "1 MiB", "Whole"])
    reads.set(yscale="log", ylabel="Median GET latency (ms)", title="Parquet · three matched trials")
    reads.legend(loc="lower right")
    reads.grid(alpha=.18, which="both")
    retained = data["retained_storage"]["combined"]
    budget = load("c2-budget-final")
    axes = (("Finalized B10 archive", data["composite_b10_apply"]["seconds"],
             retained["b10_finalized_archive_bytes"], "#2362a2", (-5, 9)),
            ("Finalized C2 archive", data["composite_c2_apply"]["seconds"],
             retained["c2_finalized_archive_bytes"], "#c35a20", (-5, -17)),
            ("C2, 32 targets", row(budget, "combined", "budget", "apply")["seconds"],
             row(budget, "combined", "budget", "final-gc")["allocated"], "#378165", (8, 8)))
    for label, seconds, storage, color, offset in axes:
        point = seconds, storage / 2**20
        frontier.scatter(*point, color=color, zorder=3)
        frontier.annotate(label, point, xytext=offset, textcoords="offset points",
                          ha="right" if offset[0] < 0 else "left", fontsize=9)
    frontier.set(xlim=(6, 12.3), ylim=(25.4, 32), xlabel="Optimizer apply wall time (s)",
                 ylabel="Final retained allocation (MiB)", title="Composite · storage vs. apply cost")
    frontier.grid(alpha=.18)
    fig.savefig(RESULTS / "storage-read-tradeoffs.png", dpi=140)


if __name__ == "__main__":
    main()
