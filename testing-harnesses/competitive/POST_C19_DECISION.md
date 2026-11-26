# ZeroS3 post-C19 architecture decision

The campaign demonstrated specialized gains in exact compressed histories,
native transfer with a prepared source, derived index footprint, and local
pack recovery. Ordinary S3 remains viable in the single-node fixture.
The first production release remains undecided.

## Measured frontier

| Regime | Favorable evidence | Costs that constrain adoption |
| --- | --- | --- |
| Ordinary signed S3 | Integrated 4 KiB p99 13.2 ms, versus 15.9 ms MinIO and 14.8 ms RustFS over two local trials. Default immutable allocation was 14.04 MB versus 22.54/22.63 MB. | C9 achieved 11.8 ms. RustFS had lower HEAD p95. Local retention, compression, fsync, and placement policies differ. Two-core tests cannot establish a deployment-wide lead. |
| Versioned compressed software | Integrated TypeScript used 4.91 MB versus CDC 8.45 MB. It cut C9 optimization wall from 17.35 to 8.06 s. Held-out urllib3 retained 475 kB versus CDC 684 kB, with median whole reads 95 → 54 ms against C9. | CDC TypeScript whole p50 was 8.8 ms versus 945 ms. C9 retained 8 KiB fewer on each archive family. A portable TypeScript bundle occupied 8.37 MB and took 6.62 s to export. |
| Data and checkpoints | Exact native consumers reopened SQLite, Parquet, SST and typed tensors. Ordinary graph optimization cut SST 10.40 → 3.89 MB and whole p50 29.0 → 9.3 ms. | Most data gains were already present at C9. Parquet whole p50 was 94 ms versus 2.7 ms CDC. Independent MobileNet weights and entropy selected no rewrites. |
| Sparse snapshots | Rebuild and shift retained 324 kB versus C9 385 kB, with clean ext4 recovery. The in-place edit landed at 446 kB versus C9 459 kB. | A live dependent kept the oldest base after pruning. Integrated rebuild stayed at 324 kB while C9 fell to 311 kB. Sparse matching prices holes and shift well, and needs a read and retention budget. Independent backup tools verified all 54 snapshots and carry different encryption and repository costs. |
| Transfer and local recovery | With a normal receiver base, TypeScript closure took 2.70 s and 382,491 two-leg bytes versus direct PUT 4.13 s and 4.18 MB under pacing. A single bad preset copy was repaired with the original pack SHA in 0.289 s versus 1.294 s for forced repack. | Source preparation cost 7.98 s and 13.56 CPU seconds. Loopback direct PUT took 33 ms versus 1.56 s for closure. The later peer-equivalence merge kept the same shaped advantage at 382,713 bytes and 2.83 s. The two preset copies share one file. Neither test qualifies independent-media durability or real WAN behavior. |

C12's native matcher and bounded cache cut apply latency while preserving
the exact priced delta bytes. Independent patch encoders sometimes made
smaller programs, and the residual prototypes paid more for range reads.
C13's native pack context paid for two copies and lowered allocation by
3.75% on a wide source-fragment panel. Held-out HTTPX refused the context.
Block coding remained expensive on native cold short reads. C15 reduced
derived index allocation with compatible lookup, at some sparse lookup
and concurrent status cost. C17 reduced exact canonical apply work and
receiver memory without changing the archive or transfer formats.

## Verification scope

The performance tables freeze 519f53b against C9 at 3cf73e3. The 11
portable histories restored through C9 and an old reader refused the
new pack context. The root acceptance catalogs show exactly the
119 passing tests and the unrun remainder. Nine root race tests and a
reader-only gzip race test passed. The historical B8 race run reached its
cap. At a808140, 31 focused tests and seven race tests checked later
restore-session and pack-locator changes.
See [acceptance evidence](c19/results/README.md).

## Product options

| Architecture | Evidence and remaining work |
| --- | --- |
| General-purpose single-node S3/CAS with opt-in advanced modes | Default signed I/O, mixed-load scheduling and recovery are credible for local trials. Qualify broader clients, namespaces, version semantics and data-loss boundaries. Admission limits and storage amplification also need long-running concurrent measurements. |
| Version-aware package and build store | Exact source archives show strong storage and preparation economics on both TypeScript and urllib3. Keep hot whole and short-range reads on affordable forms, price portable export and retain an explicit migration and rollback plan. |
| Checkpoint, dataset and snapshot engine | Exact structured restores and sparse volume deltas broaden the useful corpus. Select by workload and retained closure cost. Measure native checkpoint readers, pruning over long histories, and several independently produced model families. |
| Replication substrate or partner backend | Prepared receivers can benefit at constrained bandwidth and proof can be bounded locally. Validate billing and topology over actual permitted networks, peer authorization and negotiation across versions, and safe fallback for unrelated or unprepared data. |
| Local hot tier plus optimized cold tier | The sharp range/whole costs make placement an important decision. A hot tier can cover frequently read forms while a colder tier pays for verbatim reconstruction. Evidence establishes the trade-off, while hot-set migration, eviction behavior, availability and backing-store economics still need measurement. |

Single-node throughput shares CPU, storage queues and fsync with proof
and maintenance. Exact wire and portable proofs cannot replace replica
availability, independent-media recovery, or host power-loss tests.
Security and operations qualification still needs a multi-tenant policy,
production credentials and rotation, dependency updates, and fault
handling across independent systems. ARM evidence is an emulated
functional smoke. The candidate's explicit gates and old-reader refusal
keep current storage behavior usable while those areas are tested.

## Next research

Prioritize access-aware representation and base placement under an explicit
read SLO, a migration CPU budget, and a retained-byte budget. Compare a
bounded workload-driven planner to pinned CDC and archive policies
under real workload shifts. Charge hot copies, dependency retention,
portable exports and verification. Retain the independently verified
snapshot and archive cases and add genuinely independent training runs
and sparse image histories before choosing a release architecture.

[C19 scoreboards](c19/README.md) · [Exact replay](c19/REPRODUCE.md) · [Raw results](c19/results/)
