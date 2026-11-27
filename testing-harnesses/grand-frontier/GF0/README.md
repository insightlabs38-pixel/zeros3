# GF0 · native baseline and cost map

Source engine `3db525631727c38f548420e323038ab31e047080`. Go 1.27.0 on Linux
amd64, two CPU quota, 8 GiB memory limit, local overlay. Each run reads all
retained objects against the independent input and verifies the store deeply.
Each pair reverses lane order on the repeat. The data includes two release
histories held out from current tuning, plus known generated controls.

| Family | CDC allocated bytes | Archive allocated bytes | Warm whole p95, CDC / archive |
| --- | ---: | ---: | ---: |
| TypeScript tar | 9,211,904 | 4,792,320 | 163–226 / 44–115 ms |
| TypeScript gzip | 8,392,704 | 4,829,184 | 4.8–8.5 / 1,182–1,196 ms |
| urllib3 gzip | 622,592 | 405,504 | 0.17–0.57 / 92–95 ms |
| SQLite | 655,360 | 319,488 | 1.2–4.6 / 3.0–3.5 ms |
| Parquet | 1,253,376 | 634,880 | 1.9–4.4 / 81–127 ms |
| Sparse volume | 405,504 | 319,488 | 27–38 / 25–49 ms |
| HTTPX tar | 319,488 | 188,416 | 0.28–0.30 / 1.7–2.1 ms |
| Independent entropy | 8,433,664 | 8,433,664 | 14–15 / 5.8–8.6 ms |
| Synthetic typed safetensors | 192,512 | 86,016 | 1.2–3.2 / 5.1–5.9 ms |

The timings are in-process probes, with four or six whole samples per
phase and 18 or 27 ranges per phase. Tail values are descriptive. Page
cache was ambient and the first pass followed native reopen. Independently
verified whole and range bytes, process CPU, archive planning, finalization,
pack blocks, dependency extents, and 4 KiB CAS work are in the raw rows.
Dependency extents describe the reachable graph. Their storage is already
included in allocated blocks when the same data serves a live root.

## Decision map

1. Fan out from exact manifest digests to current CDC, XD, canonical replay,
   structured codec and deep-splice options. Separate byte identity from
   the current encoding and compact-storage locator.
2. `rgGraph` deduplicates components by digest and builds dependency-aware
   options. `rgEval` tracks live roots, pins and external references by
   transition and charges a referenced component once. The solver has
   shared changes, partition search, a small oracle and calibrated read
   work, including restartable blocks where enabled.
3. Record costs include stored bytes, per-record pack/index entries,
   and a grouped record estimate. Loose records round individually.
   Packed bytes model average group overhead, and filesystem rounding,
   pack placement, duplicate tier copies, and compaction slack can change
   the actual marginal charge. Per-root latency is approximated across
   probes and can diverge from measured tails or mixed-load effects.
4. `rgGraph.apply` verifies each exact path and preserves live refs through
   transition bridges. Canonical GC/repack/compact materializes the retained
   allocation. Range readers follow native offsets and dependencies.
   Native sync can offer existing exact records and receiver-derived parents
   under its admission rules. Transfer-preparation CPU and receiver state
   remain separate inputs to the research planner.

## Related matched controls

FB-12's record comparison charges native pack geometry. TypeScript
DEFLATE-6 was 4,558,848 bytes, its smallest experimental codec 3,751,936,
and xz 3,895,296 on the same record set. The experimental codec missed the
general decode-throughput gate. C13 pack dictionaries saved 3.75% on
4,926 real small fragments, spending 1.54 versus 0.71 s repack CPU.
These are component/fragment controls with separate input sets. They
enter future comparisons when that same input and codec mode is enabled.
[FB-12 results](../../foundational/FB-12/README.md) · [C13 results](../../competitive/c13/README.md)

GF1's restricted exact oracle supplies a known optimum and a pack
counterexample. GF2 starts with the wider gzip latency gap. GF3 targets
retained intermediate costs and physically local sparse access.

[Raw rows](results/native.jsonl) · [Corpus](corpus.json) · [Reproduction](REPRODUCE.md) · [Preregistration](PREREGISTRATION.md)
