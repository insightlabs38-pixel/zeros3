# DF1 saturation mechanism

The signed 240-request DF1 mix reproduced whole p95 of 144 ms at
two clients, 455–580 ms at eight and 1.96 s at 32. The older rates
and tail instability are preserved in DF1 and the independent rerun.
The separate whole-only isolation uses 320 completed requests per
client-count lane, streaming SHA verification on the client, an idle
connection pool matched to the workers and the same 4,616,192 B store.

| Clients | GET/s | CPU ms per GET | CPU cores used | Whole p50 / p95 / p99 | Peak sampled heap / RSS |
| ---: | ---: | ---: | ---: | --- | --- |
| 2 | 14.28 | 138.4 | 1.98 | 135 / 180 / 211 ms | 64 / 98 MiB |
| 8 | 15.35 | 128.6 | 1.97 | 511 / 676 / 745 ms | 161 / 205 MiB |
| 32 | 15.04 | 131.8 | 1.98 | 2,001 / 2,968 / 3,477 ms | 588 / 639 MiB |

Reducing GOMAXPROCS to one in a separate 96-read run roughly halved
throughput to 7.6 GET/s while per-operation CPU remained 131 ms.
At 32 clients, the closed-loop approximation of 32 / 15.04 is 2.13 s
per request. The observed median is 2.00 s. This supports competition
for core time as the main cause. GF7 replay work overlaps effectively
across requests until both cores are occupied. More CPU would matter
more than adding workers to this particular workload.

The 2- and 32-client scoped profiles each recorded about 192 record
fetches and 4.50 MB of stored record bytes per GET. The measured
kernel read bytes were zero once warm. Targeted eviction read about
4.49 MB from storage in its first 96-request window and produced
15.6 GET/s. This is a first-burst eviction experiment, and subsequent
requests share the OS cache. The 32-client CPU profile accumulated
13.19 s in 6.71 s of wall time, led by DEFLATE output and verification.
The mutex profile attributed 14.3 ms of aggregate delay, and syscall
trace attributed 74.6 ms of aggregate pread wait. The block profile
is dominated by transport channels and waiting for native producers.
Scheduler delay accumulates across many runnable goroutines, often
where execution resumes from asynchronous preemption. These are
aggregate delays across goroutines. They cannot be added to one
request's elapsed time.

The active per-request input/output state explains the higher heap and
RSS under concurrency. GC cycles fell with the larger live set.
Equal source reads, full utilization, core-count sensitivity and
small observed lock and pread waits jointly weigh against lock or
storage serialization on this infrastructure. The measurements still
allow scheduling and memory effects to change tails. That motivates
bounded admission and eliminating duplicate work among requests
which already selected the same authenticated immutable revision.

[DF1 exact mix rerun](results/df1-replay.jsonl) ·
[GET-only and causal controls](results/isolation.jsonl) ·
[Profile tops](results/isolation-profiles.txt)
