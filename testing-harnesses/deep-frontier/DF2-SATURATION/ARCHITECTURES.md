# Whole-read scheduling experiments

The driver attaches only through the optional hook after the signed GET
handler selects its immutable manifest and parses its range/conditions.
That hook is nil in the ordinary server. The driver is compiled for
research tests. It recognizes native GF7 whole representations with no
body-reference resolver. Ranges and other formats retain their native path.

Both alternatives call the normal bounded native reconstruction.
Source members, frames and final object SHA are verified before a shared
result is visible. The producer owns its source and observes cancellation
between frames. A disconnected waiter releases its lease. The last
waiter cancels a producer. A two-slot bound remains in force through
response delivery, including slow readers. Each output is at most
8 MiB. The 16 MiB maximum shared output budget excludes replay scratch,
source records, the HTTP transport and the Go runtime. Admission and
per-group waiting are bounded at 128 and 64. There is no changed disk
format or persistent allocation.

The broadcast gate bounds independent producers and copies through the
same verifier and output buffer path as the sharing alternative. It is a
negative latency control at 8 and 32 clients. New requests can win an
open slot before a woken older waiter. A second gate uses a blocking
channel rather than repeated broadcast competition.

The coalescer keys on bucket, object key, manifest UUID and representation
digest. A fresh matching request may join a producer still building the
answer. Full verification publishes one immutable byte slice to those
waiters. Completion removes the group from lookup. It remains charged
until every reader closes. Future requests rebuild from storage. New
manifest identities, even with identical bytes, never join an old group.

This is request collapsing, a known singleflight technique. The result is
its scoped operating region with exact native replay, byte and resource
proof, and an explicit contrast to admission without work sharing.

| Clients | Native GET/s | Gate GET/s | Merged GET/s | Native p95 | Gate p95 | Merged p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 2 | 14.6–14.8 | 15.1–15.6 | 16.7–17.0 | 165–194 ms | 149–168 ms | 136–142 ms |
| 8 | 15.2–15.3 | 15.1 | 60.2–63.6 | 683–700 ms | 1.85–1.99 s | 155–159 ms |
| 32 | 15.1–15.7 | 15.1 | 169.4–173.4 | 2.78–3.05 s | 7.04–8.21 s | 284–302 ms |

Each arm completed 320 signed whole GETs twice in opposite arm orders.
At 2/8/32 clients, merging ran exactly 160/40/10 reconstructions per
320 requests in both orders. At 32, CPU per GET declined from
126.9–131.0 to 8.6–9.3 ms. Sampled live heap fell from 595–602
to 47–49 MiB and RSS from 639–669 to 81–82 MiB. The broadcast gate
had a 83–90 MiB sampled heap but used 129–130 ms CPU per GET.
[Complete native rows](results/architectures.jsonl)
