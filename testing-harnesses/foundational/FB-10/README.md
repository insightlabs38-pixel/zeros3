# FB-10 · Journal and publication

**Partial.** `serve -binary-journal` activates compact root PUT
records in a J2 store. Exclusive writer ownership is required while
the feature is active. The reader can replay legacy JSON and compact
records in the same hash-chained log and still adopts an independently
bound namespace checkpoint. A pre-feature J2 binary fails at open.

| Independent payload | JSON bytes | Compact bytes | Reduction |
|---|---:|---:|---:|
| New PUT | 315 | 99 | 68.6% |
| Overwrite plus archived identity | 661 | 205 | 69.0% |

The encoder preserves SHA-256 and ETag bytes, canonical UUIDs,
metadata and archived timestamp. Other record families and unusual PUT
metadata still use JSON. The enclosing frame and J2 group digest,
write and sync are unchanged. Native payload microbenchmarks cut
encode and decode time for both fixtures, with some extra decoded
allocations. End-to-end throughput and foreground p99 remain open.

Real process kills at marker activation and before journal append
left the prior acknowledgement visible. The cut after sync recovered
the overwrite and archived the prior acknowledgement. A cut after
the group write also recovered the full overwrite on this host.
These process cuts exercise visibility recovery. Power interruption
and correlated media loss have separate proof limits.

`serve -multipart-pages` keeps large part reference vectors in
immutable, digest-addressed leaves and a checked index. The journal
and namespace checkpoint store just the index digest and existing
part metadata. Every page is synced before its first reference can
commit. A missing or damaged referenced page refuses replay.
Journal reconciliation for maintenance resolves the same page and
compact PUT roots so they remain in the exact live set.

| 10,000 parts with the same 129-reference vector | Inline | Paged |
|---|---:|---:|
| Journal bytes | 123,348,321 | 4,158,384 |
| Checkpoint bytes | 92,231,231 | 1,270,900 |
| Additional page file bytes | 0 | 4,692 |
| Reopen from journal | 627 ms | 21 ms |

This uses native commit and reload with reused payload references on
the same local filesystem. Distinct vectors have higher page costs.
A separate 16 MiB ordinary upload recovered all 265 references,
reconciled exactly through maintenance, and completed byte-for-byte.
Page files are retained as long as their journal references can replay.
This keeps old groups recoverable without enabling journal retirement.

`journal cursor` binds the store ID, J2 genesis, group number, byte
offset, sequence and chain digest. A retained previous cursor can be
checked against the real chain. It detected coherent prefix rollback,
and the reader rejected foreign, interior and wrong-digest cursors.
This is a single-log epoch and an optional read-only witness check.

The S1/S2 X14 matrix ran 44 byte cuts with JSON and compact records.
Every recovery retained the exact acknowledged journal prefix and
state. A coherent rollback or damaged acknowledged final group can
produce the same local bytes as a benign interrupted append. The
stronger X14 gate is **UNSATISFIABLE UNDER THIS MODEL** without
independent evidence. An external cursor shows rollback only if it
covered the lost group and was preserved separately.

| Signed mixed workload, medians of six rounds | v1 lazy | J2 JSON | J2 compact |
|---|---:|---:|---:|
| Ops/s | 5,690 | 5,633 | 5,862 |
| GET 4 KiB p99, ms | 7.02 | 8.23 | 8.51 |
| HEAD p99, ms | 6.60 | 8.66 | 7.34 |
| Journal bytes | 530,199 | 592,009 | 440,419 |

The 32-writer, 32 KiB mixed workload still missed the p99 gate.

`serve -multipart-adopt` records exact part and prefix SHA evidence.
The signed in-order completion of 8,444,327 bytes read zero payload
after a reopen and matched the 115 continuous CDC references of PUT.
The existing multipart ETag was preserved. Whole-object part copy
adopted a locally verified ordinary PUT with zero payload reads.
Ranges, missing provenance and replaced predecessors use the reader.
Midstates stay in memory. Already-established digests survive replay.

Shared publication orders both new and equivalent intents. For 24
concurrent equivalent intents, each journal mode wrote one manifest
and one journal record with exact final identity and zero history.
This measures the publication stage after payload preparation.
Default durability, cross-segment retirement and hardware crash
qualification remain open. The queue continues with FB-11 snapshots.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
