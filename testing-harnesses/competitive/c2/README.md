# ZeroS3 C2 · Storage frontier and read performance

**C2_PERF_EFFECTIVE.**
Frozen B10: `dcefbcb5c2539c74a0d33bfc0d6e4a380f671743`. C1 corpus and immutable
keys are preserved. Tests ran on Debian 13, overlayfs, two quota CPUs and
8 GiB cgroup memory. Read timings are process-cold and repeated, with the
kernel page cache uncontrolled. [Checkpoint A](CHECKPOINT-A.md)

## Physical storage

These are allocated filesystem bytes. Full finalization runs GC, repack at
100% live threshold with recompression, compact at a 1 MiB target, and GC.

| C1 family | B10 CDC, GC | B10 archive, GC | B10 finalized CDC | B10 finalized delta | B10 finalized archive | C2 finalized archive |
|---|---:|---:|---:|---:|---:|---:|
| lodash | 1,036,288 | 4,694,016 | 1,032,192 | 1,032,192 | 507,904 | 507,904 |
| Parquet | 2,191,360 | 2,625,536 | 1,306,624 | 1,097,728 | 712,704 | 712,704 |
| SST | 11,337,728 | 4,546,560 | 10,391,552 | 3,878,912 | 3,878,912 | 3,878,912 |
| tensor | 2,457,600 | 2,207,744 | 421,888 | 421,888 | 344,064 | 344,064 |
| composite | 42,364,928 | 37,285,888 | 33,902,592 | 28,737,536 | 28,295,168 | 28,295,168 |

B10 priced the eventual recompressed closure. C1 stopped before the old pack
was reclaimed and the new loose components were combined on lodash.
Parquet retained packs the default repack threshold skipped. Full
finalization reduced composite archive allocation another 24.1% from
GC-only. Depth 4, four bases and a 2% threshold did not improve the three
tested regression families. B10's grouped record setting helped small lanes.

C2 adds one locked archive apply-and-finalize command and retains a valid
generic-only incumbent where legacy structural options also exist.
Archive rejects an increase in modeled closure when the previous selection
is valid. Current-storage pricing charges raw, separately allocated
components consistently. It rejected the lodash expansion and selected
generic deltas for Parquet, recovering 610,304 bytes without repack.
These checks bound modeled closure. Pack slack, directory overhead,
interrupted maintenance and peak staging still depend on the actual store.
Finalization is store-wide even when candidate analysis uses a narrower scope.

## Read and optimization costs

Legacy Parquet and SST now use native range decoders as delta bases for
output spans through 16 KiB. Larger requests materialize the base once,
and the planner counts its full component footprint. Deep page and COPY
replay can reuse up to 128 KiB within one request. No stored format changes.

Across three matched Parquet trials, median run-level p50 at 4 KiB fell
from 88.67 to 4.08 ms. Medians at 64 KiB were 88.58 and 87.65 ms.
At 1 MiB they were 88.58 and 90.72 ms. Whole reads were 91.84 and 98.14 ms.
Four-client 4 KiB throughput rose from 37.7 to 301.3 verified GETs/s.
The initial unbounded seek trial regressed 64 KiB and whole reads. It is
retained separately. No broad canonical-read speedup was established.

Composite apply measured 11.19 s wall, 11.74 CPU s and 212.4 MiB peak RSS
on finalized B10 versus 11.01 s, 11.55 CPU s and 212.2 MiB on C2.
Sampled peak store allocation was 60.9 and 61.3 MiB. Candidate analysis
still dominated. The 32-target cap reduced apply to 7.15 s, skipped 160
targets and retained 32,165,888 bytes. It bounds discovery targets.
A completed no-change pass makes an opt-in memo reusable. On the composite,
a memo hit took 0.052 s wall and 0.024 CPU s, versus B10's 10.05 and 10.75 s.
The memo is disposable, covers unchanged local stores and confers no integrity
validation. New revisions, changed policy and GC can force fresh analysis.
The memo falls back above 100,000 inventory entries or a 32 MiB journal.

## Validation and replay

RubyGems, TypeScript, Django, Coreutils and Go subsets, and held-out urllib3
matched finalized B10 storage and restored exactly. Their storage-only passes overlapped
the root suite, so their wall times are excluded from speed comparisons.

Focused race tests and two archive phase SIGKILL tests passed. The uninterrupted
root invocation timed out at 18 minutes. The remaining 1,151 tests ran separately
with 1,139 passes, 12 skips and zero failures. [Validation evidence](results/validation.json)
All 18,504 default S3 operations verified. Four-client median rates in the
PUT, random, sequential and range GET lanes fell 5–10%. Client read CPU
also rose. The source of the timing change remains unisolated.

C2 keeps advanced analysis explicit. Use a bounded or delta-only pass where
retention economics warrant it, then archive finalization for worthwhile
graphs. Full incremental indexing, broader replay calibration and automatic
rewarming need further evidence. C3 should measure dependency transfer and
reconstruction against generic CDC on the same versions and network.

`./replay.sh WORK C1_CORPUS core` builds both binaries and validates the input
manifest. Use `external` for release subsets, `requests` for serving checks
and `tests` for Go validation. Use the pinned C1 Python requirements and Go 1.27.0.
For exact SST input replay, install C1 codec requirements and run
`python sst_replay.py C1_CORPUS results/sst-replay.json`.
[Machine-readable summary](results/summary.json) · [Raw results](results/) ·
[Storage and read tradeoffs](results/storage-read-tradeoffs.png)
