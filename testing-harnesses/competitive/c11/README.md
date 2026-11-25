# ZeroS3 C11 · Foreground latency under storage work

**C11_SPECIALIZED.** `serve -background-yield adaptive` uses the existing
S3 request counter before maintenance scans, replacement-pack staging,
and live native gzip proof work. Checkpoints proceed at an idle boundary
or after a 4 ms timer. Busy proofs issue at most half the usual worker
count in new units, with a floor of one. As traffic drains, the decoder
can resume its full lookahead. Work already issued can finish. Publication,
verification and reclamation use the established journal and leases.

## Matched two-CPU results

Each cell is the median of two alternating trials of 512 signed GET,
range GET, HEAD, LIST and PUT requests. Mixed background work runs one
verified native import, one online GC/repack pass and one archive restore.

| Clients | Foreground p99 incumbent → adaptive | S3 ops/s incumbent → adaptive | Import incumbent → adaptive |
| --- | ---: | ---: | ---: |
| 1 | 76.1 → 40.6 ms | 178 → 189 | 2.33 → 2.70 s |
| 4 | 97.2 → 57.6 ms | 189 → 284 | 2.47 → 2.78 s |
| 8 | 98.1 → 73.2 ms | 215 → 341 | 2.53 → 2.69 s |
| 16 | 137.3 → 104.0 ms | 354 → 500 | 2.57 → 2.66 s |

At four clients both builds reclaimed 18.36 MB. Combined import traffic
was exactly 350,034 HTTP bytes across the two relay legs. Receiver CPU was
4.44 → 4.54 s and peak RSS 90.17 → 81.25 MB. Physical writes stayed near
5.87 MB. The receiver's tracked reader grace fell from 2,363 → 2,097 ms.
The two-CPU cgroup counters, elapsed time, memory and process I/O for every
trial are in the raw records.

## Ablations and limits

In the first campaign, four-client idle, incumbent, fixed-delay and
admission-time adaptive p99 were 22, 108, 56 and 59 ms. Import took 2.50 s
on the incumbent, 5.34 s with fixed waits and 3.39 s with admission-time
capping. The dynamic policy reclaimed concurrency between proof units
when S3 load drained. Isolated four-client import p99 was 23.7 → 20.1 ms
on the final policy, with import time 1.45 → 1.54 s. Eight-client
isolated import p99 was 46.9 → 43.7 ms.

Isolated GC and repack showed small, mixed foreground changes. Restore
competes as a regular S3 read and saw little change. The no-background
control at four clients sustained about 815 operations/s in both builds.
The policy remains opt-in. The measured resource sharing is for one
native import and one maintenance pass. Concurrent native imports have
no separate global cap.

Using one Go worker on the same host, mixed four-client p99 was
117.7 → 71.9 ms and import time 4.17 → 4.46 s. At eight clients those
figures were 117.8 → 92.1 ms and 4.14 → 4.43 s.

## Correctness and profiling

All mixed lanes reproduced the original archive digests and recovered
the same retained base. The contention pilot held its mark lock for
32 µs and the pack locator swap for 2 µs. Offline indexed optimization
and archive finalization returned exclusive writer lease errors.
Existing readers can finish on their old locator before unlinking.

Deep verification passed after each measured import and maintenance
combination. The focused race suite covered pressure transitions and
cancellation in gzip replay. Tests also covered maintenance cancellation,
queued deferred work and shutdown, a busy cross-process reader, bad
signed import payloads and three existing real import crash cuts. The
policy adds no durable boundaries.

The standalone scheduler and GC traces include runtime tracing overhead.
The focused lock and syscall profiles cover smaller controlled cases.
The trace recorded 736 pread64, 85 pwrite64 and 147 fsync calls across
those cases. The archive mix itself uses process I/O and cgroup pressure
counters. The one-worker control changes the Go resource budget on the
same machine.

[Reproduction](REPRODUCE.md) · [Raw records and diagnostics](results/)
