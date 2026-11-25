"""Summarize matched C2 storage, read, and reoptimization evidence."""
import json
from pathlib import Path
from statistics import median

RESULTS = Path(__file__).parent / "results"
FAMILIES = ("lodash", "parquet", "sst", "tensor", "combined")


def load(name):
    return [json.loads(line) for line in (RESULTS / (name + ".jsonl")).read_text().splitlines()]


def row(rows, family, lane, stage):
    return next(r for r in rows if (r["family"], r["lane"], r["stage"]) == (family, lane, stage))


def main():
    original, full, c2 = map(load, ("b10-primary", "b10-finalized", "c2-finalized"))
    comparatives = {}
    for family in FAMILIES:
        comparatives[family] = {
            "b10_cdc_gc_bytes": row(original, family, "cdc", "gc")["allocated"],
            "b10_archive_gc_bytes": row(original, family, "c1", "gc")["allocated"],
            "b10_finalized_cdc_bytes": row(full, family, "cdc", "final-gc")["allocated"],
            "b10_finalized_delta_bytes": row(full, family, "delta", "final-gc")["allocated"],
            "b10_finalized_archive_bytes": row(full, family, "c1", "final-gc")["allocated"],
            "c2_finalized_archive_bytes": row(c2, family, "c1", "final-gc")["allocated"],
        }

    def metrics(rows, family, stage):
        selected = row(rows, family, "c1", stage)
        return {k: selected[k] for k in
                ("seconds", "cpu_seconds", "rss_bytes", "write_blocks", "peak_store_bytes")}

    def parquet_performance(build, initial):
        observations = [row(data, "parquet", "c1", "reads")["latency"]
                        for data in (initial, load(build + "-parquet-1"), load(build + "-parquet-2"))]
        return {k: {"median_run_p50_ms": median(obs[k]["p50_ms"] for obs in observations),
                    "run_p50_ms": [obs[k]["p50_ms"] for obs in observations]}
                for k in ("4096/0", "65536/0", "1048576/0", "0/0")}

    result = {"retained_storage": comparatives,
              "parquet_read_b10": parquet_performance("b10", full),
              "parquet_read_c2": parquet_performance("c2", c2),
              "composite_b10_apply": metrics(full, "combined", "apply"),
              "composite_c2_apply": metrics(c2, "combined", "apply"),
              "composite_b10_reoptimize": metrics(full, "combined", "reoptimize"),
              "composite_c2_memo": metrics(c2, "combined", "memo-hit")}
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
