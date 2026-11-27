# ZeroS3 foundational campaign

FB-J2 and FB-04 through FB-16 have implementation results or measured
research dispositions on `research/next`. The reference on `main`
was kept at `2ac9caba60cc050a595cefef7b791aa718803c0e`.
The campaign covers local research and development qualification.

## Journal decision

J2 is available as an opt-in format. The integrated transaction writes
checked frames under a parent-bound seal and uses one sync. Exact replay,
feature refusal and deterministic failure gates passed under the tested
filesystem model. FB-10 recovered all 44 benign X14 samples with the
independently kept cursor evidence. A final group and its local evidence
can be rolled back together. A stronger guarantee in that model requires
an independently durable observation. Six final paired rounds measured median signed 4 KiB GET p99 of
9.09 ms for v1 lazy, 8.50 ms for J2, and 6.00 ms for compact J2.
Median writer throughput was 5,030, 5,016, and 5,609 operations/s,
respectively. Earlier compact-J2 trials saw a worse read tail.
Deployment latency and media qualification remain open. The default
remains v1 lazy.
[Journal evidence](FB-J2/README.md) · [Cursor and compact records](FB-10/README.md)

## Batch dispositions

| Batch | Result |
| --- | --- |
| FB-04 | Ordered publication across mutation families. A signed 1,000-key delete used one sync. Replay, fault cases and interval checks passed. [Evidence](FB-04/README.md) |
| FB-05 | Bounded maintenance and scoped multipart expiry shipped. Foreground latency, native placement and general lifecycle policy remain open. [Evidence](FB-05/README.md) |
| FB-06 | Optional exact locator generations qualified locally at 16.8 million records, including forced collisions and interrupted publication. Background refresh remains open. [Evidence](FB-06/README.md) |
| FB-07 | Physical scrub, targeted repair and verified raw carry shipped. Additional compressed forms and scheduling remain open. [Evidence](FB-07/README.md) |
| FB-08 | Signed native batching and streaming qualified on loopback, including a verified connection-cut prefix. Durable process-cut resume and WAN remain open. [Evidence](FB-08/README.md) |
| FB-09 | Binary manifests, canonical metadata and a verified current-only legacy copy shipped behind opt-in controls. Retained-history downgrade remains open. [Evidence](FB-09/README.md) |
| FB-10 | Compact journal roots, paged multipart vectors, a read-only cursor and proven multipart adoption shipped. Multi-epoch rotation and automatic history retirement remain open. [Evidence](FB-10/README.md) |
| FB-11 | Shared snapshot pages and changes-only bundles qualified with exact reconstruction and reclamation. Remote range proofs remained research only. [Evidence](FB-11/README.md) |
| FB-12 | Native record and delta experiments missed codec and density gates. Shared window configuration and exact aligned-piece pricing landed. [Evidence](FB-12/README.md) |
| FB-13 | Measured workload-dependent CDC economics and shipped explicit local rebasing. The X9 replay released 3.84 MB of dependency-only extents while preserving four historical versions and current. [Evidence](FB-13/README.md) |
| FB-14 | Chunk deltas gave two clear corpus wins. Read and physical placement results missed broader ingest admission. [Evidence](FB-14/README.md) |
| FB-15 | Exact candidate research passed on the mixed trace and 25 million generated roots. High-churn X9 still favors canonical full marking. A persistent liveness index lacked justification. [Evidence](FB-15/README.md) |
| FB-16 | Authenticated incremental reconciliation qualified at one million generated rows and on signed native peers. Mirror is explicit and guarded. Global lookup stays research only. [Evidence](FB-16/README.md) |

## Integrated verification

The final run rechecked critical recovery, compatibility, and publication
paths on the same implementation as the final report. Its selection
covers v1 and J2 replay, compact PUT and multipart process cuts,
namespace ordering and exact history, maintenance and locator damage,
signed streaming and fallback, old-reader format refusal, portable
snapshot and delta restore, authority pricing and rebase recovery, and
authenticated namespace comparison. Actual older binaries were used for
the format and wire boundaries. The integrated union passed 67
top-level cases. The separately enabled scale run checked 10,000
multipart parts through reopen. Each log names its cases and outcomes.
[Integrated union](FB-16/results/final-integrated.txt) · [Race run](FB-16/results/final-race.txt) · [Decoder fuzz run](FB-16/results/final-fuzz.txt) · [Part-page scale](FB-16/results/final-parts-scale.txt)

In the final signed 144-object comparison, seven changed objects
replicated exactly with equal upload payload and destination allocation.
Median total application bodies went from 345,964 bytes to 189,911.
Median process CPU went from 24.52 to 16.03 ms. Median 4 KiB GET p99
went from 4.23 to 3.06 ms. These are three paired two-core loopback
trials. The projection exchanged more requests. The one-million-row
gate measured application frames including authentication bytes.
[Measurements](FB-16/results/verified-projection.txt)

## Owner decisions and infrastructure

Owner decisions remain open for the default journal and recovery policy,
history retirement, default identity or checksum behavior, default ZF2,
background maintenance and compression, CDC format evolution, lifecycle
defaults, default if-unchanged replication and mirror behavior, and the
public million-object snapshot commitment. [Detailed ADRs](ADR_DELTA.md)

Real media failure and host power cuts, independent witness deployment,
WAN paths, production-scale physical stores, ARM throughput and operating
policy validation remain open. Local process interruption, synthetic
damage, charged storage experiments and generated metadata have explicit
coverage in the linked reports. [Ledger](LEDGER.md)
