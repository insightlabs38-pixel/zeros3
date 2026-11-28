# Counterexamples and admission controls

A channel with two slots bounded live state without the broadcast
gate's slow-waiter tail. In one diagnostic order it measured
15.5, 15.3 and 15.0 GET/s at 2, 8 and 32 clients. Whole p95 was
164 ms, 572 ms and 2.24 s. At 32 clients it sampled 122 MiB RSS
and attributed a mean 1.87 s to admission wait. It changed where
requests waited and reduced resident memory. It did not remove CPU
work or raise the saturation throughput ceiling.

Whole GETs spanning three real curl targets still produced sharing.
Merging ran seven producers for 96 requests, improved whole p95 from
2.87 s to 533 ms and raised 14.2 to 113.4 GET/s at identical
15,179,776 B allocation. This covers several hot revisions.

A stricter control copied the same exact compressed object under
one independently authorized key per worker. Zero requests joined.
At 32 clients the rate remained roughly 15 GET/s, while whole p95
rose from 2.74 to 5.22 s with unordered slots. This separate control
retained identical packs and had one 4 KiB block of metadata
allocation difference between the native and merge lanes.
At two clients both orders had identical full allocation. Baseline
achieved 15.53–15.68 GET/s and 153–162 ms p95. The no-join merge
arm achieved 14.53–15.03 GET/s and 174–175 ms p95. The experimental
path should only serve work with expected overlap. Ordinary reads
continue to use the native path.

In separate 32-client diagnostic arms, cold first-burst storage
reads remained about 4.49 MB in both modes. The same-revision
coalescer used one producer for 32 requests. Mixed small and medium
ranges use ordinary range decoding. They benefited from fewer
competing whole-request producers in this run.
In the independently paced foreground PUT arm, baseline acknowledged
13 writes and merge acknowledged 24 before the GET phase drained.
All acknowledgments read back exactly. The differing completed-writer
counts are outcomes, not a fixed identical secondary write load.

These controls use single diagnostic orders except the low-concurrency
distinct-key check. Individual p95/p99 values describe these samples.
[Raw controls](results/controls.jsonl)
