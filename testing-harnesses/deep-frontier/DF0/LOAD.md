# DF0 · Signed load and replay cost

The curl 8.10.0–8.10.1 validation pair is the fixed profile target.
Four native lanes were prepared in full. CDC, GF2 and GF7 received the
same 1, 2, 8 and 32-client request lists per round. The archive had the
same 2-client lists and cold probes. Each warm load point used 240
requests per order, including 80 4 KiB, 60 64 KiB, 60 whole, 20 HEAD
and 20 PUT requests. Client and server ran under a two-CPU quota on
AMD EPYC, Linux 6.18.57, 4 KiB overlay and Go 1.27.0 linux/amd64.

| GF7 clients | 4 KiB p95 | 64 KiB p95 | Whole p95 |
| ---: | ---: | ---: | ---: |
| 1 | 4.43–4.68 ms | 6.97–7.80 ms | 196–220 ms |
| 2 | 5.20–5.93 ms | 8.35–9.95 ms | 203–220 ms |
| 8 | 155–156 ms | 114–145 ms | 717–737 ms |
| 32 | 496–598 ms | 461–582 ms | 2,540–2,815 ms |

The two reversed orders show a compute saturation regime. The tails
describe this worker mix on the tested quota. At 8 clients the
maintenance pass physically rewrote 8,368 B from a 402,160 B partially
live pack. It overlapped 23 and 24 GF7 requests and 14 and 22 CDC
requests. Whole p95 shifted to 737–788 ms for GF7 and 17–36 ms for
CDC in that arm. Range tails shifted in both directions. Maintenance
effects and queueing variation are observable at this scale.

Each cold and warm control started from a new clone of one finalized seed.
Cold advice reduced data-file residency from 35,580 to zero pages across
24 clones, with no advice errors, immediately before reopen. The matched
warm control did an exact whole priming GET. A fresh signed HTTP server
served the measured request in both cases.

| GF7 first request | Warm | Cold |
| --- | ---: | ---: |
| 4 KiB | 2.91–3.26 ms | 9.46–10.32 ms |
| 64 KiB | 7.41–7.93 ms | 13.32–13.40 ms |
| Whole | 166–171 ms | 175–177 ms |

These are first-request values from two orders per state and size.
Namespace and pack-index reads on reopen can warm some data pages ahead
of the request. Individual first-read groups are too small for tail claims.

## Measured dominant work

Live signed CPU samples put 81.6% of sampled sparse CPU and 81.8% of
sampled whole CPU under `translateReplayFrame`. In the whole profile,
`translateNext` and `repDFStreamWriter.Token` each account for 23.2%
cumulative CPU in distinct call branches. The hottest single leaf is
the output bit writer at 14.9% flat CPU. Huffman decoder lookup and
input bit-buffer refill together account for another 15.0% flat.
Instruction parsing via `repWireReader.uv` consumes 25.1% cumulative
whole CPU. Its canonical-width check calls `binary.AppendUvarint(nil, v)`.
An independent Lua heap profile attributes 98.0% of estimated allocation
objects to that exact line. Request-window memory counters independently
recorded about 106 million allocations and 4.3 GB of total allocation
across 1,536 mixed GF7 requests. The heap profiler is sampled and its
allocation estimates are separate from runtime memory counters.
SHA-256 accounts for 5.1% flat on whole requests and 12.8% on sparse
requests. Pack decode and reads are also visible on sparse requests.

Twelve isolated whole replays of the same store consumed 27.85 million
source tokens in 58,704 runs and 1,560 frame visits. They emitted
396.47 million frame bits. Verified member fetch took 132 ms of
1,965 ms replay wall time. The sampled interpretation is that token
decode and emission dominate warm whole-read CPU. The signed mixed
trace attributed 77.5 ms aggregate blocked time to syscalls, including
23.6 ms of pread. Most synchronization delay was transport channels
and test wait groups. The trace delay totals span goroutines and cannot
be interpreted as client wall time or a per-request lock budget.

The signed record observer measured an average 449 KB logical records
for each 4 KiB request, 756 KB for 64 KiB and 6.76 MB for a 4.13 MB
whole request. The packed logical and stored-record counts differ.
Sparse work amplification comes from whole-block reconstruction and
source records. The 57,357-byte index took 5.93 CPU-ms per independent
parse. Replaying verified frames is still the dominant warmed cost.

The first DF1 hypothesis should test an allocation-free canonical
varint-width check in the replay loop. It should still refuse overflow
and overlong integers. Compare identical stored bytes, frame hashes,
and malformed-wire rejection. After isolating the measured allocation
cost, use the unchanged codec and a run-oriented codeword accumulator
as emission controls. Carry the same warm, cold and 8/32-client
sweeps into that comparison. DF0 measured the incumbent only.

[Load and read-cost rows](results/load.jsonl) ·
[CPU and wait receipt](results/profile-receipt.txt)
