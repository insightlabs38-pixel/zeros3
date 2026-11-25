# ZeroS3 post-C2 frontier campaign

The campaign started from C2 `4bc66f3` on `research/next`.
Stable `main` remained at `2ac9caba60cc050a595cefef7b791aa718803c0e`.
The [C9 reproducibility manifest](c9/RESULTS_MANIFEST.json) pins the final
engine, independent binaries, fixture hashes, environment and raw outputs.

## Results

| Batch | Verdict | Measured outcome |
| --- | --- | --- |
| [C3](c3/README.md) | Transfer specialized | Scoped TypeScript closure sent 350,034 HTTP bytes versus 8,381,025 for peer CDC. |
| [C4](c4/README.md) | Operational effective | 49,152 default S3 operations yielded no consistent regression. Core operations passed on Linux amd64 and ARM64 emulation. |
| [C5](c5/README.md) | Specialized value | TypeScript and SST archive retention fell 58.3% and 62.7% versus finalized CDC. MobileNetV2 and entropy were negative controls. |
| [C6](c6/README.md) | Specialized | Verified unchanged repack avoided about 41 MB of staging across two opaque stores at identical pack IDs and allocation. |
| [C7](c7/README.md) | Index effective | One new revision took 0.314 CPU seconds with indexed bounded discovery versus 1.488 for full optimization on the dense control. |
| [C8](c8/README.md) | Stream effective | Native TypeScript receiver CPU fell from 4.98 to 1.84 seconds and the client avoided file staging. |
| [C9](c9/README.md) | Specialized value | Package metadata, database contents and exact bytes were verified after restart and independent restores. |

C3 retained a deliberately scoped hint design. Both source and receiver C2
compatibility lanes restored exact data via CDC. Held authority and slow
links remain the valuable preconditions. The local OpenXet exact and shifted
controls confirm the value of client-side chunk negotiation. Its historical
tensor-edit reconstruction defect remains excluded from timed comparisons.

C4 verified old-reader refusal and revert, core client behavior, restart,
portable bundles and peer repair. The Linux ARM64 test executed under QEMU
user emulation. The full historical root suite remained outside this campaign.
Targeted deterministic partitions, race tests and actual SIGKILL cuts provide
the recorded verification scope. Native ARM64 and physical power-loss
qualification remain open.

C5 showed that archive allocation, read latency and maintenance CPU have
different optima. TypeScript archive full reads took roughly 1.14 s
while CDC served the matched full pass in about 20 ms. For Parquet,
the mixed policy admitted less compression and substantially cheaper reads.
Generic transparent CDC performed well on some ordinary data without
structural reconstruction. The break-even models depend on actual
retention, read frequency, copy count and operating costs.

C6 retained the existing pack format. Dictionary/reconstruction layouts
raised mixed history cost in this campaign, and the four-cycle churn
experiment showed no broad placement win. C7 reused the existing durable
index, verified its historical prefix, and scoped hints to current same-bucket
nodes. Retaining older generations and opening the index cost real bytes
and I/O. The one-million-entry test used journal metadata without bodies.

C8 combined the target and CDC proof and buffered small signed frames.
The compressed planner scratch prototype saved allocation with a large
random-read penalty. Planning retains its seekable spool. Foreground p99
rose in a short simultaneous import sample, and online maintenance also
had measurable tail cost. Those results keep the deployment advantage
specific to planned archival and replica workloads.

C9 repeated standard S3, Restic and local OpenXet in separate lanes.
The artifact mirror used the scoped closure for an independently released
package pair. The database workflow opened restored Parquet and SST data
with the producer-format readers. The independent S3 request sample
includes the rerun of one RustFS trial invalidated by a live sampling race.

## Architectural and commercial decision

The strongest measured fit is a single-owner, S3-compatible artifact and
snapshot store with a cold archive policy and optional scoped native
replication. Transparent server-side CDC saves local storage. Scoped
transfer can save network bytes for prepared replicas. Backup engines add
client-side snapshots and encryption, and Xet has effective generic
chunking and reconstruction over its own interface. The comparisons
retain their different durability, retention, encryption and cache profiles.

The engine remains Apache 2.0, stdlib-only at the Go source level and
single-node. One static SigV4 pair covers one served store. Distributed
availability, IAM/STS parity and broad hardware-fault qualification are
outside the validated deployment profile. The production reference remains
stable and the research changes stay on `research/next`.

Prioritize exact full-read replay for compressed artifacts and bounded
online maintenance under higher foreground load. They address the largest
measured penalties in successful workflows. Continue only as a separate
qualified research effort after this completed queue.
