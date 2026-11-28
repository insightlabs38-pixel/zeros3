# DF2-SATURATION · Concurrent exact replay

This research milestone is separate from the queue's compression-efficiency
DF2. The engine retains the existing serialized format and all production
defaults. The scheduler is attached by the native signed research harness
and exercises the existing GF7 decoder under the normal GET front door.
[Preregistration](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md)

## What caused saturation

The signed DF1 mix independently reproduced 1.96 s whole-read p95 at
32 clients. When all requests were whole reads, stock saturated near
15 GET/s across 2, 8 and 32 clients, using 1.97–1.98 cores. CPU cost
remained 129–138 ms per GET. One Go scheduler core halved throughput.
The heap grew from 64 to 588 MiB as more replay allocations remained live.

In matched two- and 32-client profiles, record cost stayed about
192 fetches and 4.50 MB stored bytes per operation. Warm kernel storage
reads were zero. A cold first burst read 4.49 MB and reached the same
saturation band. In the 32-client trace, cumulative runnable delay
was 208.7 s over many goroutines, versus 5.85 s at two clients.
The 32-client mutex profile measured only 14.3 ms of aggregate delay.
These traces measure across goroutines. CPU demand and runnable queueing
explain the scaling cliff. Memory grows with overlapping replay. Observed
shared locks and disk waits accounted for much less on this device.
[Detailed isolation and raw profiles](ISOLATION.md)

## What changed the operating point

Two-slot broadcast admission reduced heap without improving CPU
throughput. Queued requests could repeatedly lose an open slot.
Its 32-client p95 reached 7.04–8.21 s. Replacing broadcast competition
with channel admission bounded RSS to 122 MiB and achieved 2.24 s
p95 in a separate diagnostic order, at about 15 GET/s.

Coalescing shares one verified replay among overlapping whole reads
of the same namespace revision. It uses the same two leases. At 32
clients, 320 curl requests needed ten productions in each of two
opposite orders. At 2 and 8 clients, they needed 160 and 40.
The result disappears from lookup upon completion, with the lease
held until delivery or cancellation.

| Curl whole clients | GET/s native → coalesced | p95 native → coalesced |
| ---: | --- | --- |
| 2 | 14.6–14.8 → 16.7–17.0 | 165–194 → 136–142 ms |
| 8 | 15.2–15.3 → 60.2–63.6 | 683–700 → 155–159 ms |
| 32 | 15.1–15.7 → 169.4–173.4 | 2.78–3.05 s → 284–302 ms |

Each curl arm retained the same 4,616,192 bytes. The 32-client
CPU per GET fell from 127–131 ms to 8.6–9.3 ms. RSS fell from
639–669 MiB to 81–82 MiB. Shared output leases are bounded at
16 MiB in total. Sampled memory also includes source buffers,
assembly scratch, the transport and the runtime.
[Architecture and measured bounds](ARCHITECTURES.md)

The improvement is specific to overlapping identical work. Three
real curl revisions benefited, but separate keys with zero joins
stayed near 15 GET/s. At two clients those keys also showed a smaller
latency penalty, about 174–175 ms versus 153–162 ms. At 32 clients
unordered admission amplified that penalty. This leaves admission
policy and mixed-popularity fairness open for deployment.
[Distinct-key, cold, range and PUT controls](CONTROLS.md)

## Frozen nginx confirmation

The two nginx pairs ran only after the development decisions were frozen
at `8a89688`. They completed 7,680 byte-exact signed whole GETs over
24 trials. Both pairs retained identical allocations within comparisons.

| nginx release pair | Clients | GET/s native → coalesced | p95 native → coalesced |
| --- | ---: | --- | --- |
| 1.27.2–1.27.3 | 2 | 42.7–46.9 → 49.0–51.0 | 56–67 → 44–45 ms |
| 1.27.2–1.27.3 | 8 | 43.1–46.0 → 174–188 | 277–284 → 51–54 ms |
| 1.27.2–1.27.3 | 32 | 43.4–48.5 → 494–497 | 979–1,066 → 99–139 ms |
| 1.27.3–1.27.4 | 2 | 40.8–47.0 → 50.2–51.8 | 56–66 → 42–51 ms |
| 1.27.3–1.27.4 | 8 | 47.1–47.9 → 186–197 | 257–265 → 48–53 ms |
| 1.27.3–1.27.4 | 32 | 48.0–48.7 → 552–565 | 990–1,036 → 82–83 ms |

This confirms repeatable gains on a separate source family in the
high-overlap regime. Curl's 32-client delivered rate was 63–65 MB/s
native and 700–716 MB/s merged. Nginx reached 628–722 MB/s merged,
against 55–62 MB/s native. These are application payload rates on
loopback with verification, independent of disk device reads.
The whole-read tails use 320 samples per trial. Descriptive block
intervals group completion order with blocks of at least eight and up
to the client count. They account for some burst correlation. They
are scoped to these trials with as few as ten producer waves.
[Holdout rows](results/nginx.jsonl) · [Tail intervals](results/tail-intervals.jsonl)

## Qualification and decision

The focused race group and separate signed eight-client race passed.
The tests exercise admission and cancellation, immutable revision
identity, exact native output, original signed range routing,
unsigned rejection, missing-frame failure and fresh signed recovery.
The older-reader, process-cut, snapshot, relocation, and grouped
journal controls also passed. Two focused decoder fuzz targets
completed at least 6,000 executions each.
[Verification receipts](results/verification.jsonl)

DF2-SATURATION is complete as specialized research. Keep the scoped
driver for overlapping whole-revision workloads. Distinct work still
faces the CPU limit and needs different scheduling. Channel admission
is a separate memory-control point. The measured distinct-key
penalty and unqualified mixed-popularity fairness rule out a
general default change. This campaign had a two-core host and local
loopback. It did not qualify wide hardware or slow-client fleets.
