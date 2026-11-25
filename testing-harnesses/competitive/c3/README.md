# ZeroS3 C3 · Scoped representation transfer

**C3_TRANSFER_SPECIALIZED.** An opt-in named destination base makes
a previously materialized canonical component reusable across archive keys.
The useful operating point combines a slow link, a shared decoded stream,
and a receiver that already has verified authority for a related archive.

## What changed

`replicate -authority-closure -authority-base s3://BUCKET/PRIOR` names one
object in the destination bucket. The signed descriptor can advertise eight
reachable base digests in addition to the existing bounded component hints.
The existing version 1 closure frame and namespace commit verify and publish
the target. Ordinary S3 and local CDC sync keep their existing behavior.
The [protocol specification](../../../docs/ZEROS3_PROTOCOL.md) describes
the optional fields and compatibility behavior.

## Matched observations

| Workload and receiver state | Candidate vs baseline | HTTP bytes, both legs |
| --- | --- | ---: |
| TypeScript 5.6.2 → 5.6.3, held archive at a different key | Scoped closure vs peer CDC | 350,034 vs 8,381,025 |
| TypeScript, same held archive | C2 closure vs peer CDC | 8,382,035 vs 8,381,025 |
| Shifted binary with ordinary prior bytes at target key | Native sync vs S3 PUT | 202,408 vs 4,203,476 |
| Same shifted binary via relay | C2 closure vs peer CDC | 418,499 vs 401,038 |
| Exact repeat at target key | Native sync vs S3 PUT | 12,330 vs 4,195,284 |
| Independent entropy with prior at target key | Native sync vs S3 PUT | 4,209,611 vs 4,195,284 |
| SST with ordinary prior bytes at target key | C2 closure vs peer CDC | 119,384 vs 7,505,618 |
| Coreutils bundled source at target key | C2 closure vs peer CDC | 24,539 vs 1,115,960 |

The TypeScript scoped path saved 95.8% of aggregate HTTP bytes and reduced
receiver-leg bytes from 4,192,312 to 180,663. Three locally paced trials per
lane at 768,000 B/s per direction per leg took median 4.63 s vs 11.04 s.
On unrestricted loopback, the early matched pair took median 6.53 s vs
0.15 s. Closure replay consumed about 5.39 server CPU seconds vs 0.06.
A rough break-even based on that pair is 1.26 MB/s over the serialized
relay legs. Results describe controlled loopback, including framing and
excluding TCP/IP headers. Source CPU and I/O come from the server process.
The ordinary PUT client runs in the same Python process as the taps.

A C2 peer on either side fell back to bulk CDC and restored the same SHA-256.
A plain previously uploaded TypeScript revision lacked decoded authority.
A receiver provisioned with the prior's verified closure could reuse it.
The cold source optimization took 81 CPU seconds on the Django pair
and its ordinary-prior transfer did not beat CDC. Preparation and replay
therefore remain material costs. The gzip level-1 model on actual missing
bulk frames shrank tensor and source payloads, added bytes for TypeScript,
and shrank Django by only 0.25%. It measures compression sizing and CPU.

C1 OpenXet rows with identical restored SHA-256 add context for the exact,
shifted, and entropy controls. Its exact repeat used 7,032 loopback TX bytes
and its insertion edit used 97,811. Those counters include the loopback
interface's transport overhead and the local Xet workflow. The tensor-edit
reconstruction error excludes that datapoint. [C1 traces](../c1/results/xet-local-repeat.jsonl)

## Verification and replay

The matrix covers 13 families and five receiver-knowledge states with four
transports. Single-trial lanes establish byte counts and exactness. The
TypeScript pacing result has three alternating replicates. Every measured
destination was read back and hashed. The scoped retention lane also applied
full finalization, deep verification and exact post-reopen reads. After the
same four maintenance stages, the two TypeScript objects occupied 4,915,200
allocated bytes using the scoped closure and 9,039,872 bytes using CDC.

Go coverage exercised native sync, bulk replication, old peers, signed
requests, absent scope, wrong digest, interruption and targeted retry.
Three actual receiver SIGKILL cuts after component flush, after verified
manifest publication and after journal sync converged after reopen and retry.
The focused suites ran without a full historical root replay. Bulk telemetry
assertions wait for server handlers to publish their counters before checking
exact byte totals. The first broader transfer run exposed this timing issue.

Freeze/rebuild inputs using the [C1 instructions](../c1/REPRODUCE.md) and
[C2 release inputs](../c2/inputs.py). Run `transfer.py --help` for the
corpus, download, binary, results, pacing and receiver-knowledge options.
[Input hashes](inputs.jsonl) and [raw traces](results/) are checked in.
Run `python summarize.py results/*.jsonl` to regenerate the transfer summaries.

C4 takes the same exactness contract into operational checks and default-S3
performance triage. The research flag remains opt-in.
