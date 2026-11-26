# FB-10 · Journal and publication

**In progress.** `serve -binary-journal` activates compact root PUT
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

[Reproduction](REPRODUCE.md) · [Evidence](results/)
